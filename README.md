# kv-store

A distributed in-memory key-value store built in **Python** as a hands-on implementation of the systems ideas behind durable, replicated storage: write-ahead logging, TTL expiry, consistent hashing, leader election, and Raft-style log replication.

The project started as a way to go beyond a simple dictionary-backed cache. I wanted to understand what has to happen when a key-value store needs to survive restarts, route keys across nodes, keep replicas ordered, and continue operating when one node fails.

## Overview

`kv-store` is a learning-focused distributed storage prototype. A single node acts like a fast in-memory store with WAL-backed recovery. A small cluster adds a Raft consensus layer so writes are ordered through a leader, replicated to followers, and committed only after a quorum acknowledges the operation.

The intent was not to build a Redis replacement. The goal was to learn the internals by implementing the moving pieces directly instead of hiding them behind a framework.

## Architecture

```text
                        +---------------------+
                        |     Client / CLI    |
                        +----------+----------+
                                   |
                                   v
                        +---------------------+
                        |   Proxy / Router    |
                        |  consistent hashing |
                        +----------+----------+
                                   |
              +--------------------+--------------------+
              |                    |                    |
              v                    v                    v
   +-----------------+  +-----------------+  +-----------------+
   | Node 1          |  | Node 2          |  | Node 3          |
   | Leader/Follower |  | Leader/Follower |  | Leader/Follower |
   |                 |  |                 |  |                 |
   |  +-----------+  |  |  +-----------+  |  |  +-----------+  |
   |  | Raft FSM  |  |  |  | Raft FSM  |  |  |  | Raft FSM  |  |
   |  +-----+-----+  |  |  +-----+-----+  |  |  +-----+-----+  |
   |        |        |  |        |        |  |        |        |
   |  +-----v-----+  |  |  +-----v-----+  |  |  +-----v-----+  |
   |  | KV Store  |  |  |  | KV Store  |  |  |  | KV Store  |  |
   |  +-----+-----+  |  |  +-----+-----+  |  |  +-----+-----+  |
   |        |        |  |        |        |  |        |        |
   |  +-----v-----+  |  |  +-----v-----+  |  |  +-----v-----+  |
   |  |    WAL    |  |  |  |    WAL    |  |  |  |    WAL    |  |
   |  +-----------+  |  |  +-----------+  |  |  +-----------+  |
   +-----------------+  +-----------------+  +-----------------+
```

**Write path:** client -> XML-RPC proxy -> responsible node -> Raft log append -> follower replication -> majority commit -> apply to store -> response.

**Read path:** client -> proxy -> responsible node. Linearizable reads are routed through the leader; replica reads can be allowed when stale reads are acceptable.

**Recovery path:** node starts -> WAL replay reconstructs local state -> Raft catches the node up with any committed log entries it missed.

## Features

- **In-memory key-value store** with `set`, `get`, `delete`, key listing, and optional TTLs.
- **TTL expiry worker** that removes expired keys in the background while reads also lazily reject expired values.
- **Write-ahead log** using append-only records so committed mutations can be replayed after restart.
- **Raft-style consensus** with follower, candidate, and leader states, randomized election timeouts, vote requests, heartbeats, log replication, and quorum commit.
- **Consistent hashing proxy** with virtual nodes so keys spread across the cluster and only a small portion of keys move when membership changes.
- **XML-RPC transport** using Python standard-library RPC primitives for the client proxy and Raft peer messages.
- **Dockerized local cluster** for running the project in a repeatable environment.

## Python Project Layout

```text
kv-store/
|-- kvstore/
|   |-- store/
|   |   |-- store.py        # In-memory KV store and command application
|   |   `-- ttl.py          # Background expiry worker
|   |-- wal/
|   |   |-- wal.py          # Append-only WAL writer
|   |   `-- replay.py       # WAL replay on startup
|   |-- raft/
|   |   |-- node.py         # Raft state machine
|   |   |-- election.py     # Election timeout and vote handling
|   |   |-- log.py          # Replicated log entries
|   |   `-- rpc.py          # Inter-node RPC messages
|   |-- proxy/
|   |   |-- router.py       # Consistent hash ring
|   |   `-- handler.py      # Request routing
|   `-- config.py           # Node and cluster configuration
|-- run_node.py             # Node entrypoint
|-- run_proxy.py            # Proxy entrypoint
|-- docker-compose.yml
|-- Dockerfile
|-- pyproject.toml
`-- README.md
```

## Design Decisions

### Python for the First Implementation

I chose Python because I wanted the first version to make the distributed systems logic easy to inspect. Raft already has enough moving parts: terms, votes, leader transitions, log indexes, commit indexes, and retries. Python made it easier to iterate on those ideas quickly before worrying about lower-level performance details.

