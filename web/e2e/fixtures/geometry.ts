import type { RouteGeometry, StationFeature, ShapeFeature } from '../../src/geometry';
const station = (id: string, name: string, coordinates: [number, number] | null, reason: string | null = null): StationFeature => ({
  type: 'Feature', id, geometry: coordinates ? { type: 'Point', coordinates } : null, properties: { name, reason },
});
const shape = (id: string, coordinates: [number, number][], usable = true): ShapeFeature => ({
  type: 'Feature', id, geometry: { type: 'LineString', coordinates }, properties: { usable, reason: usable ? null : 'unusable_shape' },
});
export const geometry: RouteGeometry = {
  route_id: 'A', short_name: 'A', long_name: '8 Avenue Express', schedule_version: 'fixture:branching',
  schedule_eligible: true, schedule_source: 'supplemented', schedule_validated_at: '2026-09-14T12:00:00Z', schedule_age_seconds: 0,
  stations: [
    station('S10', '10 St', [-73.95, 40.8]), station('S2', '2 St', [-73.98, 40.73]),
    station('S3', '2 St', [-73.91, 40.7]), station('S4', 'Alpha', [-74.01, 40.68]),
    station('S5', 'Unlocated', null, 'missing_station_coordinates'),
    station('S6', 'Unknown', null, '<script>unexpected()</script>'),
  ],
  shapes: [
    shape('trunk', [[-73.95, 40.8], [-73.97, 40.76], [-73.98, 40.73]]),
    shape('west', [[-73.98, 40.73], [-74, 40.71], [-74.01, 40.68]]),
    shape('east', [[-73.98, 40.73], [-73.94, 40.71], [-73.91, 40.7]]),
    shape('unusable', [[-74.3, 40.9], [-74.2, 40.85]], false),
    { type: 'Feature', id: 'missing', geometry: null, properties: { usable: false, reason: 'insufficient_points' } },
  ],
  patterns: [{ service_pattern_id: 'pattern-west', route_id: 'A', shape_id: 'west', direction_id: 1, reason: null, stops: [
    { stop_id: 'S2S', station_id: 'S2', stop_sequence: 1, distance_meters: 0, association: 'shape_dist_traveled', reason: null },
    { stop_id: 'S4S', station_id: 'S4', stop_sequence: 2, distance_meters: 5000, association: 'shape_dist_traveled', reason: null },
  ] }],
};
export const basemap = {
  version: 8, sources: {},
  layers: [
    { id: 'background', type: 'background', paint: { 'background-color': '#edf1f5' } },
    { id: 'building-test', type: 'background', paint: { 'background-color': '#ff0000' } },
  ],
};
