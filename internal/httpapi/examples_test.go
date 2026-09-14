package httpapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"hol-up-prototype/internal/projection"
	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/reconcile"
)

func TestDocumentedPositionLifecycleMatchesProjection(t *testing.T) {
	at := time.Date(2026, 9, 13, 4, 10, 0, 0, time.UTC)
	v := geometryVersion(t, at, "a", -73.9)
	trip := realtime.Train{TripID: "trip", EntityID: "update", RouteID: "A", ServiceDate: "20260913", VehicleMatch: realtime.VehicleUnmatched,
		Stops: []realtime.StopPrediction{
			{StopID: "platform", Departure: &realtime.StopEvent{Time: at.Add(-30 * time.Second)}},
			{StopID: "second", Arrival: &realtime.StopEvent{Time: at.Add(570 * time.Second)}},
		},
	}
	snapshot := realtime.Snapshot{SourceTimestamp: at.Add(-time.Minute), ReceivedAt: at.Add(-time.Minute), Complete: true, NYCTVersion: "1.0", Trains: []realtime.Train{trip}}
	sources := []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: snapshot}}
	b := reconcile.Reconcile(v, sources, at, time.Minute, time.Hour)
	for i, offset := range []time.Duration{0, time.Second} {
		now := at.Add(offset)
		p := positionDTO(projection.Positions(b, "A", now)[0], now)
		name := []string{"RealtimePositions", "StaleSchedulePositions"}[i]
		var documented positionsResponse
		if err := json.NewDecoder(strings.NewReader(documentedExample(t, name))).Decode(&documented); err != nil {
			t.Fatal(err)
		}
		if len(documented.Trains) != 1 || !reflect.DeepEqual(p, documented.Trains[0]) {
			t.Fatalf("%s differs from actual projection: %+v", name, p)
		}
	}
}

func TestDocumentedMissingScheduleMatchesResponse(t *testing.T) {
	at := time.Date(2026, 9, 13, 4, 10, 0, 0, time.UTC)
	results := &reconcile.Store{}
	results.Publish(reconcile.Reconcile(nil, []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: realtime.Snapshot{
		SourceTimestamp: at, ReceivedAt: at, Complete: true, NYCTVersion: "1.0",
		Trains: []realtime.Train{{EntityID: "update", RouteID: "A", TripID: "unmatched-trip", ServiceDate: "20260913", VehicleMatch: realtime.VehicleUnmatched}},
	}}}, at, time.Minute, time.Hour))
	h := NewHandler(nil, nil, func() time.Time { return at }, time.Minute, results)
	response := get(h, "/api/v1/trains?route_id=A&view=positions")
	var actual, documented positionsResponse
	decode(t, response, &actual)
	if err := json.NewDecoder(strings.NewReader(documentedExample(t, "PositionsWithoutSchedule"))).Decode(&documented); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || !reflect.DeepEqual(actual, documented) {
		t.Fatalf("documented missing-schedule response differs: %d/%s", response.Code, response.Body.String())
	}
}
