package projection

import (
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/reconcile"
	"hol-up-prototype/internal/schedule"
)

func loopFixture(t *testing.T, duplicateLoop bool) (*schedule.Version, time.Time) {
	t.Helper()
	return fixture(t, func(d *schedule.Data) {
		d.Shapes[0].Points = append(d.Shapes[0].Points, schedule.ShapePoint{Sequence: 3})
		last := d.Trips[0].Stops[0]
		last.Sequence = 3
		last.Arrival.Seconds, last.Departure.Seconds = 44400, 44400
		d.Trips[0].Stops = append(d.Trips[0].Stops, last)
		if duplicateLoop {
			for i := 1; i <= 3; i++ {
				p := d.Shapes[0].Points[i]
				p.Sequence += 3
				d.Shapes[0].Points = append(d.Shapes[0].Points, p)
			}
		}
	})
}

func TestRepeatedStopProjectionUsesOccurrenceAndRejectsAmbiguousAnchors(t *testing.T) {
	v, now := loopFixture(t, false)
	r := record(now)
	r.Stops[0].Sequence = ptr(uint32(2))
	r.Stops[1].Sequence = ptr(uint32(3))
	position := only(t, bundle(v, now, r), now)
	if position.Latitude == nil || math.Abs(*position.Latitude-.005) > 1e-8 || math.Abs(*position.Longitude-.005) > 1e-8 {
		t.Fatalf("wrong repeated-stop occurrence on return segment: %+v", position)
	}
	r.Stops[1].StopID, r.Stops[1].Sequence = "start", nil
	ambiguous := only(t, bundle(v, now, r), now)
	if ambiguous.Latitude != nil || ambiguous.NullReason != "repeated_stop" {
		t.Fatalf("guessed ambiguous realtime stop identity: %+v", ambiguous)
	}
	v, now = loopFixture(t, true)
	r.Stops[1].Sequence = ptr(uint32(3))
	ambiguous = only(t, bundle(v, now, r), now)
	if ambiguous.Latitude != nil || ambiguous.NullReason != "ambiguous_stop_anchor" {
		t.Fatalf("guessed loop's geometric anchor: %+v", ambiguous)
	}
}

func TestPartialAnchorsAllowResolvedDwellAndRetainNullIntervals(t *testing.T) {
	v, now := fixture(t, func(d *schedule.Data) { d.Stops[0].HasCoordinates = false })
	r := record(now)
	missing := only(t, bundle(v, now, r), now)
	if missing.Latitude != nil || missing.NullReason != "missing_stop_coordinates" || missing.PositionBasis != realtime.PositionBasisRealtimePrediction {
		t.Fatalf("missing interval anchor simulated or guessed: %+v", missing)
	}
	r.VehicleMatch, r.VehicleStatus, r.VehicleTimestamp = realtime.VehicleMatched, "STOPPED_AT", now
	r.VehicleTimestampPresent, r.VehicleStopSequence = true, ptr(uint32(2))
	r.Stops = nil
	stopped := only(t, bundle(v, now, r), now)
	if stopped.Latitude == nil || *stopped.Latitude != .01 || *stopped.Longitude != .01 || stopped.MovementState != realtime.MovementStateReportedStopped {
		t.Fatalf("unrelated missing geometry vetoed resolved dwell: %+v", stopped)
	}
}

func TestGeometryVersionChangesCoordinatesWithoutChangingRunIdentity(t *testing.T) {
	first, now := fixture(t, nil)
	second, _ := fixture(t, func(d *schedule.Data) {
		for i := range d.Stops {
			d.Stops[i].Longitude += .03
		}
		for i := range d.Shapes[0].Points {
			d.Shapes[0].Points[i].Longitude += .03
		}
	})
	meta := second.Metadata()
	meta.ArchiveSHA256 = strings.Repeat("b", 64)
	var err error
	second, err = schedule.NewVersion(second.Dataset(), meta)
	if err != nil {
		t.Fatal(err)
	}
	firstBundle, secondBundle := bundle(first, now, record(now)), bundle(second, now, record(now))
	var store reconcile.Store
	store.Publish(firstBundle)
	held := store.Load()
	var publication sync.WaitGroup
	publication.Go(func() {
		for range 100 {
			store.Publish(secondBundle)
			store.Publish(firstBundle)
		}
	})
	for range 100 {
		captured := store.Load()
		position := only(t, captured, now)
		wantLongitude := .01
		if captured == secondBundle {
			wantLongitude = .04
		}
		if position.Latitude == nil || math.Abs(*position.Longitude-wantLongitude) > 1e-8 || position.ScheduleVersion != captured.Schedule().Metadata().Version {
			t.Fatalf("position mixed captured geometry versions: %+v", position)
		}
	}
	publication.Wait()
	old := only(t, held, now)
	updated := only(t, secondBundle, now)
	if old.ScheduleVersion == updated.ScheduleVersion || old.TripInstanceID != updated.TripInstanceID || old.SourceRecordID != updated.SourceRecordID || math.Abs(*old.Longitude-.01) > 1e-8 || math.Abs(*updated.Longitude-.04) > 1e-8 {
		t.Fatalf("geometry publication changed run identity or held data: old=%+v new=%+v", old, updated)
	}
}
