// Walkthrough helper for docs/test-runs/2026-11-02-director-config-change.md:
// node e2e/walk-config-change.mjs <url> <code> <taskId> <check|reject|approve> [label]
import { chromium } from "@playwright/test";
import { signIn } from "./accesscode.js";

const [url, code, id, action, label = action] = process.argv.slice(2);
const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
const out = {};
try {
  const views =
    action === "check"
      ? [
          [1440, 900, "light"],
          [1440, 900, "dark"],
          [390, 844, "light"],
          [390, 844, "dark"],
        ]
      : [[1440, 900, "light"]];
  for (const [width, height, scheme] of views) {
    const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: scheme });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(`${url}/tasks/${id}`);
    const card = page.locator(".pending-config-change").first();
    await card.waitFor({ timeout: 20000 });
    const box = await card.boundingBox();
    const key = `${width}-${scheme}`;
    const approve = card.getByRole("button", { name: /Approve/ });
    const reject = card.getByRole("button", { name: /^Reject$/ });
    const a = await approve.boundingBox();
    const r = await reject.boundingBox();
    const inside = (b) =>
      b.x >= box.x - 0.5 && b.y >= box.y - 0.5 && b.x + b.width <= box.x + box.width + 0.5 && b.y + b.height <= box.y + box.height + 0.5;
    const docWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    out[key] = {
      card: box,
      approve: a,
      reject: r,
      approveInside: inside(a),
      rejectInside: inside(r),
      cardWithinViewport: box.x >= 0 && box.x + box.width <= width + 0.5,
      pageScrollWidth: docWidth,
      approveEnabled: await approve.isEnabled(),
      validation: (await card.getByTestId("config-change-validation").innerText()).slice(0, 300),
      chatCards: await page.locator(".pending-config-change").count(),
      errors,
    };
    await page.screenshot({ path: `/var/tmp/wt/card-${label}-${key}.png`, fullPage: true });
    if (action === "reject") {
      await reject.click();
      await card.getByLabel("Why are you rejecting it?").fill("walkthrough: the YAML does not parse");
      const resp = page.waitForResponse((res) => res.url().includes("/config-change/reject"));
      await card.getByRole("button", { name: "Reject change" }).click();
      const res = await resp;
      out.decision = { status: res.status(), body: (await res.text()).slice(0, 300) };
    }
    if (action === "approve") {
      const resp = page.waitForResponse((res) => res.url().includes("/config-change/approve"));
      await approve.click();
      const res = await resp;
      out.decision = { status: res.status(), body: (await res.text()).slice(0, 400) };
      await page.waitForTimeout(1500);
      out.afterText = (await page.locator("body").innerText()).match(/.{0,120}(head moved|changed since|conflict|Approved|moved).{0,200}/is)?.[0] ?? null;
      await page.screenshot({ path: `/var/tmp/wt/card-${label}-after.png`, fullPage: true });
    }
    await ctx.close();
  }
} finally {
  await browser.close();
}
console.log(JSON.stringify(out, null, 1));
