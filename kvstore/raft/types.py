# defines the RPC arguments and reply types

from dataclasses import dataclass, field
from .log import LogEntry


@dataclass
class RequestVoteArgs:
    term: int = 0
    candidate_id: int = 0
    last_log_index: int = 0
    last_log_term: int = 0


@dataclass
class RequestVoteReply:
    term: int = 0
    vote_granted: bool = False


@dataclass
class AppendEntriesArgs:
    term: int = 0
    leader_id: int = 0
    prev_log_index: int = 0
    prev_log_term: int = 0
    entries: list[LogEntry] = field(default_factory=list)
    leader_commit: int = 0


@dataclass
class AppendEntriesReply:
    term: int = 0
    success: bool = False
