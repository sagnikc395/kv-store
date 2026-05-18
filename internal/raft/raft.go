package raft

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/sagnikc395/kv-store/internal/store"
)

type Transport interface {
	RequestVote(ctx context.Context, peerID int, args RequestVoteArgs) (RequestVoteReply, error)
	AppendEntries(ctx context.Context, peerID int, args AppendEntriesArgs) (AppendEntriesReply, error)
}

type Config struct {
	ID                  int
	Peers               []int
	Transport           Transport
	Storage             Storage
	ElectionMin         time.Duration
	ElectionMax         time.Duration
	HeartbeatInterval   time.Duration
	LeaderQuorumTimeout time.Duration
}

type Report struct {
	ID          int    `json:"id"`
	Term        int    `json:"term"`
	State       string `json:"state"`
	IsLeader    bool   `json:"is_leader"`
	CommitIndex int    `json:"commit_index"`
	LastApplied int    `json:"last_applied"`
	LogLength   int    `json:"log_length"`
}

type Node struct {
	mu        sync.Mutex
	id        int
	peers     []int
	transport Transport
	storage   Storage

	currentTerm int
	votedFor    int
	log         []LogEntry

	commitIndex int
	lastApplied int
	state       State

	nextIndex  map[int]int
	matchIndex map[int]int

	electionReset      time.Time
	leaderQuorumSeenAt time.Time

	electionMin         time.Duration
	electionMax         time.Duration
	heartbeatInterval   time.Duration
	leaderQuorumTimeout time.Duration

	commitCh chan LogEntry
	wake     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func NewNode(cfg Config) (*Node, error) {
	if cfg.Storage == nil {
		cfg.Storage = NewMemoryStorage()
	}
	if cfg.ElectionMin <= 0 {
		cfg.ElectionMin = 300 * time.Millisecond
	}
	if cfg.ElectionMax <= cfg.ElectionMin {
		cfg.ElectionMax = cfg.ElectionMin + 200*time.Millisecond
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 50 * time.Millisecond
	}
	if cfg.LeaderQuorumTimeout <= 0 {
		cfg.LeaderQuorumTimeout = 150 * time.Millisecond
	}

	persistent, err := cfg.Storage.Load()
	if err != nil {
		return nil, err
	}
	if persistent.VotedFor == 0 && persistent.CurrentTerm == 0 && len(persistent.Log) == 0 {
		persistent.VotedFor = -1
	}

	n := &Node{
		id:                  cfg.ID,
		peers:               append([]int(nil), cfg.Peers...),
		transport:           cfg.Transport,
		storage:             cfg.Storage,
		currentTerm:         persistent.CurrentTerm,
		votedFor:            persistent.VotedFor,
		log:                 append([]LogEntry(nil), persistent.Log...),
		commitIndex:         -1,
		lastApplied:         -1,
		state:               Follower,
		nextIndex:           make(map[int]int),
		matchIndex:          make(map[int]int),
		electionMin:         cfg.ElectionMin,
		electionMax:         cfg.ElectionMax,
		heartbeatInterval:   cfg.HeartbeatInterval,
		leaderQuorumTimeout: cfg.LeaderQuorumTimeout,
		commitCh:            make(chan LogEntry, 256),
		wake:                make(chan struct{}, 1),
		done:                make(chan struct{}),
	}
	return n, nil
}

func (n *Node) Start() {
	n.mu.Lock()
	n.electionReset = time.Now()
	n.mu.Unlock()
	go n.runElectionTimer()
	go n.runCommitNotifier()
}

func (n *Node) Stop() {
	n.stopOnce.Do(func() {
		n.mu.Lock()
		n.state = Dead
		n.mu.Unlock()
		close(n.done)
		n.signalCommit()
	})
}

func (n *Node) CommitChan() <-chan LogEntry {
	return n.commitCh
}

func (n *Node) Submit(cmd store.Command) bool {
	_, ok := n.SubmitWithIndex(cmd)
	return ok
}

func (n *Node) SubmitWithIndex(cmd store.Command) (int, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state != Leader {
		return -1, false
	}
	n.log = append(n.log, LogEntry{Command: cmd, Term: n.currentTerm})
	index := len(n.log) - 1
	n.saveLocked()
	if len(n.peers) == 0 {
		n.commitIndex = index
		n.signalCommit()
	}
	return index, true
}

func (n *Node) Report() Report {
	n.mu.Lock()
	defer n.mu.Unlock()
	return Report{
		ID:          n.id,
		Term:        n.currentTerm,
		State:       n.state.String(),
		IsLeader:    n.state == Leader,
		CommitIndex: n.commitIndex,
		LastApplied: n.lastApplied,
		LogLength:   len(n.log),
	}
}

func (n *Node) RequestVote(args RequestVoteArgs) RequestVoteReply {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state == Dead {
		return RequestVoteReply{Term: n.currentTerm}
	}
	if args.Term > n.currentTerm {
		n.becomeFollowerLocked(args.Term)
	}

	reply := RequestVoteReply{Term: n.currentTerm}
	if args.Term < n.currentTerm {
		return reply
	}

	lastIndex, lastTerm := n.lastLogIndexAndTermLocked()
	upToDate := args.LastLogTerm > lastTerm ||
		(args.LastLogTerm == lastTerm && args.LastLogIndex >= lastIndex)
	if (n.votedFor == -1 || n.votedFor == args.CandidateID) && upToDate {
		n.votedFor = args.CandidateID
		n.electionReset = time.Now()
		n.saveLocked()
		reply.VoteGranted = true
	}
	reply.Term = n.currentTerm
	return reply
}

func (n *Node) AppendEntries(args AppendEntriesArgs) AppendEntriesReply {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state == Dead {
		return AppendEntriesReply{Term: n.currentTerm}
	}
	if args.Term > n.currentTerm {
		n.becomeFollowerLocked(args.Term)
	}
	reply := AppendEntriesReply{Term: n.currentTerm}
	if args.Term < n.currentTerm {
		return reply
	}

	if n.state != Follower {
		n.becomeFollowerLocked(args.Term)
	}
	n.electionReset = time.Now()

	if args.PrevLogIndex >= 0 {
		if args.PrevLogIndex >= len(n.log) || n.log[args.PrevLogIndex].Term != args.PrevLogTerm {
			return reply
		}
	}

	insertAt := args.PrevLogIndex + 1
	i := 0
	for insertAt < len(n.log) && i < len(args.Entries) {
		if n.log[insertAt].Term != args.Entries[i].Term {
			break
		}
		insertAt++
		i++
	}
	if i < len(args.Entries) {
		n.log = append(append([]LogEntry(nil), n.log[:insertAt]...), args.Entries[i:]...)
		n.saveLocked()
	}
	if args.LeaderCommit > n.commitIndex {
		n.commitIndex = min(args.LeaderCommit, len(n.log)-1)
		n.signalCommit()
	}

	reply.Success = true
	reply.Term = n.currentTerm
	return reply
}

func (n *Node) runElectionTimer() {
	timeout := n.randomElectionTimeout()
	n.mu.Lock()
	termStarted := n.currentTerm
	n.mu.Unlock()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-n.done:
			return
		case <-ticker.C:
			n.mu.Lock()
			if n.state != Follower && n.state != Candidate {
				n.mu.Unlock()
				return
			}
			if n.currentTerm != termStarted {
				n.mu.Unlock()
				return
			}
			if time.Since(n.electionReset) >= timeout {
				n.startElectionLocked()
				n.mu.Unlock()
				return
			}
			n.mu.Unlock()
		}
	}
}

