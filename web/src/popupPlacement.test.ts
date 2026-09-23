import assert from 'node:assert/strict';
import { test } from 'node:test';
import { choosePopupPlacement } from './popupPlacement.ts';

const marker = { x: 400, y: 300 };
const card = { width: 240, height: 160 };
const viewport = { width: 800, height: 600 };

test('places the card opposite the upcoming direction with matching explicit offsets', () => {
  const placement = choosePopupPlacement(marker, [marker, { x: 400, y: 50 }], card, viewport);
  assert.deepEqual(placement, {
    anchor: 'top', offset: { x: 0, y: 18 }, pan: { x: 0, y: 0 },
    rect: { left: 280, top: 318, right: 520, bottom: 478 },
  });
  const diagonal = choosePopupPlacement(marker, [{ x: 550, y: 150 }], card, viewport)!;
  assert.equal(diagonal.anchor, 'top-right');
  assert.equal(Math.hypot(diagonal.offset.x, diagonal.offset.y), 18);
  assert.equal(diagonal.rect.right, marker.x + diagonal.offset.x);
  assert.equal(diagonal.rect.top, marker.y + diagonal.offset.y);
});

test('checks later route segments and their clearance, including duplicate vertices', () => {
  const route = [marker, marker, { x: 400, y: 50 }, { x: 600, y: 50 }, { x: 600, y: 500 }, { x: 400, y: 500 }, { x: 400, y: 450 }];
  const placement = choosePopupPlacement(marker, route, card, viewport)!;
  assert.notEqual(placement.anchor, 'top');
  assert.ok(placement.rect.right < marker.x);

  // The later horizontal segment misses the card but crosses its 12px clearance.
  const nearRoute = [marker, { x: 400, y: 50 }, { x: 700, y: 50 }, { x: 700, y: 490 }, { x: 200, y: 490 }];
  assert.notEqual(choosePopupPlacement(marker, nearRoute, card, viewport)?.anchor, 'top');
});

test('uses another safe anchor before moving the map', () => {
  const edgeMarker = { x: 400, y: 500 };
  const placement = choosePopupPlacement(edgeMarker, [{ x: 400, y: 50 }], card, viewport, true)!;
  assert.deepEqual(placement.pan, { x: 0, y: 0 });
  assert.notEqual(placement.anchor, 'top');
  assert.ok(placement.rect.bottom <= viewport.height - 12);
});

test('pans only enough to fit a card when every fixed placement is clipped', () => {
  const smallViewport = { width: 300, height: 220 };
  const center = { x: 150, y: 110 };
  assert.equal(choosePopupPlacement(center, [], card, smallViewport), null);
  const placement = choosePopupPlacement(center, [], card, smallViewport, true)!;
  assert.equal(placement.anchor, 'bottom');
  assert.deepEqual(placement.pan, { x: 0, y: 80 });
  assert.deepEqual(placement.rect, { left: 30, top: 12, right: 270, bottom: 172 });
  // Reprojection after panning can land a fraction of a pixel off the target.
  assert.equal(choosePopupPlacement({ x: 150, y: 198 - 1e-9 }, [], card, smallViewport)?.anchor, 'bottom');
});

test('declines impossible placement rather than clipping or exceeding pan bounds', () => {
  assert.equal(choosePopupPlacement(marker, [], { width: 790, height: 160 }, viewport, true), null);
  assert.equal(choosePopupPlacement({ x: -1000, y: -1000 }, [], card, viewport, true), null);
  assert.equal(choosePopupPlacement(marker, [{ x: NaN, y: 100 }], card, viewport), null);
});

test('uses a deterministic in-bounds placement when every candidate intersects a route', () => {
  const route = [marker];
  for (const point of [
    { x: 400, y: 0 }, { x: 800, y: 0 }, { x: 800, y: 300 }, { x: 800, y: 600 },
    { x: 400, y: 600 }, { x: 0, y: 600 }, { x: 0, y: 300 }, { x: 0, y: 0 },
  ]) route.push(point, marker);
  const placement = choosePopupPlacement(marker, route, card, viewport, true)!;
  assert.equal(placement.anchor, 'top');
  assert.deepEqual(placement.pan, { x: 0, y: 0 });
  assert.deepEqual(placement.rect, { left: 280, top: 318, right: 520, bottom: 478 });
});

test('overlap fallback still prefers a fixed card over panning', () => {
  const nearEdge = { x: 150, y: 180 };
  const blocking = [[{ x: 0, y: 80 }, { x: 300, y: 80 }], [{ x: 0, y: 100 }, { x: 300, y: 100 }]];
  const placement = choosePopupPlacement(nearEdge, [], { width: 120, height: 80 }, { width: 300, height: 220 }, true, blocking)!;
  assert.deepEqual(placement.pan, { x: 0, y: 0 });
});

test('avoids unrelated visible route branches as well as the selected journey', () => {
  const selected = [marker, { x: 400, y: 50 }];
  const blockingBranch = [{ x: 250, y: 400 }, { x: 550, y: 400 }];
  const placement = choosePopupPlacement(marker, selected, card, viewport, false, [blockingBranch]);
  assert.ok(placement);
  assert.notEqual(placement.anchor, 'top');
});
