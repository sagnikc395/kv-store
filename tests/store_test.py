"""Tests for the in-memory KV store and TTL worker."""

import time

import pytest

from kvstore.store import KVStore, TTLWorker


# ─────────────────────────────────────────────
# KVStore
# ─────────────────────────────────────────────


def test_set_and_get():
    s = KVStore()
    s.set("key", "value")
    assert s.get("key") == "value"


def test_get_missing_key():
    s = KVStore()
    assert s.get("missing") is None


def test_delete_existing_key():
    s = KVStore()
    s.set("k", "v")
    assert s.delete("k") is True
    assert s.get("k") is None


def test_delete_missing_key():
    s = KVStore()
    assert s.delete("nope") is False


def test_overwrite_key():
    s = KVStore()
    s.set("k", "v1")
    s.set("k", "v2")
    assert s.get("k") == "v2"


def test_keys_and_size():
    s = KVStore()
    s.set("a", "1")
    s.set("b", "2")
    s.set("c", "3")
    assert set(s.keys()) == {"a", "b", "c"}
    assert s.size() == 3


def test_ttl_expires():
    s = KVStore()
    s.set("ephemeral", "gone", ttl_seconds=0.05)
    assert s.get("ephemeral") == "gone"
    time.sleep(0.1)
    assert s.get("ephemeral") is None


def test_ttl_does_not_expire_early():
    s = KVStore()
    s.set("persistent", "here", ttl_seconds=5.0)
    assert s.get("persistent") == "here"


def test_no_ttl_lives_forever():
    s = KVStore()
    s.set("forever", "yes")
    time.sleep(0.05)
    assert s.get("forever") == "yes"


def test_purge_expired():
    s = KVStore()
    s.set("short", "x", ttl_seconds=0.05)
    s.set("long", "y", ttl_seconds=60.0)
    time.sleep(0.1)
    removed = s.purge_expired()
    assert removed == 1
    assert s.get("long") == "y"


def test_ttl_key_not_in_keys_after_expiry():
    s = KVStore()
    s.set("gone", "soon", ttl_seconds=0.05)
    time.sleep(0.1)
    assert "gone" not in s.keys()


# ── apply (Raft command) ──────────────────────


def test_apply_set():
    s = KVStore()
    result = s.apply({"op": "set", "key": "foo", "value": "bar"})
    assert result == "bar"
    assert s.get("foo") == "bar"


def test_apply_set_with_ttl():
    s = KVStore()
    s.apply({"op": "set", "key": "tmp", "value": "bye", "ttl": 0.05})
    assert s.get("tmp") == "bye"
    time.sleep(0.1)
    assert s.get("tmp") is None


def test_apply_delete():
    s = KVStore()
    s.set("del_me", "yes")
    s.apply({"op": "delete", "key": "del_me"})
    assert s.get("del_me") is None


def test_apply_unknown_op_is_noop():
    s = KVStore()
    result = s.apply({"op": "nuke", "key": "k"})
    assert result is None


# ─────────────────────────────────────────────
# TTLWorker
# ─────────────────────────────────────────────


def test_ttl_worker_cleans_expired_keys():
    s = KVStore()
    s.set("w1", "x", ttl_seconds=0.05)
    s.set("w2", "y", ttl_seconds=60.0)

    worker = TTLWorker(s, interval_s=0.03)
    worker.start()
    time.sleep(0.15)
    worker.stop()

    assert s.get("w1") is None
    assert s.get("w2") == "y"


def test_ttl_worker_stop_is_idempotent():
    s = KVStore()
    worker = TTLWorker(s, interval_s=0.1)
    worker.start()
    worker.stop()
    worker.stop()  # should not raise
