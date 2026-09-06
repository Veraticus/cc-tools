package notify

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"sync"
	"syscall"
	"unicode/utf8"
)

const (
	labelRecordVersion        = 1
	labelStateDirectoryName   = "session-labels"
	labelSnapshotSuffix       = ".json"
	maximumLabelSnapshotBytes = 4 * 1024
	labelRefreshInterval      = uint64(4)
	labelTempRandomBytes      = 8

	labelFieldVersion             = "version"
	labelFieldHarness             = "harness"
	labelFieldSession             = "session"
	labelFieldLabel               = "label"
	labelFieldSourceGeneration    = "source_generation"
	labelFieldLatestCompletion    = "latest_completion_id"
	labelFieldExchangeCount       = "exchange_count"
	labelFieldSuccessfulRefresh   = "last_successful_refresh_exchange"
	labelFieldMaterialFingerprint = "last_attempted_material_fingerprint"

	labelDirectoryMode os.FileMode = 0o700
	labelFileMode      os.FileMode = 0o600
)

// LabelStore owns the daemon's minimal persistent naming metadata. It is
// intentionally separate from completion claims: labels survive restarts,
// while delivery ownership remains in memory and best-effort.
type LabelStore struct {
	stateBase string
	mutex     sync.Mutex
}

type labelRecord struct {
	Version                          int    `json:"version"`
	Harness                          string `json:"harness"`
	Session                          string `json:"session"`
	Label                            string `json:"label"`
	SourceGeneration                 uint64 `json:"source_generation"`
	LatestCompletionID               string `json:"latest_completion_id"`
	ExchangeCount                    uint64 `json:"exchange_count"`
	LastSuccessfulRefreshExchange    uint64 `json:"last_successful_refresh_exchange"`
	LastAttemptedMaterialFingerprint string `json:"last_attempted_material_fingerprint"`
}

type labelCompositionPlan struct {
	harness          string
	session          string
	completionID     string
	current          string
	refresh          bool
	sourceGeneration uint64
	exchangeCount    uint64
}

// NewLabelStore constructs the daemon-only session label store rooted beneath
// stateBase. Construction is side-effect free; the first eligible completion
// creates the private directory, while lookups of absent state stay read-only.
func NewLabelStore(stateBase string) *LabelStore {
	return &LabelStore{stateBase: stateBase}
}

func (store *LabelStore) planCompletion(event PreparedEvent) (labelCompositionPlan, error) {
	if store == nil || validatePreparedEvent(event) != nil || event.Kind != eventKindCompletion ||
		event.SessionID == "" || !validCompletionID(event.CompletionID) {
		return labelCompositionPlan{}, labelUnavailableError()
	}
	material := labelPairFingerprint(event.User, event.Assistant)

	store.mutex.Lock()
	defer store.mutex.Unlock()
	root, _, err := store.openRoot(true)
	if err != nil {
		return labelCompositionPlan{}, err
	}
	defer func() { _ = root.Close() }()

	record, present, err := readLabelRecord(root, event.Harness, event.SessionID)
	if err != nil {
		return labelCompositionPlan{}, err
	}
	if !present {
		record = labelRecord{
			Version: labelRecordVersion,
			Harness: event.Harness,
			Session: event.SessionID,
		}
	}
	plan := labelCompositionPlan{
		harness: event.Harness, session: event.SessionID,
		completionID: event.CompletionID, current: record.Label,
		sourceGeneration: record.SourceGeneration,
		exchangeCount:    record.ExchangeCount,
	}
	if present && record.LatestCompletionID == event.CompletionID {
		return plan, nil
	}
	if record.SourceGeneration == math.MaxUint64 || record.ExchangeCount == math.MaxUint64 {
		return labelCompositionPlan{}, labelUnavailableError()
	}

	record.SourceGeneration++
	record.ExchangeCount++
	record.LatestCompletionID = event.CompletionID
	plan.sourceGeneration = record.SourceGeneration
	plan.exchangeCount = record.ExchangeCount
	plan.refresh = labelRefreshDue(record, material)
	if plan.refresh {
		record.LastAttemptedMaterialFingerprint = material
	}
	if writeErr := writeLabelRecord(root, record); writeErr != nil {
		return labelCompositionPlan{}, writeErr
	}
	return plan, nil
}

func labelRefreshDue(record labelRecord, material string) bool {
	if material == record.LastAttemptedMaterialFingerprint {
		return false
	}
	if record.Label == "" {
		return record.ExchangeCount == 1 || record.LastAttemptedMaterialFingerprint != ""
	}
	return record.ExchangeCount >= record.LastSuccessfulRefreshExchange &&
		record.ExchangeCount-record.LastSuccessfulRefreshExchange >= labelRefreshInterval
}

