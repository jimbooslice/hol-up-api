import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';
import { advancedAge, compatible, directionText, isPositions, isStale, journeyView, loadPositions, needsGeometry, PositionsError, reasonText, routeStyle, trainIdentity, warningText } from './positions.ts';
import { positions, train, withVersion } from '../e2e/fixtures/positions.ts';
import { geometry } from '../e2e/fixtures/geometry.ts';
import type { RouteGeometry } from './geometry.ts';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });
test('validates consumed contract, empty arrays, null versions, signed ages and coordinate boundaries', () => {
  assert.ok(isPositions(positions));
  assert.ok(isPositions({ ...positions, trains: [] }));
  assert.ok(isPositions(withVersion(null)));
  assert.ok(isPositions({ ...positions, trains: [{ ...train, latitude: -90, longitude: 180, source_age_seconds: -30 }] }));
  assert.ok(isPositions({ ...positions, trains: [{ ...train, latitude: null, longitude: null, shape_distance_meters: null,
    next_station_id: null, next_stop_sequence: null, next_arrival_at: null, no_position_reason: 'unknown' }] }));
});
test('rejects malformed fields and mismatched per-record versions', () => {
  for (const patch of [
    { route_id: 'B' }, { direction: 'uptown' }, { direction_id: 2 }, { position_basis: 'gps' }, { movement_state: 'moving' },
    { freshness: 'current' }, { projected_at: '2026-02-30T00:00:00Z' }, { source_timestamp: undefined },
    { observation_age_seconds: Infinity }, { source_age_seconds: '10' }, { latitude: 91 }, { longitude: -181 },
    { latitude: null }, { latitude: null, longitude: null }, { source_index: 0.5 }, { reconciliation_outcome: 'guessed' },
    { schedule_version: 'other' }, { trip_instance_id: undefined },
  ]) assert.equal(isPositions({ ...positions, trains: [{ ...train, ...patch }] }), false, JSON.stringify(patch));
  for (const patch of [{ trains: null }, { sources: {} }, { coverage: 'unknown' }, { schedule_age_seconds: NaN },
    { sources: [{ ...positions.sources[0], routes: [1] }] }, { sources: [{ ...positions.sources[0], freshness: 'unknown' }] }]) {
    assert.equal(isPositions({ ...positions, ...patch }), false);
  }
});
test('namespaced identities preserve uncertain records and reject all collisions', () => {
  const uncertain = { ...train, trip_instance_id: null };
  const fallback = { ...uncertain, source_record_id: null };
  assert.notEqual(trainIdentity(train), trainIdentity({ ...uncertain, source_record_id: train.trip_instance_id }));
  assert.notEqual(trainIdentity(fallback), trainIdentity({ ...fallback, source_index: 1 }));
  assert.notEqual(trainIdentity({ ...fallback, source_name: 'a,b', entity_id: 'c' }), trainIdentity({ ...fallback, source_name: 'a', entity_id: 'b,c' }));
  for (const record of [train, uncertain, fallback]) assert.equal(isPositions({ ...positions, trains: [record, record] }), false);
  assert.ok(isPositions({ ...positions, trains: [fallback, { ...fallback, source_index: 1 }] }));
});
test('warnings are optional for older servers and must be arrays of strings when present', () => {
  const { reconciliation_warnings: _, ...older } = train;
  assert.ok(isPositions({ ...positions, trains: [older] }));
  for (const warnings of [[], ['start_time_conflict'], ['start_time_conflict', 'stop_id_sequence_conflict'], ['future_warning', '__proto__', '<script>bad()</script>']]) {
    assert.ok(isPositions({ ...positions, trains: [{ ...train, reconciliation_warnings: warnings }] }));
  }
  for (const warnings of [undefined, null, 'start_time_conflict', 1, {}, [null], [1], ['start_time_conflict', {}]]) {
    assert.equal(isPositions({ ...positions, trains: [{ ...train, reconciliation_warnings: warnings }] }), false);
  }
  assert.match(warningText('start_time_conflict'), /exact trip ID or unique origin\/path suffix was preferred/);
  assert.match(warningText('stop_id_sequence_conflict'), /stop ID was preferred/);
  for (const code of ['future_warning', '__proto__', 'constructor', '<script>bad()</script>']) {
    assert.equal(warningText(code), `The API reported a reconciliation warning: ${code}.`);
  }
  assert.match(reasonText('start_time_conflict'), /rejected/);
  assert.match(reasonText('stop_id_sequence_conflict'), /rejected/);
});
test('versions must match and be non-null before positions can be shown', () => {
  assert.ok(compatible(geometry, positions));
  assert.equal(compatible({ ...geometry, schedule_version: null }, withVersion(null)), false);
  assert.ok(needsGeometry(null, positions));
  assert.ok(needsGeometry(geometry, withVersion('new')));
  assert.equal(needsGeometry(geometry, withVersion(null)), false);
});
test('builds route-colored journey progress and station states', () => {
  const view = journeyView(geometry, { ...train, shape_distance_meters: 2000 });
  assert.equal(view.destination, 'Alpha');
  assert.equal(view.nextStation, 'Alpha');
  assert.deepEqual(view.lines.features.map(feature => feature.properties?.phase), ['visited', 'upcoming']);
  assert.equal(view.nextLeg.features.length, 1);
  assert.deepEqual(view.nextLeg.features[0]?.geometry.coordinates[0], view.lines.features[1]?.geometry.coordinates[0]);
  assert.deepEqual(view.stops.features.map(feature => [feature.properties?.phase, feature.properties?.next]), [['visited', false], ['upcoming', true]]);
  assert.deepEqual(routeStyle('A'), { color: '#0039A6', text: '#FFFFFF' });
  assert.deepEqual(routeStyle('unknown'), { color: '#334155', text: '#FFFFFF' });
});

