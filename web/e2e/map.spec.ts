import { test, expect, type Page } from '@playwright/test';
import { basemap, geometry } from './fixtures/geometry';
import { emptyPositions } from './fixtures/positions';

test.beforeEach(async ({ page }) => {
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: emptyPositions }));
  await page.route('https://tiles.openfreemap.org/**', (route) => route.fulfill({ json: basemap }));
  await page.route('**/api/v1/routes/A/geometry', (route) => route.fulfill({ json: geometry }));
});

async function ready(page: Page) {
  await page.goto('/');
  await expect(page.getByText('A-line map ready.', { exact: false })).toBeVisible();
}

for (const width of [1440, 390, 320]) test(`renders the map and directory at ${width}px`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 });
  await ready(page);
  await expect(page.locator('.station-list > li > span')).toHaveText(['2 St', '2 St', '10 St', 'Alpha', 'Unknown', 'Unlocated']);
  await expect(page.getByText('Location unavailable: Station coordinates are missing.')).toBeVisible();
  await expect(page.getByText('Location unavailable: <script>unexpected()</script>')).toBeVisible();
  await expect(page.locator('.route-map')).toHaveScreenshot(`branches-${width}.png`);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.keyboard.press('Tab');
  await expect(page.getByRole('link', { name: 'Skip to stations' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.locator('#stations')).toBeFocused();
});

test('shows an ineligible-schedule warning', async ({ page }) => {
  await page.route('**/api/v1/routes/A/geometry', (route) => route.fulfill({ json: { ...geometry, schedule_eligible: false } }));
  await page.goto('/');
  await expect(page.getByText('Route data loaded with a warning.')).toBeVisible();
  await expect(page.locator('.station-list > li')).toHaveCount(6);
});

test('retries a failed geometry request', async ({ page }) => {
  // A positions version would trigger automatic geometry recovery before the manual click.
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: { ...emptyPositions, schedule_version: null } }));
  let failed = true;
  await page.route('**/api/v1/routes/A/geometry', (route) => failed ? route.fulfill({ status: 503, json: { error: { code: 'schedule_unavailable' } } }) : route.fulfill({ json: geometry }));
  await page.goto('/');
  await expect(page.getByRole('alert')).toContainText('Schedule data is temporarily unavailable.');
  failed = false;
  await page.getByRole('button', { name: 'Try again' }).click();
  await expect(page.getByText('Route data ready.')).toBeVisible();
});

test('times out after ten seconds and retries', async ({ page }) => {
  await page.route('**/api/v1/trains?*', route => route.fulfill({ json: { ...emptyPositions, schedule_version: null } }));
  await page.route('**/api/v1/routes/A/geometry', () => {});
  const started = Date.now();
  await page.goto('/');
  await expect(page.getByRole('alert')).toContainText('The route request timed out.', { timeout: 12_000 });
  expect(Date.now() - started).toBeGreaterThanOrEqual(10_000);
  await page.route('**/api/v1/routes/A/geometry', (route) => route.fulfill({ json: geometry }));
  await page.getByRole('button', { name: 'Try again' }).click();
  await expect(page.getByText('Route data ready.')).toBeVisible();
});

for (const failure of ['style', 'deadline'] as const) test(`falls back when the basemap ${failure}s`, async ({ page }) => {
  await page.route('https://tiles.openfreemap.org/**', (route) => failure === 'style' ? route.abort('failed') : new Promise(() => {}));
  await page.goto('/');
  await expect(page.getByText('Basemap unavailable.', { exact: false })).toBeVisible({ timeout: 12_000 });
  await expect(page.locator('.route-map')).toHaveScreenshot('fallback.png');
  await expect(page.locator('.station-list > li')).toHaveCount(6);
});

test('keeps the directory when WebGL is unavailable', async ({ page }) => {
  await page.addInitScript(() => {
    const original = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement, ...args: Parameters<typeof original>) {
      return String(args[0]).includes('webgl') ? null : original.apply(this, args);
    } as typeof original;
  });
  await page.goto('/');
  await expect(page.getByText('Map unavailable.', { exact: false })).toBeVisible();
  await expect(page.locator('.station-list > li')).toHaveCount(6);
});

test('distinguishes empty geometry', async ({ page }) => {
  await page.route('**/api/v1/routes/A/geometry', (route) => route.fulfill({ json: { ...geometry, stations: [], shapes: [] } }));
  await ready(page);
  await expect(page.getByText('No stations are listed for this route.')).toBeVisible();
  await expect(page.getByText('No usable route shapes are available.', { exact: false })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Fit A line' })).toBeDisabled();
});
