package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sagnikc395/kv-store/internal/routing"
	"github.com/sagnikc395/kv-store/internal/store"
)

type nodeDirectory struct {
	mu    sync.RWMutex
	nodes []string
	ring  *routing.HashRing
}

type clusterMembersReply struct {
	Members []struct {
		ID  int    `json:"id"`
		URL string `json:"url"`
	} `json:"members"`
}

type statusReply struct {
	IsLeader bool `json:"is_leader"`
}

func main() {
	var (
		addr            = flag.String("addr", ":8000", "HTTP listen address")
		nodesFlag       = flag.String("nodes", "", "comma-separated seed node base URLs")
		virtualNodes    = flag.Int("virtual-nodes", 100, "virtual nodes per physical node")
		timeout         = flag.Duration("timeout", 2*time.Second, "upstream request timeout")
		refreshInterval = flag.Duration("refresh-interval", 2*time.Second, "cluster membership refresh interval; 0 disables")
	)
	flag.Parse()

	nodes := splitCSV(*nodesFlag)
	dir := &nodeDirectory{nodes: nodes, ring: routing.NewHashRing(nodes, *virtualNodes)}
	client := &http.Client{Timeout: *timeout}
	if *refreshInterval > 0 {
		go refreshMembershipLoop(context.Background(), client, dir, *refreshInterval)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/kv/get", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		node, ok := dir.get(key)
		if !ok {
			http.Error(w, "no nodes available", http.StatusServiceUnavailable)
			return
		}
		forwardWithLeaderRetry(w, r.Context(), client, dir, node, http.MethodGet, "/kv/get?key="+url.QueryEscape(key), nil)
	})
	mux.HandleFunc("/kv/set", func(w http.ResponseWriter, r *http.Request) {
		var cmd store.Command
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		node, ok := dir.get(cmd.Key)
		if !ok {
			http.Error(w, "no nodes available", http.StatusServiceUnavailable)
			return
		}
		forwardJSON(w, r.Context(), client, dir, node, "/kv/set", cmd)
	})
	mux.HandleFunc("/kv/delete", func(w http.ResponseWriter, r *http.Request) {
		var cmd store.Command
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		node, ok := dir.get(cmd.Key)
		if !ok {
			http.Error(w, "no nodes available", http.StatusServiceUnavailable)
			return
		}
		forwardJSON(w, r.Context(), client, dir, node, "/kv/delete", cmd)
	})
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"nodes": dir.nodesList()})
	})

	log.Printf("kv-proxy addr=%s nodes=%v", *addr, nodes)
	if err := http.ListenAndServe(*addr, mux); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func forwardJSON(w http.ResponseWriter, ctx context.Context, client *http.Client, dir *nodeDirectory, node, path string, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	forwardWithLeaderRetry(w, ctx, client, dir, node, http.MethodPost, path, payload)
}

func forwardWithLeaderRetry(w http.ResponseWriter, ctx context.Context, client *http.Client, dir *nodeDirectory, node, method, path string, payload []byte) {
	resp, body, err := doForward(ctx, client, method, strings.TrimRight(node, "/")+path, payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if resp.StatusCode == http.StatusConflict {
		if leader, ok := findLeader(ctx, client, dir.nodesList()); ok && leader != node {
			resp, body, err = doForward(ctx, client, method, strings.TrimRight(leader, "/")+path, payload)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
	}
	copyResponse(w, resp, body)
}

func doForward(ctx context.Context, client *http.Client, method, upstream string, payload []byte) (*http.Response, []byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, upstream, body)
	if err != nil {
		return nil, nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp, data, err
}

func copyResponse(w http.ResponseWriter, resp *http.Response, body []byte) {
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func refreshMembershipLoop(ctx context.Context, client *http.Client, dir *nodeDirectory, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	refreshMembership(ctx, client, dir)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshMembership(ctx, client, dir)
		}
	}
}

func refreshMembership(ctx context.Context, client *http.Client, dir *nodeDirectory) {
	for _, node := range dir.nodesList() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(node, "/")+"/cluster/members", nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var reply clusterMembersReply
		err = json.NewDecoder(resp.Body).Decode(&reply)
		resp.Body.Close()
		if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}
		nodes := make([]string, 0, len(reply.Members))
		for _, member := range reply.Members {
			if member.URL != "" {
				nodes = append(nodes, strings.TrimRight(member.URL, "/"))
			}
		}
		if len(nodes) > 0 {
			dir.setNodes(nodes)
		}
		return
	}
}

func findLeader(ctx context.Context, client *http.Client, nodes []string) (string, bool) {
	for _, node := range nodes {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(node, "/")+"/status", nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var status statusReply
		err = json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && status.IsLeader {
			return node, true
		}
	}
	return "", false
}

func (d *nodeDirectory) get(key string) (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.ring.Get(key)
}

func (d *nodeDirectory) nodesList() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return append([]string(nil), d.nodes...)
}

func (d *nodeDirectory) setNodes(nodes []string) {
	seen := make(map[string]struct{}, len(nodes))
	unique := make([]string, 0, len(nodes))
	for _, node := range nodes {
		node = strings.TrimRight(strings.TrimSpace(node), "/")
		if node == "" {
			continue
		}
		if _, ok := seen[node]; ok {
			continue
		}
		seen[node] = struct{}{}
		unique = append(unique, node)
	}
	sort.Strings(unique)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nodes = unique
	d.ring.SetNodes(unique)
}

func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
