package schedule_test

import (
	"math"
	"slices"
	"testing"

	"hol-up-prototype/internal/schedule"
)

func geometricFixture(t *testing.T) schedule.Data {
	t.Helper()
	data, _ := fixture(t, "A")
	data.Stops = []schedule.Stop{
		{ID: "P1", Name: "First station", LocationType: 1, HasCoordinates: true},
		{ID: "S1", ParentID: "P1", HasCoordinates: true},
		{ID: "S2", Latitude: 0, Longitude: .002, HasCoordinates: true},
		{ID: "S3", Latitude: .002, Longitude: .002, HasCoordinates: true},
		{ID: "S4", Latitude: -.002, Longitude: .002, HasCoordinates: true},
	}
	data.Shapes = []schedule.Shape{{ID: "north", Points: []schedule.ShapePoint{
		{Sequence: 30, Latitude: .002, Longitude: .002, ShapeDistance: schedule.OptionalDistance{Value: 10, Present: true}},
		{Sequence: 10, ShapeDistance: schedule.OptionalDistance{Value: 0, Present: true}},
		{Sequence: 20, Longitude: .002, ShapeDistance: schedule.OptionalDistance{Value: 5, Present: true}},
	}}}
	data.Trips = []schedule.Trip{geometryTrip("north_1", "A", "north", 0, "S1", "S2", "S3")}
	return data
}

func geometryTrip(id, route, shape string, direction int, stops ...string) schedule.Trip {
	trip := schedule.Trip{ID: id, RouteID: route, ShapeID: shape, Direction: direction, ServiceID: "daily"}
	for i, stopID := range stops {
		at := schedule.OptionalTime{Seconds: int32(3600 + i*60), Present: true}
		trip.Stops = append(trip.Stops, schedule.StopTime{StopID: stopID, Sequence: uint32(10 + i*10), Arrival: at, Departure: at})
	}
	return trip
}

func TestGeometryDirectionsBranchesGenericRouteAndImmutablePatterns(t *testing.T) {
	data := geometricFixture(t)
	data.Shapes = append(data.Shapes,
		schedule.Shape{ID: "south", Points: []schedule.ShapePoint{{Sequence: 1, Latitude: .002, Longitude: .002}, {Sequence: 2, Longitude: .002}, {Sequence: 3}}},
		schedule.Shape{ID: "branch", Points: []schedule.ShapePoint{{Sequence: 1}, {Sequence: 2, Longitude: .002}, {Sequence: 3, Latitude: -.002, Longitude: .002}}},
	)
	data.Routes = append(data.Routes, schedule.Route{ID: "B", ShortName: "B"})
	data.Trips = append(data.Trips,
		geometryTrip("north_2", "A", "north", 0, "S1", "S2", "S3"),
		geometryTrip("south", "A", "south", 1, "S3", "S2", "S1"),
		geometryTrip("branch", "A", "branch", 0, "S1", "S2", "S4"),
		geometryTrip("generic", "B", "north", 0, "S1", "S2", "S3"),
	)
	for i := range data.Trips[1].Stops {
		data.Trips[1].Stops[i].Arrival.Seconds += 600
		data.Trips[1].Stops[i].Departure.Seconds += 600
	}
	d, err := schedule.NewDataset(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.RoutePatterns("A")) != 3 || len(d.RoutePatterns("B")) != 1 || len(d.RouteShapes("A")) != 3 {
		t.Fatalf("incorrect pattern grouping: A=%+v B=%+v", d.RoutePatterns("A"), d.RoutePatterns("B"))
	}
	first, _ := d.TripPattern("north_1")
	second, _ := d.TripPattern("north_2")
	generic, _ := d.TripPattern("generic")
	if first.ID != second.ID || first.ID == generic.ID || len(first.ID) != 64 || first.Reason != "" {
		t.Fatalf("unstable or incorrect identity: %+v / %+v / %+v", first, second, generic)
	}
	for _, route := range []string{"A", "B"} {
		for _, pattern := range d.RoutePatterns(route) {
			for i, stop := range pattern.Stops {
				if !stop.HasDistance || stop.Reason != "" || (i > 0 && stop.DistanceMeters <= pattern.Stops[i-1].DistanceMeters) {
					t.Fatalf("unresolved ordered anchor: %+v", pattern)
				}
			}
		}
	}
	stations := d.RouteStations("A")
	if len(stations) != 4 || stations[0].ID != "P1" || first.Stops[0].StopID != "S1" || first.Stops[0].StationID != "P1" {
		t.Fatalf("canonical stations lost platforms: %+v %+v", stations, first.Stops)
	}
	shape, _ := d.Shape("north")
	length := shape.Points[2].DistanceMeters
	lat, lon, ok := shape.At(length / 2)
	if !ok || math.Abs(lat) > 1e-12 || math.Abs(lon-.002) > 1e-12 {
		t.Fatalf("bent midpoint = %f,%f; station straight line would be .001,.001", lat, lon)
	}
	lat, lon, ok = shape.At(length / 4)
	if !ok || math.Abs(lat) > 1e-12 || math.Abs(lon-.001) > 1e-12 {
		t.Fatalf("first segment midpoint = %f,%f", lat, lon)
	}
	for _, distance := range []float64{-1, length + 1, math.NaN(), math.Inf(1)} {
		if _, _, ok := shape.At(distance); ok {
			t.Fatalf("extrapolated %f", distance)
		}
	}
	data.Shapes[0].Points[0].Latitude = 50
	shape.Points[0].Latitude = 50
	first.Stops[0].DistanceMeters = 1000
	patterns := d.RoutePatterns("A")
	patterns[0].Stops[0].StopID = "changed"
	shapes := d.RouteShapes("A")
	shapes[0].Points[0].Latitude = 50
	stations[0].ID = "changed"
	shape, _ = d.Shape("north")
	first, _ = d.TripPattern("north_1")
	if shape.Points[0].Latitude != 0 || first.Stops[0].DistanceMeters != 0 || first.Stops[0].StopID != "S1" || d.RouteStations("A")[0].ID != "P1" {
		t.Fatal("geometry mutated through staging or lookup slices")
	}
}

