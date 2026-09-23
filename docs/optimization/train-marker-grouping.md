# Superseded Train Marker Grouping Reference

> Status: inactive as of 2026-09-22. The user found the count/counter marker
> and its chooser confusing and visually disliked it. The runtime
> implementation is removed; individual markers may overlap. Do not use this
> reference to restore that pattern unless the user explicitly reverses the
> product decision in `WEBAPP_ARCHITECTURE_PLAN.md`.

The train list and keyboard navigation remain available when one overlapping
marker prevents pointer access to another. That known pointer-reachability
limitation is accepted for now. HUP-16 remains deferred; any later
decluttering work must not reintroduce count/counter badges or their chooser
without an explicit reversal.

## Historical pure implementation

This is the former `web/src/trainGrouping.ts` implementation, retained after
its runtime removal as a record of the collision logic. It is not active code
or restoration guidance.

```ts
import type { Point, Size } from './popupPlacement';

export const trainHitSize = 44;
export type LocatedTrain = Point & { identity: string };
export type TrainGroup<T extends LocatedTrain> = Point & { identity: string; members: T[] };

/** Group interactive footprints, independently of visual badge size or zoom. */
export function groupTrains<T extends LocatedTrain>(trains: T[], viewport: Size, hitSize = trainHitSize): TrainGroup<T>[] {
  const half = hitSize / 2;
  const visible = trains.filter(train => Number.isFinite(train.x) && Number.isFinite(train.y)
    && train.x >= half && train.y >= half && train.x <= viewport.width - half && train.y <= viewport.height - half)
    .sort((a, b) => a.identity < b.identity ? -1 : a.identity > b.identity ? 1 : 0);
  const unseen = new Set(visible);
  const groups: TrainGroup<T>[] = [];
  // ponytail: quadratic at current route scale; use spatial buckets if measured
  // layout P95 exceeds 2ms (docs/optimization/train-marker-grouping.md).
  for (const anchor of visible) {
    if (!unseen.delete(anchor)) continue;
    const members = [anchor];
    for (let index = 0; index < members.length; index++) {
      const member = members[index]!;
      for (const candidate of unseen) {
        if (Math.abs(member.x - candidate.x) < hitSize && Math.abs(member.y - candidate.y) < hitSize) {
          unseen.delete(candidate); members.push(candidate);
        }
      }
    }
    groups.push({ identity: anchor.identity, x: anchor.x, y: anchor.y, members });
  }
  return groups;
}
```
