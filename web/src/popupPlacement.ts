export type Point = { x: number; y: number };
export type Size = { width: number; height: number };
export type PopupAnchor = 'left' | 'right' | 'top' | 'bottom' | 'top-left' | 'top-right' | 'bottom-left' | 'bottom-right';
export type Rect = { left: number; top: number; right: number; bottom: number };
export type Placement = { anchor: PopupAnchor; offset: Point; rect: Rect; pan: Point };
export type LabelPlacement = Rect & { side: 'left' | 'right' | 'top' | 'bottom' | 'top-left' | 'top-right' | 'bottom-left' | 'bottom-right' };

const clearance = 12;
// The arrow tip lands at the outer edge of the emphasized train marker.
const distance = 18;
const labelDistance = 16;
const anchors: { anchor: PopupAnchor; x: number; y: number }[] = [
  { anchor: 'bottom', x: 0, y: -1 },
  { anchor: 'left', x: 1, y: 0 },
  { anchor: 'right', x: -1, y: 0 },
  { anchor: 'top', x: 0, y: 1 },
  { anchor: 'bottom-left', x: 1, y: -1 },
  { anchor: 'bottom-right', x: -1, y: -1 },
  { anchor: 'top-left', x: 1, y: 1 },
  { anchor: 'top-right', x: -1, y: 1 },
];

function intersects(start: Point, end: Point, rect: Rect, margin = clearance): boolean {
  let entering = 0, leaving = 1;
  for (const [origin, delta, lower, upper] of [
    [start.x, end.x - start.x, rect.left - margin, rect.right + margin],
    [start.y, end.y - start.y, rect.top - margin, rect.bottom + margin],
  ]) {
    if (delta === 0) {
      if (origin < lower || origin > upper) return false;
    } else {
      const a = (lower - origin) / delta, b = (upper - origin) / delta;
      entering = Math.max(entering, Math.min(a, b));
      leaving = Math.min(leaving, Math.max(a, b));
      if (entering > leaving) return false;
    }
  }
  return true;
}

export function chooseLabelPlacement(point: Point, label: Size, viewport: Size, obstacles: Rect[] = [], lines: Point[][] = []): LabelPlacement | null {
  if (![point.x, point.y, label.width, label.height, viewport.width, viewport.height].every(Number.isFinite)
    || label.width <= 0 || label.height <= 0 || viewport.width <= 0 || viewport.height <= 0
    || label.width > viewport.width - 2 * clearance || label.height > viewport.height - 2 * clearance) return null;
  const candidates = (point.x > viewport.width / 2
    ? ['left', 'right', 'top', 'bottom', 'top-left', 'bottom-left', 'top-right', 'bottom-right']
    : ['right', 'left', 'top', 'bottom', 'top-right', 'bottom-right', 'top-left', 'bottom-left'])
    .map(side => {
      const left = side.endsWith('right') || side === 'right' ? point.x + labelDistance
        : side.endsWith('left') || side === 'left' ? point.x - labelDistance - label.width : point.x - label.width / 2;
      const top = side.startsWith('bottom') || side === 'bottom' ? point.y + labelDistance
        : side.startsWith('top') || side === 'top' ? point.y - labelDistance - label.height : point.y - label.height / 2;
      return { side, left, top, right: left + label.width, bottom: top + label.height } as LabelPlacement;
    })
    .filter(rect => rect.left >= clearance && rect.top >= clearance && rect.right <= viewport.width - clearance && rect.bottom <= viewport.height - clearance);
  const overlap = (a: Rect, b: Rect) => Math.max(0, Math.min(a.right, b.right + clearance) - Math.max(a.left, b.left - clearance))
    * Math.max(0, Math.min(a.bottom, b.bottom + clearance) - Math.max(a.top, b.top - clearance));
  const routeClear = candidates.filter(candidate => lines.every(line => line.slice(1)
    .every((end, index) => !intersects(line[index]!, end, candidate, 5))));
  return routeClear.find(candidate => obstacles.every(obstacle => overlap(candidate, obstacle) === 0))
    ?? null;
}

export type PopupFootprint = { anchor: PopupAnchor; rect: Rect };
export type LabelSize = Size & { maxWidth: number };
export type InspectionScene = {
  marker: Point; nextStop: Point | null; viewport: Size;
  popups: PopupFootprint[]; labels: LabelSize[];
  lines: { points: Point[]; margin: number }[];
  direction: Point;
};
export type InspectionLayout = {
  popup: Placement | null; label: LabelPlacement | null; labelWidth: number | null;
  zoomDelta: number; pan: Point;
};

export const rectsOverlap = (a: Rect, b: Rect, gap = 8) =>
  a.left < b.right + gap && a.right > b.left - gap && a.top < b.bottom + gap && a.bottom > b.top - gap;
const shiftRect = (rect: Rect, point: Point): Rect => ({ left: rect.left + point.x, right: rect.right + point.x,
  top: rect.top + point.y, bottom: rect.bottom + point.y });
