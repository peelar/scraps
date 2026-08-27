package store

import (
	"context"
	"errors"
	"testing"
)

func TestOnlyOneActiveRunPerWorkspace(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.CreateWorkspace(ctx, Workspace{ID: "ws", State: "running"}); err != nil {
		t.Fatal(err)
	}
	first := Run{ID: "first", WorkspaceID: "ws", SessionKey: "s", Prompt: "one"}
	if err := st.CreateRun(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, Run{ID: "second", WorkspaceID: "ws", SessionKey: "s", Prompt: "two"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active run error = %v, want ErrConflict", err)
	}
	if err := st.FinishRun(ctx, first.ID, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, Run{ID: "second", WorkspaceID: "ws", SessionKey: "s", Prompt: "two"}); err != nil {
		t.Fatalf("run after terminal state: %v", err)
	}
}

func TestReconcileInterruptedRuns(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.CreateWorkspace(ctx, Workspace{ID: "queued-ws", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkspace(ctx, Workspace{ID: "running-ws", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, Run{ID: "queued", WorkspaceID: "queued-ws", SessionKey: "s", Prompt: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, Run{ID: "running", WorkspaceID: "running-ws", SessionKey: "s", Prompt: "two"}); err != nil {
		t.Fatal(err)
	}
	if err := st.StartRun(ctx, "running"); err != nil {
		t.Fatal(err)
	}
	count, err := st.ReconcileInterruptedRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("reconciled %d runs, want 2", count)
	}
	for _, id := range []string{"queued", "running"} {
		run, err := st.GetRun(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if run.State != "failed" || run.FinishedAt == nil || run.Error == "" {
			t.Fatalf("run %s was not reconciled: %+v", id, run)
		}
	}
	count, err = st.ReconcileInterruptedRuns(ctx)
	if err != nil || count != 0 {
		t.Fatalf("second reconciliation = %d, %v; want 0, nil", count, err)
	}
}
