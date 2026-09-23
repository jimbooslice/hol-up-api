import { expect, test, type Page } from '@playwright/test';
import { basemap, geometry } from './fixtures/geometry';
import { positions, train } from './fixtures/positions';
import type { Train } from '../src/positions';

const harness = 'http://127.0.0.1:4174/e2e/harness.html';
const trains: Train[] = [
  { ...train, latitude: 40.72, longitude: -73.99, direction: 'northbound', trip_instance_id: 'one' },
  { ...train, latitude: 40.72, longitude: -73.99, direction: 'southbound', trip_instance_id: 'two' },
  { ...train, latitude: 40.72, longitude: -73.99, direction: 'unknown', trip_instance_id: 'three', position_basis: 'schedule_fallback', next_arrival_at: null },
];
const identity = (run: string) => `run:${JSON.stringify(run)}`;
const marker = (page: Page, run: string) => page.locator(`.train-marker[data-identity=${JSON.stringify(identity(run))}]`);
const listButton = (page: Page, run: string) => page.getByRole('button', { name: `Run ${run}`, exact: true });

test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.route('https://tiles.openfreemap.org/**', route => route.fulfill({ json: basemap }));
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: geometry }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: { ...positions, trains } }));
});

async function start(page: Page) {
  await page.goto(harness);
  await expect(page.locator('.train-marker')).toHaveCount(3);
  await marker(page, 'one').scrollIntoViewIfNeeded();
}

test('coincident trains remain separate native targets without counters or a chooser', async ({ page }) => {
  await start(page);
  await expect(marker(page, 'one')).toHaveAttribute('data-identity', identity('one'));
  await expect(marker(page, 'two')).toHaveAttribute('data-identity', identity('two'));
  await expect(marker(page, 'three')).toHaveAttribute('data-simulation', 'true');
  await expect(marker(page, 'one')).not.toHaveAttribute('data-group');
  await expect(page.locator('.train-chooser')).toHaveCount(0);
  await expect(page.getByRole('button', { name: /trains nearby|close train chooser/i })).toHaveCount(0);
  await expect(page.locator('.train-marker-badge')).toHaveText(['A', 'A', 'A']);
  const centers = await page.locator('.train-marker').evaluateAll(items => items.map(item => {
    const box = item.getBoundingClientRect();
    return [box.left + box.width / 2, box.top + box.height / 2];
  }));
  expect(centers.every(center => center[0] === centers[0]![0] && center[1] === centers[0]![1])).toBe(true);
});

test('keyboard and list selection reach an obscured train, and polling refreshes it', async ({ page }) => {
  await page.clock.install();
  let current = trains;
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: { ...positions, trains: current } }));
  await start(page);
  await listButton(page, 'two').click();
  await expect(marker(page, 'two')).toHaveAttribute('aria-pressed', 'true');
  await marker(page, 'three').focus();
  await page.keyboard.press('Enter');
  await expect(listButton(page, 'three')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.marker-card')).toContainText('Schedule estimate · not realtime');
  current = [...trains].reverse().map(item => item.trip_instance_id === 'three' ? { ...item, next_arrival_at: '2026-09-14T12:10:00Z' } : item);
  await page.clock.runFor(5_000);
  await expect(marker(page, 'three')).toBeFocused();
  await expect(page.locator('.marker-card')).toContainText('8:10 AM EDT');
});

for (const width of [1920, 1440, 1024, 768, 390, 320]) test(`responsive overlapping markers at ${width}px`, async ({ page }, testInfo) => {
  await page.setViewportSize({ width, height: 1000 });
  await start(page);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  for (const run of ['one', 'two', 'three']) {
    const box = await marker(page, run).boundingBox();
    expect(box!.width).toBeGreaterThanOrEqual(44);
    expect(box!.height).toBeGreaterThanOrEqual(44);
  }
  await page.locator('.map-panel').screenshot({ path: testInfo.outputPath(`collision-${width}.png`) });
});
