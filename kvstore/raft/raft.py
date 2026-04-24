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

    def request_vote(self, args: RequestVoteArgs, reply: RequestVoteReply) -> None:
        with self.mu:
            if self.state == CMState.Dead:
                return

            self.dlog(
                "RequestVote: %s [current_term=%d, voted_for=%d]",
                args,
                self.current_term,
                self.voted_for,
            )

            if args.term > self.current_term:
                self.dlog("... term out of date in RequestVote")
                self._become_follower(args.term)

            if self.current_term == args.term and (
                self.voted_for == -1 or self.voted_for == args.candidate_id
            ):
                reply.vote_granted = True
                self.voted_for = args.candidate_id
                self.election_reset_event = time.monotonic()
            else:
                reply.vote_granted = False

            reply.term = self.current_term
            self.dlog("... RequestVote reply: %s", reply)

    def append_entries(
        self, args: AppendEntriesArgs, reply: AppendEntriesReply
    ) -> None:
        with self.mu:
            if self.state == CMState.Dead:
                return

            self.dlog("AppendEntries: %s", args)

            if args.term > self.current_term:
                self.dlog("... term out of date in AppendEntries")
                self._become_follower(args.term)

            reply.success = False
            if args.term == self.current_term:
                if self.state != CMState.Follower:
                    self._become_follower(args.term)
                self.election_reset_event = time.monotonic()
                reply.success = True

            reply.term = self.current_term
            self.dlog("AppendEntries reply: %s", reply)

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

    def _start_election(self):
        """
        Transition to Candidate and send RequestVote RPCs.
        Expects mu hold
        """
        self.state = CMState.Candidate
        self.current_term += 1
        saved_term = self.current_term
        self.election_reset_event = time.monotonic()
        self.voted_for = self.id
        self.dlog("becomes Candidate (current_term=%d); log=%s", saved_term, self.log)

        votes_received = 1

        for peer_id in self.peer_ids:

            def _send_vote(peer_id: int = peer_id) -> None:
                args = RequestVoteArgs(
                    term=saved_term,
                    candidate_id=self.id,
                )
                self.dlog("sending RequestVote to %d: %s", peer_id, args)
                try:
                    reply_data = self.server.call(peer_id, "RequestVote", args.__dict__)
                    reply = RequestVoteReply(**reply_data)
                except Exception:
                    return

                nonlocal votes_received
                with self.mu:
                    self.dlog("received RequestVoteReply %s", reply)

                    if self.state != CMState.Candidate:
                        self.dlog("while waiting for reply, state=%s", self.state)
                        return

                    if reply.term > saved_term:
                        self.dlog("term out of date in RequestVoteReply")
                        self._become_follower(reply.term)
                        return

                    if reply.term == saved_term and reply.vote_granted:
                        votes_received += 1
                        if votes_received * 2 > len(self.peer_ids) + 1:
                            self.dlog("wins election with %d votes", votes_received)
                            self._start_leader()

            threading.Thread(target=_send_vote, daemon=True).start()

        # Fallback timer in case this election fails.
        threading.Thread(target=self.run_election_timer, daemon=True).start()

    # ── state transitions ────────────────────

    def _become_follower(self, term: int) -> None:
        """Revert to Follower. Expects mu held."""
        self.dlog("becomes Follower with term=%d; log=%s", term, self.log)
        self.state = CMState.Follower
        self.current_term = term
        self.voted_for = -1
        self.election_reset_event = time.monotonic()

        threading.Thread(target=self.run_election_timer, daemon=True).start()

    def _start_leader(self) -> None:
        """Become Leader and start sending heartbeats. Expects mu held."""
        self.state = CMState.Leader
        self.dlog("becomes Leader; term=%d, log=%s", self.current_term, self.log)

        def _heartbeat_loop() -> None:
            while True:
                self._leader_send_heartbeats()
                time.sleep(0.050)  # 50 ms heartbeat interval

                with self.mu:
                    if self.state != CMState.Leader:
                        return

        threading.Thread(target=_heartbeat_loop, daemon=True).start()

    # heartbeats

    def _leader_send_heartbeats(self) -> None:
        # send AppendEntries (heartbeat) to all peers and process replies.
        with self.mu:
            if self.state != CMState.Leader:
                return
            saved_term = self.current_term

        for peer_id in self.peer_ids:
            args = AppendEntriesArgs(term=saved_term, leader_id=self.id)

            def _send(peer_id: int = peer_id, args: AppendEntriesArgs = args) -> None:
                self.dlog("sending AppendEntries to %d: ni=0, args=%s", peer_id, args)
                try:
                    reply_data = self.server.call(
                        peer_id, "AppendEntries", args.__dict__
                    )
                    reply = AppendEntriesReply(**reply_data)
                except Exception:
                    return

                with self.mu:
                    if reply.term > saved_term:
                        self.dlog("term out of date in heartbeat reply")
                        self._become_follower(reply.term)

            threading.Thread(target=_send, daemon=True).start()
