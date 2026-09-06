package notify

import (
	"container/list"
	"sync"
	"time"
)

const (
	claimTTL      = 24 * time.Hour
	maximumClaims = 10000
)

type claimKey struct {
	Harness      string
	SessionID    string
	Kind         string
	CompletionID string
}

type claimToken uint64

type claimEntry struct {
	claimedAt time.Time
	token     claimToken
}

// claimStore is daemon-local, bounded, and intentionally non-durable. The
// list stores the same keys as entries in first-claim order, so expiry and
// capacity eviction are oldest-first without unbounded auxiliary history.
type claimStore struct {
	mutex   sync.Mutex
	now     func() time.Time
	next    claimToken
	entries map[claimKey]claimEntry
	order   *list.List
}

func newClaimStore(now func() time.Time) *claimStore {
	if now == nil {
		now = time.Now
	}
	return &claimStore{
		now: now, entries: make(map[claimKey]claimEntry), order: &list.List{},
	}
}

// claim atomically admits a new owner. Existing in-flight and successful
// claims are both duplicates; duplicate observations never refresh TTL or
// ordering.
func (store *claimStore) claim(key claimKey) (claimToken, bool) {
	store.mutex.Lock()
	defer store.mutex.Unlock()

	now := store.now()
	store.removeExpired(now)
	if _, exists := store.entries[key]; exists {
		return 0, false
	}
	if len(store.entries) >= maximumClaims {
		store.removeOldest()
	}
	store.next++
	if store.next == 0 {
		store.next++
	}
	entry := claimEntry{claimedAt: now, token: store.next}
	store.entries[key] = entry
	store.order.PushBack(key)
	return entry.token, true
}

// finish retains a successful owner and releases a failed owner. Ownership is
// token-checked so a delayed failed job cannot delete a replacement admitted
// after TTL expiry or capacity eviction.
func (store *claimStore) finish(key claimKey, token claimToken, succeeded bool) {
	if succeeded {
		return
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	entry, exists := store.entries[key]
	if !exists || entry.token != token {
		return
	}
	delete(store.entries, key)
	store.removeKeyFromOrder(key)
}

func (store *claimStore) removeExpired(now time.Time) {
	for element := store.order.Front(); element != nil; {
		next := element.Next()
		key, valid := claimElementKey(element)
		if !valid {
			store.order.Remove(element)
			element = next
			continue
		}
		entry, exists := store.entries[key]
		if !exists {
			store.order.Remove(element)
			element = next
			continue
		}
		if now.Sub(entry.claimedAt) < claimTTL {
			return
		}
		delete(store.entries, key)
		store.order.Remove(element)
		element = next
	}
}

func (store *claimStore) removeOldest() {
	oldest := store.order.Front()
	if oldest == nil {
		return
	}
	if key, valid := claimElementKey(oldest); valid {
		delete(store.entries, key)
	}
	store.order.Remove(oldest)
}

func (store *claimStore) removeKeyFromOrder(key claimKey) {
	for element := store.order.Front(); element != nil; element = element.Next() {
		stored, valid := claimElementKey(element)
		if valid && stored == key {
			store.order.Remove(element)
			return
		}
	}
}

func claimElementKey(element *list.Element) (claimKey, bool) {
	key, valid := element.Value.(claimKey)
	return key, valid
}
