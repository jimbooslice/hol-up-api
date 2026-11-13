import { timestamp } from './geometry.ts';
import type { RouteGeometry } from './geometry.ts';

export type Freshness = 'fresh' | 'stale' | 'unavailable';
export interface Train {
  route_id: string;
  direction: 'northbound' | 'eastbound' | 'southbound' | 'westbound' | 'unknown';
  direction_id: 0 | 1 | null;
  trip_instance_id: string | null;
  source_record_id: string | null;
  source_name: string;
  entity_id: string;
  source_index: number;
  schedule_version: string | null;
  shape_id: string | null;
  service_pattern_id: string | null;
  latitude: number | null;
  longitude: number | null;
  shape_distance_meters: number | null;
  next_station_id: string | null;
  next_stop_sequence: number | null;
  next_arrival_at: string | null;
  projected_at: string | null;
  predicted_departure_at?: string | null;
  position_basis: 'realtime_prediction' | 'schedule_fallback';
  movement_state: 'inferred_moving' | 'reported_stopped' | 'unknown';
  source_timestamp: string | null;
  source_age_seconds: number | null;
  freshness: Freshness;
  observation_time: string | null;
  observation_age_seconds: number | null;
  observation_basis: string;
  reconciliation_outcome: 'matched' | 'unmatched' | 'invalid' | 'ambiguous' | 'unavailable';
  reconciliation_reason: string;
  reconciliation_warnings?: string[];
  fallback_reason: string | null;
  no_position_reason: string | null;
}
export interface Positions {
  schedule_version: string | null;
  schedule_eligible: boolean;
  schedule_source: RouteGeometry['schedule_source'];
  schedule_validated_at: string | null;
  schedule_age_seconds: number | null;
  as_of: string | null;
  projected_at: string | null;
  bundle_evaluated_at: string | null;
  freshness: Freshness;
  coverage: 'complete' | 'partial' | 'unavailable';
  sources: { name: string; routes: string[]; source_timestamp: string | null; freshness: Freshness }[];
  trains: Train[];
}
export type Snapshot = { data: Positions; startedAt: number };
export type PositionsErrorKind = 'source-unavailable' | 'schedule-unavailable' | 'timeout' | 'network' | 'http-error' | 'invalid-response';
export class PositionsError extends Error {
  readonly kind: PositionsErrorKind;
  constructor(kind: PositionsErrorKind) { super(kind); this.kind = kind; }
}
const object = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const string = (v: unknown): v is string => typeof v === 'string';
const nullableString = (v: unknown) => v === null || string(v);
const number = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v);
const nullableNumber = (v: unknown) => v === null || number(v);
const time = (v: unknown) => v === null || timestamp(v);
const oneOf = (v: unknown, values: readonly unknown[]) => values.includes(v);
const freshness = (v: unknown) => oneOf(v, ['fresh', 'stale', 'unavailable']);