func TestSuppliedDistancesConvertUnitsAndFallback(t *testing.T) {
	for _, mode := range []string{"complete", "partial shape", "partial stop", "inconsistent units"} {
		t.Run(mode, func(t *testing.T) {
			data := geometricFixture(t)
			for i := range data.Trips[0].Stops {
				data.Trips[0].Stops[i].ShapeDistance = schedule.OptionalDistance{Value: float64(i * 5), Present: true}
			}
			if mode == "partial shape" {
				data.Shapes[0].Points[2].ShapeDistance = schedule.OptionalDistance{}
			}
			if mode == "partial stop" {
				data.Trips[0].Stops[1].ShapeDistance = schedule.OptionalDistance{}
			}
			if mode == "inconsistent units" {
				data.Trips[0].Stops[1].ShapeDistance.Value = 5000
				data.Trips[0].Stops[2].ShapeDistance.Value = 10000
			}
			d, err := schedule.NewDataset(data)
			if err != nil {
				t.Fatal(err)
			}
			p, _ := d.TripPattern("north_1")
			if p.Reason != "" || math.Abs(p.Stops[2].DistanceMeters-444.7803209341316) > .01 {
				t.Fatalf("GTFS units treated as meters: %+v", p)
			}
			want := "shape_dist_traveled"
			if mode == "partial stop" || mode == "inconsistent units" {
				want = "platform_projection"
			}
			if p.Stops[1].Association != want {
				t.Fatalf("association=%s, want %s", p.Stops[1].Association, want)
			}
		})
	}
}

func TestRepeatedStopsOrderAndAmbiguousLoops(t *testing.T) {
	data := geometricFixture(t)
	data.Shapes = []schedule.Shape{{ID: "loop", Points: []schedule.ShapePoint{{Sequence: 1}, {Sequence: 2, Longitude: .002}, {Sequence: 3}}}}
	data.Trips = []schedule.Trip{geometryTrip("loop", "A", "loop", 0, "S1", "S2", "S1")}
	d, err := schedule.NewDataset(data)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := d.TripPattern("loop")
	if p.Reason != "" || !p.Stops[2].HasDistance || p.Stops[2].DistanceMeters <= p.Stops[0].DistanceMeters {
		t.Fatalf("occurrence order did not resolve loop: %+v", p)
	}
	data.Shapes[0].Points = append(data.Shapes[0].Points, schedule.ShapePoint{Sequence: 4, Longitude: .002}, schedule.ShapePoint{Sequence: 5})
	d, err = schedule.NewDataset(data)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = d.TripPattern("loop")
	for _, stop := range p.Stops {
		if stop.HasDistance || stop.Reason != "ambiguous_stop_anchor" {
			t.Fatalf("guessed repeated loop anchor: %+v", p)
		}
	}
}

