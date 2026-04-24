"""Raft consensus tests: election and log replication."""

import queue
import threading
import time

import pytest

from kvstore.raft.log import LogEntry
from kvstore.raft.server import Server


# ─────────────────────────────────────────────
# Test harness
# ─────────────────────────────────────────────


class Harness:
    """
    Spins up a cluster of n Raft servers, wires them together, and provides
    helpers for network control and assertions.
    """

    def __init__(self, n: int) -> None:
        self.n = n
        self.cluster: list[Server] = []
        self.connected: list[bool] = [True] * n

        self._mu = threading.Lock()
        self._committed: dict[int, list[LogEntry]] = {i: [] for i in range(n)}
        self._shutdown_event = threading.Event()

        ready = threading.Event()
        peer_ids = list(range(n))
        for i in range(n):
            peers = [p for p in peer_ids if p != i]
            s = Server(server_id=i, peer_ids=peers, ready=ready)
            s.serve()
            self.cluster.append(s)

        for i, server in enumerate(self.cluster):
            for j, peer in enumerate(self.cluster):
                if i != j:
                    server.connect_to_peer(j, peer.get_listen_addr())

        ready.set()

        # One drain thread per server collects committed entries
        for i, server in enumerate(self.cluster):
            t = threading.Thread(
                target=self._drain_commits, args=(i, server), daemon=True
            )
            t.start()

    def shutdown(self) -> None:
        self._shutdown_event.set()
        for server in self.cluster:
            server.disconnect_all()
            server.shutdown()

    # ── commit tracking ───────────────────────

    def _drain_commits(self, server_id: int, server: Server) -> None:
        while not self._shutdown_event.is_set():
            try:
                entry = server.commit_chan.get(timeout=0.05)
            except queue.Empty:
                continue
            with self._mu:
                self._committed[server_id].append(entry)

    def check_committed_n(self, index: int, n_expected: int) -> object:
        """
        Poll until at least n_expected servers report a committed entry at
        log index *index*. All reporting servers must agree on the command.
        Returns the agreed command.
        """
        deadline = time.time() + 5.0
        while time.time() < deadline:
            with self._mu:
                cmd = None
                confirmed = []
                for sid, entries in self._committed.items():
                    if index < len(entries):
                        entry_cmd = entries[index].command
                        if cmd is None:
                            cmd = entry_cmd
                        elif entry_cmd != cmd:
                            raise AssertionError(
                                f"servers disagree on command at index {index}: "
                                f"{cmd!r} vs {entry_cmd!r}"
                            )
                        confirmed.append(sid)
                if len(confirmed) >= n_expected:
                    return cmd
            time.sleep(0.05)

        with self._mu:
            confirmed_count = sum(
                1 for entries in self._committed.values() if index < len(entries)
            )
        raise AssertionError(
            f"index {index}: only {confirmed_count}/{n_expected} servers committed after 5s"
        )

    def check_no_commit(self, index: int) -> None:
        """Assert that no server has committed an entry at *index*."""
        with self._mu:
            for sid, entries in self._committed.items():
                if index < len(entries):
                    raise AssertionError(
                        f"server {sid} unexpectedly committed index {index}: "
                        f"{entries[index]!r}"
                    )

    def submit_to_leader(self, cmd) -> tuple[bool, int]:
        """Submit *cmd* to the current leader. Returns (True, leader_id) on success."""
        for i, server in enumerate(self.cluster):
            if self.connected[i]:
                ok = server.cm.submit(cmd)
                if ok:
                    return True, i
        return False, -1

    # ── network control ───────────────────────

    def disconnect_peer(self, peer_id: int) -> None:
        self.connected[peer_id] = False
        for j, server in enumerate(self.cluster):
            if j != peer_id:
                server.disconnect_peer(peer_id)
                self.cluster[peer_id].disconnect_peer(j)

    def reconnect_peer(self, peer_id: int) -> None:
        self.connected[peer_id] = True
        for j, server in enumerate(self.cluster):
            if j != peer_id and self.connected[j]:
                server.connect_to_peer(peer_id, self.cluster[peer_id].get_listen_addr())
                self.cluster[peer_id].connect_to_peer(j, server.get_listen_addr())

    # ── assertions ───────────────────────────

    def check_single_leader(self) -> tuple[int, int]:
        for _ in range(10):
            leader_id, leader_term = None, None
            for server in self.cluster:
                sid, term, is_leader = server.cm.report()
                if is_leader:
                    if leader_id is not None:
                        raise AssertionError(
                            f"both {leader_id} and {sid} claim to be leader"
                        )
                    leader_id, leader_term = sid, term

            if leader_id is not None:
                return leader_id, leader_term

            time.sleep(0.5)

        raise AssertionError("no single leader elected after 5 s")

    def check_no_leader(self) -> None:
        for server in self.cluster:
            _, _, is_leader = server.cm.report()
            if is_leader:
                sid, term, _ = server.cm.report()
                raise AssertionError(f"server {sid} (term={term}) is unexpectedly a leader")


