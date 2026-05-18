package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagnikc395/kv-store/internal/raft"
	"github.com/sagnikc395/kv-store/internal/store"
	"github.com/sagnikc395/kv-store/internal/wal"
)

type appliedTracker struct {
	mu    sync.Mutex
	cond  *sync.Cond
	index int
}

func newAppliedTracker() *appliedTracker {
	t := &appliedTracker{index: -1}
	t.cond = sync.NewCond(&t.mu)
	return t
}

func (t *appliedTracker) mark(index int) {
	t.mu.Lock()
	t.index = index
	t.cond.Broadcast()
	t.mu.Unlock()
}

func (t *appliedTracker) wait(ctx context.Context, index int) bool {
	done := make(chan struct{})
	go func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		for t.index < index {
			t.cond.Wait()
		}
		close(done)
	}()
	select {
	case <-ctx.Done():
		return false
	case <-done:
		return true
	}
}

func main() {
	var (
		id            = flag.Int("id", 1, "node id")
		addr          = flag.String("addr", ":7001", "HTTP listen address")
		walDir        = flag.String("wal-dir", "./data/node1", "WAL and raft metadata directory")
		peerFlag      = flag.String("peers", "", "comma-separated peers as id=url, e.g. 2=http://127.0.0.1:7002")
		ttlInterval   = flag.Duration("ttl-interval", time.Second, "TTL cleanup interval")
		compactEvery  = flag.Int("compact-every", 1000, "compact WAL after this many committed mutations; 0 disables")
		commitTimeout = flag.Duration("commit-timeout", 2*time.Second, "write wait timeout")
	)
	flag.Parse()

	peerIDs, peerURLs, err := parsePeers(*peerFlag)
	if err != nil {
		log.Fatal(err)
	}

	kv := store.New()
	snapshot, commands, err := wal.Recover(*walDir)
	if err != nil {
		log.Fatal(err)
	}
	kv.Restore(snapshot)
	for _, cmd := range commands {
		kv.Apply(cmd)
	}

	logFile, err := wal.Open(*walDir)
	if err != nil {
		log.Fatal(err)
	}
	defer logFile.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go store.NewTTLWorker(kv, *ttlInterval).Run(ctx)

	node, err := raft.NewNode(raft.Config{
		ID:        *id,
		Peers:     peerIDs,
		Transport: raft.HTTPTransport{PeerURLs: peerURLs, Client: &http.Client{Timeout: 750 * time.Millisecond}},
		Storage:   raft.NewFileStorage(*walDir),
	})
	if err != nil {
		log.Fatal(err)
	}
	node.Start()
	defer node.Stop()

	tracker := newAppliedTracker()
	go applyCommitted(node, kv, logFile, tracker, *compactEvery)

	mux := http.NewServeMux()
	raft.RegisterHTTPHandlers(mux, node)
	registerKVHandlers(mux, node, kv, tracker, *commitTimeout)

	log.Printf("kv-node id=%d addr=%s peers=%v replayed=%d", *id, *addr, peerURLs, len(commands))
	if err := http.ListenAndServe(*addr, mux); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func applyCommitted(node *raft.Node, kv *store.Store, logFile *wal.WAL, tracker *appliedTracker, compactEvery int) {
	applied := -1
	sinceCompact := 0
	for entry := range node.CommitChan() {
		if err := logFile.Append(entry.Command); err != nil {
			log.Printf("wal append failed: %v", err)
			continue
		}
		kv.Apply(entry.Command)
		applied++
		sinceCompact++
		tracker.mark(applied)

		if compactEvery > 0 && sinceCompact >= compactEvery {
			if err := logFile.Compact(kv.Snapshot()); err != nil {
				log.Printf("wal compaction failed: %v", err)
			} else {
				sinceCompact = 0
			}
		}
	}
}

func registerKVHandlers(mux *http.ServeMux, node *raft.Node, kv *store.Store, tracker *appliedTracker, commitTimeout time.Duration) {
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, node.Report())
	})
	mux.HandleFunc("/kv/get", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "missing key", http.StatusBadRequest)
			return
		}
		value, ok := kv.Get(key)
		writeJSON(w, map[string]any{"found": ok, "value": value})
	})
	mux.HandleFunc("/kv/keys", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": kv.Keys()})
	})
	mux.HandleFunc("/kv/set", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var cmd store.Command
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cmd.Op = "set"
		cmd = store.DurableCommand(cmd, time.Now())
		submitAndWait(w, r, node, tracker, cmd, commitTimeout)
	})
	mux.HandleFunc("/kv/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var cmd store.Command
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cmd.Op = "delete"
		submitAndWait(w, r, node, tracker, cmd, commitTimeout)
	})
}

func submitAndWait(w http.ResponseWriter, r *http.Request, node *raft.Node, tracker *appliedTracker, cmd store.Command, timeout time.Duration) {
	index, ok := node.SubmitWithIndex(cmd)
	if !ok {
		http.Error(w, "not leader", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	if !tracker.wait(ctx, index) {
		http.Error(w, "commit timeout", http.StatusGatewayTimeout)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "index": index})
}

func parsePeers(raw string) ([]int, map[int]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, map[int]string{}, nil
	}
	var ids []int
	urls := make(map[int]string)
	for _, part := range strings.Split(raw, ",") {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) != 2 {
			return nil, nil, fmt.Errorf("invalid peer %q, expected id=url", part)
		}
		id, err := strconv.Atoi(pair[0])
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		urls[id] = pair[1]
	}
	return ids, urls, nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
