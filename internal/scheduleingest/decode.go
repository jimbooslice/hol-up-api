// Package scheduleingest adapts HTTP, ZIP/CSV and a disk cache to schedules.
package scheduleingest

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"hol-up-prototype/internal/schedule"
)

const (
	ParserSchema               = "2"
	MaxArchiveBytes      int64 = 64 << 20
	MaxUncompressedBytes int64 = 512 << 20
	MaxEntryBytes        int64 = 256 << 20
	MaxEntries                 = 64
	MaxRecordBytes             = 1 << 20
)

var (
	ErrLimit   = errors.New("schedule archive or CSV exceeds size limit")
	ErrInvalid = errors.New("invalid schedule archive")
)

type limits struct {
	archive, total, entry int64
	entries, record       int
}

var defaultLimits = limits{MaxArchiveBytes, MaxUncompressedBytes, MaxEntryBytes, MaxEntries, MaxRecordBytes}

func Decode(archive []byte) (*schedule.Dataset, error) { return decode(archive, defaultLimits) }

func decode(archive []byte, limit limits) (*schedule.Dataset, error) {
	if int64(len(archive)) > limit.archive {
		return nil, ErrLimit
	}
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if len(z.File) > limit.entries {
		return nil, ErrLimit
	}
	files := map[string]*zip.File{}
	var declared uint64
	for _, f := range z.File {
		if _, exists := files[f.Name]; exists || f.Name == "" || strings.ContainsAny(f.Name, "/\\") || !f.Mode().IsRegular() {
			return nil, ErrInvalid
		}
		if f.UncompressedSize64 > uint64(limit.entry) || f.UncompressedSize64 > uint64(limit.total)-declared {
			return nil, ErrLimit
		}
		declared += f.UncompressedSize64
		files[f.Name] = f
	}
	for _, name := range []string{"agency.txt", "routes.txt", "stops.txt", "trips.txt", "stop_times.txt"} {
		if files[name] == nil {
			return nil, fmt.Errorf("%w: missing %s", ErrInvalid, name)
		}
	}
	if files["calendar.txt"] == nil && files["calendar_dates.txt"] == nil {
		return nil, fmt.Errorf("%w: missing service calendars", ErrInvalid)
	}
	var data schedule.Data
	tripIndexes := map[string]int{}
	shapeIndexes := map[string]int{}
	tables := []struct {
		name     string
		required []string
		parse    func(row) error
	}{
		{"agency.txt", []string{"agency_name", "agency_url", "agency_timezone"}, func(r row) error {
			if err := r.require("agency_name", "agency_url", "agency_timezone"); err != nil {
				return err
			}
			if !validURL(r.get("agency_url")) {
				return ErrInvalid
			}
			data.Agencies = append(data.Agencies, schedule.Agency{ID: r.get("agency_id"), Name: r.get("agency_name"), URL: r.get("agency_url"), Timezone: r.get("agency_timezone")})
			return nil
		}},
		{"routes.txt", []string{"route_id", "route_type"}, func(r row) error {
			if err := r.require("route_id", "route_type"); err != nil {
				return err
			}
			t, err := integer(r.get("route_type"), 12)
			if err != nil || t == 8 || t == 9 || t == 10 {
				return ErrInvalid
			}
			data.Routes = append(data.Routes, schedule.Route{ID: r.get("route_id"), AgencyID: r.get("agency_id"), ShortName: r.get("route_short_name"), LongName: r.get("route_long_name"), Type: int(t)})
			return nil
		}},
		{"stops.txt", []string{"stop_id"}, func(r row) error {
			if err := r.require("stop_id"); err != nil {
				return err
			}
			locationType, err := optionalInteger(r.get("location_type"), 0, 4)
			if err != nil {
				return err
			}
			s := schedule.Stop{ID: r.get("stop_id"), Name: r.get("stop_name"), ParentID: r.get("parent_station"), LocationType: locationType}
			if locationType <= 2 && s.Name == "" {
				return ErrInvalid
			}
			lat, lon := r.get("stop_lat"), r.get("stop_lon")
			if lat != "" || lon != "" || locationType <= 2 {
				s.Latitude, err = coordinate(lat, 90)
				if err != nil {
					return err
				}
				s.Longitude, err = coordinate(lon, 180)
				if err != nil {
					return err
				}
				s.HasCoordinates = true
			}
			data.Stops = append(data.Stops, s)
			return nil
		}},
		{"calendar.txt", []string{"service_id", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday", "start_date", "end_date"}, func(r row) error {
			if err := r.require("service_id"); err != nil {
				return err
			}
			c := schedule.Calendar{ServiceID: r.get("service_id")}
			var err error
			c.Start, err = schedule.ParseDate(r.get("start_date"))
			if err != nil {
				return err
			}
			c.End, err = schedule.ParseDate(r.get("end_date"))
			if err != nil {
				return err
			}
			for i, name := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
				n, err := integer(r.get(name), 1)
				if err != nil {
					return err
				}
				c.Days[i] = n == 1
			}
			data.Calendars = append(data.Calendars, c)
			return nil
		}},
		{"calendar_dates.txt", []string{"service_id", "date", "exception_type"}, func(r row) error {
			if err := r.require("service_id"); err != nil {
				return err
			}
			date, err := schedule.ParseDate(r.get("date"))
			if err != nil {
				return err
			}
			t, err := integer(r.get("exception_type"), 2)
			if err != nil || t == 0 {
				return ErrInvalid
			}
			data.Exceptions = append(data.Exceptions, schedule.Exception{ServiceID: r.get("service_id"), Date: date, Added: t == 1})
			return nil
		}},
		{"trips.txt", []string{"trip_id", "route_id", "service_id"}, func(r row) error {
			if err := r.require("trip_id", "route_id", "service_id"); err != nil {
				return err
			}
			if _, exists := tripIndexes[r.get("trip_id")]; exists {
				return ErrInvalid
			}
			dir, err := optionalInteger(r.get("direction_id"), -1, 1)
			if err != nil {
				return err
			}
			tripIndexes[r.get("trip_id")] = len(data.Trips)
			data.Trips = append(data.Trips, schedule.Trip{ID: r.get("trip_id"), RouteID: r.get("route_id"), ServiceID: r.get("service_id"), Headsign: r.get("trip_headsign"), ShapeID: r.get("shape_id"), Direction: dir})
			return nil
		}},
		{"stop_times.txt", []string{"trip_id", "stop_id", "stop_sequence", "arrival_time", "departure_time"}, func(r row) error {
			if err := r.require("trip_id", "stop_id", "stop_sequence"); err != nil {
				return err
			}
			index, exists := tripIndexes[r.get("trip_id")]
			if !exists {
				return ErrInvalid
			}
			sequence, err := integer(r.get("stop_sequence"), math.MaxUint32)
			if err != nil {
				return err
			}
			arrival, err := parseTime(r.get("arrival_time"))
			if err != nil {
				return err
			}
			departure, err := parseTime(r.get("departure_time"))
			if err != nil {
				return err
			}
			tp, err := optionalInteger(r.get("timepoint"), 1, 1)
			if err != nil {
				return err
			}
			distance, err := optionalDistance(r.get("shape_dist_traveled"))
			if err != nil {
				return err
			}
			data.Trips[index].Stops = append(data.Trips[index].Stops, schedule.StopTime{StopID: r.get("stop_id"), Sequence: uint32(sequence), Arrival: arrival, Departure: departure, Timepoint: tp == 1, ShapeDistance: distance})
			return nil
		}},
		{"shapes.txt", []string{"shape_id", "shape_pt_lat", "shape_pt_lon", "shape_pt_sequence"}, func(r row) error {
			if err := r.require("shape_id", "shape_pt_lat", "shape_pt_lon", "shape_pt_sequence"); err != nil {
				return err
			}
			latitude, err := coordinate(r.get("shape_pt_lat"), 90)
			if err != nil {
				return err
			}
			longitude, err := coordinate(r.get("shape_pt_lon"), 180)
			if err != nil {
				return err
			}
			sequence, err := integer(r.get("shape_pt_sequence"), math.MaxUint32)
			if err != nil {
				return err
			}
			distance, err := optionalDistance(r.get("shape_dist_traveled"))
			if err != nil {
				return err
			}
			id := r.get("shape_id")
			index, exists := shapeIndexes[id]
			if !exists {
				index = len(data.Shapes)
				shapeIndexes[id] = index
				data.Shapes = append(data.Shapes, schedule.Shape{ID: id})
			}
			data.Shapes[index].Points = append(data.Shapes[index].Points, schedule.ShapePoint{Sequence: uint32(sequence), Latitude: latitude, Longitude: longitude, ShapeDistance: distance})
			return nil
		}},
		{"feed_info.txt", []string{"feed_publisher_name", "feed_publisher_url", "feed_lang"}, func(r row) error {
			if data.FeedInfo.Publisher != "" {
				return ErrInvalid
			}
			if err := r.require("feed_publisher_name", "feed_publisher_url", "feed_lang"); err != nil {
				return err
			}
			if !validURL(r.get("feed_publisher_url")) {
				return ErrInvalid
			}
			f := schedule.FeedInfo{Publisher: r.get("feed_publisher_name"), URL: r.get("feed_publisher_url"), Language: r.get("feed_lang"), Version: r.get("feed_version")}
			var err error
			if r.get("feed_start_date") != "" {
				f.Start, err = schedule.ParseDate(r.get("feed_start_date"))
				if err != nil {
					return err
				}
			}
			if r.get("feed_end_date") != "" {
				f.End, err = schedule.ParseDate(r.get("feed_end_date"))
				if err != nil {
					return err
				}
			}
			if f.Start != "" && f.End != "" && f.Start > f.End {
				return ErrInvalid
			}
			data.FeedInfo = f
			return nil
		}},
	}
	var actual int64
	readEntry := func(f *zip.File, consume func(io.Reader) error) error {
		r, err := f.Open()
		if err != nil {
			return err
		}
		defer r.Close()
		allowed := min(limit.entry, limit.total-actual)
		bounded := &io.LimitedReader{R: r, N: allowed + 1}
		err = consume(bounded)
		read := allowed + 1 - bounded.N
		actual += read
		if read > allowed {
			return ErrLimit
		}
		return err
	}
	for _, table := range tables {
		f := files[table.name]
		if f == nil {
			continue
		}
		err := readEntry(f, func(r io.Reader) error { return readCSV(r, limit.record, table.required, table.parse) })
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, table.name, err)
		}
		delete(files, table.name)
	}
	// Consume even unused entries: their actual sizes and checksums must be valid.
	for _, f := range files {
		if err := readEntry(f, func(r io.Reader) error { _, err := io.Copy(io.Discard, r); return err }); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	d, err := schedule.NewDataset(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return d, nil
}

type row struct {
	header map[string]int
	values []string
}

func (r row) get(name string) string {
	if i, ok := r.header[name]; ok {
		return r.values[i]
	}
	return ""
}
func (r row) require(names ...string) error {
	for _, name := range names {
		if r.get(name) == "" {
			return fmt.Errorf("missing %s", name)
		}
	}
	return nil
}

// recordReader bounds raw logical records, including quoted multiline fields,
// before encoding/csv can allocate an arbitrarily large record. CSV still owns
// syntax validation; paired escaped quotes leave the quote state unchanged.
type recordReader struct {
	source      io.Reader
	size, limit int
	quoted      bool
}

func (r *recordReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	for i, b := range p[:n] {
		r.size++
		if r.size > r.limit {
			return i, ErrLimit
		}
		if b == '"' {
			r.quoted = !r.quoted
		}
		if b == '\n' && !r.quoted {
			r.size = 0
		}
	}
	return n, err
}

func readCSV(source io.Reader, limit int, required []string, consume func(row) error) error {
	bounded := bufio.NewReader(&recordReader{source: source, limit: limit})
	if prefix, _ := bounded.Peek(3); bytes.Equal(prefix, []byte("\ufeff")) {
		_, _ = bounded.Discard(3)
	}
	r := csv.NewReader(bounded)
	r.ReuseRecord = true
	headers, err := r.Read()
	if err != nil {
		return err
	}
	header := make(map[string]int, len(headers))
	for i, name := range headers {
		if i == 0 {
			name = strings.TrimPrefix(name, "\ufeff")
		}
		if _, exists := header[name]; exists || name == "" || !utf8.ValidString(name) {
			return ErrInvalid
		}
		header[name] = i
	}
	for _, name := range required {
		if _, exists := header[name]; !exists {
			return fmt.Errorf("missing column %s", name)
		}
	}
	for line := 2; ; line++ {
		values, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		for _, value := range values {
			if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return ErrInvalid
			}
		}
		if err := consume(row{header, values}); err != nil {
			return fmt.Errorf("record %d: %w", line, err)
		}
	}
}

