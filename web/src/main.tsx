import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
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

      <div className="status" role="status">
        <p><strong>Not connected yet.</strong> Live transit data isn’t connected in this preview.</p>
      </div>

      <div className="overview">
        <section className="panel map-panel" aria-labelledby="map-heading">
          <h2 id="map-heading">A-line map</h2>
          <div className="map-placeholder">
            <span className="line-badge" aria-hidden="true">A</span>
            <h3>Map not connected yet</h3>
            <p>The route, its branches, and stations will appear here once map data is connected.</p>
          </div>
        </section>

        <div className="lists">
          <section className="panel" id="stations" tabIndex={-1} aria-labelledby="stations-heading">
            <p className="eyebrow">Along the line</p>
            <h2 id="stations-heading">Stations</h2>
            <p className="empty-state">Station data is not connected yet. A-line stations will be listed here.</p>
          </section>

          <section className="panel" aria-labelledby="trains-heading">
            <p className="eyebrow">On the move</p>
            <h2 id="trains-heading">Trains</h2>
            <p className="empty-state">Train data is not connected yet. No train positions or arrival estimates are shown.</p>
          </section>
        </div>
      </div>

      <section className="panel details" aria-labelledby="details-heading">
        <h2 id="details-heading">Train details</h2>
        <p>Nothing selected. Train details will be available once train data is connected.</p>
      </section>
    </main>

    <footer className="site-footer">
      <p>Hol Up · An independent transit prototype</p>
      <p>Transit data source: <a href="https://www.mta.info/developers">MTA</a></p>
    </footer>
  </StrictMode>,
);
