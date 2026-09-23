// Package reconcile relates immutable schedules and normalized realtime inputs.
// It neither fetches inputs nor changes predictions or public API selection.
package reconcile

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"slices"
	"sync/atomic"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/schedule"
)

type Instance struct {
	TripID      string
	ServiceDate schedule.Date
}

// ID encodes the pair without delimiter collisions or dependence on realtime IDs.
func (i Instance) ID() string {
	if i.TripID == "" || i.ServiceDate == "" {
		return ""
	}
	value, _ := json.Marshal([2]string{i.TripID, string(i.ServiceDate)})
	return base64.RawURLEncoding.EncodeToString(value)
}

type Source struct {
	Name     string
	Routes   []string
	Snapshot realtime.Snapshot
}

type Provenance struct {
	Name                        string
	Routes                      []string
	SourceTimestamp, ReceivedAt time.Time
	NYCTVersion                 string
	Complete                    bool
	Freshness                   realtime.Freshness
}

type Reference struct {
	Source Provenance
	Index  int
}

type Outcome string

const (
	Matched     Outcome = "matched"
	Unmatched   Outcome = "unmatched"
	Invalid     Outcome = "invalid"
	Ambiguous   Outcome = "ambiguous"
	Unavailable Outcome = "unavailable"
)

type StopResolution struct {
	UpdateIndex    int
	ScheduledIndex int // -1 when unresolved.
	StopID         string
	Sequence       uint32
	Outcome        Outcome
	Reason         string
	Warnings       []string
}

type Rejection struct {
	Instance Instance
	Reason   string
}

type Match struct {
	Reference    Reference
	Unidentified bool
	Train        realtime.Train
	Owned        bool
	Rule         string
	Outcome      Outcome
	Reason       string
	Warnings     []string
	Rejections   []Rejection
	Candidates   []Instance
	Instance     Instance // Set only for a unique match, including cancellations.
	Stops        []StopResolution
	VehicleStop  *StopResolution
}

type ReplacementRule struct {
	Reference Reference
	Period    realtime.ReplacementPeriod
	Valid     bool
	Reason    string
}

type Metadata struct {
	Schedule                          schedule.Metadata
	SchedulePresent, ScheduleEligible bool
	EvaluatedAt                       time.Time
	Reason                            string
}

type sourceState struct {
	provenance    Provenance
	rules         []ReplacementRule
	matches       []Match
	blocked       map[Instance]bool
	blockedRoutes map[string]bool
	blockedSource bool
}

// Bundle owns all mutable inputs. The captured schedule is immutable; callers
// must use this version, never combine a result with a separately loaded store.
type Bundle struct {
	version                    *schedule.Version
	metadata                   Metadata
	staleAfter, maxScheduleAge time.Duration
	sources                    []sourceState
}

func (b *Bundle) Metadata() Metadata          { return b.metadata }
func (b *Bundle) Schedule() *schedule.Version { return b.version }

// ScheduleEligible and MetadataAt recheck the captured version at reader time.
func (b *Bundle) ScheduleEligible(now time.Time) bool {
	return b != nil && b.metadata.ScheduleEligible && b.version.Eligible(now, b.maxScheduleAge)
}

func (b *Bundle) MetadataAt(now time.Time) Metadata {
	if b == nil {
		return Metadata{EvaluatedAt: now, Reason: "schedule_missing"}
	}
	m := b.metadata
	m.EvaluatedAt, m.ScheduleEligible = now, b.ScheduleEligible(now)
	if m.SchedulePresent && !m.ScheduleEligible {
		m.Reason = "schedule_ineligible"
	}
	return m
}

func (b *Bundle) StaleAfter() time.Duration { return b.staleAfter }
func (b *Bundle) FreshnessAt(timestamp, now time.Time) realtime.Freshness {
	return realtime.FreshnessAt(timestamp, now, b.staleAfter)
}
func (b *Bundle) Sources() []Provenance {
	var out []Provenance
	for _, s := range b.sources {
		out = append(out, cloneProvenance(s.provenance))
	}
	return out
}
func (b *Bundle) Matches() []Match {
	var out []Match
	for _, s := range b.sources {
		for _, m := range s.matches {
			m.Reference.Source = cloneProvenance(m.Reference.Source)
			m.Train = m.Train.Clone()
			m.Candidates = slices.Clone(m.Candidates)
			m.Rejections = slices.Clone(m.Rejections)
			m.Warnings = slices.Clone(m.Warnings)
			m.Stops = slices.Clone(m.Stops)
			for i := range m.Stops {
				m.Stops[i].Warnings = slices.Clone(m.Stops[i].Warnings)
			}
			if m.VehicleStop != nil {
				stop := *m.VehicleStop
				stop.Warnings = slices.Clone(stop.Warnings)
				m.VehicleStop = &stop
			}
			out = append(out, m)
		}
	}
	return out
}
func (b *Bundle) ReplacementRules() []ReplacementRule {
	var out []ReplacementRule
	for _, s := range b.sources {
		for _, r := range s.rules {
			r.Reference.Source = cloneProvenance(r.Reference.Source)
			r.Period = r.Period.Clone()
			out = append(out, r)
		}
	}
	return out
}
func cloneProvenance(p Provenance) Provenance { p.Routes = slices.Clone(p.Routes); return p }