func (store *LabelStore) finishCompletion(plan labelCompositionPlan, label string) error {
	if store == nil || !plan.refresh {
		return nil
	}
	if !validPiGeneratedLabel(label) {
		return labelUnavailableError()
	}

	store.mutex.Lock()
	defer store.mutex.Unlock()
	root, missing, err := store.openRoot(false)
	if err != nil {
		return err
	}
	if missing {
		return labelUnavailableError()
	}
	defer func() { _ = root.Close() }()
	record, present, err := readLabelRecord(root, plan.harness, plan.session)
	if err != nil || !present {
		return labelUnavailableError()
	}
	if record.SourceGeneration != plan.sourceGeneration ||
		record.LatestCompletionID != plan.completionID {
		return nil
	}
	record.Label = label
	record.LastSuccessfulRefreshExchange = plan.exchangeCount
	return writeLabelRecord(root, record)
}

func (store *LabelStore) lookupLabel(harness, session string) (string, error) {
	if store == nil || !knownHarness(harness) ||
		!validPreparedMetadata(session, maximumPreparedIDBytes, false) {
		return "", labelUnavailableError()
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	root, missing, err := store.openRoot(false)
	if err != nil {
		return "", err
	}
	if missing {
		return "", nil
	}
	defer func() { _ = root.Close() }()
	record, present, err := readLabelRecord(root, harness, session)
	if err != nil || !present {
		return "", err
	}
	return record.Label, nil
}

func (store *LabelStore) openRoot(create bool) (*os.Root, bool, error) {
	base, missing, err := store.openStateBase(create)
	if err != nil || missing {
		return nil, missing, err
	}
	defer func() { _ = base.Close() }()
	return openLabelDirectory(base, create)
}

func (store *LabelStore) openStateBase(create bool) (*os.Root, bool, error) {
	if store.stateBase == "" {
		return nil, false, labelUnavailableError()
	}
	if create {
		if err := os.MkdirAll(store.stateBase, labelDirectoryMode); err != nil {
			return nil, false, labelUnavailableError()
		}
	}
	info, err := os.Lstat(store.stateBase)
	if err != nil {
		if !create && errors.Is(err, os.ErrNotExist) {
			return nil, true, nil
		}
		return nil, false, labelUnavailableError()
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, labelUnavailableError()
	}
	base, err := os.OpenRoot(store.stateBase)
	if err != nil {
		return nil, false, labelUnavailableError()
	}
	return base, false, nil
}

func openLabelDirectory(base *os.Root, create bool) (*os.Root, bool, error) {
	info, err := base.Lstat(labelStateDirectoryName)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, false, labelUnavailableError()
		}
		if !create {
			return nil, true, nil
		}
		if err = base.Mkdir(labelStateDirectoryName, labelDirectoryMode); err != nil {
			return nil, false, labelUnavailableError()
		}
		info, err = base.Lstat(labelStateDirectoryName)
	}
	if !validLabelDirectoryInfo(info, err) {
		return nil, false, labelUnavailableError()
	}
	root, err := base.OpenRoot(labelStateDirectoryName)
	if err != nil {
		return nil, false, labelUnavailableError()
	}
	rootInfo, err := root.Stat(".")
	if !validLabelDirectoryInfo(rootInfo, err) {
		_ = root.Close()
		return nil, false, labelUnavailableError()
	}
	return root, false, nil
}

func validLabelDirectoryInfo(info os.FileInfo, err error) bool {
	return err == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir() &&
		ownedPrivateFile(info, labelDirectoryMode)
}

func ownedPrivateFile(info os.FileInfo, mode os.FileMode) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid() && info.Mode().Perm() == mode
}

func readLabelRecord(root *os.Root, harness, session string) (labelRecord, bool, error) {
	name := labelSnapshotName(harness, session)
	info, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return labelRecord{}, false, nil
		}
		return labelRecord{}, false, labelUnavailableError()
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		!ownedPrivateFile(info, labelFileMode) || info.Size() > maximumLabelSnapshotBytes {
		return labelRecord{}, false, labelUnavailableError()
	}
	file, err := root.Open(name)
	if err != nil {
		return labelRecord{}, false, labelUnavailableError()
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() ||
		!ownedPrivateFile(openedInfo, labelFileMode) || openedInfo.Size() > maximumLabelSnapshotBytes {
		return labelRecord{}, false, labelUnavailableError()
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumLabelSnapshotBytes+1))
	if err != nil || len(data) > maximumLabelSnapshotBytes {
		return labelRecord{}, false, labelUnavailableError()
	}
	record, valid := decodeLabelRecord(data, harness, session)
	if !valid {
		return labelRecord{}, false, labelUnavailableError()
	}
	return record, true, nil
}

