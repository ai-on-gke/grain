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

import { expect, test } from "./fixtures.js";

// docs/test-plans/2026-10-23-afternoon.md §5, over one `grain demo`
// running every repository-declaration scenario at once: pinned System
// and Settings sub-targets (95f0d3825), the /director route's canonical
// address and the console session page's task guard (61707e025), the
// session page's live work tree, and the merged union of the scenarios'
// .grain/ declarations on /processes and the repository page
// (cd2b49e44, f82f5ee81). The run is written up in
// docs/test-runs/2026-10-23-sessions-worktree-and-pinned-nav.md.
test.use({
  demoArgs: ["-merge-rules", "-sensitive-hold", "-mechanical-port"],
});
test.setTimeout(120_000);

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

const PINS = [
  "System",
  "System → Health",
  "System → Sandboxes",
  "Settings",
  "Settings → General",
  "Settings → Legacy capabilities",
];

async function openPinnedOverlay(page) {
  await page
    .locator("aside")
    .getByRole("button", { name: "Customize sidebar" })
    .click();
  await expect(
    page.getByRole("heading", { name: "Customize sidebar" }),
  ).toBeVisible();
  const typeSelect = page.getByLabel("Type");
  await typeSelect.click();
  await page.getByRole("option", { name: "View" }).click();
}

async function pickItem(page, name) {
  await page.locator("#add-item-select").click();
  await page.getByRole("option", { name, exact: true }).click();
}

// selected is the label of every pinned rail entry drawn selected: the
// rail's first <nav>, the pins and the destinations under them, rather
// than its footer's own System entry or the page's tab list.
async function selected(page) {
  return page
    .locator("aside nav")
    .first()
    .locator(".MuiListItemButton-root.Mui-selected")
    .evaluateAll((els) =>
      els.map((el) =>
        (
          el.querySelector(".MuiListItemText-primary")?.textContent || ""
        ).trim(),
      ),
    );
}

