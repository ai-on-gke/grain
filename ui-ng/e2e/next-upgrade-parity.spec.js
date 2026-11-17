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

// /next/settings/upgrade against the classic Upgrade pane (2026-11-13 and
// 2026-11-14 deployment-operations walkthroughs), and an upgrade watched
// from it through to the reload onto the new build (2026-11-15, the
// 3.7.0 -> 3.8.0 upgrade done from /next/). Only the upgrade boundary is
// stubbed -- GET/POST /api/upgrade, its targets and release notes, and
// the build GET /api/config reports -- and nothing is ever really
// upgraded: every POST /api/upgrade is answered here.

import { test, expect } from "./fixtures.js";

const STATUS = {
  controllable: true,
  running: { release: "v2.3.1" },
  mode: "image",
  phase: "",
  imageRepo: "docker.io/grainai/grain",
  defaultImageRepo: "docker.io/grainai/grain",
  imageRepos: ["docker.io/grainai/grain", "ghcr.io/acme/grain"],
  inFlightRuns: 0,
  database: { driver: "sqlite", dsn: "/var/lib/grain/data/grain.db" },
};

const TARGETS = {
  targets: [
    { name: "v2.4.0", kind: "version", allowed: true, retracted: true },
    { name: "v2.3.9", kind: "version", allowed: true },
    { name: "v2.4.0-rc.1", kind: "version", allowed: true, discarded: true },
  ],
  allowedBranches: ["**"],
};

const NOTES =
  "## v2.4.0\n\n**Breaking:** the v1 task API is removed.\n\n- Faster boots";

const json = (body) => ({
  contentType: "application/json",
  body: JSON.stringify(body),
});

// stubUpgrade answers the upgrade routes from `script`: GET /api/upgrade
// returns script.status(), and POST /api/upgrade records the request and
// returns script.start(body).
async function stubUpgrade(page, script) {
  const posts = [];
  await page.route("**/api/upgrade", (route) => {
    const req = route.request();
    if (req.method() === "POST") {
      const body = req.postDataJSON();
      posts.push(body);
      return route.fulfill(json(script.start(body)));
    }
    return route.fulfill(json(script.status()));
  });
  await page.route("**/api/upgrade/targets", (route) =>
    route.fulfill(json(TARGETS)),
  );
  await page.route("**/api/upgrade/release-notes?**", (route) =>
    route.fulfill(json(NOTES)),
  );
  await page.route("**/api/upgrade/current", (route) =>
    route.fulfill(json({ ...STATUS, phase: "failed", detail: "cancelled" })),
  );
  return posts;
}

// stubBuild has GET /api/config report the real config with its commit
// swapped for another build's once build.restarted is set.
async function stubBuild(page, build) {
  await page.route("**/api/config**", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    if (build.restarted) {
      body.version = {
        ...(body.version ?? {}),
        release: "v2.4.1",
        commit: "feedfacefeedfacefeedfacefeedfacefeedface",
        unversioned: false,
      };
    }
    return route.fulfill({ response, json: body });
  });
}

const steps = (states, detail = {}) =>
  ["check target", "pull image", "verify image", "restart"].map((name, i) => ({
    name,
    state: states[i] ?? "pending",
    ...(states[i] && states[i] !== "pending"
      ? { startedAt: new Date(Date.now() - 4000).toISOString() }
      : {}),
    ...(states[i] === "done" || states[i] === "failed"
      ? { finishedAt: new Date().toISOString() }
      : {}),
    ...(detail[i] ? { detail: detail[i] } : {}),
  }));

