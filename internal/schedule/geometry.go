package schedule

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
)

const (
	MaxStopShapeOffsetMeters = 100.0
	AnchorAmbiguityMeters    = 1.0
	earthRadiusMeters        = 6371008.8
)

// ShapeDistance retains GTFS units, which need not be meters.
type OptionalDistance struct {
	Value   float64
	Present bool
}

type ShapePoint struct {
	Sequence            uint32
	Latitude, Longitude float64
	ShapeDistance       OptionalDistance
	DistanceMeters      float64
}

type Shape struct {
	ID     string
	Points []ShapePoint
	Usable bool
	Reason string
}

type StopOccurrence struct {
	StopID, StationID string
	Sequence          uint32
	DistanceMeters    float64
	HasDistance       bool
	Association       string
	Reason            string
}

type Pattern struct {
	ID, RouteID, ShapeID string
	Direction            int
	Stops                []StopOccurrence
	Reason               string
}

func validCoordinates(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) && math.Abs(lat) <= 90 && math.Abs(lon) <= 180
}

func validOptionalDistance(d OptionalDistance) bool {
	return !math.IsNaN(d.Value) && !math.IsInf(d.Value, 0) && (!d.Present || d.Value >= 0)
}

func meters(lat1, lon1, lat2, lon2 float64) float64 {
	lat1, lat2 = lat1*math.Pi/180, lat2*math.Pi/180
	dlat, dlon := lat2-lat1, (lon2-lon1)*math.Pi/180
	a := math.Pow(math.Sin(dlat/2), 2) + math.Cos(lat1)*math.Cos(lat2)*math.Pow(math.Sin(dlon/2), 2)
	return 2 * earthRadiusMeters * math.Asin(math.Sqrt(min(1, a)))
}

// At follows the shape's cumulative geometric distance, never the straight line
// between station anchors. It does not extrapolate beyond the polyline.
func (s Shape) At(distance float64) (lat, lon float64, ok bool) {
	if !s.Usable || len(s.Points) < 2 || math.IsNaN(distance) || distance < 0 || distance > s.Points[len(s.Points)-1].DistanceMeters {
		return 0, 0, false
	}
	i := sort.Search(len(s.Points), func(i int) bool { return s.Points[i].DistanceMeters >= distance })
	if i == 0 || s.Points[i].DistanceMeters == distance {
		return s.Points[i].Latitude, s.Points[i].Longitude, true
	}
	a, b := s.Points[i-1], s.Points[i]
	fraction := (distance - a.DistanceMeters) / (b.DistanceMeters - a.DistanceMeters)
	lat, lon = interpolate(a, b, fraction)
	return lat, lon, true
}

func interpolate(a, b ShapePoint, fraction float64) (lat, lon float64) {
	// Longitude wraps across the antimeridian; ordinary subway segments are short.
	delta := math.Remainder(b.Longitude-a.Longitude, 360)
	return a.Latitude + fraction*(b.Latitude-a.Latitude), math.Remainder(a.Longitude+fraction*delta, 360)
}

func (d *Dataset) buildGeometry(input []Shape) error {
	d.shapes, d.patterns = map[string]Shape{}, map[string]Pattern{}
	d.tripPatterns, d.routePatterns = map[string]string{}, map[string][]string{}
	for _, shape := range input {
		if _, exists := d.shapes[shape.ID]; exists || shape.ID == "" {
			return errors.New("invalid or duplicate shape")
		}
		shape.Points = slices.Clone(shape.Points)
		sort.Slice(shape.Points, func(i, j int) bool { return shape.Points[i].Sequence < shape.Points[j].Sequence })
		lastSupplied := -1.0
		for i := range shape.Points {
			p := &shape.Points[i]
			if !validCoordinates(p.Latitude, p.Longitude) || !validOptionalDistance(p.ShapeDistance) || (i > 0 && p.Sequence == shape.Points[i-1].Sequence) {
				return errors.New("invalid shape point or duplicate sequence")
			}
			if p.ShapeDistance.Present {
				if p.ShapeDistance.Value < lastSupplied {
					return errors.New("decreasing shape distance")
				}
				lastSupplied = p.ShapeDistance.Value
			}
			p.DistanceMeters = 0
			if i > 0 {
				prev := shape.Points[i-1]
				p.DistanceMeters = prev.DistanceMeters + meters(prev.Latitude, prev.Longitude, p.Latitude, p.Longitude)
			}
		}
		shape.Usable = len(shape.Points) >= 2 && shape.Points[len(shape.Points)-1].DistanceMeters > 0
		shape.Reason = ""
		if !shape.Usable {
			shape.Reason = "unusable_shape"
		}
		d.shapes[shape.ID] = shape
	}
	// Timetable-identical paths share both association work and immutable results.
	cache := map[string]string{}
	for _, routeID := range d.RouteIDs() {
		for _, id := range d.byRoute[routeID] {
			trip := d.trips[id]
			keyTrip := Trip{RouteID: trip.RouteID, ShapeID: trip.ShapeID, Direction: trip.Direction, Stops: make([]StopTime, len(trip.Stops))}
			for i, stop := range trip.Stops {
				keyTrip.Stops[i] = StopTime{StopID: stop.StopID, Sequence: stop.Sequence, ShapeDistance: stop.ShapeDistance}
			}
			keyBytes, _ := json.Marshal(keyTrip)
			key := string(keyBytes)
			patternID, exists := cache[key]
			if !exists {
				p := d.associate(trip)
				encoded, _ := json.Marshal(p)
				hash := sha256.Sum256(encoded)
				p.ID = hex.EncodeToString(hash[:])
				patternID = p.ID
				d.patterns[p.ID] = p
				cache[key] = p.ID
			}
			d.tripPatterns[id] = patternID
			d.routePatterns[routeID] = append(d.routePatterns[routeID], patternID)
		}
		slices.Sort(d.routePatterns[routeID])
		d.routePatterns[routeID] = slices.Compact(d.routePatterns[routeID])
	}
	return nil
}

