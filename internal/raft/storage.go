package raft

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/sagnikc395/kv-store/internal/store"
)

type PersistentState struct {
	CurrentTerm int            `json:"current_term"`
	VotedFor    int            `json:"voted_for"`
	Log         []LogEntry     `json:"log"`
	Snapshot    SnapshotState  `json:"snapshot"`
	Members     map[int]string `json:"members,omitempty"`
}

type Storage interface {
	Load() (PersistentState, error)
	Save(PersistentState) error
}

type MemoryStorage struct {
	mu    sync.Mutex
	state PersistentState
	set   bool
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{state: PersistentState{VotedFor: -1}}
}

func (s *MemoryStorage) Load() (PersistentState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set {
		return PersistentState{VotedFor: -1}, nil
	}
	return cloneState(s.state), nil
}

func (s *MemoryStorage) Save(state PersistentState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = cloneState(state)
	s.set = true
	return nil
}

type FileStorage struct {
	mu   sync.Mutex
	path string
}

func NewFileStorage(dir string) *FileStorage {
	return &FileStorage{path: filepath.Join(dir, "raft_state.json")}
}

func (s *FileStorage) Load() (PersistentState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return PersistentState{VotedFor: -1}, nil
	}
	if err != nil {
		return PersistentState{}, err
	}
	defer file.Close()
	var state PersistentState
	if err := json.NewDecoder(file).Decode(&state); err != nil {
		return PersistentState{}, err
	}
	if state.VotedFor == 0 && state.CurrentTerm == 0 && len(state.Log) == 0 {
		state.VotedFor = -1
	}
	return state, nil
}

func (s *FileStorage) Save(state PersistentState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func cloneState(state PersistentState) PersistentState {
	next := state
	if state.Log != nil {
		next.Log = append([]LogEntry(nil), state.Log...)
	}
	if state.Members != nil {
		next.Members = make(map[int]string, len(state.Members))
		for id, url := range state.Members {
			next.Members[id] = url
		}
	}
	if state.Snapshot.Data.Entries != nil {
		next.Snapshot.Data.Entries = make(map[string]store.SnapshotEntry, len(state.Snapshot.Data.Entries))
		for key, entry := range state.Snapshot.Data.Entries {
			next.Snapshot.Data.Entries[key] = entry
		}
	}
	return next
}
