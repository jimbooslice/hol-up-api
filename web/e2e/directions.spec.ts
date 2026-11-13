import { expect, test } from '@playwright/test';
import { basemap, geometry } from './fixtures/geometry';
import { positions, train } from './fixtures/positions';
import type { Positions } from '../src/positions';

const harness = 'http://127.0.0.1:4174/e2e/harness.html';
const midway = { ...train, longitude: -73.99, latitude: 40.72, shape_distance_meters: 1395 };

test.beforeEach(async ({ page }) => {
  await page.route('https://tiles.openfreemap.org/**', route => route.fulfill({ json: basemap }));
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: geometry }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: { ...positions, trains: [midway] } }));
});

for (const width of [1440, 1280, 1024, 768, 701]) test(`direction cues and Map options at ${width}px`, async ({ page }, testInfo) => {
  await page.setViewportSize({ width, height: 1000 });
  await page.goto(harness);
  const marker = page.locator('.train-marker');
  const trigger = page.getByRole('button', { name: 'Map options' });
  const checkbox = page.getByRole('checkbox', { name: 'Show train directions' });
  await expect(marker).toHaveCount(1);
  await expect(page.locator('.train-direction-dot')).toHaveCount(3);
  await expect(page.locator('.train-direction-chip')).toHaveText('Toward Alpha');
  await expect(marker).toHaveAttribute('aria-label', /A, To Alpha, realtime estimate/);
  await expect(page.locator('.train-direction-chip')).toHaveCSS('pointer-events', 'none');
  await expect(marker).toHaveCSS('width', '44px');
  const before = await page.evaluate(() => ({ width: window.testMap.getContainer().clientWidth,
    center: window.testMap.getCenter().toArray(), zoom: window.testMap.getZoom() }));
  await trigger.click();
  await expect(checkbox).toBeFocused();
  const after = await page.evaluate(() => ({ width: window.testMap.getContainer().clientWidth,
    center: window.testMap.getCenter().toArray(), zoom: window.testMap.getZoom() }));
  expect(after.center).toEqual(before.center);
  expect(after.zoom).toBe(before.zoom);
  if (width >= 1280) expect(after.width).toBeLessThan(before.width);
  else expect(after.width).toBe(before.width);
  await expect(page.locator('.train-direction-dot')).toHaveCount(3);
  await page.locator('.map-panel').screenshot({ path: testInfo.outputPath(`directions-${width}.png`) });
  await page.getByRole('radio', { name: 'Next station' }).check();
  await expect(page.locator('.train-direction-chip')).toHaveText('Next: Alpha');
  await checkbox.uncheck();
  await expect(page.locator('.train-direction-chip, .train-direction-dot')).toHaveCount(0);
  await checkbox.check();
  await marker.click();
  await expect(marker).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.train-direction-chip')).toHaveText('Next: Alpha');
  await expect(page.locator('.train-direction-chip')).toBeHidden();
  const card = page.locator('.train-dock .marker-card');
  await expect(card).toBeVisible();
  await expect(card).toHaveAttribute('data-state', 'active');
  await expect(page.locator('.route-map .marker-card')).toHaveCount(0);
  await checkbox.focus();
  await page.keyboard.press('Escape');
  await expect(trigger).toBeFocused();
  await expect(page.locator('#map-options')).toBeHidden();
  await trigger.click();
  await expect(checkbox).toBeChecked();
  await expect(page.getByRole('radio', { name: 'Next station' })).toBeChecked();
  await page.getByRole('button', { name: 'Close', exact: true }).click();
  await expect(trigger).toBeFocused();
});

for (const width of [700, 390, 320]) test(`phone map has no new direction controls at ${width}px`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 });
  await page.goto(harness);
  await expect(page.locator('.train-marker')).toHaveCount(1);
  await expect(page.getByRole('button', { name: 'Map options' })).toBeHidden();
  await expect(page.locator('.train-directions')).toBeHidden();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('polling preserves static cues for stopped and simulated trains, and missing names fall back', async ({ page }) => {
  let next: Positions = { ...positions, trains: [midway] };
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: next }));
  await page.clock.install();
  await page.goto(harness);
  await expect(page.locator('.train-direction-dot')).toHaveCount(3);
  next = { ...positions, trains: [{ ...midway, movement_state: 'reported_stopped', position_basis: 'schedule_fallback',
    next_station_id: 'missing', fallback_reason: 'stale_realtime' }] };
  await page.clock.runFor(5000);
  await expect(page.locator('.train-marker')).toHaveAttribute('data-simulation', 'true');
  await expect(page.locator('.train-direction-dot')).toHaveCount(3);
  await expect(page.locator('.train-direction-chip')).toHaveText('Toward Alpha');
  await page.getByRole('button', { name: 'Map options' }).click();
  await page.getByRole('radio', { name: 'Next station' }).check();
  await expect(page.locator('.train-direction-chip')).toHaveText('Toward Alpha');
  next = { ...positions, trains: [{ ...midway, latitude: 40.75, longitude: -73.99 }] };
  await page.clock.runFor(5000);
  await expect(page.locator('.train-direction-dot')).toHaveCount(0);
  await expect(page.locator('.train-direction-chip')).toHaveText('Next: Alpha');
  next = { ...positions, trains: [{ ...midway, latitude: 40.73, longitude: -73.98, shape_distance_meters: 0 }] };
  await page.clock.runFor(5000);
  await expect(page.locator('.train-direction-dot')).toHaveCount(0);
});

test('missing terminal falls back to the full next station name, then hides the chip when both names are missing', async ({ page }) => {
  const longName = 'Far Rockaway — Mott Avenue and a Very Long Terminal Name';
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: {
    ...geometry,
    stations: geometry.stations.map(station => station.id === 'S4' ? { ...station, properties: { ...station.properties, name: longName } } : station),
    patterns: geometry.patterns.map(pattern => ({ ...pattern, stops: pattern.stops.map((stop, index) => index === pattern.stops.length - 1
      ? { ...stop, station_id: 'missing' } : stop) })),
  } }));
  let next: Positions = { ...positions, trains: [midway] };
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: next }));
  await page.clock.install();
  await page.goto(harness);
  const chip = page.locator('.train-direction-chip');
  await expect(chip).toHaveText(`Next: ${longName}`);
  await expect(page.locator('.train-marker')).toHaveAttribute('aria-label', new RegExp(longName));
  expect(await chip.evaluate(element => element.scrollWidth > element.clientWidth)).toBe(true);
  next = { ...positions, trains: [{ ...midway, next_station_id: 'missing' }] };
  await page.clock.runFor(5000);
  await expect(chip).toHaveCount(0);
  await expect(page.locator('.train-direction-dot')).toHaveCount(3);
});
