import { expect, test, type Page } from '@playwright/test';
import { basemap, geometry } from './fixtures/geometry';
import { positions, train } from './fixtures/positions';
import type { Positions, Train } from '../src/positions';

const harness = 'http://127.0.0.1:4174/e2e/harness.html';
const west: Train = { ...train, direction: 'northbound', longitude: -73.99, latitude: 40.72, shape_distance_meters: 1400 };
const east: Train = { ...train, trip_instance_id: 'run-two', source_record_id: 'record-two', direction: 'southbound',
  longitude: -73.94, latitude: 40.71, shape_id: 'east', service_pattern_id: 'pattern-east', shape_distance_meters: 3500,
  next_station_id: 'S3' };
const third: Train = { ...west, trip_instance_id: 'run-three', source_record_id: 'record-three', longitude: -73.965,
  shape_distance_meters: 2400 };
const snapshot: Positions = { ...positions, trains: [west, east, third] };
const branches = { ...geometry, patterns: [...geometry.patterns, {
  ...geometry.patterns[0]!, service_pattern_id: 'pattern-east', shape_id: 'east', stops: [
    geometry.patterns[0]!.stops[0]!,
    { ...geometry.patterns[0]!.stops[1]!, stop_id: 'S3S', station_id: 'S3', distance_meters: 6500 },
  ],
}] };
const listButton = (page: Page, run: string) => page.getByRole('button', { name: `Run ${run}`, exact: true });
const marker = (page: Page, run: string) => page.locator(`.train-marker[data-identity=${JSON.stringify(`run:${JSON.stringify(run)}`)}]`);
const dockCards = (page: Page) => page.locator('.train-dock .marker-card');
const phases = (page: Page) => page.evaluate(() => [...new Set(window.testMap.querySourceFeatures('selected-route').map(feature => feature.properties.phase))].sort());

async function start(page: Page) {
  await page.goto(harness);
  await expect(page.locator('.train-list > li')).toHaveCount(3);
  await expect(page.locator('.train-marker')).toHaveCount(3);
}

test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.route('https://tiles.openfreemap.org/**', route => route.fulfill({ json: basemap }));
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: branches }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: snapshot }));
});

test('two pins keep order, one active journey, toggle removal, and clear all', async ({ page }) => {
  await start(page);
  const camera = await page.evaluate(() => ({ center: window.testMap.getCenter().toArray(), zoom: window.testMap.getZoom() }));
  await listButton(page, 'run-one').click();
  await listButton(page, 'run-two').click();
  await expect(dockCards(page)).toHaveCount(2);
  await expect(dockCards(page).nth(0)).toHaveAttribute('data-state', 'inactive');
  await expect(dockCards(page).nth(0)).toHaveAttribute('data-compact', 'true');
  await expect(dockCards(page).nth(1)).toHaveAttribute('data-state', 'active');
  await expect(marker(page, 'run-one')).toHaveAttribute('aria-pressed', 'true');
  await expect(marker(page, 'run-two')).toHaveAttribute('aria-pressed', 'true');
  await expect(marker(page, 'run-two')).toHaveAttribute('aria-label', /active journey/);
  await expect.poll(() => phases(page)).toEqual(['upcoming', 'visited']);
  expect(await page.evaluate(() => ({ center: window.testMap.getCenter().toArray(), zoom: window.testMap.getZoom() }))).toEqual(camera);

  await dockCards(page).nth(0).getByRole('button', { name: 'Show journey' }).click();
  await expect(dockCards(page).nth(0)).toHaveAttribute('data-state', 'active');
  await expect(marker(page, 'run-one')).toHaveAttribute('aria-label', /active journey/);
  await listButton(page, 'run-one').click();
  await expect(dockCards(page)).toHaveCount(1);
  await expect(dockCards(page)).toContainText('2 St');
  await expect(marker(page, 'run-one')).toHaveAttribute('aria-pressed', 'false');
  await page.getByRole('button', { name: 'Clear all' }).click();
  await expect(dockCards(page)).toHaveCount(0);
  await expect(marker(page, 'run-two')).toHaveAttribute('aria-pressed', 'false');
  await expect.poll(() => phases(page)).toEqual([]);
});

