package main

import (
	"log/slog"
	"sync"
	"time"

	"hol-up-prototype/internal/reconcile"
	"hol-up-prototype/internal/schedule"
)

// Application wiring owns publications. Network I/O finishes before entering
// this mutex; readers load one immutable result including its schedule version.
type reconciliationCoordinator struct {
	// ponytail: serialize runs; schedule work asynchronously only if timings delay refreshes.
	mu             sync.Mutex
	schedules      *schedule.Store
	feeds          []feed
	results        reconcile.Store
	now            func() time.Time
	logger         *slog.Logger
	maxScheduleAge time.Duration
}

func (c *reconciliationCoordinator) run(trigger string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	started := time.Now()
	version := c.schedules.Load()
	now := c.now()
	sources := make([]reconcile.Source, len(c.feeds))
	for i, feed := range c.feeds {
		sources[i] = reconcile.Source{Name: feed.name, Routes: feed.routes, Snapshot: feed.store.Load(now, staleAfter)}
	}
	bundle := reconcile.Reconcile(version, sources, now, staleAfter, c.maxScheduleAge)
	c.results.Publish(bundle)
	counts := map[reconcile.Outcome]int{}
	stopCounts := map[reconcile.Outcome]int{}
	for _, match := range bundle.Matches() {
		counts[match.Outcome]++
		for _, stop := range match.Stops {
			stopCounts[stop.Outcome]++
		}
		c.logger.Debug("Trip reconciliation", "event", "reconciliation_trip", "source", match.Reference.Source.Name,
			"entity_id", match.Train.EntityID, "trip_id", match.Train.TripID, "route_id", match.Train.RouteID,
			"service_date", match.Train.ServiceDate, "rule", match.Rule, "candidates", match.Candidates,
			"outcome", match.Outcome, "reason", match.Reason, "rejections", match.Rejections, "stops", match.Stops)
	}
	validRules := 0
	rules := bundle.ReplacementRules()
	for _, rule := range rules {
		if rule.Valid {
			validRules++
		}
	}
	c.logger.Info("Reconciliation completed", "event", "reconciliation_completed", "trigger", trigger,
		"schedule", bundle.Metadata(), "sources", bundle.Sources(), "outcomes", counts, "stop_outcomes", stopCounts,
		"replacement_rules", len(rules), "valid_replacement_rules", validRules, "duration_ms", time.Since(started).Milliseconds())
}
