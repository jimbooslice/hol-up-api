package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"hol-up-prototype/internal/gen/gtfs"
	"hol-up-prototype/internal/httpapi"
	"hol-up-prototype/internal/ingest"
	"hol-up-prototype/internal/projection"
	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/reconcile"
	"hol-up-prototype/internal/schedule"
	"hol-up-prototype/internal/scheduleingest"
	"hol-up-prototype/internal/telemetry"
)

type fixtureTransport map[string][]byte

// Optional replay of the local captures described in TRAIN_POSITION_DIAGNOSIS.md.
// Synthetic regressions run without these external files in the normal suite.
func TestLocalPositionCapture(t *testing.T) {
	dir := os.Getenv("HOLUP_TEST_POSITION_CAPTURE")
	archivePath := os.Getenv("HOLUP_TEST_SCHEDULE_ARCHIVE")
	if dir == "" || archivePath == "" {
		t.Skip("set HOLUP_TEST_POSITION_CAPTURE and HOLUP_TEST_SCHEDULE_ARCHIVE for captured-feed replay")
	}
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var report struct {
		EvaluatedAt time.Time         `json:"evaluated_at"`
		Schedule    schedule.Metadata `json:"schedule"`
	}
	if err := json.Unmarshal(read(filepath.Join(dir, "diagnosis.json")), &report); err != nil {
		t.Fatal(err)
	}
	dataset, err := scheduleingest.Decode(read(archivePath))
	if err != nil {
		t.Fatal(err)
	}
	v, err := schedule.NewVersion(dataset, report.Schedule)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ingest.Decode(read(filepath.Join(dir, "ace.pb")), "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot.ReceivedAt = report.EvaluatedAt
	b := reconcile.Reconcile(v, []reconcile.Source{{Name: "ace", Routes: []string{"A", "C", "E", "H"}, Snapshot: snapshot}}, report.EvaluatedAt, time.Minute, 24*time.Hour)
	counts := map[string]int{}
	warnings := 0
	for _, p := range projection.Positions(b, "A", report.EvaluatedAt) {
		reason := p.NullReason
		if p.Latitude != nil {
			reason = "positioned"
		}
		counts[reason]++
		if slices.Contains(p.ReconciliationWarnings, "vehicle_stop_sequence_conflict_ignored") {
			warnings++
		}
		if reason == "not_started" && (!p.PredictedDepartureAt.After(report.EvaluatedAt) || p.MovementState != realtime.MovementStateUnknown) {
			t.Fatalf("invalid upcoming departure: %+v", p)
		}
	}
	wantUpcoming := map[string]int{"2026-09-15T21:10:21Z": 16, "2026-09-15T21:15:16Z": 19}[report.EvaluatedAt.Format(time.RFC3339)]
	if wantUpcoming == 0 {
		t.Fatal("not one of the two diagnosed captures")
	}
	t.Logf("evaluated_at=%s schedule=%s counts=%v vehicle_warnings=%d", report.EvaluatedAt.Format(time.RFC3339), v.Metadata().Version, counts, warnings)
	if counts["positioned"] != 31 || counts["not_started"] != wantUpcoming || warnings != 32 || counts["insufficient_timing"] != 1 {
		t.Fatalf("capture differs from expected policy: %v warnings=%d", counts, warnings)
	}
}

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(f[r.URL.Path]))}, nil
}
func TestOfflineALineIngestionThroughReconciliation(t *testing.T) {
	archive, err := os.ReadFile("../test/fixtures/reconcile/a-line.zip")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile("../test/fixtures/reconcile/a-line.pb")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 13, 4, 9, 0, 0, time.UTC)
	now := func() time.Time { return at }
	schedules := &schedule.Store{}
	realtimeStore := realtime.NewStore()
	results := &reconcile.Store{}
	client := &http.Client{Transport: fixtureTransport{"/schedule": archive, "/realtime": wire}}
	compute := func() {
		results.Publish(reconcile.Reconcile(schedules.Load(), []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: realtimeStore.Load(at, time.Minute)}}, at, time.Minute, 24*time.Hour))
	}
	loader := scheduleingest.NewLoader(client, schedules, nil, scheduleingest.Config{SupplementedURL: "https://fixture/schedule"}, slog.New(slog.NewTextHandler(io.Discard, nil)), now)
	loader.OnPublication = func(string) { compute() }
	loader.Refresh(t.Context())
	if results.Load() == nil || !results.Load().Metadata().ScheduleEligible {
		t.Fatal("schedule ingestion/publication failed")
	}
	if err := ingest.NewLoader(client, realtimeStore, "ace", "https://fixture/realtime", "", 30*time.Second, now).Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	compute()
	bundle := results.Load()
	matches := bundle.Matches()
	instance := reconcile.Instance{TripID: "BSP26GEN-A055-Sunday-00_000950_A..S74R", ServiceDate: "20260913"}
	if len(matches) != 1 || matches[0].Outcome != reconcile.Matched || matches[0].Instance != instance || matches[0].Stops[0].StopID != "A02S" || matches[0].Train.Stops[0].Arrival.Time.Unix() != at.Unix()+30 {
		t.Fatalf("%+v", matches)
	}
	if matches[0].Train.Active() || bundle.Fallback(instance, at).Reason != "active_realtime_counterpart" {
		t.Fatal("compact fields required for active counterpart")
	}
	rules := bundle.ReplacementRules()
	if len(rules) != 1 || !rules[0].Valid || rules[0].Period.Start != nil || rules[0].Reference.Source.Name != "ace" || rules[0].Reference.Source.SourceTimestamp != at || rules[0].Reference.Source.ReceivedAt != at {
		t.Fatalf("%+v", rules)
	}
	// The same complete feed with no trip updates establishes absence.
	snapshot := realtimeStore.Load(at, time.Minute)
	snapshot.Trains = nil
	empty := reconcile.Reconcile(schedules.Load(), []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: snapshot}}, at, time.Minute, 24*time.Hour)
	if !empty.Fallback(instance, at).Suppressed || empty.Fallback(instance, at.Add(time.Minute+time.Nanosecond)).Suppressed {
		t.Fatal("replacement authority did not expire")
	}
}

