package raft

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sagnikc395/kv-store/internal/store"
)

type memoryTransport struct {
	mu    sync.RWMutex
	nodes map[int]*Node
}

func newMemoryTransport() *memoryTransport {
	return &memoryTransport{nodes: make(map[int]*Node)}
}

func (t *memoryTransport) RequestVote(ctx context.Context, peerID int, args RequestVoteArgs) (RequestVoteReply, error) {
	t.mu.RLock()
	node := t.nodes[peerID]
	t.mu.RUnlock()
	if node == nil {
		return RequestVoteReply{}, fmt.Errorf("missing peer %d", peerID)
	}
	return node.RequestVote(args), nil
}

func (t *memoryTransport) AppendEntries(ctx context.Context, peerID int, args AppendEntriesArgs) (AppendEntriesReply, error) {
	t.mu.RLock()
	node := t.nodes[peerID]
	t.mu.RUnlock()
	if node == nil {
		return AppendEntriesReply{}, fmt.Errorf("missing peer %d", peerID)
	}
	return node.AppendEntries(args), nil
}

func (t *memoryTransport) InstallSnapshot(ctx context.Context, peerID int, args InstallSnapshotArgs) (InstallSnapshotReply, error) {
	t.mu.RLock()
	node := t.nodes[peerID]
	t.mu.RUnlock()
	if node == nil {
		return InstallSnapshotReply{}, fmt.Errorf("missing peer %d", peerID)
	}
	return node.InstallSnapshot(args), nil
}

func newCluster(t *testing.T, n int) ([]*Node, func()) {
	t.Helper()
	transport := newMemoryTransport()
	nodes := make([]*Node, 0, n)
	for i := 0; i < n; i++ {
		var peers []int
		for j := 0; j < n; j++ {
			if i != j {
				peers = append(peers, j)
			}
		}
		node, err := NewNode(Config{
			ID:                  i,
			Peers:               peers,
			Transport:           transport,
			Storage:             NewMemoryStorage(),
			ElectionMin:         40 * time.Millisecond,
			ElectionMax:         90 * time.Millisecond,
			HeartbeatInterval:   15 * time.Millisecond,
			LeaderQuorumTimeout: 120 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, node)
		transport.nodes[i] = node
	}
	for _, node := range nodes {
		node.Start()
	}
	return nodes, func() {
		for _, node := range nodes {
			node.Stop()
		}
	}
}

func waitLeader(t *testing.T, nodes []*Node) *Node {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var leader *Node
		for _, node := range nodes {
			report := node.Report()
			if report.IsLeader {
				if leader != nil {
					t.Fatalf("multiple leaders: %d and %d", leader.Report().ID, report.ID)
				}
				leader = node
			}
		}
		if leader != nil {
			return leader
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no leader elected")
	return nil
}

func TestElection(t *testing.T) {
	nodes, shutdown := newCluster(t, 3)
	defer shutdown()
	waitLeader(t, nodes)
}

func TestSubmitReplicatesToMajority(t *testing.T) {
	nodes, shutdown := newCluster(t, 3)
	defer shutdown()
	leader := waitLeader(t, nodes)
	if !leader.Submit(store.Command{Op: "set", Key: "x", Value: "1"}) {
		t.Fatal("leader rejected submit")
	}

	committed := 0
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		committed = 0
		for _, node := range nodes {
			if node.Report().CommitIndex >= 0 {
				committed++
			}
		}
		if committed >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("command committed on only %d nodes", committed)
}

func TestSubmitNonLeaderFails(t *testing.T) {
	nodes, shutdown := newCluster(t, 3)
	defer shutdown()
	leader := waitLeader(t, nodes)
	for _, node := range nodes {
		if node != leader {
			if node.Submit(store.Command{Op: "set", Key: "x", Value: "1"}) {
				t.Fatal("non-leader accepted submit")
			}
			return
		}
	}
}

func TestReadIndexRequiresLeaderQuorum(t *testing.T) {
	nodes, shutdown := newCluster(t, 3)
	defer shutdown()
	leader := waitLeader(t, nodes)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok := leader.ReadIndex(ctx); !ok {
		t.Fatal("leader read index failed with quorum available")
	}

	for _, node := range nodes {
		if node == leader {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, ok := node.ReadIndex(ctx); ok {
			t.Fatal("follower read index succeeded")
		}
		return
	}
}

func TestConfigChangeAddsMember(t *testing.T) {
	nodes, shutdown := newCluster(t, 3)
	defer shutdown()
	leader := waitLeader(t, nodes)
	index, ok := leader.SubmitConfigChange(ConfigChange{Type: "add", ID: 99, URL: "http://127.0.0.1:7099"})
	if !ok {
		t.Fatal("leader rejected config change")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		applied := 0
		for _, node := range nodes {
			found := false
			for _, member := range node.Members() {
				if member.ID == 99 && member.URL == "http://127.0.0.1:7099" {
					found = true
					break
				}
			}
			if found && node.Report().LastApplied >= index {
				applied++
			}
		}
		if applied >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("config change did not apply to a majority")
}

func TestInstallSnapshotRestoresSnapshotBoundary(t *testing.T) {
	follower, err := NewNode(Config{ID: 1, Storage: NewMemoryStorage()})
	if err != nil {
		t.Fatal(err)
	}
	reply := follower.InstallSnapshot(InstallSnapshotArgs{
		Term:              2,
		LeaderID:          2,
		LastIncludedIndex: 4,
		LastIncludedTerm:  2,
		Snapshot: store.Snapshot{
			Version: 1,
			Entries: map[string]store.SnapshotEntry{
				"k": {Value: "v"},
			},
		},
	})
	if reply.Term != 2 {
		t.Fatalf("unexpected reply term %d", reply.Term)
	}
	report := follower.Report()
	if report.SnapshotIndex != 4 || report.CommitIndex != 4 || report.LastApplied != 4 {
		t.Fatalf("snapshot not installed into indexes: %#v", report)
	}
	select {
	case installed := <-follower.SnapshotChan():
		if installed.LastIncludedIndex != 4 || installed.Snapshot.Entries["k"].Value != "v" {
			t.Fatalf("bad installed snapshot: %#v", installed)
		}
	default:
		t.Fatal("snapshot install was not published")
	}
}
