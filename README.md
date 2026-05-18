# kv-store

A distributed in-memory key-value store written in **Go** using goroutines,
channels, HTTP JSON RPC, durable Raft metadata, and WAL snapshot compaction.

## Features

- **Concurrent core:** Raft elections, heartbeats, log replication, TTL cleanup,
  and commit application run as goroutines coordinated with mutexes and channels.
- **HTTP JSON transport:** Node, proxy, and Raft traffic use plain HTTP+JSON.
- **Durable Raft metadata:** each node persists `currentTerm`, `votedFor`, and
  the replicated log in `raft_state.json`.
- **WAL compaction:** the append-only WAL can be compacted into `snapshot.json`
  so replay time and disk usage do not grow forever.
- **Crash-safe TTLs:** TTL writes are converted to absolute expiration timestamps
  before replication/WAL append, so a restart does not accidentally extend a key's
  lifetime.
- **Leader-committed writes:** the node API waits for accepted writes to be
  applied locally before returning success.

## Architecture

```text
Client
  |
  v
HTTP proxy (:8000)
  |
  v
Consistent hash ring
  |
  +--> Go node (:7001) -- Raft HTTP --> peers
  +--> Go node (:7002) -- Raft HTTP --> peers
  +--> Go node (:7003) -- Raft HTTP --> peers

Each node:
  HTTP KV API -> Raft log -> quorum commit -> WAL append -> in-memory store
                                     |
                                     +-> snapshot compaction
```

## Project Layout

```text
cmd/
  kv-node/       node executable
  kv-proxy/      proxy executable
internal/
  raft/          Raft election, replication, persistence, HTTP transport
  routing/       consistent hashing ring
  store/         concurrent in-memory KV store and TTL worker
  wal/           JSONL WAL, replay, snapshot compaction
```

## Run Tests

```bash
go test ./...
```

## Run A Local Cluster

In separate terminals:

```bash
go run ./cmd/kv-node --id=1 --addr=:7001 --wal-dir=./data/node1 \
  --peers=2=http://127.0.0.1:7002,3=http://127.0.0.1:7003

go run ./cmd/kv-node --id=2 --addr=:7002 --wal-dir=./data/node2 \
  --peers=1=http://127.0.0.1:7001,3=http://127.0.0.1:7003

go run ./cmd/kv-node --id=3 --addr=:7003 --wal-dir=./data/node3 \
  --peers=1=http://127.0.0.1:7001,2=http://127.0.0.1:7002

go run ./cmd/kv-proxy --addr=:8000 \
  --nodes=http://127.0.0.1:7001,http://127.0.0.1:7002,http://127.0.0.1:7003
```

Example requests:

```bash
# set a key
curl -X POST http://127.0.0.1:8000/kv/set \
  -H 'Content-Type: application/json' \
  -d '{"key":"foo","value":"bar"}'

# get a key
curl 'http://127.0.0.1:8000/kv/get?key=foo'

# delete a key
curl -X POST http://127.0.0.1:8000/kv/delete \
  -H 'Content-Type: application/json' \
  -d '{"key":"foo"}'

# set with TTL (seconds)
curl -X POST http://127.0.0.1:8000/kv/set \
  -H 'Content-Type: application/json' \
  -d '{"key":"tmp","value":"gone-soon","ttl":5}'
```

## Docker Compose

```bash
docker compose up --build
```

Exposed ports:

- Node 1: `localhost:7001`
- Node 2: `localhost:7002`
- Node 3: `localhost:7003`
- Proxy:  `localhost:8000`

## Remaining Limitations

- Dynamic Raft membership is not implemented.
- The proxy uses static node membership.
- Snapshotting compacts the local KV WAL; Raft log snapshot installation is not yet implemented.
- Reads are local to the selected node; a full read-index path is future work.

## License

MIT
