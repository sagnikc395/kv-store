package proxy

import (
	"crypto/md5"
	"encoding/binary"
	"sort"
	"strconv"
	"sync"
)

// HashRing maps arbitrary keys to node addresses using virtual nodes.
type HashRing struct {
	virtualNodes int
	mu           sync.Mutex
	ring         map[uint64]string
	sortedKeys   []uint64
}

func NewHashRing(nodes []string, virtualNodes int) *HashRing {
	if virtualNodes <= 0 {
		virtualNodes = 100
	}
	r := &HashRing{
		virtualNodes: virtualNodes,
		ring:         make(map[uint64]string),
	}
	for _, node := range nodes {
		r.AddNode(node)
	}
	return r
}

func (r *HashRing) AddNode(node string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := 0; i < r.virtualNodes; i++ {
		h := hash(node + "#" + strconv.Itoa(i))
		if _, exists := r.ring[h]; !exists {
			r.sortedKeys = append(r.sortedKeys, h)
		}
		r.ring[h] = node
	}
	sort.Slice(r.sortedKeys, func(i, j int) bool {
		return r.sortedKeys[i] < r.sortedKeys[j]
	})
}

func (r *HashRing) RemoveNode(node string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := 0; i < r.virtualNodes; i++ {
		h := hash(node + "#" + strconv.Itoa(i))
		if _, exists := r.ring[h]; exists {
			delete(r.ring, h)
			idx := sort.Search(len(r.sortedKeys), func(i int) bool {
				return r.sortedKeys[i] >= h
			})
			if idx < len(r.sortedKeys) && r.sortedKeys[idx] == h {
				r.sortedKeys = append(r.sortedKeys[:idx], r.sortedKeys[idx+1:]...)
			}
		}
	}
}

func (r *HashRing) GetNode(key string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.sortedKeys) == 0 {
		return "", false
	}
	h := hash(key)
	idx := sort.Search(len(r.sortedKeys), func(i int) bool {
		return r.sortedKeys[i] >= h
	})
	if idx == len(r.sortedKeys) {
		idx = 0
	}
	return r.ring[r.sortedKeys[idx]], true
}

func (r *HashRing) Nodes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	seen := make(map[string]struct{})
	nodes := make([]string, 0)
	for _, key := range r.sortedKeys {
		node := r.ring[key]
		if _, ok := seen[node]; ok {
			continue
		}
		seen[node] = struct{}{}
		nodes = append(nodes, node)
	}
	return nodes
}

func hash(data string) uint64 {
	sum := md5.Sum([]byte(data))
	return binary.BigEndian.Uint64(sum[:8])
}