test.describe("Pinned System and Settings sub-targets", () => {
  test.afterEach(async ({ page }) => {
    await page.evaluate(() => localStorage.removeItem("grain.pinned"));
  });

  test("pinning a sub-target disables only that sub-target, and the most specific pin is the one lit", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await page.evaluate(() =>
      localStorage.setItem("grain.pinned", JSON.stringify([])),
    );
    await page.reload();
    await expect(page.locator(".task-row").first()).toBeVisible();

    await openPinnedOverlay(page);
    const pin = page.getByRole("button", { name: "Pin item" });
    for (const [i, name] of PINS.entries()) {
      await pickItem(page, name);
      await expect(pin, `${name} before pinning`).toBeEnabled();
      await pin.click();
      // Each pinned target is disabled now; every one still to come
      // (the bare view, a sibling tab or section) is still offered.
      await pickItem(page, name);
      await expect(pin, `${name} after pinning`).toBeDisabled();
      for (const later of PINS.slice(i + 1)) {
        await pickItem(page, later);
        await expect(pin, `${later} after pinning ${name}`).toBeEnabled();
      }
      for (const sibling of ["System → Logs", "Settings → Memory"]) {
        await pickItem(page, sibling);
        await expect(pin, `${sibling} after pinning ${name}`).toBeEnabled();
      }
    }
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Customize sidebar" }),
    ).toHaveCount(0);

    const stored = await page.evaluate(() =>
      JSON.parse(localStorage.getItem("grain.pinned")),
    );
    expect(
      stored.map((p) => [p.id, p.tab || p.section].filter(Boolean).join("/")),
    ).toEqual(
      expect.arrayContaining([
        "system",
        "system/health",
        "system/sandboxes",
        "settings",
        "settings/general",
        "settings/credentials",
      ]),
    );

    // Which pin each address lights: the one naming the open tab or
    // section when there is one, the bare view otherwise.
    const want = [
      ["/system", "/system", ["Health"]],
      ["/system/health", "/system", ["Health"]],
      ["/system/sandboxes", "/system/sandboxes", ["Sandboxes"]],
      ["/system/logs", "/system/logs", ["System"]],
      ["/settings", "/settings/models", ["Settings"]],
      ["/settings/general", "/settings/general", ["General"]],
      [
        "/settings/credentials",
        "/settings/credentials",
        ["Legacy capabilities"],
      ],
    ];
    for (const reload of [false, true]) {
      for (const [path, canonical, lit] of want) {
        await page.goto(path);
        if (reload) await page.reload();
        await expect(page).toHaveURL(new RegExp(`${canonical}$`));
        await expect
          .poll(() => selected(page), { message: `${path} lights` })
          .toEqual(lit);
      }
    }

    // Clicking the pins: Health is the canonical /system on a desktop.
    const aside = page.locator("aside");
    await page.goto("/system/logs");
    await aside.getByRole("button", { name: /^Health/ }).click();
    await expect(page).toHaveURL(/\/system$/);
    await aside.getByRole("button", { name: /^Sandboxes/ }).click();
    await expect(page).toHaveURL(/\/system\/sandboxes$/);
    await aside.getByRole("button", { name: /^Legacy capabilities/ }).click();
    await expect(page).toHaveURL(/\/settings\/credentials$/);
    await aside.getByRole("button", { name: /^General/ }).click();
    await expect(page).toHaveURL(/\/settings\/general$/);
  });

  test("a stored list with duplicate and malformed sub-target pins loads cleanly at desktop and phone widths", async ({
    page,
  }) => {
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await page.evaluate(() =>
      localStorage.setItem(
        "grain.pinned",
        JSON.stringify([
          { kind: "view", id: "system" },
          { kind: "view", id: "system", tab: "sandboxes" },
          { kind: "view", id: "system", tab: "sandboxes" },
          { kind: "view", id: "settings", section: "credentials" },
          { kind: "view", id: "settings", section: 7 },
          { kind: "view", id: "system", tab: "" },
          { kind: "nonsense", id: "system" },
          null,
          "settings",
        ]),
      ),
    );
    for (const viewport of [DESKTOP, PHONE]) {
      await page.setViewportSize(viewport);
      await page.goto("/system/sandboxes");
      await expect(page).toHaveURL(/\/system\/sandboxes$/);
      if (viewport === PHONE) {
        await page.getByRole("button", { name: "Open navigation" }).click();
      }
      const aside = page.locator("aside").filter({ visible: true }).first();
      // The two Sandboxes pins are both drawn and both lit; the bare
      // System pins are not, since a pin names the open tab.
      await expect(
        aside.getByRole("button", { name: /^Sandboxes/ }),
      ).toHaveCount(2);
      await expect
        .poll(() => selected(page))
        .toEqual(["Sandboxes", "Sandboxes"]);
      await page.goto("/settings/credentials");
      await expect(page).toHaveURL(/\/settings\/credentials$/);
      if (viewport === PHONE) {
        await page.getByRole("button", { name: "Open navigation" }).click();
      }
      await expect.poll(() => selected(page)).toEqual(["Legacy capabilities"]);
    }
    expect(errors).toEqual([]);
  });
});

async function api(page, method, path, data) {
  const res = await page.request.fetch(path, { method, data });
  expect(res.ok(), `${method} ${path}: ${res.status()}`).toBe(true);
  return res.json().catch(() => null);
}

async function sessionTaskID(page, id) {
  const body = await api(page, "GET", "/api/console-sessions");
  return body.sessions.find((s) => s.id === id)?.taskId || "";
}

// watchSessionBodies records, on every DOM change, any moment a
// session's page drew text that belongs to another session's chat or
// to a chat a restart closed: `marks` maps a pathname to the strings
// that must never be on screen there. The record is window.__stale.
async function watchSessionBodies(page, marks) {
  await page.evaluate((marks) => {
    window.__stale = [];
    window.__marks = marks;
    const check = () => {
      const body = document.querySelector(".console-session-body");
      if (!body) return;
      const text = body.textContent || "";
      for (const m of window.__marks[location.pathname] || []) {
        if (text.includes(m)) window.__stale.push(`${location.pathname}: ${m}`);
      }
    };
    new MutationObserver(check).observe(document.body, {
      subtree: true,
      childList: true,
      characterData: true,
    });
  }, marks);
}

