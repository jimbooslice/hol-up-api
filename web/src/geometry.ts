export type PointGeometry = { type: 'Point'; coordinates: [number, number] };
export type LineGeometry = { type: 'LineString'; coordinates: [number, number][] };

export interface StationFeature {
  type: 'Feature';
  id: string;
  geometry: PointGeometry | null;
  properties: { name: string; reason: string | null };
}

export interface ShapeFeature {
  type: 'Feature';
  id: string;
  geometry: LineGeometry | null;
  properties: { usable: boolean; reason: string | null };
}

export interface StopOccurrence {
  stop_id: string;
  station_id: string;
  stop_sequence: number;
  distance_meters: number | null;
  association: string;
  reason: string | null;
}

export interface ServicePattern {
  service_pattern_id: string;
  route_id: string;
  shape_id: string | null;
  direction_id: 0 | 1 | null;
  stops: StopOccurrence[];
  reason: string | null;
}

export interface RouteGeometry {
  schedule_version: string | null;
  schedule_eligible: boolean;
  schedule_source: 'supplemented' | 'regular' | '';
  schedule_validated_at: string | null;
  schedule_age_seconds: number | null;
  route_id: string;
  short_name: string;
  long_name: string;
  stations: StationFeature[];
  shapes: ShapeFeature[];
  patterns: ServicePattern[];
}

export type RouteGeometryErrorKind =
  | 'schedule-unavailable'
  | 'route-not-found'
  | 'http-error'
  | 'invalid-response'
  | 'network'
  | 'timeout';

export class RouteGeometryError extends Error {
  readonly kind: RouteGeometryErrorKind;

  constructor(kind: RouteGeometryErrorKind) {
    super(kind);
    this.name = 'RouteGeometryError';
    this.kind = kind;
  }
}

const object = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);
const string = (value: unknown): value is string => typeof value === 'string';
const nullableString = (value: unknown): value is string | null => value === null || string(value);
const finiteNullableNumber = (value: unknown): value is number | null =>
  value === null || (typeof value === 'number' && Number.isFinite(value));
const pair = (value: unknown): value is [number, number] =>
  Array.isArray(value) && value.length === 2 && value.every((item) => typeof item === 'number' && Number.isFinite(item))
    && Math.abs(value[0]) <= 180 && Math.abs(value[1]) <= 90;

export function timestamp(value: unknown): value is string {
  if (!string(value)) return false;
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.exec(value);
  if (!match || !Number.isFinite(Date.parse(value))) return false;
  const [, year, month, day, hour, minute, second] = match.map(Number);
  return month >= 1 && month <= 12
    && day >= 1 && day <= new Date(Date.UTC(year, month, 0)).getUTCDate()
    && hour <= 23 && minute <= 59 && second <= 59;
}

function point(value: unknown): value is PointGeometry {
  return object(value) && value.type === 'Point' && pair(value.coordinates);
}

function line(value: unknown): value is LineGeometry {
  return object(value) && value.type === 'LineString' && Array.isArray(value.coordinates)
    && value.coordinates.length >= 2 && value.coordinates.every(pair);
}

function station(value: unknown): value is StationFeature {
  if (!object(value) || value.type !== 'Feature' || !string(value.id) || !object(value.properties)
    || !string(value.properties.name) || !nullableString(value.properties.reason)) return false;
  return point(value.geometry) || (value.geometry === null && string(value.properties.reason));
}

function shape(value: unknown): value is ShapeFeature {
  if (!object(value) || value.type !== 'Feature' || !string(value.id) || !object(value.properties)
    || typeof value.properties.usable !== 'boolean' || !nullableString(value.properties.reason)) return false;
  return line(value.geometry) || (value.geometry === null && string(value.properties.reason));
}

function stop(value: unknown): value is StopOccurrence {
  return object(value) && string(value.stop_id) && string(value.station_id)
    && Number.isInteger(value.stop_sequence) && (value.stop_sequence as number) >= 0
    && finiteNullableNumber(value.distance_meters) && string(value.association) && nullableString(value.reason);
}

function pattern(value: unknown, routeId: string): value is ServicePattern {
  return object(value) && string(value.service_pattern_id) && value.route_id === routeId
    && nullableString(value.shape_id) && (value.direction_id === 0 || value.direction_id === 1 || value.direction_id === null)
    && Array.isArray(value.stops) && value.stops.every(stop) && nullableString(value.reason);
}

export function isRouteGeometry(value: unknown, expectedRouteId: string): value is RouteGeometry {
  return object(value)
    && nullableString(value.schedule_version)
    && typeof value.schedule_eligible === 'boolean'
    && (value.schedule_source === 'supplemented' || value.schedule_source === 'regular' || value.schedule_source === '')
    && (value.schedule_validated_at === null || timestamp(value.schedule_validated_at))
    && finiteNullableNumber(value.schedule_age_seconds)
    && value.route_id === expectedRouteId
    && string(value.short_name) && string(value.long_name)
    && Array.isArray(value.stations) && value.stations.every(station)
    && Array.isArray(value.shapes) && value.shapes.every(shape)
    && Array.isArray(value.patterns) && value.patterns.every((item) => pattern(item, expectedRouteId));
}