func integer(value string, maximum uint64) (uint64, error) {
	if value == "" {
		return 0, ErrInvalid
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, ErrInvalid
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n > maximum {
		return 0, ErrInvalid
	}
	return n, nil
}
func optionalInteger(value string, fallback int, maximum uint64) (int, error) {
	if value == "" {
		return fallback, nil
	}
	n, err := integer(value, maximum)
	return int(n), err
}
func parseTime(value string) (schedule.OptionalTime, error) {
	if value == "" {
		return schedule.OptionalTime{}, nil
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return schedule.OptionalTime{}, ErrInvalid
	}
	h, e1 := integer(parts[0], math.MaxInt32/3600)
	m, e2 := integer(parts[1], 59)
	s, e3 := integer(parts[2], 59)
	seconds := h*3600 + m*60 + s
	if e1 != nil || e2 != nil || e3 != nil || seconds > math.MaxInt32 {
		return schedule.OptionalTime{}, ErrInvalid
	}
	return schedule.OptionalTime{Seconds: int32(seconds), Present: true}, nil
}
func coordinate(value string, bound float64) (float64, error) {
	f, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > bound {
		return 0, ErrInvalid
	}
	return f, nil
}
func optionalDistance(value string) (schedule.OptionalDistance, error) {
	if value == "" {
		return schedule.OptionalDistance{}, nil
	}
	distance, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(distance) || math.IsInf(distance, 0) || distance < 0 {
		return schedule.OptionalDistance{}, ErrInvalid
	}
	return schedule.OptionalDistance{Value: distance, Present: true}, nil
}
func validURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}
