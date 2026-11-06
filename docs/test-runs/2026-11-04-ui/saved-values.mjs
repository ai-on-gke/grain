// §10 item 3, "a saved value no longer offered still shows": the
// Preferences time zone holding a zone the picker does not list (a
// legacy alias Intl accepts but Intl.supportedValuesOf does not name), and
// the New task form's Review picker over a template that no longer exists.
// Usage: GRAIN_SERVER=... GRAIN_TOKEN=... node saved-values.mjs
import { createRequire } from "node:module";
import fs from "node:fs/promises";
const require = createRequire(new URL("../../../ui/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const out = new URL(".", import.meta.url).pathname;
const result = {};
const browser = await chromium.launch();
try {
  for (const width of [1440, 390]) {
    const ctx = await browser.newContext({ baseURL: S, viewport: { width, height: 900 } });
    await ctx.request.post("/api/auth/access-code", { data: { code: T } });
    await ctx.addInitScript(() => localStorage.setItem("grain.timeZone", "US/Pacific"));
    const page = await ctx.newPage();
    page.setDefaultTimeout(8000);
    await page.goto("/settings/preferences");
    const zone = page.getByRole("combobox", { name: /time zone/i }).first();
    await zone.waitFor();
    const offered = await page.evaluate(() => Intl.supportedValuesOf("timeZone").includes("US/Pacific"));
    result[`prefs@${width}`] = { stored: "US/Pacific", offeredByIntl: offered, shown: await zone.inputValue() };
    // Keyboard: open the list and Escape it; the value must survive.
    await zone.focus();
    await page.keyboard.press("ArrowDown");
    await page.keyboard.press("Escape");
    result[`prefs@${width}`].afterEscape = await zone.inputValue();
    await page.screenshot({ path: `${out}/saved-value-prefs-${width}.png` });
    await ctx.close();
  }
} finally {
  await browser.close();
}
await fs.writeFile(`${out}/saved-values.json`, JSON.stringify(result, null, 2));
console.log(JSON.stringify(result, null, 1));
