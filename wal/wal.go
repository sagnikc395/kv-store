package wal

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type Record map[string]any

// WAL is an append-only JSON-lines write-ahead log.
type WAL struct {
	mu   sync.Mutex
	file *os.File
	path string
}

func Open(dir string) (*WAL, error) {
	return OpenFile(dir, "wal.log")
}

func OpenFile(dir, filename string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, filename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &WAL{file: f, path: path}, nil
}

func (w *WAL) Append(record Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return errors.New("wal is closed")
	}

	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := w.file.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := w.file.Sync(); err != nil {
		return err
	}
	return nil
}

func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func Replay(dir string) ([]Record, error) {
	return ReplayFile(dir, "wal.log")
}

func ReplayFile(dir, filename string) ([]Record, error) {
	path := filepath.Join(dir, filename)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Record{}, nil
		}
		return nil, err
	}
	defer f.Close()

	records := []Record{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}
