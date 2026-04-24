## implements the consensus module , the heart of the Raft algorithm

from __future__ import annotations

from .server import Server
from .log import LogEntry, log
from .consts import CMState, DEBUG_CM
import time
import threading
import os
import random


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

    def dlog(self, fmt: str, *args: object) -> None:
        if DEBUG_CM > 0:
            log.debug(f"[{self.id}] " + fmt, *args)

    # public APIs

    def report(self) -> tuple[int, int, int]:
        """
        returns (id, current_term,is_leader)
        """
        with self.mu:
            return self.id, self.current_term, self.state == CMState.Leader

    def stop(self):
        """
        mark this CM as dead.
        returns quickly
        """
        with self.mu:
            self.state = CMState.Dead
            self.dlog("becomes Dead")

    # RPC handlers

    # election timer

    def _election_timeout(self) -> float:
        """
        return a randomized election timeout in seconds.
        """
        if os.environ.get("RAFT_FORCE_MORE_REELECTION") and random.randint(0, 2) == 0:
            # force collisions for stress testing
            return 0.150
        # other delay : 150-299 ms
        return (150 + random.randint(0, 149)) / 1000

    def run_election_timer(self) -> None:
        """
        blocking election timer
        must be launched in its own thread.
        it will exit when the CM changes state or its term changes, or
        it fires startElection when the timeout elapses without a heartbeat.s
        """

        timeout = self._election_timeout()
        with self.mu:
            term_started = self.current_term

        self.dlog("election timer started {.3f}")
        while True:
            time.sleep(0.010)  # poll every 10 ms (mirrors Go's 10 ms ticker)

            with self.mu:
                if self.state not in (CMState.Follower, CMState.Candidate):
                    self.dlog("in election timer state=%s, bailing out", self.state)
                    return

                if term_started != self.current_term:
                    self.dlog(
                        "in election timer term changed from %d to %d, bailing out",
                        term_started,
                        self.current_term,
                    )
                    return

                if time.monotonic() - self.election_reset_event >= timeout:
                    self._start_election()
                    return
