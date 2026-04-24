"""Proxy request handler — routes KV operations to the correct node via consistent hashing."""

from __future__ import annotations

import logging
from xmlrpc.client import ServerProxy

from .router import HashRing

log = logging.getLogger(__name__)


class ProxyHandler:
    """
    Forwards KV RPCs to the node responsible for each key according to the hash ring.
    Node addresses are expected as "host:port" strings.
    """

    def __init__(self, ring: HashRing) -> None:
        self._ring = ring
        self._clients: dict[str, ServerProxy] = {}

    # ── routing ──────────────────────────────────

    def get(self, key: str) -> str | None:
        node = self._resolve(key)
        if node is None:
            return None
        try:
            return self._client(node).KVGet({"key": key})
        except Exception as exc:
            log.warning("GET %s→%s failed: %s", key, node, exc)
            return None

    def set(self, key: str, value: str, ttl: float | None = None) -> bool:
        node = self._resolve(key)
        if node is None:
            return False
        try:
            payload: dict = {"key": key, "value": value}
            if ttl is not None:
                payload["ttl"] = ttl
            self._client(node).KVSet(payload)
            return True
        except Exception as exc:
            log.warning("SET %s→%s failed: %s", key, node, exc)
            return False

    def delete(self, key: str) -> bool:
        node = self._resolve(key)
        if node is None:
            return False
        try:
            self._client(node).KVDelete({"key": key})
            return True
        except Exception as exc:
            log.warning("DELETE %s→%s failed: %s", key, node, exc)
            return False

    # ── helpers ──────────────────────────────────

    def _resolve(self, key: str) -> str | None:
        node = self._ring.get_node(key)
        if node is None:
            log.error("no nodes available in the ring")
        return node

    def _client(self, node: str) -> ServerProxy:
        if node not in self._clients:
            host, port = node.rsplit(":", 1)
            self._clients[node] = ServerProxy(
                f"http://{host}:{port}/", allow_none=True
            )
        return self._clients[node]