func writeLabelRecord(root *os.Root, record labelRecord) error {
	if !validLabelRecord(record, record.Harness, record.Session) {
		return labelUnavailableError()
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > maximumLabelSnapshotBytes {
		return labelUnavailableError()
	}
	random := make([]byte, labelTempRandomBytes)
	if _, err = rand.Read(random); err != nil {
		return labelUnavailableError()
	}
	tempName := "." + hex.EncodeToString(random) + ".tmp"
	if err = writeUnpublishedLabelSnapshot(root, tempName, data); err != nil {
		_ = root.Remove(tempName)
		return err
	}
	defer func() { _ = root.Remove(tempName) }()
	if err = root.Rename(tempName, labelSnapshotName(record.Harness, record.Session)); err != nil {
		return labelUnavailableError()
	}
	return syncLabelDirectory(root)
}

func writeUnpublishedLabelSnapshot(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, labelFileMode)
	if err != nil {
		return labelUnavailableError()
	}
	written, writeErr := file.Write(data)
	if writeErr != nil || written != len(data) {
		_ = file.Close()
		return labelUnavailableError()
	}
	if syncErr := file.Sync(); syncErr != nil {
		_ = file.Close()
		return labelUnavailableError()
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || !ownedPrivateFile(info, labelFileMode) {
		_ = file.Close()
		return labelUnavailableError()
	}
	if closeErr := file.Close(); closeErr != nil {
		return labelUnavailableError()
	}
	return nil
}

func syncLabelDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return labelUnavailableError()
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return labelUnavailableError()
	}
	return nil
}

func decodeLabelRecord(data []byte, harness, session string) (labelRecord, bool) {
	if len(data) == 0 || len(data) > maximumLabelSnapshotBytes || !utf8.Valid(data) {
		return labelRecord{}, false
	}
	fields, valid := strictPiObject(data)
	if !valid || !exactLabelFields(fields) {
		return labelRecord{}, false
	}
	var record labelRecord
	if !decodeLabelRecordFields(fields, &record) || !validLabelRecord(record, harness, session) {
		return labelRecord{}, false
	}
	return record, true
}

func decodeLabelRecordFields(fields map[string]json.RawMessage, record *labelRecord) bool {
	valid := []bool{
		decodeLabelInteger(fields[labelFieldVersion], &record.Version),
		decodeLabelString(fields[labelFieldHarness], &record.Harness),
		decodeLabelString(fields[labelFieldSession], &record.Session),
		decodeLabelString(fields[labelFieldLabel], &record.Label),
		decodeLabelUint(fields[labelFieldSourceGeneration], &record.SourceGeneration),
		decodeLabelString(fields[labelFieldLatestCompletion], &record.LatestCompletionID),
		decodeLabelUint(fields[labelFieldExchangeCount], &record.ExchangeCount),
		decodeLabelUint(fields[labelFieldSuccessfulRefresh], &record.LastSuccessfulRefreshExchange),
		decodeLabelString(fields[labelFieldMaterialFingerprint], &record.LastAttemptedMaterialFingerprint),
	}
	for _, fieldValid := range valid {
		if !fieldValid {
			return false
		}
	}
	return true
}

func exactLabelFields(fields map[string]json.RawMessage) bool {
	names := [...]string{
		labelFieldVersion, labelFieldHarness, labelFieldSession, labelFieldLabel,
		labelFieldSourceGeneration, labelFieldLatestCompletion, labelFieldExchangeCount,
		labelFieldSuccessfulRefresh, labelFieldMaterialFingerprint,
	}
	if len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return true
}

func decodeLabelString(raw json.RawMessage, destination *string) bool {
	return validPiJSONString(raw) && json.Unmarshal(raw, destination) == nil
}

func decodeLabelInteger(raw json.RawMessage, destination *int) bool {
	return !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && json.Unmarshal(raw, destination) == nil
}

func decodeLabelUint(raw json.RawMessage, destination *uint64) bool {
	return !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && json.Unmarshal(raw, destination) == nil
}

func validLabelRecord(record labelRecord, harness, session string) bool {
	if record.Version != labelRecordVersion || record.Harness != harness || record.Session != session ||
		!knownHarness(record.Harness) ||
		!validPreparedMetadata(record.Session, maximumPreparedIDBytes, false) ||
		!validCompletionID(record.LatestCompletionID) ||
		record.SourceGeneration == 0 || record.SourceGeneration != record.ExchangeCount ||
		record.LastSuccessfulRefreshExchange > record.ExchangeCount ||
		!validLabelFingerprint(record.LastAttemptedMaterialFingerprint) {
		return false
	}
	if record.Label == "" {
		return record.LastSuccessfulRefreshExchange == 0
	}
	return validPiGeneratedLabel(record.Label) && record.LastSuccessfulRefreshExchange > 0
}

func validLabelFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func labelSnapshotName(harness, session string) string {
	return labelPairFingerprint(harness, session) + labelSnapshotSuffix
}

func labelPairFingerprint(first, second string) string {
	encoded := make([]byte, 0, 2*binary.MaxVarintLen64+len(first)+len(second))
	encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(first)))
	encoded = append(encoded, first...)
	encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(second)))
	encoded = append(encoded, second...)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func labelUnavailableError() error {
	return errors.New("notify: labels unavailable")
}