test('marker and card previews override then restore the active journey without clearing pins', async ({ page }) => {
  await start(page);
  await listButton(page, 'run-one').click();
  await listButton(page, 'run-two').click();
  await marker(page, 'run-one').hover();
  await expect(page.locator('.route-map .marker-card')).toContainText('Northbound to Alpha');
  await expect(dockCards(page)).toHaveCount(2);
  await expect(dockCards(page).nth(0)).toHaveAttribute('data-compact', 'true');
  await expect(dockCards(page).nth(1)).toHaveAttribute('data-compact', 'true');
  await expect(page.locator('.next-stop-label')).toHaveText(/Next: Alpha\s+Toward Alpha/);
  await page.keyboard.press('Escape');
  await expect(page.locator('.route-map .marker-card')).toHaveCount(0);
  await expect(dockCards(page).nth(1)).toHaveAttribute('data-state', 'active');
  await expect(listButton(page, 'run-one')).toHaveAttribute('aria-pressed', 'true');
  await expect(listButton(page, 'run-two')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.next-stop-label')).toHaveText(/Next: 2 St\s+Toward 2 St/);

  await marker(page, 'run-one').hover();
  const mapBox = (await page.locator('.route-map').boundingBox())!;
  await page.mouse.click(mapBox.x + 8, mapBox.y + 8);
  await expect(page.locator('.route-map .marker-card')).toHaveCount(0);
  await expect(listButton(page, 'run-one')).toHaveAttribute('aria-pressed', 'true');
  await expect(listButton(page, 'run-two')).toHaveAttribute('aria-pressed', 'true');

  await dockCards(page).nth(0).getByRole('button', { name: 'Show journey' }).focus();
  await expect(dockCards(page).nth(0)).toHaveAttribute('data-state', 'preview');
  await expect(page.locator('.next-stop-label')).toHaveText(/Next: Alpha\s+Toward Alpha/);
  await page.getByRole('button', { name: 'Zoom in' }).focus();
  await page.waitForTimeout(200);
  await expect(dockCards(page).nth(1)).toHaveAttribute('data-state', 'active');
  await expect(page.locator('.next-stop-label')).toHaveText(/Next: 2 St\s+Toward 2 St/);
});

test('third pin replacement is modal, preserves the chosen slot, and restores trigger focus', async ({ page }, testInfo) => {
  await start(page);
  await listButton(page, 'run-one').click();
  await listButton(page, 'run-two').click();
  const trigger = listButton(page, 'run-three');
  await trigger.click();
  const dialog = page.getByRole('dialog', { name: 'Replace a pinned train' });
  await expect(dialog).toBeVisible();
  const choices = dialog.locator('.replacement-choice');
  await expect(dialog.getByRole('region', { name: 'New train to pin' })).toContainText('Coming upANorthboundNext station Alpha');
  await expect(dialog.locator('.replacement-line-marker')).toHaveCount(3);
  await expect(dialog.locator('.replacement-line-marker').first()).toHaveCSS('background-color', 'rgb(0, 57, 166)');
  await expect(dialog.locator('.replacement-arrow svg')).toHaveCount(2);
  await expect(choices).toHaveCount(2);
  await expect(choices.nth(0)).toHaveAccessibleName('Replace Northbound, next station Alpha');
  await expect(choices.nth(1)).toHaveAccessibleName('Replace Southbound, next station 2 St');
  await expect(choices.nth(0)).toBeFocused();
  await expect(dialog).not.toContainText(/run-one|run-two|top pin|bottom pin/i);
  await expect(dialog.locator('.replacement-arrow').nth(0)).toHaveCSS('opacity', '0');
  await expect(dialog.locator('.replacement-arrow').nth(1)).toHaveCSS('opacity', '0');
  await choices.nth(1).hover();
  await expect(dialog.locator('.replacement-arrow').nth(0)).toHaveCSS('opacity', '0');
  await expect(dialog.locator('.replacement-arrow').nth(1)).toHaveCSS('opacity', '1');
  await dialog.screenshot({ path: testInfo.outputPath('replacement-dialog.png') });
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();

  await trigger.click();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();

  await trigger.click();
  await choices.nth(0).click();
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
  await expect(dockCards(page).nth(0)).toHaveAttribute('data-identity', 'run:"run-three"');
  await expect(dockCards(page).nth(1)).toHaveAttribute('data-identity', 'run:"run-two"');
  await expect(dockCards(page).nth(0)).toHaveAttribute('data-state', 'active');

  await listButton(page, 'run-one').click();
  await choices.nth(1).click();
  await expect(dockCards(page).nth(0)).toHaveAttribute('data-identity', 'run:"run-three"');
  await expect(dockCards(page).nth(1)).toHaveAttribute('data-identity', 'run:"run-one"');
  await expect(dockCards(page).nth(1)).toHaveAttribute('data-state', 'active');
});

test('polling cancels a missing candidate and directly fills a vacated pin slot', async ({ page }) => {
  let next = snapshot;
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: next }));
  await page.clock.install();
  await start(page);
  await listButton(page, 'run-one').click();
  await listButton(page, 'run-two').click();
  await listButton(page, 'run-three').click();
  const dialog = page.getByRole('dialog', { name: 'Replace a pinned train' });
  await expect(dialog).toBeVisible();
  next = { ...snapshot, trains: [west, east] };
  await page.clock.runFor(5000);
  await expect(dialog).toBeHidden();
  await expect(dockCards(page)).toHaveCount(2);
  await expect(page.locator('[aria-live="polite"]')).toContainText('awaiting replacement was removed');

  next = snapshot;
  await page.clock.runFor(5000);
  await listButton(page, 'run-three').click();
  await expect(dialog).toBeVisible();
  next = { ...snapshot, trains: [east, third] };
  await page.clock.runFor(5000);
  await expect(dialog).toBeHidden();
  await expect(dockCards(page).nth(0)).toHaveAttribute('data-identity', 'run:"run-three"');
  await expect(dockCards(page).nth(1)).toHaveAttribute('data-identity', 'run:"run-two"');
  await expect(dockCards(page).nth(0)).toHaveAttribute('data-state', 'active');
});

