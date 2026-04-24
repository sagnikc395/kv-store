import time
import threading
import pytest

from kvstore.raft.server import Server
from kvstore.raft.raft import ConsensusModule


class Harness:
    """
    Test harness that creates a cluster of n Raft servers and wires them
    together, mirroring the Go Harness type.
    """

    def __init__(self, n: int) -> None:
        self.n = n
        self.cluster: list[Server] = []
        self.connected: list[bool] = [True] * n

        ready = threading.Event()

        # Create all servers
        peer_ids = list(range(n))
        for i in range(n):
            peers = [p for p in peer_ids if p != i]
            s = Server(server_id=i, peer_ids=peers, ready=ready)
            s.serve()
            self.cluster.append(s)

        # Wire every server to every other server
        for i, server in enumerate(self.cluster):
            for j, peer in enumerate(self.cluster):
                if i != j:
                    server.connect_to_peer(j, peer.get_listen_addr())

        ready.set()

    def shutdown(self) -> None:
        for server in self.cluster:
            server.disconnect_all()
            server.shutdown()

    # ── network control ───────────────────────

    def disconnect_peer(self, peer_id: int) -> None:
        """Simulate a network partition by dropping all connections to peer_id."""
        self.connected[peer_id] = False
        # Disconnect peer_id from everyone else
        for j, server in enumerate(self.cluster):
            if j != peer_id:
                server.disconnect_peer(peer_id)
                self.cluster[peer_id].disconnect_peer(j)

    def reconnect_peer(self, peer_id: int) -> None:
        """Heal the partition for peer_id."""
        self.connected[peer_id] = True
        for j, server in enumerate(self.cluster):
            if j != peer_id and self.connected[j]:
                server.connect_to_peer(peer_id, self.cluster[peer_id].get_listen_addr())
                self.cluster[peer_id].connect_to_peer(j, server.get_listen_addr())

    # ── assertions ───────────────────────────

    def check_single_leader(self) -> tuple[int, int]:
        """
        Poll until exactly one leader is found. Returns (leader_id, term).
        Fails (raises) if no single leader emerges within ~5 seconds.
        Mirrors Go's CheckSingleLeader.
        """
        for _ in range(10):  # up to 10 × 500 ms = 5 s
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
        """Assert that no server currently believes it is the leader."""
        for server in self.cluster:
            _, _, is_leader = server.cm.report()
            if is_leader:
                sid, term, _ = server.cm.report()
                raise AssertionError(
                    f"server {sid} (term={term}) is unexpectedly a leader"
                )


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
# Tests
# ─────────────────────────────────────────────


def test_election_basic(harness3):
    harness3.check_single_leader()


def test_election_leader_disconnect(harness3):
    h = harness3
    orig_leader_id, orig_term = h.check_single_leader()

    h.disconnect_peer(orig_leader_id)
    sleep_ms(350)

    new_leader_id, new_term = h.check_single_leader()
    assert new_leader_id != orig_leader_id, "new leader should differ from original"
    assert new_term > orig_term, (
        f"expected new_term > orig_term, got {new_term} and {orig_term}"
    )


def test_election_leader_and_another_disconnect(harness3):
    h = harness3
    orig_leader_id, _ = h.check_single_leader()

    h.disconnect_peer(orig_leader_id)
    other_id = (orig_leader_id + 1) % 3
    h.disconnect_peer(other_id)

    # No quorum — no leader should emerge
    sleep_ms(450)
    h.check_no_leader()

    # Reconnect one peer to restore quorum
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

    assert again_leader_id == new_leader_id, (
        f"again leader id got {again_leader_id}; want {new_leader_id}"
    )
    assert again_term == new_term, f"again term got {again_term}; want {new_term}"


def test_election_leader_disconnect_then_reconnect_5(harness5):
    h = harness5
    orig_leader_id, _ = h.check_single_leader()

    h.disconnect_peer(orig_leader_id)
    sleep_ms(150)
    new_leader_id, new_term = h.check_single_leader()

    h.reconnect_peer(orig_leader_id)
    sleep_ms(150)
    again_leader_id, again_term = h.check_single_leader()

    assert again_leader_id == new_leader_id, (
        f"again leader id got {again_leader_id}; want {new_leader_id}"
    )
    assert again_term == new_term, f"again term got {again_term}; want {new_term}"


def test_election_follower_comes_back(harness3):
    h = harness3
    orig_leader_id, orig_term = h.check_single_leader()

    other_id = (orig_leader_id + 1) % 3
    h.disconnect_peer(other_id)
    time.sleep(0.650)

    h.reconnect_peer(other_id)
    sleep_ms(150)

    # We can't assert on the leader id (depends on relative timeouts),
    # but a term increase confirms re-election happened.
    _, new_term = h.check_single_leader()
    assert new_term > orig_term, f"newTerm={new_term}, origTerm={orig_term}"


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

        # Let the cluster settle before the next cycle
        sleep_ms(150)
