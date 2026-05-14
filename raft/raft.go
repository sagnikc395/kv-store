package raft

import (
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/sagnikc395/kv-store/store"
)

type ConsensusModule struct {
	mu         sync.Mutex
	id         int
	peerIDs    []int
	server     *Server
	commitChan chan<- LogEntry

	currentTerm int
	votedFor    int
	log         []LogEntry

	commitIndex        int
	lastApplied        int
	state              State
	electionResetEvent time.Time

	nextIndex map[int]int
	matchIndex map[int]int

	leaderQuorumSeenAt time.Time
	leaderQuorumTimeout time.Duration

	newCommitEvent chan struct{}
	stopCh         chan struct{}
	stopOnce       sync.Once
}

func NewConsensusModule(id int, peerIDs []int, server *Server, ready <-chan struct{}, commitChan chan<- LogEntry) *ConsensusModule {
	cm := &ConsensusModule{
		id:             id,
		peerIDs:        append([]int(nil), peerIDs...),
		server:         server,
		commitChan:     commitChan,
		votedFor:       -1,
		commitIndex:    -1,
		lastApplied:    -1,
		state:          Follower,
		nextIndex:      make(map[int]int),
		matchIndex:     make(map[int]int),
		newCommitEvent: make(chan struct{}, 1),
		stopCh:         make(chan struct{}),
	}

	go func() {
		<-ready
		cm.mu.Lock()
		cm.electionResetEvent = time.Now()
		cm.mu.Unlock()
		cm.runElectionTimer()
	}()
	go cm.runCommitNotifier()

	return cm
}

func (cm *ConsensusModule) Report() (int, int, bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.id, cm.currentTerm, cm.state == Leader
}

func (cm *ConsensusModule) State() State {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.state
}

func (cm *ConsensusModule) Submit(command store.Command) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.state != Leader {
		return false
	}
	cm.log = append(cm.log, LogEntry{Command: command, Term: cm.currentTerm})
	return true
}

func (cm *ConsensusModule) Stop() {
	cm.stopOnce.Do(func() {
		cm.mu.Lock()
		cm.state = Dead
		cm.mu.Unlock()
		close(cm.stopCh)
		cm.signalCommit()
	})
}

func (cm *ConsensusModule) RequestVote(args RequestVoteArgs, reply *RequestVoteReply) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.state == Dead {
		return
	}

	lastLogIndex, lastLogTerm := cm.lastLogIndexAndTerm()
	if args.Term > cm.currentTerm {
		cm.becomeFollower(args.Term)
	}

	reply.VoteGranted = false
	if cm.currentTerm == args.Term && (cm.votedFor == -1 || cm.votedFor == args.CandidateID) {
		candidateUpToDate := args.LastLogTerm > lastLogTerm ||
			(args.LastLogTerm == lastLogTerm && args.LastLogIndex >= lastLogIndex)
		if candidateUpToDate {
			reply.VoteGranted = true
			cm.votedFor = args.CandidateID
			cm.electionResetEvent = time.Now()
		}
	}
	reply.Term = cm.currentTerm
}

func (cm *ConsensusModule) AppendEntries(args AppendEntriesArgs, reply *AppendEntriesReply) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.state == Dead {
		return
	}

	if args.Term > cm.currentTerm {
		cm.becomeFollower(args.Term)
	}

	reply.Success = false
	if args.Term == cm.currentTerm {
		if cm.state != Follower {
			cm.becomeFollower(args.Term)
		}
		cm.electionResetEvent = time.Now()

		if args.PrevLogIndex == -1 ||
			(args.PrevLogIndex < len(cm.log) && cm.log[args.PrevLogIndex].Term == args.PrevLogTerm) {
			reply.Success = true

			logInsertIndex := args.PrevLogIndex + 1
			newEntriesIndex := 0
			for logInsertIndex < len(cm.log) && newEntriesIndex < len(args.Entries) {
				if cm.log[logInsertIndex].Term != args.Entries[newEntriesIndex].Term {
					break
				}
				logInsertIndex++
				newEntriesIndex++
			}
			if newEntriesIndex < len(args.Entries) {
				newLog := append([]LogEntry(nil), cm.log[:logInsertIndex]...)
				newLog = append(newLog, args.Entries[newEntriesIndex:]...)
				cm.log = newLog
			}

			if args.LeaderCommit > cm.commitIndex {
				cm.commitIndex = min(args.LeaderCommit, len(cm.log)-1)
				cm.signalCommit()
			}
		}
	}
	reply.Term = cm.currentTerm
}