test('resizing to phone keeps only the active train and widening docks it again', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await start(page);
  await listButton(page, 'run-one').click();
  await listButton(page, 'run-two').click();
  for (const width of [1280, 1024, 768]) {
    await page.setViewportSize({ width, height: 1000 });
    await expect(dockCards(page)).toHaveCount(2);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  }
  await page.setViewportSize({ width: 700, height: 1000 });
  await expect(dockCards(page)).toHaveCount(0);
  await expect(listButton(page, 'run-one')).toHaveAttribute('aria-pressed', 'false');
  await expect(listButton(page, 'run-two')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('[aria-live="polite"]')).toContainText('phone layouts support one selected train');
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  }
  await page.setViewportSize({ width: 701, height: 1000 });
  await expect(dockCards(page)).toHaveCount(1);
  await expect(dockCards(page)).toContainText('2 St');
});

test('a constrained desktop map uses the dock fallback without moving the camera', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 1000 });
  await start(page);
  await listButton(page, 'run-one').click();
  await listButton(page, 'run-two').click();
  await page.addStyleTag({ content: '.route-map { width: 160px !important; height: 120px !important; }' });
  await page.evaluate(item => new Promise<void>(resolve => {
    window.testMap.once('idle', () => resolve());
    window.testMap.resize();
    window.testMap.jumpTo({ center: [item.longitude!, item.latitude!], zoom: 13 });
  }), west);
  const before = await page.evaluate(() => ({ center: window.testMap.getCenter().toArray(), zoom: window.testMap.getZoom() }));
  await marker(page, 'run-one').hover();
  await expect(page.locator('.marker-preview-panel .marker-card')).toHaveAttribute('data-state', 'preview');
  await expect(page.locator('.route-map .marker-card')).toHaveCount(0);
  expect(await page.evaluate(() => ({ center: window.testMap.getCenter().toArray(), zoom: window.testMap.getZoom() }))).toEqual(before);
  await page.keyboard.press('Escape');
  await expect(page.locator('.marker-preview-panel .marker-card')).toHaveCount(0);
  await expect(dockCards(page)).toHaveCount(2);
});

test('retained failures keep pins and version mismatch suppresses only map rendering', async ({ page }) => {
  let fail = false;
  let next: Positions = snapshot;
  await page.route('**/api/v1/trains?*', route => fail ? route.abort('failed') : route.fulfill({ json: next }));
  await page.clock.install();
  await start(page);
  await listButton(page, 'run-one').click();
  await listButton(page, 'run-two').click();
  const order = () => dockCards(page).evaluateAll(cards => cards.map(card => (card as HTMLElement).dataset.identity));
  const initialOrder = await order();
  fail = true;
  await page.clock.runFor(5000);
  await expect(page.locator('[aria-live="polite"]')).toContainText('could not be reached');
  expect(await order()).toEqual(initialOrder);

  fail = false;
  next = { ...snapshot, schedule_version: 'v2', trains: snapshot.trains.map(item => ({ ...item, schedule_version: 'v2' })) };
  await page.clock.runFor(5000);
  await expect(page.locator('[aria-live="polite"]')).toContainText('Schedule versions are incompatible');
  await expect(page.locator('.train-marker')).toHaveCount(0);
  await expect(dockCards(page)).toHaveCount(2);
  expect(await order()).toEqual(initialOrder);
  await expect(dockCards(page).nth(1)).toHaveAttribute('data-state', 'active');
  await expect.poll(() => phases(page)).toEqual([]);
});
