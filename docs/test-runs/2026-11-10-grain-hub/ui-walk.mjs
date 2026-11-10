// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// 2026-11-10 §7 item 3: the hub UI flows in Chromium over a real HEAD hub on
// fake GCP (fakegcp-walk.sh KEEP=1: alice at :4281, root at :4284) and a
// shapeless hub (interop.sh PAIR=new-hub KEEP=1 HUB_ONLY=1, fronted as alice
// at :4235). Run from ui-hub/ so @playwright/test resolves:
//
//   cd ui-hub && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-10-grain-hub/ui-walk.mjs <out-dir>
//
// Prints one PASS/FAIL line per check, and writes the delete dialog at
// 1440/390 px in light and dark plus dialog-contact-sheet.png to <out-dir>.
import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");

const ALICE = process.env.ALICE_URL || "http://127.0.0.1:4281";
const ROOT = process.env.ROOT_URL || "http://127.0.0.1:4284";
const NOSHAPES = process.env.NOSHAPES_URL || "http://127.0.0.1:4235";
const CONNECTED = process.env.CONNECTED_ENTRY || "walk-conn";
const outDir = path.resolve(process.argv[2] || "ui-walk-out");
await fs.mkdir(outDir, { recursive: true });

let failures = 0;
function check(ok, what, detail = "") {
  if (!ok) failures++;
  console.log(
    `${ok ? "PASS" : "FAIL"} ${what}${detail ? ` -- ${detail}` : ""}`,
  );
}
const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};

const browser = await chromium.launch();
async function open(base, route, { width = "desktop", theme = "light" } = {}) {
  const context = await browser.newContext({
    viewport: VIEWPORTS[width],
    colorScheme: theme,
    isMobile: width === "phone",
    hasTouch: width !== "desktop",
  });
  await context.addInitScript(
    (t) => localStorage.setItem("grain.themeMode", t),
    theme,
  );
  const page = await context.newPage();
  await page.goto(new URL(route, base).href);
  await page
    .waitForLoadState("networkidle", { timeout: 10_000 })
    .catch(() => {});
  return page;
}
const activeKind = (page) =>
  page
    .locator(".reg-mode-card-active .reg-mode-title")
    .first()
    .textContent({ timeout: 5_000 })
    .catch(() => "");