func (cm *ConsensusModule) electionTimeout() time.Duration {
	if os.Getenv("RAFT_FORCE_MORE_REELECTION") != "" && rand.Intn(3) == 0 {
		return 150 * time.Millisecond
	}
	return time.Duration(300+rand.Intn(200)) * time.Millisecond
}

func (cm *ConsensusModule) runElectionTimer() {
	timeout := cm.electionTimeout()

	cm.mu.Lock()
	termStarted := cm.currentTerm
	cm.mu.Unlock()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-cm.stopCh:
			return
		case <-ticker.C:
			cm.mu.Lock()
			if cm.state != Follower && cm.state != Candidate {
				cm.mu.Unlock()
				return
			}
			if termStarted != cm.currentTerm {
				cm.mu.Unlock()
				return
			}
			if time.Since(cm.electionResetEvent) >= timeout {
				cm.startElection()
				cm.mu.Unlock()
				return
			}
			cm.mu.Unlock()
		}
	}
}

func (cm *ConsensusModule) startElection() {
	cm.state = Candidate
	cm.currentTerm++
	savedTerm := cm.currentTerm
	cm.electionResetEvent = time.Now()
	cm.votedFor = cm.id

	votesReceived := 1
	lastLogIndex, lastLogTerm := cm.lastLogIndexAndTerm()
	if votesReceived*2 > len(cm.peerIDs)+1 {
		cm.startLeader()
		return
	}

	for _, peerID := range cm.peerIDs {
		peerID := peerID
		go func() {
			args := RequestVoteArgs{
				Term:         savedTerm,
				CandidateID:  cm.id,
				LastLogIndex: lastLogIndex,
				LastLogTerm:  lastLogTerm,
			}
			var reply RequestVoteReply
			if err := cm.server.Call(peerID, "RequestVote", args, &reply); err != nil {
				return
			}

			cm.mu.Lock()
			defer cm.mu.Unlock()

			if cm.state != Candidate {
				return
			}
			if reply.Term > savedTerm {
				cm.becomeFollower(reply.Term)
				return
			}
			if reply.Term == savedTerm && reply.VoteGranted {
				votesReceived++
				if votesReceived*2 > len(cm.peerIDs)+1 {
					cm.startLeader()
				}
			}
		}()
	}

	go cm.runElectionTimer()
}

func (cm *ConsensusModule) becomeFollower(term int) {
	if term > cm.currentTerm {
		cm.votedFor = -1
	}
	cm.state = Follower
	cm.currentTerm = term
	cm.electionResetEvent = time.Now()
	go cm.runElectionTimer()
}

func (cm *ConsensusModule) startLeader() {
	cm.state = Leader
	cm.nextIndex = make(map[int]int, len(cm.peerIDs))
	cm.matchIndex = make(map[int]int, len(cm.peerIDs))
	for _, pid := range cm.peerIDs {
		cm.nextIndex[pid] = len(cm.log)
		cm.matchIndex[pid] = -1
	}
	cm.leaderQuorumSeenAt = time.Now()
	cm.leaderQuorumTimeout = minDuration(cm.electionTimeout(), 125*time.Millisecond)

	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		for {
			cm.leaderSendAppendEntries()
			select {
			case <-cm.stopCh:
				return
			case <-ticker.C:
			}

			cm.mu.Lock()
			if cm.state == Leader && time.Since(cm.leaderQuorumSeenAt) >= cm.leaderQuorumTimeout {
				cm.becomeFollower(cm.currentTerm)
				cm.mu.Unlock()
				return
			}
			if cm.state != Leader {
				cm.mu.Unlock()
				return
			}
			cm.mu.Unlock()
		}
	}()
}

