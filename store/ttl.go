package store

import (
	"sync"
	"time"
)

// TTLWorker periodically removes expired keys from a KVStore.
type TTLWorker struct {
	store    *KVStore
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

func NewTTLWorker(store *KVStore, interval time.Duration) *TTLWorker {
	if interval <= 0 {
		interval = time.Second
	}
	return &TTLWorker{
		store:    store,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (w *TTLWorker) Start() {
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				w.store.PurgeExpired()
			case <-w.stop:
				return
			}
		}
	}()
}

func (w *TTLWorker) Stop() {
	w.once.Do(func() {
		close(w.stop)
		<-w.done
	})
}
