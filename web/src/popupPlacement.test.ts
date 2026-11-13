import assert from 'node:assert/strict';
import { test } from 'node:test';
import { chooseInspectionLayout, chooseLabelPlacement, choosePopupPlacement, rectsOverlap, type InspectionScene } from './popupPlacement.ts';

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

test('places station labels beside their point without covering a preview card', () => {
  const point = { x: 400, y: 300 }, label = { width: 180, height: 40 };
  assert.deepEqual(chooseLabelPlacement(point, label, viewport), {
    side: 'right', left: 416, top: 280, right: 596, bottom: 320,
  });
  assert.deepEqual(chooseLabelPlacement(point, label, viewport, [{ left: 410, top: 200, right: 650, bottom: 400 }]), {
    side: 'left', left: 204, top: 280, right: 384, bottom: 320,
  });
  assert.equal(chooseLabelPlacement(point, { width: 790, height: 40 }, viewport), null);
});

test('station labels use a vertical position when both sides are blocked', () => {
  const point = { x: 400, y: 300 }, label = { width: 180, height: 40 };
  const obstacles = [
    { left: 180, top: 310, right: 390, bottom: 330 },
    { left: 410, top: 310, right: 620, bottom: 330 },
  ];
  assert.deepEqual(chooseLabelPlacement(point, label, viewport, obstacles), {
    side: 'top', left: 310, top: 244, right: 490, bottom: 284,
  });
});

test('station labels never cross the highlighted journey', () => {
  const point = { x: 400, y: 300 }, label = { width: 180, height: 40 };
  assert.deepEqual(chooseLabelPlacement(point, label, viewport, [], [[{ x: 100, y: 300 }, { x: 700, y: 300 }]]), {
    side: 'top', left: 310, top: 244, right: 490, bottom: 284,
  });
  assert.deepEqual(chooseLabelPlacement(point, label, viewport, [], [
    [{ x: 100, y: 300 }, { x: 700, y: 300 }], [{ x: 400, y: 50 }, { x: 400, y: 550 }],
  ]), { side: 'top-right', left: 416, top: 244, right: 596, bottom: 284 });
});

const popups: InspectionScene['popups'] = [
  { anchor: 'bottom', rect: { left: -160, right: 160, top: -230, bottom: 0 } },
  { anchor: 'top', rect: { left: -160, right: 160, top: 0, bottom: 230 } },
  { anchor: 'left', rect: { left: 0, right: 330, top: -110, bottom: 110 } },
  { anchor: 'right', rect: { left: -330, right: 0, top: -110, bottom: 110 } },
  { anchor: 'bottom-left', rect: { left: 0, right: 320, top: -230, bottom: 0 } },
  { anchor: 'bottom-right', rect: { left: -320, right: 0, top: -230, bottom: 0 } },
  { anchor: 'top-left', rect: { left: 0, right: 320, top: 0, bottom: 230 } },
  { anchor: 'top-right', rect: { left: -320, right: 0, top: 0, bottom: 230 } },
];
const scene: InspectionScene = { marker, nextStop: { x: 440, y: 330 }, viewport, popups,
  labels: [{ width: 200, height: 60, maxWidth: 220 }], direction: { x: 40, y: 30 },
  lines: [{ points: [{ x: 100, y: 300 }, { x: 750, y: 340 }], margin: 10 }] };

test('joint layout never permits overlap, including popup pointers and viewport edges', () => {
  for (const point of [{ x: 400, y: 300 }, { x: 730, y: 300 }, { x: 100, y: 100 }]) {
    const layout = chooseInspectionLayout({ ...scene, marker: point }, 1);
    if (layout.popup && layout.label) assert.equal(rectsOverlap(layout.popup.rect, layout.label), false);
    for (const rect of [layout.popup?.rect, layout.label].filter(rect => rect !== null && rect !== undefined)) {
      assert.ok(rect.left >= 12 && rect.top >= 12 && rect.right <= 788 && rect.bottom <= 588);
    }
  }
  assert.equal(chooseLabelPlacement(marker, { width: 100, height: 40 }, viewport,
    [{ left: 0, top: 0, right: 800, bottom: 600 }]), null);
});

test('bounded zoom chooses the first feasible tenth-level increase and keeps a safe solo fallback', () => {
  const nearEdge: InspectionScene = { ...scene, marker: { x: 120, y: 130 }, nextStop: { x: 60, y: 160 }, direction: { x: -60, y: 30 },
    lines: [{ points: [{ x: 100, y: 130 }, { x: 750, y: 170 }], margin: 10 }] };
  assert.equal(chooseInspectionLayout(nearEdge).label, null);
  const zoomed = chooseInspectionLayout(nearEdge, 1);
  assert.equal(zoomed.zoomDelta, 0.1);
  assert.ok(zoomed.popup && zoomed.label);
  assert.ok(Math.abs(zoomed.pan.x) <= 200 && Math.abs(zoomed.pan.y) <= 150);
  const impossibleLabel = chooseInspectionLayout({ ...scene, labels: [{ width: 900, height: 60, maxWidth: 900 }] }, 1);
  assert.ok(impossibleLabel.popup);
  assert.equal(impossibleLabel.label, null);
  assert.equal(impossibleLabel.zoomDelta, 0);
  assert.equal(chooseInspectionLayout({ ...scene, viewport: { width: 100, height: 100 } }, 1).popup, null);
});
