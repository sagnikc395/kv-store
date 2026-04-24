from dataclasses import dataclass, field


@dataclass
class RequestVoteArgs:
    term: int = 0
    candidate_id: int = 0
    last_log_index: int = -1
    last_log_term: int = -1


@dataclass
class RequestVoteReply:
    term: int = 0
    vote_granted: bool = False


@dataclass
class AppendEntriesArgs:
    term: int = 0
    leader_id: int = 0
    prev_log_index: int = -1
    prev_log_term: int = -1
    entries: list = field(default_factory=list)  # list[dict] for XML-RPC serialization
    leader_commit: int = -1


@dataclass
class AppendEntriesReply:
    term: int = 0
    success: bool = False