type Store struct{ current atomic.Pointer[Bundle] }

func (s *Store) Load() *Bundle { return s.current.Load() }
func (s *Store) Publish(b *Bundle) {
	if b != nil {
		s.current.Store(b)
	}
}

// Reconcile captures no clocks or stores. Each source's entire candidate graph
// is evaluated before ownership; matching priority cannot break a duplicate tie.
func Reconcile(version *schedule.Version, sources []Source, now time.Time, staleAfter, maxScheduleAge time.Duration) *Bundle {
	b := &Bundle{version: version, staleAfter: staleAfter, maxScheduleAge: maxScheduleAge,
		metadata: Metadata{EvaluatedAt: now, SchedulePresent: version != nil, ScheduleEligible: version.Eligible(now, maxScheduleAge)}}
	if version != nil {
		b.metadata.Schedule = version.Metadata()
	}
	switch {
	case version == nil:
		b.metadata.Reason = "schedule_missing"
	case !b.metadata.ScheduleEligible:
		b.metadata.Reason = "schedule_ineligible"
	default:
		b.metadata.Reason = "schedule_eligible"
	}
	var trips []staticTrip
	if b.metadata.ScheduleEligible {
		for _, route := range version.Dataset().RouteIDs() {
			for _, id := range version.Dataset().RouteTrips(route) {
				trip, _ := version.Dataset().Trip(id)
				trips = append(trips, staticTrip{trip, parseID(id)})
			}
		}
	}
	for _, input := range sources {
		snapshot := input.Snapshot.Clone()
		p := Provenance{Name: input.Name, Routes: slices.Clone(input.Routes), SourceTimestamp: snapshot.SourceTimestamp,
			ReceivedAt: snapshot.ReceivedAt, NYCTVersion: snapshot.NYCTVersion, Complete: snapshot.Complete,
			Freshness: realtime.FreshnessAt(snapshot.SourceTimestamp, now, staleAfter)}
		state := sourceState{provenance: p, blocked: map[Instance]bool{}, blockedRoutes: map[string]bool{}}
		for index, period := range snapshot.ReplacementPeriods {
			rule := ReplacementRule{Reference: Reference{p, index}, Period: period}
			rule.Reason = validatePeriod(period, p)
			rule.Valid = rule.Reason == ""
			if !rule.Valid {
				if period.RouteID == "" {
					state.blockedSource = true
				} else {
					state.blockedRoutes[period.RouteID] = true
				}
			}
			state.rules = append(state.rules, rule)
		}
		// A malformed sibling period disables the whole affected route's union.
		for i := range state.rules {
			r := &state.rules[i]
			if r.Valid && (state.blockedSource || state.blockedRoutes[r.Period.RouteID]) {
				r.Valid, r.Reason = false, "source_or_route_uncertain"
			}
		}
		records := append(snapshot.Trains, snapshot.UnidentifiedTrips...)
		reverse := map[Instance]int{}
		for index, train := range records {
			m := Match{Reference: Reference{p, index}, Train: train, Owned: slices.Contains(p.Routes, train.RouteID), Unidentified: index >= len(snapshot.Trains)}
			if !b.metadata.ScheduleEligible {
				m.Outcome, m.Reason = Unavailable, b.metadata.Reason
			} else {
				matchTrip(&m, trips, version.Dataset())
				if m.Outcome == Matched || m.Outcome == Ambiguous {
					for _, candidate := range m.Candidates {
						reverse[candidate]++
					}
				}
			}
			state.matches = append(state.matches, m)
		}
		for i := range state.matches {
			m := &state.matches[i]
			if m.Outcome == Matched {
				if reverse[m.Candidates[0]] != 1 {
					m.Outcome, m.Reason = Ambiguous, "multiple_realtime_counterparts"
				} else if !p.Complete {
					m.Outcome, m.Reason = Ambiguous, "source_incomplete"
				} else {
					m.Instance = m.Candidates[0]
					trip, _ := version.Dataset().Trip(m.Instance.TripID)
					m.Owned = slices.Contains(p.Routes, trip.RouteID)
					m.Stops = resolveStops(m.Train.Stops, trip.Stops, true)
					if m.Train.VehicleMatch == realtime.VehicleMatched && (m.Train.VehicleStopID != "" || m.Train.VehicleStopSequence != nil) {
						stop := ResolveStop(realtime.StopPrediction{StopID: m.Train.VehicleStopID, Sequence: m.Train.VehicleStopSequence}, trip.Stops)
						m.VehicleStop = &stop
					}
				}
			}
			if m.Outcome != Matched {
				for _, instance := range m.Candidates {
					state.blocked[instance] = true
				}
				if len(m.Candidates) == 0 || m.Reason == "route_conflict" || m.Reason == "identifier_route_conflict" {
					// An unlocalizable record may be the supposedly absent trip. Protect
					// every supplied route, or the entire source when none is known.
					route := parseID(m.Train.TripID).route
					if m.Train.RouteID == "" && route == "" {
						state.blockedSource = true
					}
					if m.Train.RouteID != "" {
						state.blockedRoutes[m.Train.RouteID] = true
					}
					if route != "" {
						state.blockedRoutes[route] = true
					}
				}
			}
		}
		b.sources = append(b.sources, state)
	}
	// A run cannot acquire a unique identity from two different feed snapshots.
	reverse := map[Instance]int{}
	for _, source := range b.sources {
		for _, match := range source.matches {
			if match.Outcome == Matched || match.Outcome == Ambiguous {
				for _, candidate := range match.Candidates {
					reverse[candidate]++
				}
			}
		}
	}
	for i := range b.sources {
		source := &b.sources[i]
		for j := range source.matches {
			m := &source.matches[j]
			if m.Outcome != Matched {
				m.Warnings = nil
				continue
			}
			trip, _ := version.Dataset().Trip(m.Instance.TripID)
			reason := ""
			if reverse[m.Instance] != 1 {
				reason = "multiple_realtime_counterparts"
			} else if b.routeOwner(trip.RouteID) == -2 {
				reason = "ambiguous_route_owner"
			}
			if reason != "" {
				source.blocked[m.Instance] = true
				m.Outcome, m.Reason, m.Instance = Ambiguous, reason, Instance{}
				m.Warnings = nil
				for k := range m.Stops {
					m.Stops[k].Warnings = nil
				}
			}
		}
	}
	return b
}

