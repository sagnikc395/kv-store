package routing

import "testing"

func TestHashRingRoutesStableKeys(t *testing.T) {
	ring := NewHashRing([]string{"n1", "n2", "n3"}, 100)
	first, ok := ring.Get("stable")
	if !ok {
		t.Fatal("ring returned no node")
	}
	for i := 0; i < 20; i++ {
		next, ok := ring.Get("stable")
		if !ok || next != first {
			t.Fatalf("route changed from %q to %q", first, next)
		}
	}
}

func TestHashRingRemove(t *testing.T) {
	ring := NewHashRing([]string{"n1", "n2", "n3"}, 50)
	ring.Remove("n2")
	for i := 0; i < 100; i++ {
		node, ok := ring.Get("key")
		if !ok {
			t.Fatal("ring returned no node")
		}
		if node == "n2" {
			t.Fatal("removed node was selected")
		}
	}
}
