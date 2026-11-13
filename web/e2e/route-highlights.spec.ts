import { expect, test } from '@playwright/test';
import { basemap, geometry } from './fixtures/geometry';
import { positions, train } from './fixtures/positions';
import type { RouteGeometry, StationFeature, StopOccurrence } from '../src/geometry';
import type { Positions } from '../src/positions';

const harness = 'http://127.0.0.1:4174/e2e/harness.html';
const station = (id: string, name: string, coordinates: [number, number]): StationFeature => ({
  type: 'Feature', id, geometry: { type: 'Point', coordinates }, properties: { name, reason: null },
});
const stop = (station_id: string, stop_sequence: number, distance_meters: number): StopOccurrence => ({
  stop_id: station_id, station_id, stop_sequence, distance_meters, association: 'shape_dist_traveled', reason: null,
});
const meters = (a: [number, number], b: [number, number]) => {
  const lat1 = a[1] * Math.PI / 180, lat2 = b[1] * Math.PI / 180;
  const dlat = lat2 - lat1, dlon = (b[0] - a[0]) * Math.PI / 180;
  return 2 * 6371008.8 * Math.asin(Math.sqrt(Math.sin(dlat / 2) ** 2 + Math.cos(lat1) * Math.cos(lat2) * Math.sin(dlon / 2) ** 2));
};
const farPath: [number, number][] = [[-73.98, 40.73], [-74, 40.71], [-74.01, 40.68], [-74.02, 40.67]];
const farNextDistance = meters(farPath[0]!, farPath[1]!);
const farTerminalDistance = farNextDistance + meters(farPath[1]!, farPath[2]!);
const parkPath: [number, number][] = [[-73.98, 40.73], [-73.94, 40.71], [-73.93, 40.69], [-73.92, 40.68]];
const branchGeometry: RouteGeometry = {
  ...geometry,
  stations: [
    ...geometry.stations.map(item => item.id === 'S4' ? { ...item, properties: { ...item.properties, name: 'Far Rockaway-Mott Av' } }
      : item.id === 'S3' ? { ...item, properties: { ...item.properties, name: 'Ozone Park-Lefferts Blvd' } } : item),
    station('FR_NEXT', 'Howard Beach-JFK Airport', farPath[1]!),
    station('L104', '104 St', [-73.96, 40.705]), station('L111', '111 St', [-73.94, 40.70]),
    station('B90', 'Beach 90 St', parkPath[1]!), station('B98', 'Beach 98 St', parkPath[2]!),
    station('B105', 'Beach 105 St', [-73.925, 40.685]), station('RP116', 'Rockaway Park-Beach 116 St', parkPath[3]!),
  ],
  shapes: [...geometry.shapes.map(item => item.id === 'west' ? { ...item, geometry: { type: 'LineString' as const, coordinates: farPath } } : item),
    { type: 'Feature', id: 'rockaway-park', geometry: { type: 'LineString', coordinates: parkPath }, properties: { usable: true, reason: null } }],
  patterns: [
    { ...geometry.patterns[0]!, stops: [stop('S2', 1, 0), stop('FR_NEXT', 2, farNextDistance), stop('S4', 3, farTerminalDistance)] },
    { ...geometry.patterns[0]!, service_pattern_id: 'lefferts', shape_id: 'east', stops: [stop('S2', 1, 0), stop('L104', 2, 2500), stop('L111', 3, 4500), stop('S3', 4, 6000)] },
    { ...geometry.patterns[0]!, service_pattern_id: 'rockaway-park', shape_id: 'rockaway-park', stops: [
      stop('S2', 1, 0), stop('B90', 2, 4000), stop('B98', 3, 6000), stop('B105', 4, 7000), stop('RP116', 5, 8000),
    ] },
  ],
};