test("Settings -> Upgrade warns about retracted and discarded targets, searches them, renders breaking notes, and shows the image source, database move and check mode", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const posts = await stubUpgrade(page, {
    status: () => STATUS,
    start: () => STATUS,
  });
  await page.goto("/next/settings/upgrade");
  const card = page.getByTestId("card-upgrade");
  await expect(card.getByTestId("current-daemon-version")).toHaveText("v2.3.1");

  // The retracted v2.4.0 is not preselected: the first safe target is.
  const picker = card.getByTestId("select-upgrade-target");
  await expect(picker).toHaveValue("v2.3.9");
  await expect(card.getByTestId("upgrade-target-warning")).toHaveCount(0);

  // The list flags both, and filters as it is typed into.
  await picker.click();
  const list = card.getByTestId("upgrade-target-list");
  await expect(list.locator('[data-value="v2.4.0"]')).toContainText(
    "retracted",
  );
  await expect(list.locator('[data-value="v2.4.0-rc.1"]')).toContainText(
    "discarded",
  );
  await picker.fill("rc");
  await expect(list.getByRole("option")).toHaveCount(1);
  await list.locator('[data-value="v2.4.0-rc.1"]').click();
  await expect(card.getByTestId("upgrade-target-warning")).toContainText(
    "was discarded",
  );
  await picker.fill("v2.4");
  await list.locator('[data-value="v2.4.0"]').click();
  await expect(picker).toHaveValue("v2.4.0");
  await expect(card.getByTestId("upgrade-target-warning")).toContainText(
    "v2.4.0 was retracted",
  );

  // Release notes render as Markdown, the breaking change called out.
  await expect(card.getByTestId("upgrade-breaking-warning")).toContainText(
    "breaking change",
  );
  const notes = card.getByTestId("upgrade-release-notes-body");
  await expect(notes.locator("h2")).toHaveText("v2.4.0");
  await expect(notes.locator("strong")).toHaveText("Breaking:");
  await expect(notes).not.toContainText("**");

  // The image source: what it pulls, the other repository, and locked
  // read-only once the upgrade waits for the deployment to be idle.
  const image = card.getByTestId("card-upgrade-image");
  await expect(image).toContainText("docker.io/grainai/grain");
  await expect(image.getByTestId("upgrade-will-pull")).toHaveText(
    "Will pull docker.io/grainai/grain:v2.4.0",
  );
  await image.getByRole("button", { name: "ghcr.io/acme/grain" }).click();
  await expect(image.getByTestId("upgrade-will-pull")).toHaveText(
    "Will pull ghcr.io/acme/grain:v2.4.0",
  );
  await expect(image.getByTestId("upgrade-image-readonly")).toHaveCount(0);
  await card.getByTestId("switch-upgrade-drain").click();
  await expect(image.getByTestId("upgrade-image-readonly")).toHaveText(
    "Read-only",
  );
  await expect(image.getByTestId("upgrade-image-input")).toBeDisabled();
  await card.getByTestId("switch-upgrade-drain").click();

  // The database move is offline only, with the commands for it.
  const db = page.getByTestId("upgrade-database");
  await expect(db).toContainText("This deployment runs on SQLite");
  await expect(db.getByTestId("upgrade-database-offline")).toContainText(
    "only with the daemon stopped",
  );
  await expect(db.getByTestId("upgrade-database-commands")).toContainText(
    "sudo ./scripts/setup.sh --db-driver postgres",
  );

  // Start upgrade still asks first, naming the retracted target; Cancel
  // sends nothing.
  await card.getByTestId("start-upgrade-btn").click();
  const dialog = page.getByTestId("confirm-dialog");
  await expect(dialog).toContainText("Upgrade grain to v2.4.0?");
  await expect(dialog).toContainText("v2.4.0 was retracted.");
  await dialog.getByTestId("confirm-cancel").click();
  expect(posts).toEqual([]);

  // The check mode is read here and edited in Settings -> General.
  await expect(card.getByTestId("upgrade-check-mode")).toContainText(
    "After an upgrade lands",
  );
  await expect(card.getByTestId("select-upgrade-check-mode")).toHaveCount(0);
  await card.getByTestId("upgrade-check-settings-link").click();
  await expect(page).toHaveURL(/\/next\/settings\/general#updates$/);
  await expect(
    page.getByTestId("card-updates").getByTestId("select-upgrade-check-mode"),
  ).toBeVisible();
});

test("an upgrade started from Settings -> Upgrade shows its steps here and in the shell, then reloads onto the new build", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const build = { restarted: false };
  await stubBuild(page, build);
  let phase = 0;
  const posts = await stubUpgrade(page, {
    start: () => {
      phase = 1;
      return {
        ...STATUS,
        phase: "running",
        branch: "v2.3.9",
        cancellable: true,
        steps: steps(["done", "running"]),
      };
    },
    status: () => {
      if (phase === 0) return STATUS;
      phase += 1;
      if (phase <= 3) {
        return {
          ...STATUS,
          phase: "running",
          branch: "v2.3.9",
          steps: steps(["done", "done", "running"]),
        };
      }
      // The daemon restarts onto the new build.
      build.restarted = true;
      return {
        ...STATUS,
        running: { release: "v2.4.1" },
        phase: "ok",
        branch: "v2.3.9",
        steps: steps(["done", "done", "done", "done"]),
      };
    },
  });

  await page.goto("/next/settings/upgrade");
  const card = page.getByTestId("card-upgrade");
  await expect(card.getByTestId("select-upgrade-target")).toHaveValue("v2.3.9");
  await card.getByTestId("start-upgrade-btn").click();
  await page
    .getByTestId("confirm-dialog")
    .getByTestId("confirm-accept")
    .click();
  await expect.poll(() => posts.length).toBe(1);
  expect(posts[0]).toMatchObject({ branch: "v2.3.9" });

  await expect(card.getByTestId("upgrade-current-step")).toContainText(
    "Step 2 of 4: pull image",
  );
  await expect(card.getByTestId("upgrade-step-1")).toContainText("done");
  await expect(card.getByTestId("upgrade-step-2")).toContainText("running");

  // Leave the page: the shell keeps the upgrade in view.
  await page.locator('.nav-rail a[href="/next/inbox"]').first().click();
  await expect(page).toHaveURL(/\/next\/inbox/);
  const banner = page.getByTestId("upgrade-progress-banner");
  await expect(banner).toContainText("Upgrading to v2.3.9");
  await expect(banner).toContainText("step 3/4 verify image");

  // The daemon comes back on another commit: the prompt says so and
  // reloads by itself.
  const reloaded = page.waitForEvent("load", { timeout: 30_000 });
  await expect(page.getByTestId("upgrade-reload-banner")).toContainText(
    "grain was upgraded to v2.4.1",
    { timeout: 20_000 },
  );
  await expect(page.getByTestId("upgrade-reload-banner")).toContainText(
    "Reloading in",
  );
  await reloaded;
  await expect(page.getByTestId("build-stamp")).toContainText("v2.4.1");
  await expect(page.getByTestId("upgrade-reload-banner")).toHaveCount(0);
});

