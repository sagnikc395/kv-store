package store

import (
	"context"
	"time"
)

type TTLWorker struct {
	store    *Store
	interval time.Duration
}

func NewTTLWorker(store *Store, interval time.Duration) *TTLWorker {
	if interval <= 0 {
		interval = time.Second
	}
	return &TTLWorker{store: store, interval: interval}
}

func (w *TTLWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.store.PurgeExpired()
		}
	}
}