test.describe("Console sessions: /director, switching, restart", () => {
  test("/director replaces itself with /session/director and Back/Forward are not trapped", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/processes");
    await expect(page.locator(".process-row").first()).toBeVisible();
    const before = await page.evaluate(() => history.length);
    await page.goto("/director");
    await expect(page).toHaveURL(/\/session\/director$/);
    expect(await page.evaluate(() => location.pathname)).toBe(
      "/session/director",
    );
    // One entry for the visit, none for the canonical rewrite.
    expect(await page.evaluate(() => history.length)).toBe(before + 1);
    await expect(page.locator(".console-session-page")).toBeVisible();

    await page.goBack();
    await expect(page).toHaveURL(/\/processes$/);
    await expect(page.locator(".process-row").first()).toBeVisible();
    await page.goForward();
    await expect(page).toHaveURL(/\/session\/director$/);
    await expect(page.locator(".console-session-page")).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL(/\/processes$/);
  });

  test("switching rapidly between two sessions and restarting one never draws the other's or the closed chat's conversation", async ({
    page,
  }) => {
    const id = `triage${Date.now() % 100000}`;
    await api(page, "POST", "/api/console-sessions", {
      id,
      title: "Triage",
    });
    try {
      const oldTriageTask = await sessionTaskID(page, id);
      expect(oldTriageTask).toBeTruthy();
      const OLD = `old triage chat ${id}`;
      await api(page, "POST", `/api/tasks/${oldTriageTask}/comments`, {
        body: OLD,
      });
      const DIRECTOR = "plan the Q3 delivery milestone";

      await page.setViewportSize(DESKTOP);
      await page.goto("/");
      await page.evaluate((id) => {
        localStorage.setItem(
          "grain.pinned",
          JSON.stringify([
            { kind: "session", id: "director" },
            { kind: "session", id },
          ]),
        );
      }, id);
      await page.goto(`/session/${id}`);
      await expect(page.locator(".console-session-body")).toContainText(OLD);
      await watchSessionBodies(page, {
        [`/session/${id}`]: [DIRECTOR],
        "/session/director": [OLD],
      });

      const aside = page.locator("aside");
      const directorPin = aside.getByRole("button", { name: /^Director/ });
      const triagePin = aside.getByRole("button", { name: /^Triage/ });
      for (let i = 0; i < 6; i++) {
        await directorPin.click();
        await triagePin.click();
      }
      await expect(page).toHaveURL(new RegExp(`/session/${id}$`));
      await expect(page.locator(".console-session-body")).toContainText(OLD);
      await directorPin.click();
      await expect(page.locator(".console-session-body")).toContainText(
        DIRECTOR,
      );
      await triagePin.click();
      await expect(page.locator(".console-session-body")).toContainText(OLD);

      // Start a fresh chat: the replacement task is drawn at once and the
      // closed one's conversation never again, even switched straight
      // to the director and back while the restart is answered.
      await page.getByRole("button", { name: "Start a fresh chat" }).click();
      const restarted = page.waitForResponse(
        (res) =>
          res.url().endsWith(`/api/console-sessions/${id}/restart`) &&
          res.request().method() === "POST",
      );
      await page
        .getByRole("button", { name: "Close this chat and start a fresh one" })
        .click();
      expect((await restarted).ok()).toBe(true);
      await page.evaluate(
        ({ id, OLD, DIRECTOR }) => {
          window.__marks[`/session/${id}`] = [DIRECTOR, OLD];
        },
        { id, OLD, DIRECTOR },
      );
      for (let i = 0; i < 4; i++) {
        await directorPin.click();
        await triagePin.click();
      }
      await expect.poll(() => sessionTaskID(page, id)).not.toBe(oldTriageTask);
      await expect(page.locator(".console-session-body")).not.toContainText(
        OLD,
      );
      await expect(
        page.getByRole("button", { name: "Start a fresh chat" }),
      ).toBeVisible();
      await page.waitForTimeout(3500); // one background poll tick
      await expect(page.locator(".console-session-body")).not.toContainText(
        OLD,
      );
      expect(await page.evaluate(() => window.__stale)).toEqual([]);
    } finally {
      await page.request.delete(`/api/console-sessions/${id}`).catch(() => {});
      await page.evaluate(() => localStorage.removeItem("grain.pinned"));
    }
  });
});