test('the remaining trip ends at the scheduled terminal, not the shape end', () => {
  const meterPerDegree = 6371008.8 * Math.PI / 180;
  const station = (id: string, longitude: number) => ({ type: 'Feature' as const, id,
    geometry: { type: 'Point' as const, coordinates: [longitude, 0] as [number, number] },
    properties: { name: id, reason: null } });
  const stop = (station_id: string, stop_sequence: number, distance_meters: number | null) => ({
    stop_id: station_id, station_id, stop_sequence, distance_meters, association: 'shape_dist_traveled', reason: null,
  });
  const pattern = { ...geometry.patterns[0]!, service_pattern_id: 'short-turn', shape_id: 'branch', stops: [
    stop('origin', 1, 0), stop('next', 2, 0.006 * meterPerDegree), stop('terminal', 3, 0.01 * meterPerDegree),
  ] };
  const route: RouteGeometry = { ...geometry, stations: [station('origin', 0), station('next', 0.006), station('terminal', 0.01), station('beyond', 0.02)],
    shapes: [{ type: 'Feature', id: 'branch', geometry: { type: 'LineString', coordinates: [[0, 0], [0.01, 0], [0.02, 0]] },
      properties: { usable: true, reason: null } }, { type: 'Feature', id: 'other', geometry: { type: 'LineString', coordinates: [[0, 0], [0, 0.02]] },
      properties: { usable: true, reason: null } }], patterns: [pattern] };
  const current = { ...train, shape_id: 'branch', service_pattern_id: 'short-turn', shape_distance_meters: 0.002 * meterPerDegree,
    next_station_id: 'next', next_stop_sequence: 2 };
  const view = journeyView(route, current);
  assert.equal(view.destination, 'terminal');
  assert.ok(Math.abs(view.lines.features.find(feature => feature.properties?.phase === 'upcoming')!.geometry.coordinates.at(-1)![0]! - 0.01) < 1e-9);
  assert.ok(Math.abs(view.nextLeg.features[0]!.geometry.coordinates.at(-1)![0]! - 0.006) < 1e-9);
  assert.equal(view.stops.features.length, 3);
  assert.equal(journeyView(route, { ...current, shape_id: 'other' }).lines.features.length, 0);
  assert.equal(journeyView(route, { ...current, shape_distance_meters: 0.03 * meterPerDegree }).lines.features.length, 0);
  assert.equal(journeyView(route, { ...current, shape_distance_meters: 0.015 * meterPerDegree }).lines.features.length, 0);
  const unknownTerminal = { ...route, patterns: [{ ...pattern, stops: [...pattern.stops.slice(0, -1), stop('terminal', 3, null)] }] };
  assert.deepEqual(journeyView(unknownTerminal, current).lines.features.map(feature => feature.properties?.phase), ['visited']);
  assert.equal(journeyView(unknownTerminal, current).nextLeg.features.length, 1);
  const unknownNext = { ...route, patterns: [{ ...pattern, stops: [pattern.stops[0]!, stop('next', 2, null), pattern.stops[2]!] }] };
  assert.equal(journeyView(unknownNext, current).nextLeg.features.length, 0);
});

