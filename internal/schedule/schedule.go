// Package schedule owns immutable static schedules and service-day evaluation.
// It has no transport or persistence dependencies.
package schedule

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync/atomic"
	"time"
	_ "time/tzdata"
)

const Timezone = "America/New_York"

var newYork = func() *time.Location {
	l, err := time.LoadLocation(Timezone)
	if err != nil {
		panic(err)
	}
	return l
}()

// Date is a validated YYYYMMDD service date, independent of UTC offsets.
type Date string

func ParseDate(value string) (Date, error) {
	t, err := time.Parse("20060102", value)
	if err != nil || t.Year() < 1 || t.Format("20060102") != value {
		return "", errors.New("invalid service date")
	}
	return Date(value), nil
}

func LocalDate(at time.Time) Date { return Date(at.In(newYork).Format("20060102")) }

func (d Date) AddDays(days int) Date {
	t, err := time.Parse("20060102", string(d))
	if err != nil {
		return ""
	}
	return Date(t.AddDate(0, 0, days).Format("20060102"))
}

// Instant follows GTFS's local noon minus twelve elapsed hours definition.
// On DST transition days this differs from local midnight.
func (d Date) Instant(seconds int32) time.Time {
	t, err := time.Parse("20060102", string(d))
	if err != nil {
		return time.Time{}
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, newYork).
		Add(-12*time.Hour + time.Duration(seconds)*time.Second)
}

type OptionalTime struct {
	Seconds int32
	Present bool
}

type Agency struct{ ID, Name, URL, Timezone string }
type Route struct {
	ID, AgencyID, ShortName, LongName string
	Type                              int
}
type Stop struct {
	ID, Name, ParentID  string
	Latitude, Longitude float64
	HasCoordinates      bool
	LocationType        int
}
type StopTime struct {
	StopID             string
	Sequence           uint32
	Arrival, Departure OptionalTime
	Timepoint          bool
}
type Trip struct {
	ID, RouteID, ServiceID, Headsign, ShapeID string
	Direction                                 int // -1 means absent; otherwise 0 or 1.
	Stops                                     []StopTime
}
type Calendar struct {
	ServiceID  string
	Start, End Date
	Days       [7]bool // time.Weekday order, Sunday first.
}
type Exception struct {
	ServiceID string
	Date      Date
	Added     bool
}
type FeedInfo struct {
	Publisher, URL, Language, Version string
	Start, End                        Date
}
type Coverage struct{ Start, End Date }

func (c Coverage) Contains(date Date) bool { return c.Start != "" && date >= c.Start && date <= c.End }

// Data is mutable staging input. NewDataset validates and copies it before use.
type Data struct {
	Agencies   []Agency
	Routes     []Route
	Stops      []Stop
	Trips      []Trip
	Calendars  []Calendar
	Exceptions []Exception
	FeedInfo   FeedInfo
}

type service struct {
	calendar   *Calendar
	exceptions map[Date]bool
}
type Counts struct{ Agencies, Routes, Stops, Trips, StopTimes, Services int }

type Dataset struct {
	agencies           []Agency
	routes             map[string]Route
	stops              map[string]Stop
	trips              map[string]Trip
	services           map[string]service
	byRoute, byService map[string][]string
	coverage           Coverage
	feedInfo           FeedInfo
	counts             Counts
	maxTime            int32
}

