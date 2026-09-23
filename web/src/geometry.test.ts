import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';
import {
  alphabeticalStations,
  geometryBounds,
  mapFeatures,
  geometryReducer,
  isRouteGeometry,
  loadRouteGeometry,
  RouteGeometryError,
  updateRouteGeometry,
} from './geometry.ts';
import type { GeometryAction, RouteGeometry } from './geometry.ts';

const valid: RouteGeometry = {
  schedule_version: 'supplemented:2:abc',
  schedule_eligible: true,
  schedule_source: 'supplemented',
  schedule_validated_at: '2026-09-13T04:09:00Z',
  schedule_age_seconds: 60,
  route_id: 'A',
  short_name: 'A',
  long_name: '8 Avenue Express',
  stations: [{
    type: 'Feature',
    id: 'A02',
    geometry: { type: 'Point', coordinates: [-73.9, 40.8] },
    properties: { name: 'Station', reason: null },
  }],
  shapes: [{
    type: 'Feature',
    id: 'shape',
    geometry: { type: 'LineString', coordinates: [[-73.9, 40.8], [-74, 40.7]] },
    properties: { usable: true, reason: null },
  }],
  patterns: [{
    service_pattern_id: 'pattern',
    route_id: 'A',
    shape_id: 'shape',
    direction_id: 1,
    stops: [{
      stop_id: 'A02S',
      station_id: 'A02',
      stop_sequence: 1,
      distance_meters: 0,
      association: 'shape_dist_traveled',
      reason: null,
    }],
    reason: null,
  }],
};

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

const clone = (): RouteGeometry => structuredClone(valid);
const response = (body: unknown, status = 200) => new Response(
  typeof body === 'string' ? body : JSON.stringify(body),
  { status, headers: { 'content-type': 'application/json' } },
);

test('validates complete and explicitly unavailable geometry responses', () => {
  assert.equal(isRouteGeometry(valid, 'A'), true);

  const unavailable = clone();
  unavailable.schedule_version = null;
  unavailable.schedule_eligible = false;
  unavailable.schedule_source = '';
  unavailable.schedule_validated_at = null;
  unavailable.schedule_age_seconds = null;
  unavailable.stations[0]!.geometry = null;
  unavailable.stations[0]!.properties.reason = 'missing_station_coordinates';
  unavailable.shapes[0]!.geometry = null;
  unavailable.shapes[0]!.properties.usable = false;
  unavailable.shapes[0]!.properties.reason = 'insufficient_points';
  unavailable.patterns[0]!.shape_id = null;
  unavailable.patterns[0]!.direction_id = null;
  unavailable.patterns[0]!.stops[0]!.distance_meters = null;
  unavailable.patterns[0]!.stops[0]!.reason = 'no_shape';
  (unavailable as unknown as Record<string, unknown>).extra = 'ignored';
  assert.equal(isRouteGeometry(unavailable, 'A'), true);

  const empty = { ...clone(), stations: [], shapes: [], patterns: [] };
  assert.equal(isRouteGeometry(empty, 'A'), true);
});

test('rejects malformed consumed geometry fields', async (t) => {
  const cases: [string, (value: Record<string, unknown>) => void][] = [
    ['wrong route', (value) => { value.route_id = 'C'; }],
    ['missing arrays', (value) => { delete value.stations; }],
    ['invalid source', (value) => { value.schedule_source = 'cached'; }],
    ['invalid timestamp', (value) => { value.schedule_validated_at = '2026-02-30T00:00:00Z'; }],
    ['malformed point', (value) => { (value.stations as RouteGeometry['stations'])[0]!.geometry = { type: 'Point', coordinates: [Infinity, 40] }; }],
    ['short line', (value) => { (value.shapes as RouteGeometry['shapes'])[0]!.geometry = { type: 'LineString', coordinates: [[-74, 40]] }; }],
    ['invalid direction', (value) => { (value.patterns as unknown as { direction_id: number }[])[0]!.direction_id = 2; }],
    ['invalid stop sequence', (value) => { (value.patterns as RouteGeometry['patterns'])[0]!.stops[0]!.stop_sequence = -1; }],
    ['wrong pattern route', (value) => { (value.patterns as RouteGeometry['patterns'])[0]!.route_id = 'C'; }],
    ['missing null reason', (value) => {
      const station = (value.stations as RouteGeometry['stations'])[0]!;
      station.geometry = null;
      station.properties.reason = null;
    }],
  ];

  for (const [name, mutate] of cases) await t.test(name, () => {
    const value = clone() as unknown as Record<string, unknown>;
    mutate(value);
    assert.equal(isRouteGeometry(value, 'A'), false);
  });
});

