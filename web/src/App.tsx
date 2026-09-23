import { useCallback, useEffect, useRef, useState } from 'react';
import { alphabeticalStations } from './geometry';
import type { RouteGeometryErrorKind } from './geometry';
import { RouteMap } from './RouteMap';
import './styles.css';
import { useTrainData } from './useTrainData';
import { advancedAge, compatible, isStale, reasonText, trainIdentity, type PositionsErrorKind } from './positions';
import { TrainInfo, sourceAge } from './TrainInfo';

const failureMessages: Record<RouteGeometryErrorKind, string> = {
  'schedule-unavailable': 'Schedule data is temporarily unavailable.',
  'route-not-found': 'The A line was not found in the current schedule.',
  'http-error': 'The route service returned an error.',
  'invalid-response': 'The route service returned data this app cannot safely use.',
  network: 'The route service could not be reached.',
  timeout: 'The route request timed out.',
};

export function App() {
  const data = useTrainData();
  const { geometry: ready, geometryError, snapshot, failure, paused, hidden, retained, now } = data;
  const [selected, setSelected] = useState<string | null>(null);
  const [removal, setRemoval] = useState('');
  const heading = useRef<HTMLHeadingElement>(null);
  // Stable relative order prevents DOM moves from dropping keyboard focus on replacement.
  const trains = [...(snapshot?.data.trains ?? [])].sort((a, b) => {
    const left = trainIdentity(a), right = trainIdentity(b);
    return left < right ? -1 : left > right ? 1 : 0;
  });
  const selectedTrain = trains.find(t => trainIdentity(t) === selected);
  useEffect(() => {
    if (selected && !selectedTrain) {
      setSelected(null); heading.current?.focus(); setRemoval('The selected train was removed from the latest response.');
    }
  }, [selected, selectedTrain]);
  const selectTrain = useCallback((identity: string | null) => { setSelected(identity); setRemoval(''); }, []);
  const compatibleVersions = compatible(ready, snapshot?.data ?? null);
  const positioned = trains.filter(train => train.latitude !== null && train.longitude !== null).length;
  const upcoming = trains.filter(train => train.no_position_reason === 'not_started').length;
  const unavailableReasons = Object.entries(trains.reduce<Record<string, number>>((counts, train) => {
    if (train.no_position_reason && train.no_position_reason !== 'not_started') counts[train.no_position_reason] = (counts[train.no_position_reason] ?? 0) + 1;
    return counts;
  }, {})).sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1));
  const stale = snapshot && (isStale(snapshot.data.freshness, advancedAge(null, snapshot.data.projected_at, snapshot.data.as_of, snapshot.startedAt, now))
    || snapshot.data.sources.some(source => isStale(source.freshness, advancedAge(null, snapshot.data.projected_at, source.source_timestamp, snapshot.startedAt, now)))
    || trains.some(t => isStale(t.freshness, sourceAge(t, snapshot, now))));
  const simulation = trains.some(t => t.position_basis === 'schedule_fallback');
  const failures: Record<PositionsErrorKind, string> = {
    'source-unavailable': 'Realtime source unavailable.', 'schedule-unavailable': 'Train schedule unavailable.',
    timeout: 'Train request timed out.', network: 'Train service could not be reached.',
    'http-error': 'Train service returned an HTTP error.', 'invalid-response': 'Train response failed validation.',
  };
  const transition = [paused ? 'Updates paused.' : hidden ? 'Updates suspended while this tab is hidden.' :
    failure ? `${failures[failure]} Retrying automatically.` : retained && snapshot ? 'Refreshing; retaining the last received snapshot.' :
    snapshot ? 'Train updates available.' : 'Loading train data.',
    stale ? 'Source evidence is stale.' : '', simulation ? retained ? 'Retained positions were schedule simulations.' : 'Schedule simulations are shown.' : '',
    snapshot && !compatibleVersions ? 'Schedule versions are incompatible or unavailable. Train markers are hidden; outcomes remain in the list.' : '',
    removal].filter(Boolean).join(' ');
  const usableShapes = ready?.shapes.filter((shape) => shape.properties.usable && shape.geometry !== null).length ?? 0;

  return <>
    <a className="skip-link" href="#stations">Skip to stations</a>

    <header className="site-header">
      <div className="brand">
        <span className="line-badge" aria-hidden="true">A</span>
        <div>
          <p className="wordmark">Hol Up</p>
          <p className="eyebrow">New York City subway</p>
        </div>
      </div>
      <div className="line-selector">
        <label htmlFor="line">Subway line</label>
        <select id="line" name="line" defaultValue="A">
          <option value="A">A line</option>
        </select>
      </div>
    </header>

    <main>
      <div className="intro">
        <p className="eyebrow">A line · Transit preview</p>
        <h1>Your line, at a glance.</h1>
        <p>A place for your route, stations, and trains.</p>
      </div>

      <div className={`status ${geometryError !== null ? 'status-error' : ''}`} role={geometryError !== null ? 'alert' : 'status'}>
        {!ready && !geometryError && <p><strong>Loading route data…</strong> Fetching A-line stations and geometry.</p>}
        {geometryError !== null && <p><strong>{ready ? 'Route refresh failed; retaining previous geometry.' : 'Route data unavailable.'}</strong> {failureMessages[geometryError!]}{' '}
          <button type="button" onClick={data.retryGeometry} disabled={paused || hidden}>Try again</button>
        </p>}
        {ready && <p><strong>{ready.schedule_eligible ? 'Route data ready.' : 'Route data loaded with a warning.'}</strong>{' '}
          {ready.stations.length} stations and {usableShapes} usable shapes loaded.
          {!ready.schedule_eligible && ' This schedule cannot currently support train simulation.'}
        </p>}
      </div>

      <div className="overview">
        <section className="panel map-panel" aria-labelledby="map-heading">
          <h2 id="map-heading">A-line map</h2>
          {ready ? <RouteMap geometry={ready} positions={snapshot?.data ?? null} selected={selected} onSelect={selectTrain} /> : <div className="map-placeholder">
            <h3>{!ready && !geometryError ? 'Loading map data' : 'Map data unavailable'}</h3>
            <p>The route and its branches will appear here when route data is available.</p>
          </div>}
        </section>

        <div className="lists">
          <section className="panel" id="stations" tabIndex={-1} aria-labelledby="stations-heading">
            <p className="eyebrow">Alphabetical directory</p>
            <h2 id="stations-heading">Stations</h2>
            {ready && ready.stations.length > 0 ? <ul className="station-list">
              {alphabeticalStations(ready.stations).map((station) => <li key={station.id}>
                <span>{station.properties.name}</span>
                {!station.geometry && <small>Location unavailable: {station.properties.reason === 'missing_station_coordinates'
                  ? 'Station coordinates are missing.' : station.properties.reason}</small>}
              </li>)}
            </ul> : <p className="empty-state">{ready ? 'No stations are listed for this route.'
              : !ready && !geometryError ? 'Loading A-line stations…' : 'Stations could not be loaded.'}</p>}
          </section>

          <section className="panel" aria-labelledby="trains-heading">
            <p className="eyebrow">Service overview</p>
            <h2 id="trains-heading" tabIndex={-1} ref={heading}>Trains</h2>
            <button type="button" onClick={data.togglePause}>{paused ? 'Resume updates' : 'Pause updates'}</button>
            <p role="status" aria-live="polite" aria-atomic="true">{transition}</p>
            {snapshot && <>
              <p>{retained ? 'Last received positions' : 'Accepted positions'}, projected at {snapshot.data.projected_at ?? 'an unavailable time'}.</p>
              <p>Source coverage: {snapshot.data.coverage}. {!snapshot.data.schedule_eligible && 'Schedule is ineligible for simulation.'}</p>
              <p>{compatibleVersions
                ? `${positioned} of ${trains.length} train outcomes ${positioned === 1 ? 'is' : 'are'} positioned on the map.`
                : 'Train markers are hidden until geometry and position schedule versions match.'}</p>
              <p>{positioned} positioned · {upcoming} upcoming · {trains.length - positioned - upcoming} position unavailable</p>
              {unavailableReasons.length > 0 && <section className="position-warning" aria-labelledby="position-warning-heading">
                <h3 id="position-warning-heading">Why some trains are not on the map</h3>
                <p>The API withheld coordinates where realtime and schedule evidence could not support a trustworthy position. These trains remain in the list rather than appearing at guessed locations.</p>
                <ul>{unavailableReasons.map(([reason, count]) => <li key={reason}>
                  <strong>{count} {count === 1 ? 'train' : 'trains'}:</strong> {reasonText(reason)} <code>{reason}</code>
                </li>)}</ul>
              </section>}
              <ul className="train-list">{trains.map(train => <li key={trainIdentity(train)}>
                <button type="button" aria-pressed={selected === trainIdentity(train)} onClick={() => selectTrain(selected === trainIdentity(train) ? null : trainIdentity(train))}>
                  {train.trip_instance_id ? `Run ${train.trip_instance_id}` : `Source record ${train.source_record_id ?? `${train.source_name} / ${train.entity_id} / ${train.source_index}`}`}
                </button>
                <TrainInfo train={train} snapshot={snapshot} now={now} retained={retained} geometry={ready} />
              </li>)}</ul>
              {trains.length === 0 && <p>No trains in the latest response.</p>}
            </>}
          </section>
        </div>
      </div>

      <section className="panel details" aria-labelledby="details-heading">
        <h2 id="details-heading">Train details</h2>
        {selectedTrain && snapshot ? <>
          <h3>{selectedTrain.trip_instance_id ? `Run ${selectedTrain.trip_instance_id}` : `Source record ${selectedTrain.source_record_id ?? selectedTrain.entity_id}`}</h3>
          <TrainInfo train={selectedTrain} snapshot={snapshot} now={now} retained={retained} geometry={ready} />
        </> : <p>Select a train from the map or list to inspect its evidence.</p>}
      </section>
    </main>

    <footer className="site-footer">
      <p>Hol Up · An independent transit prototype</p>
      <p>Transit data source: <a href="https://www.mta.info/developers">MTA</a></p>
    </footer>
  </>;
}