function errorCode(value: unknown): unknown {
  return object(value) && object(value.error) ? value.error.code : undefined;
}

export async function loadRouteGeometry(routeId: string, signal: AbortSignal): Promise<RouteGeometry> {
  try {
    const response = await fetch(`/api/v1/routes/${encodeURIComponent(routeId)}/geometry`, { signal });
    let body: unknown;
    try {
      body = await response.json();
    } catch (error) {
      if (signal.aborted) throw error;
      throw new RouteGeometryError(response.ok ? 'invalid-response' : 'http-error');
    }
    signal.throwIfAborted();
    if (!response.ok) {
      const code = errorCode(body);
      if (response.status === 503 && code === 'schedule_unavailable') throw new RouteGeometryError('schedule-unavailable');
      if (response.status === 404 && code === 'route_not_found') throw new RouteGeometryError('route-not-found');
      throw new RouteGeometryError('http-error');
    }
    if (!isRouteGeometry(body, routeId)) throw new RouteGeometryError('invalid-response');
    return body;
  } catch (error) {
    if ((signal.aborted && signal.reason instanceof DOMException && signal.reason.name === 'TimeoutError')
      || (error instanceof DOMException && error.name === 'TimeoutError')) {
      throw new RouteGeometryError('timeout');
    }
    if (signal.aborted) throw error;
    if (error instanceof RouteGeometryError) throw error;
    throw new RouteGeometryError('network');
  }
}

export type GeometryState =
  | { status: 'loading' }
  | { status: 'ready'; geometry: RouteGeometry }
  | { status: 'failure'; kind: RouteGeometryErrorKind };

export type GeometryAction =
  | { type: 'load' }
  | { type: 'ready'; geometry: RouteGeometry }
  | { type: 'failure'; kind: RouteGeometryErrorKind };

export function geometryReducer(_state: GeometryState, action: GeometryAction): GeometryState {
  if (action.type === 'load') return { status: 'loading' };
  if (action.type === 'ready') return { status: 'ready', geometry: action.geometry };
  return { status: 'failure', kind: action.kind };
}

export async function updateRouteGeometry(
  routeId: string,
  signal: AbortSignal,
  dispatch: (action: GeometryAction) => void,
  isCurrent: () => boolean = () => true,
): Promise<void> {
  try {
    const geometry = await loadRouteGeometry(routeId, signal);
    if (isCurrent() && !signal.aborted) dispatch({ type: 'ready', geometry });
  } catch (error) {
    if (!isCurrent()) return;
    if (signal.aborted && !(signal.reason instanceof DOMException && signal.reason.name === 'TimeoutError')) return;
    dispatch({ type: 'failure', kind: error instanceof RouteGeometryError ? error.kind : 'network' });
  }
}

const stationCollator = new Intl.Collator('en', { numeric: true });

export function alphabeticalStations(stations: StationFeature[]): StationFeature[] {
  return [...stations].sort((a, b) => stationCollator.compare(a.properties.name, b.properties.name)
    || stationCollator.compare(a.id, b.id) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}

export function mapFeatures(geometry: RouteGeometry) {
  // promoteId preserves string feature IDs through MapLibre's vector tiles.
  return {
    shapes: {
      type: 'FeatureCollection' as const,
      features: geometry.shapes
        .filter((shape): shape is ShapeFeature & { geometry: LineGeometry } => shape.properties.usable && shape.geometry !== null)
        .map((shape) => ({ ...shape, properties: { ...shape.properties, feature_id: shape.id } })),
    },
    stations: {
      type: 'FeatureCollection' as const,
      features: geometry.stations
        .filter((station): station is StationFeature & { geometry: PointGeometry } => station.geometry !== null)
        .map((station) => ({ ...station, properties: { ...station.properties, feature_id: station.id } })),
    },
  };
}

export function geometryBounds(geometry: RouteGeometry): [[number, number], [number, number]] | null {
  const bounds: [[number, number], [number, number]] = [[Infinity, Infinity], [-Infinity, -Infinity]];
  let count = 0;
  const include = ([lng, lat]: [number, number]) => {
    bounds[0][0] = Math.min(bounds[0][0], lng); bounds[0][1] = Math.min(bounds[0][1], lat);
    bounds[1][0] = Math.max(bounds[1][0], lng); bounds[1][1] = Math.max(bounds[1][1], lat);
    count++;
  };
  for (const shape of geometry.shapes) if (shape.properties.usable && shape.geometry) shape.geometry.coordinates.forEach(include);
  if (!count) for (const station of geometry.stations) if (station.geometry) include(station.geometry.coordinates);
  return count ? bounds : null;
}
