import { expect, test, type Page } from '@playwright/test';
import { basemap, geometry } from './fixtures/geometry';
import { positions, train } from './fixtures/positions';
import type { RouteGeometry } from '../src/geometry';
import type { Positions } from '../src/positions';

declare global {
  interface Window { tooltipAudit: { samples: number; violations: string[] } }
}
const harness = 'http://127.0.0.1:4174/e2e/harness.html';
const camera = (page: Page) => page.evaluate(() => ({ center: window.testMap.getCenter().toArray(), zoom: window.testMap.getZoom() }));
const meters = (a: [number, number], b: [number, number]) => {
  const lat1 = a[1] * Math.PI / 180, lat2 = b[1] * Math.PI / 180;
  return 2 * 6371008.8 * Math.asin(Math.sqrt(Math.sin((lat2 - lat1) / 2) ** 2
    + Math.cos(lat1) * Math.cos(lat2) * Math.sin((b[0] - a[0]) * Math.PI / 360) ** 2));
};

async function audit(page: Page) {
  await page.addInitScript(() => {
    window.tooltipAudit = { samples: 0, violations: [] };
    const visible = (element: HTMLElement | null) => element && !element.hidden && getComputedStyle(element).visibility === 'visible'
      && element.getBoundingClientRect().width > 0;
    const check = () => {
      const popup = document.querySelector<HTMLElement>('.train-popup');
      const label = document.querySelector<HTMLElement>('.next-stop-label');
      if (visible(popup) && visible(label)) {
        window.tooltipAudit.samples++;
        const a = popup!.getBoundingClientRect(), b = label!.getBoundingClientRect();
        if (a.left < b.right + 7 && a.right > b.left - 7 && a.top < b.bottom + 7 && a.bottom > b.top - 7)
          window.tooltipAudit.violations.push('tooltip overlap');
        const map = window.testMap.getContainer().getBoundingClientRect();
        for (const rect of [a, b]) if (rect.left < map.left + 10 || rect.top < map.top + 10 || rect.right > map.right - 10 || rect.bottom > map.bottom - 10)
          window.tooltipAudit.violations.push(`clipped tooltip: ${JSON.stringify({ box: rect.toJSON(), map: map.toJSON(), time: performance.now() })}`);
      }
      // Sample after the frame's layout/ResizeObserver notifications complete.
      requestAnimationFrame(() => setTimeout(check, 0));
    };
    requestAnimationFrame(() => setTimeout(check, 0));
  });
}

async function start(page: Page, mapSize?: { width: number; height: number }) {
  let currentGeometry: RouteGeometry = geometry, currentPositions: Positions = positions;
  await page.route('https://tiles.openfreemap.org/**', route => route.fulfill({ json: basemap }));
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: currentGeometry }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: currentPositions }));
  await audit(page);
  await page.goto(harness);
  await expect(page.locator('.train-marker')).toHaveCount(1);
  if (mapSize) await page.addStyleTag({ content: `.route-map { width: ${mapSize.width}px !important; height: ${mapSize.height}px !important; }` });
  await page.evaluate(() => window.testMap.resize());
  // Use a coherent journey with the train and next stop close to the left edge.
  const coordinates = await page.evaluate(() => {
    const map = window.testMap;
    return [[600, 180], [120, 180], [60, 210], [30, 240]].map(point => map.unproject(point as [number, number]).toArray());
  });
  const [origin, marker, stop, terminal] = coordinates as [number, number][];
  const progress = meters(origin!, marker!), nextDistance = progress + meters(marker!, stop!);
  const terminalDistance = nextDistance + meters(stop!, terminal!);
  currentGeometry = { ...geometry, schedule_version: 'tooltip:fixture', stations: [
    { ...geometry.stations[0]!, id: 'origin', geometry: { type: 'Point', coordinates: origin! }, properties: { name: 'Origin', reason: null } },
    { ...geometry.stations[0]!, id: 'next', geometry: { type: 'Point', coordinates: stop! }, properties: { name: '80 St', reason: null } },
    { ...geometry.stations[0]!, id: 'terminal', geometry: { type: 'Point', coordinates: terminal! }, properties: { name: 'Inwood-207 St', reason: null } },
  ], shapes: [{ ...geometry.shapes[0]!, id: 'west', geometry: { type: 'LineString', coordinates: coordinates as [number, number][] } }],
  patterns: [{ ...geometry.patterns[0]!, shape_id: 'west', stops: [
    { ...geometry.patterns[0]!.stops[0]!, station_id: 'origin', stop_id: 'origin', stop_sequence: 1, distance_meters: 0 },
    { ...geometry.patterns[0]!.stops[0]!, station_id: 'next', stop_id: 'next', stop_sequence: 2, distance_meters: nextDistance },
    { ...geometry.patterns[0]!.stops[0]!, station_id: 'terminal', stop_id: 'terminal', stop_sequence: 3, distance_meters: terminalDistance },
  ] }] };
  currentPositions = { ...positions, schedule_version: 'tooltip:fixture', trains: [{ ...train, schedule_version: 'tooltip:fixture',
    longitude: marker![0], latitude: marker![1], shape_distance_meters: progress, direction: 'northbound', next_station_id: 'next', next_stop_sequence: 2 }] };
  await page.clock.install();
  await page.clock.runFor(5000);
  await expect(page.locator('.train-marker')).toHaveAttribute('aria-label', /Inwood-207 St/);
  await page.clock.resume();
  return { refresh: (nextName: string) => {
    currentGeometry = { ...currentGeometry, schedule_version: 'tooltip:refresh', stations: currentGeometry.stations.map(station =>
      station.id === 'next' ? { ...station, properties: { ...station.properties, name: nextName } } : station) };
    currentPositions = { ...currentPositions, schedule_version: 'tooltip:refresh', trains: currentPositions.trains.map(train => ({ ...train, schedule_version: 'tooltip:refresh' })) };
  } };
}

