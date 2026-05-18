package wal

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/sagnikc395/kv-store/internal/store"
)

const (
	LogFile      = "wal.log"
	SnapshotFile = "snapshot.json"
)

type WAL struct {
	mu   sync.Mutex
	path string
	file *os.File
}

func Open(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, LogFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &WAL{path: path, file: file}, nil
}

func (w *WAL) Append(cmd store.Command) error {
	line, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.file.Write(append(line, '\n')); err != nil {
		return err
	}
	return w.file.Sync()
}

func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

func (w *WAL) Compact(snapshot store.Snapshot) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.file.Close(); err != nil {
		return err
	}
	dir := filepath.Dir(w.path)
	if err := Compact(dir, snapshot); err != nil {
		file, openErr := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if openErr == nil {
			w.file = file
		}
		return err
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.file = file
	return nil
}

func Recover(dir string) (store.Snapshot, []store.Command, error) {
	snapshot, err := readSnapshot(filepath.Join(dir, SnapshotFile))
	if err != nil {
		return store.Snapshot{}, nil, err
	}
	commands, err := Replay(filepath.Join(dir, LogFile))
	if err != nil {
		return store.Snapshot{}, nil, err
	}
	return snapshot, commands, nil
}

func Replay(path string) ([]store.Command, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var commands []store.Command
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var cmd store.Command
		if err := json.Unmarshal(line, &cmd); err != nil {
			continue
		}
		commands = append(commands, cmd)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return commands, nil
}

func Compact(dir string, snapshot store.Snapshot) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	snapshotPath := filepath.Join(dir, SnapshotFile)
	tmpSnapshot := snapshotPath + ".tmp"
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmpSnapshot, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := syncFile(tmpSnapshot); err != nil {
		return err
	}
	if err := os.Rename(tmpSnapshot, snapshotPath); err != nil {
		return err
	}

	logPath := filepath.Join(dir, LogFile)
	tmpLog := logPath + ".tmp"
	file, err := os.OpenFile(tmpLog, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmpLog, logPath)
}

func readSnapshot(path string) (store.Snapshot, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return store.Snapshot{Version: 1, Entries: map[string]store.SnapshotEntry{}}, nil
	}
	if err != nil {
		return store.Snapshot{}, err
	}
	defer file.Close()

	var snapshot store.Snapshot
	if err := json.NewDecoder(file).Decode(&snapshot); err != nil && !errors.Is(err, io.EOF) {
		return store.Snapshot{}, err
	}
	if snapshot.Entries == nil {
		snapshot.Entries = map[string]store.SnapshotEntry{}
	}
	return snapshot, nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
