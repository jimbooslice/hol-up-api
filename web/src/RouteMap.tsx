import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Map, setWorkerUrl, type ErrorEvent, type ExpressionSpecification, type GeoJSONSource, type StyleSpecification } from 'maplibre-gl';
import workerURL from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url';
import 'maplibre-gl/dist/maplibre-gl.css';
import { geometryBounds, mapFeatures, type RouteGeometry } from './geometry';
import { compatible, directionText, journeyView, routeStyle, trainIdentity, type Positions, type Train } from './positions';
import { createTrainInteraction } from './trainInteraction';
import { TrainMarkers, type ProjectedTrain } from './TrainMarkers';

setWorkerUrl(workerURL);

// TODO: Make the basemap style and detail visibility configurable when a
// second style or user control is required.
const styleURL = 'https://tiles.openfreemap.org/styles/positron';
const colors = { background: '#edf1f5', casing: '#18263a', station: '#ffffff', visited: '#8d98a8' };
const fallbackStyle: StyleSpecification = {
  version: 8, sources: {}, layers: [{ id: 'background', type: 'background', paint: { 'background-color': colors.background } }],
};
const fitOptions = { padding: 32, maxZoom: 14, duration: 0 };
const emptyLines: GeoJSON.FeatureCollection<GeoJSON.LineString> = { type: 'FeatureCollection', features: [] };
const emptyPoints: GeoJSON.FeatureCollection<GeoJSON.Point> = { type: 'FeatureCollection', features: [] };
const emptyGradient = ['interpolate', ['linear'], ['line-progress'], 0, 'rgba(255, 255, 255, 0)', 1, 'rgba(255, 255, 255, 0)'] as ExpressionSpecification;

function installGeometry(map: Map, geometry: RouteGeometry) {
  for (const layer of map.getStyle().layers) {
    if (layer.id.includes('building') || ('source-layer' in layer && layer['source-layer'] === 'building')) map.setLayoutProperty(layer.id, 'visibility', 'none');
  }
  const features = mapFeatures(geometry);
  if (!map.getSource('shapes')) map.addSource('shapes', { type: 'geojson', data: features.shapes, promoteId: 'feature_id' });
  if (!map.getSource('stations')) map.addSource('stations', { type: 'geojson', data: features.stations, promoteId: 'feature_id' });
  if (!map.getSource('selected-route')) map.addSource('selected-route', { type: 'geojson', data: emptyLines });
  if (!map.getSource('selected-progress')) map.addSource('selected-progress', { type: 'geojson', data: emptyLines, lineMetrics: true });
  if (!map.getSource('selected-stops')) map.addSource('selected-stops', { type: 'geojson', data: emptyPoints });
  if (!map.getLayer('route-casing')) map.addLayer({ id: 'route-casing', type: 'line', source: 'shapes', layout: { 'line-cap': 'round', 'line-join': 'round' }, paint: { 'line-color': colors.casing, 'line-width': 7 } });
  if (!map.getLayer('route-line')) map.addLayer({ id: 'route-line', type: 'line', source: 'shapes', layout: { 'line-cap': 'round', 'line-join': 'round' }, paint: { 'line-color': routeStyle(geometry.route_id).color, 'line-width': 4 } });
  if (!map.getLayer('station-circles')) map.addLayer({ id: 'station-circles', type: 'circle', source: 'stations', paint: { 'circle-radius': 5, 'circle-color': colors.station, 'circle-stroke-color': colors.casing, 'circle-stroke-width': 2 } });
  if (map.getStyle().glyphs && !map.getLayer('station-labels')) map.addLayer({
    id: 'station-labels', type: 'symbol', source: 'stations', minzoom: 13,
    layout: { 'text-field': ['get', 'name'], 'text-font': ['Noto Sans Regular'], 'text-size': 12, 'text-anchor': 'left', 'text-offset': [0.8, 0] },
    paint: { 'text-color': colors.casing, 'text-halo-color': colors.station, 'text-halo-width': 2 },
  });
  if (!map.getLayer('selected-route-casing')) map.addLayer({ id: 'selected-route-casing', type: 'line', source: 'selected-route', layout: { 'line-cap': 'round', 'line-join': 'round' }, paint: { 'line-color': colors.station, 'line-width': 10 } });
  if (!map.getLayer('selected-route-line')) map.addLayer({ id: 'selected-route-line', type: 'line', source: 'selected-route', layout: { 'line-cap': 'round', 'line-join': 'round' }, paint: { 'line-color': ['case', ['==', ['get', 'phase'], 'visited'], colors.visited, ['get', 'color']], 'line-width': 6 } });
  if (!map.getLayer('selected-progress')) map.addLayer({ id: 'selected-progress', type: 'line', source: 'selected-progress', layout: { visibility: 'none', 'line-cap': 'round', 'line-join': 'round' }, paint: { 'line-width': 6, 'line-blur': 0.6, 'line-gradient': emptyGradient } });
  if (!map.getLayer('selected-stations')) map.addLayer({ id: 'selected-stations', type: 'circle', source: 'selected-stops', paint: {
    'circle-radius': ['case', ['boolean', ['get', 'next'], false], 8, 6],
    'circle-color': ['case', ['==', ['get', 'phase'], 'visited'], colors.visited, colors.station],
    'circle-stroke-color': ['case', ['==', ['get', 'phase'], 'visited'], colors.casing, ['get', 'color']],
    'circle-stroke-width': ['case', ['boolean', ['get', 'next'], false], 3, 2],
  } });
}

