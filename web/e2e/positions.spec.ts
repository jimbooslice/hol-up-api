import { test, expect, type Page } from '@playwright/test';
import { basemap, geometry } from './fixtures/geometry';
import { positions, train, withVersion } from './fixtures/positions';
import type { Positions } from '../src/positions';

const harness = 'http://127.0.0.1:4174/e2e/harness.html';
const trainItems = (page: Page) => page.locator('.train-list > li');
const notice = (page: Page) => page.locator('[aria-live="polite"][aria-atomic="true"]');
async function markers(page: Page) {
  return page.locator('.train-marker').evaluateAll(items => items.map(item => (item as HTMLElement).dataset.identity));
}
async function markerPaint(page: Page) {
  return page.locator('.train-marker-badge').evaluate(element => ({
    background: getComputedStyle(element).backgroundColor,
    border: getComputedStyle(element).borderColor,
    outline: getComputedStyle(element).outlineColor,
  }));
}
async function start(page: Page) {
  await page.goto(harness);
  await expect(trainItems(page)).toHaveCount(1);
  await expect.poll(() => markers(page)).toHaveLength(1);
}
async function hidden(page: Page, value: boolean) {
  await page.evaluate(value => {
    Object.defineProperty(document, 'hidden', { configurable: true, value });
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: value ? 'hidden' : 'visible' });
    document.dispatchEvent(new Event('visibilitychange'));
  }, value);
}
test.beforeEach(async ({ page }) => {
  await page.route('https://tiles.openfreemap.org/**', route => route.fulfill({ json: basemap }));
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: geometry }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: positions }));
});

test('complete outcomes, escaped text, selection and focus survive replacement; removed selection is announced', async ({ page }) => {
  let next: Positions = { ...positions, trains: [train, { ...train, trip_instance_id: null, source_record_id: null,
    entity_id: '<script>unexpected()</script>', latitude: null, longitude: null, shape_distance_meters: null,
    next_station_id: null, next_stop_sequence: null, next_arrival_at: null, no_position_reason: '<img src=x onerror=unexpected()>' }] };
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: next }));
  await page.clock.install();
  await page.goto(harness);
  await expect(trainItems(page)).toHaveCount(2);
  await expect(page.getByText('Position unavailable: The API reported <img src=x onerror=unexpected()>.', { exact: false })).toBeVisible();
  await expect(page.locator('.train-list img, .train-list script')).toHaveCount(0);
  const button = page.getByRole('button', { name: 'Run run-one', exact: true });
  await button.click();
  await expect(page.getByRole('region', { name: 'Pinned train comparison' })).toContainText('Active journey');
  next = { ...next, trains: [...next.trains].reverse() };
  await page.clock.runFor(5000);
  await expect(button).toBeFocused();
  await expect(button).toHaveAttribute('aria-pressed', 'true');
  next = { ...positions, trains: [] };
  await page.clock.runFor(5000);
  await expect(trainItems(page)).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'Trains', exact: true })).toBeFocused();
  await expect(notice(page)).toContainText('pinned train was removed');
  await expect.poll(() => markers(page)).toHaveLength(0);
});

