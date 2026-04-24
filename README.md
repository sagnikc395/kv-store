# kv-store

A distributed in-memory key-value store built in **Python**, implementing the **Raft consensus algorithm** for fault-tolerant replication across a 3-node cluster. Designed as a ground-up exploration of the core engineering primitives behind systems like etcd and Redis Cluster.

## Overview

`kv-store` is a distributed key-value store that prioritizes correctness over complexity. A single-node instance behaves like an in-process cache with WAL-backed durability. A 3-node cluster introduces Raft consensus — tolerating one node failure without data loss or availability interruption.

## Architecture

```
                        ┌─────────────────────┐
                        │     Client / CLI     │
                        └──────────┬──────────┘
                                   │ gRPC
                                   ▼
                        ┌─────────────────────┐
                        │   Proxy / Router    │
                        │  (consistent hash)  │
                        └──────────┬──────────┘
                                   │
              ┌────────────────────┼────────────────────┐
              │                    │                    │
              ▼                    ▼                    ▼
   ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
   │   Node 1        │  │   Node 2        │  │   Node 3        │
   │   (Leader)      │  │   (Follower)    │  │   (Follower)    │
   │                 │  │                 │  │                 │
   │  ┌───────────┐  │  │  ┌───────────┐  │  │  ┌───────────┐  │
   │  │ Raft FSM  │  │  │  │ Raft FSM  │  │  │  │ Raft FSM  │  │
   │  └─────┬─────┘  │  │  └─────┬─────┘  │  │  └─────┬─────┘  │
   │        │        │  │        │        │  │        │        │
   │  ┌─────▼─────┐  │  │  ┌─────▼─────┐  │  │  ┌─────▼─────┐  │
   │  │  KV Store │  │  │  │  KV Store │  │  │  │  KV Store │  │
   │  └─────┬─────┘  │  │  └─────┬─────┘  │  │  └─────┬─────┘  │
   │        │        │  │        │        │  │        │        │
   │  ┌─────▼─────┐  │  │  ┌─────▼─────┐  │  │  ┌─────▼─────┐  │
   │  │    WAL    │  │  │  │    WAL    │  │  │  │    WAL    │  │
   │  └───────────┘  │  │  └───────────┘  │  │  └───────────┘  │
   └─────────────────┘  └─────────────────┘  └─────────────────┘
```

**Write path:** Client → Proxy → Leader → WAL append → replicate to followers → majority ack → apply → respond
**Read path:** Client → Proxy → leader (linearizable) or replica (stale reads)
**Failure:** Leader crashes → election → new leader in ~150–300ms → writes resume


## Features

* **Raft consensus** — leader election, log replication, quorum-based writes, follower recovery
* **Write-ahead log (WAL)** — durable append-only persistence with replay on restart
* **Consistent hashing** — virtual nodes for even key distribution and minimal reshuffling
* **TTL-based expiry** — background async task cleans expired keys
* **gRPC interface** — typed API using protobufs (unary + streaming)
* **Async concurrency (asyncio)** — election timers, heartbeats, and replication run as independent tasks
* **Dockerized cluster** — spin up a full 3-node cluster with `docker compose up`


## Project Structure

```
kv-store/
├── kvstore/
│   ├── raft/
│   │   ├── node.py         # Raft state machine
│   │   ├── log.py          # Log replication
│   │   ├── election.py     # Leader election logic
│   │   └── rpc.py          # gRPC communication
│   ├── store/
│   │   ├── store.py        # In-memory KV store
│   │   └── ttl.py          # TTL cleanup worker
│   ├── wal/
│   │   ├── wal.py          # WAL append logic
│   │   └── replay.py       # WAL recovery
│   ├── proxy/
│   │   ├── router.py       # Consistent hashing ring
│   │   └── handler.py      # Request routing
│   └── config.py           # Configuration
├── proto/
│   ├── kv.proto
│   └── raft.proto
│── run_node.py
│── run_proxy.py
├── docker-compose.yml
├── Dockerfile
├── pyproject.toml
└── README.md
```

## Quickstart

### Prerequisites

* Python 3.12+
* `grpcio`, `protobuf`
* Docker & Docker Compose

### 1. Install dependencies

```bash
git clone https://github.com/sagnikc395/kv-store.git
cd kv-store

pip install -e .
```

### 2. Run a single node

```bash
python scripts/run_node.py --id=1 --port=7001 --wal-dir=./data/node1
```

### 3. Run a 3-node cluster

```bash
docker compose up --build

# Node 1 → localhost:7001
# Node 2 → localhost:7002
# Node 3 → localhost:7003
# Proxy → localhost:8000
```

### 4. Interact via CLI

```bash
# Set
grpcurl -plaintext -d '{"key":"foo","value":"bar"}' \
  localhost:8000 kv.KVService/Set

# Get
grpcurl -plaintext -d '{"key":"foo"}' \
  localhost:8000 kv.KVService/Get
```

## How It Works

### Raft Consensus

Each node operates in one of three states: **follower**, **candidate**, or **leader**.

* Followers wait for heartbeats
* Timeout → become candidate → request votes
* Majority vote → leader elected
* Leader handles all writes and replicates logs

Replication is parallelized using async tasks. A write is committed only after a majority acknowledges it.


### Write-Ahead Log

Every mutation is written to disk before being applied. On restart, the node replays the WAL to reconstruct state. This ensures durability without requiring full snapshots.


### Consistent Hashing

The proxy distributes keys using a hash ring with virtual nodes. This avoids hotspots and minimizes key movement when nodes join or leave.

## License

MIT

## References:

- [Eli Bendersky's Awesome Blog Post Series on Building Raft Consensus in Go](https://eli.thegreenplace.net/2020/implementing-raft-part-0-introduction/)
- [Raft Consensus Paper](https://raft.github.io/raft.pdf)