package store

import (
	"testing"
	"time"
)

func TestSetGetDelete(t *testing.T) {
	s := New()
	s.Set("key", "value", nil)
	if value, ok := s.Get("key"); !ok || value != "value" {
		t.Fatalf("Get() = %q, %v", value, ok)
	}
	if !s.Delete("key") {
		t.Fatal("Delete() returned false")
	}
	if _, ok := s.Get("key"); ok {
		t.Fatal("deleted key is still present")
	}
}

func TestTTLExpires(t *testing.T) {
	s := New()
	ttl := 0.03
	s.Set("short", "lived", &ttl)
	if _, ok := s.Get("short"); !ok {
		t.Fatal("key expired too early")
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := s.Get("short"); ok {
		t.Fatal("key did not expire")
	}
}

func TestSnapshotRestoreSkipsExpiredKeys(t *testing.T) {
	s := New()
	expired := Command{Op: "set", Key: "old", Value: "x", ExpiresAtUnixNano: time.Now().Add(-time.Second).UnixNano()}
	fresh := Command{Op: "set", Key: "new", Value: "y", ExpiresAtUnixNano: time.Now().Add(time.Hour).UnixNano()}
	s.Apply(expired)
	s.Apply(fresh)

	restored := New()
	restored.Restore(s.Snapshot())
	if _, ok := restored.Get("old"); ok {
		t.Fatal("expired key restored")
	}
	if value, ok := restored.Get("new"); !ok || value != "y" {
		t.Fatalf("fresh key missing after restore: %q %v", value, ok)
	}
}
