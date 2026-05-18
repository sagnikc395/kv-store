package routing

import (
	"crypto/md5"
	"encoding/binary"
	"sort"
	"strconv"
	"sync"
)

type HashRing struct {
	mu           sync.RWMutex
	virtualNodes int
	ring         map[uint64]string
	sorted       []uint64
}

func NewHashRing(nodes []string, virtualNodes int) *HashRing {
	if virtualNodes <= 0 {
		virtualNodes = 100
	}
	r := &HashRing{virtualNodes: virtualNodes, ring: make(map[uint64]string)}
	for _, node := range nodes {
		r.Add(node)
	}
	return r
}

func (r *HashRing) Add(node string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := 0; i < r.virtualNodes; i++ {
		key := hash(node + "#" + strconv.Itoa(i))
		if _, exists := r.ring[key]; !exists {
			r.sorted = append(r.sorted, key)
		}
		r.ring[key] = node
	}
	sort.Slice(r.sorted, func(i, j int) bool { return r.sorted[i] < r.sorted[j] })
}

func (r *HashRing) Remove(node string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := 0; i < r.virtualNodes; i++ {
		key := hash(node + "#" + strconv.Itoa(i))
		delete(r.ring, key)
		idx := sort.Search(len(r.sorted), func(i int) bool { return r.sorted[i] >= key })
		if idx < len(r.sorted) && r.sorted[idx] == key {
			r.sorted = append(r.sorted[:idx], r.sorted[idx+1:]...)
		}
	}
}

func (r *HashRing) Get(key string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.sorted) == 0 {
		return "", false
	}
	h := hash(key)
	idx := sort.Search(len(r.sorted), func(i int) bool { return r.sorted[i] >= h })
	if idx == len(r.sorted) {
		idx = 0
	}
	return r.ring[r.sorted[idx]], true
}

func (r *HashRing) Nodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]struct{})
	nodes := make([]string, 0)
	for _, key := range r.sorted {
		node := r.ring[key]
		if _, ok := seen[node]; ok {
			continue
		}
		seen[node] = struct{}{}
		nodes = append(nodes, node)
	}
	return nodes
}

func hash(value string) uint64 {
	sum := md5.Sum([]byte(value))
	return binary.BigEndian.Uint64(sum[:8])
}