func (n *Node) startElectionLocked() {
	n.state = Candidate
	n.currentTerm++
	n.votedFor = n.id
	n.electionReset = time.Now()
	n.saveLocked()

	savedTerm := n.currentTerm
	lastIndex, lastTerm := n.lastLogIndexAndTermLocked()
	votes := 1
	if n.hasMajority(votes) {
		n.startLeaderLocked()
		return
	}

	for _, peerID := range n.peers {
		go func(peerID int) {
			if n.transport == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			reply, err := n.transport.RequestVote(ctx, peerID, RequestVoteArgs{
				Term:         savedTerm,
				CandidateID:  n.id,
				LastLogIndex: lastIndex,
				LastLogTerm:  lastTerm,
			})
			if err != nil {
				return
			}
			n.mu.Lock()
			defer n.mu.Unlock()
			if n.state != Candidate || n.currentTerm != savedTerm {
				return
			}
			if reply.Term > n.currentTerm {
				n.becomeFollowerLocked(reply.Term)
				return
			}
			if reply.Term == savedTerm && reply.VoteGranted {
				votes++
				if n.hasMajority(votes) {
					n.startLeaderLocked()
				}
			}
		}(peerID)
	}
	go n.runElectionTimer()
}

func (n *Node) becomeFollowerLocked(term int) {
	if term > n.currentTerm {
		n.votedFor = -1
	}
	n.state = Follower
	n.currentTerm = term
	n.electionReset = time.Now()
	n.saveLocked()
	go n.runElectionTimer()
}

func (n *Node) startLeaderLocked() {
	n.state = Leader
	n.nextIndex = make(map[int]int, len(n.peers))
	n.matchIndex = make(map[int]int, len(n.peers))
	for _, peerID := range n.peers {
		n.nextIndex[peerID] = len(n.log)
		n.matchIndex[peerID] = -1
	}
	n.leaderQuorumSeenAt = time.Now()
	go n.heartbeatLoop()
}