test('map clicks share selection; refresh and explicit fit preserve one map and user camera', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await start(page);
  expect(await markerPaint(page)).toEqual({
    background: 'rgb(0, 57, 166)',
    border: 'rgb(0, 57, 166)',
    outline: 'rgb(86, 100, 119)',
  });
  await expect(page.getByText('1 of 1 train outcomes is positioned on the map.')).toBeVisible();
  await page.locator('.route-map').scrollIntoViewIfNeeded();
  const point = await page.evaluate(() => window.testMap.project([-73.98, 40.73]));
  const box = (await page.locator('.route-map').boundingBox())!;
  await page.mouse.move(box.x + point.x, box.y + point.y);
  const card = page.locator('.marker-card');
  await expect(card).toContainText('A to Alpha');
  await expect(card).toContainText('Realtime estimate · not GPS');
  await expect(card).toContainText('Next stationAlpha');
  await expect(card).toContainText('8:05 AM EDT · in 5 min');
  await expect(card.getByRole('button', { name: 'Close train details' })).toHaveCount(0);
  await page.mouse.click(box.x + point.x, box.y + point.y);
  await expect(page.getByRole('button', { name: 'Run run-one' })).toHaveAttribute('aria-pressed', 'true');
  const dock = page.locator('.train-dock .marker-card');
  await expect(page.locator('.route-map .marker-card')).toHaveCount(0);
  await expect(dock).toHaveAttribute('data-state', 'active');
  await expect.poll(() => page.evaluate(() => [...new Set(window.testMap.querySourceFeatures('selected-route').map(feature => feature.properties.phase))])).toEqual(['upcoming']);
  await expect.poll(() => page.evaluate(() => [...new Set(window.testMap.querySourceFeatures('selected-stops').map(feature => `${feature.properties.phase}:${feature.properties.next}`))])).toEqual(['visited:false', 'upcoming:true']);
  await page.getByRole('button', { name: 'Zoom in', exact: true }).click();
  const camera = await page.evaluate(() => ({ center: window.testMap.getCenter(), zoom: window.testMap.getZoom(), count: window.mapCount }));
  await page.clock.install();
  await page.clock.runFor(5000);
  expect(await page.evaluate(() => ({ center: window.testMap.getCenter(), zoom: window.testMap.getZoom(), count: window.mapCount }))).toEqual(camera);
  await page.getByRole('button', { name: 'Fit A line' }).click();
  const fitted = await page.evaluate(() => ({ center: window.testMap.getCenter(), zoom: window.testMap.getZoom() }));
  expect(fitted.zoom).toBeLessThanOrEqual(camera.zoom);
  expect(fitted.center).not.toEqual(camera.center);
  await dock.getByRole('button', { name: 'Remove' }).click();
  await expect(page.getByRole('button', { name: 'Run run-one' })).toHaveAttribute('aria-pressed', 'false');
  await expect(card).toHaveCount(0);
});

test('desktop Escape and empty map preserve pins while pin toggles clear route selection', async ({ page }) => {
  await start(page);
  const button = page.getByRole('button', { name: 'Run run-one' });
  await button.click();
  await page.keyboard.press('Escape');
  await expect(button).toHaveAttribute('aria-pressed', 'true');
  await button.click();
  await expect(button).toHaveAttribute('aria-pressed', 'false');
  await expect.poll(() => markers(page)).toHaveLength(1);
  await page.locator('.route-map').scrollIntoViewIfNeeded();
  const box = (await page.locator('.route-map').boundingBox())!;
  const point = await page.evaluate(() => window.testMap.project([-73.98, 40.73]));
  await page.mouse.click(box.x + point.x, box.y + point.y);
  await expect(button).toHaveAttribute('aria-pressed', 'true');
  await page.mouse.click(box.x + 8, box.y + 8);
  await expect(button).toHaveAttribute('aria-pressed', 'true');
  await page.mouse.click(box.x + point.x, box.y + point.y);
  await expect(button).toHaveAttribute('aria-pressed', 'false');
  await expect.poll(() => page.evaluate(() => window.testMap.querySourceFeatures('selected-route'))).toEqual([]);
});

test('slow requests do not overlap and retries start five seconds after completion', async ({ page }) => {
  await page.clock.install();
  let requests = 0;
  let release: (() => Promise<void>) | undefined;
  await page.route('**/api/v1/trains?*', route => { requests++; release = () => route.fulfill({ json: positions }); });
  await page.goto(harness);
  await expect.poll(() => requests).toBe(1);
  await page.clock.runFor(9000);
  await page.clock.pauseAt(await page.evaluate(() => Date.now() + 100));
  expect(requests).toBe(1);
  await release!();
  await expect(trainItems(page)).toHaveCount(1);
  await page.clock.runFor(4999);
  expect(requests).toBe(1);
  await page.clock.runFor(1);
  await expect.poll(() => requests).toBe(2);
});

test('body-read deadline retains snapshot and recovers without overlapping', async ({ page }) => {
  await page.addInitScript(() => {
    const fetch = window.fetch;
    let calls = 0;
    window.fetch = async (input, init) => {
      const response = await fetch(input, init);
      if (String(input).includes('/trains') && ++calls === 2) {
        response.json = () => new Promise((_resolve, reject) => init!.signal!.addEventListener('abort', () => reject(init!.signal!.reason), { once: true }));
      }
      return response;
    };
  });
  await page.clock.install();
  await start(page);
  await page.clock.runFor(5000);
  await page.clock.runFor(10000);
  await expect(notice(page)).toContainText('Train request timed out');
  await expect(trainItems(page)).toHaveCount(1);
  await expect(page.getByText('Last received positions,', { exact: false })).toBeVisible();
  await page.clock.runFor(5000);
  await expect(notice(page)).toContainText('Train updates available');
});

