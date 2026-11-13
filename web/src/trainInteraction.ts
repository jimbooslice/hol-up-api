import { Popup, type Map as RouteMap, type GeoJSONSource, type EaseToOptions } from 'maplibre-gl';
import type { RouteGeometry } from './geometry';
import { compatible, directionText, journeyView, routeStyle, trainIdentity, type Positions, type Train } from './positions';
import { chooseInspectionLayout, chooseLabelPlacement, choosePopupPlacement, inspectionRectClear, type InspectionLayout, type InspectionScene,
  type LabelSize, type PopupFootprint, type Rect } from './popupPlacement';

const layers = ['selected-route-casing', 'selected-route-line', 'selected-next-halo', 'selected-progress', 'selected-stations'];
const empty = { type: 'FeatureCollection' as const, features: [] };
const clock = new Intl.DateTimeFormat('en-US', { timeZone: 'America/New_York', hour: 'numeric', minute: '2-digit', timeZoneName: 'short' });
export type TrainInteractionView = {
  geometry: RouteGeometry;
  positions: Positions | null;
  pinned: string[];
  active: string | null;
  pendingReplacement: string | null;
  desktop: boolean;
  labelMode: 'terminal' | 'next';
  onToggle: (identity: string, trigger?: HTMLElement) => void;
  onActivate: (identity: string) => void;
  onRemove: (identity: string) => void;
  onClear: () => void;
};
type Camera = { center: [number, number]; zoom: number; bearing: number; pitch: number };
type CardMode = 'preview' | 'selected' | 'panel' | 'dock-active' | 'dock-inactive' | 'dock-preview' | 'dock-compact';

function detail(list: HTMLDListElement, label: string, value: string) {
  const term = document.createElement('dt'); term.textContent = label;
  const description = document.createElement('dd'); description.textContent = value;
  list.append(term, description);
}

function syncPanel(panel: HTMLElement) {
  const parent = panel.parentElement;
  if (!parent?.classList.contains('marker-details-panel')) return;
  parent.hidden = [...parent.children].every(child => (child as HTMLElement).hidden);
}

function hidePanel(panel: HTMLElement) {
  panel.replaceChildren(); panel.hidden = true; syncPanel(panel);
}

function markerCard(view: TrainInteractionView, train: Train, mode: CardMode, close?: () => void, move?: () => void) {
  const docked = mode.startsWith('dock-');
  const compact = mode === 'dock-inactive' || mode === 'dock-compact';
  const root = document.createElement('section'); root.className = 'marker-card'; root.dataset.identity = trainIdentity(train);
  root.dataset.state = mode === 'selected' || mode === 'panel' ? 'selected'
    : mode === 'dock-active' ? 'active' : mode === 'dock-inactive' || mode === 'dock-compact' ? 'inactive' : 'preview';
  if (compact) root.dataset.compact = 'true';
  const header = document.createElement('div'); header.className = 'marker-card-header';
  const badge = document.createElement('span'); badge.className = 'marker-card-badge'; badge.textContent = train.route_id;
  const style = routeStyle(train.route_id); badge.style.background = style.color; badge.style.color = style.text;
  root.style.setProperty('--route-color', style.color);
  const title = document.createElement('strong');
  const journey = compatible(view.geometry, view.positions) ? journeyView(view.geometry, train) : null;
  title.textContent = `${train.route_id} to ${journey?.destination ?? 'destination unavailable'}`;
  header.append(badge, title);
  if (mode === 'selected') {
    const pin = document.createElement('button'); pin.type = 'button'; pin.textContent = '⇩'; pin.title = 'Pin details below map'; pin.setAttribute('aria-label', pin.title);
    pin.addEventListener('click', event => { event.stopPropagation(); move?.(); }); header.append(pin);
  }
  if (mode === 'selected' || mode === 'panel') {
    const button = document.createElement('button'); button.type = 'button'; button.textContent = 'Close'; button.setAttribute('aria-label', 'Close train details');
    button.addEventListener('click', event => { event.stopPropagation(); close?.(); }); header.append(button);
  }
  if (mode === 'panel') {
    const show = document.createElement('button'); show.type = 'button'; show.textContent = 'Show details on map'; show.setAttribute('aria-label', show.textContent);
    show.addEventListener('click', event => { event.stopPropagation(); move?.(); }); header.insertBefore(show, header.lastElementChild);
  }
  root.append(header);
  const state = document.createElement('p'); state.className = 'marker-card-state';
  state.textContent = mode === 'dock-active' ? 'Active journey' : mode === 'dock-preview' ? 'Previewing journey'
    : docked ? 'Pinned train' : mode === 'preview' ? 'Trip preview · Click or tap to pin' : 'Selected train';
  root.append(state);
  const list = document.createElement('dl');
  if (!compact) {
    detail(list, 'Direction', directionText(train.direction, journey?.destination ?? null));
    detail(list, 'Status', train.position_basis === 'realtime_prediction' ? 'Realtime estimate · not GPS' : 'Schedule estimate · not realtime');
    const updated = train.observation_time ?? train.source_timestamp;
    const age = train.observation_age_seconds ?? train.source_age_seconds;
    const relativeAge = age === null ? 'age unavailable' : age < 0 ? `source clock ${Math.ceil(-age)}s ahead` : age < 60 ? `${Math.floor(age)}s ago` : `${Math.floor(age / 60)}m ago`;
    detail(list, 'Last update', updated ? `${clock.format(new Date(updated))} · ${relativeAge}` : 'Unavailable');
  }
  detail(list, 'Next station', journey?.nextStation ?? 'Unavailable');
  if (!compact) {
    const projected = train.projected_at ?? view.positions?.projected_at;
    const delta = train.next_arrival_at && projected ? Date.parse(train.next_arrival_at) - Date.parse(projected) : NaN;
    const relativeArrival = Number.isFinite(delta) ? delta <= 0 ? ' · due' : ` · in ${Math.max(1, Math.ceil(delta / 60_000))} min` : '';
    detail(list, 'Estimated arrival', train.next_arrival_at ? clock.format(new Date(train.next_arrival_at)) + relativeArrival : 'Arrival unavailable');
  }
  root.append(list);
  if (docked) {
    const controls = document.createElement('div'); controls.className = 'marker-card-controls';
    const show = document.createElement('button'); show.type = 'button'; show.textContent = 'Show journey'; show.dataset.action = 'show';
    show.disabled = view.active === trainIdentity(train) && mode !== 'dock-preview';
    show.addEventListener('click', event => { event.stopPropagation(); view.onActivate(trainIdentity(train)); });
    const remove = document.createElement('button'); remove.type = 'button'; remove.textContent = 'Remove'; remove.dataset.action = 'remove';
    remove.addEventListener('click', event => { event.stopPropagation(); view.onRemove(trainIdentity(train)); });
    controls.append(show, remove); root.append(controls);
  }
  return root;
}

