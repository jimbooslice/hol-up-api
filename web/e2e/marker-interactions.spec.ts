import { expect, test, type Page } from '@playwright/test';
import { basemap, geometry } from './fixtures/geometry';
import { positions, train } from './fixtures/positions';
import type { Positions, Train } from '../src/positions';

const harness = 'http://127.0.0.1:4174/e2e/harness.html';
const west: Train = { ...train, direction: 'northbound', longitude: -73.99, latitude: 40.72, shape_distance_meters: 1400 };
const east: Train = { ...train, trip_instance_id: 'run-two', source_record_id: 'record-two', direction: 'southbound',
  longitude: -73.94, latitude: 40.71, shape_id: 'east', service_pattern_id: 'pattern-east', shape_distance_meters: 3500,
  next_station_id: 'S3' };
const snapshot: Positions = { ...positions, trains: [west, east] };
const branches = { ...geometry, patterns: [...geometry.patterns, {
  ...geometry.patterns[0]!, service_pattern_id: 'pattern-east', shape_id: 'east', stops: [
    geometry.patterns[0]!.stops[0]!,
    { ...geometry.patterns[0]!.stops[1]!, stop_id: 'S3S', station_id: 'S3', distance_meters: 6500 },
  ],
}] };
const card = (page: Page) => page.locator('.marker-card');
const button = (page: Page, run = 'run-one') => page.getByRole('button', { name: `Run ${run}`, exact: true });
const phases = (page: Page) => page.evaluate(() => [...new Set(window.testMap.querySourceFeatures('selected-route').map(feature => feature.properties.phase))].sort());
const camera = (page: Page) => page.evaluate(() => ({ center: window.testMap.getCenter().toArray(), zoom: window.testMap.getZoom() }));
const marker = (page: Page, item = west) => page.locator(`.train-marker[data-identity=${JSON.stringify(`run:${JSON.stringify(item.trip_instance_id)}`)}]`);

async function start(page: Page, expected = 2) {
  await page.goto(harness);
  await expect(page.locator('.train-list > li')).toHaveCount(expected);
  await expect(page.locator('.train-marker')).toHaveCount(expected);
  await page.locator('.route-map').scrollIntoViewIfNeeded();
}

async function markerPoint(page: Page, item = west) {
  const box = (await marker(page, item).locator('.train-marker-badge').boundingBox())!;
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

async function hover(page: Page, item = west) {
  const point = await markerPoint(page, item);
  await page.mouse.move(point.x, point.y);
}

async function leave(page: Page) {
  const box = (await page.locator('.route-map').boundingBox())!;
  await page.mouse.move(box.x + 5, box.y - 5);
}

test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.route('https://tiles.openfreemap.org/**', route => route.fulfill({ json: basemap }));
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: branches }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: snapshot }));
});

test('hover previews the journey and direction, then restores the unselected map', async ({ page }, testInfo) => {
  await start(page);
  await expect(page.getByText('Hover a train badge to preview its trip; click or tap to pin. Overlapping trains share the same map position.', { exact: true })).toBeVisible();
  const before = await camera(page);
  await hover(page);
  await expect(card(page)).toHaveAttribute('data-state', 'preview');
  await expect(card(page)).toContainText('Northbound to Alpha');
  await expect(button(page)).toHaveAttribute('aria-pressed', 'false');
  await expect(marker(page, east).locator('.train-marker-badge')).toHaveCSS('opacity', '0.55');
  await expect.poll(() => phases(page)).toEqual(['upcoming', 'visited']);
  await expect.poll(() => page.evaluate(() => window.testMap.querySourceFeatures('selected-progress').length)).toBeGreaterThan(0);
  await expect.poll(() => page.evaluate(() => JSON.stringify(window.testMap.getPaintProperty('selected-progress', 'line-gradient')))).toContain('0.35');
  await expect.poll(() => page.evaluate(() => [...new Set(window.testMap.querySourceFeatures('selected-stops')
    .map(feature => `${feature.properties.phase}:${feature.properties.next}`))].sort())).toEqual(['upcoming:true', 'visited:false']);
  expect(await page.evaluate(() => window.testMap.getPaintProperty('selected-stations', 'circle-color'))).toEqual(
    ['case', ['==', ['get', 'phase'], 'visited'], '#8d98a8', '#ffffff']);
  expect(await page.evaluate(() => window.testMap.getPaintProperty('route-line', 'line-opacity'))).toBeLessThan(1);
  await page.locator('.map-panel').screenshot({ path: testInfo.outputPath('train-preview.png') });
  await leave(page);
  await expect(card(page)).toHaveCount(0);
  await expect(marker(page, east).locator('.train-marker-badge')).toHaveCSS('opacity', '1');
  await expect.poll(() => phases(page)).toEqual([]);
  await expect.poll(() => page.evaluate(() => window.testMap.querySourceFeatures('selected-progress').length)).toBe(0);
  await expect.poll(() => camera(page)).toEqual(before);
  expect(await page.evaluate(() => window.testMap.getPaintProperty('route-line', 'line-opacity'))).toBe(1);
});

