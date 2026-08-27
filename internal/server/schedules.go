package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/peelar/scraps/internal/schedule"
	"github.com/peelar/scraps/internal/store"
)

const maxSchedulePayload = 64 << 10

type scheduleRequest struct {
	Name              string          `json:"name"`
	CronExpression    string          `json:"cron"`
	Timezone          string          `json:"timezone"`
	Enabled           *bool           `json:"enabled,omitempty"`
	Payload           json.RawMessage `json:"payload,omitempty"`
	ConcurrencyPolicy string          `json:"concurrencyPolicy"`
}

type schedulePatch struct {
	Name              *string          `json:"name,omitempty"`
	CronExpression    *string          `json:"cron,omitempty"`
	Timezone          *string          `json:"timezone,omitempty"`
	Enabled           *bool            `json:"enabled,omitempty"`
	Payload           *json.RawMessage `json:"payload,omitempty"`
	ConcurrencyPolicy *string          `json:"concurrencyPolicy,omitempty"`
}

type scheduleResponse struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	CronExpression    string          `json:"cron"`
	Timezone          string          `json:"timezone"`
	Enabled           bool            `json:"enabled"`
	Payload           json.RawMessage `json:"payload"`
	ConcurrencyPolicy string          `json:"concurrencyPolicy"`
	NextRunAt         *time.Time      `json:"nextRunAt"`
	LastRunAt         *time.Time      `json:"lastRunAt"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

type occurrenceResponse struct {
	ID          string          `json:"id"`
	ScheduleID  string          `json:"scheduleId"`
	ScheduledAt time.Time       `json:"scheduledAt"`
	State       string          `json:"state"`
	LeaseToken  string          `json:"leaseToken,omitempty"`
	LeaseUntil  *time.Time      `json:"leaseUntil,omitempty"`
	Attempts    int             `json:"attempts"`
	Error       string          `json:"error,omitempty"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	var body scheduleRequest
	if err := decodeBody(r, &body); err != nil {
		writeAPIError(w, err)
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if body.Timezone == "" {
		body.Timezone = "UTC"
	}
	if body.ConcurrencyPolicy == "" {
		body.ConcurrencyPolicy = "queue"
	}
	if len(body.Payload) == 0 {
		body.Payload = json.RawMessage(`{}`)
	}
	if err := validateSchedule(body.Name, body.CronExpression, body.Timezone, body.ConcurrencyPolicy, body.Payload); err != nil {
		writeAPIError(w, err)
		return
	}
	id, err := schedule.ID("sch_")
	if err != nil {
		writeAPIError(w, err)
		return
	}
	item := store.Schedule{ID: id, Name: strings.TrimSpace(body.Name), CronExpression: body.CronExpression,
		Timezone: body.Timezone, Enabled: enabled, Payload: body.Payload, ConcurrencyPolicy: body.ConcurrencyPolicy}
	if enabled {
		next, err := schedule.Next(item.CronExpression, item.Timezone, time.Now().UTC())
		if err != nil {
			writeAPIError(w, invalidSchedule(err.Error()))
			return
		}
		item.NextRunAt = &next
	}
	if err := s.store.CreateSchedule(r.Context(), item); err != nil {
		writeAPIError(w, err)
		return
	}
	created, err := s.store.GetSchedule(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, publicSchedule(created))
}

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListSchedules(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	out := make([]scheduleResponse, 0, len(items))
	for _, item := range items {
		out = append(out, publicSchedule(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": out})
}

func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicSchedule(item))
}

func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetSchedule(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var patch schedulePatch
	if err := decodeBody(r, &patch); err != nil {
		writeAPIError(w, err)
		return
	}
	recompute := false
	if patch.Name != nil {
		item.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.CronExpression != nil {
		item.CronExpression = *patch.CronExpression
		recompute = true
	}
	if patch.Timezone != nil {
		item.Timezone = *patch.Timezone
		recompute = true
	}
	if patch.Payload != nil {
		item.Payload = *patch.Payload
	}
	if patch.ConcurrencyPolicy != nil {
		item.ConcurrencyPolicy = *patch.ConcurrencyPolicy
	}
	if patch.Enabled != nil {
		if !item.Enabled && *patch.Enabled {
			recompute = true
		}
		item.Enabled = *patch.Enabled
	}
	if err := validateSchedule(item.Name, item.CronExpression, item.Timezone, item.ConcurrencyPolicy, item.Payload); err != nil {
		writeAPIError(w, err)
		return
	}
	if !item.Enabled {
		item.NextRunAt = nil
	} else if recompute {
		next, err := schedule.Next(item.CronExpression, item.Timezone, time.Now().UTC())
		if err != nil {
			writeAPIError(w, invalidSchedule(err.Error()))
			return
		}
		item.NextRunAt = &next
	}
	if err := s.store.UpdateSchedule(r.Context(), item); err != nil {
		writeAPIError(w, err)
		return
	}
	updated, err := s.store.GetSchedule(r.Context(), item.ID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicSchedule(updated))
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSchedule(r.Context(), r.PathValue("id")); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listScheduleOccurrences(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.ListOccurrences(r.Context(), r.URL.Query().Get("scheduleId"), limit)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	out := make([]occurrenceResponse, 0, len(items))
	for _, item := range items {
		item.LeaseToken = ""
		out = append(out, publicOccurrence(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"occurrences": out})
}

func (s *Server) claimScheduleOccurrence(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LeaseSeconds int `json:"leaseSeconds"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeAPIError(w, err)
		return
	}
	if body.LeaseSeconds == 0 {
		body.LeaseSeconds = 300
	}
	if body.LeaseSeconds < 30 || body.LeaseSeconds > 3600 {
		writeAPIError(w, invalidSchedule("leaseSeconds must be between 30 and 3600"))
		return
	}
	token, err := schedule.ID("lease_")
	if err != nil {
		writeAPIError(w, err)
		return
	}
	now := time.Now().UTC()
	item, err := s.store.ClaimOccurrence(r.Context(), now, now.Add(time.Duration(body.LeaseSeconds)*time.Second), token)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicOccurrence(item))
}

func (s *Server) renewScheduleOccurrence(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LeaseToken   string `json:"leaseToken"`
		LeaseSeconds int    `json:"leaseSeconds"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeAPIError(w, err)
		return
	}
	if body.LeaseToken == "" {
		writeAPIError(w, invalidSchedule("leaseToken is required"))
		return
	}
	if body.LeaseSeconds == 0 {
		body.LeaseSeconds = 300
	}
	if body.LeaseSeconds < 30 || body.LeaseSeconds > 3600 {
		writeAPIError(w, invalidSchedule("leaseSeconds must be between 30 and 3600"))
		return
	}
	if err := s.store.RenewOccurrence(r.Context(), r.PathValue("id"), body.LeaseToken,
		time.Now().UTC().Add(time.Duration(body.LeaseSeconds)*time.Second)); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) completeScheduleOccurrence(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LeaseToken string `json:"leaseToken"`
		State      string `json:"state"`
		Error      string `json:"error"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeAPIError(w, err)
		return
	}
	if body.LeaseToken == "" || (body.State != "completed" && body.State != "failed") {
		writeAPIError(w, invalidSchedule("leaseToken and state completed|failed are required"))
		return
	}
	if len(body.Error) > 16<<10 {
		writeAPIError(w, invalidSchedule("error exceeds 16 KiB"))
		return
	}
	if err := s.store.CompleteOccurrence(r.Context(), r.PathValue("id"), body.LeaseToken, body.State, body.Error); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateSchedule(name, expression, timezone, concurrency string, payload json.RawMessage) error {
	if strings.TrimSpace(name) == "" || len(name) > 200 {
		return invalidSchedule("name is required and must not exceed 200 bytes")
	}
	if len(payload) > maxSchedulePayload || !json.Valid(payload) {
		return invalidSchedule("payload must be valid JSON no larger than 64 KiB")
	}
	if concurrency != "queue" && concurrency != "skip" {
		return invalidSchedule("concurrencyPolicy must be queue or skip")
	}
	if _, err := schedule.Next(expression, timezone, time.Now().UTC()); err != nil {
		return invalidSchedule(err.Error())
	}
	return nil
}

func invalidSchedule(message string) error {
	return &apiError{status: http.StatusBadRequest, code: "invalid_request", message: message}
}

func publicSchedule(item store.Schedule) scheduleResponse {
	return scheduleResponse{ID: item.ID, Name: item.Name, CronExpression: item.CronExpression, Timezone: item.Timezone,
		Enabled: item.Enabled, Payload: item.Payload, ConcurrencyPolicy: item.ConcurrencyPolicy, NextRunAt: item.NextRunAt,
		LastRunAt: item.LastRunAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
}

func publicOccurrence(item store.Occurrence) occurrenceResponse {
	return occurrenceResponse{ID: item.ID, ScheduleID: item.ScheduleID, ScheduledAt: item.ScheduledAt, State: item.State,
		LeaseToken: item.LeaseToken, LeaseUntil: item.LeaseUntil, Attempts: item.Attempts, Error: item.Error,
		Payload: item.Payload, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
}
