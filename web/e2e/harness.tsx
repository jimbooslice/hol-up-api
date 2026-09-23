// Test-only entry: exercise real components and MapLibre, including delayed source completions.
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { Map, GeoJSONSource } from 'maplibre-gl';
import { App } from '../src/App';

declare global {
  interface Window {
    testMap: Map;
    mapCount: number;
    unmountApp: () => void;
    holdSources: boolean;
    releaseSources: () => void;
  }
}
const addSource = Map.prototype.addSource;
window.mapCount = 0;
Map.prototype.addSource = function (...args) {
  if (window.testMap !== this) { window.mapCount++; window.testMap = this; }
  return addSource.apply(this, args);
};
const setData = GeoJSONSource.prototype.setData;
let releases: (() => void)[] = [];
window.releaseSources = () => { window.holdSources = false; releases.splice(0).forEach(release => release()); };
GeoJSONSource.prototype.setData = async function (...args) {
  if (window.holdSources) await new Promise<void>(resolve => releases.push(resolve));
  return setData.apply(this, args);
};
const root = createRoot(document.getElementById('root')!);
root.render(<StrictMode><App /></StrictMode>);
window.unmountApp = () => root.unmount();
