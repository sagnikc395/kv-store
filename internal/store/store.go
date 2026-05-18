package store

import (
	"sort"
	"sync"
	"time"
)

type Command struct {
	Op                string   `json:"op"`
	Key               string   `json:"key"`
	Value             string   `json:"value,omitempty"`
	TTLSeconds        *float64 `json:"ttl,omitempty"`
	ExpiresAtUnixNano int64    `json:"expires_at_unix_nano,omitempty"`
}

type SnapshotEntry struct {
	Value             string `json:"value"`
	ExpiresAtUnixNano int64  `json:"expires_at_unix_nano,omitempty"`
}

type Snapshot struct {
	Version           int                      `json:"version"`
	CreatedAtUnixNano int64                    `json:"created_at_unix_nano"`
	Entries           map[string]SnapshotEntry `json:"entries"`
}

type item struct {
	value     string
	expiresAt time.Time
}

type Store struct {
	mu   sync.RWMutex
	data map[string]item
}

func New() *Store {
	return &Store{data: make(map[string]item)}
}

func DurableCommand(cmd Command, now time.Time) Command {
	if cmd.Op == "set" && cmd.TTLSeconds != nil && *cmd.TTLSeconds > 0 && cmd.ExpiresAtUnixNano == 0 {
		cmd.ExpiresAtUnixNano = now.Add(durationFromSeconds(*cmd.TTLSeconds)).UnixNano()
	}
	return cmd
}

func (s *Store) Set(key, value string, ttlSeconds *float64) {
	cmd := DurableCommand(Command{Op: "set", Key: key, Value: value, TTLSeconds: ttlSeconds}, time.Now())
	s.Apply(cmd)
}

func (s *Store) Get(key string) (string, bool) {
	s.mu.RLock()
	it, ok := s.data[key]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	if expired(it, time.Now()) {
		s.mu.Lock()
		if current, exists := s.data[key]; exists && expired(current, time.Now()) {
			delete(s.data, key)
		}
		s.mu.Unlock()
		return "", false
	}
	return it.value, true
}

func (s *Store) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; !ok {
		return false
	}
	delete(s.data, key)
	return true
}

func (s *Store) Keys() []string {
	now := time.Now()
	s.mu.RLock()
	keys := make([]string, 0, len(s.data))
	for key, it := range s.data {
		if !expired(it, now) {
			keys = append(keys, key)
		}
	}
	s.mu.RUnlock()
	sort.Strings(keys)
	return keys
}

func (s *Store) Size() int {
	return len(s.Keys())
}

func (s *Store) Apply(cmd Command) (string, bool) {
	switch cmd.Op {
	case "set":
		var expiresAt time.Time
		if cmd.ExpiresAtUnixNano > 0 {
			expiresAt = time.Unix(0, cmd.ExpiresAtUnixNano)
		} else if cmd.TTLSeconds != nil && *cmd.TTLSeconds > 0 {
			expiresAt = time.Now().Add(durationFromSeconds(*cmd.TTLSeconds))
		}

		s.mu.Lock()
		s.data[cmd.Key] = item{value: cmd.Value, expiresAt: expiresAt}
		s.mu.Unlock()
		return cmd.Value, true
	case "delete":
		s.Delete(cmd.Key)
		return "", false
	default:
		return "", false
	}
}

func (s *Store) PurgeExpired() int {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for key, it := range s.data {
		if expired(it, now) {
			delete(s.data, key)
			removed++
		}
	}
	return removed
}

func (s *Store) Snapshot() Snapshot {
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make(map[string]SnapshotEntry, len(s.data))
	for key, it := range s.data {
		if expired(it, now) {
			continue
		}
		entry := SnapshotEntry{Value: it.value}
		if !it.expiresAt.IsZero() {
			entry.ExpiresAtUnixNano = it.expiresAt.UnixNano()
		}
		entries[key] = entry
	}
	return Snapshot{Version: 1, CreatedAtUnixNano: now.UnixNano(), Entries: entries}
}

func (s *Store) Restore(snapshot Snapshot) {
	now := time.Now()
	next := make(map[string]item, len(snapshot.Entries))
	for key, entry := range snapshot.Entries {
		it := item{value: entry.Value}
		if entry.ExpiresAtUnixNano > 0 {
			it.expiresAt = time.Unix(0, entry.ExpiresAtUnixNano)
		}
		if !expired(it, now) {
			next[key] = it
		}
	}
	s.mu.Lock()
	s.data = next
	s.mu.Unlock()
}

func expired(it item, now time.Time) bool {
	return !it.expiresAt.IsZero() && !now.Before(it.expiresAt)
}

func durationFromSeconds(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}
