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
	"strings"
	"time"

	"github.com/sagnikc395/kv-store/internal/routing"
	"github.com/sagnikc395/kv-store/internal/store"
)

func main() {
	var (
		addr         = flag.String("addr", ":8000", "HTTP listen address")
		nodesFlag    = flag.String("nodes", "", "comma-separated node base URLs")
		virtualNodes = flag.Int("virtual-nodes", 100, "virtual nodes per physical node")
		timeout      = flag.Duration("timeout", 2*time.Second, "upstream request timeout")
	)
	flag.Parse()

	nodes := splitCSV(*nodesFlag)
	ring := routing.NewHashRing(nodes, *virtualNodes)
	client := &http.Client{Timeout: *timeout}

	mux := http.NewServeMux()
	mux.HandleFunc("/kv/get", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		node, ok := ring.Get(key)
		if !ok {
			http.Error(w, "no nodes available", http.StatusServiceUnavailable)
			return
		}
		forward(w, r.Context(), client, http.MethodGet, strings.TrimRight(node, "/")+"/kv/get?key="+url.QueryEscape(key), nil)
	})
	mux.HandleFunc("/kv/set", func(w http.ResponseWriter, r *http.Request) {
		var cmd store.Command
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		node, ok := ring.Get(cmd.Key)
		if !ok {
			http.Error(w, "no nodes available", http.StatusServiceUnavailable)
			return
		}
		forwardJSON(w, r.Context(), client, strings.TrimRight(node, "/")+"/kv/set", cmd)
	})
	mux.HandleFunc("/kv/delete", func(w http.ResponseWriter, r *http.Request) {
		var cmd store.Command
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		node, ok := ring.Get(cmd.Key)
		if !ok {
			http.Error(w, "no nodes available", http.StatusServiceUnavailable)
			return
		}
		forwardJSON(w, r.Context(), client, strings.TrimRight(node, "/")+"/kv/delete", cmd)
	})
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"nodes": ring.Nodes()})
	})

	log.Printf("kv-proxy addr=%s nodes=%v", *addr, nodes)
	if err := http.ListenAndServe(*addr, mux); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func forwardJSON(w http.ResponseWriter, ctx context.Context, client *http.Client, upstream string, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	forward(w, ctx, client, http.MethodPost, upstream, payload)
}

func forward(w http.ResponseWriter, ctx context.Context, client *http.Client, method, upstream string, payload []byte) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, upstream, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
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
