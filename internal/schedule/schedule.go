// Package schedule implements Scraps' durable, execution-agnostic clock.
package schedule

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/peelar/scraps/internal/store"
	"github.com/robfig/cron/v3"
)

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Next returns the first firing strictly after the supplied instant.
func Next(expression, timezone string, after time.Time) (time.Time, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone %q", timezone)
	}
	parsed, err := parser.Parse(expression)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	return parsed.Next(after.In(location)).UTC(), nil
}

// ID returns an opaque random identifier with the supplied prefix.
func ID(prefix string) (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

// Engine turns due schedules into durable occurrences. It never executes a
// workflow; external consumers claim occurrences through the API.
type Engine struct {
	store  *store.Store
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func NewEngine(st *store.Store) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	engine := &Engine{store: st, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go engine.run()
	return engine
}

func (e *Engine) run() {
	defer close(e.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := e.Tick(e.ctx, time.Now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("schedule tick failed", "error", err)
		}
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Close stops the schedule clock.
func (e *Engine) Close() {
	e.cancel()
	<-e.done
}

// Tick enqueues up to 100 due occurrences. Repeated ticks catch up additional
// missed firings without allowing one schedule to monopolize the daemon.
func (e *Engine) Tick(ctx context.Context, now time.Time) error {
	for processed := 0; processed < 100; processed++ {
		due, err := e.store.DueSchedules(ctx, now, 1)
		if err != nil {
			return err
		}
		if len(due) == 0 {
			return nil
		}
		item := due[0]
		if item.NextRunAt == nil {
			continue
		}
		next, err := Next(item.CronExpression, item.Timezone, *item.NextRunAt)
		if err != nil {
			return fmt.Errorf("advance schedule %s: %w", item.ID, err)
		}
		occurrenceID, err := ID("occ_")
		if err != nil {
			return err
		}
		if err := e.store.FireSchedule(ctx, item, occurrenceID, next); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}