export function trainIdentity(train: Train): string {
  if (train.trip_instance_id !== null) return `run:${JSON.stringify(train.trip_instance_id)}`;
  if (train.source_record_id !== null) return `record:${JSON.stringify(train.source_record_id)}`;
  return `source:${JSON.stringify([train.source_name, train.entity_id, train.source_index])}`;
}
function isTrain(v: unknown, route: string, version: unknown): v is Train {
  if (!object(v) || v.route_id !== route || v.schedule_version !== version
    || !oneOf(v.direction, ['northbound', 'eastbound', 'southbound', 'westbound', 'unknown'])
    || !oneOf(v.direction_id, [0, 1, null])
    || !['trip_instance_id', 'source_record_id', 'schedule_version', 'shape_id', 'service_pattern_id', 'next_station_id', 'fallback_reason', 'no_position_reason'].every(k => nullableString(v[k]))
    || !['source_name', 'entity_id', 'observation_basis', 'reconciliation_reason'].every(k => string(v[k]))
    || ('reconciliation_warnings' in v && (!Array.isArray(v.reconciliation_warnings) || !v.reconciliation_warnings.every(string)))
    || !Number.isSafeInteger(v.source_index)
    || !['projected_at', 'source_timestamp', 'observation_time', 'next_arrival_at'].every(k => time(v[k]))
    || ('predicted_departure_at' in v && !time(v.predicted_departure_at))
    || !['source_age_seconds', 'observation_age_seconds'].every(k => nullableNumber(v[k]))
    || !(v.shape_distance_meters === null || (number(v.shape_distance_meters) && v.shape_distance_meters >= 0))
    || !(v.next_stop_sequence === null || (Number.isSafeInteger(v.next_stop_sequence) && (v.next_stop_sequence as number) >= 0))
    || !freshness(v.freshness)
    || !oneOf(v.position_basis, ['realtime_prediction', 'schedule_fallback'])
    || !oneOf(v.movement_state, ['inferred_moving', 'reported_stopped', 'unknown'])
    || !oneOf(v.reconciliation_outcome, ['matched', 'unmatched', 'invalid', 'ambiguous', 'unavailable'])) return false;
  const positioned = number(v.latitude) && Math.abs(v.latitude) <= 90 && number(v.longitude) && Math.abs(v.longitude) <= 180
    && number(v.shape_distance_meters) && v.shape_distance_meters >= 0 && string(v.shape_id) && string(v.service_pattern_id);
  const absent = v.latitude === null && v.longitude === null && v.shape_distance_meters === null
    && string(v.no_position_reason) && v.no_position_reason.length > 0;
  return (positioned || absent) && ((v.next_station_id === null && v.next_stop_sequence === null && v.next_arrival_at === null)
    || (string(v.next_station_id) && Number.isSafeInteger(v.next_stop_sequence)));
}
export function isPositions(v: unknown, route = 'A'): v is Positions {
  if (!object(v) || !nullableString(v.schedule_version) || typeof v.schedule_eligible !== 'boolean'
    || !oneOf(v.schedule_source, ['supplemented', 'regular', '']) || !nullableNumber(v.schedule_age_seconds)
    || !['schedule_validated_at', 'as_of', 'projected_at', 'bundle_evaluated_at'].every(k => time(v[k]))
    || !freshness(v.freshness) || !oneOf(v.coverage, ['complete', 'partial', 'unavailable'])
    || !Array.isArray(v.sources) || !v.sources.every(s => object(s)
      && oneOf(s.name, ['ace', 'bdfm', 'g', 'jz', 'l', 'nqrw', 'numbered', 'si'])
      && Array.isArray(s.routes) && s.routes.every(string) && time(s.source_timestamp) && freshness(s.freshness))
    || !Array.isArray(v.trains) || !v.trains.every(t => isTrain(t, route, v.schedule_version))) return false;
  const ids = v.trains.map(trainIdentity);
  return new Set(ids).size === ids.length;
}
export async function loadPositions(signal: AbortSignal): Promise<Positions> {
  try {
    const response = await fetch('/api/v1/trains?route_id=A&view=positions', { signal });
    let body: unknown;
    try { body = await response.json(); }
    catch (error) {
      if (signal.aborted) throw error;
      throw new PositionsError(response.ok ? 'invalid-response' : 'http-error');
    }
    signal.throwIfAborted();
    if (!response.ok) {
      const code = object(body) && object(body.error) ? body.error.code : null;
      if (response.status === 503 && code === 'source_unavailable') throw new PositionsError('source-unavailable');
      if (response.status === 503 && code === 'schedule_unavailable') throw new PositionsError('schedule-unavailable');
      throw new PositionsError('http-error');
    }
    if (!isPositions(body)) throw new PositionsError('invalid-response');
    return body;
  } catch (error) {
    if (signal.aborted) {
      if (signal.reason instanceof DOMException && signal.reason.name === 'TimeoutError') throw new PositionsError('timeout');
      throw error;
    }
    if (error instanceof PositionsError) throw error;
    throw new PositionsError('network');
  }
}
export function compatible(geometry: RouteGeometry | null, positions: Positions | null): boolean {
  return !!geometry && !!positions && geometry.schedule_version !== null
    && geometry.schedule_version === positions.schedule_version;
}
export function needsGeometry(geometry: RouteGeometry | null, positions: Positions): boolean {
  return positions.schedule_version !== null && !compatible(geometry, positions);
}
const routeStyles: Record<string, { color: string; text: string }> = {
  A: { color: '#0039A6', text: '#FFFFFF' }, C: { color: '#0039A6', text: '#FFFFFF' }, E: { color: '#0039A6', text: '#FFFFFF' },
  B: { color: '#FF6319', text: '#111827' }, D: { color: '#FF6319', text: '#111827' }, F: { color: '#FF6319', text: '#111827' }, M: { color: '#FF6319', text: '#111827' },
  G: { color: '#6CBE45', text: '#111827' }, J: { color: '#996633', text: '#FFFFFF' }, Z: { color: '#996633', text: '#FFFFFF' },
  L: { color: '#A7A9AC', text: '#111827' }, N: { color: '#FCCC0A', text: '#111827' }, Q: { color: '#FCCC0A', text: '#111827' },
  R: { color: '#FCCC0A', text: '#111827' }, W: { color: '#FCCC0A', text: '#111827' },
  '1': { color: '#EE352E', text: '#FFFFFF' }, '2': { color: '#EE352E', text: '#FFFFFF' }, '3': { color: '#EE352E', text: '#FFFFFF' },
  '4': { color: '#00933C', text: '#FFFFFF' }, '5': { color: '#00933C', text: '#FFFFFF' }, '6': { color: '#00933C', text: '#FFFFFF' },
  '7': { color: '#B933AD', text: '#FFFFFF' }, S: { color: '#808183', text: '#FFFFFF' },
};
const neutralRouteStyle = { color: '#334155', text: '#FFFFFF' };
export const routeStyle = (routeID: string) => routeStyles[routeID] ?? neutralRouteStyle;

