export type Point = { x: number; y: number };
export type Size = { width: number; height: number };
export type PopupAnchor = 'left' | 'right' | 'top' | 'bottom' | 'top-left' | 'top-right' | 'bottom-left' | 'bottom-right';
export type Rect = { left: number; top: number; right: number; bottom: number };
export type Placement = { anchor: PopupAnchor; offset: Point; rect: Rect; pan: Point };

const clearance = 12;
// The arrow tip lands at the outer edge of the emphasized train marker.
const distance = 18;
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

function intersects(start: Point, end: Point, rect: Rect): boolean {
  let entering = 0, leaving = 1;
  for (const [origin, delta, lower, upper] of [
    [start.x, end.x - start.x, rect.left - clearance, rect.right + clearance],
    [start.y, end.y - start.y, rect.top - clearance, rect.bottom + clearance],
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
