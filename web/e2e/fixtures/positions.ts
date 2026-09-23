import type { Positions, Train } from '../../src/positions';
export const train: Train = {
  route_id: 'A', direction: 'unknown', direction_id: 1, trip_instance_id: 'run-one', source_record_id: 'record-one',
  source_name: 'ace', entity_id: 'entity', source_index: 0, schedule_version: 'fixture:branching',
  shape_id: 'west', service_pattern_id: 'pattern-west', latitude: 40.73, longitude: -73.98, shape_distance_meters: 0,
  next_station_id: 'S4', next_stop_sequence: 2, next_arrival_at: '2026-09-14T12:05:00Z',
  projected_at: '2026-09-14T12:00:00Z', position_basis: 'realtime_prediction',
  movement_state: 'inferred_moving', source_timestamp: '2026-09-14T11:59:50Z', source_age_seconds: 10,
  freshness: 'fresh', observation_time: '2026-09-14T11:59:20Z', observation_age_seconds: 40,
  observation_basis: 'trip_update', reconciliation_outcome: 'matched', reconciliation_reason: 'unique_match',
  reconciliation_warnings: [],
  fallback_reason: null, no_position_reason: null,
};
export const positions: Positions = {
  schedule_version: 'fixture:branching', schedule_eligible: true, schedule_source: 'supplemented',
  schedule_validated_at: '2026-09-14T12:00:00Z', schedule_age_seconds: 0,
  as_of: train.source_timestamp, projected_at: train.projected_at, bundle_evaluated_at: train.projected_at,
  freshness: 'fresh', coverage: 'complete', sources: [{ name: 'ace', routes: ['A', 'C', 'E'], source_timestamp: train.source_timestamp, freshness: 'fresh' }],
  trains: [train],
};
export const emptyPositions = { ...positions, trains: [] };
export const withVersion = (version: string | null): Positions => ({ ...positions, schedule_version: version,
  trains: [{ ...train, schedule_version: version }] });