const emptyLines = (): GeoJSON.FeatureCollection<GeoJSON.LineString> => ({ type: 'FeatureCollection', features: [] });
const emptyStops = (): GeoJSON.FeatureCollection<GeoJSON.Point> => ({ type: 'FeatureCollection', features: [] });
export interface JourneyView {
  lines: GeoJSON.FeatureCollection<GeoJSON.LineString>;
  nextLeg: GeoJSON.FeatureCollection<GeoJSON.LineString>;
  stops: GeoJSON.FeatureCollection<GeoJSON.Point>;
  destination: string | null;
  nextStation: string | null;
}

export function directionText(direction: Train['direction'], destination: string | null): string {
  const cardinal = direction === 'unknown' ? '' : direction[0]!.toUpperCase() + direction.slice(1);
  return destination ? `${cardinal ? `${cardinal} to` : 'To'} ${destination}` : cardinal || 'Unavailable';
}

const meters = (a: [number, number], b: [number, number]) => {
  const radians = (value: number) => value * Math.PI / 180;
  const lat1 = radians(a[1]), lat2 = radians(b[1]);
  const dlat = lat2 - lat1, dlon = radians(b[0] - a[0]);
  const value = Math.sin(dlat / 2) ** 2 + Math.cos(lat1) * Math.cos(lat2) * Math.sin(dlon / 2) ** 2;
  return 2 * 6371008.8 * Math.asin(Math.sqrt(Math.min(1, value)));
};

function splitLine(coordinates: [number, number][], requested: number) {
  const total = coordinates.slice(1).reduce((sum, point, index) => sum + meters(coordinates[index]!, point), 0);
  const distance = Math.max(0, Math.min(total, requested));
  let traversed = 0;
  for (let i = 1; i < coordinates.length; i++) {
    const a = coordinates[i - 1]!, b = coordinates[i]!, length = meters(a, b);
    if (traversed + length < distance) { traversed += length; continue; }
    const fraction = length === 0 ? 0 : (distance - traversed) / length;
    const delta = ((b[0] - a[0] + 540) % 360) - 180;
    const split: [number, number] = [((a[0] + fraction * delta + 540) % 360) - 180, a[1] + fraction * (b[1] - a[1])];
    const past = [...coordinates.slice(0, i), split];
    const upcoming = [split, ...coordinates.slice(i)];
    return { past: past.length > 1 && distance > 0 ? past : null, upcoming: upcoming.length > 1 && distance < total ? upcoming : null, total };
  }
  return { past: coordinates, upcoming: null, total };
}