for (const action of ['pause', 'hidden', 'unmount'] as const) test(`${action} aborts active work and prevents late completion from restarting polling`, async ({ page }) => {
  await page.addInitScript(() => {
    const fetch = window.fetch;
    Object.assign(window, { trainAborted: false });
    window.fetch = (input, init) => {
      if (String(input).includes('/trains')) init!.signal!.addEventListener('abort', () => Object.assign(window, { trainAborted: true }), { once: true });
      return fetch(input, init);
    };
  });
  await page.clock.install();
  let count = 0;
  let release: (() => Promise<void>) | undefined;
  await page.route('**/api/v1/trains?*', route => {
    count++;
    if (count === 1) return route.fulfill({ json: positions });
    release = () => route.fulfill({ json: { ...positions, trains: [] } }).catch(() => {});
  });
  await start(page);
  await page.clock.runFor(5000);
  await expect.poll(() => count).toBe(2);
  if (action === 'pause') await page.getByRole('button', { name: 'Pause updates' }).click();
  if (action === 'hidden') await hidden(page, true);
  if (action === 'unmount') await page.evaluate(() => window.unmountApp());
  await expect.poll(() => page.evaluate(() => Reflect.get(window, 'trainAborted'))).toBe(true);
  await release!();
  await page.clock.runFor(30000);
  expect(count).toBe(2);
  if (action !== 'unmount') {
    await expect(trainItems(page)).toHaveCount(1);
    if (action === 'pause') await page.getByRole('button', { name: 'Resume updates' }).click();
    else await hidden(page, false);
    await expect.poll(() => count).toBe(3);
    await expect(page.getByText('Last received positions,', { exact: false })).toBeVisible();
    await release!();
    await expect(trainItems(page)).toHaveCount(0);
  }
});

test('pause freezes age ticks, hidden restoration respects pause, resume recomputes signed age', async ({ page }) => {
  let next: Positions = { ...positions, trains: [{ ...train, source_age_seconds: -10, position_basis: 'schedule_fallback' }] };
  let count = 0;
  await page.route('**/api/v1/trains?*', route => { count++; return route.fulfill({ json: next }); });
  await page.clock.install();
  await start(page);
  await expect(trainItems(page)).toContainText('Source clock is approximately');
  await page.getByRole('button', { name: 'Pause updates' }).click();
  const frozen = await trainItems(page).textContent();
  await hidden(page, true);
  await page.clock.runFor(80000);
  await hidden(page, false);
  expect(await trainItems(page).textContent()).toBe(frozen);
  expect(count).toBe(1);
  next = { ...positions, trains: [{ ...train, position_basis: 'schedule_fallback' }] };
  await page.route('**/api/v1/trains?*', () => {});
  await page.getByRole('button', { name: 'Resume updates' }).click();
  await expect(trainItems(page)).toContainText('Stale source evidence');
  await expect(trainItems(page)).toContainText('Was a schedule simulation');
});

test('failures retain coordinates, age evidence, and do not announce each age tick', async ({ page }) => {
  let fail = false;
  await page.route('**/api/v1/trains?*', route => fail ? route.abort('failed') : route.fulfill({ json: positions }));
  await page.clock.install();
  await start(page);
  const before = await markers(page);
  fail = true;
  await page.clock.runFor(5000);
  await expect(notice(page)).toContainText('could not be reached');
  const text = await notice(page).textContent();
  await page.clock.runFor(1000);
  expect(await notice(page).textContent()).toBe(text);
  await page.clock.runFor(50000);
  await expect(notice(page)).toContainText('Source evidence is stale');
  expect(await markers(page)).toEqual(before);
});

