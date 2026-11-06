// §10 item 4: accessible names on navigation and controls, and the header
// tray's alignment, at 1440/1024/768/390.
// Usage: GRAIN_SERVER=... GRAIN_TOKEN=... node icons-walk.mjs
import { createRequire } from "node:module";
import fs from "node:fs/promises";
const require = createRequire(new URL("../../../ui/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const out = new URL(".", import.meta.url).pathname;
const NAME_RULES = ["button-name", "link-name", "aria-command-name", "aria-toggle-field-name", "aria-input-field-name", "input-button-name", "select-name", "svg-img-alt", "role-img-alt"];
const PAGES = ["/", "/inbox", "/boards", "/ci", "/settings", "/system"];
const browser = await chromium.launch();
const result = {};
try {
  for (const width of [1440, 1024, 768, 390]) {
    const ctx = await browser.newContext({ baseURL: S, viewport: { width, height: 900 } });
    await ctx.request.post("/api/auth/access-code", { data: { code: T } });
    const page = await ctx.newPage();
    const r = (result[width] = { pages: {} });
    for (const path of PAGES) {
      await page.goto(path);
      await page.waitForLoadState("networkidle").catch(() => {});
      await page.waitForTimeout(500);
      const axe = await new AxeBuilder({ page }).withRules(NAME_RULES).analyze();
      // Every control the header tray holds, and its box.
      const tray = await page.$$eval(
        ".header-logo-box, .header-control-icon, .header-split-button",
        (els) =>
          els
            .filter((e) => e.offsetParent !== null)
            .map((e) => {
              const b = e.getBoundingClientRect();
              return {
                cls: [...e.classList].filter((c) => c.startsWith("header")).join(" "),
                name: e.getAttribute("aria-label") || e.textContent.trim().slice(0, 20),
                top: Math.round(b.top * 10) / 10,
                height: Math.round(b.height * 10) / 10,
                centre: Math.round((b.top + b.height / 2) * 10) / 10,
                left: Math.round(b.left),
                right: Math.round(b.right),
              };
            }),
      );
      const centres = tray.map((t) => t.centre);
      const heights = [...new Set(tray.map((t) => t.height))];
      r.pages[path] = {
        axeNameViolations: axe.violations.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target.join(" ")).slice(0, 6) })),
        trayControls: tray.length,
        centreSpread: centres.length ? Math.max(...centres) - Math.min(...centres) : null,
        heights,
        overflowRight: tray.some((t) => t.right > width),
        tray: path === "/" ? tray : undefined,
      };
    }
    // Every navigation destination, by its accessible name.
    const navOpen = page.getByRole("button", { name: /open navigation|menu|navigation/i }).first();
    if (width <= 768 && (await navOpen.isVisible().catch(() => false))) {
      await navOpen.click();
      await page.waitForTimeout(400);
      r.drawer = true;
    }
    r.navDestinations = await page.$$eval('nav a, [role="navigation"] a, nav [role="button"], nav button', (els) =>
      els
        .filter((e) => e.offsetParent !== null || e.getClientRects().length)
        .map((e) => ({
          name: (e.getAttribute("aria-label") || e.innerText || e.getAttribute("title") || "").trim().replace(/\s+/g, " ").slice(0, 40),
        })),
    );
    r.unnamedNav = r.navDestinations.filter((d) => !d.name).length;
    const axeNav = await new AxeBuilder({ page }).withRules(NAME_RULES).analyze();
    r.navAxe = axeNav.violations.map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target.join(" ")).slice(0, 6) }));
    await page.screenshot({ path: `${out}/icons-${width}.png` });
    await ctx.close();
  }
} finally {
  await browser.close();
}
await fs.writeFile(`${out}/icons-walk.json`, JSON.stringify(result, null, 2));
for (const [w, r] of Object.entries(result)) {
  console.log(w, "navDestinations", r.navDestinations.length, "unnamed", r.unnamedNav, "navAxe", JSON.stringify(r.navAxe));
  for (const [p, v] of Object.entries(r.pages))
    console.log("  ", p, "tray", v.trayControls, "spread", v.centreSpread, "heights", JSON.stringify(v.heights), "overflow", v.overflowRight, "axe", JSON.stringify(v.axeNameViolations));
}
