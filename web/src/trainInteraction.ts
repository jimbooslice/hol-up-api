import { Popup, type Map as RouteMap, type GeoJSONSource, type EaseToOptions, type ExpressionSpecification } from 'maplibre-gl';
import type { RouteGeometry } from './geometry';
import { compatible, directionText, journeyView, routeStyle, trainIdentity, type Positions, type Train } from './positions';
import { choosePopupPlacement, type Point } from './popupPlacement';

const layers = ['selected-route-casing', 'selected-route-line', 'selected-progress', 'selected-stations'];
const empty = { type: 'FeatureCollection' as const, features: [] };
const clock = new Intl.DateTimeFormat('en-US', { timeZone: 'America/New_York', hour: 'numeric', minute: '2-digit', timeZoneName: 'short' });
type View = { geometry: RouteGeometry; positions: Positions | null; selected: string | null; onSelect: (identity: string | null) => void };
type Camera = { center: [number, number]; zoom: number; bearing: number; pitch: number };
type CardMode = 'preview' | 'selected' | 'panel';
export type MarkerTargetResolver = (identity: string) => Point | null;

function progressGradient(position: number, color: string) {
  const value = Number.parseInt(color.slice(1), 16);
  const channels = [16, 8, 0].map(shift => (value >> shift) & 255);
  const shade = (mix: number) => `rgb(${channels.map(channel => Math.round(channel * (1 - mix) + 255 * mix)).join(', ')})`;
  const clear = `rgba(${channels.join(', ')}, 0)`;
  return ['interpolate', ['linear'], ['abs', ['-', ['line-progress'], position]],
    0, shade(0.65), 0.05, shade(0.35), 0.12, color, 0.14, clear] as ExpressionSpecification;
}

function detail(list: HTMLDListElement, label: string, value: string) {
  const term = document.createElement('dt'); term.textContent = label;
  const description = document.createElement('dd'); description.textContent = value;
  list.append(term, description);
}

function markerCard(view: View, train: Train, mode: CardMode, close: () => void, move: () => void) {
  const selected = mode !== 'preview';
  const root = document.createElement('section'); root.className = 'marker-card'; root.dataset.state = selected ? 'selected' : 'preview';
  const header = document.createElement('div'); header.className = 'marker-card-header';
  const badge = document.createElement('span'); badge.className = 'marker-card-badge'; badge.textContent = train.route_id;
  const style = routeStyle(train.route_id); badge.style.background = style.color; badge.style.color = style.text;
  root.style.setProperty('--route-color', style.color);
  const title = document.createElement('strong');
  const journey = journeyView(view.geometry, train);
  title.textContent = `${train.route_id} to ${journey.destination ?? 'destination unavailable'}`;
  header.append(badge, title);
  if (mode === 'selected') {
    const pin = document.createElement('button'); pin.type = 'button'; pin.textContent = '⇩'; pin.title = 'Pin details below map'; pin.setAttribute('aria-label', pin.title);
    pin.addEventListener('click', event => { event.stopPropagation(); move(); }); header.append(pin);
  }
  if (selected) {
    const button = document.createElement('button'); button.type = 'button'; button.textContent = 'Close'; button.setAttribute('aria-label', 'Close train details');
    button.addEventListener('click', event => { event.stopPropagation(); close(); }); header.append(button);
  }
  if (mode === 'panel') {
    const show = document.createElement('button'); show.type = 'button'; show.textContent = 'Show details on map'; show.setAttribute('aria-label', show.textContent);
    show.addEventListener('click', event => { event.stopPropagation(); move(); }); header.insertBefore(show, header.lastElementChild);
  }
  const state = document.createElement('p'); state.className = 'marker-card-state';
  state.textContent = selected ? 'Selected train' : 'Trip preview · Click or tap to pin';
  const list = document.createElement('dl');
  detail(list, 'Direction', directionText(train.direction, journey.destination));
  detail(list, 'Status', train.position_basis === 'realtime_prediction' ? 'Realtime estimate · not GPS' : 'Schedule estimate · not realtime');
  const updated = train.observation_time ?? train.source_timestamp;
  const age = train.observation_age_seconds ?? train.source_age_seconds;
  const relativeAge = age === null ? 'age unavailable' : age < 0 ? `source clock ${Math.ceil(-age)}s ahead` : age < 60 ? `${Math.floor(age)}s ago` : `${Math.floor(age / 60)}m ago`;
  detail(list, 'Last update', updated ? `${clock.format(new Date(updated))} · ${relativeAge}` : 'Unavailable');
  detail(list, 'Next station', journey.nextStation ?? 'Unavailable');
  const projected = train.projected_at ?? view.positions?.projected_at;
  const delta = train.next_arrival_at && projected ? Date.parse(train.next_arrival_at) - Date.parse(projected) : NaN;
  const relativeArrival = Number.isFinite(delta) ? delta <= 0 ? ' · due' : ` · in ${Math.max(1, Math.ceil(delta / 60_000))} min` : '';
  detail(list, 'Estimated arrival', train.next_arrival_at ? clock.format(new Date(train.next_arrival_at)) + relativeArrival : 'Arrival unavailable');
  root.append(header, state, list);
  return root;
}

