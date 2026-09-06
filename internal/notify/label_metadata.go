package notify

// LabelMetadata is the validated, read-only public projection of one persisted
// harness/session naming record. LabelGeneration is the exchange at which the
// current label was last successfully refreshed; it is independent of the
// latest accepted source generation.
type LabelMetadata struct {
	Label            string
	CompletionID     string
	SourceGeneration uint64
	LabelGeneration  uint64
}

// ReadLabelMetadata reads one exact harness/session scope without creating or
// modifying label state. The boolean reports whether a validated record was
// present. Missing state is not an error; corrupt or unsafe state is.
func ReadLabelMetadata(stateBase, harness, session string) (LabelMetadata, bool, error) {
	if !knownHarness(harness) || !validPreparedMetadata(session, maximumPreparedIDBytes, false) {
		return LabelMetadata{}, false, labelUnavailableError()
	}

	store := NewLabelStore(stateBase)
	root, missing, err := store.openRoot(false)
	if err != nil {
		return LabelMetadata{}, false, err
	}
	if missing {
		return LabelMetadata{}, false, nil
	}
	defer func() { _ = root.Close() }()

	record, present, err := readLabelRecord(root, harness, session)
	if err != nil || !present {
		return LabelMetadata{}, present, err
	}
	return LabelMetadata{
		Label:            record.Label,
		CompletionID:     record.LatestCompletionID,
		SourceGeneration: record.SourceGeneration,
		LabelGeneration:  record.LastSuccessfulRefreshExchange,
	}, true, nil
}