func (cm *ConsensusModule) leaderSendAppendEntries() {
	cm.mu.Lock()
	if cm.state != Leader {
		cm.mu.Unlock()
		return
	}
	savedTerm := cm.currentTerm
	roundState := &appendRound{successes: 1, quorumRecorded: len(cm.peerIDs) == 0}
	if roundState.quorumRecorded {
		cm.leaderQuorumSeenAt = time.Now()
	}
	cm.mu.Unlock()

	for _, peerID := range cm.peerIDs {
		peerID := peerID
		cm.mu.Lock()
		ni := cm.nextIndex[peerID]
		prevLogIndex := ni - 1
		prevLogTerm := -1
		if prevLogIndex >= 0 {
			prevLogTerm = cm.log[prevLogIndex].Term
		}
		entries := append([]LogEntry(nil), cm.log[ni:]...)
		leaderCommit := cm.commitIndex
		cm.mu.Unlock()

		args := AppendEntriesArgs{
			Term:         savedTerm,
			LeaderID:     cm.id,
			PrevLogIndex: prevLogIndex,
			PrevLogTerm:  prevLogTerm,
			Entries:      entries,
			LeaderCommit: leaderCommit,
		}

		go func() {
			var reply AppendEntriesReply
			if err := cm.server.Call(peerID, "AppendEntries", args, &reply); err != nil {
				return
			}

			cm.mu.Lock()
			defer cm.mu.Unlock()

			if reply.Term > savedTerm {
				cm.becomeFollower(reply.Term)
				return
			}
			if cm.state != Leader || savedTerm != reply.Term {
				return
			}

			if reply.Success {
				cm.nextIndex[peerID] = ni + len(args.Entries)
				cm.matchIndex[peerID] = cm.nextIndex[peerID] - 1
				roundState.successes++
				if !roundState.quorumRecorded && roundState.successes*2 > len(cm.peerIDs)+1 {
					cm.leaderQuorumSeenAt = time.Now()
					roundState.quorumRecorded = true
				}

				oldCommitIndex := cm.commitIndex
				for i := cm.commitIndex + 1; i < len(cm.log); i++ {
					if cm.log[i].Term != cm.currentTerm {
						continue
					}
					matchCount := 1
					for _, pid := range cm.peerIDs {
						if cm.matchIndex[pid] >= i {
							matchCount++
						}
					}
					if matchCount*2 > len(cm.peerIDs)+1 {
						cm.commitIndex = i
					}
				}
				if cm.commitIndex != oldCommitIndex {
					cm.signalCommit()
				}
			} else {
				cm.nextIndex[peerID] = max(ni-1, 0)
			}
		}()
	}
}

type appendRound struct {
	successes      int
	quorumRecorded bool
}

func (cm *ConsensusModule) runCommitNotifier() {
	for {
		select {
		case <-cm.stopCh:
			return
		case <-cm.newCommitEvent:
			cm.mu.Lock()
			if cm.state == Dead {
				cm.mu.Unlock()
				return
			}
			entries := append([]LogEntry(nil), cm.log[cm.lastApplied+1:cm.commitIndex+1]...)
			cm.lastApplied = cm.commitIndex
			cm.mu.Unlock()

			for _, entry := range entries {
				select {
				case cm.commitChan <- entry:
				case <-cm.stopCh:
					return
				}
			}
		}
	}
}

func (cm *ConsensusModule) signalCommit() {
	select {
	case cm.newCommitEvent <- struct{}{}:
	default:
	}
}

func (cm *ConsensusModule) lastLogIndexAndTerm() (int, int) {
	if len(cm.log) == 0 {
		return -1, -1
	}
	idx := len(cm.log) - 1
	return idx, cm.log[idx].Term
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
