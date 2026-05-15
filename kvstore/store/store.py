"""In-memory key-value store with optional per-key TTL."""

from __future__ import annotations

import threading
import time
from typing import Optional


class KVStore:
    def __init__(self) -> None:
        self._mu = threading.Lock()
        # value → (value, expires_at_monotonic | None)
        self._data: dict[str, tuple[str, float | None]] = {}

    # ── core operations ──────────────────────────

    def get(self, key: str) -> Optional[str]:
        with self._mu:
            entry = self._data.get(key)
            if entry is None:
                return None
            value, expires_at = entry
            if expires_at is not None and time.monotonic() >= expires_at:
                del self._data[key]
                return None
            return value

    def set(self, key: str, value: str, ttl_seconds: Optional[float] = None) -> None:
        expires_at = None
        if ttl_seconds is not None and ttl_seconds > 0:
            expires_at = time.monotonic() + ttl_seconds
        with self._mu:
            self._data[key] = (value, expires_at)

    def delete(self, key: str) -> bool:
        with self._mu:
            if key in self._data:
                del self._data[key]
                return True
            return False

    def keys(self) -> list[str]:
        now = time.monotonic()
        with self._mu:
            return [
                k for k, (_, exp) in self._data.items()
                if exp is None or exp > now
            ]

    def size(self) -> int:
        return len(self.keys())

    # ── Raft command application ─────────────────

    def apply(self, command: dict) -> Optional[str]:
        """
        Apply a committed Raft command to the store.
        Supported ops: "set", "delete".
        Returns the stored value for "set", None otherwise.
        """
        op = command.get("op")
        key = command.get("key", "")
        if op == "set":
            value = command.get("value", "")
            ttl = command.get("ttl")
            self.set(key, value, ttl_seconds=ttl)
            return value
        if op == "delete":
            self.delete(key)
            return None
        return None

    # ── TTL sweep ────────────────────────────────

    def purge_expired(self) -> int:
        """Remove all expired keys. Returns the number of keys removed."""
        now = time.monotonic()
        with self._mu:
            expired = [
                k for k, (_, exp) in self._data.items()
                if exp is not None and now >= exp
            ]
            for k in expired:
                del self._data[k]
            return len(expired)
