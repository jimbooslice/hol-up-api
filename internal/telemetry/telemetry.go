// Package telemetry holds bounded, process-local measurements for operations.
package telemetry

import (
	"sort"
	"sync"
	"time"
)

// Feed is the latest operational state for one realtime source.
type Feed struct {
	Name                string
	LastSuccess         time.Time
	SourceTimestamp     time.Time
	TrainCount          int
	LastRefreshDuration time.Duration
	RefreshSuccesses    uint64
	RefreshErrors       uint64
}

// Store safely records poll outcomes for the Prometheus endpoint.
type Store struct {
	mu    sync.RWMutex
	feeds map[string]Feed
}

func New(feeds ...string) *Store {
	store := &Store{feeds: make(map[string]Feed, len(feeds))}
	for _, name := range feeds {
		store.feeds[name] = Feed{Name: name}
	}
	return store
}

// Record stores one completed refresh. Source data remains visible after a
// failed refresh because the caller supplies its last-known-good snapshot.
func (s *Store) Record(name string, success bool, duration time.Duration, completedAt, sourceTimestamp time.Time, trainCount int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	feed := s.feeds[name]
	feed.Name = name
	feed.LastRefreshDuration = duration
	feed.SourceTimestamp = sourceTimestamp
	feed.TrainCount = trainCount
	if success {
		feed.RefreshSuccesses++
		feed.LastSuccess = completedAt
	} else {
		feed.RefreshErrors++
	}
	s.feeds[name] = feed
}

// Snapshot returns feeds in a stable order for deterministic exposition.
func (s *Store) Snapshot() []Feed {
	s.mu.RLock()
	defer s.mu.RUnlock()

	feeds := make([]Feed, 0, len(s.feeds))
	for _, feed := range s.feeds {
		feeds = append(feeds, feed)
	}
	sort.Slice(feeds, func(left, right int) bool { return feeds[left].Name < feeds[right].Name })
	return feeds
}