func (d *Dataset) stationID(stopID string) string {
	s := d.stops[stopID]
	for s.ParentID != "" {
		s = d.stops[s.ParentID]
	}
	return s.ID
}

func (d *Dataset) Shape(id string) (Shape, bool) {
	s, ok := d.shapes[id]
	s.Points = slices.Clone(s.Points)
	return s, ok
}

func (d *Dataset) TripPattern(id string) (Pattern, bool) {
	p, ok := d.patterns[d.tripPatterns[id]]
	p.Stops = slices.Clone(p.Stops)
	return p, ok
}

func (d *Dataset) RoutePatterns(routeID string) []Pattern {
	result := make([]Pattern, 0, len(d.routePatterns[routeID]))
	for _, id := range d.routePatterns[routeID] {
		p := d.patterns[id]
		p.Stops = slices.Clone(p.Stops)
		result = append(result, p)
	}
	return result
}

func (d *Dataset) RouteShapes(routeID string) []Shape {
	seen := map[string]bool{}
	result := []Shape{}
	for _, id := range d.routePatterns[routeID] {
		shapeID := d.patterns[id].ShapeID
		if !seen[shapeID] {
			if shape, ok := d.Shape(shapeID); ok {
				result = append(result, shape)
				seen[shapeID] = true
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (d *Dataset) RouteStations(routeID string) []Stop {
	seen := map[string]bool{}
	result := []Stop{}
	for _, id := range d.routePatterns[routeID] {
		for _, stop := range d.patterns[id].Stops {
			if !seen[stop.StationID] {
				result = append(result, d.stops[stop.StationID])
				seen[stop.StationID] = true
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

type anchorCandidate struct{ distance, offset float64 }

func (d *Dataset) associate(trip Trip) Pattern {
	p := Pattern{RouteID: trip.RouteID, ShapeID: trip.ShapeID, Direction: trip.Direction, Stops: make([]StopOccurrence, len(trip.Stops))}
	for i, stop := range trip.Stops {
		p.Stops[i] = StopOccurrence{StopID: stop.StopID, StationID: d.stationID(stop.StopID), Sequence: stop.Sequence}
	}
	unresolved := func(reason string) Pattern {
		p.Reason = reason
		for i := range p.Stops {
			p.Stops[i].Reason = reason
		}
		return p
	}
	shape, ok := d.shapes[trip.ShapeID]
	if !ok {
		return unresolved("missing_shape")
	}
	if !shape.Usable {
		return unresolved(shape.Reason)
	}
	candidates := make([][]anchorCandidate, len(trip.Stops))
	lastSupplied := -1.0
	for i, stopTime := range trip.Stops {
		stop := d.stops[stopTime.StopID]
		if stopTime.ShapeDistance.Present {
			if stopTime.ShapeDistance.Value < lastSupplied {
				return unresolved("contradictory_stop_distances")
			}
			lastSupplied = stopTime.ShapeDistance.Value
			if distance, ok := shape.suppliedMeters(stopTime.ShapeDistance.Value); ok {
				lat, lon, _ := shape.At(distance)
				if !stop.HasCoordinates || meters(stop.Latitude, stop.Longitude, lat, lon) <= MaxStopShapeOffsetMeters {
					candidates[i] = []anchorCandidate{{distance: distance}}
					p.Stops[i].Association = "shape_dist_traveled"
					continue
				}
			}
		}
		p.Stops[i].Association = "platform_projection"
		if !stop.HasCoordinates {
			p.Stops[i].Reason = "missing_stop_coordinates"
			continue
		}
		candidates[i] = shape.nearby(stop)
		if len(candidates[i]) == 0 {
			p.Stops[i].Reason = "stop_too_far_from_shape"
		}
	}
	// Missing anchors remain explicit, while known anchors on either side still
	// constrain occurrence order. A missing stop never supplies an interval anchor.
	var known [][]anchorCandidate
	var stops []StopTime
	var indices []int
	for i, choices := range candidates {
		if len(choices) == 0 {
			p.Reason = "unresolved_stop_anchors"
			continue
		}
		known, stops, indices = append(known, choices), append(stops, trip.Stops[i]), append(indices, i)
	}
	compatible := orderedCandidates(known, stops)
	if compatible {
		for i := range known {
			nearest := math.Inf(1)
			for _, candidate := range known[i] {
				nearest = min(nearest, candidate.offset)
			}
			known[i] = slices.DeleteFunc(known[i], func(c anchorCandidate) bool { return c.offset > nearest+AnchorAmbiguityMeters })
		}
		compatible = orderedCandidates(known, stops)
	}
	for i, choices := range known {
		anchor := &p.Stops[indices[i]]
		switch {
		case !compatible:
			anchor.Reason = "contradictory_stop_anchors"
		case len(choices) != 1:
			anchor.Reason = "ambiguous_stop_anchor"
		default:
			anchor.DistanceMeters, anchor.HasDistance = choices[0].distance, true
		}
		if anchor.Reason != "" {
			p.Reason = "unresolved_stop_anchors"
		}
	}
	return p
}

// Prune candidates that cannot participate in any ordered complete assignment.
// Each list is sorted by distance, so the earliest/latest feasible paths suffice.
func orderedCandidates(candidates [][]anchorCandidate, stops []StopTime) bool {
	for i := 1; i < len(candidates); i++ {
		minimum := candidates[i-1][0].distance
		strict := stops[i-1].StopID == stops[i].StopID
		candidates[i] = slices.DeleteFunc(candidates[i], func(c anchorCandidate) bool {
			return c.distance < minimum || (strict && c.distance <= minimum)
		})
		if len(candidates[i]) == 0 {
			return false
		}
	}
	for i := len(candidates) - 2; i >= 0; i-- {
		maximum := candidates[i+1][len(candidates[i+1])-1].distance
		strict := stops[i+1].StopID == stops[i].StopID
		candidates[i] = slices.DeleteFunc(candidates[i], func(c anchorCandidate) bool {
			return c.distance > maximum || (strict && c.distance >= maximum)
		})
		if len(candidates[i]) == 0 {
			return false
		}
	}
	return true
}

func (s Shape) suppliedMeters(value float64) (float64, bool) {
	var previous ShapePoint
	hasPrevious := false
	for _, point := range s.Points {
		if !point.ShapeDistance.Present {
			continue
		}
		if hasPrevious && point.ShapeDistance.Value == previous.ShapeDistance.Value && point.DistanceMeters != previous.DistanceMeters {
			return 0, false
		}
		if point.ShapeDistance.Value == value {
			// Equal supplied values on distinct geometry are ambiguous.
			for _, other := range s.Points {
				if other.ShapeDistance.Present && other.ShapeDistance.Value == value && other.DistanceMeters != point.DistanceMeters {
					return 0, false
				}
			}
			return point.DistanceMeters, true
		}
		if hasPrevious && value > previous.ShapeDistance.Value && value < point.ShapeDistance.Value {
			fraction := (value - previous.ShapeDistance.Value) / (point.ShapeDistance.Value - previous.ShapeDistance.Value)
			return previous.DistanceMeters + fraction*(point.DistanceMeters-previous.DistanceMeters), true
		}
		previous, hasPrevious = point, true
	}
	return 0, false
}

// ponytail: scan segments once per distinct pattern; add a spatial index if archive rebuild profiling warrants it.
func (s Shape) nearby(stop Stop) []anchorCandidate {
	result := []anchorCandidate{}
	for i := 1; i < len(s.Points); i++ {
		a, b := s.Points[i-1], s.Points[i]
		if b.DistanceMeters == a.DistanceMeters {
			continue
		}
		// Local equirectangular projection selects the segment fraction; the
		// final offset and accumulated path length use great-circle meters.
		cos := math.Cos(stop.Latitude * math.Pi / 180)
		ax, ay := math.Remainder(a.Longitude-stop.Longitude, 360)*cos, a.Latitude-stop.Latitude
		bx, by := math.Remainder(b.Longitude-stop.Longitude, 360)*cos, b.Latitude-stop.Latitude
		dx, dy := bx-ax, by-ay
		if dx*dx+dy*dy == 0 {
			continue
		}
		fraction := max(0, min(1, -(ax*dx+ay*dy)/(dx*dx+dy*dy)))
		lat, lon := interpolate(a, b, fraction)
		offset := meters(stop.Latitude, stop.Longitude, lat, lon)
		if offset > MaxStopShapeOffsetMeters {
			continue
		}
		candidate := anchorCandidate{distance: a.DistanceMeters + fraction*(b.DistanceMeters-a.DistanceMeters), offset: offset}
		// Shared segment endpoints describe one path location, not two choices.
		if len(result) > 0 && math.Abs(result[len(result)-1].distance-candidate.distance) <= 1e-6 {
			if candidate.offset < result[len(result)-1].offset {
				result[len(result)-1] = candidate
			}
		} else {
			result = append(result, candidate)
		}
	}
	return result
}
