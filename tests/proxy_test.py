"""Tests for the consistent-hash ring and proxy handler."""

import pytest

from kvstore.proxy import HashRing, ProxyHandler


# ─────────────────────────────────────────────
# HashRing
# ─────────────────────────────────────────────


def test_ring_routes_to_single_node():
    ring = HashRing(nodes=["node1:7001"])
    assert ring.get_node("any_key") == "node1:7001"


def test_ring_returns_none_when_empty():
    ring = HashRing()
    assert ring.get_node("key") is None


def test_ring_distributes_across_nodes():
    ring = HashRing(nodes=["n1", "n2", "n3"], virtual_nodes=150)
    seen = set()
    for i in range(300):
        node = ring.get_node(f"key-{i}")
        seen.add(node)
    assert seen == {"n1", "n2", "n3"}, "all nodes should receive at least one key"


def test_ring_same_key_always_same_node():
    ring = HashRing(nodes=["a", "b", "c"], virtual_nodes=100)
    first = ring.get_node("stable-key")
    for _ in range(50):
        assert ring.get_node("stable-key") == first


def test_ring_add_node():
    ring = HashRing(nodes=["n1", "n2"])
    ring.add_node("n3")
    assert "n3" in ring.nodes()


def test_ring_remove_node():
    ring = HashRing(nodes=["n1", "n2", "n3"])
    ring.remove_node("n2")
    assert "n2" not in ring.nodes()
    # After removal, keys still resolve to remaining nodes
    for i in range(50):
        node = ring.get_node(f"k{i}")
        assert node in {"n1", "n3"}


def test_ring_minimal_reshuffling_on_add():
    """
    Adding one node should remap only ~1/n of keys,
    not a complete reshuffle.
    """
    nodes = ["n1", "n2", "n3"]
    ring_before = HashRing(nodes=nodes, virtual_nodes=200)
    keys = [f"key-{i}" for i in range(1000)]
    mapping_before = {k: ring_before.get_node(k) for k in keys}

    ring_after = HashRing(nodes=nodes + ["n4"], virtual_nodes=200)
    mapping_after = {k: ring_after.get_node(k) for k in keys}

    changed = sum(1 for k in keys if mapping_before[k] != mapping_after[k])
    # Expect roughly 25% (1/4) to be remapped; allow up to 50% for hash variance
    assert changed < len(keys) * 0.5, (
        f"{changed}/{len(keys)} keys remapped — more than expected 50%"
    )


def test_ring_nodes_list_unique():
    ring = HashRing(nodes=["a", "b", "c"], virtual_nodes=50)
    nodes = ring.nodes()
    assert len(nodes) == len(set(nodes))


def test_ring_handles_unicode_keys():
    ring = HashRing(nodes=["alpha", "beta"])
    node = ring.get_node("日本語キー")
    assert node in {"alpha", "beta"}


# ─────────────────────────────────────────────
# ProxyHandler (unit — no real network)
# ─────────────────────────────────────────────


def test_proxy_handler_resolves_to_correct_node():
    ring = HashRing(nodes=["localhost:7001", "localhost:7002"])
    handler = ProxyHandler(ring)
    key = "mykey"
    expected_node = ring.get_node(key)
    # Verify the client dict is populated for the right node after a failed call
    # (get will fail because the node isn't real, but the routing still happens)
    handler.get(key)  # will fail silently
    assert expected_node in handler._clients


def test_proxy_returns_none_on_empty_ring():
    ring = HashRing()
    handler = ProxyHandler(ring)
    assert handler.get("key") is None
    assert handler.set("key", "value") is False
    assert handler.delete("key") is False
