package raft

import "github.com/sagnikc395/kv-store/internal/store"

type State int

const (
	Follower State = iota
	Candidate
	Leader
	Dead
)

func (s State) String() string {
	switch s {
	case Follower:
		return "follower"
	case Candidate:
		return "candidate"
	case Leader:
		return "leader"
	case Dead:
		return "dead"
	default:
		return "unknown"
	}
}

type LogEntry struct {
	Command      store.Command `json:"command,omitempty"`
	ConfigChange *ConfigChange `json:"config_change,omitempty"`
	Term         int           `json:"term"`
	Index        int           `json:"-"`
}

type ConfigChange struct {
	Type string `json:"type"`
	ID   int    `json:"id"`
	URL  string `json:"url,omitempty"`
}

type Member struct {
	ID  int    `json:"id"`
	URL string `json:"url"`
}

type SnapshotState struct {
	LastIncludedIndex int            `json:"last_included_index"`
	LastIncludedTerm  int            `json:"last_included_term"`
	Data              store.Snapshot `json:"data"`
}

type RequestVoteArgs struct {
	Term         int `json:"term"`
	CandidateID  int `json:"candidate_id"`
	LastLogIndex int `json:"last_log_index"`
	LastLogTerm  int `json:"last_log_term"`
}

type RequestVoteReply struct {
	Term        int  `json:"term"`
	VoteGranted bool `json:"vote_granted"`
}

type AppendEntriesArgs struct {
	Term         int        `json:"term"`
	LeaderID     int        `json:"leader_id"`
	PrevLogIndex int        `json:"prev_log_index"`
	PrevLogTerm  int        `json:"prev_log_term"`
	Entries      []LogEntry `json:"entries"`
	LeaderCommit int        `json:"leader_commit"`
}

type AppendEntriesReply struct {
	Term    int  `json:"term"`
	Success bool `json:"success"`
}

type InstallSnapshotArgs struct {
	Term              int            `json:"term"`
	LeaderID          int            `json:"leader_id"`
	LastIncludedIndex int            `json:"last_included_index"`
	LastIncludedTerm  int            `json:"last_included_term"`
	Snapshot          store.Snapshot `json:"snapshot"`
}

type InstallSnapshotReply struct {
	Term int `json:"term"`
}