func NewDataset(data Data) (*Dataset, error) {
	d := &Dataset{agencies: slices.Clone(data.Agencies), routes: map[string]Route{}, stops: map[string]Stop{}, trips: map[string]Trip{}, services: map[string]service{}, byRoute: map[string][]string{}, byService: map[string][]string{}, feedInfo: data.FeedInfo}
	agencies := map[string]bool{}
	for _, a := range data.Agencies {
		if agencies[a.ID] || (a.ID == "" && len(data.Agencies) != 1) || a.Name == "" || a.URL == "" || a.Timezone != Timezone {
			return nil, errors.New("invalid or duplicate agency")
		}
		agencies[a.ID] = true
	}
	for _, r := range data.Routes {
		if r.AgencyID == "" && len(data.Agencies) == 1 {
			r.AgencyID = data.Agencies[0].ID
		}
		if _, exists := d.routes[r.ID]; exists || r.ID == "" || !agencies[r.AgencyID] || (r.ShortName == "" && r.LongName == "") {
			return nil, errors.New("invalid or duplicate route")
		}
		d.routes[r.ID] = r
	}
	for _, s := range data.Stops {
		if _, exists := d.stops[s.ID]; exists || s.ID == "" {
			return nil, errors.New("invalid or duplicate stop")
		}
		d.stops[s.ID] = s
	}
	for _, s := range d.stops {
		if s.ParentID != "" {
			parent, ok := d.stops[s.ParentID]
			if !ok || s.ParentID == s.ID || (s.LocationType == 4 && parent.LocationType != 0) || (s.LocationType != 4 && parent.LocationType != 1) {
				return nil, errors.New("invalid parent stop reference")
			}
		}
		if (s.LocationType == 1 && s.ParentID != "") || (s.LocationType >= 2 && s.ParentID == "") {
			return nil, errors.New("invalid parent stop")
		}
	}
	for _, c := range data.Calendars {
		if _, exists := d.services[c.ServiceID]; exists || c.ServiceID == "" || !validRange(c.Start, c.End) {
			return nil, errors.New("invalid or duplicate calendar")
		}
		d.services[c.ServiceID] = service{calendar: &c, exceptions: map[Date]bool{}}
	}
	for _, e := range data.Exceptions {
		if _, err := ParseDate(string(e.Date)); err != nil || e.ServiceID == "" {
			return nil, errors.New("invalid calendar exception")
		}
		s := d.services[e.ServiceID]
		if s.exceptions == nil {
			s.exceptions = map[Date]bool{}
		}
		if _, exists := s.exceptions[e.Date]; exists {
			return nil, errors.New("duplicate calendar exception")
		}
		s.exceptions[e.Date] = e.Added
		d.services[e.ServiceID] = s
	}
	for _, t := range data.Trips {
		_, routeOK := d.routes[t.RouteID]
		_, serviceOK := d.services[t.ServiceID]
		if _, exists := d.trips[t.ID]; exists || t.ID == "" || !routeOK || !serviceOK || t.Direction < -1 || t.Direction > 1 || len(t.Stops) < 2 {
			return nil, errors.New("invalid or duplicate trip")
		}
		t.Stops = slices.Clone(t.Stops)
		sort.Slice(t.Stops, func(i, j int) bool { return t.Stops[i].Sequence < t.Stops[j].Sequence })
		last := int32(-1)
		for i, s := range t.Stops {
			stop, exists := d.stops[s.StopID]
			if !exists || stop.LocationType != 0 || (i > 0 && s.Sequence == t.Stops[i-1].Sequence) {
				return nil, errors.New("invalid stop time reference or duplicate sequence")
			}
			if (i == 0 || i == len(t.Stops)-1 || s.Timepoint) && (!s.Arrival.Present || !s.Departure.Present) {
				return nil, errors.New("missing required stop time")
			}
			for _, value := range []OptionalTime{s.Arrival, s.Departure} {
				if value.Present {
					if value.Seconds < 0 || value.Seconds < last {
						return nil, errors.New("negative or decreasing stop time")
					}
					last = value.Seconds
					d.maxTime = max(d.maxTime, last)
				}
			}
		}
		d.trips[t.ID] = t
		d.byRoute[t.RouteID] = append(d.byRoute[t.RouteID], t.ID)
		d.byService[t.ServiceID] = append(d.byService[t.ServiceID], t.ID)
		d.counts.StopTimes += len(t.Stops)
	}
	for id := range d.byService {
		s := d.services[id]
		if s.calendar != nil {
			d.extendCoverage(s.calendar.Start)
			d.extendCoverage(s.calendar.End)
		}
		for date, added := range s.exceptions {
			if added {
				d.extendCoverage(date)
			}
		}
	}
	if data.FeedInfo.Start != "" {
		if _, err := ParseDate(string(data.FeedInfo.Start)); err != nil {
			return nil, err
		}
		d.coverage.Start = max(d.coverage.Start, data.FeedInfo.Start)
	}
	if data.FeedInfo.End != "" {
		if _, err := ParseDate(string(data.FeedInfo.End)); err != nil {
			return nil, err
		}
		d.coverage.End = min(d.coverage.End, data.FeedInfo.End)
	}
	if len(d.agencies) == 0 || len(d.routes) == 0 || len(d.stops) == 0 || len(d.trips) == 0 || !validRange(d.coverage.Start, d.coverage.End) {
		return nil, errors.New("empty schedule or service coverage")
	}
	for _, index := range []map[string][]string{d.byRoute, d.byService} {
		for _, ids := range index {
			slices.Sort(ids)
		}
	}
	d.counts.Agencies, d.counts.Routes, d.counts.Stops, d.counts.Trips, d.counts.Services = len(d.agencies), len(d.routes), len(d.stops), len(d.trips), len(d.services)
	return d, nil
}

func validRange(start, end Date) bool {
	_, e1 := ParseDate(string(start))
	_, e2 := ParseDate(string(end))
	return e1 == nil && e2 == nil && start <= end
}
func (d *Dataset) extendCoverage(date Date) {
	if d.coverage.Start == "" || date < d.coverage.Start {
		d.coverage.Start = date
	}
	d.coverage.End = max(d.coverage.End, date)
}