test('version mismatch hides markers; failed geometry retains route; repeated changes use scheduled cycles', async ({ page }) => {
  await page.clock.install();
  await start(page);
  const camera = await page.evaluate(() => ({ center: window.testMap.getCenter(), zoom: window.testMap.getZoom(), count: window.mapCount }));
  let next = withVersion('v2');
  let geometryCalls = 0;
  let release: (() => Promise<void>) | undefined;
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: next }));
  await page.route('**/api/v1/routes/A/geometry', route => { geometryCalls++; release = () => route.fulfill({ status: 503, json: { error: { code: 'schedule_unavailable' } } }); });
  await page.clock.runFor(5000);
  await expect(notice(page)).toContainText('Schedule versions are incompatible');
  await expect.poll(() => markers(page)).toHaveLength(0);
  await release!();
  await expect(page.getByRole('alert')).toContainText('retaining previous geometry');
  await expect(page.locator('.station-list li')).toHaveCount(6);
  expect(geometryCalls).toBe(1);
  await page.route('**/api/v1/routes/A/geometry', route => { geometryCalls++; next = withVersion('v3'); return route.fulfill({ json: { ...geometry, schedule_version: 'v2' } }); });
  await page.clock.runFor(5000);
  await expect.poll(() => geometryCalls).toBe(2);
  await expect(notice(page)).toContainText('Schedule versions are incompatible');
  await expect.poll(() => markers(page)).toHaveLength(0);
  await page.clock.runFor(4999);
  expect(geometryCalls).toBe(2);
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: { ...geometry, schedule_version: 'v3' } }));
  await page.clock.runFor(1);
  await expect.poll(() => markers(page)).toHaveLength(1);
  expect(await page.evaluate(() => ({ center: window.testMap.getCenter(), zoom: window.testMap.getZoom(), count: window.mapCount }))).toEqual(camera);
});

test('fallback during delayed source refresh cannot reveal obsolete markers', async ({ page }) => {
  await start(page);
  await page.clock.install();
  await page.evaluate(() => { window.holdSources = true; });
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: withVersion('v2') }));
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: { ...geometry, schedule_version: 'v2' } }));
  await page.clock.runFor(5000);
  await expect.poll(() => markers(page)).toHaveLength(0);
  await page.evaluate(() => window.testMap.fire('error', { error: new Error('fixture basemap failure') }));
  await expect(page.getByText('Basemap unavailable.', { exact: false })).toBeVisible();
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: { ...withVersion('v2'), trains: [] } }));
  await page.clock.runFor(5000);
  await expect(trainItems(page)).toHaveCount(0);
  await page.evaluate(() => window.releaseSources());
  await expect.poll(() => markers(page)).toHaveLength(0);
  await expect(page.getByText('A-line map ready.', { exact: false })).toBeVisible();
  expect(await page.evaluate(() => window.mapCount)).toBe(1);
});

test('geometry failure still polls HTML outcomes; null versions hide markers and manual retry recovers', async ({ page }) => {
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ status: 503, json: { error: { code: 'schedule_unavailable' } } }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: withVersion(null) }));
  await page.goto(harness);
  await expect(trainItems(page)).toHaveCount(1);
  await expect(notice(page)).toContainText('markers are hidden');
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: geometry }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: positions }));
  await page.getByRole('button', { name: 'Try again' }).click();
  await expect.poll(() => markers(page)).toHaveLength(1);
});

for (const width of [1440, 390, 320]) test(`train evidence and controls fit ${width}px without WebGL`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 });
  await page.addInitScript(() => {
    const original = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement, ...args: Parameters<typeof original>) {
      return String(args[0]).includes('webgl') ? null : original.apply(this, args);
    } as typeof original;
  });
  await page.goto(harness);
  await expect(trainItems(page)).toHaveCount(1);
  await expect(page.getByText('Map unavailable.', { exact: false })).toBeVisible();
  await page.getByRole('button', { name: 'Run run-one' }).click();
  if (width > 700) await expect(page.getByRole('region', { name: 'Pinned train comparison' })).toContainText('Active journey');
  else await expect(page.getByRole('region', { name: 'Train details' })).toContainText('Observation age');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('null coordinates remove a marker while keeping its selected outcome and reason', async ({ page }) => {
  await page.clock.install();
  await start(page);
  await page.getByRole('button', { name: 'Run run-one' }).click();
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: { ...positions,
    trains: [{ ...train, latitude: null, longitude: null, shape_distance_meters: null,
      next_station_id: null, next_stop_sequence: null, next_arrival_at: null, no_position_reason: 'insufficient_timing' }] } }));
  await page.clock.runFor(5000);
  await expect(trainItems(page)).toContainText('Position unavailable: No trustworthy arrival/departure interval');
  await expect(page.getByText('0 of 1 train outcomes are positioned on the map.')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Why some trains are not on the map' })).toBeVisible();
  await expect(page.locator('.position-warning')).toContainText('1 train: No trustworthy arrival/departure interval');
  await expect(page.getByRole('button', { name: 'Run run-one' })).toBeFocused();
  await expect.poll(() => markers(page)).toHaveLength(0);
  await expect(trainItems(page)).toContainText('insufficient_timing');
  await expect(page.getByRole('region', { name: 'Pinned train comparison' })).toContainText('Next stationUnavailable');
});

