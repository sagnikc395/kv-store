"""Background worker that periodically sweeps expired TTL keys."""

from __future__ import annotations

import threading

from .store import KVStore


class TTLWorker:
    def __init__(self, store: KVStore, interval_s: float = 1.0) -> None:
        self._store = store
        self._interval = interval_s
        self._stop_event = threading.Event()
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop_event.set()
        if self._thread is not None:
            self._thread.join(timeout=self._interval + 1)

    def _run(self) -> None:
        while not self._stop_event.wait(timeout=self._interval):
            self._store.purge_expired()
