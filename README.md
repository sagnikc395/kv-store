# kv-store

`kv-store` is a small distributed key-value store written in Python. It combines:

- An in-memory store with optional per-key TTL
- A Raft consensus module for leader election and log replication
- WAL replay for restoring node state on restart
- A simple XML-RPC proxy with consistent hashing for key routing

The project is intentionally compact. It is useful as a learning implementation of replication, coordination, and failure handling rather than a production-ready datastore.

## What Is In The Repo

```text
kvstore/
  raft/     Raft state machine, RPC server, log replication
  store/    In-memory KV store and TTL worker
  wal/      WAL append and replay helpers
  proxy/    Consistent hash ring and XML-RPC proxy handler
run_node.py Start a single Raft node
run_proxy.py Start the routing proxy
tests/      Store, WAL, proxy, and Raft tests
```

## Current Transport

The active implementation uses Python's built-in `xmlrpc` modules for node-to-node RPCs and proxy-to-node routing.

## Requirements

- Python 3.10+
- `uv` recommended for dependency management and running tests

## Setup

```bash
git clone https://github.com/sagnikc395/kv-store.git
cd kv-store
uv sync
```

If you prefer `pip`:

```bash
pip install -e .
```

## Run Tests

```bash
python run_node.py --id=1 --port=7001 --wal-dir=./data/node1
uv run pytest -q
```

## Run A Node

Start one node:

```bash
python run_node.py --id=1 --port=7001 --wal-dir=./data/node1 --peers=2,3
```

Arguments:

- `--id`: numeric node ID
- `--port`: local listen port
- `--wal-dir`: directory used for WAL storage
- `--peers`: comma-separated peer IDs

`run_node.py` restores previous commands from the WAL, starts the TTL worker, and applies committed Raft entries to both the store and the WAL.

## Run The Proxy

```bash
python run_proxy.py --port=8000 --nodes=localhost:7001,localhost:7002,localhost:7003
```

The proxy uses a consistent-hash ring to route each key to a node address.

## Architecture Notes

- Writes are accepted only by the Raft leader.
- Followers replicate log entries through `AppendEntries`.
- Committed entries are applied to the in-memory store.
- TTL expiry is handled by a background sweep worker.
- WAL replay rebuilds store state after restart.

## Things to Consider

- Raft leaders now step down when they lose quorum instead of continuing to report themselves as leader indefinitely.
- Vote state is preserved correctly when a node steps down in the same term, which prevents unstable leader changes after reconnection.
- Election timing is less aggressive, reducing avoidable leader churn in multi-node tests.
- Project metadata and documentation now match the actual implementation.

## License

MIT
