package raft

import (
	"context"
	"errors"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/sagnikc395/kv-store/internal/store"
)

type Transport interface {
	RequestVote(ctx context.Context, peerID int, args RequestVoteArgs) (RequestVoteReply, error)
	AppendEntries(ctx context.Context, peerID int, args AppendEntriesArgs) (AppendEntriesReply, error)
	InstallSnapshot(ctx context.Context, peerID int, args InstallSnapshotArgs) (InstallSnapshotReply, error)
}

type PeerURLUpdater interface {
	SetPeerURL(peerID int, url string)
	RemovePeerURL(peerID int)
}

type Config struct {
	ID                  int
	Peers               []int
	MemberURLs          map[int]string
	Transport           Transport
	Storage             Storage
	ElectionMin         time.Duration
	ElectionMax         time.Duration
	HeartbeatInterval   time.Duration
	LeaderQuorumTimeout time.Duration
}

type Report struct {
	ID            int    `json:"id"`
	Term          int    `json:"term"`
	State         string `json:"state"`
	IsLeader      bool   `json:"is_leader"`
	CommitIndex   int    `json:"commit_index"`
	LastApplied   int    `json:"last_applied"`
	LogLength     int    `json:"log_length"`
	SnapshotIndex int    `json:"snapshot_index"`
}

type InstalledSnapshot struct {
	LastIncludedIndex int
	Snapshot          store.Snapshot
}