func TestMissingAndContradictoryGeometryReasons(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		mutate       func(*schedule.Data)
	}{
		{"missing shape", "missing_shape", func(d *schedule.Data) { d.Shapes = nil }},
		{"single point", "unusable_shape", func(d *schedule.Data) { d.Shapes[0].Points = d.Shapes[0].Points[:1] }},
		{"zero length", "unusable_shape", func(d *schedule.Data) {
			for i := range d.Shapes[0].Points {
				d.Shapes[0].Points[i].Latitude, d.Shapes[0].Points[i].Longitude = 0, 0
			}
		}},
		{"too far", "stop_too_far_from_shape", func(d *schedule.Data) { d.Stops[1].Latitude = 1 }},
		{"missing platform", "missing_stop_coordinates", func(d *schedule.Data) { d.Stops[1].HasCoordinates = false }},
		{"contradictory spatial", "contradictory_stop_anchors", func(d *schedule.Data) {
			d.Trips[0] = geometryTrip("north_1", "A", "north", 0, "S3", "S2", "S1")
		}},
		{"contradictory across missing", "contradictory_stop_anchors", func(d *schedule.Data) {
			d.Trips[0] = geometryTrip("north_1", "A", "north", 0, "S3", "S2", "S1")
			d.Stops[2].HasCoordinates = false
		}},
		{"contradictory supplied", "contradictory_stop_distances", func(d *schedule.Data) {
			d.Trips[0].Stops[0].ShapeDistance = schedule.OptionalDistance{Value: 10, Present: true}
			d.Trips[0].Stops[2].ShapeDistance = schedule.OptionalDistance{Value: 0, Present: true}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := geometricFixture(t)
			tc.mutate(&data)
			d, err := schedule.NewDataset(data)
			if err != nil {
				t.Fatal(err)
			}
			p, _ := d.TripPattern("north_1")
			if p.Reason == "" || p.Stops[0].Reason != tc.reason || p.Stops[0].HasDistance {
				t.Fatalf("unresolved geometry guessed: %+v", p)
			}
		})
	}
}

func TestMalformedShapeInputRejected(t *testing.T) {
	for _, mutate := range []func(*schedule.Data){
		func(d *schedule.Data) { d.Shapes = append(d.Shapes, d.Shapes[0]) },
		func(d *schedule.Data) { d.Shapes[0].ID = "" },
		func(d *schedule.Data) { d.Shapes[0].Points[0].Sequence = 10 },
		func(d *schedule.Data) { d.Shapes[0].Points[0].Latitude = math.NaN() },
		func(d *schedule.Data) { d.Shapes[0].Points[0].Longitude = math.Inf(1) },
		func(d *schedule.Data) { d.Shapes[0].Points[0].Latitude = 91 },
		func(d *schedule.Data) { d.Shapes[0].Points[0].Longitude = -181 },
		func(d *schedule.Data) { d.Shapes[0].Points[0].ShapeDistance.Value = -1 },
		func(d *schedule.Data) { d.Shapes[0].Points[0].ShapeDistance.Value = 1 },
	} {
		data := geometricFixture(t)
		mutate(&data)
		if _, err := schedule.NewDataset(data); err == nil {
			t.Fatal("accepted malformed shape")
		}
	}
	data := geometricFixture(t)
	d1, _ := schedule.NewDataset(data)
	slices.Reverse(data.Shapes[0].Points)
	d2, _ := schedule.NewDataset(data)
	p1, _ := d1.TripPattern("north_1")
	p2, _ := d2.TripPattern("north_1")
	if p1.ID != p2.ID {
		t.Fatal("row order changed geometry identity")
	}
}

func TestOnlyEquivalentSharedEndpointsAreCoalesced(t *testing.T) {
	data := geometricFixture(t)
	// The platform is about 33 cm before the bend. Its projection onto the
	// incoming segment and the outgoing segment endpoint are distinct candidates,
	// even though their offsets differ by less than the 1 m ambiguity tolerance.
	data.Stops[2].Longitude -= .000003
	d, err := schedule.NewDataset(data)
	if err != nil {
		t.Fatal(err)
	}
	pattern, _ := d.TripPattern("north_1")
	if pattern.Stops[1].HasDistance || pattern.Stops[1].Reason != "ambiguous_stop_anchor" {
		t.Fatalf("nearby distinct candidates collapsed: %+v", pattern)
	}
	if !pattern.Stops[0].HasDistance || !pattern.Stops[2].HasDistance {
		t.Fatal("independent unambiguous endpoints were discarded")
	}
}