for (const [status, body, message] of [
  [200, { ...positions, trains: [train, train] }, 'Train response failed validation'],
  [503, { error: { code: 'source_unavailable' } }, 'Realtime source unavailable'],
  [503, { error: { code: 'schedule_unavailable' } }, 'Train schedule unavailable'],
  [500, {}, 'Train service returned an HTTP error'],
] as const) test(`retains snapshot on ${message} and recovers`, async ({ page }) => {
  await page.clock.install();
  await start(page);
  await page.route('**/api/v1/trains?*', route => route.fulfill({ status, json: body }));
  await page.clock.runFor(5000);
  await expect(notice(page)).toContainText(message);
  await expect(trainItems(page)).toHaveCount(1);
  await expect.poll(() => markers(page)).toHaveLength(1);
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: positions }));
  await page.clock.runFor(5000);
  await expect(notice(page)).toContainText('Train updates available');
});

test('Strict Mode cleanup aborts the first geometry request and ignores its late body', async ({ page }) => {
  await page.addInitScript(() => {
    const fetch = window.fetch;
    let first = true;
    Object.assign(window, { firstAborted: false, releaseFirst: () => {} });
    window.fetch = async (input, init) => {
      if (String(input).includes('/geometry') && first) {
        first = false;
        init!.signal!.addEventListener('abort', () => Object.assign(window, { firstAborted: true }), { once: true });
        return { ok: true, json: () => new Promise(resolve => Object.assign(window, {
          releaseFirst: () => resolve({ schedule_version: 'obsolete-invalid' }),
        })) } as Response;
      }
      return fetch(input, init);
    };
  });
  await page.clock.install();
  let positionsCalls = 0;
  await page.route('**/api/v1/trains?*', route => { positionsCalls++; return route.fulfill({ json: positions }); });
  await start(page);
  expect(await page.evaluate(() => Reflect.get(window, 'firstAborted'))).toBe(true);
  await page.evaluate(() => Reflect.get(window, 'releaseFirst')());
  await expect(page.getByText('Route data ready.')).toBeVisible();
  expect(positionsCalls).toBe(1);
  await page.clock.runFor(5000);
  await expect.poll(() => positionsCalls).toBe(2);
});

test('later non-null version automatically recovers from failed initial geometry', async ({ page }) => {
  await page.clock.install();
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ status: 503, json: { error: { code: 'schedule_unavailable' } } }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: withVersion(null) }));
  await page.goto(harness);
  await expect(trainItems(page)).toHaveCount(1);
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: geometry }));
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: positions }));
  await page.clock.runFor(5000);
  await expect.poll(() => markers(page)).toHaveLength(1);
  await expect(page.getByText('Route data ready.')).toBeVisible();
});