export function RouteMap({ geometry, positions, selected, onSelect }: { geometry: RouteGeometry; positions: Positions | null; selected: string | null; onSelect: (identity: string | null) => void }) {
  const container = useRef<HTMLDivElement>(null);
  const detailsPanel = useRef<HTMLElement>(null);
  const stage = useRef<HTMLDivElement>(null);
  const mapRef = useRef<Map | null>(null);
  const interaction = useRef<ReturnType<typeof createTrainInteraction> | null>(null);
  const [status, setStatus] = useState<'loading' | 'ready' | 'unavailable'>('loading');
  const [basemapWarning, setBasemapWarning] = useState(false);
  const [markers, setMarkers] = useState<ProjectedTrain[]>([]);
  const bounds = geometryBounds(geometry);
  const hasUsableShapes = geometry.shapes.some(shape => shape.properties.usable && shape.geometry !== null);
  const latest = useRef({ geometry, positions, selected, onSelect });
  const update = useRef<() => void>(() => {});
  const updateMarkers = useRef<() => void>(() => {});
  useLayoutEffect(() => {
    const dataChanged = latest.current.geometry !== geometry || latest.current.positions !== positions;
    latest.current = { geometry, positions, selected, onSelect };
    if (dataChanged) update.current(); else interaction.current?.refresh();
    updateMarkers.current();
  }, [geometry, positions, selected, onSelect]);

  useEffect(() => {
    let map: Map | undefined;
    let observer: ResizeObserver | undefined;
    let deadline: ReturnType<typeof setTimeout> | undefined;
    let disposed = false, fallback = false, fitted = false, revision = 0;
    let queue = Promise.resolve();
    let pendingReveal: number | null = null;
    let renderedGeometry: RouteGeometry | null = null;
    const layoutMarkers = () => {
      const accepted = latest.current;
      if (!map || accepted.geometry !== renderedGeometry || !compatible(accepted.geometry, accepted.positions)) { setMarkers([]); return; }
      setMarkers(accepted.positions!.trains
        .filter((train): train is Train & { latitude: number; longitude: number } => train.latitude !== null && train.longitude !== null)
        .map(train => {
          const point = map!.project([train.longitude, train.latitude]);
          const journey = journeyView(accepted.geometry, train);
          const status = train.position_basis === 'realtime_prediction' ? 'realtime estimate, not GPS' : 'schedule simulation, not realtime';
          return { train, identity: trainIdentity(train), x: point.x, y: point.y,
            accessibilityText: `${train.route_id}, ${directionText(train.direction, journey.destination)}, ${status}, ${train.next_arrival_at ?? 'arrival unavailable'}` };
        }));
    };
    updateMarkers.current = layoutMarkers;
    const synchronize = () => {
      if (disposed) return;
      const version = ++revision;
      interaction.current?.suspend();
      queue = queue.then(async () => {
        if (disposed || version !== revision || !map?.getSource('shapes') || !map.getSource('stations')) return;
        const accepted = latest.current;
        const features = mapFeatures(accepted.geometry);
        await Promise.all([
          (map.getSource('shapes') as GeoJSONSource).setData(features.shapes),
          (map.getSource('stations') as GeoJSONSource).setData(features.stations),
        ]);
        if (disposed || version !== revision) return;
        map.setPaintProperty('route-line', 'line-color', routeStyle(accepted.geometry.route_id).color);
        const initialBounds = geometryBounds(accepted.geometry);
        if (!fitted && initialBounds) { map.fitBounds(initialBounds, fitOptions, { trainInspection: true }); fitted = true; }
        pendingReveal = version; layoutMarkers();
      }).catch(() => { if (!disposed && version === revision) unavailable(); });
    };
    update.current = synchronize;
    setStatus('loading'); setBasemapWarning(false);
    const unavailable = () => {
      if (disposed) return;
      disposed = true; ++revision; clearTimeout(deadline); observer?.disconnect();
      map?.off('style.load', onStyle); map?.off('idle', onIdle); map?.off('error', onError); map?.off('webglcontextlost', unavailable);
      interaction.current?.destroy(); interaction.current = null;
      map?.remove(); mapRef.current = null; setStatus('unavailable');
    };
    const useFallback = () => {
      if (disposed || fallback || !map) return;
      fallback = true; clearTimeout(deadline); setBasemapWarning(true); ++revision;
      renderedGeometry = null; setMarkers([]);
      interaction.current?.suspend();
      try { map.setStyle(fallbackStyle, { diff: false }); } catch { unavailable(); }
    };
    const onStyle = () => {
      if (disposed || !map) return;
      queue = Promise.resolve();
      try { installGeometry(map, latest.current.geometry); synchronize(); } catch { unavailable(); }
    };
    const onIdle = () => {
      clearTimeout(deadline);
      if (disposed || !map || pendingReveal !== revision) return;
      if (!['shapes', 'stations'].every(id => map!.isSourceLoaded(id))) return;
      pendingReveal = null;
      renderedGeometry = latest.current.geometry;
      interaction.current?.resume(); layoutMarkers();
      setStatus('ready');
    };
    const onError = (event: ErrorEvent & { sourceId?: string }) => {
      if (fallback && ['shapes', 'stations', 'selected-route', 'selected-progress', 'selected-stops'].includes(event.sourceId ?? '')) unavailable(); else useFallback();
    };
    try {
      map = new Map({ container: container.current!, style: styleURL, center: [-73.94, 40.72], zoom: 10, bearing: 0, pitch: 0, minPitch: 0, maxPitch: 0,
        dragRotate: false, touchZoomRotate: true, touchPitch: false, pitchWithRotate: false, rollEnabled: false, cooperativeGestures: true, attributionControl: false });
      mapRef.current = map; map.touchZoomRotate.disableRotation(); map.keyboard.disableRotation();
      map.getCanvas().draggable = false;
      interaction.current = createTrainInteraction(map, detailsPanel.current!, () => latest.current, identity => {
        const marker = [...(stage.current?.querySelectorAll<HTMLElement>('.train-marker') ?? [])]
          .find(element => element.dataset.identity === identity)?.querySelector<HTMLElement>('.train-marker-badge');
        if (!marker || !container.current) return null;
        const markerBounds = marker.getBoundingClientRect(), mapBounds = container.current.getBoundingClientRect();
        return { x: markerBounds.left + markerBounds.width / 2 - mapBounds.left, y: markerBounds.top + markerBounds.height / 2 - mapBounds.top };
      });
      map.getCanvas().setAttribute('aria-label', 'A-line map. Route badges show trains. Overlapping trains share the same map position; every train outcome is in the train list. Use arrow keys to pan and plus or minus to zoom.');
      map.on('style.load', onStyle); map.on('idle', onIdle); map.on('error', onError); map.on('webglcontextlost', unavailable);
      map.on('move', layoutMarkers);
      observer = new ResizeObserver(() => { if (!disposed) { map?.resize(); layoutMarkers(); } }); observer.observe(container.current!);
      deadline = setTimeout(useFallback, 10_000);
    } catch { unavailable(); }
    return () => {
      clearTimeout(deadline); observer?.disconnect(); interaction.current?.destroy(); interaction.current = null;
      if (!disposed && map) {
        disposed = true; map.off('style.load', onStyle); map.off('idle', onIdle); map.off('error', onError); map.off('webglcontextlost', unavailable);
        map.remove();
      }
      map?.off('move', layoutMarkers); ++revision; update.current = () => {}; updateMarkers.current = () => {}; mapRef.current = null;
    };
  }, []);

  const route = routeStyle(geometry.route_id);
  return <>
    <div className="map-controls" aria-label="Map controls">
      <button type="button" aria-label="Zoom in" disabled={status !== 'ready'} onClick={() => mapRef.current?.zoomIn({ duration: 0 })}>+</button>
      <button type="button" aria-label="Zoom out" disabled={status !== 'ready'} onClick={() => mapRef.current?.zoomOut({ duration: 0 })}>−</button>
      <button type="button" disabled={status !== 'ready' || !bounds} onClick={() => { if (bounds) mapRef.current?.fitBounds(bounds, fitOptions); }}>Fit A line</button>
    </div>
    <div className="map-stage" ref={stage}>
      <div className="route-map" ref={container} hidden={status === 'unavailable'} />
      <TrainMarkers markers={status === 'ready' ? markers : []} selected={selected} map={mapRef.current} interaction={interaction} />
    </div>
    <section className="marker-details-panel" aria-label="Map train details" ref={detailsPanel} hidden />
    <p className="map-hint">Hover a train badge to preview its trip; click or tap to pin. Overlapping trains share the same map position.</p>
    <p className="map-status" role="status">
      {status === 'unavailable' ? 'Map unavailable. Your browser could not render the map. Stations and trains remain available in the lists.'
        : status === 'loading' ? 'Loading map…' : 'A-line map ready. Lines show route shapes; circles show located stations.'}
      {basemapWarning && ' Basemap unavailable. Showing route geometry on a plain background; station names remain in the directory.'}
    </p>
    {!hasUsableShapes && <p className="map-status">No usable route shapes are available.{bounds ? ' Showing located stations.' : ' No map locations are available.'}</p>}
    <p className="map-legend"><span className="train-key realtime-key" style={{ backgroundColor: route.color, borderColor: route.color }} aria-hidden="true" /> Filled route badges: realtime inference. <span className="train-key simulation-key" style={{ borderColor: route.color }} aria-hidden="true" /> Hollow route badges: schedule simulation. Neither is GPS tracking.</p>
    <p className="map-attribution"><a href="https://openfreemap.org/">OpenFreeMap</a> · <a href="https://www.openmaptiles.org/">© OpenMapTiles</a> · Data from <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a></p>
  </>;
}
