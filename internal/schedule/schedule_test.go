package schedule

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/peelar/scraps/internal/store"
)

func TestNextUsesTimezone(t *testing.T) {
	after := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	next, err := Next("0 9 * * *", "America/New_York", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 1, 14, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %v, want %v", next, want)
	}
}

func TestTickCreatesCatchupOccurrences(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	due := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	item := store.Schedule{ID: "sch_tick", Name: "tick", CronExpression: "* * * * *", Timezone: "UTC",
		Enabled: true, Payload: []byte(`{}`), ConcurrencyPolicy: "queue", NextRunAt: &due}
	if err := st.CreateSchedule(ctx, item); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{store: st}
	if err := engine.Tick(ctx, due.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	occurrences, err := st.ListOccurrences(ctx, item.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(occurrences) != 3 {
		t.Fatalf("occurrence count = %d, want 3", len(occurrences))
	}
	updated, err := st.GetSchedule(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantNext := due.Add(3 * time.Minute)
	if updated.NextRunAt == nil || !updated.NextRunAt.Equal(wantNext) {
		t.Fatalf("next = %v, want %v", updated.NextRunAt, wantNext)
	}
}