test('direction includes the terminal without inventing missing information', () => {
  assert.equal(directionText('northbound', 'Inwood-207 St'), 'Northbound to Inwood-207 St');
  assert.equal(directionText('unknown', 'Alpha'), 'To Alpha');
  assert.equal(directionText('southbound', null), 'Southbound');
  assert.equal(directionText('unknown', null), 'Unavailable');
});
test('upcoming departure times are optional, validated, and never create markers', () => {
  const upcoming = { ...train, latitude: null, longitude: null, shape_distance_meters: null,
    next_station_id: null, next_stop_sequence: null, next_arrival_at: null, movement_state: 'unknown',
    no_position_reason: 'not_started', predicted_departure_at: '2026-09-14T12:05:00Z' };
  assert.ok(isPositions({ ...positions, trains: [upcoming] }));
  assert.ok(isPositions({ ...positions, trains: [{ ...upcoming, predicted_departure_at: null }] }));
  const { predicted_departure_at: _, ...older } = upcoming;
  assert.ok(isPositions({ ...positions, trains: [older] }));
  for (const value of [undefined, 42, 'tomorrow', '2026-02-30T12:00:00Z']) {
    assert.equal(isPositions({ ...positions, trains: [{ ...upcoming, predicted_departure_at: value }] }), false);
  }
  assert.match(reasonText('not_started'), /future predicted departure/);
  assert.match(warningText('vehicle_stop_sequence_conflict_ignored'), /vehicle observation was excluded/);
});
test('signed server age, timestamp fallback, monotonic request latency and exact stale boundary', () => {
  assert.equal(advancedAge(-30, null, null, 1000, 6000), -25);
  assert.equal(advancedAge(null, '2026-09-14T12:00:00Z', '2026-09-14T12:00:30Z', 1000, 6000), -25);
  assert.equal(advancedAge(null, null, null, 0, 99999), null);
  assert.equal(advancedAge(60, null, null, 1000, 1000), 60);
  assert.equal(isStale('fresh', 60), false);
  assert.equal(isStale('fresh', 60.001), true);
  assert.equal(isStale('stale', -30), true);
  assert.equal(isStale('unavailable', null), false);
});
test('describes current position conflicts without discarding their API codes', () => {
  assert.match(reasonText('insufficient_timing'), /extrapolation/);
  assert.match(reasonText('start_time_conflict'), /start time disagrees/);
  assert.match(reasonText('stop_id_sequence_conflict'), /different scheduled stops/);
  assert.equal(reasonText('future_reason'), 'The API reported future reason.');
});
test('fetch accepts authoritative empty collection and distinguishes failures', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({ ...positions, trains: [] }));
  assert.deepEqual((await loadPositions(new AbortController().signal)).trains, []);
  for (const [status, body, kind] of [
    [503, { error: { code: 'source_unavailable' } }, 'source-unavailable'],
    [503, { error: { code: 'schedule_unavailable' } }, 'schedule-unavailable'],
    [500, {}, 'http-error'], [200, {}, 'invalid-response'],
  ] as const) {
    globalThis.fetch = async () => new Response(JSON.stringify(body), { status });
    await assert.rejects(loadPositions(new AbortController().signal), (e: unknown) => e instanceof PositionsError && e.kind === kind);
  }
  globalThis.fetch = async () => { throw new TypeError('network'); };
  await assert.rejects(loadPositions(new AbortController().signal), { kind: 'network' });
});
test('body reading observes timeout and cancellation, including late body success', async () => {
  for (const timeout of [false, true]) {
    const controller = new AbortController();
    globalThis.fetch = async () => ({ ok: true, json: async () => {
      controller.abort(new DOMException('aborted', timeout ? 'TimeoutError' : 'AbortError'));
      return positions;
    } }) as Response;
    await assert.rejects(loadPositions(controller.signal), timeout ? { kind: 'timeout' } : { name: 'AbortError' });
  }
});