# ─────────────────────────────────────────────
# Helpers
# ─────────────────────────────────────────────


def sleep_ms(ms: int) -> None:
    time.sleep(ms / 1000)


# ─────────────────────────────────────────────
# Fixtures
# ─────────────────────────────────────────────


@pytest.fixture
def harness3():
    h = Harness(3)
    yield h
    h.shutdown()


@pytest.fixture
def harness5():
    h = Harness(5)
    yield h
    h.shutdown()


# ─────────────────────────────────────────────
# Election tests
# ─────────────────────────────────────────────


def test_election_basic(harness3):
    harness3.check_single_leader()


def test_election_leader_disconnect(harness3):
    h = harness3
    orig_leader_id, orig_term = h.check_single_leader()

    h.disconnect_peer(orig_leader_id)
    sleep_ms(350)

    new_leader_id, new_term = h.check_single_leader()
    assert new_leader_id != orig_leader_id
    assert new_term > orig_term


def test_election_leader_and_another_disconnect(harness3):
    h = harness3
    orig_leader_id, _ = h.check_single_leader()

    h.disconnect_peer(orig_leader_id)
    other_id = (orig_leader_id + 1) % 3
    h.disconnect_peer(other_id)

    sleep_ms(450)
    h.check_no_leader()

    h.reconnect_peer(other_id)
    h.check_single_leader()


def test_disconnect_all_then_restore(harness3):
    h = harness3
    sleep_ms(100)

    for i in range(3):
        h.disconnect_peer(i)

    sleep_ms(450)
    h.check_no_leader()

    for i in range(3):
        h.reconnect_peer(i)

    h.check_single_leader()


def test_election_leader_disconnect_then_reconnect(harness3):
    h = harness3
    orig_leader_id, _ = h.check_single_leader()

    h.disconnect_peer(orig_leader_id)
    sleep_ms(350)
    new_leader_id, new_term = h.check_single_leader()

    h.reconnect_peer(orig_leader_id)
    sleep_ms(150)
    again_leader_id, again_term = h.check_single_leader()

    assert again_leader_id == new_leader_id
    assert again_term == new_term


def test_election_leader_disconnect_then_reconnect_5(harness5):
    h = harness5
    orig_leader_id, _ = h.check_single_leader()

    h.disconnect_peer(orig_leader_id)
    sleep_ms(150)
    new_leader_id, new_term = h.check_single_leader()

    h.reconnect_peer(orig_leader_id)
    sleep_ms(150)
    again_leader_id, again_term = h.check_single_leader()

    assert again_leader_id == new_leader_id
    assert again_term == new_term


def test_election_follower_comes_back(harness3):
    h = harness3
    orig_leader_id, orig_term = h.check_single_leader()

    other_id = (orig_leader_id + 1) % 3
    h.disconnect_peer(other_id)
    time.sleep(0.650)

    h.reconnect_peer(other_id)
    sleep_ms(150)

    _, new_term = h.check_single_leader()
    assert new_term > orig_term


def test_election_disconnect_loop(harness3):
    h = harness3
    for _ in range(5):
        leader_id, _ = h.check_single_leader()

        h.disconnect_peer(leader_id)
        other_id = (leader_id + 1) % 3
        h.disconnect_peer(other_id)

        sleep_ms(310)
        h.check_no_leader()

        h.reconnect_peer(other_id)
        h.reconnect_peer(leader_id)

        sleep_ms(150)