test('hovering another train temporarily replaces the clicked journey and restores it on leave', async ({ page }, testInfo) => {
  await start(page);
  const point = await markerPoint(page);
  await page.mouse.click(point.x, point.y);
  await expect(button(page)).toHaveAttribute('aria-pressed', 'true');
  await expect(card(page)).toHaveAttribute('data-state', 'selected');
  await expect(card(page)).toContainText('Selected');
  await expect.poll(() => page.evaluate(() => window.testMap.querySourceFeatures('selected-progress').length)).toBeGreaterThan(0);
  await page.locator('.map-panel').screenshot({ path: testInfo.outputPath('train-selected.png') });
  const selectedCamera = await camera(page);
  await hover(page, east);
  await expect(card(page)).toHaveAttribute('data-state', 'preview');
  await expect(card(page)).toContainText('Southbound to 2 St');
  await expect(button(page)).toHaveAttribute('aria-pressed', 'true');
  await expect(button(page, 'run-two')).toHaveAttribute('aria-pressed', 'false');
  await expect.poll(() => page.evaluate(() => window.testMap.querySourceFeatures('selected-stops')
    .some(feature => feature.geometry.type === 'Point' && Math.abs(feature.geometry.coordinates[0]! + 73.91) < 0.001))).toBe(true);
  await leave(page);
  await expect(card(page)).toHaveAttribute('data-state', 'selected');
  await expect(card(page)).toContainText('Northbound to Alpha');
  await expect.poll(() => camera(page)).toEqual(selectedCamera);
  await card(page).getByRole('button', { name: 'Close train details' }).click();
  await expect(card(page)).toHaveCount(0);
  await expect(button(page)).toHaveAttribute('aria-pressed', 'false');
});

test('the next-stop progress pulse advances when motion is allowed', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await start(page);
  await hover(page);
  const position = () => page.evaluate(() => {
    const gradient = window.testMap.getPaintProperty('selected-progress', 'line-gradient') as unknown as [unknown, unknown, [unknown, [unknown, unknown, number]]];
    return gradient?.[2]?.[1]?.[2];
  });
  await expect.poll(async () => typeof await position()).toBe('number');
  const first = await position();
  await expect.poll(position).not.toBe(first);
});

for (const dismiss of ['Escape', 'empty map'] as const) test(`${dismiss} dismisses an unpinned hover preview`, async ({ page }) => {
  await start(page);
  await hover(page);
  await expect(card(page)).toHaveAttribute('data-state', 'preview');
  if (dismiss === 'Escape') await page.keyboard.press('Escape');
  else {
    const box = (await page.locator('.route-map').boundingBox())!;
    await page.mouse.click(box.x + 8, box.y + 8);
  }
  await expect(card(page)).toHaveCount(0);
  await expect.poll(() => phases(page)).toEqual([]);
});

test('accepted train refresh preserves the hovered state and removal restores the selected train', async ({ page }) => {
  let next = snapshot;
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: next }));
  await start(page);
  await button(page).click();
  await page.locator('.route-map').scrollIntoViewIfNeeded();
  await hover(page, east);
  await expect(card(page)).toContainText('Southbound to 2 St');
  await page.clock.install();
  next = { ...snapshot, trains: [west, { ...east, next_arrival_at: '2026-09-14T12:09:00Z' }] };
  await page.clock.runFor(5000);
  await expect(card(page)).toContainText('8:09 AM EDT');
  await expect(card(page)).toHaveAttribute('data-state', 'preview');
  await expect(button(page)).toHaveAttribute('aria-pressed', 'true');
  next = { ...snapshot, trains: [west] };
  await page.clock.runFor(5000);
  await expect(card(page)).toHaveAttribute('data-state', 'selected');
  await expect(card(page)).toContainText('Northbound to Alpha');
  await expect(button(page)).toHaveAttribute('aria-pressed', 'true');
});