test('near-edge tooltips stay separate from first reveal, use bounded zoom, and restore the camera', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  const data = await start(page, { width: 800, height: 600 });
  const before = await camera(page);
  await page.locator('.train-marker').hover();
  await expect(page.locator('.train-popup .marker-card')).toBeVisible();
  await expect(page.locator('.next-stop-label')).toBeVisible();
  await expect.poll(async () => (await camera(page)).zoom).toBeGreaterThan(before.zoom);
  expect((await camera(page)).zoom).toBeLessThanOrEqual(before.zoom + 1.000001);
  await expect.poll(() => page.evaluate(() => window.tooltipAudit.samples)).toBeGreaterThan(3);
  await page.locator('.map-panel').screenshot({ path: testInfo.outputPath('joint-tooltips.png') });
  const zoomed = await camera(page);
  data.refresh('80 St with a considerably longer station name');
  await page.clock.runFor(5000);
  await expect(page.locator('.marker-card')).toContainText('considerably longer');
  expect((await camera(page)).zoom).toBeLessThanOrEqual(zoomed.zoom + 0.000001);
  await page.addStyleTag({ content: '.marker-card, .next-stop-label { font-size: 1rem; } .route-map { width: 720px !important; height: 520px !important; }' });
  await expect(page.locator('.marker-card')).toBeVisible();
  await expect(page.locator('.route-map')).toHaveCSS('width', '720px');
  await page.waitForTimeout(350);
  expect((await camera(page)).zoom).toBeLessThanOrEqual(zoomed.zoom + 0.000001);
  await page.keyboard.press('Escape');
  await expect(page.locator('.train-popup')).toHaveCount(0);
  await expect.poll(async () => Math.abs((await camera(page)).zoom - before.zoom)).toBeLessThan(1e-8);
  expect((await camera(page)).center[0]).toBeCloseTo(before.center[0]!, 8);
  expect(await page.evaluate(() => window.tooltipAudit.violations)).toEqual([]);
});

test('manual camera navigation cancels automatic restoration', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await start(page, { width: 800, height: 600 });
  await page.locator('.train-marker').focus();
  await expect(page.locator('.train-popup .marker-card')).toBeVisible();
  await page.getByRole('button', { name: 'Zoom in' }).click();
  const manual = await camera(page);
  await page.keyboard.press('Escape');
  await expect(page.locator('.train-popup')).toHaveCount(0);
  expect(await camera(page)).toEqual(manual);
  expect(await page.evaluate(() => window.tooltipAudit.violations)).toEqual([]);
});

for (const width of [1440, 1280, 1024, 768, 700, 390, 320]) test(`tooltip containment and two-line styling at ${width}px`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await start(page);
  await page.locator('.train-marker').focus();
  await expect(page.locator('.marker-card')).toBeVisible();
  await expect(page.locator('.next-stop-label')).toHaveText(/Next: 80 St\s+Toward Inwood-207 St/);
  await expect(page.locator('.next-stop-label')).toHaveCSS('white-space', 'pre-line');
  await expect(page.locator('.next-stop-label')).toHaveCSS('background-color', 'rgb(0, 57, 166)');
  await expect(page.locator('.next-stop-label')).toHaveCSS('color', 'rgb(255, 255, 255)');
  expect(await page.evaluate(() => window.tooltipAudit.violations)).toEqual([]);
});
