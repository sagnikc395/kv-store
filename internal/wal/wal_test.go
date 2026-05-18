package wal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sagnikc395/kv-store/internal/store"
)

func TestAppendReplay(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(store.Command{Op: "set", Key: "a", Value: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(store.Command{Op: "delete", Key: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	commands, err := Replay(filepath.Join(dir, LogFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[0].Key != "a" || commands[1].Op != "delete" {
		t.Fatalf("unexpected commands: %#v", commands)
	}
}

func TestReplaySkipsTruncatedLine(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(store.Command{Op: "set", Key: "good", Value: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, LogFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"op":"set","key":"bad"`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	commands, err := Replay(filepath.Join(dir, LogFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Key != "good" {
		t.Fatalf("unexpected replay result: %#v", commands)
	}
}

func TestCompactWritesSnapshotAndClearsLog(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(store.Command{Op: "set", Key: "a", Value: "1"}); err != nil {
		t.Fatal(err)
	}
	s := store.New()
	s.Apply(store.Command{Op: "set", Key: "a", Value: "2"})
	if err := w.Compact(s.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	snapshot, commands, err := Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 0 {
		t.Fatalf("log was not cleared: %#v", commands)
	}
	if snapshot.Entries["a"].Value != "2" {
		t.Fatalf("snapshot not recovered: %#v", snapshot)
	}
}
