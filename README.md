# kv-store

`kv-store` is a Go implementation of a small distributed key-value store. I built it to practice the pieces that sit behind durable, replicated storage systems: command application, write-ahead logging, leader election, log replication, TTL cleanup, and key routing.



- Simple in-memory key-value stores are fast, but they lose data on restart and become hard to scale once traffic has to be split across nodes. I wanted this project to go past a basic map-backed store and explore the systems work needed to keep writes ordered, recoverable, and routable across a small cluster.

- The goal was to build the core building blocks of a distributed KV store without hiding the interesting parts behind a large framework. The project needed to support basic `set`, `get`, and `delete` behavior, optional per-key TTLs, durable replay from disk, Raft-style consensus for replicated writes, and consistent hashing for routing keys to nodes.

- I split the project into focused Go packages:

- `store`: thread-safe in-memory storage with TTL-aware reads, deletes, key listing, and a background expiry worker.
- `wal`: append-only JSON-lines write-ahead log with replay support for restoring committed commands.
- `raft`: a compact Raft consensus module with follower, candidate, leader, and dead states, randomized elections, vote requests, heartbeats, log replication, commit notification, and leader step-down when quorum is lost.
- `proxy`: a consistent hash ring with virtual nodes plus an RPC-aware handler that routes key operations to the node responsible for that key.

The implementation keeps the command format shared across the store, WAL, and Raft layers so committed entries can flow through the system cleanly. Writes are submitted through the leader, replicated with `AppendEntries`, committed after quorum agreement, and then applied to the store. TTL cleanup is handled separately so normal reads and writes stay simple.

- The result is a small but complete distributed storage prototype that demonstrates the main mechanics behind a replicated key-value system. It can:

  - Store, read, delete, and expire keys in memory.
  - Persist committed operations to a WAL and replay them after restart.
  - Elect a leader and replicate log entries across peers using Go RPC.
  - Route keys across nodes with consistent hashing and virtual nodes.
  - Compile cleanly across all current packages with `go test ./...`.

## Repository Layout

```text
.
|-- store/   In-memory KV store, command application, and TTL worker
|-- wal/     Append-only JSON-lines WAL and replay logic
|-- raft/    Raft state machine, RPC server, elections, replication, commits
|-- proxy/   Consistent hash ring and RPC routing handler
|-- go.mod   Go module definition
`-- README.md
```

## Tech Stack

- Go 1.26.3
- Standard library networking with `net/rpc`
- JSON-lines WAL records
- Consistent hashing with virtual nodes

## Running Checks

```bash
go test ./...
```

At the moment the repository is organized as reusable packages rather than a finished command-line service. The test command is still useful because it builds every package and catches compile-time integration issues across the store, WAL, Raft, and proxy layers.

## License

MIT
