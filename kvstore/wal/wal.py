"""Append-only Write-Ahead Log backed by a JSON-lines file."""

from __future__ import annotations

import json
import os
import threading
from pathlib import Path
from typing import Any


class WAL:
    """
    Each record is a JSON object terminated by a newline.
    Writes are serialized with a mutex; an fsync is issued after each append.
    """

    def __init__(self, wal_dir: str, filename: str = "wal.log") -> None:
        self._path = Path(wal_dir) / filename
        self._path.parent.mkdir(parents=True, exist_ok=True)
        self._mu = threading.Lock()
        self._file = self._path.open("a", encoding="utf-8")

    def append(self, record: dict[str, Any]) -> None:
        """Atomically write *record* to the log and fsync."""
        line = json.dumps(record, ensure_ascii=False) + "\n"
        with self._mu:
            self._file.write(line)
            self._file.flush()
            os.fsync(self._file.fileno())

    def close(self) -> None:
        with self._mu:
            self._file.close()

    def __enter__(self) -> "WAL":
        return self

    def __exit__(self, *_: Any) -> None:
        self.close()