test('late source completion cannot reveal a superseded version and fallback reinstalls the latest snapshot', async ({ page }) => {
  await start(page);
  await page.clock.install();
  await page.evaluate(() => { window.holdSources = true; });
  let next = withVersion('v2');
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: next }));
  await page.route('**/api/v1/routes/A/geometry', route => route.fulfill({ json: { ...geometry, schedule_version: next.schedule_version } }));
  await page.clock.runFor(5000);
  await expect(notice(page)).not.toContainText('incompatible');
  await expect.poll(() => markers(page)).toHaveLength(0);
  next = { ...withVersion('v3'), trains: [{ ...train, schedule_version: 'v3', trip_instance_id: 'latest-run', longitude: -73.97, latitude: 40.76 }] };
  await page.clock.runFor(5000);
  await expect(page.getByRole('button', { name: 'Run latest-run' })).toBeVisible();
  await page.evaluate(() => {
    window.testMap.fire('error', { error: new Error('fixture') });
    window.releaseSources();
  });
  await expect.poll(() => markers(page)).toEqual(['run:"latest-run"']);
  expect(await page.evaluate(() => window.mapCount)).toBe(1);
});

for (const width of [1440, 390]) test(`realtime and simulation presentation at ${width}px`, async ({ page }, testInfo) => {
  await page.setViewportSize({ width, height: 1000 });
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: { ...positions, trains: [train,
    { ...train, trip_instance_id: 'simulation', position_basis: 'schedule_fallback', movement_state: 'unknown', source_timestamp: '2026-09-14T11:58:30Z', longitude: -73.94, latitude: 40.71, freshness: 'stale', source_age_seconds: 90, fallback_reason: 'stale_realtime' },
  ] } }));
  await page.goto(harness);
  await expect.poll(() => markers(page)).toHaveLength(2);
  await expect(page.getByText('Filled route badges: realtime inference.', { exact: false })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath(`trains-${width}.png`), fullPage: true });
});


test('production bundle connects train list and details', async ({ page }) => {
  await page.goto('/');
  await expect(trainItems(page)).toHaveCount(1);
  await expect(notice(page)).toContainText('Train updates available');
  await page.getByRole('button', { name: 'Run run-one' }).click();
  await expect(page.getByRole('region', { name: 'Pinned train comparison' })).toContainText('Realtime estimate');
});

test('warnings appear on positioned and unpositioned trains, survive simulation and retention, and clear on replacement', async ({ page }) => {
  const warnings = ['start_time_conflict', 'stop_id_sequence_conflict', '<img src=x onerror=unexpected()>'];
  let fail = false;
  let next: Positions = { ...positions, trains: [
    { ...train, reconciliation_warnings: warnings },
    { ...train, trip_instance_id: 'without-timing', source_record_id: 'record-two', latitude: null, longitude: null,
      shape_distance_meters: null, next_station_id: null, next_stop_sequence: null, next_arrival_at: null,
      no_position_reason: 'insufficient_timing', reconciliation_warnings: warnings },
  ] };
  await page.route('**/api/v1/trains?*', route => fail ? route.abort('failed') : route.fulfill({ json: next }));
  await page.clock.install();
  await page.goto('/');
  await expect(trainItems(page)).toHaveCount(2);
  const sections = page.getByRole('region', { name: 'Reconciliation warnings', exact: true });
  await expect(sections).toHaveCount(2);
  await expect(trainItems(page).nth(1)).toContainText('insufficient_timing');
  await expect(page.getByText('1 of 2 train outcomes is positioned on the map.', { exact: false })).toBeVisible();
  await page.getByRole('button', { name: 'Run run-one', exact: true }).click();
  const details = page.getByRole('region', { name: 'Pinned train comparison', exact: true });
  await expect(sections).toHaveCount(2);
  await expect(trainItems(page).first()).toContainText('exact trip ID or unique origin/path suffix was preferred');
  await expect(trainItems(page).first()).toContainText('stop ID was preferred');
  await expect(trainItems(page).first()).toContainText('The API reported a reconciliation warning: <img src=x onerror=unexpected()>.');
  await expect(page.locator('.train-info img, .train-info script')).toHaveCount(0);
  next = { ...next, trains: next.trains.map(t => t.trip_instance_id === 'run-one'
    ? { ...t, position_basis: 'schedule_fallback', freshness: 'stale', movement_state: 'unknown', fallback_reason: 'stale_realtime_counterpart' } : t) };
  await page.clock.runFor(5000);
  await expect(details).toContainText('Schedule estimate');
  await expect(sections).toHaveCount(2);
  fail = true;
  await page.clock.runFor(5000);
  await expect(notice(page)).toContainText('could not be reached');
  await expect(trainItems(page).first()).toContainText('Was a schedule simulation');
  await expect(sections).toHaveCount(2);
  await page.getByRole('button', { name: 'Pause updates' }).click();
  await expect(sections).toHaveCount(2);
  fail = false;
  next = { ...next, trains: next.trains.map(t => ({ ...t, reconciliation_warnings: [] })) };
  await page.getByRole('button', { name: 'Resume updates' }).click();
  await expect(sections).toHaveCount(0);
  await expect(details).toContainText('Active journey');
  await expect(trainItems(page)).toHaveCount(2);
});