type Node struct {
	mu        sync.Mutex
	id        int
	peers     []int
	members   map[int]string
	transport Transport
	storage   Storage

	currentTerm int
	votedFor    int
	log         []LogEntry
	snapshot    SnapshotState

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

	commitCh   chan LogEntry
	snapshotCh chan InstalledSnapshot
	wake       chan struct{}
	done       chan struct{}
	stopOnce   sync.Once
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
	members := cloneMembers(persistent.Members)
	if len(members) == 0 {
		members = cloneMembers(cfg.MemberURLs)
		if len(members) == 0 {
			members = make(map[int]string, len(cfg.Peers)+1)
			members[cfg.ID] = ""
			for _, peerID := range cfg.Peers {
				members[peerID] = ""
			}
		}
	}
	peers := peersFromMembers(cfg.ID, members)
	baseIndex := persistent.Snapshot.LastIncludedIndex
	baseTerm := persistent.Snapshot.LastIncludedTerm
	if baseIndex == 0 && baseTerm == 0 && persistent.Snapshot.Data.Entries == nil {
		baseIndex = -1
		baseTerm = -1
	}
	persistent.Snapshot.LastIncludedIndex = baseIndex
	persistent.Snapshot.LastIncludedTerm = baseTerm

	n := &Node{
		id:                  cfg.ID,
		peers:               peers,
		members:             members,
		transport:           cfg.Transport,
		storage:             cfg.Storage,
		currentTerm:         persistent.CurrentTerm,
		votedFor:            persistent.VotedFor,
		log:                 append([]LogEntry(nil), persistent.Log...),
		snapshot:            persistent.Snapshot,
		commitIndex:         baseIndex,
		lastApplied:         baseIndex,
		state:               Follower,
		nextIndex:           make(map[int]int),
		matchIndex:          make(map[int]int),
		electionMin:         cfg.ElectionMin,
		electionMax:         cfg.ElectionMax,
		heartbeatInterval:   cfg.HeartbeatInterval,
		leaderQuorumTimeout: cfg.LeaderQuorumTimeout,
		commitCh:            make(chan LogEntry, 256),
		snapshotCh:          make(chan InstalledSnapshot, 4),
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

func (n *Node) SnapshotChan() <-chan InstalledSnapshot {
	return n.snapshotCh
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
	index := n.lastLogIndexLocked()
	n.saveLocked()
	if len(n.peers) == 0 {
		n.commitIndex = index
		n.signalCommit()
	}
	return index, true
}

func (n *Node) SubmitConfigChange(change ConfigChange) (int, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state != Leader {
		return -1, false
	}
	if change.Type == "add" {
		if change.ID == n.id {
			return -1, false
		}
		if change.URL == "" {
			return -1, false
		}
	} else if change.Type != "remove" {
		return -1, false
	}
	n.log = append(n.log, LogEntry{ConfigChange: &change, Term: n.currentTerm})
	index := n.lastLogIndexLocked()
	n.saveLocked()
	if len(n.peers) == 0 {
		n.commitIndex = index
		n.signalCommit()
	}
	return index, true
}

func (n *Node) Members() []Member {
	n.mu.Lock()
	defer n.mu.Unlock()
	return membersList(n.members)
}

func (n *Node) Compact(snapshot store.Snapshot) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.lastApplied <= n.snapshot.LastIncludedIndex {
		return false
	}
	term, ok := n.termAtLocked(n.lastApplied)
	if !ok {
		return false
	}
	keepFrom := n.lastApplied + 1
	if keepFrom <= n.lastLogIndexLocked() {
		n.log = append([]LogEntry(nil), n.log[n.offsetLocked(keepFrom):]...)
	} else {
		n.log = nil
	}
	n.snapshot = SnapshotState{
		LastIncludedIndex: n.lastApplied,
		LastIncludedTerm:  term,
		Data:              snapshot,
	}
	n.saveLocked()
	return true
}

func (n *Node) ReadIndex(ctx context.Context) (int, bool) {
	n.mu.Lock()
	if n.state != Leader {
		n.mu.Unlock()
		return -1, false
	}
	savedTerm := n.currentTerm
	readIndex := n.commitIndex
	peers := append([]int(nil), n.peers...)
	if len(peers) == 0 {
		n.mu.Unlock()
		return readIndex, true
	}
	type heartbeat struct {
		peerID int
		args   AppendEntriesArgs
	}
	heartbeats := make([]heartbeat, 0, len(peers))
	for _, peerID := range peers {
		nextIndex := n.nextIndex[peerID]
		prevIndex := nextIndex - 1
		prevTerm, ok := n.termAtLocked(prevIndex)
		if !ok {
			prevTerm = -1
		}
		heartbeats = append(heartbeats, heartbeat{
			peerID: peerID,
			args: AppendEntriesArgs{
				Term:         savedTerm,
				LeaderID:     n.id,
				PrevLogIndex: prevIndex,
				PrevLogTerm:  prevTerm,
				LeaderCommit: n.commitIndex,
			},
		})
	}
	n.mu.Unlock()

	results := make(chan bool, len(heartbeats))
	for _, hb := range heartbeats {
		go func(hb heartbeat) {
			if n.transport == nil {
				results <- false
				return
			}
			reqCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			reply, err := n.transport.AppendEntries(reqCtx, hb.peerID, hb.args)
			if err != nil {
				results <- false
				return
			}
			n.mu.Lock()
			if reply.Term > n.currentTerm {
				n.becomeFollowerLocked(reply.Term)
				n.mu.Unlock()
				results <- false
				return
			}
			ok := n.state == Leader && n.currentTerm == savedTerm && reply.Success
			n.mu.Unlock()
			results <- ok
		}(hb)
	}

	acks := 1
	for range heartbeats {
		select {
		case <-ctx.Done():
			return -1, false
		case ok := <-results:
			if ok {
				acks++
				if n.hasMajority(acks) {
					return readIndex, true
				}
			}
		}
	}
	return -1, false
}

func (n *Node) Report() Report {
	n.mu.Lock()
	defer n.mu.Unlock()
	return Report{
		ID:            n.id,
		Term:          n.currentTerm,
		State:         n.state.String(),
		IsLeader:      n.state == Leader,
		CommitIndex:   n.commitIndex,
		LastApplied:   n.lastApplied,
		LogLength:     len(n.log),
		SnapshotIndex: n.snapshot.LastIncludedIndex,
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
		prevTerm, ok := n.termAtLocked(args.PrevLogIndex)
		if !ok || prevTerm != args.PrevLogTerm {
			return reply
		}
	}

	insertAt := args.PrevLogIndex + 1
	i := 0
	for insertAt <= n.lastLogIndexLocked() && i < len(args.Entries) {
		existing, ok := n.entryAtLocked(insertAt)
		if !ok || existing.Term != args.Entries[i].Term {
			break
		}
		insertAt++
		i++
	}
	if i < len(args.Entries) {
		if insertAt <= n.snapshot.LastIncludedIndex {
			return reply
		}
		prefix := append([]LogEntry(nil), n.log[:n.offsetLocked(insertAt)]...)
		n.log = append(prefix, args.Entries[i:]...)
		n.saveLocked()
	}
	if args.LeaderCommit > n.commitIndex {
		n.commitIndex = min(args.LeaderCommit, n.lastLogIndexLocked())
		n.signalCommit()
	}

	reply.Success = true
	reply.Term = n.currentTerm
	return reply
}

func (n *Node) InstallSnapshot(args InstallSnapshotArgs) InstallSnapshotReply {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state == Dead {
		return InstallSnapshotReply{Term: n.currentTerm}
	}
	if args.Term > n.currentTerm {
		n.becomeFollowerLocked(args.Term)
	}
	reply := InstallSnapshotReply{Term: n.currentTerm}
	if args.Term < n.currentTerm {
		return reply
	}
	if n.state != Follower {
		n.becomeFollowerLocked(args.Term)
	}
	n.electionReset = time.Now()
	if args.LastIncludedIndex <= n.snapshot.LastIncludedIndex {
		return reply
	}

	var retained []LogEntry
	if term, ok := n.termAtLocked(args.LastIncludedIndex); ok && term == args.LastIncludedTerm && args.LastIncludedIndex < n.lastLogIndexLocked() {
		retained = append([]LogEntry(nil), n.log[n.offsetLocked(args.LastIncludedIndex+1):]...)
	}
	n.log = retained
	n.snapshot = SnapshotState{
		LastIncludedIndex: args.LastIncludedIndex,
		LastIncludedTerm:  args.LastIncludedTerm,
		Data:              args.Snapshot,
	}
	if n.commitIndex < args.LastIncludedIndex {
		n.commitIndex = args.LastIncludedIndex
	}
	if n.lastApplied < args.LastIncludedIndex {
		n.lastApplied = args.LastIncludedIndex
	}
	n.saveLocked()
	n.signalSnapshotLocked(args.LastIncludedIndex, args.Snapshot)
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
	if _, ok := n.members[n.id]; !ok {
		return
	}
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
		n.nextIndex[peerID] = n.lastLogIndexLocked() + 1
		n.matchIndex[peerID] = n.snapshot.LastIncludedIndex
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
		if nextIndex <= n.snapshot.LastIncludedIndex {
			args := InstallSnapshotArgs{
				Term:              savedTerm,
				LeaderID:          n.id,
				LastIncludedIndex: n.snapshot.LastIncludedIndex,
				LastIncludedTerm:  n.snapshot.LastIncludedTerm,
				Snapshot:          n.snapshot.Data,
			}
			n.mu.Unlock()
			go n.sendInstallSnapshot(peerID, args)
			continue
		}
		prevIndex := nextIndex - 1
		prevTerm := -1
		if prevIndex >= 0 {
			if term, ok := n.termAtLocked(prevIndex); ok {
				prevTerm = term
			}
		}
		entries := append([]LogEntry(nil), n.log[n.offsetLocked(nextIndex):]...)
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
			} else if n.nextIndex[peerID] > n.snapshot.LastIncludedIndex+1 {
				n.nextIndex[peerID]--
			} else {
				n.nextIndex[peerID] = n.snapshot.LastIncludedIndex
			}
		}(peerID, nextIndex, args)
	}
}