const pointRect = (point: Point, radius: number): Rect => ({ left: point.x - radius, right: point.x + radius,
  top: point.y - radius, bottom: point.y + radius });

export function inspectionRectClear(rect: Rect, scene: InspectionScene, obstacles: Rect[] = []): boolean {
  return rect.left >= clearance && rect.top >= clearance && rect.right <= scene.viewport.width - clearance
    && rect.bottom <= scene.viewport.height - clearance
    && !rectsOverlap(rect, pointRect(scene.marker, 22), 2)
    && (!scene.nextStop || !rectsOverlap(rect, pointRect(scene.nextStop, 12), 2))
    && obstacles.every(obstacle => !rectsOverlap(rect, obstacle))
    && scene.lines.every(line => line.points.slice(1).every((end, index) => !intersects(line.points[index]!, end, rect, line.margin)));
}

/** Two boxes, eight anchors each. Search camera candidates without moving the map. */
// ponytail: finite anchors for two boxes; add candidates if continuous placement becomes necessary.
export function chooseInspectionLayout(scene: InspectionScene, maxZoomDelta = 0, previous?: InspectionLayout | null, allowPan = false): InspectionLayout {
  const emptyLayout: InspectionLayout = { popup: null, label: null, labelWidth: null, zoomDelta: 0, pan: { x: 0, y: 0 } };
  if (![scene.marker.x, scene.marker.y, scene.viewport.width, scene.viewport.height].every(Number.isFinite)) return emptyLayout;
  const focus = scene.nextStop ? { x: (scene.marker.x + scene.nextStop.x) / 2, y: (scene.marker.y + scene.nextStop.y) / 2 } : scene.marker;
  let solo: InspectionLayout | null = null;
  for (let step = 0; step <= Math.floor(Math.min(1, maxZoomDelta) * 10 + 1e-6); step++) {
    const zoomDelta = step / 10, scale = 2 ** zoomDelta;
    const project = (point: Point): Point => ({ x: focus.x + (point.x - focus.x) * scale, y: focus.y + (point.y - focus.y) * scale });
    const scaled: InspectionScene = { ...scene, marker: project(scene.marker), nextStop: scene.nextStop && project(scene.nextStop),
      lines: scene.lines.map(line => ({ ...line, points: line.points.map(project) })) };
    const ordered = [...anchors].sort((a, b) => {
      if (step === 0 && previous?.popup) {
        if (a.anchor === previous.popup.anchor) return -1;
        if (b.anchor === previous.popup.anchor) return 1;
      }
      return (a.x * scene.direction.x + a.y * scene.direction.y) / Math.hypot(a.x, a.y)
        - (b.x * scene.direction.x + b.y * scene.direction.y) / Math.hypot(b.x, b.y);
    });
    let best: InspectionLayout | null = null, bestPan = Infinity;
    for (const candidate of ordered) {
      const footprint = scene.popups.find(popup => popup.anchor === candidate.anchor);
      if (!footprint) continue;
      const offset = { x: candidate.x * 26, y: candidate.y * 26 };
      const popupRect = shiftRect(footprint.rect, { x: scaled.marker.x + offset.x, y: scaled.marker.y + offset.y });
      const sizes = [...scene.labels];
      if (step === 0 && previous?.label) sizes.sort((a, b) =>
        Number(b.maxWidth === previous.labelWidth) - Number(a.maxWidth === previous.labelWidth));
      const labels = scaled.nextStop ? sizes.flatMap(size => {
        const sides = scaled.nextStop!.x > scene.viewport.width / 2
          ? ['left', 'right', 'top', 'bottom', 'top-left', 'bottom-left', 'top-right', 'bottom-right']
          : ['right', 'left', 'top', 'bottom', 'top-right', 'bottom-right', 'top-left', 'bottom-left'];
        if (step === 0 && previous?.label && previous.labelWidth === size.maxWidth) sides.sort((a, b) =>
          Number(b === previous.label!.side) - Number(a === previous.label!.side));
        return sides.map(side => {
          const left = side.endsWith('right') ? scaled.nextStop!.x + labelDistance : side.endsWith('left')
            ? scaled.nextStop!.x - labelDistance - size.width : scaled.nextStop!.x - size.width / 2;
          const top = side.startsWith('bottom') ? scaled.nextStop!.y + labelDistance : side.startsWith('top')
            ? scaled.nextStop!.y - labelDistance - size.height : scaled.nextStop!.y - size.height / 2;
          return { rect: { side, left, top, right: left + size.width, bottom: top + size.height } as LabelPlacement, width: size.maxWidth };
        });
      }) : [];
      for (const label of [...labels, null]) {
        const union = [popupRect, pointRect(scaled.marker, 22), ...(label && scaled.nextStop ? [label.rect, pointRect(scaled.nextStop, 12)] : [])];
        const left = Math.min(...union.map(rect => rect.left)), right = Math.max(...union.map(rect => rect.right));
        const top = Math.min(...union.map(rect => rect.top)), bottom = Math.max(...union.map(rect => rect.bottom));
        const pan = step > 0 || allowPan ? {
          x: Math.max(clearance - left, Math.min(0, scene.viewport.width - clearance - right)),
          y: Math.max(clearance - top, Math.min(0, scene.viewport.height - clearance - bottom)),
        } : { x: 0, y: 0 };
        const panLimit = allowPan ? 0.5 : 0.25;
        if (Math.abs(pan.x) > scene.viewport.width * panLimit || Math.abs(pan.y) > scene.viewport.height * panLimit) continue;
        const shifted: InspectionScene = { ...scaled, marker: { x: scaled.marker.x + pan.x, y: scaled.marker.y + pan.y },
          nextStop: scaled.nextStop && { x: scaled.nextStop.x + pan.x, y: scaled.nextStop.y + pan.y },
          lines: scaled.lines.map(line => ({ ...line, points: line.points.map(point => ({ x: point.x + pan.x, y: point.y + pan.y })) })) };
        const rect = shiftRect(popupRect, pan);
        if (!inspectionRectClear(rect, shifted)) continue;
        const labelRect = label ? { ...label.rect, ...shiftRect(label.rect, pan) } : null;
        if (labelRect && !inspectionRectClear(labelRect, shifted, [rect])) continue;
        const layout: InspectionLayout = { popup: { anchor: candidate.anchor, offset, rect, pan }, label: labelRect,
          labelWidth: label?.width ?? null, zoomDelta, pan };
        if (!label && scene.nextStop) { if (step === 0 && !solo) solo = layout; continue; }
        const amount = Math.hypot(pan.x, pan.y);
        if (amount < bestPan) { best = layout; bestPan = amount; }
        if (amount === 0) return layout;
      }
    }
    if (best) return best;
  }
  return solo ?? emptyLayout;
}

