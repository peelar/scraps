package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestScheduleOccurrenceLeaseLifecycle(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	due := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	next := due.Add(time.Hour)
	item := Schedule{ID: "sch_test", Name: "test", CronExpression: "0 * * * *", Timezone: "UTC", Enabled: true,
		Payload: []byte(`{"kind":"test"}`), ConcurrencyPolicy: "queue", NextRunAt: &due}
	if err := st.CreateSchedule(ctx, item); err != nil {
		t.Fatal(err)
	}
	stored, err := st.GetSchedule(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FireSchedule(ctx, stored, "occ_test", next); err != nil {
		t.Fatal(err)
	}

	now := due.Add(time.Minute)
	claimed, err := st.ClaimOccurrence(ctx, now, now.Add(5*time.Minute), "lease_one")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != "occ_test" || claimed.State != "leased" || claimed.Attempts != 1 || string(claimed.Payload) != `{"kind":"test"}` {
		t.Fatalf("claimed = %+v", claimed)
	}
	if err := st.CompleteOccurrence(ctx, claimed.ID, "wrong", "completed", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong lease error = %v", err)
	}
	if err := st.CompleteOccurrence(ctx, claimed.ID, claimed.LeaseToken, "completed", ""); err != nil {
		t.Fatal(err)
	}
	occurrences, err := st.ListOccurrences(ctx, item.ID, 10)
	if err != nil || len(occurrences) != 1 || occurrences[0].State != "completed" {
		t.Fatalf("occurrences = %+v, err = %v", occurrences, err)
	}
}

func TestExpiredOccurrenceCanBeReclaimed(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	next := due.Add(time.Hour)
	item := Schedule{ID: "sch_reclaim", Name: "test", CronExpression: "0 * * * *", Timezone: "UTC", Enabled: true,
		Payload: []byte(`{}`), ConcurrencyPolicy: "queue", NextRunAt: &due}
	if err := st.CreateSchedule(ctx, item); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.GetSchedule(ctx, item.ID)
	if err := st.FireSchedule(ctx, stored, "occ_reclaim", next); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimOccurrence(ctx, due, due.Add(time.Minute), "lease_old"); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := st.ClaimOccurrence(ctx, due.Add(2*time.Minute), due.Add(7*time.Minute), "lease_new")
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.LeaseToken != "lease_new" || reclaimed.Attempts != 2 {
		t.Fatalf("reclaimed = %+v", reclaimed)
	}
}

func TestSkipConcurrencyRecordsSkippedOccurrence(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	item := Schedule{ID: "sch_skip", Name: "test", CronExpression: "0 * * * *", Timezone: "UTC", Enabled: true,
		Payload: []byte(`{}`), ConcurrencyPolicy: "skip", NextRunAt: &due}
	if err := st.CreateSchedule(ctx, item); err != nil {
		t.Fatal(err)
	}
	first, _ := st.GetSchedule(ctx, item.ID)
	if err := st.FireSchedule(ctx, first, "occ_first", due.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	second, _ := st.GetSchedule(ctx, item.ID)
	if err := st.FireSchedule(ctx, second, "occ_second", due.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	occurrences, err := st.ListOccurrences(ctx, item.ID, 10)
	if err != nil || len(occurrences) != 2 {
		t.Fatalf("occurrences = %+v, err = %v", occurrences, err)
	}
	states := map[string]string{}
	for _, occurrence := range occurrences {
		states[occurrence.ID] = occurrence.State
	}
	if states["occ_first"] != "pending" || states["occ_second"] != "skipped" {
		t.Fatalf("states = %#v", states)
	}
}
