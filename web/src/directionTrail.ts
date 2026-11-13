type Point = { x: number; y: number };

// Distances are screen pixels along the visited, ordered trip shape.
export function directionTrail(path: [number, number][], marker: Point, project: (coordinate: [number, number]) => Point): Point[] {
  const last = path.at(-1);
  if (!last || !Number.isFinite(marker.x) || !Number.isFinite(marker.y)) return [];
  let from = project(last);
  if (Math.hypot(from.x - marker.x, from.y - marker.y) > 8) return [];

  const targets = [18, 26, 34];
  const dots: Point[] = [];
  let distance = 0;
  for (let i = path.length - 1; i > 0; i--) {
    const to = project(path[i - 1]!);
    const length = Math.hypot(to.x - from.x, to.y - from.y);
    if (!Number.isFinite(length)) return [];
    if (length === 0) { from = to; continue; }
    while (dots.length < targets.length && targets[dots.length]! <= distance + length) {
      const fraction = (targets[dots.length]! - distance) / length;
      dots.push({ x: from.x + (to.x - from.x) * fraction, y: from.y + (to.y - from.y) * fraction });
    }
    if (dots.length === targets.length) return dots;
    distance += length;
    from = to;
  }
  return [];
}
