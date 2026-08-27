package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestScheduleAPIAndOccurrenceLease(t *testing.T) {
	ts := newTestServer(t)
	ts.clock.Close()
	createdResponse := ts.do(t, http.MethodPost, "/v1/schedules", map[string]any{
		"name": "nightly audit", "cron": "0 2 * * *", "timezone": "UTC",
		"payload": map[string]any{"kind": "audit"}, "concurrencyPolicy": "queue",
	}, "")
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created scheduleResponse
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.NextRunAt == nil || string(created.Payload) != `{"kind":"audit"}` {
		t.Fatalf("created = %+v", created)
	}

	patchResponse := ts.do(t, http.MethodPatch, "/v1/schedules/"+created.ID, map[string]any{"name": "daily audit"}, "")
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("patch status = %d: %s", patchResponse.Code, patchResponse.Body.String())
	}

	stored, err := ts.store.GetSchedule(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	stored.NextRunAt = &due
	if err := ts.store.UpdateSchedule(t.Context(), stored); err != nil {
		t.Fatal(err)
	}
	if err := ts.clock.Tick(t.Context(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	claimResponse := ts.do(t, http.MethodPost, "/v1/schedule-occurrences/claim", map[string]any{"leaseSeconds": 60}, "")
	if claimResponse.Code != http.StatusOK {
		t.Fatalf("claim status = %d: %s", claimResponse.Code, claimResponse.Body.String())
	}
	var claimed occurrenceResponse
	if err := json.NewDecoder(claimResponse.Body).Decode(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.ScheduleID != created.ID || claimed.LeaseToken == "" || string(claimed.Payload) != `{"kind":"audit"}` {
		t.Fatalf("claimed = %+v", claimed)
	}
	renewed := ts.do(t, http.MethodPost, "/v1/schedule-occurrences/"+claimed.ID+"/renew", map[string]any{
		"leaseToken": claimed.LeaseToken, "leaseSeconds": 120,
	}, "")
	if renewed.Code != http.StatusNoContent {
		t.Fatalf("renew status = %d: %s", renewed.Code, renewed.Body.String())
	}

	wrong := ts.do(t, http.MethodPost, "/v1/schedule-occurrences/"+claimed.ID+"/complete", map[string]any{
		"leaseToken": "wrong", "state": "completed",
	}, "")
	if wrong.Code != http.StatusConflict {
		t.Fatalf("wrong lease status = %d: %s", wrong.Code, wrong.Body.String())
	}
	completed := ts.do(t, http.MethodPost, "/v1/schedule-occurrences/"+claimed.ID+"/complete", map[string]any{
		"leaseToken": claimed.LeaseToken, "state": "completed",
	}, "")
	if completed.Code != http.StatusNoContent {
		t.Fatalf("complete status = %d: %s", completed.Code, completed.Body.String())
	}

	listed := ts.do(t, http.MethodGet, "/v1/schedule-occurrences?scheduleId="+created.ID, nil, "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", listed.Code, listed.Body.String())
	}
	var occurrences struct {
		Occurrences []occurrenceResponse `json:"occurrences"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&occurrences); err != nil {
		t.Fatal(err)
	}
	if len(occurrences.Occurrences) != 1 || occurrences.Occurrences[0].State != "completed" || occurrences.Occurrences[0].LeaseToken != "" {
		t.Fatalf("occurrences = %+v", occurrences.Occurrences)
	}
}

func TestScheduleValidation(t *testing.T) {
	ts := newTestServer(t)
	response := ts.do(t, http.MethodPost, "/v1/schedules", map[string]any{
		"name": "bad", "cron": "not cron", "timezone": "UTC",
	}, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}