func (d *Dataset) Counts() Counts                { return d.counts }
func (d *Dataset) Coverage() Coverage            { return d.coverage }
func (d *Dataset) FeedInfo() FeedInfo            { return d.feedInfo }
func (d *Dataset) Agencies() []Agency            { return slices.Clone(d.agencies) }
func (d *Dataset) Route(id string) (Route, bool) { r, ok := d.routes[id]; return r, ok }
func (d *Dataset) Stop(id string) (Stop, bool)   { s, ok := d.stops[id]; return s, ok }
func (d *Dataset) Trip(id string) (Trip, bool) {
	t, ok := d.trips[id]
	t.Stops = slices.Clone(t.Stops)
	return t, ok
}
func (d *Dataset) RouteTrips(id string) []string   { return slices.Clone(d.byRoute[id]) }
func (d *Dataset) ServiceTrips(id string) []string { return slices.Clone(d.byService[id]) }
func (d *Dataset) RouteIDs() []string              { return slices.Sorted(maps.Keys(d.routes)) }

func (d *Dataset) Active(serviceID string, date Date) bool {
	if !d.coverage.Contains(date) {
		return false
	}
	s, ok := d.services[serviceID]
	if !ok {
		return false
	}
	if added, exists := s.exceptions[date]; exists {
		return added
	}
	if s.calendar == nil || date < s.calendar.Start || date > s.calendar.End {
		return false
	}
	t, err := time.Parse("20060102", string(date))
	return err == nil && s.calendar.Days[t.Weekday()]
}

// CandidateServiceDates includes preceding service dates for extended-hour trips.
// The extra next date handles the spring DST day whose GTFS origin is 23:00.
func (d *Dataset) CandidateServiceDates(at time.Time) []Date {
	var dates []Date
	date := LocalDate(at).AddDays(1)
	for i := 0; i <= int(d.maxTime/86400)+2; i++ {
		origin := date.Instant(0)
		if d.coverage.Contains(date) && !at.Before(origin) && !at.After(date.Instant(d.maxTime)) {
			dates = append(dates, date)
		}
		date = date.AddDays(-1)
	}
	return dates
}

// Covers includes extended-hour service from the last advertised service date.
// A caller still checks Active for each trip's service and candidate date.
func (d *Dataset) Covers(at time.Time) bool {
	return d.coverage.Contains(LocalDate(at)) || len(d.CandidateServiceDates(at)) > 0
}

type Source string

const (
	Supplemented Source = "supplemented"
	Regular      Source = "regular"
)

type Metadata struct {
	Source           Source
	ParserSchema     string
	ArchiveSHA256    string
	Version          string
	ValidatedAt      time.Time
	PublishedAt      time.Time
	Coverage         Coverage
	PlanningCoverage Coverage // Seven local calendar days; zero for regular GTFS.
}

// Version ties immutable data and indexes to metadata in a single publication.
type Version struct {
	dataset  *Dataset
	metadata Metadata
}

func NewVersion(dataset *Dataset, meta Metadata) (*Version, error) {
	if dataset == nil || (meta.Source != Supplemented && meta.Source != Regular) || meta.ParserSchema == "" || len(meta.ArchiveSHA256) != 64 || meta.ValidatedAt.IsZero() || meta.PublishedAt.Before(meta.ValidatedAt) {
		return nil, errors.New("invalid schedule metadata")
	}
	meta.Version = fmt.Sprintf("%s:%s:%s", meta.Source, meta.ParserSchema, meta.ArchiveSHA256)
	meta.Coverage = dataset.Coverage()
	meta.PlanningCoverage = Coverage{}
	if meta.Source == Supplemented {
		start := LocalDate(meta.ValidatedAt)
		meta.PlanningCoverage = Coverage{Start: max(start, meta.Coverage.Start), End: min(start.AddDays(6), meta.Coverage.End)}
		if meta.PlanningCoverage.Start > meta.PlanningCoverage.End {
			meta.PlanningCoverage = Coverage{}
		}
	}
	return &Version{dataset: dataset, metadata: meta}, nil
}
func (v *Version) Dataset() *Dataset  { return v.dataset }
func (v *Version) Metadata() Metadata { return v.metadata }

// Eligible never removes expired data. Cache restoration cannot renew ValidatedAt.
func (v *Version) Eligible(now time.Time, maxAge time.Duration) bool {
	return v != nil && v.dataset != nil && maxAge > 0 && !now.Before(v.metadata.ValidatedAt) && now.Sub(v.metadata.ValidatedAt) <= maxAge && v.dataset.Covers(now)
}

type Store struct{ current atomic.Pointer[Version] }

func (s *Store) Load() *Version { return s.current.Load() }
func (s *Store) Publish(v *Version) {
	if v != nil && v.dataset != nil {
		s.current.Store(v)
	}
}
