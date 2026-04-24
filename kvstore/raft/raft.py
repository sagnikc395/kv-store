"""Raft consensus module — leader election + log replication."""

from __future__ import annotations

import os
import queue
import random
import threading
import time
from typing import TYPE_CHECKING

from .consts import CMState, DEBUG_CM
from .log import LogEntry, log
from .types import (
    AppendEntriesArgs,
    AppendEntriesReply,
    RequestVoteArgs,
    RequestVoteReply,
)

if TYPE_CHECKING:
    from .server import Server


class ConsensusModule:
    def __init__(
        self,
        id: int,
        peer_ids: list[int],
        server: Server,
        ready: threading.Event,
        commit_chan: queue.Queue,
    ) -> None:
        self.mu = threading.Lock()
        self.id = id
        self.peer_ids = peer_ids
        self.server = server
        self.commit_chan = commit_chan

        # persistent state
        self.current_term: int = 0
        self.voted_for: int = -1
        self.log: list[LogEntry] = []

        # volatile state — all servers
        self.commit_index: int = -1
        self.last_applied: int = -1
        self.state: CMState = CMState.Follower
        self.election_reset_event: float = 0.0

        # volatile state — leader only (reset on each election win)
        self.next_index: dict[int, int] = {}
        self.match_index: dict[int, int] = {}

        # signals the commit-notifier thread
        self._new_commit_event = threading.Event()

        def _start(ready: threading.Event) -> None:
            ready.wait()
            with self.mu:
                self.election_reset_event = time.monotonic()
            self.run_election_timer()

        threading.Thread(target=_start, args=(ready,), daemon=True).start()
        threading.Thread(target=self._run_commit_notifier, daemon=True).start()

    def dlog(self, fmt: str, *args: object) -> None:
        if DEBUG_CM > 0:
            log.debug(f"[{self.id}] " + fmt, *args)

    # ── public API ───────────────────────────────

    def report(self) -> tuple[int, int, bool]:
        """Return (id, current_term, is_leader)."""
        with self.mu:
            return self.id, self.current_term, self.state == CMState.Leader

    def submit(self, command) -> bool:
        """
        Append a command to the leader's log.
        Returns True if this server accepted the command as leader.
        The command will appear on commit_chan once a quorum commits it.
        """
        with self.mu:
            if self.state != CMState.Leader:
                return False
            self.log.append(LogEntry(command=command, term=self.current_term))
            self.dlog("submit %s; log index=%d", command, len(self.log) - 1)
            return True

    def stop(self) -> None:
        with self.mu:
            self.state = CMState.Dead
            self.dlog("becomes Dead")
        self._new_commit_event.set()

    # ── RPC handlers ─────────────────────────────

    def request_vote(self, args: RequestVoteArgs, reply: RequestVoteReply) -> None:
        with self.mu:
            if self.state == CMState.Dead:
                return

            last_log_index, last_log_term = self._last_log_index_and_term()
            self.dlog(
                "RequestVote: %s [current_term=%d, voted_for=%d]",
                args, self.current_term, self.voted_for,
            )

            if args.term > self.current_term:
                self.dlog("... term out of date in RequestVote")
                self._become_follower(args.term)

            if self.current_term == args.term and (
                self.voted_for == -1 or self.voted_for == args.candidate_id
            ):
                # §5.4.1 — grant vote only if candidate log is at least as up-to-date
                candidate_up_to_date = (
                    args.last_log_term > last_log_term
                    or (args.last_log_term == last_log_term and args.last_log_index >= last_log_index)
                )
                if candidate_up_to_date:
                    reply.vote_granted = True
                    self.voted_for = args.candidate_id
                    self.election_reset_event = time.monotonic()
                else:
                    reply.vote_granted = False
            else:
                reply.vote_granted = False

            reply.term = self.current_term
            self.dlog("... RequestVote reply: %s", reply)

    def append_entries(self, args: AppendEntriesArgs, reply: AppendEntriesReply) -> None:
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

                # Log consistency check (§5.3)
                if args.prev_log_index == -1 or (
                    args.prev_log_index < len(self.log)
                    and self.log[args.prev_log_index].term == args.prev_log_term
                ):
                    reply.success = True

                    entries = [LogEntry.from_dict(e) for e in args.entries]
                    log_insert_index = args.prev_log_index + 1
                    new_entries_index = 0

                    # Skip over already-matching entries
                    while (
                        log_insert_index < len(self.log)
                        and new_entries_index < len(entries)
                    ):
                        if self.log[log_insert_index].term != entries[new_entries_index].term:
                            break
                        log_insert_index += 1
                        new_entries_index += 1

                    # Overwrite / append remaining entries
                    if new_entries_index < len(entries):
                        self.log = (
                            self.log[:log_insert_index] + entries[new_entries_index:]
                        )

                    if args.leader_commit > self.commit_index:
                        self.commit_index = min(args.leader_commit, len(self.log) - 1)
                        self.dlog("... follower commit_index=%d", self.commit_index)
                        self._new_commit_event.set()

            reply.term = self.current_term
            self.dlog("AppendEntries reply: %s", reply)

    # ── election timer ───────────────────────────

    def _election_timeout(self) -> float:
        if os.environ.get("RAFT_FORCE_MORE_REELECTION") and random.randint(0, 2) == 0:
            return 0.150
        return (150 + random.randint(0, 149)) / 1000

    def run_election_timer(self) -> None:
        timeout = self._election_timeout()
        with self.mu:
            term_started = self.current_term

        self.dlog("election timer started (%.3f s)", timeout)
        while True:
            time.sleep(0.010)

            with self.mu:
                if self.state not in (CMState.Follower, CMState.Candidate):
                    self.dlog("election timer: state=%s, bailing out", self.state)
                    return
                if term_started != self.current_term:
                    self.dlog(
                        "election timer: term changed %d→%d, bailing out",
                        term_started, self.current_term,
                    )
                    return
                if time.monotonic() - self.election_reset_event >= timeout:
                    self._start_election()
                    return

    def _start_election(self) -> None:
        """Transition to Candidate and send RequestVote RPCs. Expects mu held."""
        self.state = CMState.Candidate
        self.current_term += 1
        saved_term = self.current_term
        self.election_reset_event = time.monotonic()
        self.voted_for = self.id
        self.dlog("becomes Candidate (current_term=%d); log=%s", saved_term, self.log)

        votes_received = 1
        last_log_index, last_log_term = self._last_log_index_and_term()

        for peer_id in self.peer_ids:
            def _send_vote(peer_id: int = peer_id) -> None:
                args = RequestVoteArgs(
                    term=saved_term,
                    candidate_id=self.id,
                    last_log_index=last_log_index,
                    last_log_term=last_log_term,
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

        threading.Thread(target=self.run_election_timer, daemon=True).start()

    # ── state transitions ────────────────────────

    def _become_follower(self, term: int) -> None:
        """Revert to Follower. Expects mu held."""
        self.dlog("becomes Follower with term=%d; log=%s", term, self.log)
        self.state = CMState.Follower
        self.current_term = term
        self.voted_for = -1
        self.election_reset_event = time.monotonic()
        threading.Thread(target=self.run_election_timer, daemon=True).start()

    def _start_leader(self) -> None:
        """Become Leader. Expects mu held."""
        self.state = CMState.Leader
        self.next_index = {pid: len(self.log) for pid in self.peer_ids}
        self.match_index = {pid: -1 for pid in self.peer_ids}
        self.dlog(
            "becomes Leader; term=%d, next_index=%s, match_index=%s",
            self.current_term, self.next_index, self.match_index,
        )

        def _heartbeat_loop() -> None:
            while True:
                self._leader_send_append_entries()
                time.sleep(0.050)
                with self.mu:
                    if self.state != CMState.Leader:
                        return

        threading.Thread(target=_heartbeat_loop, daemon=True).start()

    # ── log replication ──────────────────────────

    def _leader_send_append_entries(self) -> None:
        with self.mu:
            if self.state != CMState.Leader:
                return
            saved_current_term = self.current_term

        for peer_id in self.peer_ids:
            with self.mu:
                ni = self.next_index[peer_id]
                prev_log_index = ni - 1
                prev_log_term = -1
                if prev_log_index >= 0:
                    prev_log_term = self.log[prev_log_index].term
                entries = [e.to_dict() for e in self.log[ni:]]
                leader_commit = self.commit_index

            args = AppendEntriesArgs(
                term=saved_current_term,
                leader_id=self.id,
                prev_log_index=prev_log_index,
                prev_log_term=prev_log_term,
                entries=entries,
                leader_commit=leader_commit,
            )

            def _send(
                peer_id: int = peer_id,
                ni: int = ni,
                args: AppendEntriesArgs = args,
            ) -> None:
                self.dlog("sending AppendEntries to %d: ni=%d, args=%s", peer_id, ni, args)
                try:
                    reply_data = self.server.call(peer_id, "AppendEntries", args.__dict__)
                    reply = AppendEntriesReply(**reply_data)
                except Exception:
                    return

                with self.mu:
                    if reply.term > saved_current_term:
                        self.dlog("term out of date in AppendEntries reply")
                        self._become_follower(reply.term)
                        return

                    if self.state == CMState.Leader and saved_current_term == reply.term:
                        if reply.success:
                            self.next_index[peer_id] = ni + len(args.entries)
                            self.match_index[peer_id] = self.next_index[peer_id] - 1
                            self.dlog(
                                "AppendEntries reply from %d success: ni=%d mi=%d",
                                peer_id, self.next_index[peer_id], self.match_index[peer_id],
                            )
                            # Advance commit_index if a majority has replicated entry i
                            saved_commit_index = self.commit_index
                            for i in range(self.commit_index + 1, len(self.log)):
                                if self.log[i].term == self.current_term:
                                    match_count = 1
                                    for pid in self.peer_ids:
                                        if self.match_index[pid] >= i:
                                            match_count += 1
                                    if match_count * 2 > len(self.peer_ids) + 1:
                                        self.commit_index = i
                            if self.commit_index != saved_commit_index:
                                self.dlog("leader commit_index=%d", self.commit_index)
                                self._new_commit_event.set()
                        else:
                            self.next_index[peer_id] = max(ni - 1, 0)
                            self.dlog(
                                "AppendEntries reply from %d failed: ni=%d",
                                peer_id, self.next_index[peer_id],
                            )

            threading.Thread(target=_send, daemon=True).start()

    # ── commit notifier ──────────────────────────

    def _run_commit_notifier(self) -> None:
        """Push newly committed entries onto commit_chan."""
        while True:
            self._new_commit_event.wait()
            self._new_commit_event.clear()

            with self.mu:
                if self.state == CMState.Dead:
                    return
                entries = self.log[self.last_applied + 1 : self.commit_index + 1]
                self.last_applied = self.commit_index

            self.dlog("commit_notifier: sending %d entries", len(entries))
            for entry in entries:
                self.commit_chan.put(entry)

    # ── helpers ──────────────────────────────────

    def _last_log_index_and_term(self) -> tuple[int, int]:
        """Return (last_log_index, last_log_term). Expects mu held."""
        if self.log:
            idx = len(self.log) - 1
            return idx, self.log[idx].term
        return -1, -1
