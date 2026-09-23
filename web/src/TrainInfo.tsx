import type { RouteGeometry } from './geometry';
import { advancedAge, directionText, isStale, journeyView, reasonText, warningText, type Snapshot, type Train } from './positions';

const words = (value: string) => value.replaceAll('_', ' ');
const departureTime = new Intl.DateTimeFormat('en-US', {
  timeZone: 'America/New_York', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', second: '2-digit', timeZoneName: 'short',
});
function ageText(age: number | null, clock: string) {
  if (age === null) return 'Unavailable';
  if (age < 0) return `${clock} clock is approximately ${Math.ceil(-age)} seconds ahead`;
  return `Approximately ${Math.floor(age)} seconds`;
}
export function sourceAge(train: Train, snapshot: Snapshot, now: number) {
  return advancedAge(train.source_age_seconds, train.projected_at ?? snapshot.data.projected_at,
    train.source_timestamp, snapshot.startedAt, now);
}
export function TrainInfo({ train, snapshot, now, retained, geometry }: { train: Train; snapshot: Snapshot; now: number; retained: boolean; geometry?: RouteGeometry | null }) {
  const age = sourceAge(train, snapshot, now);
  const observation = advancedAge(train.observation_age_seconds, train.projected_at ?? snapshot.data.projected_at,
    train.observation_time, snapshot.startedAt, now);
  const upcoming = train.no_position_reason === 'not_started';
  const journey = geometry ? journeyView(geometry, train) : null;
  return <div className="train-info">
    <p><strong>{upcoming ? 'Upcoming' : train.position_basis === 'schedule_fallback'
      ? retained ? 'Was a schedule simulation' : 'Schedule simulation' : 'Realtime inference'}</strong> · {isStale(train.freshness, age) ? 'Stale source evidence' : train.freshness === 'unavailable' ? 'Source freshness unavailable' : 'Fresh source evidence'}</p>
    <dl>
      {journey?.destination && <><dt>Destination</dt><dd>{journey.destination}</dd></>}
      {journey?.nextStation && <><dt>Next station</dt><dd>{journey.nextStation}{train.next_arrival_at
        ? <> · <time dateTime={train.next_arrival_at}>{departureTime.format(new Date(train.next_arrival_at))}</time></> : ' · Arrival unavailable'}</dd></>}
      {upcoming && <><dt>Predicted departure</dt><dd>{train.predicted_departure_at
        ? <time dateTime={train.predicted_departure_at}>{departureTime.format(new Date(train.predicted_departure_at))}</time>
        : 'Unavailable'}</dd></>}
      <dt>Direction</dt><dd>{directionText(train.direction, journey?.destination ?? null)}</dd>
      <dt>Movement state</dt><dd>{words(train.movement_state)}</dd>
      <dt>Projected at</dt><dd>{train.projected_at ?? snapshot.data.projected_at ?? 'Unavailable'}</dd>
      <dt>Source timestamp</dt><dd>{train.source_timestamp ?? 'Unavailable'}</dd>
      <dt>Source age</dt><dd>{ageText(age, 'Source')}</dd>
      <dt>Observation timestamp</dt><dd>{train.observation_time ?? 'Unavailable'}</dd>
      <dt>Observation age</dt><dd>{ageText(observation, 'Observation')}</dd>
      <dt>Observation basis</dt><dd>{words(train.observation_basis)}</dd>
      <dt>Reconciliation</dt><dd>{words(train.reconciliation_outcome)}: {reasonText(train.reconciliation_reason)} <code>{train.reconciliation_reason}</code></dd>
    </dl>
    {!!train.reconciliation_warnings?.length && <section className="position-warning" aria-label="Reconciliation warnings">
      <h3>Reconciliation warnings</h3>
      <ul>{train.reconciliation_warnings.map((warning, index) => <li key={index}>
        {warningText(warning)} <code>{warning}</code>
      </li>)}</ul>
    </section>}
    {train.fallback_reason && <p>Simulation reason: {train.fallback_reason}</p>}
    {train.no_position_reason && <p>{upcoming ? 'Upcoming departure' : 'Position unavailable'}: {reasonText(train.no_position_reason)} <code>{train.no_position_reason}</code></p>}
  </div>;
}