func TestOfflineALineGeometryAndPositionLifecycle(t *testing.T) {
	for _, conflicts := range []bool{false, true} {
		name := "clean"
		if conflicts {
			name = "tolerated conflicts"
		}
		t.Run(name, func(t *testing.T) { checkOfflineALineLifecycle(t, conflicts) })
	}
}

func checkOfflineALineLifecycle(t *testing.T, conflicts bool) {
	archive, err := os.ReadFile("../test/fixtures/reconcile/a-line.zip")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile("../test/fixtures/reconcile/a-line.pb")
	if err != nil {
		t.Fatal(err)
	}
	initial := time.Date(2026, 9, 13, 4, 9, 0, 0, time.UTC)
	at := initial.Add(time.Minute)
	now := func() time.Time { return at }
	var feed gtfs.FeedMessage
	if err := proto.Unmarshal(wire, &feed); err != nil {
		t.Fatal(err)
	}
	update := feed.Entity[0].TripUpdate
	if update == nil {
		t.Fatal("fixture first entity is not a trip update")
	}
	// Preserve the sequence-only fixture and add the second event to bracket now.
	update.StopTimeUpdate[0].Departure = &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(initial.Add(30 * time.Second).Unix())}
	update.StopTimeUpdate = append(update.StopTimeUpdate, &gtfs.TripUpdate_StopTimeUpdate{
		StopSequence: proto.Uint32(2), Arrival: &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(initial.Add(630 * time.Second).Unix())},
	})
	var warnings []string
	if conflicts {
		update.Trip.StartTime = proto.String("00:10:00")
		update.StopTimeUpdate[0].StopId = proto.String("A02S")
		update.StopTimeUpdate[0].StopSequence = proto.Uint32(2)
		update.StopTimeUpdate[1].StopId = proto.String("A03S")
		update.StopTimeUpdate[1].StopSequence = proto.Uint32(1)
		warnings = []string{"start_time_conflict", "stop_id_sequence_conflict"}
	}
	wire, err = proto.Marshal(&feed)
	if err != nil {
		t.Fatal(err)
	}
	schedules, realtimeStore, results := &schedule.Store{}, realtime.NewStore(), &reconcile.Store{}
	transport := fixtureTransport{"/schedule": archive, "/realtime": wire}
	client := &http.Client{Transport: transport}
	loader := scheduleingest.NewLoader(client, schedules, nil, scheduleingest.Config{SupplementedURL: "https://fixture/schedule"}, slog.New(slog.NewTextHandler(io.Discard, nil)), now)
	loader.Refresh(t.Context())
	realtimeLoader := ingest.NewLoader(client, realtimeStore, "ace", "https://fixture/realtime", "", 30*time.Second, now)
	if err := realtimeLoader.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	compute := func() {
		results.Publish(reconcile.Reconcile(schedules.Load(), []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: realtimeStore.Load(at, time.Minute)}}, at, time.Minute, 24*time.Hour))
	}
	compute()
	h := httpapi.NewHandler([]httpapi.Source{{Name: "ace", Routes: []string{"A"}, Store: realtimeStore}}, telemetry.New(), now, time.Minute, results)
	var geometry struct {
		ScheduleVersion string                `json:"schedule_version"`
		Stations        []struct{ ID string } `json:"stations"`
		Shapes          []struct {
			Geometry struct {
				Coordinates [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"shapes"`
	}
	g := request(h, "/api/v1/routes/A/geometry")
	if err := json.Unmarshal(g.Body.Bytes(), &geometry); err != nil {
		t.Fatal(err)
	}
	if g.Code != http.StatusOK || len(geometry.Stations) != 2 || geometry.Stations[0].ID != "A02" || len(geometry.Shapes) != 1 || len(geometry.Shapes[0].Geometry.Coordinates) != 3 || geometry.Shapes[0].Geometry.Coordinates[1] != [2]float64{-74, 40.8} {
		t.Fatalf("geometry = %d/%s", g.Code, g.Body.String())
	}
	type projected struct {
		Warnings            []string `json:"reconciliation_warnings"`
		TripInstanceID      string   `json:"trip_instance_id"`
		ScheduleVersion     string   `json:"schedule_version"`
		Basis               string   `json:"position_basis"`
		Freshness           string   `json:"freshness"`
		Movement            string   `json:"movement_state"`
		Latitude, Longitude *float64
		SourceTimestamp     string  `json:"source_timestamp"`
		Reason              *string `json:"no_position_reason"`
	}
	positions := func() []projected {
		t.Helper()
		r := request(h, "/api/v1/trains?route_id=A&view=positions")
		var body struct {
			Trains []projected `json:"trains"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if r.Code != http.StatusOK || len(body.Trains) != 1 {
			t.Fatalf("positions = %d/%s", r.Code, r.Body.String())
		}
		if body.Trains[0].Warnings == nil || !slices.Equal(body.Trains[0].Warnings, warnings) {
			t.Fatalf("warnings = %v, want %v", body.Trains[0].Warnings, warnings)
		}
		return body.Trains
	}
	fresh := positions()[0]
	if fresh.Basis != "realtime_prediction" || fresh.TripInstanceID == "" || fresh.ScheduleVersion != geometry.ScheduleVersion || fresh.Latitude == nil || *fresh.Latitude != 40.8 || fresh.Longitude == nil || *fresh.Longitude >= -73.9 || fresh.Reason != nil {
		t.Fatalf("bent-polyline projection = %+v", fresh)
	}
	// Crossing the freshness boundary changes the basis of the same retained record.
	at = at.Add(time.Second)
	stale := positions()[0]
	if stale.TripInstanceID != fresh.TripInstanceID || stale.Basis != "schedule_fallback" || stale.Freshness != "stale" || stale.Movement != "unknown" || stale.Latitude == nil || stale.SourceTimestamp != fresh.SourceTimestamp {
		t.Fatalf("stale simulation = %+v", stale)
	}
	// A failed refresh retains the same schedule and realtime evidence.
	transport["/schedule"], transport["/realtime"] = []byte("invalid"), []byte("invalid")
	loader.Refresh(t.Context())
	if err := realtimeLoader.Load(t.Context()); err == nil {
		t.Fatal("malformed realtime replacement accepted")
	}
	if retained := positions()[0]; retained.TripInstanceID != fresh.TripInstanceID || retained.ScheduleVersion != fresh.ScheduleVersion {
		t.Fatalf("failed refresh replaced retained position: %+v", retained)
	}
	// A clean replacement removes tolerated warnings without changing run identity.
	if conflicts {
		update.Trip.StartTime = nil
		for i, stop := range update.StopTimeUpdate {
			stop.StopId = nil
			stop.StopSequence = proto.Uint32(uint32(i + 1))
		}
		warnings = nil
	}
	feed.Header.Timestamp = proto.Uint64(uint64(at.Unix()))
	wire, err = proto.Marshal(&feed)
	if err != nil {
		t.Fatal(err)
	}
	transport["/realtime"] = wire
	if err := realtimeLoader.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	compute()
	recovered := positions()[0]
	if recovered.TripInstanceID != fresh.TripInstanceID || recovered.Basis != "realtime_prediction" || recovered.Latitude == nil || *recovered.Longitude >= *fresh.Longitude {
		t.Fatalf("fresh recovery = %+v", recovered)
	}
	compact := request(h, "/api/v1/trains?route_id=A")
	if compact.Code != http.StatusOK || !strings.Contains(compact.Body.String(), `"trains":[]`) {
		t.Fatalf("sequence-only compact compatibility = %d/%s", compact.Code, compact.Body.String())
	}
}
