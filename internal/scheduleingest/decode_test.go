package scheduleingest

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"

	"hol-up-prototype/internal/schedule"
)

func fixtureFiles() map[string]string {
	return map[string]string{
		"agency.txt":         "\ufeff\"agency_timezone\",agency_url,agency_name,agency_id,extra\r\nAmerica/New_York,https://www.mta.info,\"MTA, Transit\",mta,ignored\r\n",
		"routes.txt":         "route_type,route_id,route_short_name,agency_id\n1,A,A,mta\n1,SI,SI,mta\n",
		"stops.txt":          "stop_id,stop_name,stop_lat,stop_lon,location_type,parent_station\nP,Station,40,-73,1,\nS1,First,40,-73,0,P\nS2,Second,40,-73,0,\n",
		"trips.txt":          "trip_id,service_id,route_id,direction_id,shape_id\nstatic_090000_A..N,daily,A,0,A..N\n",
		"stop_times.txt":     "stop_sequence,stop_id,departure_time,trip_id,arrival_time,timepoint\n9,S2,26:00:00,static_090000_A..N,26:00:00,1\n1,S1,23:00:00,static_090000_A..N,23:00:00,1\n5,S1,,static_090000_A..N,,0\n",
		"calendar.txt":       "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\ndaily,1,1,1,1,1,1,1,20260101,20261231\n",
		"calendar_dates.txt": "service_id,date,exception_type\ndaily,20260913,2\n",
		"feed_info.txt":      "feed_publisher_name,feed_publisher_url,feed_lang,feed_start_date,feed_end_date,feed_version\nMTA,https://www.mta.info,en,20260101,20261231,fixture\n",
		"shapes.txt":         "shape_id,shape_pt_lat,shape_pt_lon,shape_pt_sequence,shape_dist_traveled\nA..N,40,-73,9,10\nA..N,40.001,-73,5,5\nA..N,40,-73,1,0\n",
		"unused.txt":         "unused but checksum-validated\n",
	}
}

func zipFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, value := range files {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDecodeHeadersTimesAndExceptionOnlyCalendar(t *testing.T) {
	files := fixtureFiles()
	d, err := Decode(zipFixture(t, files))
	if err != nil {
		t.Fatal(err)
	}
	trip, ok := d.Trip("static_090000_A..N")
	if !ok || trip.Direction != 0 || len(trip.Stops) != 3 || trip.Stops[0].Sequence != 1 || trip.Stops[1].Arrival.Present || trip.Stops[1].Departure.Present || trip.Stops[2].Arrival.Seconds != 93600 {
		t.Fatalf("trip = %+v", trip)
	}
	if d.Counts().Routes != 2 || d.Agencies()[0].Timezone != schedule.Timezone || d.FeedInfo().Version != "fixture" || d.Active("daily", "20260913") {
		t.Fatalf("dataset = %+v", d.Counts())
	}
	shape, ok := d.Shape("A..N")
	if !ok || !shape.Usable || len(shape.Points) != 3 || shape.Points[0].Sequence != 1 || shape.Points[2].ShapeDistance.Value != 10 || shape.Points[2].DistanceMeters < 200 {
		t.Fatalf("shape geometry not retained in sequence order: %+v", shape)
	}
	delete(files, "calendar.txt")
	files["calendar_dates.txt"] = "service_id,date,exception_type\ndaily,20260912,1\n"
	d, err = Decode(zipFixture(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Active("daily", "20260912") || d.Active("daily", "20260913") {
		t.Fatal("exception-only service lost")
	}
	for _, tc := range []struct {
		raw     string
		seconds int32
		present bool
	}{{"", 0, false}, {"0:00:00", 0, true}, {"00:00:00", 0, true}, {"49:10:03", 177003, true}} {
		got, err := parseTime(tc.raw)
		if err != nil || got.Seconds != tc.seconds || got.Present != tc.present {
			t.Errorf("parseTime(%q)=%+v/%v", tc.raw, got, err)
		}
	}
	for _, value := range []string{"-1:00:00", "1:60:00", "24:00:60", "1:0:00", "1:00", "99999999999999:00:00", "+1:00:00"} {
		if _, err := parseTime(value); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
}

func TestDecodeRejectsInvalidData(t *testing.T) {
	for _, tc := range []struct{ name, file, old, new string }{
		{"missing column", "trips.txt", "service_id", "wrong"},
		{"duplicate column", "trips.txt", "service_id", "trip_id"},
		{"empty ID", "routes.txt", "1,A,A,mta", "1,,A,mta"},
		{"duplicate route", "routes.txt", "1,SI,SI,mta", "1,A,A,mta"},
		{"duplicate agency", "agency.txt", "ignored\r\n", "ignored\r\nAmerica/New_York,https://www.mta.info,MTA,mta,ignored\r\n"},
		{"duplicate stop", "stops.txt", "S2,Second", "S1,Second"},
		{"duplicate trip", "trips.txt", "static_090000_A..N,daily,A,0,A..N\n", "static_090000_A..N,daily,A,0,A..N\nstatic_090000_A..N,daily,A,0,A..N\n"},
		{"duplicate calendar", "calendar.txt", "daily,1,1,1,1,1,1,1,20260101,20261231\n", "daily,1,1,1,1,1,1,1,20260101,20261231\ndaily,1,1,1,1,1,1,1,20260101,20261231\n"},
		{"duplicate exception", "calendar_dates.txt", "daily,20260913,2\n", "daily,20260913,2\ndaily,20260913,1\n"},
		{"unknown route", "trips.txt", ",daily,A,", ",daily,X,"},
		{"unknown service", "trips.txt", ",daily,A,", ",unknown,A,"},
		{"unknown agency", "routes.txt", ",mta", ",unknown"},
		{"unknown trip", "stop_times.txt", "static_090000_A..N", "unknown"},
		{"unknown stop", "stop_times.txt", ",S2,", ",unknown,"},
		{"unknown parent", "stops.txt", ",0,P", ",0,unknown"},
		{"station stop time", "stop_times.txt", ",S2,", ",P,"},
		{"duplicate sequence", "stop_times.txt", "9,S2", "1,S2"},
		{"negative sequence", "stop_times.txt", "9,S2", "-1,S2"},
		{"overflow sequence", "stop_times.txt", "9,S2", "4294967296,S2"},
		{"invalid date", "calendar.txt", "20260101", "20260230"},
		{"reversed dates", "calendar.txt", "20260101", "20270101"},
		{"invalid weekday", "calendar.txt", "daily,1", "daily,2"},
		{"invalid exception", "calendar_dates.txt", "20260913,2", "20260913,3"},
		{"invalid timezone", "agency.txt", "America/New_York", "UTC"},
		{"invalid direction", "trips.txt", ",0,A..N", ",2,A..N"},
		{"invalid coordinate", "stops.txt", "40,-73", "NaN,-73"},
		{"shape missing column", "shapes.txt", "shape_pt_lat", "wrong"},
		{"shape duplicate key", "shapes.txt", "-73,9,10", "-73,1,10"},
		{"shape empty ID", "shapes.txt", "A..N,40,-73,9,10", ",40,-73,9,10"},
		{"shape latitude", "shapes.txt", "40,-73,9,10", "91,-73,9,10"},
		{"shape longitude", "shapes.txt", "40,-73,9,10", "40,-181,9,10"},
		{"shape nonfinite coordinate", "shapes.txt", "40,-73,9,10", "NaN,-73,9,10"},
		{"shape nonfinite distance", "shapes.txt", "-73,9,10", "-73,9,Inf"},
		{"shape negative distance", "shapes.txt", "-73,9,10", "-73,9,-10"},
		{"shape decreasing distance", "shapes.txt", "-73,9,10", "-73,9,1"},
		{"shape negative sequence", "shapes.txt", "-73,9,10", "-73,-1,10"},
		{"shape overflowing sequence", "shapes.txt", "-73,9,10", "-73,4294967296,10"},
		{"invalid time", "stop_times.txt", "26:00:00", "26:61:00"},
		{"decreasing time", "stop_times.txt", "26:00:00", "22:00:00"},
		{"missing endpoint time", "stop_times.txt", "26:00:00", ""},
		{"missing timepoint time", "stop_times.txt", ",,0\n", ",,1\n"},
		{"invalid metadata date", "feed_info.txt", "20261231", "20261232"},
		{"malformed CSV", "routes.txt", "1,A,A,mta", "1,\"A,A,mta"},
		{"invalid UTF8", "routes.txt", "1,A,A,mta", "1,A,\xff,mta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := fixtureFiles()
			if !strings.Contains(files[tc.file], tc.old) {
				t.Fatal("bad fixture mutation")
			}
			files[tc.file] = strings.ReplaceAll(files[tc.file], tc.old, tc.new)
			if _, err := Decode(zipFixture(t, files)); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	for _, name := range []string{"agency.txt", "routes.txt", "stops.txt", "trips.txt", "stop_times.txt"} {
		files := fixtureFiles()
		delete(files, name)
		if _, err := Decode(zipFixture(t, files)); err == nil {
			t.Errorf("accepted missing %s", name)
		}
	}
}

func TestOptionalGeometryAndStopDistances(t *testing.T) {
	files := fixtureFiles()
	files["stop_times.txt"] = "trip_id,stop_id,stop_sequence,arrival_time,departure_time,shape_dist_traveled\nstatic_090000_A..N,S1,1,23:00:00,23:00:00,0\nstatic_090000_A..N,S2,2,26:00:00,26:00:00,10\n"
	d, err := Decode(zipFixture(t, files))
	if err != nil {
		t.Fatal(err)
	}
	trip, _ := d.Trip("static_090000_A..N")
	pattern, _ := d.TripPattern(trip.ID)
	if !trip.Stops[0].ShapeDistance.Present || trip.Stops[1].ShapeDistance.Value != 10 || pattern.Stops[1].Association != "shape_dist_traveled" || !pattern.Stops[1].HasDistance {
		t.Fatalf("stop distance presence lost: %+v %+v", trip, pattern)
	}
	for _, value := range []string{"NaN", "Inf", "-1", "invalid"} {
		bad := fixtureFiles()
		bad["stop_times.txt"] = strings.ReplaceAll(files["stop_times.txt"], "26:00:00,10", "26:00:00,"+value)
		if _, err := Decode(zipFixture(t, bad)); err == nil {
			t.Fatalf("accepted invalid stop distance %s", value)
		}
	}
	delete(files, "shapes.txt")
	d, err = Decode(zipFixture(t, files))
	if err != nil {
		t.Fatal(err)
	}
	pattern, _ = d.TripPattern(trip.ID)
	if pattern.Reason != "missing_shape" || pattern.Stops[0].HasDistance {
		t.Fatalf("missing geometry did not retain schedule with a reason: %+v", pattern)
	}
}

func TestArchiveAndRecordBounds(t *testing.T) {
	archive := zipFixture(t, fixtureFiles())
	for _, tc := range []struct {
		name   string
		change func(*limits)
	}{
		{"compressed", func(l *limits) { l.archive = int64(len(archive) - 1) }},
		{"entry", func(l *limits) { l.entry = 100 }},
		{"total", func(l *limits) { l.total = 300 }},
		{"entries", func(l *limits) { l.entries = 2 }},
		{"record", func(l *limits) { l.record = 32 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := defaultLimits
			tc.change(&l)
			if _, err := decode(archive, l); !errors.Is(err, ErrLimit) {
				t.Fatalf("got %v, want limit", err)
			}
		})
	}
	for _, record := range []string{"a\n" + strings.Repeat("x", 100) + "\n", "a\n\"" + strings.Repeat("x\n", 100) + "\"\n", "a\n\"" + strings.Repeat("\"\"", 100) + "\"\n"} {
		if err := readCSV(strings.NewReader(record), 32, []string{"a"}, func(row) error { return nil }); !errors.Is(err, ErrLimit) {
			t.Fatalf("unbounded record: %v", err)
		}
	}
	if err := readCSV(strings.NewReader("a\n\"x\ny\"\n"), 6, []string{"a"}, func(row) error { return nil }); err != nil {
		t.Fatalf("valid bounded multiline: %v", err)
	}
	if err := readCSV(strings.NewReader("a\n"+strings.Repeat("x\n", 100)), 2, []string{"a"}, func(row) error { return nil }); err != nil {
		t.Fatalf("record budget did not reset: %v", err)
	}
	files := fixtureFiles()
	files["../outside"] = "never extract"
	if _, err := Decode(zipFixture(t, files)); err == nil {
		t.Fatal("accepted path")
	}
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for i := 0; i < 2; i++ {
		f, err := w.Create("agency.txt")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte("same name"))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(b.Bytes()); err == nil {
		t.Fatal("accepted duplicate filename")
	}
	corrupt := bytes.Clone(archive)
	index := bytes.Index(corrupt, []byte("unused but checksum-validated"))
	if index < 0 {
		t.Fatal("missing unused entry")
	}
	corrupt[index] = 'X'
	if _, err := Decode(corrupt); err == nil {
		t.Fatal("ignored unused checksum failure")
	}
	if _, err := Decode(archive[:len(archive)-25]); err == nil {
		t.Fatal("accepted truncated ZIP")
	}
	// Understate every declared uncompressed size. The actual output must still
	// be rejected, including entries not needed by the schedule model.
	forged := bytes.Clone(archive)
	for offset := 0; offset+46 < len(forged); offset++ {
		if bytes.Equal(forged[offset:offset+4], []byte{'P', 'K', 1, 2}) {
			binary.LittleEndian.PutUint32(forged[offset+24:offset+28], 1)
		}
	}
	if _, err := Decode(forged); err == nil {
		t.Fatal("accepted false declared sizes")
	}
	// A highly compressed entry must be rejected before inflation.
	b.Reset()
	w = zip.NewWriter(&b)
	f, _ := w.Create("bomb.txt")
	_, _ = f.Write(bytes.Repeat([]byte("x"), 1<<20))
	_ = w.Close()
	l := defaultLimits
	l.entry = 1 << 10
	if _, err := decode(b.Bytes(), l); !errors.Is(err, ErrLimit) {
		t.Fatalf("compressed bomb: %v", err)
	}
}

// Optional, reproducible real-archive check; never a network-dependent test gate.
func TestLocalMTAArchive(t *testing.T) {
	path := os.Getenv("HOLUP_TEST_SCHEDULE_ARCHIVE")
	if path == "" {
		t.Skip("set HOLUP_TEST_SCHEDULE_ARCHIVE to a downloaded MTA ZIP")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.RouteTrips("A")) == 0 {
		t.Fatal("archive contains no A trips")
	}
	t.Logf("archive_sha256=%s counts=%+v coverage=%+v feed_version=%s", archiveHash(b), d.Counts(), d.Coverage(), d.FeedInfo().Version)
}