test('the preview card remains usable while the pointer moves from the train to its content', async ({ page }) => {
  await start(page);
  await hover(page);
  await expect(card(page)).toHaveAttribute('data-state', 'preview');
  await card(page).hover();
  await expect(card(page)).toBeVisible();
  await leave(page);
  await expect(card(page)).toHaveCount(0);
});

test('the floating card and outlined pointer stay clear of every visible route line', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  const straightTrain = { ...west, longitude: -73.98, latitude: 40.725, shape_distance_meters: 2500 };
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: {
    ...branches, shapes: [{ ...branches.shapes[0]!, id: 'west', geometry: {
      type: 'LineString', coordinates: [[-73.98, 40.75], [-73.98, 40.70]],
    } }], patterns: branches.patterns.slice(0, 1),
  } }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: { ...snapshot, trains: [straightTrain] } }));
  await start(page, 1);
  await hover(page, straightTrain);
  await expect(page.locator('.route-map .marker-card')).toBeVisible();
  await expect(page.locator('.train-popup')).toHaveCSS('z-index', '4');
  await expect(page.locator('.train-marker-overlay')).toHaveCSS('z-index', '2');
  const tip = page.locator('.train-popup .maplibregl-popup-tip');
  await expect(tip).toBeVisible();
  await expect.poll(() => tip.evaluate(element => getComputedStyle(element).filter)).not.toBe('none');
  await expect.poll(() => tip.evaluate(element => getComputedStyle(element).filter.match(/drop-shadow/g)?.length ?? 0)).toBe(1);
  await expect.poll(async () => { const box = await tip.boundingBox(); return box ? Math.max(box.width, box.height) : 0; }).toBeGreaterThanOrEqual(10);
  await expect.poll(() => page.evaluate(() => {
    const badge = document.querySelector('.train-marker-badge')?.getBoundingClientRect();
    const arrow = document.querySelector('.train-popup .maplibregl-popup-tip')?.getBoundingClientRect();
    if (!arrow || !badge) return Infinity;
    const marker = { x: badge.left + badge.width / 2, y: badge.top + badge.height / 2 };
    const x = Math.max(arrow.left, Math.min(marker.x, arrow.right));
    const y = Math.max(arrow.top, Math.min(marker.y, arrow.bottom));
    return Math.hypot(x - marker.x, y - marker.y);
  })).toBeLessThanOrEqual(20);
  await expect.poll(() => page.evaluate(() => {
    const map = window.testMap;
    const bounds = map.getContainer().getBoundingClientRect();
    const details = document.querySelector('.route-map .marker-card')?.getBoundingClientRect();
    if (!details) return false;
    if (details.left < bounds.left || details.right > bounds.right || details.top < bounds.top || details.bottom > bounds.bottom) return false;
    const lines = map.querySourceFeatures('shapes');
    if (!lines.length) return false;
    return lines.every(feature => {
      if (feature.geometry.type !== 'LineString') return false;
      const points = feature.geometry.coordinates.map(coordinate => map.project([coordinate[0]!, coordinate[1]!]));
      return points.slice(1).every((end, index) => {
        const start = points[index]!;
        const steps = Math.ceil(Math.hypot(end.x - start.x, end.y - start.y) / 2);
        for (let step = 0; step <= steps; step++) {
          const t = steps === 0 ? 0 : step / steps;
          const x = bounds.left + start.x + (end.x - start.x) * t;
          const y = bounds.top + start.y + (end.y - start.y) * t;
          if (x >= details.left && x <= details.right && y >= details.top && y <= details.bottom) return false;
        }
        return true;
      });
    });
  })).toBe(true);
  await page.locator('.map-panel').screenshot({ path: testInfo.outputPath('outlined-pointer.png') });
});