try {
  // 1. `creating` + status_detail while the service account propagates.
  {
    const page = await open(ALICE, "/");
    const row = page.getByRole("row", { name: /walk-creating/ });
    // The reconciler records the propagation reason after its first
    // instances.insert attempt fails, a few seconds after the entry is made.
    let text = "";
    for (let i = 0; i < 30; i++) {
      text = (await row.textContent({ timeout: 10_000 }).catch(() => "")) || "";
      if (/waiting for service account/.test(text)) break;
      await page.waitForTimeout(1_000);
      await page.reload();
    }
    check(
      /creating/.test(text),
      "My Entries shows walk-creating as creating",
      text.slice(0, 80),
    );
    check(
      /waiting for service account/.test(text),
      "My Entries shows its status_detail",
      (text.match(/waiting[^.]*/) || [""])[0].slice(0, 90),
    );
    await page.goto(`${ALICE}/entries/walk-creating`);
    const banner = page.getByTestId("lifecycle-status-banner");
    const bannerText =
      (await banner.textContent({ timeout: 10_000 }).catch(() => "")) || "";
    check(
      /^Provisioning instance:\s*waiting for service account/.test(
        bannerText.trim(),
      ),
      "Entry Detail shows the Provisioning instance: banner",
      bannerText.slice(0, 90),
    );
    await page.context().close();
  }

  // 2. /new defaults to Hosted VM with an editable random name.
  let suggested = "";
  {
    const page = await open(ALICE, "/new");
    await page.getByLabel(/Entry subdomain name/i).waitFor();
    check(
      /Hosted VM/.test(await activeKind(page)),
      "/new opens on Hosted VM",
      await activeKind(page),
    );
    const nameInput = page.getByLabel(/Entry subdomain name/i);
    suggested = await nameInput.inputValue();
    check(
      /^[a-z0-9]+(-[a-z0-9]+)+$/.test(suggested),
      "/new suggests a random name",
      suggested,
    );
    await page
      .getByRole("button", { name: "Generate another random name" })
      .click();
    const again = await nameInput.inputValue();
    check(
      again !== suggested && again.length > 0,
      "Generate another random name changes it",
      `${suggested} -> ${again}`,
    );
    suggested = again;
    await page
      .getByTestId("name-availability-status")
      .filter({ hasText: "free" })
      .waitFor({ timeout: 10_000 });
    await page.getByRole("button", { name: "Create hosted VM" }).click();
    const created = page.getByTestId("created-entry-banner");
    check(
      (
        (await created.textContent({ timeout: 10_000 }).catch(() => "")) || ""
      ).includes(suggested),
      "Create hosted VM with the suggested name",
      suggested,
    );
    await page.context().close();
  }
  {
    const page = await open(ALICE, "/new");
    const nameInput = page.getByLabel(/Entry subdomain name/i);
    await nameInput.fill("walk-edited");
    await page
      .getByTestId("name-availability-status")
      .filter({ hasText: "free" })
      .waitFor({ timeout: 10_000 });
    await page.getByRole("button", { name: "Create hosted VM" }).click();
    const created = page.getByTestId("created-entry-banner");
    check(
      (
        (await created.textContent({ timeout: 10_000 }).catch(() => "")) || ""
      ).includes("walk-edited"),
      "Create hosted VM with an edited name",
      "walk-edited",
    );
    await page.context().close();
  }

  // 3. ?kind=connected, and the shapeless hub's fallback.
  {
    const page = await open(ALICE, "/new?kind=connected");
    await page.getByLabel(/Entry subdomain name/i).waitFor();
    check(
      /Connected/i.test(await activeKind(page)),
      "/new?kind=connected opens on Connected",
      await activeKind(page),
    );
    await page.context().close();
  }
  {
    const page = await open(NOSHAPES, "/new");
    await page.getByLabel(/Entry subdomain name/i).waitFor();
    await page.waitForTimeout(500);
    check(
      /Connected/i.test(await activeKind(page)),
      "/new on a hub with no shapes falls back to Connected",
      await activeKind(page),
    );
    await page.context().close();
  }

  // 4. Delete walk-edited from My Entries: the dialog (shot at every size),
  // gone from the list, Restore at its URL.
  const shots = [];
  for (const width of ["desktop", "phone"]) {
    for (const theme of ["light", "dark"]) {
      const page = await open(ALICE, "/", { width, theme });
      const row = page.getByRole("row", { name: /walk-edited/ });
      await row
        .getByRole("button", { name: /^Delete/ })
        .click({ timeout: 10_000 });
      const dialog = page.getByRole("dialog");
      await dialog.waitFor();
      const file = path.join(outDir, `delete-dialog-${width}-${theme}.png`);
      await page.screenshot({ path: file });
      shots.push(file);
      const body = (await dialog.textContent()) || "";
      if (width === "desktop" && theme === "light") {
        check(
          !/registry admin/i.test(body),
          "Delete dialog says hub admin, not registry admin",
          (body.match(/restored[^.]*\./) || [""])[0],
        );
      }
      if (width === "phone" && theme === "dark") {
        await page.getByLabel(/to\s+confirm/i).fill("walk-edited");
        await dialog.getByRole("button", { name: "Delete entry" }).click();
        await dialog.waitFor({ state: "hidden" });
        await page.reload();
        await page.waitForLoadState("networkidle").catch(() => {});
        check(
          !(await page.getByRole("row", { name: /walk-edited/ }).count()),
          "Deleted entry is gone from My Entries",
        );
      } else {
        await page.keyboard.press("Escape");
      }
      await page.context().close();
    }
  }
  {
    const page = await open(ALICE, "/entries/walk-edited");
    const restore = page.getByRole("button", { name: "Restore deleted entry" });
    check(
      await restore.isVisible({ timeout: 10_000 }).catch(() => false),
      "The deleted entry's URL still offers Restore",
    );
    await page.context().close();
  }

  // 5. Admin sees it with Restore and Hard purge; purge asks first.
  {
    const page = await open(ROOT, "/admin");
    const row = page.getByRole("row", { name: /walk-edited/ }).first();
    await row.waitFor({ timeout: 10_000 }).catch(() => {});
    check(
      await row
        .getByRole("button", { name: "Restore" })
        .isVisible()
        .catch(() => false),
      "Admin sees the deleted entry with Restore",
    );
    const purge = row.getByRole("button", { name: "Hard purge" });
    check(
      await purge.isVisible().catch(() => false),
      "Admin sees the deleted entry with Hard purge",
    );
    await purge.click();
    const dialog = page.getByRole("dialog");
    check(
      await dialog
        .getByText(/Hard purge walk-edited\?/)
        .isVisible({ timeout: 5_000 })
        .catch(() => false),
      "Hard purge asks for confirmation",
    );
    await page.keyboard.press("Escape");
    await dialog.waitFor({ state: "hidden" }).catch(() => {});
    await page.reload();
    await page.waitForLoadState("networkidle").catch(() => {});
    check(
      await page
        .getByRole("row", { name: /walk-edited/ })
        .first()
        .getByRole("button", { name: "Restore" })
        .isVisible()
        .catch(() => false),
      "Cancelling the purge leaves the entry restorable",
    );
    await page.context().close();
  }

  // 6. Revoking a connected entry's credential asks first.
  {
    const page = await open(ALICE, `/entries/${CONNECTED}`);
    const revoke = page.getByRole("button", { name: /^Revoke$/ }).first();
    await revoke.click({ timeout: 10_000 });
    const dialog = page.getByRole("dialog");
    check(
      await dialog
        .getByText(/Revoke registration credential\?/)
        .isVisible({ timeout: 5_000 })
        .catch(() => false),
      "Revoke credential asks for confirmation",
    );
    await page.keyboard.press("Escape");
    await page.context().close();
  }

  // 7. The grain mark and favicon follow the theme.
  for (const theme of ["light", "dark"]) {
    const page = await open(ALICE, "/", { theme });
    const got = await page.evaluate(() => ({
      svg: document.getElementById("favicon")?.getAttribute("href"),
      png: document.getElementById("favicon-png")?.getAttribute("href"),
      mark: [...document.querySelectorAll("img")]
        .map((i) => i.getAttribute("src"))
        .find((s) => /grain-mark/.test(s || "")),
    }));
    const want = `/grain-mark-${theme}`;
    check(
      got.svg === `${want}.svg` &&
        got.png === `${want}.png` &&
        got.mark === `${want}.svg`,
      `grain mark and favicon follow the ${theme} theme`,
      JSON.stringify(got),
    );
    for (const asset of [got.svg, got.png]) {
      const res = await page.request.get(new URL(asset, ALICE).href);
      check(
        res.ok(),
        `${asset} is served`,
        `HTTP ${res.status()} ${res.headers()["content-type"]}`,
      );
    }
    await page.context().close();
  }

  // Contact sheet of the delete dialog shots.
  const page = await browser.newPage({
    viewport: { width: 1600, height: 900 },
  });
  const imgs = await Promise.all(
    shots.map(
      async (f) =>
        `data:image/png;base64,${(await fs.readFile(f)).toString("base64")}`,
    ),
  );
  await page.setContent(
    `<body style="margin:0;background:#888;font:12px sans-serif;display:flex;gap:8px;flex-wrap:wrap;align-items:flex-start;padding:8px">${imgs
      .map(
        (src, i) =>
          `<figure style="margin:0;background:#fff"><img src="${src}" style="height:420px;display:block"><figcaption style="padding:2px 4px">${path.basename(shots[i], ".png")}</figcaption></figure>`,
      )
      .join("")}</body>`,
  );
  await page.screenshot({
    path: path.join(outDir, "dialog-contact-sheet.png"),
    fullPage: true,
  });
  await page.close();
} finally {
  await browser.close();
}
console.log(failures ? `${failures} check(s) FAILED` : "all checks passed");
process.exit(failures ? 1 : 0);