test('loads and validates a successful response', async () => {
  globalThis.fetch = (async (url) => {
    assert.equal(url, '/api/v1/routes/A/geometry');
    return response(valid);
  }) as typeof fetch;
  assert.deepEqual(await loadRouteGeometry('A', new AbortController().signal), valid);
});

test('maps HTTP, response, network, and timeout failures to stable kinds', async (t) => {
  const cases: [string, () => Promise<Response>, string][] = [
    ['schedule unavailable', async () => response({ error: { code: 'schedule_unavailable', message: 'do not show' } }, 503), 'schedule-unavailable'],
    ['route missing', async () => response({ error: { code: 'route_not_found', message: 'do not show' } }, 404), 'route-not-found'],
    ['other HTTP error', async () => response({ error: { code: 'other', message: 'do not show' } }, 500), 'http-error'],
    ['invalid successful response', async () => response({ route_id: 'A' }), 'invalid-response'],
    ['malformed success JSON', async () => response('{', 200), 'invalid-response'],
  ];

  for (const [name, fetcher, kind] of cases) await t.test(name, async () => {
    globalThis.fetch = fetcher as typeof fetch;
    await assert.rejects(
      loadRouteGeometry('A', new AbortController().signal),
      (error: unknown) => error instanceof RouteGeometryError && error.kind === kind,
    );
  });

  await t.test('network failure', async () => {
    globalThis.fetch = (async () => { throw new TypeError('offline'); }) as typeof fetch;
    await assert.rejects(
      loadRouteGeometry('A', new AbortController().signal),
      (error: unknown) => error instanceof RouteGeometryError && error.kind === 'network',
    );
  });

  await t.test('timeout', async () => {
    const timeout = AbortSignal.abort(new DOMException('timed out', 'TimeoutError'));
    globalThis.fetch = (async () => { throw timeout.reason; }) as typeof fetch;
    await assert.rejects(
      loadRouteGeometry('A', timeout),
      (error: unknown) => error instanceof RouteGeometryError && error.kind === 'timeout',
    );
  });
});

test('retry enters loading then replaces failure with ready', () => {
  let state = geometryReducer({ status: 'loading' }, { type: 'failure', kind: 'network' });
  state = geometryReducer(state, { type: 'load' });
  assert.deepEqual(state, { status: 'loading' });
  state = geometryReducer(state, { type: 'ready', geometry: valid });
  assert.deepEqual(state, { status: 'ready', geometry: valid });
});

test('an aborted effect cannot publish a stale response', async () => {
  let resolve!: (value: Response) => void;
  globalThis.fetch = (() => new Promise<Response>((done) => { resolve = done; })) as typeof fetch;
  const controller = new AbortController();
  const actions: GeometryAction[] = [];
  const pending = updateRouteGeometry('A', controller.signal, (action) => actions.push(action));
  controller.abort();
  resolve(response(valid));
  await pending;
  assert.deepEqual(actions, []);
});

test('rejects out-of-range coordinates in both points and lines, accepts boundaries', () => {
  for (const coordinates of [[181, 40], [-181, 40], [-74, 91], [-74, -91]] as [number, number][]) {
    const value = clone();
    value.stations[0]!.geometry!.coordinates = coordinates;
    assert.equal(isRouteGeometry(value, 'A'), false);
    value.stations = [];
    value.shapes[0]!.geometry!.coordinates[0] = coordinates;
    assert.equal(isRouteGeometry(value, 'A'), false);
  }
  const value = clone();
  value.shapes[0]!.geometry!.coordinates = [[-180, -90], [180, 90]];
  assert.equal(isRouteGeometry(value, 'A'), true);
});

