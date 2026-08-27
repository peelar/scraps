package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/peelar/scraps/internal/provider"
	"github.com/peelar/scraps/internal/workspace"
)

type poolProvider struct {
	provider.Provider
	mu       sync.Mutex
	ready    []workspace.Workspace
	preheats int
	creates  int
	deleted  []string
}

func (f *poolProvider) Create(_ context.Context, opt workspace.CreateOptions) (workspace.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	return workspace.Workspace{ID: "fresh", Project: opt.Project, RepoURL: opt.RepoURL, State: "running"}, nil
}
func (f *poolProvider) Preheat(context.Context) (workspace.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preheats++
	w := workspace.Workspace{ID: "warm-" + string(rune('0'+f.preheats)), State: "preheated"}
	f.ready = append(f.ready, w)
	return w, nil
}
func (f *poolProvider) Ready(context.Context) ([]workspace.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]workspace.Workspace(nil), f.ready...), nil
}
func (f *poolProvider) Checkout(_ context.Context, id string, opt workspace.CreateOptions) (workspace.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, w := range f.ready {
		if w.ID == id {
			f.ready = append(f.ready[:i], f.ready[i+1:]...)
			break
		}
	}
	return workspace.Workspace{ID: id, Project: opt.Project, RepoURL: opt.RepoURL, State: "running"}, nil
}
func (f *poolProvider) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	for i, w := range f.ready {
		if w.ID == id {
			f.ready = append(f.ready[:i], f.ready[i+1:]...)
			break
		}
	}
	return nil
}

func waitFor(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not reached")
}

func TestReadyPoolPreheatsChecksOutAndReplenishes(t *testing.T) {
	f := &poolProvider{}
	p := newReadyPool(f)
	defer p.close()
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.preheats == 1 })

	created, err := p.create(context.Background(), workspace.CreateOptions{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "warm-1" || created.Project != "demo" {
		t.Fatalf("created = %+v", created)
	}
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.preheats == 2 && len(f.ready) == 1 })
}

func TestReadyPoolCreatesRepositoryWorkspaceFresh(t *testing.T) {
	f := &poolProvider{}
	p := newReadyPool(f)
	defer p.close()
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.preheats == 1 })

	created, err := p.create(context.Background(), workspace.CreateOptions{Project: "demo", RepoURL: "https://github.com/owner/repo.git"})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if created.ID != "fresh" || f.creates != 1 || len(f.ready) != 1 {
		t.Fatalf("created=%+v creates=%d ready=%+v", created, f.creates, f.ready)
	}
}

func TestReadyPoolReclaimsExcessPersistedSpares(t *testing.T) {
	f := &poolProvider{ready: []workspace.Workspace{{ID: "keep", State: "preheated"}, {ID: "excess", State: "preheated"}}}
	p := newReadyPool(f)
	defer p.close()
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.deleted) == 1 })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleted[0] != "excess" || f.preheats != 0 {
		t.Fatalf("deleted=%v preheats=%d", f.deleted, f.preheats)
	}
}
