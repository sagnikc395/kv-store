"""Consistent hash ring with virtual nodes."""

from __future__ import annotations

import hashlib
import threading
from bisect import bisect_left, insort


class HashRing:
    """
    A consistent-hash ring mapping arbitrary string keys to node addresses.

    Virtual nodes are inserted for each physical node to even out the
    distribution and minimise reshuffling when nodes join or leave.
    """

    def __init__(self, nodes: list[str] | None = None, virtual_nodes: int = 100) -> None:
        self._virtual_nodes = virtual_nodes
        self._mu = threading.Lock()
        self._ring: dict[int, str] = {}   # hash → node address
        self._sorted_keys: list[int] = []

        for node in (nodes or []):
            self.add_node(node)

    # ── ring management ──────────────────────────

    def add_node(self, node: str) -> None:
        with self._mu:
            for i in range(self._virtual_nodes):
                key = self._hash(f"{node}#{i}")
                self._ring[key] = node
                insort(self._sorted_keys, key)

    def remove_node(self, node: str) -> None:
        with self._mu:
            for i in range(self._virtual_nodes):
                key = self._hash(f"{node}#{i}")
                if key in self._ring:
                    del self._ring[key]
                    idx = bisect_left(self._sorted_keys, key)
                    if idx < len(self._sorted_keys) and self._sorted_keys[idx] == key:
                        self._sorted_keys.pop(idx)

    # ── key routing ──────────────────────────────

    def get_node(self, key: str) -> str | None:
        with self._mu:
            if not self._ring:
                return None
            h = self._hash(key)
            idx = bisect_left(self._sorted_keys, h)
            if idx == len(self._sorted_keys):
                idx = 0
            return self._ring[self._sorted_keys[idx]]

    def nodes(self) -> list[str]:
        with self._mu:
            return list(dict.fromkeys(self._ring.values()))

    # ── internal ─────────────────────────────────

    @staticmethod
    def _hash(data: str) -> int:
        digest = hashlib.md5(data.encode("utf-8"), usedforsecurity=False).hexdigest()
        return int(digest, 16)