test("a failed upgrade step shows its error in the pane and the shell", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const error = "unauthorized: authentication failed";
  let started = false;
  await stubUpgrade(page, {
    start: () => {
      started = true;
      return {
        ...STATUS,
        phase: "running",
        branch: "v2.3.9",
        steps: steps(["done", "running"]),
      };
    },
    status: () =>
      started
        ? {
            ...STATUS,
            phase: "failed",
            branch: "v2.3.9",
            detail: "upgrade failed at pull image",
            steps: steps(["done", "failed"], { 1: error }),
          }
        : STATUS,
  });

  await page.goto("/next/settings/upgrade");
  const card = page.getByTestId("card-upgrade");
  await expect(card.getByTestId("select-upgrade-target")).toHaveValue("v2.3.9");
  await card.getByTestId("start-upgrade-btn").click();
  await page
    .getByTestId("confirm-dialog")
    .getByTestId("confirm-accept")
    .click();

  await expect(card.getByTestId("upgrade-failed")).toContainText(
    `Upgrade to v2.3.9 failed: ${error}`,
    { timeout: 10_000 },
  );
  await expect(card.getByTestId("upgrade-step-2")).toContainText("failed");
  await expect(card.getByTestId("upgrade-step-2")).toContainText(error);
  await expect(card.getByTestId("upgrade-current-step")).toContainText(
    "Step 2 of 4: pull image",
  );

  await page.locator('.nav-rail a[href="/next/inbox"]').first().click();
  const banner = page.getByTestId("upgrade-failed-banner");
  await expect(banner).toContainText(`Upgrade to v2.3.9 failed: ${error}`);
  await banner.getByTestId("upgrade-failed-dismiss").click();
  await expect(banner).toHaveCount(0);
});

test("an upgrade whose target has not changed says so @phone", async ({
  page,
}) => {
  await stubUpgrade(page, {
    start: () => STATUS,
    status: () => ({
      ...STATUS,
      phase: "ok",
      branch: "v2.3.9",
      unchanged: true,
    }),
  });
  await page.goto("/next/settings/upgrade");
  await expect(page.getByTestId("upgrade-unchanged")).toHaveText(
    "The version on v2.3.9 has not changed, so this deployment was not upgraded.",
  );
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(page.viewportSize().width);
});

// Phone layout: the picker's list, the Image source card, the database
// commands, the step list and the shell banner all stay inside the
// viewport, and nothing scrolls the page sideways.
test("the Upgrade pane and its shell banner fit a phone @phone", async ({
  page,
}) => {
  let started = false;
  await stubUpgrade(page, {
    start: () => {
      started = true;
      return running();
    },
    status: () => (started ? running() : STATUS),
  });
  function running() {
    return {
      ...STATUS,
      phase: "running",
      branch: "v2.3.9",
      steps: steps(["done", "running"], {
        1: "pulling docker.io/grainai/grain:v2.3.9 layer 3 of 12",
      }),
    };
  }
  await page.goto("/next/settings/upgrade");
  const width = page.viewportSize().width;
  const inside = async (locator) => {
    const box = await locator.boundingBox();
    expect(box).not.toBeNull();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width + 1);
  };
  const noSideScroll = async () =>
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(width);

  const card = page.getByTestId("card-upgrade");
  const picker = card.getByTestId("select-upgrade-target");
  await expect(picker).toHaveValue("v2.3.9");
  await picker.click();
  await inside(card.getByTestId("upgrade-target-list"));
  await page.keyboard.press("Escape");
  await inside(card.getByTestId("card-upgrade-image"));
  await inside(card.getByTestId("upgrade-will-pull"));
  await page.getByTestId("upgrade-database-commands").scrollIntoViewIfNeeded();
  await inside(page.getByTestId("upgrade-database-commands"));
  await noSideScroll();

  await card.getByTestId("start-upgrade-btn").click();
  await page
    .getByTestId("confirm-dialog")
    .getByTestId("confirm-accept")
    .click();
  await expect(card.getByTestId("upgrade-step-2")).toContainText("running", {
    timeout: 10_000,
  });
  await inside(card.getByTestId("upgrade-step-2"));
  await noSideScroll();

  await page.goto("/next/inbox");
  const banner = page.getByTestId("upgrade-progress-banner");
  await expect(banner).toContainText("step 2/4 pull image");
  await inside(banner);
  await inside(banner.getByRole("link", { name: "Details" }));
  await noSideScroll();
});