test('a constrained map keeps details on-map until the user explicitly moves them below', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 1000 });
  await start(page);
  await page.addStyleTag({ content: '.route-map { height: 120px !important; }' });
  await page.evaluate(() => window.testMap.resize());
  await button(page).click();
  const panel = page.locator('.marker-details-panel');
  const popup = page.locator('.route-map .marker-card');
  await expect(popup).toBeVisible();
  await expect(popup).toHaveAttribute('data-state', 'selected');
  await expect(panel).toBeHidden();
  await popup.getByRole('button', { name: 'Pin details below map' }).click();
  await expect(panel.locator('.marker-card')).toBeVisible();
  await expect(panel.locator('.marker-card')).toHaveAttribute('data-state', 'selected');
  await expect(panel).toContainText('Northbound to Alpha');
  await expect(popup).toHaveCount(0);
  await page.evaluate(item => window.testMap.jumpTo({ center: [item.longitude!, item.latitude!], zoom: 13 }), east);
  await hover(page, east);
  await expect(popup).toHaveAttribute('data-state', 'preview');
  await expect(panel.locator('.marker-card')).toHaveAttribute('data-state', 'selected');
  await leave(page);
  await expect(popup).toHaveCount(0);
  await page.evaluate(item => window.testMap.jumpTo({ center: [item.longitude!, item.latitude!], zoom: 13 }), west);
  await panel.getByRole('button', { name: 'Show details on map' }).click();
  await expect(panel).toBeHidden();
  await expect(popup).toHaveAttribute('data-state', 'selected');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.locator('.map-panel').screenshot({ path: testInfo.outputPath('mobile-details-panel.png') });
  await popup.getByRole('button', { name: 'Close train details' }).click();
  await expect(card(page)).toHaveCount(0);
});

test('temporary inspection movement restores unless the user deliberately drags', async ({ page }) => {
  await start(page);
  await page.addStyleTag({ content: '.route-map { width: 380px !important; height: 300px !important; }' });
  await page.evaluate(item => new Promise<void>(resolve => {
    window.testMap.once('idle', () => resolve());
    window.testMap.resize();
    window.testMap.jumpTo({ center: [item.longitude!, item.latitude!], zoom: 13 });
  }), west);
  const before = await camera(page);
  await hover(page);
  await expect(card(page)).toBeVisible();
  await leave(page);
  await expect(card(page)).toHaveCount(0);
  await expect.poll(() => camera(page)).toEqual(before);

  const point = await markerPoint(page);
  await page.mouse.click(point.x, point.y);
  await expect(card(page)).toHaveAttribute('data-state', 'selected');
  await expect.poll(() => camera(page)).not.toEqual(before);
  const box = (await page.locator('.route-map').boundingBox())!;
  await page.mouse.move(box.x + 20, box.y + box.height - 20);
  await page.mouse.down();
  await page.mouse.move(box.x + 70, box.y + box.height - 60, { steps: 8 });
  await page.mouse.up();
  await expect.poll(() => page.evaluate(() => window.testMap.isMoving())).toBe(false);
  const dragged = await camera(page);
  await page.keyboard.press('Escape');
  await expect(card(page)).toHaveCount(0);
  await expect.poll(() => camera(page)).toEqual(dragged);
});

test.describe('touch selection', () => {
  test.use({ hasTouch: true, viewport: { width: 390, height: 1000 } });
  test('tap pins a train and tapping the empty map clears it', async ({ page }) => {
    await start(page);
    const point = await markerPoint(page);
    await page.touchscreen.tap(point.x, point.y);
    await expect(button(page)).toHaveAttribute('aria-pressed', 'true');
    await expect(card(page)).toHaveAttribute('data-state', 'selected');
    await page.locator('.route-map').scrollIntoViewIfNeeded();
    const box = (await page.locator('.route-map').boundingBox())!;
    await page.touchscreen.tap(box.x + 8, box.y + 8);
    await expect(button(page)).toHaveAttribute('aria-pressed', 'false');
    await expect(card(page)).toHaveCount(0);
  });
});

test('offscreen trains leave the marker tab order and remain available in the train list', async ({ page }) => {
  await start(page);
  await page.evaluate(() => window.testMap.jumpTo({ center: [-73.85, 40.72], zoom: 15 }));
  await expect(marker(page)).toHaveCount(0);
  await expect(button(page)).toBeAttached();
  await button(page).click();
  await expect(button(page)).toHaveAttribute('aria-pressed', 'true');
});
