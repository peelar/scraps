package server

import (
	"context"
	"log/slog"
	"sync"

	"github.com/peelar/scraps/internal/provider"
	"github.com/peelar/scraps/internal/workspace"
)

// readyPool maintains one clean, running, unassigned provider runtime. Capacity
// failures are intentionally non-fatal: the normal provider Create path remains
// available and a later checkout/delete causes another replenishment attempt.
type readyPool struct {
	provider provider.Provider
	warm     provider.Preheater
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	readyID  string
	creating bool
	wg       sync.WaitGroup
}

func newReadyPool(runtime provider.Provider) *readyPool {
	warm, ok := runtime.(provider.Preheater)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &readyPool{provider: runtime, warm: warm, ctx: ctx, cancel: cancel}
	p.wg.Add(1)
	go func() { defer p.wg.Done(); p.reconcile() }()
	return p
}

func (p *readyPool) close() {
	if p == nil {
		return
	}
	p.cancel()
	p.wg.Wait()
}

func (p *readyPool) reconcile() {
	ready, err := p.warm.Ready(p.ctx)
	if err != nil {
		slog.Warn("inspect ready pool", "error", err)
		return
	}
	p.mu.Lock()
	if len(ready) > 0 {
		p.readyID = ready[0].ID
	}
	p.mu.Unlock()
	// Excess persisted spares can arise after a crash between creation and
	// publication. They are always safe to destroy.
	if len(ready) > 1 {
		for _, extra := range ready[1:] {
			if err := p.provider.Delete(p.ctx, extra.ID); err != nil {
				slog.Warn("delete excess ready runtime", "workspace", extra.ID, "error", err)
			}
		}
	}
	p.replenish()
}

func (p *readyPool) replenish() {
	p.mu.Lock()
	if p.readyID != "" || p.creating || p.ctx.Err() != nil {
		p.mu.Unlock()
		return
	}
	p.creating = true
	p.mu.Unlock()

	created, err := p.warm.Preheat(p.ctx)
	p.mu.Lock()
	p.creating = false
	if err == nil {
		p.readyID = created.ID
	}
	p.mu.Unlock()
	if err != nil && p.ctx.Err() == nil {
		slog.Warn("preheat ready runtime", "error", err)
	}
}

func (p *readyPool) create(ctx context.Context, options workspace.CreateOptions) (workspace.Workspace, error) {
	if p == nil {
		return p.provider.Create(ctx, options)
	}
	p.mu.Lock()
	id := p.readyID
	p.readyID = ""
	p.mu.Unlock()
	if id == "" {
		return p.provider.Create(ctx, options)
	}

	created, err := p.warm.Checkout(ctx, id, options)
	if err != nil {
		// A partially initialized runtime must never return to the pool.
		if deleteErr := p.provider.Delete(context.Background(), id); deleteErr != nil {
			slog.Warn("delete failed checkout", "workspace", id, "error", deleteErr)
		}
	}
	p.wg.Add(1)
	go func() { defer p.wg.Done(); p.replenish() }()
	return created, err
}