// One preview sits above persistent selection. Camera snapshots follow the same
// stack; deliberate navigation invalidates both snapshots.
export function createTrainInteraction(map: RouteMap, panel: HTMLElement, current: () => View, markerTarget?: MarkerTargetResolver) {
  let hovered: string | null = null;
  let selected = current().selected;
  let hoverCamera: Camera | null = null;
  let selectedCamera: Camera | null = null;
  let popup: Popup | undefined;
  let restoreCardFocus = false;
  let detailsBelow = false;
  let pendingFocusLabel: string | null = null;
  let suspended = true, disposed = false, revision = 0, placementAttempted = false, manualCamera = false;
  let sourceQueue = Promise.resolve();
  let journeySource: GeoJSONSource | undefined;
  let leaveTimer: ReturnType<typeof setTimeout> | undefined;
  let trigger: HTMLElement | null = null;
  let previewElement: HTMLElement | null = null;
  let progressFrame: number | null = null;
  const capture = (): Camera => ({ center: map.getCenter().toArray(), zoom: map.getZoom(), bearing: map.getBearing(), pitch: map.getPitch() });
  const visibility = (visible: boolean) => { for (const layer of layers) if (map.getLayer(layer)) map.setLayoutProperty(layer, 'visibility', visible ? 'visible' : 'none'); };
  const hidePopup = () => popup?.remove();
  const hideCard = () => { hidePopup(); panel.replaceChildren(); panel.hidden = true; };
  const restore = (camera: Camera | null) => { if (camera && !disposed) map.jumpTo(camera, { trainInspection: true }); };
  const cancelLeave = () => clearTimeout(leaveTimer);
  const clearPreviewElement = () => { previewElement?.removeAttribute('data-previewed'); previewElement = null; };
  const stopProgress = () => {
    if (progressFrame !== null) cancelAnimationFrame(progressFrame);
    progressFrame = null;
    if (map.getLayer('selected-progress')) map.setPaintProperty('selected-progress', 'line-opacity', 0);
  };
  const startProgress = (train: Train, version: number) => {
    const paint = (position: number) => map.setPaintProperty('selected-progress', 'line-gradient', progressGradient(position, routeStyle(train.route_id).color));
    map.setPaintProperty('selected-progress', 'line-opacity', 1);
    if (matchMedia('(prefers-reduced-motion: reduce)').matches) { paint(0.35); return; }
    let started: number | undefined;
    const frame = (now: number) => {
      if (disposed || suspended || version !== revision || !map.getLayer('selected-progress')) { progressFrame = null; return; }
      started ??= now; paint(((now - started) % 1800) / 1800);
      progressFrame = requestAnimationFrame(frame);
    };
    progressFrame = requestAnimationFrame(frame);
  };
  const clearHover = () => {
    cancelLeave(); clearPreviewElement(); hovered = null;
    const camera = hoverCamera; hoverCamera = null; restore(camera);
  };
  const dismiss = () => {
    const hadCardFocus = popup?.getElement()?.contains(document.activeElement) || panel.contains(document.activeElement);
    ++revision; clearHover(); restore(selectedCamera); selectedCamera = null;
    placementAttempted = false; restoreCardFocus = false; detailsBelow = false; pendingFocusLabel = null; hideCard();
    if (hadCardFocus) (trigger?.isConnected ? trigger : map.getCanvas()).focus({ preventScroll: true });
    if (selected) current().onSelect(null); else refresh();
  };
  const scheduleLeave = () => {
    cancelLeave();
    leaveTimer = setTimeout(() => { if (hovered) { ++revision; clearHover(); placementAttempted = true; refresh(); } }, 150);
  };
  const changeCamera = (options: EaseToOptions) => new Promise<void>(resolve => {
    const done = () => { map.off('moveend', done); resolve(); };
    map.once('moveend', done);
    map.easeTo({ ...options, duration: matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 180 }, { trainInspection: true });
    if (!map.isMoving()) done();
  });

  const moveDetails = (below: boolean) => {
    detailsBelow = below; restoreCardFocus = true; placementAttempted = false;
    pendingFocusLabel = below ? 'Show details on map' : 'Pin details below map';
    if (below) restore(selectedCamera);
    else { manualCamera = false; selectedCamera ??= capture(); }
    ++revision; refresh();
  };

  function renderPanel(view: View, train: Train | undefined) {
    const active = panel.contains(document.activeElement) ? document.activeElement as HTMLElement : null;
    const focusLabel = pendingFocusLabel ?? active?.closest('button')?.getAttribute('aria-label');
    panel.replaceChildren(); panel.hidden = true;
    if (!detailsBelow || !train) return;
    panel.append(markerCard(view, train, 'panel', dismiss, () => moveDetails(false)));
    panel.hidden = false;
    if ((restoreCardFocus || active) && focusLabel) {
      panel.querySelector<HTMLButtonElement>(`[aria-label="${focusLabel}"]`)?.focus({ preventScroll: true });
      restoreCardFocus = false; pendingFocusLabel = null;
    }
  }

  async function showCard(train: Train, version: number) {
    const view = current();
    const pinned = trainIdentity(train) === selected && !hovered;
    const restoreCloseFocus = (restoreCardFocus && document.activeElement === document.body)
      || popup?.getElement()?.contains(document.activeElement) || panel.contains(document.activeElement);
    const card = markerCard(view, train, pinned ? 'selected' : 'preview', dismiss, () => moveDetails(true));
    card.addEventListener('pointerenter', cancelLeave);
    card.addEventListener('pointerleave', scheduleLeave);
    const viewport = { width: map.getContainer().clientWidth, height: map.getContainer().clientHeight };
    // Leave room for the popup tip and the placement algorithm's 12px edge
    // clearance on both sides, even in the deliberately tiny test viewport.
    card.style.width = `${Math.max(1, Math.min(320, viewport.width - 60))}px`;
    card.style.maxHeight = `${Math.max(1, viewport.height - 60)}px`;
    card.style.overflowY = 'auto';
    const measure = document.createElement('div'); measure.className = 'marker-card-measure'; measure.append(card); map.getContainer().append(measure);
    const size = { width: card.offsetWidth, height: card.offsetHeight };
    measure.remove();
    const location: [number, number] = [train.longitude!, train.latitude!];
    const upcoming = journeyView(view.geometry, train).lines.features.find(feature => feature.properties?.phase === 'upcoming')?.geometry.coordinates ?? [];
    const identity = trainIdentity(train);
    const target = () => markerTarget?.(identity) ?? map.project(location);
    const choose = (allowPan = false) => choosePopupPlacement(target(), upcoming.map(point => map.project(point as [number, number])), size, viewport, allowPan,
      view.geometry.shapes.flatMap(shape => shape.geometry && shape.properties.usable ? [shape.geometry.coordinates.map(point => map.project(point))] : []));
    let placement = choose();
    const live = () => !disposed && !suspended && version === revision;
    if (pinned && !selectedCamera && !manualCamera) selectedCamera = capture();
    if (!placement && !placementAttempted && !manualCamera && size.width <= viewport.width - 24 && size.height <= viewport.height - 24) {
      placementAttempted = true;
      popup?.remove();
      const pan = choose(true);
      if (pan) {
        const center = map.unproject([viewport.width / 2 - pan.pan.x, viewport.height / 2 - pan.pan.y]);
        await changeCamera({ center });
        if (!live()) return;
        await new Promise<void>(resolve => requestAnimationFrame(() => resolve()));
        if (!live()) return;
        // The returned placement already includes the exact screen shift that
        // this camera move applies. Re-running collision placement here can
        // reject it again while React is committing the reprojected marker.
        placement = pan;
      }
    }
    if (!live()) return;
    if (placement) {
      popup ??= new Popup({ closeButton: false, closeOnClick: false, focusAfterOpen: false, maxWidth: 'none', className: 'train-popup' });
      popup.options.anchor = placement.anchor;
      const point = target();
      popup.setOffset([placement.offset.x, placement.offset.y]).setDOMContent(card).setLngLat(map.unproject([point.x, point.y])).addTo(map);
    } else {
      hidePopup();
    }
    restoreCardFocus = false;
    if (restoreCloseFocus) {
      const requested = pendingFocusLabel && card.querySelector<HTMLButtonElement>(`button[aria-label="${pendingFocusLabel}"]`);
      (requested || card.querySelector('button'))?.focus({ preventScroll: true }); pendingFocusLabel = null;
    }
  }

  function refresh() {
    if (disposed) return;
    const view = current();
    if (view.selected !== selected) {
      ++revision;
      detailsBelow = false; pendingFocusLabel = null;
      if (view.selected) {
        if (view.selected === hovered) {
          selectedCamera ??= hoverCamera ?? capture();
          hovered = null; hoverCamera = null; cancelLeave(); clearPreviewElement();
        } else {
          clearHover(); selectedCamera ??= capture(); placementAttempted = false;
        }
        manualCamera = false;
      } else {
        clearHover(); restore(selectedCamera); selectedCamera = null; placementAttempted = false;
      }
      selected = view.selected;
    }
    if (hovered && !view.positions?.trains.some(train => trainIdentity(train) === hovered && train.latitude !== null)) {
      clearHover(); placementAttempted = true;
    }
    if (suspended) return;
    const version = ++revision;
    stopProgress();
    visibility(false);
    const identity = hovered ?? selected;
    const train = compatible(view.geometry, view.positions) ? view.positions?.trains.find(item => trainIdentity(item) === identity && item.latitude !== null) : undefined;
    const selectedTrain = compatible(view.geometry, view.positions) ? view.positions?.trains.find(item => trainIdentity(item) === selected && item.latitude !== null) : undefined;
    if (selected && !selectedTrain) detailsBelow = false;
    renderPanel(view, selectedTrain);
    const journey = train ? journeyView(view.geometry, train) : null;
    if (!train || (detailsBelow && !hovered)) { hidePopup(); if (!train) restoreCardFocus = false; }
    const source = map.getSource('selected-route') as GeoJSONSource | undefined;
    if (source !== journeySource) { journeySource = source; sourceQueue = Promise.resolve(); }
    sourceQueue = sourceQueue.then(async () => {
      if (disposed || suspended || version !== revision || !map.getSource('selected-route') || !map.getSource('selected-progress')) return;
      await Promise.all([
        (map.getSource('selected-route') as GeoJSONSource).setData(journey?.lines ?? empty),
        (map.getSource('selected-progress') as GeoJSONSource).setData(journey?.nextLeg ?? empty),
        (map.getSource('selected-stops') as GeoJSONSource).setData(journey?.stops ?? empty),
      ]);
      if (disposed || suspended || version !== revision) return;
      const active = !!journey?.lines.features.length;
      map.setPaintProperty('route-line', 'line-opacity', active ? 0.2 : 1);
      map.setPaintProperty('route-casing', 'line-opacity', active ? 0.25 : 1);
      map.setPaintProperty('station-circles', 'circle-opacity', active ? 0.35 : 1);
      map.setPaintProperty('station-circles', 'circle-stroke-opacity', active ? 0.35 : 1);
      visibility(active);
      if (train && journey?.nextLeg.features.length) startProgress(train, version);
      if (train && (!detailsBelow || !!hovered)) void showCard(train, version);
    }).catch(() => { if (!disposed && version === revision) { hideCard(); visibility(false); } });
  }

  function onClick(event: { originalEvent: MouseEvent }) {
    if ((event.originalEvent.target as HTMLElement)?.closest('.marker-card')) return;
    dismiss();
  }
  function onKeyDown(event: KeyboardEvent) {
    if (!event.defaultPrevented && event.key === 'Escape' && (hovered || selected)) dismiss();
  }
  function onMoveStart(event: { type: string; trainInspection?: boolean }) {
    if (event.trainInspection) return;
    ++revision; hoverCamera = null; selectedCamera = null; placementAttempted = true; manualCamera = true;
    popup?.remove();
  }
  function onMoveEnd(event: { type: string; trainInspection?: boolean }) {
    if (!event.trainInspection && !suspended) refresh();
  }
  map.on('click', onClick);
  map.on('movestart', onMoveStart); map.on('moveend', onMoveEnd);
  document.addEventListener('keydown', onKeyDown);
  return {
    refresh,
    preview(identity: string, element: HTMLElement) {
      if (suspended || map.isMoving() || identity === selected) return;
      cancelLeave(); trigger = element;
      if (identity !== hovered) {
        ++revision; clearHover(); hoverCamera = capture(); hovered = identity;
        placementAttempted = false; manualCamera = false; refresh();
      }
      clearPreviewElement(); previewElement = element; previewElement.dataset.previewed = 'true';
    },
    leave(identity: string) { if (identity === hovered) scheduleLeave(); },
    select(identity: string, element: HTMLElement) {
      if (suspended) return;
      cancelLeave(); trigger = element; current().onSelect(identity);
    },
    suspend() {
      restoreCardFocus ||= !!popup?.getElement()?.contains(document.activeElement) || panel.contains(document.activeElement);
      suspended = true; ++revision; stopProgress(); hideCard(); visibility(false);
    },
    resume() { suspended = false; refresh(); },
    destroy() {
      disposed = true; ++revision; cancelLeave(); clearPreviewElement(); stopProgress(); hideCard();
      map.off('click', onClick); map.off('movestart', onMoveStart); map.off('moveend', onMoveEnd);
      document.removeEventListener('keydown', onKeyDown);
    },
  };
}