func (n *Node) heartbeatLoop() {
	ticker := time.NewTicker(n.heartbeatInterval)
	defer ticker.Stop()
	for {
		n.leaderSendAppendEntries()
		select {
		case <-n.done:
			return
		case <-ticker.C:
			n.mu.Lock()
			if n.state != Leader {
				n.mu.Unlock()
				return
			}
			if len(n.peers) > 0 && time.Since(n.leaderQuorumSeenAt) >= n.leaderQuorumTimeout {
				n.becomeFollowerLocked(n.currentTerm)
				n.mu.Unlock()
				return
			}
			n.mu.Unlock()
		}
	}
}

func (n *Node) leaderSendAppendEntries() {
	n.mu.Lock()
	if n.state != Leader {
		n.mu.Unlock()
		return
	}
	savedTerm := n.currentTerm
	successes := 1
	if len(n.peers) == 0 {
		n.leaderQuorumSeenAt = time.Now()
	}
	peers := append([]int(nil), n.peers...)
	n.mu.Unlock()

	for _, peerID := range peers {
		n.mu.Lock()
		if n.state != Leader || n.currentTerm != savedTerm {
			n.mu.Unlock()
			return
		}
		nextIndex := n.nextIndex[peerID]
		prevIndex := nextIndex - 1
		prevTerm := -1
		if prevIndex >= 0 {
			prevTerm = n.log[prevIndex].Term
		}
		entries := append([]LogEntry(nil), n.log[nextIndex:]...)
		args := AppendEntriesArgs{
			Term:         savedTerm,
			LeaderID:     n.id,
			PrevLogIndex: prevIndex,
			PrevLogTerm:  prevTerm,
			Entries:      entries,
			LeaderCommit: n.commitIndex,
		}
		n.mu.Unlock()

		go func(peerID, sentNext int, args AppendEntriesArgs) {
			if n.transport == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			reply, err := n.transport.AppendEntries(ctx, peerID, args)
			if err != nil {
				return
			}

			n.mu.Lock()
			defer n.mu.Unlock()
			if reply.Term > n.currentTerm {
				n.becomeFollowerLocked(reply.Term)
				return
			}
			if n.state != Leader || n.currentTerm != savedTerm {
				return
			}
			if reply.Success {
				n.nextIndex[peerID] = sentNext + len(args.Entries)
				n.matchIndex[peerID] = n.nextIndex[peerID] - 1
				successes++
				if n.hasMajority(successes) {
					n.leaderQuorumSeenAt = time.Now()
				}
				n.advanceCommitLocked()
			} else if n.nextIndex[peerID] > 0 {
				n.nextIndex[peerID]--
			}
		}(peerID, nextIndex, args)
	}
}

func (n *Node) advanceCommitLocked() {
	oldCommit := n.commitIndex
	for i := n.commitIndex + 1; i < len(n.log); i++ {
		if n.log[i].Term != n.currentTerm {
			continue
		}
		matches := 1
		for _, peerID := range n.peers {
			if n.matchIndex[peerID] >= i {
				matches++
			}
		}
		if n.hasMajority(matches) {
			n.commitIndex = i
		}
	}
	if n.commitIndex != oldCommit {
		n.signalCommit()
	}
}

func (n *Node) runCommitNotifier() {
	for {
		select {
		case <-n.done:
			return
		case <-n.wake:
			for {
				n.mu.Lock()
				if n.state == Dead || n.lastApplied >= n.commitIndex {
					n.mu.Unlock()
					break
				}
				n.lastApplied++
				entry := n.log[n.lastApplied]
				n.mu.Unlock()

				select {
				case <-n.done:
					return
				case n.commitCh <- entry:
				}
			}
		}
	}
}

func (n *Node) signalCommit() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *Node) saveLocked() {
	if n.storage == nil {
		return
	}
	_ = n.storage.Save(PersistentState{
		CurrentTerm: n.currentTerm,
		VotedFor:    n.votedFor,
		Log:         append([]LogEntry(nil), n.log...),
	})
}

func (n *Node) lastLogIndexAndTermLocked() (int, int) {
	if len(n.log) == 0 {
		return -1, -1
	}
	idx := len(n.log) - 1
	return idx, n.log[idx].Term
}

func (n *Node) hasMajority(votes int) bool {
	return votes*2 > len(n.peers)+1
}

func (n *Node) randomElectionTimeout() time.Duration {
	delta := n.electionMax - n.electionMin
	if delta <= 0 {
		return n.electionMin
	}
	return n.electionMin + time.Duration(rand.Int64N(int64(delta)))
}

var ErrNoTransport = errors.New("raft transport is not configured")
