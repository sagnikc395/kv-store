# kv-store

A distributed in-memory key-value store built in Go, implementing the Raft consensus algorithm for fault-tolerant replication across a 3-node cluster. Designed as a ground-up exploration of the core engineering primitives behind systems like etcd and Redis Cluster.

![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8?style=flat-square)
![Raft](https://img.shields.io/badge/Consensus-Raft-orange?style=flat-square)
![gRPC](https://img.shields.io/badge/RPC-gRPC-4285F4?style=flat-square)
![Docker](https://img.shields.io/badge/Docker-ready-blue?style=flat-square)
![License](https://img.shields.io/badge/License-MIT-gray?style=flat-square)

---

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Features](#features)
- [Project Structure](#project-structure)
- [Quickstart](#quickstart)
- [API Reference](#api-reference)
- [Configuration](#configuration)
- [How It Works](#how-it-works)
- [Roadmap](#roadmap)
- [License](#license)

---

## Overview

`kv-store` is a distributed key-value store that prioritizes correctness over complexity. A single-node instance behaves like an in-process cache with WAL-backed durability. A 3-node cluster adds Raft consensus — meaning the cluster tolerates one node failure without data loss or availability interruption.

The goal is not to replace Redis. The goal is to build something that forces real engagement with the hard problems: split-brain prevention, log replication under partial failure, and consistent routing across shards.

---

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

**Write path:** Client → Proxy (consistent hash routes to shard leader) → Leader appends to WAL → replicates to followers → majority ack → applies to in-memory store → responds to client.

**Read path:** Client → Proxy → any node in shard (linearizable reads go to leader, stale reads can go to any replica).

**Failure:** If the leader crashes, the remaining two nodes hold an election. The node with the most up-to-date log and the first randomized timeout wins. Writes resume within ~150–300ms.

---

## Features

- **Raft consensus** — leader election, log replication, majority quorum writes, follower catch-up on rejoin
- **Write-ahead log (WAL)** — every mutation is persisted to disk before acknowledgment; crashed nodes replay WAL on restart
- **Consistent hashing** — virtual nodes distribute keys evenly across shards; the proxy routes requests without client-side topology knowledge
- **TTL-based expiry** — keys expire automatically; a background goroutine sweeps expired keys on a configurable interval
- **gRPC interface** — strongly typed client/server API with protobuf definitions; supports both unary and server-streaming RPCs
- **Goroutine-native concurrency** — each Raft role (election timer, heartbeat sender, log applier) runs as an independent goroutine communicating over channels
- **3-node Docker Compose cluster** — `docker compose up` starts a fully functional cluster with no manual setup

---

## Project Structure

```
kv-store/
├── cmd/
│   ├── node/
│   │   └── main.go          # Node entrypoint
│   └── proxy/
│       └── main.go          # Proxy / router entrypoint
├── internal/
│   ├── raft/
│   │   ├── node.go          # Raft state machine (leader/follower/candidate)
│   │   ├── log.go           # Raft log entries and replication
│   │   ├── election.go      # Leader election and heartbeat logic
│   │   └── rpc.go           # Inter-node gRPC communication
│   ├── store/
│   │   ├── store.go         # In-memory KV store
│   │   └── ttl.go           # TTL expiry background worker
│   ├── wal/
│   │   ├── wal.go           # Write-ahead log (append-only segment files)
│   │   └── replay.go        # WAL replay on node startup
│   ├── proxy/
│   │   ├── router.go        # Consistent hashing ring
│   │   └── handler.go       # gRPC proxy handler
│   └── config/
│       └── config.go        # Node and cluster configuration
├── proto/
│   ├── kv.proto             # KV store service definition
│   └── raft.proto           # Raft RPC service definition
├── docker-compose.yml       # 3-node cluster setup
├── Dockerfile
├── Makefile
└── README.md
```

---

## Quickstart

### Prerequisites

- Go 1.22+
- `protoc` and `protoc-gen-go` for regenerating protobuf bindings
- Docker and Docker Compose

### 1. Clone and build

```bash
git clone https://github.com/sagnikc395/kv-store.git
cd kv-store

go mod tidy
make build
```

### 2. Run a single node

```bash
./bin/node --id=1 --port=7001 --wal-dir=./data/node1
```

### 3. Run a 3-node cluster

```bash
docker compose up --build

# Nodes start on:
#   Node 1 (initial leader candidate)  localhost:7001
#   Node 2                             localhost:7002
#   Node 3                             localhost:7003
#   Proxy                              localhost:8000
```

### 4. Interact via CLI

```bash
# Set a key
grpcurl -plaintext -d '{"key": "foo", "value": "bar"}' \
  localhost:8000 kv.KVService/Set

# Get a key
grpcurl -plaintext -d '{"key": "foo"}' \
  localhost:8000 kv.KVService/Get

# Set with TTL (seconds)
grpcurl -plaintext -d '{"key": "session:abc", "value": "token", "ttl": 3600}' \
  localhost:8000 kv.KVService/Set

# Delete a key
grpcurl -plaintext -d '{"key": "foo"}' \
  localhost:8000 kv.KVService/Delete
```

### 5. Simulate a leader failure

```bash
# Kill the current leader
docker compose stop node1

# The remaining two nodes elect a new leader within ~300ms
# Writes continue uninterrupted through the proxy
docker compose start node1
# Node 1 rejoins, replays WAL, and catches up with the cluster
```

---

## API Reference

### KV Service (`kv.proto`)

| RPC | Request | Response | Description |
|-----|---------|----------|-------------|
| `Get` | `{key}` | `{value, found}` | Read a key |
| `Set` | `{key, value, ttl?}` | `{ok}` | Write a key with optional TTL |
| `Delete` | `{key}` | `{ok}` | Delete a key |
| `Watch` | `{key}` | stream `{key, value, event}` | Stream mutations on a key |

### Raft Service (`raft.proto`, internal)

| RPC | Description |
|-----|-------------|
| `RequestVote` | Candidate requests vote from peer |
| `AppendEntries` | Leader replicates log entries / sends heartbeat |

---

## Configuration

### Node flags

| Flag | Default | Description |
|------|---------|-------------|
| `--id` | required | Unique node ID within the cluster |
| `--port` | `7001` | gRPC listen port |
| `--peers` | `""` | Comma-separated peer addresses |
| `--wal-dir` | `./data` | Directory for WAL segment files |
| `--election-timeout-min` | `150ms` | Minimum randomized election timeout |
| `--election-timeout-max` | `300ms` | Maximum randomized election timeout |
| `--heartbeat-interval` | `50ms` | Leader heartbeat interval |
| `--ttl-sweep-interval` | `1s` | How often expired keys are evicted |

### Proxy flags

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8000` | Proxy listen port |
| `--nodes` | required | Comma-separated node addresses |
| `--virtual-nodes` | `150` | Virtual nodes per physical node in hash ring |

---

## How It Works

### Raft consensus

Each node is always in one of three states: **follower**, **candidate**, or **leader**.

Followers wait for heartbeats from the leader. If no heartbeat arrives within a randomized timeout (150–300ms), a follower becomes a candidate, increments its term, votes for itself, and sends `RequestVote` RPCs to peers. If it receives votes from a majority (≥2 of 3), it becomes leader and begins sending heartbeats.

The leader handles all writes. When a client sends a `Set`, the leader appends the entry to its local log, sends `AppendEntries` RPCs to followers in parallel, and waits for a majority to acknowledge before committing and responding to the client. Followers apply committed entries to their local KV stores in order.

If a follower's log falls behind (e.g. after a crash), the leader sends it all missing entries on the next `AppendEntries`. The follower replays them in order and rejoins the cluster fully caught up.

### Write-ahead log

Before any entry is applied to the in-memory store, it is appended to an append-only WAL segment file on disk. On startup, a node reads its WAL from the beginning and replays all committed entries to reconstruct its state. This ensures durability across process restarts without a full snapshot.

Segment files are rotated at a configurable size threshold. Old segments can be compacted once a snapshot is taken (snapshotting is on the roadmap).

### Consistent hashing

The proxy maintains a hash ring of virtual nodes (default 150 per physical node). When a request arrives, the proxy hashes the key and walks the ring clockwise to find the responsible node. Virtual nodes ensure keys distribute evenly even with a small cluster — without them, a 3-node cluster would leave two-thirds of the ring to whichever node lands at the largest arc.

Adding or removing a node only remaps the keys in the affected arc, not the entire keyspace.

---

## Roadmap

- [ ] Log compaction and snapshotting (truncate WAL after snapshot)
- [ ] Linearizable reads without going through leader (read index optimization)
- [ ] Membership changes (add/remove nodes without downtime)
- [ ] Prometheus metrics (replication lag, election count, WAL write latency)
- [ ] Benchmark suite (throughput vs replication factor, read/write latency under failure)

---

## License

MIT