export function journeyView(geometry: RouteGeometry, train: Train): JourneyView {
  const pattern = geometry.patterns.find(item => item.service_pattern_id === train.service_pattern_id);
  const shape = geometry.shapes.find(item => item.id === train.shape_id && item.geometry !== null && item.properties.usable);
  const stations = new Map(geometry.stations.map(station => [station.id, station]));
  const destination = pattern?.stops.at(-1) ? stations.get(pattern.stops.at(-1)!.station_id)?.properties.name ?? null : null;
  const nextStation = train.next_station_id ? stations.get(train.next_station_id)?.properties.name ?? null : null;
  if (!pattern || pattern.shape_id !== train.shape_id || !shape?.geometry || train.shape_distance_meters === null) {
    return { lines: emptyLines(), nextLeg: emptyLines(), stops: emptyStops(), destination, nextStation };
  }
  const style = routeStyle(train.route_id);
  const split = splitLine(shape.geometry.coordinates, train.shape_distance_meters);
  if (train.shape_distance_meters > split.total) return { lines: emptyLines(), nextLeg: emptyLines(), stops: emptyStops(), destination, nextStation };
  const terminalDistance = pattern.stops.at(-1)?.distance_meters ?? null;
  const usableTerminal = terminalDistance !== null && terminalDistance >= 0 && terminalDistance <= split.total;
  if (usableTerminal && train.shape_distance_meters > terminalDistance) {
    return { lines: emptyLines(), nextLeg: emptyLines(), stops: emptyStops(), destination, nextStation };
  }
  const lines: JourneyView['lines'] = { type: 'FeatureCollection', features: [] };
  if (split.past) lines.features.push({ type: 'Feature', properties: { phase: 'visited', color: style.color }, geometry: { type: 'LineString', coordinates: split.past } });
  if (split.upcoming && usableTerminal && terminalDistance > train.shape_distance_meters) {
    const coordinates = splitLine(split.upcoming, terminalDistance - train.shape_distance_meters).past;
    if (coordinates) lines.features.push({ type: 'Feature', properties: { phase: 'upcoming', color: style.color }, geometry: { type: 'LineString', coordinates } });
  }
  const nextLeg = emptyLines();
  const nextStop = pattern.stops.find(stop => stop.station_id === train.next_station_id && stop.stop_sequence === train.next_stop_sequence);
  if (split.upcoming && nextStop?.distance_meters !== null && nextStop?.distance_meters !== undefined
    && nextStop.distance_meters > train.shape_distance_meters && nextStop.distance_meters <= split.total
    && (!usableTerminal || nextStop.distance_meters <= terminalDistance)) {
    const coordinates = splitLine(split.upcoming, nextStop.distance_meters - train.shape_distance_meters).past;
    if (coordinates) nextLeg.features.push({ type: 'Feature', properties: { color: style.color }, geometry: { type: 'LineString', coordinates } });
  }
  const stops: JourneyView['stops'] = { type: 'FeatureCollection', features: pattern.stops.flatMap(stop => {
    const station = stations.get(stop.station_id);
    if (!station?.geometry || stop.distance_meters === null || stop.distance_meters < 0 || stop.distance_meters > split.total
      || (usableTerminal && stop.distance_meters > terminalDistance)) return [];
    return [{ type: 'Feature' as const, properties: { phase: stop.distance_meters <= train.shape_distance_meters! ? 'visited' : 'upcoming',
      next: stop.station_id === train.next_station_id && stop.stop_sequence === train.next_stop_sequence, color: style.color }, geometry: station.geometry }];
  }) };
  return { lines, nextLeg, stops, destination, nextStation };
}
export function advancedAge(age: number | null, projected: string | null, source: string | null, startedAt: number, now: number): number | null {
  const base = age ?? (projected !== null && source !== null ? (Date.parse(projected) - Date.parse(source)) / 1000 : null);
  return base === null ? null : base + Math.max(0, now - startedAt) / 1000;
}
export const isStale = (freshness: Freshness, age: number | null) => freshness === 'stale' || (age !== null && age > 60);

const reasonDescriptions: Record<string, string> = {
  not_started: 'This trip has a future predicted departure from its first stop.',
  insufficient_timing: 'No trustworthy arrival/departure interval brackets the current time. Locating this train would require extrapolation.',
  start_time_conflict: 'The realtime trip start time disagrees with the origin time encoded by or scheduled for the candidate trip, so the run match is rejected.',
  stop_id_sequence_conflict: 'A realtime stop ID and stop sequence point to different scheduled stops, so that stop evidence is rejected.',
  unique_identity: 'The realtime record matches exactly one scheduled run.',
};

export function reasonText(reason: string): string {
  return reasonDescriptions[reason] ?? `The API reported ${reason.replaceAll('_', ' ')}.`;
}

export function warningText(warning: string): string {
  switch (warning) {
    case 'vehicle_stop_sequence_conflict_ignored': return 'The vehicle stop ID and sequence disagree. That vehicle observation was excluded; positioning depends on trip predictions.';
    case 'start_time_conflict': return 'The exact trip ID or unique origin/path suffix was preferred over the conflicting realtime start time.';
    case 'stop_id_sequence_conflict': return 'The stop ID was preferred over the conflicting stop sequence because it identifies one scheduled stop and preserves the original update order.';
    default: return `The API reported a reconciliation warning: ${warning}.`;
  }
}