export function renderTrainDock(dock: HTMLElement, view: TrainInteractionView, hovered: string | null = null, hoveredFromDock = false,
  preview: (identity: string, element: HTMLElement, source?: 'marker' | 'dock') => boolean = () => false,
  leave: (identity: string) => void = () => {}) {
  const focusedCard = dock.contains(document.activeElement) ? (document.activeElement as HTMLElement).closest<HTMLElement>('.marker-card') : null;
  const focusedIdentity = focusedCard?.dataset.identity;
  const focusedAction = (document.activeElement as HTMLElement | null)?.dataset.action;
  dock.replaceChildren();
  if (!view.desktop || view.pinned.length === 0) { dock.hidden = true; syncPanel(dock); return; }
  const heading = document.createElement('div'); heading.className = 'train-dock-heading';
  const title = document.createElement('h3'); title.textContent = 'Pinned trains';
  const clear = document.createElement('button'); clear.type = 'button'; clear.textContent = 'Clear all'; clear.addEventListener('click', view.onClear);
  heading.append(title, clear); dock.append(heading);
  for (const identity of view.pinned) {
    const train = view.positions?.trains.find(item => trainIdentity(item) === identity);
    if (!train) continue;
    const mode: CardMode = hovered
      ? hoveredFromDock && hovered === identity ? 'dock-preview' : 'dock-compact'
      : view.active === identity ? 'dock-active' : 'dock-inactive';
    const card = markerCard(view, train, mode);
    if (identity !== view.active) {
      card.addEventListener('pointerenter', event => { if ((event as PointerEvent).pointerType !== 'touch') preview(identity, card, 'dock'); });
      card.addEventListener('pointerleave', () => leave(identity));
      card.addEventListener('focusin', () => preview(identity, card, 'dock'));
      card.addEventListener('focusout', event => { if (!card.contains(event.relatedTarget as Node | null)) leave(identity); });
    }
    dock.append(card);
  }
  dock.hidden = false; syncPanel(dock);
  if (focusedIdentity && focusedAction) {
    const card = [...dock.querySelectorAll<HTMLElement>('.marker-card')].find(item => item.dataset.identity === focusedIdentity);
    card?.querySelector<HTMLElement>(`[data-action="${focusedAction}"]`)?.focus({ preventScroll: true });
  }
}