# ─────────────────────────────────────────────
# Log replication tests
# ─────────────────────────────────────────────


def test_commit_one_command(harness3):
    h = harness3
    h.check_single_leader()

    ok, _ = h.submit_to_leader({"op": "set", "key": "x", "value": "1"})
    assert ok, "expected submit to succeed on leader"

    sleep_ms(250)
    h.check_committed_n(0, 3)


def test_submit_non_leader_fails(harness3):
    h = harness3
    leader_id, _ = h.check_single_leader()

    # Pick any non-leader and try submitting there
    non_leader_id = (leader_id + 1) % 3
    ok = h.cluster[non_leader_id].cm.submit({"op": "set", "key": "y", "value": "2"})
    assert not ok, "submit to non-leader should return False"


def test_commit_multiple_commands(harness3):
    h = harness3
    h.check_single_leader()

    commands = [
        {"op": "set", "key": "a", "value": "1"},
        {"op": "set", "key": "b", "value": "2"},
        {"op": "set", "key": "c", "value": "3"},
    ]
    for cmd in commands:
        ok, _ = h.submit_to_leader(cmd)
        assert ok

    sleep_ms(300)
    for i in range(len(commands)):
        h.check_committed_n(i, 3)


def test_no_commit_without_quorum(harness3):
    h = harness3
    leader_id, _ = h.check_single_leader()

    # Disconnect two peers so the leader has no quorum
    peer1 = (leader_id + 1) % 3
    peer2 = (leader_id + 2) % 3
    h.disconnect_peer(peer1)
    h.disconnect_peer(peer2)

    h.cluster[leader_id].cm.submit({"op": "set", "key": "z", "value": "99"})
    sleep_ms(300)
    h.check_no_commit(0)


def test_commit_after_leader_disconnect(harness3):
    h = harness3
    orig_leader_id, _ = h.check_single_leader()

    # Submit one command to the original leader
    h.cluster[orig_leader_id].cm.submit({"op": "set", "key": "k1", "value": "v1"})
    sleep_ms(250)
    h.check_committed_n(0, 3)

    # Disconnect original leader and elect a new one
    h.disconnect_peer(orig_leader_id)
    sleep_ms(350)
    new_leader_id, _ = h.check_single_leader()

    # Submit to the new leader
    ok = h.cluster[new_leader_id].cm.submit({"op": "set", "key": "k2", "value": "v2"})
    assert ok
    sleep_ms(250)
    # Only 2 active servers; majority of 3 is still 2
    h.check_committed_n(1, 2)


def test_follower_catches_up_on_reconnect(harness3):
    h = harness3
    leader_id, _ = h.check_single_leader()

    # Isolate one follower
    slow_peer = (leader_id + 1) % 3
    h.disconnect_peer(slow_peer)

    # Commit several entries without the slow peer
    for i in range(5):
        ok, lid = h.submit_to_leader({"op": "set", "key": f"k{i}", "value": str(i)})
        assert ok, f"submit {i} failed"
        sleep_ms(100)

    # Reconnect the slow peer
    h.reconnect_peer(slow_peer)
    sleep_ms(500)

    # All 5 entries should eventually be committed on the rejoined peer too
    for i in range(5):
        h.check_committed_n(i, 3)


def test_commit_with_unreliable_network():
    """Stress test under RAFT_UNRELIABLE_RPC conditions."""
    import os
    os.environ["RAFT_UNRELIABLE_RPC"] = "1"
    try:
        h = Harness(3)
        try:
            h.check_single_leader()
            ok, _ = h.submit_to_leader({"op": "set", "key": "u", "value": "1"})
            if ok:
                sleep_ms(600)
                # At least 1 server should have committed even under drops/delays
                with h._mu:
                    committed_count = sum(
                        1 for entries in h._committed.values() if len(entries) > 0
                    )
                assert committed_count >= 1
        finally:
            h.shutdown()
    finally:
        del os.environ["RAFT_UNRELIABLE_RPC"]
