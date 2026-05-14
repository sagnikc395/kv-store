package store

import (
	"sync"
	"time"
)

// Command is the committed operation format shared by the store, WAL, and Raft.
type Command map[string]any

type entry struct {
	value     string
	expiresAt time.Time
}

// KVStore is an in-memory key-value store with optional per-key TTL.
type KVStore struct {
	mu   sync.Mutex
	data map[string]entry
}

func New() *KVStore {
	return &KVStore{data: make(map[string]entry)}
}

func (s *KVStore) Get(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.data[key]
	if !ok {
		return "", false
	}
	if !e.expiresAt.IsZero() && !time.Now().Before(e.expiresAt) {
		delete(s.data, key)
		return "", false
	}
	return e.value, true
}

func (s *KVStore) Set(key, value string, ttl time.Duration) {
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}

	s.mu.Lock()
	s.data[key] = entry{value: value, expiresAt: expiresAt}
	s.mu.Unlock()
}

func (s *KVStore) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.data[key]; !ok {
		return false
	}
	delete(s.data, key)
	return true
}

func (s *KVStore) Keys() []string {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	keys := make([]string, 0, len(s.data))
	for k, e := range s.data {
		if e.expiresAt.IsZero() || now.Before(e.expiresAt) {
			keys = append(keys, k)
		}
	}
	return keys
}

func (s *KVStore) Size() int {
	return len(s.Keys())
}

// Apply applies a committed Raft command. Supported ops are set and delete.
func (s *KVStore) Apply(command Command) (string, bool) {
	op, _ := command["op"].(string)
	key, _ := command["key"].(string)

	switch op {
	case "set":
		value, _ := command["value"].(string)
		s.Set(key, value, ttlFromCommand(command["ttl"]))
		return value, true
	case "delete":
		s.Delete(key)
	}
	return "", false
}

func (s *KVStore) PurgeExpired() int {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	for k, e := range s.data {
		if !e.expiresAt.IsZero() && !now.Before(e.expiresAt) {
			delete(s.data, k)
			removed++
		}
	}
	return removed
}

func ttlFromCommand(v any) time.Duration {
	switch t := v.(type) {
	case nil:
		return 0
	case time.Duration:
		return t
	case float64:
		return time.Duration(t * float64(time.Second))
	case float32:
		return time.Duration(float64(t) * float64(time.Second))
	case int:
		return time.Duration(t) * time.Second
	case int64:
		return time.Duration(t) * time.Second
	case int32:
		return time.Duration(t) * time.Second
	default:
		return 0
	}
}
