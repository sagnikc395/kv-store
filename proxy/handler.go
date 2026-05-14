package proxy

import (
	"net/rpc"
	"sync"
)

type GetArgs struct {
	Key string
}

type SetArgs struct {
	Key   string
	Value string
	TTL   float64
}

type DeleteArgs struct {
	Key string
}

type GetReply struct {
	Value string
	Found bool
}

type NodeClient interface {
	Call(serviceMethod string, args any, reply any) error
	Close() error
}

type nodeClient struct {
	*rpc.Client
}

type Handler struct {
	ring    *HashRing
	mu      sync.Mutex
	clients map[string]NodeClient
	dial    func(node string) (NodeClient, error)
}

func NewHandler(ring *HashRing) *Handler {
	return NewHandlerWithDialer(ring, func(node string) (NodeClient, error) {
		c, err := rpc.Dial("tcp", node)
		if err != nil {
			return nil, err
		}
		return nodeClient{Client: c}, nil
	})
}

func NewHandlerWithDialer(ring *HashRing, dial func(node string) (NodeClient, error)) *Handler {
	return &Handler{
		ring:    ring,
		clients: make(map[string]NodeClient),
		dial:    dial,
	}
}

func (h *Handler) Get(key string) (string, bool) {
	node, ok := h.ring.GetNode(key)
	if !ok {
		return "", false
	}

	var reply GetReply
	if err := h.clientCall(node, "KVService.Get", GetArgs{Key: key}, &reply); err != nil {
		return "", false
	}
	return reply.Value, reply.Found
}

func (h *Handler) Set(key, value string, ttlSeconds float64) bool {
	node, ok := h.ring.GetNode(key)
	if !ok {
		return false
	}
	return h.clientCall(node, "KVService.Set", SetArgs{
		Key:   key,
		Value: value,
		TTL:   ttlSeconds,
	}, new(bool)) == nil
}

func (h *Handler) Delete(key string) bool {
	node, ok := h.ring.GetNode(key)
	if !ok {
		return false
	}
	return h.clientCall(node, "KVService.Delete", DeleteArgs{Key: key}, new(bool)) == nil
}

func (h *Handler) clientCall(node, method string, args any, reply any) error {
	h.mu.Lock()
	client := h.clients[node]
	if client == nil {
		var err error
		client, err = h.dial(node)
		if err != nil {
			h.mu.Unlock()
			return err
		}
		h.clients[node] = client
	}
	h.mu.Unlock()

	return client.Call(method, args, reply)
}

func (h *Handler) CachedNodes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	nodes := make([]string, 0, len(h.clients))
	for node := range h.clients {
		nodes = append(nodes, node)
	}
	return nodes
}

func (h *Handler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for node, client := range h.clients {
		_ = client.Close()
		delete(h.clients, node)
	}
}