test.describe("The session page's live work tree", () => {
  test("follow-ups changing state two levels down redraw the tree in place, and a child opens over the session", async ({
    page,
  }) => {
    const chat = await sessionTaskID(page, "director");
    const all = await api(page, "GET", "/api/tasks");
    const list = Array.isArray(all) ? all : all.tasks;
    const find = (title) => list.find((t) => t.title === title).id;
    const darkMode = find("Add a dark mode toggle");
    const helper = find("Extract pagination query helper");
    const toolchain = find("Bump the Go toolchain to 1.24");

    await page.setViewportSize(DESKTOP);
    await page.goto("/session/director");
    const tree = page.getByRole("region", { name: "Work tree" });
    await expect(tree).toContainText(`Work from task ${chat}: 0 of 4 done`);
    await tree.locator(".provenance-toggle").click();
    const node = (id) => tree.locator(`li[data-task-id="${id}"]`);
    await expect(node(helper)).toBeVisible();

    // The composer is a contenteditable, so its draft is its text.
    const composer = page.getByRole("textbox", { name: "Message" });
    const DRAFT = "a reply still being written";
    await composer.click();
    await page.keyboard.type(DRAFT);
    await expect(composer).toHaveText(DRAFT);

    // Count every moment the tree is not on the page from here on.
    const treeSelector = '[aria-label="Work tree"], .work-tree';
    expect(
      await page.evaluate((sel) => !!document.querySelector(sel), treeSelector),
    ).toBe(true);
    await page.evaluate((sel) => {
      window.__treeGone = 0;
      new MutationObserver(() => {
        if (!document.querySelector(sel)) window.__treeGone++;
      }).observe(document.body, { subtree: true, childList: true });
    }, treeSelector);

    try {
      // A grandchild (the pagination task's own follow-up) closes...
      await api(page, "POST", `/api/tasks/${helper}/close`, {});
      await expect(tree).toContainText(`Work from task ${chat}: 1 of 4 done`, {
        timeout: 15_000,
      });
      // ...a child is approved...
      await api(page, "POST", `/api/tasks/${darkMode}/approve`, {});
      await expect(node(darkMode)).not.toContainText("planned", {
        timeout: 15_000,
      });
      // ...and another is stopped, all with the session page left open.
      await api(page, "POST", `/api/tasks/${toolchain}/stop`, {});
      await expect(node(toolchain)).not.toContainText("running", {
        timeout: 15_000,
      });
      await expect(page).toHaveURL(/\/session\/director$/);
      expect(await page.evaluate(() => window.__treeGone)).toBe(0);
      await expect(composer).toHaveText(DRAFT);

      // A child's link opens its detail over the session; Back returns
      // to the session page with the tree still drawn.
      await node(darkMode)
        .getByRole("link", { name: darkMode, exact: true })
        .click();
      await expect(page).toHaveURL(new RegExp(`/tasks/${darkMode}$`));
      await expect(
        page.getByRole("heading", { name: /Add a dark mode toggle/ }),
      ).toBeVisible();
      await page.goBack();
      await expect(page).toHaveURL(/\/session\/director$/);
      await expect(tree).toContainText(`Work from task ${chat}: 1 of 4 done`);
    } finally {
      await page.request.post(`/api/tasks/${helper}/reopen`).catch(() => {});
    }
  });
});

test.describe("Every scenario's declarations together", () => {
  test("/processes and the repository page show the union of what -merge-rules, -sensitive-hold and -mechanical-port declare", async ({
    page,
  }) => {
    const status = (
      await api(page, "GET", "/api/repos/acme/widgets/declarations")
    ).declarationsStatus;
    expect(status).toEqual(
      expect.objectContaining({
        templates: [".grain/templates/nightly.yml"],
        suites: [".grain/suites/smoke.yml"],
        schedules: [".grain/schedules/nightly.yml"],
        triggers: [".grain/triggers/payments.yml"],
        mergeRules: ".grain/merge-rules.yml",
      }),
    );
    expect(status.error).toMatch(/\.grain\/triggers\/broken\.yml/);
    // The merge rules' own warnings and the port settings' together.
    expect(status.warnings.join("\n")).toMatch(/merge-rules\.yml/);
    expect(status.warnings.join("\n")).toMatch(
      /port-types in \.grain\/settings\.yml/,
    );

    await page.setViewportSize(DESKTOP);
    await page.goto("/processes");
    for (const name of [
      "nightly-maintenance",
      "release-qualification",
      "security-audit",
      "Smoke Verification Suite",
      "Release Qualification Suite",
    ]) {
      await expect(
        page.locator(".process-row").getByRole("link", { name, exact: true }),
      ).toBeVisible();
    }
    await expect(
      page.locator('.process-row[data-kind="schedule"]'),
    ).toHaveCount(3);
    await expect(
      page.locator('.process-row[data-kind="trigger"]'),
    ).not.toHaveCount(0);

    await page.goto("/repos/acme/widgets#advanced");
    // What a failed read leaves running is still counted, merge rules
    // included, beside the read error itself.
    await expect(
      page.getByText(
        "Still running from 0f1e2d3c4b5a: 1 template(s), 1 suite(s), 1 schedule(s), 1 trigger(s), merge rules in .grain/merge-rules.yml",
      ),
    ).toBeVisible();
    await expect(
      page.getByText(/Last read failed: .*\.grain\/triggers\/broken\.yml/),
    ).toBeVisible();
    // The repository's own port-types findings are said twice by design
    // (pkg/ui/repos.go): among the declarations' warnings, and again in
    // the Ports box beside the directions they are about.
    await expect(
      page.locator(".repo-ports").getByText(/^Warning: port-types in/),
    ).toHaveCount(2);
    await expect(page.getByText(/^Warning: port-types in/)).toHaveCount(4);
    await expect(
      page.getByText(/^Warning: \.grain\/merge-rules\.yml/),
    ).not.toHaveCount(0);
  });
});

