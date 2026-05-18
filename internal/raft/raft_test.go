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