test('Far Rockaway preview and pin never highlight the Lefferts or Rockaway Park branches', async ({ page }, testInfo) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.clock.install();
  const farTrain = { ...train, next_station_id: 'FR_NEXT', next_stop_sequence: 2 };
  let current: Positions = { ...positions, trains: [farTrain] };
  await page.route('https://tiles.openfreemap.org/**', route => route.fulfill({ json: basemap }));
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: branchGeometry }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: current }));
  await page.goto(harness);
  const marker = page.locator('.train-marker');
  await expect(marker).toHaveCount(1);
  await marker.hover();
  await expect(page.locator('.marker-card')).toContainText('Far Rockaway-Mott Av');
  await expect(page.locator('.next-stop-label')).toHaveText(/Next: Howard Beach-JFK Airport\s+Toward Far Rockaway-Mott Av/);
  await expect(page.locator('.next-stop-label')).toBeVisible();
  const path = () => page.evaluate(() => {
    const map = window.testMap;
    const upcoming = map.querySourceFeatures('selected-route').find(feature => feature.properties.phase === 'upcoming');
    const next = map.querySourceFeatures('selected-progress')[0];
    return { upcoming: upcoming?.geometry.type === 'LineString' ? upcoming.geometry.coordinates : [],
      next: next?.geometry.type === 'LineString' ? next.geometry.coordinates : [],
      stops: map.querySourceFeatures('selected-stops').map(feature => feature.geometry.type === 'Point' ? feature.geometry.coordinates : []) };
  });
  await expect.poll(async () => (await path()).upcoming.length).toBeGreaterThan(1);
  const preview = await path();
  expect(preview.upcoming.at(-1)![0]).toBeCloseTo(farPath[2]![0], 3);
  expect(preview.upcoming.at(-1)![1]).toBeCloseTo(farPath[2]![1], 3);
  expect(preview.next.at(-1)![0]).toBeCloseTo(farPath[1]![0], 3);
  expect(new Set(preview.stops.map(point => point.map(value => value.toFixed(3)).join(','))).size).toBe(3);
  for (const other of [[-73.96, 40.705], [-73.94, 40.70], ...parkPath.slice(2)] as [number, number][]) {
    expect(preview.stops.some(point => Math.abs(point[0]! - other[0]) < 0.001 && Math.abs(point[1]! - other[1]) < 0.001)).toBe(false);
  }
  expect(await page.evaluate(() => window.testMap.getPaintProperty('route-line', 'line-color'))).toBe('#8d98a8');
  await page.locator('.map-panel').screenshot({ path: testInfo.outputPath('far-rockaway-preview.png') });
  await marker.click();
  const dock = page.locator('.train-dock .marker-card');
  await expect(dock).toHaveAttribute('data-state', 'active');
  await expect.poll(async () => (await path()).upcoming.at(-1)?.[0]).toBeCloseTo(farPath[2]![0], 3);
  current = { ...positions, trains: [{ ...farTrain, movement_state: 'reported_stopped', position_basis: 'schedule_fallback', fallback_reason: 'stale_realtime' }] };
  await page.clock.runFor(5000);
  await expect(marker).toHaveAttribute('data-simulation', 'true');
  await expect(dock).toContainText('Schedule estimate · not realtime');
  await expect.poll(() => page.evaluate(() => window.testMap.getPaintProperty('selected-progress', 'line-opacity'))).toBe(1);
});

for (const width of [390, 320]) test(`next stop name stays beside its circle at ${width}px`, async ({ page }, testInfo) => {
  await page.setViewportSize({ width, height: 1000 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.route('https://tiles.openfreemap.org/**', route => route.fulfill({ json: basemap }));
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: branchGeometry }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: { ...positions,
    trains: [{ ...train, next_station_id: 'FR_NEXT', next_stop_sequence: 2 }] } }));
  await page.goto(harness);
  await page.locator('.train-marker').click();
  const label = page.locator('.next-stop-label');
  await expect(label).toHaveText(/Next: Howard Beach-JFK Airport\s+Toward Far Rockaway-Mott Av/);
  await page.evaluate(() => window.testMap.jumpTo({ center: [-74, 40.71], zoom: 13 }));
  if (width === 320) {
    await expect(label).toBeHidden();
    await page.locator('.map-panel').screenshot({ path: testInfo.outputPath(`next-stop-${width}.png`) });
    return;
  }
  await expect(label).toBeVisible();
  const box = (await label.boundingBox())!, mapBox = (await page.locator('.route-map').boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(mapBox.x - 1);
  expect(box.x + box.width).toBeLessThanOrEqual(mapBox.x + mapBox.width + 1);
  expect(box.y).toBeGreaterThanOrEqual(mapBox.y - 1);
  expect(box.y + box.height).toBeLessThanOrEqual(mapBox.y + mapBox.height + 1);
  const stopPoint = await page.evaluate(() => window.testMap.project([-74, 40.71]));
  const dx = Math.max(box.x - mapBox.x - stopPoint.x, 0, stopPoint.x - (box.x + box.width - mapBox.x));
  const dy = Math.max(box.y - mapBox.y - stopPoint.y, 0, stopPoint.y - (box.y + box.height - mapBox.y));
  expect(Math.hypot(dx, dy)).toBeLessThanOrEqual(26);
  await page.locator('.map-panel').screenshot({ path: testInfo.outputPath(`next-stop-${width}.png`) });
});

test('missing next-stop anchor keeps the card name without a map label or segment', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const unknownNext: RouteGeometry = { ...branchGeometry, patterns: branchGeometry.patterns.map(pattern => pattern.service_pattern_id === 'pattern-west'
    ? { ...pattern, stops: pattern.stops.map(stop => stop.station_id === 'FR_NEXT' ? { ...stop, distance_meters: null } : stop) } : pattern) };
  await page.route('https://tiles.openfreemap.org/**', route => route.fulfill({ json: basemap }));
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: unknownNext }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: { ...positions,
    trains: [{ ...train, next_station_id: 'FR_NEXT', next_stop_sequence: 2 }] } }));
  await page.goto(harness);
  await page.locator('.train-marker').hover();
  await expect(page.locator('.marker-card')).toContainText('Howard Beach-JFK Airport');
  await expect(page.locator('.next-stop-label')).toBeHidden();
  await expect.poll(() => page.evaluate(() => window.testMap.querySourceFeatures('selected-progress').length)).toBe(0);
});
