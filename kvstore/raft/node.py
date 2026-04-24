## implements the consensus module , the heart of the Raft algorithm

from __future__ import annotations

from .server import Server
from .log import LogEntry
from .consts import CMState
import time
import threading


from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .server import Server


class ConsensusModule:
    def __init__(
        self, id: int, peer_ids: list[int], server: Server, ready: threading.Event
    ) -> None:
        self.mu = threading.Lock()
        self.id = id
        self.peer_ids = peer_ids
        self.server = server

        # persistent Raft state on all servers
        self.current_term: int = 0
        self.voted_for: int = -1
        self.log: list[LogEntry] = []

        # volatile Raft state on all servers
        self.state: CMState = CMState.Follower
        self.election_reset_event: float = 0.0

        def _start(ready: threading.Event) -> None:
            ready.wait()
            with self.mu:
                self.election_reset_event = time.time()
            self.run_election_timer()

        t = threading.Thread(target=_start, args=(ready,), daemon=True)
        t.start()

    def run_election_timer(self) -> None:
        raise NotImplementedError
