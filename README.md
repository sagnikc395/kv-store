# kv-store

A distributed in-memory key-value store written in **Go** using goroutines,
channels, HTTP JSON RPC, durable Raft metadata, and WAL snapshot compaction.

## Features

- **Concurrent core:** Raft elections, heartbeats, log replication, TTL cleanup,
  and commit application run as goroutines coordinated with mutexes and channels.
- **HTTP JSON transport:** Node, proxy, and Raft traffic use plain HTTP+JSON.
- **Durable Raft metadata:** each node persists `currentTerm`, `votedFor`,
  membership, snapshots, and the retained replicated log in `raft_state.json`.
- **WAL compaction:** the append-only WAL can be compacted into `snapshot.json`
  so replay time and disk usage do not grow forever.
- **Raft snapshot install:** lagging followers can receive installed KV
  snapshots and continue replication from the compacted Raft log boundary.
- **Dynamic membership:** leaders replicate single-step add/remove membership
  changes through the Raft log.
- **Read index:** node reads first confirm leader quorum and wait for the local
  store to apply through the returned read index.
- **Dynamic proxy discovery:** the proxy refreshes its node ring from
  `/cluster/members` and retries leader-only operations against the elected
  leader.
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
  --advertise-url=http://127.0.0.1:7001 \
  --peers=2=http://127.0.0.1:7002,3=http://127.0.0.1:7003

go run ./cmd/kv-node --id=2 --addr=:7002 --wal-dir=./data/node2 \
  --advertise-url=http://127.0.0.1:7002 \
  --peers=1=http://127.0.0.1:7001,3=http://127.0.0.1:7003

go run ./cmd/kv-node --id=3 --addr=:7003 --wal-dir=./data/node3 \
  --advertise-url=http://127.0.0.1:7003 \
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

# add a node through the Raft leader
curl -X POST http://127.0.0.1:7001/cluster/add \
  -H 'Content-Type: application/json' \
  -d '{"id":4,"url":"http://127.0.0.1:7004"}'

# remove a node through the Raft leader
curl -X POST http://127.0.0.1:7001/cluster/remove \
  -H 'Content-Type: application/json' \
  -d '{"id":4}'
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

## Notes

- Membership changes are single-step add/remove operations, not joint-consensus
  reconfiguration.
- Newly added nodes still need to be started with enough peer seed URLs to
  contact the current cluster.

## License

MIT
