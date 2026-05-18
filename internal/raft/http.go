package raft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type HTTPTransport struct {
	PeerURLs map[int]string
	Client   *http.Client
}

func (t HTTPTransport) RequestVote(ctx context.Context, peerID int, args RequestVoteArgs) (RequestVoteReply, error) {
	var reply RequestVoteReply
	err := t.post(ctx, peerID, "/raft/request-vote", args, &reply)
	return reply, err
}

func (t HTTPTransport) AppendEntries(ctx context.Context, peerID int, args AppendEntriesArgs) (AppendEntriesReply, error) {
	var reply AppendEntriesReply
	err := t.post(ctx, peerID, "/raft/append-entries", args, &reply)
	return reply, err
}

func (t HTTPTransport) post(ctx context.Context, peerID int, path string, body any, out any) error {
	baseURL, ok := t.PeerURLs[peerID]
	if !ok {
		return fmt.Errorf("missing peer URL for id %d", peerID)
	}
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("peer %d returned %s", peerID, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func RegisterHTTPHandlers(mux *http.ServeMux, node *Node) {
	mux.HandleFunc("/raft/request-vote", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var args RequestVoteArgs
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, node.RequestVote(args))
	})
	mux.HandleFunc("/raft/append-entries", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var args AppendEntriesArgs
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, node.AppendEntries(args))
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
