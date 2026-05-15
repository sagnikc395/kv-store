"""WAL replay — read committed entries from disk and rebuild state."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any


def replay_wal(wal_dir: str, filename: str = "wal.log") -> list[dict[str, Any]]:
    """
    Read all valid JSON-lines records from the WAL and return them in order.
    Truncated or malformed lines (e.g. from a crash mid-write) are silently skipped.
    """
    path = Path(wal_dir) / filename
    if not path.exists():
        return []

    records: list[dict[str, Any]] = []
    with path.open("r", encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                records.append(json.loads(line))
            except json.JSONDecodeError:
                # Partial write at crash boundary — safe to skip
                continue
    return records
