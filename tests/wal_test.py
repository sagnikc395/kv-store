"""Tests for the Write-Ahead Log and WAL replay."""

import tempfile

import pytest

from kvstore.wal import WAL, replay_wal


# ─────────────────────────────────────────────
# WAL
# ─────────────────────────────────────────────


def test_append_and_replay_single_record():
    with tempfile.TemporaryDirectory() as d:
        with WAL(d) as w:
            w.append({"op": "set", "key": "foo", "value": "bar"})

        records = replay_wal(d)
        assert records == [{"op": "set", "key": "foo", "value": "bar"}]


def test_append_and_replay_multiple_records():
    entries = [
        {"op": "set", "key": "a", "value": "1"},
        {"op": "set", "key": "b", "value": "2"},
        {"op": "delete", "key": "a"},
    ]
    with tempfile.TemporaryDirectory() as d:
        with WAL(d) as w:
            for e in entries:
                w.append(e)

        assert replay_wal(d) == entries


def test_replay_preserves_order():
    with tempfile.TemporaryDirectory() as d:
        with WAL(d) as w:
            for i in range(20):
                w.append({"op": "set", "key": f"k{i}", "value": str(i)})

        records = replay_wal(d)
        assert len(records) == 20
        for i, rec in enumerate(records):
            assert rec["key"] == f"k{i}"
            assert rec["value"] == str(i)


def test_replay_empty_wal():
    with tempfile.TemporaryDirectory() as d:
        assert replay_wal(d) == []


def test_replay_missing_directory():
    # Non-existent path returns empty list
    assert replay_wal("/tmp/_no_such_dir_kv_wal_test") == []


def test_wal_survives_reopen():
    """Entries written in one WAL session are visible after reopening."""
    with tempfile.TemporaryDirectory() as d:
        with WAL(d) as w:
            w.append({"op": "set", "key": "x", "value": "1"})
        # Reopen and append more
        with WAL(d) as w:
            w.append({"op": "set", "key": "y", "value": "2"})

        records = replay_wal(d)
        assert len(records) == 2
        assert records[0]["key"] == "x"
        assert records[1]["key"] == "y"


def test_replay_tolerates_truncated_line():
    """A partial (malformed) line at the end of the WAL is silently skipped."""
    import os
    with tempfile.TemporaryDirectory() as d:
        wal_path = os.path.join(d, "wal.log")
        with WAL(d) as w:
            w.append({"op": "set", "key": "good", "value": "ok"})

        # Corrupt the WAL by appending a truncated line
        with open(wal_path, "a") as f:
            f.write('{"op": "set", "key": "bad"')  # no closing brace or newline

        records = replay_wal(d)
        assert records == [{"op": "set", "key": "good", "value": "ok"}]


def test_wal_unicode_values():
    with tempfile.TemporaryDirectory() as d:
        entry = {"op": "set", "key": "emoji", "value": "🚀"}
        with WAL(d) as w:
            w.append(entry)

        records = replay_wal(d)
        assert records == [entry]


def test_wal_creates_directory_if_missing():
    import os
    with tempfile.TemporaryDirectory() as base:
        nested = os.path.join(base, "a", "b", "c")
        with WAL(nested) as w:
            w.append({"op": "set", "key": "deep", "value": "yes"})
        assert replay_wal(nested) == [{"op": "set", "key": "deep", "value": "yes"}]