func (n *Node) sendInstallSnapshot(peerID int, args InstallSnapshotArgs) {
	if n.transport == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reply, err := n.transport.InstallSnapshot(ctx, peerID, args)
	if err != nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if reply.Term > n.currentTerm {
		n.becomeFollowerLocked(reply.Term)
		return
	}
	if n.state != Leader || n.currentTerm != args.Term {
		return
	}
	n.nextIndex[peerID] = args.LastIncludedIndex + 1
	n.matchIndex[peerID] = args.LastIncludedIndex
}

func (n *Node) advanceCommitLocked() {
	oldCommit := n.commitIndex
	for i := n.commitIndex + 1; i <= n.lastLogIndexLocked(); i++ {
		term, ok := n.termAtLocked(i)
		if !ok || term != n.currentTerm {
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
				entry, ok := n.entryAtLocked(n.lastApplied)
				if !ok {
					n.mu.Unlock()
					break
				}
				entry.Index = n.lastApplied
				if entry.ConfigChange != nil {
					n.applyConfigChangeLocked(*entry.ConfigChange)
					n.saveLocked()
				}
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
		Snapshot:    n.snapshot,
		Members:     cloneMembers(n.members),
	})
}

func (n *Node) lastLogIndexAndTermLocked() (int, int) {
	idx := n.lastLogIndexLocked()
	term, _ := n.termAtLocked(idx)
	return idx, term
}