for (const theme of ["light", "dark"]) {
  test.describe(`/processes on a phone in ${theme}`, () => {
    test.use({
      viewport: PHONE,
      hasTouch: true,
      isMobile: true,
      colorScheme: theme,
    });
    test("template, suite and schedule rows fit 390px with no button over their text", async ({
      page,
    }) => {
      {
        await page.goto("/processes");
        const rows = page.locator(".process-row[data-kind]");
        await expect(rows.first()).toBeVisible();
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= window.innerWidth,
          ),
        ).toBe(true);
        const measured = await rows.evaluateAll((els) =>
          els.map((row) => {
            const box = (el) => el && el.getBoundingClientRect().toJSON();
            const kids = [
              ...row.querySelectorAll(
                ".process-kind, .process-name a, .process-name .MuiChip-root, .process-name .hint, .process-repo, .process-next, .process-actions button",
              ),
            ].filter((el) => el.getClientRects().length > 0);
            return {
              kind: row.dataset.kind,
              name: row.querySelector(".process-name a")?.textContent,
              row: box(row),
              kids: kids.map((el) => ({
                cls: el.className.toString().split(" ")[0] || el.tagName,
                text: (el.textContent || "").slice(0, 30),
                isButton: el.tagName === "BUTTON",
                box: box(el),
              })),
            };
          }),
        );
        expect(measured.map((r) => r.kind)).toEqual(
          expect.arrayContaining(["template", "schedule", "suite"]),
        );
        const overlap = (a, b) =>
          a.left < b.right - 1 &&
          b.left < a.right - 1 &&
          a.top < b.bottom - 1 &&
          b.top < a.bottom - 1;
        for (const r of measured) {
          for (const k of r.kids) {
            expect(
              k.box.right,
              `${r.name}: ${k.cls} "${k.text}" runs off the row`,
            ).toBeLessThanOrEqual(Math.min(r.row.right, PHONE.width) + 0.5);
            expect(k.box.left).toBeGreaterThanOrEqual(r.row.left - 0.5);
          }
          for (const b of r.kids.filter((k) => k.isButton)) {
            for (const k of r.kids.filter((k) => !k.isButton)) {
              expect(
                overlap(b.box, k.box),
                `${r.name}: "${b.text}" overlaps ${k.cls} "${k.text}"`,
              ).toBe(false);
            }
          }
        }

        // A schedule's own page: Run now and Pause/Resume side by side,
        // on screen, neither over the other.
        const schedule = rows
          .and(page.locator('[data-kind="schedule"]'))
          .first();
        await schedule.locator(".process-name a").click();
        const runNow = page.getByRole("button", { name: "Run now" });
        const toggle = page.getByRole("button", { name: /^(Pause|Resume)$/ });
        await expect(runNow).toBeVisible();
        await expect(toggle).toBeVisible();
        const [a, b] = [await runNow.boundingBox(), await toggle.boundingBox()];
        for (const x of [a, b]) {
          expect(x.x).toBeGreaterThanOrEqual(0);
          expect(x.x + x.width).toBeLessThanOrEqual(PHONE.width);
        }
        expect(
          overlap(
            {
              left: a.x,
              right: a.x + a.width,
              top: a.y,
              bottom: a.y + a.height,
            },
            {
              left: b.x,
              right: b.x + b.width,
              top: b.y,
              bottom: b.y + b.height,
            },
          ),
        ).toBe(false);
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= window.innerWidth,
          ),
        ).toBe(true);
      }
    });
  });
}