test('body-read abort retains timeout and cancellation classifications', async () => {
  for (const name of ['TimeoutError', 'AbortError']) {
    const controller = new AbortController();
    globalThis.fetch = (async () => ({ ok: true, json: async () => {
      controller.abort(new DOMException('aborted', name));
      throw controller.signal.reason;
    } })) as unknown as typeof fetch;
    await assert.rejects(loadRouteGeometry('A', controller.signal), (error: unknown) => name === 'TimeoutError'
      ? error instanceof RouteGeometryError && error.kind === 'timeout'
      : error === controller.signal.reason);
  }
});

test('cleanup suppresses late body success or failure even after the timeout wins', async () => {
  for (const success of [true, false]) {
    let finish!: () => void;
    let current = true;
    const controller = new AbortController();
    globalThis.fetch = (async () => ({ ok: true, json: () => new Promise((resolve, reject) => {
      finish = () => success ? resolve(valid) : reject(controller.signal.reason);
    }) })) as unknown as typeof fetch;
    const actions: GeometryAction[] = [];
    const pending = updateRouteGeometry('A', controller.signal, (action) => actions.push(action), () => current);
    await Promise.resolve();
    controller.abort(new DOMException('deadline', 'TimeoutError'));
    current = false;
    finish();
    await pending;
    assert.deepEqual(actions, []);
  }
});

test('body-read timeout publishes a timeout failure while current', async () => {
  const controller = new AbortController();
  globalThis.fetch = (async () => ({ ok: true, json: async () => {
    controller.abort(new DOMException('deadline', 'TimeoutError'));
    throw controller.signal.reason;
  } })) as unknown as typeof fetch;
  const actions: GeometryAction[] = [];
  await updateRouteGeometry('A', controller.signal, (action) => actions.push(action));
  assert.deepEqual(actions, [{ type: 'failure', kind: 'timeout' }]);
});

test('feature collections preserve IDs and branch order, filter only unusable/null geometry', () => {
  const value = clone();
  value.shapes.push({ ...structuredClone(value.shapes[0]!), id: 'branch' },
    { ...structuredClone(value.shapes[0]!), id: 'unusable', properties: { usable: false, reason: 'bad' } },
    { type: 'Feature', id: 'missing', geometry: null, properties: { usable: true, reason: 'missing' } });
  value.stations.push({ type: 'Feature', id: 'unknown', geometry: null, properties: { name: 'Unknown', reason: 'missing' } });
  const features = mapFeatures(value);
  assert.equal(features.shapes.type, 'FeatureCollection');
  assert.deepEqual(features.shapes.features, value.shapes.slice(0, 2).map((shape) => ({ ...shape, properties: { ...shape.properties, feature_id: shape.id } })));
  assert.deepEqual(features.stations.features, value.stations.slice(0, 1).map((station) => ({ ...station, properties: { ...station.properties, feature_id: station.id } })));
  assert.deepEqual(geometryBounds(value), [[-74, 40.7], [-73.9, 40.8]]);
  value.stations[0]!.geometry!.coordinates = [-100, 30];
  assert.deepEqual(geometryBounds(value), [[-74, 40.7], [-73.9, 40.8]]);
  value.shapes = [];
  assert.deepEqual(geometryBounds(value), [[-100, 30], [-100, 30]]);
  value.stations = [];
  assert.equal(geometryBounds(value), null);
  assert.deepEqual(mapFeatures(value), { shapes: { type: 'FeatureCollection', features: [] }, stations: { type: 'FeatureCollection', features: [] } });
});

test('alphabetical English numeric order keeps same-name stations and uses IDs for ties', () => {
  const stations = ['10 St', '2 St', '2 St', 'Alpha'].map((name, index) => ({ ...clone().stations[0]!, id: String(4 - index), properties: { name, reason: null } }));
  assert.deepEqual(alphabeticalStations(stations).map((station) => station.id), ['2', '3', '4', '1']);
  assert.deepEqual(stations.map((station) => station.id), ['4', '3', '2', '1']);
});