func (n *Node) lastLogIndexLocked() int {
	return n.snapshot.LastIncludedIndex + len(n.log)
}

func (n *Node) offsetLocked(index int) int {
	return index - n.snapshot.LastIncludedIndex - 1
}

func (n *Node) termAtLocked(index int) (int, bool) {
	if index == n.snapshot.LastIncludedIndex {
		return n.snapshot.LastIncludedTerm, true
	}
	if index < n.snapshot.LastIncludedIndex {
		return -1, false
	}
	offset := n.offsetLocked(index)
	if offset < 0 || offset >= len(n.log) {
		return -1, false
	}
	return n.log[offset].Term, true
}

func (n *Node) entryAtLocked(index int) (LogEntry, bool) {
	if index <= n.snapshot.LastIncludedIndex {
		return LogEntry{}, false
	}
	offset := n.offsetLocked(index)
	if offset < 0 || offset >= len(n.log) {
		return LogEntry{}, false
	}
	return n.log[offset], true
}

func (n *Node) applyConfigChangeLocked(change ConfigChange) {
	if n.members == nil {
		n.members = map[int]string{n.id: ""}
	}
	switch change.Type {
	case "add":
		n.members[change.ID] = change.URL
		if updater, ok := n.transport.(PeerURLUpdater); ok && change.ID != n.id {
			updater.SetPeerURL(change.ID, change.URL)
		}
	case "remove":
		delete(n.members, change.ID)
		if updater, ok := n.transport.(PeerURLUpdater); ok && change.ID != n.id {
			updater.RemovePeerURL(change.ID)
		}
	}
	n.peers = peersFromMembers(n.id, n.members)
	if change.Type == "remove" && change.ID == n.id {
		n.state = Follower
		n.nextIndex = make(map[int]int)
		n.matchIndex = make(map[int]int)
		return
	}
	if n.state == Leader {
		for _, peerID := range n.peers {
			if _, ok := n.nextIndex[peerID]; !ok {
				n.nextIndex[peerID] = n.lastLogIndexLocked() + 1
				n.matchIndex[peerID] = n.snapshot.LastIncludedIndex
			}
		}
		for peerID := range n.nextIndex {
			if _, ok := n.members[peerID]; !ok || peerID == n.id {
				delete(n.nextIndex, peerID)
				delete(n.matchIndex, peerID)
			}
		}
	}
}

func (n *Node) signalSnapshotLocked(index int, snapshot store.Snapshot) {
	installed := InstalledSnapshot{LastIncludedIndex: index, Snapshot: snapshot}
	select {
	case n.snapshotCh <- installed:
	default:
		go func() {
			select {
			case n.snapshotCh <- installed:
			case <-n.done:
			}
		}()
	}
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

func cloneMembers(members map[int]string) map[int]string {
	if len(members) == 0 {
		return nil
	}
	next := make(map[int]string, len(members))
	for id, url := range members {
		next[id] = url
	}
	return next
}

func peersFromMembers(selfID int, members map[int]string) []int {
	peers := make([]int, 0, len(members))
	for id := range members {
		if id != selfID {
			peers = append(peers, id)
		}
	}
	sort.Ints(peers)
	return peers
}

func membersList(members map[int]string) []Member {
	out := make([]Member, 0, len(members))
	for id, url := range members {
		out = append(out, Member{ID: id, URL: url})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

var ErrNoTransport = errors.New("raft transport is not configured")