/** Pan is the required screen-space shift; MapLibre panBy takes its negative. */
export function choosePopupPlacement(marker: Point, route: Point[], card: Size, viewport: Size, allowPan = false, obstacles: Point[][] = []): Placement | null {
  const points = [marker, ...route];
  if (![...points, ...obstacles.flat()].every(point => Number.isFinite(point.x) && Number.isFinite(point.y)) ||
    ![card.width, card.height, viewport.width, viewport.height].every(value => Number.isFinite(value) && value > 0) ||
    card.width > viewport.width - 2 * clearance || card.height > viewport.height - 2 * clearance) return null;

  const next = route.find(point => point.x !== marker.x || point.y !== marker.y);
  const direction = next ? { x: next.x - marker.x, y: next.y - marker.y } : { x: 0, y: 1 };
  const ranked = [...anchors].sort((a, b) =>
    (a.x * direction.x + a.y * direction.y) / Math.hypot(a.x, a.y) -
    (b.x * direction.x + b.y * direction.y) / Math.hypot(b.x, b.y));

  for (const panAllowed of allowPan ? [false, true] : [false]) {
    // Prefer a route-clear card, but an in-bounds card is always better than
    // hiding active train details.
    for (const avoidRoutes of [true, false]) for (const candidate of ranked) {
      const { anchor, x, y } = candidate;
      const length = Math.hypot(x, y);
      const offset = { x: x * distance / length, y: y * distance / length };
      const left = marker.x + offset.x - (x === 0 ? card.width / 2 : x < 0 ? card.width : 0);
      const top = marker.y + offset.y - (y === 0 ? card.height / 2 : y < 0 ? card.height : 0);
      const rect = { left, top, right: left + card.width, bottom: top + card.height };
      const pan = { x: 0, y: 0 };
      if (panAllowed) {
        pan.x = Math.max(clearance - rect.left, Math.min(0, viewport.width - clearance - rect.right));
        pan.y = Math.max(clearance - rect.top, Math.min(0, viewport.height - clearance - rect.bottom));
      }
      // Projection and popup positioning round at subpixel boundaries.
      if (Math.abs(pan.x) > viewport.width / 2 || Math.abs(pan.y) > viewport.height / 2 ||
        rect.left + pan.x < clearance - 0.5 || rect.top + pan.y < clearance - 0.5 ||
        rect.right + pan.x > viewport.width - clearance + 0.5 || rect.bottom + pan.y > viewport.height - clearance + 0.5) continue;
      // Moving the map shifts route geometry and card together, preserving intersections.
      const selectedIntersects = points.slice(1).some((point, index) => intersects(points[index]!, point, rect));
      const routeIntersects = obstacles.some(line => line.slice(1).some((point, index) => intersects(line[index]!, point, rect)));
      if (avoidRoutes && (selectedIntersects || routeIntersects)) continue;
      return {
        anchor, offset, pan,
        rect: { left: rect.left + pan.x, top: rect.top + pan.y, right: rect.right + pan.x, bottom: rect.bottom + pan.y },
      };
    }
  }
  return null;
}