// MapLibre owns transient previews and route highlighting; the persistent pin
// state remains in React so polling and responsive changes are pure transitions.
export function createTrainInteraction(map: RouteMap, panel: HTMLElement, dock: HTMLElement, current: () => TrainInteractionView) {
  let hovered: string | null = null;
  let hoveredFromDock = false;
  let selected = current().active;
  let desktop = current().desktop;
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
  let previewMarker: HTMLElement | null = null;
  let progressFrame: number | null = null;
  let nextStopCoordinate: [number, number] | null = null;
  let journeyIdentity: string | null = null;
  let journeyLines: [number, number][][] = [];
  let journeyNextLeg: [number, number][][] = [];
  let floatingTrain: Train | null = null;
  let floatingCard: HTMLElement | null = null;
  let lastLayout: InspectionLayout | null = null;
  let tooltipFrame: number | null = null;
  let automaticMotion = false, automaticZoomAttempted = false;
  let suppressCameraLeave = false;
  let dismissedPreview: string | null = null;
  const nextStopLabel = document.createElement('span');
  nextStopLabel.className = 'next-stop-label'; nextStopLabel.hidden = true; nextStopLabel.setAttribute('aria-hidden', 'true');
  map.getContainer().append(nextStopLabel);
  const concealTooltips = () => {
    popup?.addClassName('train-popup-pending'); nextStopLabel.style.visibility = 'hidden';
  };
  const localRect = (element: HTMLElement): Rect => {
    const bounds = element.getBoundingClientRect(), container = map.getContainer(), origin = container.getBoundingClientRect();
    return { left: bounds.left - origin.left - container.clientLeft, right: bounds.right - origin.left - container.clientLeft,
      top: bounds.top - origin.top - container.clientTop, bottom: bounds.bottom - origin.top - container.clientTop };
  };
  const measureLabels = (): LabelSize[] => {
    if (!nextStopCoordinate) return [];
    nextStopLabel.hidden = false; nextStopLabel.style.visibility = 'hidden';
    nextStopLabel.style.left = '0px'; nextStopLabel.style.top = '0px';
    return [...new Set([220, 160, 120, 96].map(width => Math.min(width, map.getContainer().clientWidth - 24)))].map(maxWidth => {
      nextStopLabel.style.maxWidth = `${maxWidth}px`;
      const rect = nextStopLabel.getBoundingClientRect();
      return { width: rect.width, height: rect.height, maxWidth };
    });
  };
  const sceneFor = (train: Train, popups: PopupFootprint[], labels: LabelSize[]): InspectionScene => {
    const marker = map.project([train.longitude!, train.latitude!]), container = map.getContainer();
    const point = nextStopCoordinate && map.project(nextStopCoordinate);
    const nextStop = point && point.x >= 12 && point.y >= 12 && point.x <= container.clientWidth - 12 && point.y <= container.clientHeight - 12 ? point : null;
    const margin = (layer: string, fallback: number) => {
      const width = map.getPaintProperty(layer, 'line-width');
      return (typeof width === 'number' ? width : fallback) / 2 + 2;
    };
    const lines = [...journeyLines.map(line => ({ points: line.map(coordinate => map.project(coordinate)), margin: margin('selected-route-casing', 10) })),
      ...journeyNextLeg.map(line => ({ points: line.map(coordinate => map.project(coordinate)), margin: margin('selected-next-halo', 16) }))];
    const upcoming = journeyView(current().geometry, train).lines.features.find(feature => feature.properties?.phase === 'upcoming')?.geometry.coordinates;
    const next = upcoming?.slice(1).map(coordinate => map.project(coordinate as [number, number])).find(point => point.x !== marker.x || point.y !== marker.y);
    return { marker, nextStop, viewport: { width: container.clientWidth, height: container.clientHeight }, popups, labels, lines,
      direction: next ? { x: next.x - marker.x, y: next.y - marker.y } : { x: 0, y: 1 } };
  };
  function layoutNextStopLabel() {
    const train = current().positions?.trains.find(train => trainIdentity(train) === journeyIdentity && train.latitude !== null);
    if (!train || !nextStopCoordinate || suspended) { nextStopLabel.hidden = true; return; }
    const labels = measureLabels(), scene = sceneFor(train, [], labels);
    if (!scene.nextStop) { nextStopLabel.hidden = true; return; }
    for (const size of labels) {
      const marker = { left: scene.marker.x - 22, right: scene.marker.x + 22, top: scene.marker.y - 22, bottom: scene.marker.y + 22 };
      const placement = chooseLabelPlacement(scene.nextStop, size, scene.viewport, [marker], scene.lines.map(line => line.points));
      if (!placement || !inspectionRectClear(placement, scene)) continue;
      nextStopLabel.style.maxWidth = `${size.maxWidth}px`; nextStopLabel.style.left = `${placement.left}px`; nextStopLabel.style.top = `${placement.top}px`;
      nextStopLabel.dataset.side = placement.side; nextStopLabel.hidden = false; nextStopLabel.style.visibility = 'visible'; return;
    }
    nextStopLabel.hidden = true;
  }
  function scheduleInspectionLayout() {
    if (disposed) return;
    concealTooltips();
    if (tooltipFrame !== null) return;
    tooltipFrame = requestAnimationFrame(() => {
      tooltipFrame = null;
      if (disposed || suspended || automaticMotion || map.isMoving()) return;
      if (floatingTrain && floatingCard) void layoutFloatingCard(revision);
      else layoutNextStopLabel();
    });
  }
  map.on('move', scheduleInspectionLayout); map.on('resize', scheduleInspectionLayout);
  const tooltipObserver = new ResizeObserver(scheduleInspectionLayout);
  tooltipObserver.observe(nextStopLabel);
  // Stylesheet/font-size changes can precede ResizeObserver delivery by a frame.
  const styleObserver = new MutationObserver(scheduleInspectionLayout);
  styleObserver.observe(document.head, { childList: true, subtree: true, characterData: true });
  styleObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['style', 'class'] });
  styleObserver.observe(map.getContainer(), { attributes: true, attributeFilter: ['style', 'class'] });
  document.fonts.ready.then(() => { if (!disposed) scheduleInspectionLayout(); });
  const capture = (): Camera => ({ center: map.getCenter().toArray(), zoom: map.getZoom(), bearing: map.getBearing(), pitch: map.getPitch() });
  const visibility = (visible: boolean) => { for (const layer of layers) if (map.getLayer(layer)) map.setLayoutProperty(layer, 'visibility', visible ? 'visible' : 'none'); };
  const hidePopup = () => {
    if (popup?.getElement()) tooltipObserver.unobserve(popup.getElement());
    popup?.remove(); floatingTrain = null; floatingCard = null; lastLayout = null;
  };
  const hideCard = () => { hidePopup(); hidePanel(panel); };
  const restore = (camera: Camera | null) => {
    if (!camera || disposed) return;
    const center = map.getCenter();
    if (Math.abs(center.lng - camera.center[0]) < 1e-9 && Math.abs(center.lat - camera.center[1]) < 1e-9
      && Math.abs(map.getZoom() - camera.zoom) < 1e-9) return;
    if (desktop) map.easeTo({ ...camera, duration: matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 300 }, { trainInspection: true });
    else map.jumpTo(camera, { trainInspection: true });
  };
  const cancelLeave = () => clearTimeout(leaveTimer);
  const clearPreviewElement = () => {
    previewElement?.removeAttribute('data-previewed'); previewMarker?.removeAttribute('data-previewed');
    previewElement = null; previewMarker = null;
  };
  const stopPulse = () => {
    if (progressFrame !== null) cancelAnimationFrame(progressFrame);
    progressFrame = null;
    if (map.getLayer('selected-progress')) map.setPaintProperty('selected-progress', 'line-opacity', 0);
    if (map.getLayer('selected-stations')) map.setPaintProperty('selected-stations', 'circle-stroke-opacity',
      ['case', ['boolean', ['get', 'next'], false], 1, 0.55]);
  };
  const startPulse = (version: number) => {
    const paint = (opacity: number) => {
      map.setPaintProperty('selected-progress', 'line-opacity', opacity);
      map.setPaintProperty('selected-stations', 'circle-stroke-opacity', ['case', ['boolean', ['get', 'next'], false], opacity, 0.55]);
    };
    if (matchMedia('(prefers-reduced-motion: reduce)').matches) { paint(1); return; }
    paint(0.55);
    let started: number | undefined;
    const frame = (now: number) => {
      if (disposed || suspended || version !== revision || !map.getLayer('selected-progress')) { progressFrame = null; return; }
      started ??= now;
      paint(0.55 + 0.45 * (1 - Math.cos(2 * Math.PI * (now - started) / 1600)) / 2);
      progressFrame = requestAnimationFrame(frame);
    };
    progressFrame = requestAnimationFrame(frame);
  };
  const clearHover = (restoreCamera = true) => {
    cancelLeave(); clearPreviewElement(); hovered = null; hoveredFromDock = false;
    const camera = hoverCamera; hoverCamera = null;
    automaticZoomAttempted = false; suppressCameraLeave = false;
    if (automaticMotion) { automaticMotion = false; map.stop(); }
    if (restoreCamera) restore(camera);
  };
  const dismissMobile = () => {
    const hadCardFocus = popup?.getElement()?.contains(document.activeElement) || panel.contains(document.activeElement);
    ++revision; clearHover(); restore(selectedCamera); selectedCamera = null;
    placementAttempted = false; restoreCardFocus = false; detailsBelow = false; pendingFocusLabel = null; hideCard();
    if (hadCardFocus) (trigger?.isConnected ? trigger : map.getCanvas()).focus({ preventScroll: true });
    if (selected) current().onRemove(selected); else refresh();
  };
  const dismissPreview = () => { if (!hovered) return; dismissedPreview = hovered; ++revision; clearHover(); placementAttempted = false; hideCard(); refresh(); };
  const scheduleLeave = () => {
    if (automaticMotion || suppressCameraLeave) return;
    cancelLeave();
    leaveTimer = setTimeout(() => { if (hovered) { ++revision; clearHover(); placementAttempted = true; refresh(); } }, 150);
  };
  const changeCamera = (options: EaseToOptions, duration = 180) => new Promise<void>(resolve => {
    const done = () => { map.off('moveend', done); resolve(); };
    map.once('moveend', done);
    map.easeTo({ ...options, duration: matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : duration }, { trainInspection: true });
    if (!map.isMoving()) done();
  });
  const moveDetails = (below: boolean) => {
    detailsBelow = below; restoreCardFocus = true; placementAttempted = false;
    pendingFocusLabel = below ? 'Show details on map' : 'Pin details below map';
    if (below) restore(selectedCamera);
    else { manualCamera = false; selectedCamera ??= capture(); }
    ++revision; refresh();
  };
  function renderMobilePanel(view: TrainInteractionView, train: Train | undefined) {
    const active = panel.contains(document.activeElement) ? document.activeElement as HTMLElement : null;
    const focusLabel = pendingFocusLabel ?? active?.closest('button')?.getAttribute('aria-label');
    hidePanel(panel);
    if (!detailsBelow || !train) return;
    panel.append(markerCard(view, train, 'panel', dismissMobile, () => moveDetails(false)));
    panel.hidden = false; syncPanel(panel);
    if ((restoreCardFocus || active) && focusLabel) {
      panel.querySelector<HTMLButtonElement>(`[aria-label="${focusLabel}"]`)?.focus({ preventScroll: true });
      restoreCardFocus = false; pendingFocusLabel = null;
    }
  }
  async function showCard(train: Train, version: number) {
    const view = current();
    const pinned = trainIdentity(train) === selected && !hovered;
    const restoreCloseFocus = !view.desktop && ((restoreCardFocus && document.activeElement === document.body)
      || popup?.getElement()?.contains(document.activeElement) || panel.contains(document.activeElement));
    const card = markerCard(view, train, view.desktop || !pinned ? 'preview' : 'selected', dismissMobile, () => moveDetails(true));
    card.addEventListener('pointerenter', cancelLeave);
    card.addEventListener('pointerleave', scheduleLeave);
    const viewport = { width: map.getContainer().clientWidth, height: map.getContainer().clientHeight };
    card.style.width = `${Math.max(1, Math.min(320, viewport.width - 60))}px`;
    card.style.maxHeight = `${Math.max(1, viewport.height - 60)}px`;
    card.style.overflowY = 'auto';
    if (disposed || suspended || version !== revision) return;
    floatingTrain = train; floatingCard = card;
    concealTooltips();
    popup ??= new Popup({ closeButton: false, closeOnClick: false, focusAfterOpen: false, subpixelPositioning: true,
      maxWidth: 'none', className: 'train-popup train-popup-pending' });
    if (popup.getElement()) tooltipObserver.unobserve(popup.getElement());
    popup.setDOMContent(card).setLngLat([train.longitude!, train.latitude!]).addTo(map);
    tooltipObserver.observe(popup.getElement());
    await layoutFloatingCard(version);
    if (disposed || suspended || version !== revision) return;
    restoreCardFocus = false;
    if (restoreCloseFocus) {
      const requested = pendingFocusLabel && card.querySelector<HTMLButtonElement>(`button[aria-label="${pendingFocusLabel}"]`);
      (requested || card.querySelector('button'))?.focus({ preventScroll: true }); pendingFocusLabel = null;
    }
  }

  function measurePopupFootprints(): PopupFootprint[] {
    if (!popup || !floatingTrain) return [];
    const origin = map.project([floatingTrain.longitude!, floatingTrain.latitude!]);
    return (['bottom', 'left', 'right', 'top', 'bottom-left', 'bottom-right', 'top-left', 'top-right'] as const).map(anchor => {
      popup!.options.anchor = anchor; popup!.setOffset([0, 0]);
      const rect = localRect(popup!.getElement());
      return { anchor, rect: { left: rect.left - origin.x, right: rect.right - origin.x, top: rect.top - origin.y, bottom: rect.bottom - origin.y } };
    });
  }

  function phoneLayout(scene: InspectionScene, allowPan: boolean): InspectionLayout {
    const size = { width: Math.max(...scene.popups.map(item => item.rect.right - item.rect.left)),
      height: Math.max(...scene.popups.map(item => item.rect.bottom - item.rect.top)) };
    const placement = choosePopupPlacement(scene.marker, scene.lines.flatMap(line => line.points), size, scene.viewport, allowPan);
    return { popup: placement, label: null, labelWidth: null, zoomDelta: 0, pan: placement?.pan ?? { x: 0, y: 0 } };
  }

  async function layoutFloatingCard(version: number) {
    const train = floatingTrain, card = floatingCard;
    if (!train || !card || !popup || disposed || suspended || automaticMotion) return;
    concealTooltips();
    const live = () => !disposed && !suspended && version === revision && floatingTrain === train;
    let scene = sceneFor(train, measurePopupFootprints(), measureLabels());
    const zoomLimit = desktop && hovered && !automaticZoomAttempted && !manualCamera && scene.nextStop
      ? Math.max(0, Math.min(1, map.getMaxZoom() - map.getZoom(), (hoverCamera?.zoom ?? map.getZoom()) + 1 - map.getZoom())) : 0;
    let layout = chooseInspectionLayout(scene, zoomLimit, lastLayout);
    if (zoomLimit) automaticZoomAttempted = true;
    if (!desktop && !layout.popup && !placementAttempted && !manualCamera) {
      placementAttempted = true; layout = phoneLayout(scene, true);
      if (selected && !selectedCamera) selectedCamera = capture();
    } else if (!desktop && !layout.popup) layout = phoneLayout(scene, false);
    if (layout.popup && (layout.zoomDelta || layout.pan.x || layout.pan.y)) {
      const focus = scene.nextStop ? { x: (scene.marker.x + scene.nextStop.x) / 2, y: (scene.marker.y + scene.nextStop.y) / 2 } : scene.marker;
      const center = map.project(map.getCenter()), scale = 2 ** layout.zoomDelta;
      const target = map.unproject([focus.x + (center.x - focus.x - layout.pan.x) / scale,
        focus.y + (center.y - focus.y - layout.pan.y) / scale]);
      if (desktop) hoverCamera ??= capture();
      automaticMotion = true; suppressCameraLeave = true;
      try { await changeCamera({ center: target, zoom: map.getZoom() + layout.zoomDelta }, desktop ? 300 : 180); }
      finally { automaticMotion = false; }
      if (!live()) { scheduleInspectionLayout(); return; }
      scene = sceneFor(train, measurePopupFootprints(), measureLabels());
      layout = desktop ? chooseInspectionLayout(scene, 0) : phoneLayout(scene, false);
    }
    if (!live()) return;
    for (let attempt = 0; attempt < 2 && layout.popup; attempt++) {
      popup.options.anchor = layout.popup.anchor;
      popup.setOffset([layout.popup.offset.x, layout.popup.offset.y]).setLngLat([train.longitude!, train.latitude!]);
      const actualPopup = localRect(popup.getElement());
      const phoneFit = !desktop && actualPopup.left >= 11 && actualPopup.top >= 11
        && actualPopup.right <= scene.viewport.width - 11 && actualPopup.bottom <= scene.viewport.height - 11;
      if (inspectionRectClear(actualPopup, scene) || phoneFit) {
        if (!desktop && scene.nextStop && !layout.label) {
          for (const size of scene.labels) {
            const label = chooseLabelPlacement(scene.nextStop, size, scene.viewport, [actualPopup], scene.lines.map(line => line.points));
            if (label && inspectionRectClear(label, scene, [actualPopup])) { layout = { ...layout, label, labelWidth: size.maxWidth }; break; }
          }
        }
        if (layout.label && layout.labelWidth) {
          nextStopLabel.style.maxWidth = `${layout.labelWidth}px`;
          nextStopLabel.style.left = `${layout.label.left}px`; nextStopLabel.style.top = `${layout.label.top}px`;
          nextStopLabel.dataset.side = layout.label.side; nextStopLabel.hidden = false;
          if (inspectionRectClear(localRect(nextStopLabel), scene, [actualPopup])) nextStopLabel.style.visibility = 'visible';
          else { nextStopLabel.hidden = true; layout = { ...layout, label: null, labelWidth: null }; }
        } else nextStopLabel.hidden = true;
        if (desktop) hidePanel(panel);
        lastLayout = layout;
        popup.removeClassName('train-popup-pending'); return;
      }
      scene = sceneFor(train, measurePopupFootprints(), measureLabels());
      layout = chooseInspectionLayout(scene, 0);
    }
    hidePopup(); layoutNextStopLabel();
    if (desktop) { panel.replaceChildren(card); panel.hidden = false; syncPanel(panel); }
  }
  function refresh() {
    if (disposed) return;
    const view = current();
    if (view.desktop !== desktop) {
      ++revision; hoverCamera = null; selectedCamera = null; clearHover(false); hideCard(); detailsBelow = false; desktop = view.desktop;
    }
    if (view.pendingReplacement && hovered) clearHover();
    if (view.active !== selected) {
      ++revision; detailsBelow = false; pendingFocusLabel = null;
      if (!view.desktop) {
        if (view.active) {
          if (view.active === hovered) {
            selectedCamera ??= hoverCamera ?? capture(); hovered = null; hoveredFromDock = false; hoverCamera = null; cancelLeave(); clearPreviewElement();
          } else { clearHover(); selectedCamera ??= capture(); placementAttempted = false; }
          manualCamera = false;
        } else { clearHover(); restore(selectedCamera); selectedCamera = null; placementAttempted = false; }
      } else clearHover();
      selected = view.active;
    }
    if (hovered && !view.positions?.trains.some(train => trainIdentity(train) === hovered && (hoveredFromDock || train.latitude !== null))) {
      clearHover(); placementAttempted = true;
    }
    renderTrainDock(dock, view, hovered, hoveredFromDock, preview, leave);
    if (suspended) return;
    const version = ++revision;
    concealTooltips();
    stopPulse(); visibility(false); nextStopCoordinate = null; journeyIdentity = null; journeyLines = []; journeyNextLeg = []; nextStopLabel.hidden = true;
    const identity = hovered ?? selected;
    const train = compatible(view.geometry, view.positions)
      ? view.positions?.trains.find(item => trainIdentity(item) === identity && item.latitude !== null) : undefined;
    const selectedTrain = !view.desktop && compatible(view.geometry, view.positions)
      ? view.positions?.trains.find(item => trainIdentity(item) === selected && item.latitude !== null) : undefined;
    if (!view.desktop) {
      if (selected && !selectedTrain) detailsBelow = false;
      renderMobilePanel(view, selectedTrain);
    } else hidePanel(panel);
    const journey = train ? journeyView(view.geometry, train) : null;
    journeyIdentity = train ? identity : null;
    journeyLines = journey?.lines.features.map(feature => feature.geometry.coordinates as [number, number][]) ?? [];
    journeyNextLeg = journey?.nextLeg.features.map(feature => feature.geometry.coordinates as [number, number][]) ?? [];
    if (!train || (!view.desktop && detailsBelow && !hovered) || (view.desktop && (!hovered || hoveredFromDock))) {
      hidePopup(); if (!train) restoreCardFocus = false;
    }
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
      const active = !!journey && !!(journey.lines.features.length || journey.nextLeg.features.length || journey.stops.features.length);
      map.setPaintProperty('route-line', 'line-color', active ? '#8d98a8' : routeStyle(view.geometry.route_id).color);
      map.setPaintProperty('route-line', 'line-opacity', active ? 0.2 : 1);
      map.setPaintProperty('route-casing', 'line-opacity', active ? 0.25 : 1);
      map.setPaintProperty('station-circles', 'circle-opacity', active ? 0.35 : 1);
      map.setPaintProperty('station-circles', 'circle-stroke-opacity', active ? 0.35 : 1);
      visibility(active);
      const nextStop = journey?.stops.features.find(feature => feature.properties?.next);
      nextStopCoordinate = active && journey?.nextStation && nextStop ? nextStop.geometry.coordinates as [number, number] : null;
      if (nextStopCoordinate && train) {
        nextStopLabel.textContent = `Next: ${journey!.nextStation}${journey!.destination ? `\nToward ${journey!.destination}` : ''}`;
        nextStopLabel.style.setProperty('--route-color', routeStyle(train.route_id).color);
        nextStopLabel.style.setProperty('--route-text', routeStyle(train.route_id).text);
      }
      const floatingCard = !!train && (view.desktop ? !!hovered && !hoveredFromDock : !detailsBelow || !!hovered);
      if (!floatingCard) layoutNextStopLabel();
      if (train && journey?.nextLeg.features.length) startPulse(version);
      if (floatingCard) void showCard(train, version);
    }).catch(() => { if (!disposed && version === revision) { hideCard(); visibility(false); } });
  }
  function onClick(event: { originalEvent: MouseEvent }) {
    if ((event.originalEvent.target as HTMLElement)?.closest('.marker-card')) return;
    if (desktop) dismissPreview(); else dismissMobile();
  }
  function onKeyDown(event: KeyboardEvent) {
    if (event.defaultPrevented || event.key !== 'Escape') return;
    if (desktop && hovered) dismissPreview();
    else if (!desktop && (hovered || selected)) dismissMobile();
  }
  function onMoveStart(event: { type: string; trainInspection?: boolean }) {
    if (event.trainInspection) return;
    ++revision; hoverCamera = null; selectedCamera = null; placementAttempted = true; manualCamera = true;
    automaticMotion = false; automaticZoomAttempted = true; suppressCameraLeave = false; hidePopup();
  }
  function onMoveEnd(event: { type: string; trainInspection?: boolean }) {
    if (!event.trainInspection && !suspended) refresh(); else scheduleInspectionLayout();
  }
  function preview(identity: string, element: HTMLElement, source: 'marker' | 'dock' = 'marker') {
    const view = current();
    if (suspended || identity === dismissedPreview || (source === 'marker' && map.isMoving()) || identity === view.active) return false;
    cancelLeave(); trigger = element;
    if (identity !== hovered || hoveredFromDock !== (source === 'dock')) {
      const camera = hoverCamera;
      ++revision; clearHover(false);
      hoverCamera = view.desktop ? camera ?? capture() : capture();
      lastLayout = null;
      hovered = identity; hoveredFromDock = source === 'dock'; placementAttempted = false; manualCamera = false; refresh();
    }
    clearPreviewElement(); previewElement = element; previewElement.dataset.previewed = 'true';
    previewMarker = [...(map.getContainer().parentElement?.querySelectorAll<HTMLElement>('.train-marker') ?? [])]
      .find(marker => marker.dataset.identity === identity) ?? null;
    previewMarker?.setAttribute('data-previewed', 'true');
    return true;
  }
  function leave(identity: string, force = false) {
    if (force) { suppressCameraLeave = false; if (identity === hovered) dismissPreview(); }
    else if (automaticMotion || suppressCameraLeave) return false;
    else if (identity === hovered) scheduleLeave();
    return true;
  }
  function onPointerMove(event: PointerEvent) {
    dismissedPreview = null;
    if (!suppressCameraLeave || automaticMotion) return;
    suppressCameraLeave = false;
    const target = event.target as HTMLElement;
    if (target.closest('.train-popup') || target.closest<HTMLElement>('.train-marker')?.dataset.identity === hovered) cancelLeave();
    else scheduleLeave();
  }
  map.on('click', onClick); map.on('movestart', onMoveStart); map.on('moveend', onMoveEnd);
  document.addEventListener('keydown', onKeyDown);
  document.addEventListener('pointermove', onPointerMove);
  return {
    refresh, preview, leave,
    select(identity: string, element: HTMLElement) {
      if (suspended) return;
      cancelLeave(); trigger = element; current().onToggle(identity, element);
    },
    suspend() {
      restoreCardFocus ||= !!popup?.getElement()?.contains(document.activeElement) || panel.contains(document.activeElement);
      const previous = lastLayout;
      suspended = true; ++revision; stopPulse(); hideCard(); lastLayout = previous; visibility(false); nextStopLabel.hidden = true;
      renderTrainDock(dock, current(), null, false, preview, leave);
    },
    resume() { suspended = false; refresh(); },
    destroy() {
      disposed = true; ++revision; cancelLeave(); clearPreviewElement(); stopPulse(); hideCard();
      if (tooltipFrame !== null) cancelAnimationFrame(tooltipFrame);
      tooltipObserver.disconnect();
      styleObserver.disconnect();
      map.off('click', onClick); map.off('movestart', onMoveStart); map.off('moveend', onMoveEnd);
      map.off('move', scheduleInspectionLayout); map.off('resize', scheduleInspectionLayout); nextStopLabel.remove();
      document.removeEventListener('keydown', onKeyDown);
      document.removeEventListener('pointermove', onPointerMove);
    },
  };
}