func validatePeriod(p realtime.ReplacementPeriod, source Provenance) string {
	switch {
	case p.RouteID == "":
		return "missing_route"
	case source.NYCTVersion != "1.0":
		return "unsupported_nyct_version"
	case !source.Complete:
		return "source_incomplete"
	case !slices.Contains(source.Routes, p.RouteID):
		return "not_route_owner"
	case p.Start == nil && p.End == nil:
		return "missing_boundaries"
	case p.Start != nil && !representable(*p.Start), p.End != nil && !representable(*p.End):
		return "boundary_overflow"
	case p.Start != nil && p.End != nil && *p.Start >= *p.End:
		return "empty_or_reversed_interval"
	}
	return ""
}

// Go's time.Time stores seconds since year 1 in an int64, not Unix seconds.
func representable(seconds uint64) bool {
	return seconds <= math.MaxInt64 && !time.Unix(int64(seconds), 0).Before(time.Unix(0, 0))
}

type FallbackDecision struct {
	Eligible   bool
	Suppressed bool
	Reason     string
}

// Fallback rechecks authority at the reader's time, including outages after
// publication. Windows form a union without enumerating unbounded service dates.
func (b *Bundle) Fallback(instance Instance, now time.Time) FallbackDecision {
	if !b.ScheduleEligible(now) {
		return FallbackDecision{Reason: "schedule_ineligible"}
	}
	trip, ok := b.version.Dataset().Trip(instance.TripID)
	if !ok || !b.version.Dataset().Active(trip.ServiceID, instance.ServiceDate) {
		return FallbackDecision{Reason: "instance_ineligible"}
	}
	allowed := FallbackDecision{Eligible: true, Reason: "no_replacement_authority"}
	// Duplicate configured owners cannot establish absence.
	owner := b.routeOwner(trip.RouteID)
	if owner == -2 {
		return FallbackDecision{Eligible: true, Reason: "ambiguous_route_owner"}
	}
	if owner < 0 {
		return allowed
	}
	source := b.sources[owner]
	if realtime.FreshnessAt(source.provenance.SourceTimestamp, now, b.staleAfter) != realtime.FreshnessFresh {
		return FallbackDecision{Eligible: true, Reason: "source_not_fresh"}
	}
	for _, match := range source.matches {
		if match.Outcome == Matched && match.Instance == instance && !match.Train.Canceled() {
			return FallbackDecision{Reason: "active_realtime_counterpart"}
		}
	}
	if source.blockedSource || source.blockedRoutes[trip.RouteID] || source.blocked[instance] {
		return FallbackDecision{Eligible: true, Reason: "uncertain_counterpart_or_metadata"}
	}
	departure := instance.ServiceDate.Instant(trip.Stops[0].Departure.Seconds)
	if !departure.After(now) {
		return FallbackDecision{Eligible: true, Reason: "departure_not_future"}
	}
	if replacementContains(source.rules, trip.RouteID, departure) {
		return FallbackDecision{Suppressed: true, Reason: "absent_from_replacement_period"}
	}
	return allowed
}

func replacementContains(rules []ReplacementRule, routeID string, departure time.Time) bool {
	for _, rule := range rules {
		if !rule.Valid || rule.Period.RouteID != routeID {
			continue
		}
		p := rule.Period
		if (p.Start == nil || !departure.Before(time.Unix(int64(*p.Start), 0))) && (p.End == nil || departure.Before(time.Unix(int64(*p.End), 0))) {
			return true
		}
	}
	return false
}
