#!/usr/bin/env python3
"""
Launch a single KV-store Raft node.

Usage:
    python run_node.py --id=1 --port=7001 --wal-dir=./data/node1 \
        --peers=localhost:7002,localhost:7003
"""

import argparse
import logging
import queue
import threading

from kvstore.raft.server import Server
from kvstore.store import KVStore, TTLWorker
from kvstore.wal import WAL, replay_wal

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger(__name__)


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="Run a kv-store Raft node")
    p.add_argument("--id", type=int, required=True, help="Node ID (integer)")
    p.add_argument("--port", type=int, default=7001, help="Listen port")
    p.add_argument("--wal-dir", default="./data", help="WAL directory")
    p.add_argument(
        "--peers",
        default="",
        help="Comma-separated peer IDs (integers), e.g. 2,3",
    )
    return p.parse_args()


def main() -> None:
    args = parse_args()
    node_id = args.id
    peer_ids = [int(p) for p in args.peers.split(",") if p.strip()]

    # Replay WAL to restore state
    wal_dir = args.wal_dir
    prior_records = replay_wal(wal_dir)
    store = KVStore()
    for record in prior_records:
        store.apply(record)
    log.info("Replayed %d WAL records", len(prior_records))

    wal = WAL(wal_dir)
    ttl_worker = TTLWorker(store)
    ttl_worker.start()

    ready = threading.Event()
    server = Server(server_id=node_id, peer_ids=peer_ids, ready=ready)
    server.serve()
    ready.set()

    # Apply committed Raft entries to the KV store and WAL
    def _apply_loop() -> None:
        while True:
            entry = server.commit_chan.get()
            if entry is None:
                break
            result = store.apply(entry.command)
            wal.append(entry.command)
            log.debug("applied command=%s result=%s", entry.command, result)

    apply_thread = threading.Thread(target=_apply_loop, daemon=True)
    apply_thread.start()

    log.info("Node %d running (peers=%s)", node_id, peer_ids)
    try:
        threading.Event().wait()
    except KeyboardInterrupt:
        pass
    finally:
        server.shutdown()
        ttl_worker.stop()
        wal.close()
        log.info("Node %d shut down", node_id)


if __name__ == "__main__":
    main()