test('absent warnings remain compatible; malformed warnings retain the last valid snapshot', async ({ page }) => {
  const { reconciliation_warnings: _, ...older } = train;
  let next: unknown = { ...positions, trains: [older] };
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: next }));
  await page.clock.install();
  await start(page);
  await expect(page.getByRole('region', { name: 'Reconciliation warnings', exact: true })).toHaveCount(0);
  next = { ...positions, trains: [{ ...train, reconciliation_warnings: ['start_time_conflict', 42] }] };
  await page.clock.runFor(5000);
  await expect(notice(page)).toContainText('Train response failed validation');
  await expect(trainItems(page)).toHaveCount(1);
  await expect.poll(() => markers(page)).toHaveLength(1);
  next = positions;
  await page.clock.runFor(5000);
  await expect(notice(page)).toContainText('Train updates available');
});

test('upcoming departures have separate counts, Eastern times, selection, and no marker until positioned', async ({ page }) => {
  let next: Positions = { ...positions, trains: [
    { ...train, reconciliation_warnings: ['vehicle_stop_sequence_conflict_ignored'] },
    { ...train, trip_instance_id: 'upcoming', latitude: null, longitude: null, shape_distance_meters: null,
      next_station_id: null, next_stop_sequence: null, next_arrival_at: null, movement_state: 'unknown',
      no_position_reason: 'not_started', predicted_departure_at: '2026-09-14T12:05:00Z' },
    { ...train, trip_instance_id: 'unavailable', latitude: null, longitude: null, shape_distance_meters: null,
      next_station_id: null, next_stop_sequence: null, next_arrival_at: null, no_position_reason: 'insufficient_timing' },
  ] };
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: next }));
  await page.clock.install();
  await page.goto(harness);
  await expect(trainItems(page)).toHaveCount(3);
  await expect.poll(() => markers(page)).toHaveLength(1);
  await expect(page.getByText('1 positioned · 1 upcoming · 1 position unavailable')).toBeVisible();
  const failures = page.getByRole('region', { name: 'Why some trains are not on the map' });
  await expect(failures).toContainText('insufficient_timing');
  await expect(failures).not.toContainText('not_started');
  const upcomingButton = page.getByRole('button', { name: 'Run upcoming', exact: true });
  const upcomingItem = upcomingButton.locator('..');
  await upcomingButton.click();
  const details = page.getByRole('region', { name: 'Pinned train comparison', exact: true });
  await expect(upcomingItem).toContainText('Upcoming');
  await expect(upcomingItem.locator('time')).toHaveAttribute('datetime', '2026-09-14T12:05:00Z');
  await expect(upcomingItem.locator('time')).toContainText('8:05:00 AM EDT');
  await expect(upcomingItem).not.toContainText('Position unavailable:');
  await expect(details).toContainText('Realtime estimate');
  await expect(page.getByRole('region', { name: 'Reconciliation warnings', exact: true })).toContainText('vehicle observation was excluded');
  next = { ...next, trains: next.trains.map(t => t.trip_instance_id === 'upcoming'
    ? { ...t, latitude: 40.76, longitude: -73.97, shape_distance_meters: 0, next_station_id: 'S4', next_stop_sequence: 2,
      next_arrival_at: '2026-09-14T12:10:00Z', movement_state: 'inferred_moving', no_position_reason: null, predicted_departure_at: null } : t) };
  await page.clock.runFor(5000);
  await expect.poll(() => markers(page)).toHaveLength(2);
  await expect(upcomingButton).toHaveAttribute('aria-pressed', 'true');
  await expect(details).toContainText('Realtime estimate');
  await expect(details).toContainText('8:10 AM EDT');
  await expect(page.getByText('2 positioned · 0 upcoming · 1 position unavailable')).toBeVisible();
});