The tradeoff is that Python is not the fastest runtime for a high-throughput database. For this project, that was acceptable because the goal was correctness, observability, and learning the mechanics.

### In-Memory Store With Explicit Command Application

The store accepts committed commands rather than arbitrary direct mutations. That keeps the boundary clean: Raft decides when an operation is committed, and the store only applies operations that have already passed consensus.

This also makes recovery easier because WAL replay and Raft commit application can use the same command shape.

### Write-Ahead Log Before State Mutation

Mutating memory first and writing later is simpler, but it can lose acknowledged writes during a crash. The WAL records mutations before they are treated as durable. On restart, the node can replay the log and rebuild the in-memory state.

I used append-only records because they are easy to reason about and match the mental model used by many storage systems. Compaction and snapshotting are intentionally left as future work.

### TTL as Store Metadata

TTL is stored alongside each value instead of in a separate scheduler-only structure. Reads check expiry lazily, while a background worker periodically removes expired keys.

This gives two useful properties: expired keys do not appear valid even if the cleanup worker has not run yet, and the cleanup worker can stay simple.

### Raft Leader for Writes

Writes flow through the current leader. Followers do not independently accept writes because that would risk divergent ordering. The leader appends the command, replicates it, waits for a majority, then advances the commit index.

This design prioritizes consistency over accepting writes anywhere in the cluster.

### Randomized Election Timeouts

Each follower waits for a randomized election timeout before becoming a candidate. This reduces the chance that multiple nodes start elections at the same time and split the vote repeatedly.

Heartbeats reset the election timer, so a healthy leader keeps followers from starting unnecessary elections.

### Consistent Hashing With Virtual Nodes

The proxy uses consistent hashing so the client does not need to know which node owns a key. Virtual nodes improve distribution because a tiny physical cluster can otherwise place keys unevenly around the ring.

This is useful for learning because it separates two concerns: Raft handles replication inside a shard, while the proxy handles key placement across nodes.

### Small Cluster First

I focused on a 3-node cluster because it is the smallest useful Raft setup. It can tolerate one node failure while still requiring a majority of two for progress.

That kept the project small enough to finish while still exposing the real failure cases: leader crash, follower lag, stale logs, and re-election.

## How I Learned This

I learned this project in layers instead of trying to build the whole distributed system at once.

First, I built a simple in-memory key-value store. That gave me a clean baseline for `set`, `get`, `delete`, and TTL behavior.

Next, I added durability with a write-ahead log. This helped me understand why databases record intent before mutating state and why replay needs a stable command format.

After that, I studied Raft and implemented the core state transitions: follower, candidate, and leader. The hardest part was not writing the RPC handlers themselves; it was understanding the invariants behind them. Terms must only move forward, each node votes carefully, leaders must step down when they see a newer term, and entries must be applied in the same order on every node.

Then I added routing with consistent hashing. That helped me see the difference between replication and sharding. Replication keeps copies of the same data available; sharding decides where different keys should live.

The biggest lesson was that distributed systems are mostly about preserving simple invariants under messy timing. Timers fire at different moments, messages can arrive late, nodes can restart, and the code still has to keep one agreed order of writes.

## Running Locally

### Install

```bash
git clone https://github.com/sagnikc395/kv-store.git
cd kv-store
pip install -e .
```

### Start a Node

```bash
python run_node.py --id=1 --port=7001 --wal-dir=./data/node1 --peers=2,3
```

### Start a 3-Node Cluster

```bash
docker compose up --build
```

Expected local ports:

- Node 1: `localhost:7001`
- Node 2: `localhost:7002`
- Node 3: `localhost:7003`
- Proxy: `localhost:8000`

### Example Requests

```python
from xmlrpc.client import ServerProxy

client = ServerProxy("http://localhost:8000/", allow_none=True)

client.KVSet({"key": "foo", "value": "bar"})
client.KVGet({"key": "foo"})
client.KVDelete({"key": "foo"})
```

## Limitations

- Snapshotting and log compaction are not implemented yet, so the WAL can grow without bound.
- Dynamic cluster membership is out of scope for the initial version.
- Read-index based linearizable reads are future work.
- This is a learning project, not a production database.

## Future Work

- Add snapshots and WAL compaction.
- Add membership changes for adding and removing nodes.
- Add metrics for election count, replication lag, WAL latency, and request latency.
- Add a benchmark suite for throughput and failure scenarios.
- Add Jepsen-style fault testing or a smaller deterministic simulation harness.

## References

- Diego Ongaro and John Ousterhout, *In Search of an Understandable Consensus Algorithm*
- Raft visualization and paper resources from [raft.github.io](https://raft.github.io/)
- Eli Bendersky's Raft implementation series
- Redis and etcd documentation for storage-system design inspiration

## License

MIT
