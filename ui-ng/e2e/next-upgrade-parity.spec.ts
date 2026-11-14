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

// Settings -> Upgrade brought level with the classic Upgrade pane
// (task 5524, after the 2026-11-13 deployment-operations walkthrough):
// retracted/discarded warnings with their actions, a searchable target
// picker, formatted release notes that flag breaking changes, the image
// an image deployment would pull, the post-upgrade check shown read-only
// with a link to General, and the database backend panel.
//
// The worker's demo has no upgrader, so every upgrade route is stubbed
// here, and POST /api/upgrade is answered by the stub: nothing is ever
// upgraded.

import type { Page, Route } from "@playwright/test";
import { expect, test } from "./fixtures";

type Json = Record<string, unknown>;

interface Stubs {
  status: Json;
  targets: Json;
  notes: (branch: string) => Json;
}

const IMAGE_STATUS: Json = {
  enabled: true,
  mode: "image",
  phase: "",
  running: {
    release: "v3.6.0",
    commit: "4ae6438fdcd4a0fc373fb3da76888724685b75fd",
  },
  channel: "latest",
  imageRepo: "docker.io/grainai/grain",
  defaultImageRepo: "docker.io/grainai/grain",
  imageRepos: ["docker.io/grainai/grain", "ghcr.io/acme/grain"],
  retractedRelease: { running: "v3.6.0", latest: "v3.5.1", newer: "v3.6.1" },
  database: { driver: "sqlite", dsn: "/var/lib/grain/data/grain.db" },
};

const TARGETS: Json = {
  targets: [
    { name: "latest", kind: "channel", allowed: true },
    { name: "candidate", kind: "channel", allowed: true },
    { name: "main", kind: "channel", allowed: false },
    { name: "v3.6.1", kind: "version", allowed: true },
    { name: "v3.6.0", kind: "version", allowed: true },
    { name: "v3.5.1", kind: "version", allowed: true },
  ],
  allowedBranches: ["latest", "candidate", "v*"],
  default: "latest",
};

function notesFor(branch: string): Json {
  if (branch === "v3.6.1") {
    return {
      branch,
      from: "v3.6.0",
      version: "v3.6.1",
      notes: [
        {
          version: "v3.6.1",
          title: "v3.6.1",
          markdown:
            "# v3.6.1\n\n## Breaking changes\n\n- **Breaking:** `-old-flag` is gone; use `-new-flag`.\n\n## Fixes\n\n- Upgrades keep their schedule.",
        },
      ],
    };
  }
  return {
    branch,
    from: "v3.6.0",
    version: "v3.5.1",
    message: "v3.5.1 is not newer than v3.6.0",
    notes: [],
  };
}

async function stubUpgrade(page: Page, stubs: Stubs) {
  const posts: Json[] = [];
  const noteReads: string[] = [];
  await page.route("**/api/upgrade", async (route: Route) => {
    const req = route.request();
    if (req.method() === "GET") {
      await route.fulfill({ json: stubs.status });
      return;
    }
    // Never forwarded: the demo must not be asked to upgrade.
    posts.push({ method: req.method(), body: req.postDataJSON() });
    await route.fulfill({
      json: { ...stubs.status, phase: "running", branch: "v3.6.1" },
    });
  });
  await page.route("**/api/upgrade/targets", (route) =>
    route.fulfill({ json: stubs.targets }),
  );
  await page.route("**/api/upgrade/release-notes?**", (route) => {
    const branch = new URL(route.request().url()).searchParams.get("branch");
    noteReads.push(branch ?? "");
    return route.fulfill({ json: stubs.notes(branch ?? "") });
  });
  return { posts, noteReads };
}

test("Settings -> Upgrade warns of a retracted release, searches targets, flags breaking notes and posts the chosen image", async ({
  page,
  axe,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const { posts, noteReads } = await stubUpgrade(page, {
    status: IMAGE_STATUS,
    targets: TARGETS,
    notes: notesFor,
  });
  await page.goto("/next/settings/upgrade");
  const card = page.getByTestId("card-upgrade");
  await expect(card.getByTestId("current-daemon-version")).toHaveText("v3.6.0");

  // The retracted release is said, with the way forward as an action.
  const retracted = card.getByTestId("upgrade-retracted-release");
  await expect(retracted).toContainText("Release v3.6.0 was retracted.");
  await expect(retracted).toContainText("v3.6.1 has been published");
  await retracted.getByRole("button", { name: "Upgrade to v3.6.1" }).click();
  const box = card.getByRole("combobox", { name: "Branch" });
  await expect(box).toHaveValue("v3.6.1");
  await expect.poll(() => noteReads.at(-1)).toBe("v3.6.1");

  // Release notes render as Markdown, and the breaking change is warned of.
  const notes = card.getByTestId("upgrade-release-notes");
  await expect(
    notes.getByRole("heading", { name: "What v3.6.1 brings" }),
  ).toBeVisible();
  await expect(card.getByTestId("upgrade-breaking-warning")).toContainText(
    "v3.6.1 lists breaking changes.",
  );
  const note = notes.getByTestId("upgrade-release-note-v3.6.1");
  await expect(note).toContainText("Breaking changes");
  await expect(note.locator("strong", { hasText: "Breaking:" })).toBeVisible();
  await expect(note).not.toContainText("**Breaking");
  await expect(note.getByRole("heading", { name: "Fixes" })).toBeVisible();

  // The picker is searchable, grouped, marks the retracted release and
  // refuses a branch the deployment does not allow.
  await box.fill("");
  await box.click();
  const list = page.getByRole("listbox", { name: "Upgrade targets" });
  await expect(list).toBeVisible();
  await expect(
    list.getByRole("group", { name: "Standard branches" }),
  ).toBeVisible();
  const main = list.getByRole("option", { name: /^main/ });
  await expect(main).toHaveAttribute("aria-disabled", "true");
  await expect(main).toContainText("not allowed on this deployment");
  await expect(list.getByRole("option", { name: /^v3\.6\.0/ })).toContainText(
    "Retracted",
  );
  await box.fill("3.5");
  await expect(list.getByRole("option")).toHaveCount(1);
  await box.press("ArrowDown");
  await box.press("Enter");
  await expect(box).toHaveValue("v3.5.1");
  await expect(list).toBeHidden();
  await expect(notes).toContainText("v3.5.1 is not newer than v3.6.0.");
  await expect(card.getByTestId("upgrade-breaking-warning")).toHaveCount(0);

  // Back to the fix-forward release for the rest.
  await retracted.getByRole("button", { name: "Upgrade to v3.6.1" }).click();
  await expect(card.getByTestId("upgrade-breaking-warning")).toBeVisible();

  // The image this upgrade pulls, and another repository to pull it from.
  const image = card.getByTestId("upgrade-image");
  await expect(card.getByTestId("upgrade-will-pull")).toHaveText(
    "Will pull docker.io/grainai/grain:v3.6.1",
  );
  const imageBox = image.getByRole("textbox", { name: "Image (optional)" });
  await expect(imageBox).toBeDisabled();
  await card
    .getByRole("switch", { name: "Drain running tasks before restarting" })
    .click();
  await expect(imageBox).toBeEnabled();
  await image.getByRole("button", { name: "ghcr.io/acme/grain" }).click();
  await expect(card.getByTestId("upgrade-will-pull")).toHaveText(
    "Will pull ghcr.io/acme/grain:v3.6.1",
  );

  expect(await axe()).toEqual([]);

  // Start upgrade asks, naming the image and the breaking change, and
  // only the accept posts.
  await card.getByTestId("start-upgrade-btn").click();
  const dialog = page.getByTestId("confirm-dialog");
  await expect(dialog).toContainText("Upgrade grain to v3.6.1?");
  await expect(dialog).toContainText("It pulls ghcr.io/acme/grain:v3.6.1.");
  await expect(dialog).toContainText("breaking changes");
  expect(posts).toEqual([]);
  await page.getByTestId("confirm-accept").click();
  await expect.poll(() => posts.length).toBe(1);
  expect(posts[0]).toEqual({
    method: "POST",
    body: {
      branch: "v3.6.1",
      afterTasksFinish: false,
      image: "ghcr.io/acme/grain",
    },
  });
});

test("Settings -> Upgrade shows the check mode read-only and the SQLite migration commands", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await stubUpgrade(page, {
    status: IMAGE_STATUS,
    targets: TARGETS,
    notes: notesFor,
  });
  await page.goto("/next/settings/upgrade");
  const card = page.getByTestId("card-upgrade");

  const db = card.getByTestId("upgrade-database");
  await expect(db).toContainText("This deployment runs on SQLite");
  await expect(card.getByTestId("upgrade-database-commands")).toContainText(
    "sudo ./scripts/setup.sh --db-driver postgres",
  );
  await expect(db).toContainText("moved only with the daemon stopped");

  // The check mode is not editable here: it is set on General.
  const check = card.getByTestId("upgrade-check-mode");
  await expect(check).toContainText("After an upgrade:");
  await expect(check.getByRole("button")).toHaveCount(0);
  await expect(card.getByTestId("select-upgrade-check-mode")).toHaveCount(0);
  await check.getByRole("link", { name: "Change in General" }).click();
  await expect(page).toHaveURL(/\/next\/settings\/general#upgrade-check$/);
  const general = page.getByTestId("card-upgrade-check");
  await expect(general.getByTestId("select-upgrade-check-mode")).toBeVisible();
  await expect(
    general.getByRole("switch", {
      name: "Hold the queue until the upgrade check is done",
    }),
  ).toBeVisible();
});

test("Settings -> Upgrade warns of a discarded candidate and fits a phone @phone", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubUpgrade(page, {
    status: {
      ...IMAGE_STATUS,
      retractedRelease: undefined,
      running: { release: "v3.7.0-rc.2" },
      imageRepoWarning:
        "It names no registry host, so an upgrade would be refused.",
      imageRepo: "grainai/grain",
      database: { driver: "postgres", dsn: "postgres://grain@db/grain" },
    },
    targets: {
      ...TARGETS,
      discardedRc: { running: "v3.7.0-rc.2", latest: "v3.6.1" },
    },
    notes: notesFor,
  });
  await page.goto("/next/settings/upgrade");
  const card = page.getByTestId("card-upgrade");
  const discarded = card.getByTestId("upgrade-discarded-rc");
  await expect(discarded).toContainText(
    "Release candidate v3.7.0-rc.2 was discarded.",
  );
  await expect(discarded).toContainText("v3.7.0-rc.3");
  await expect(card.getByTestId("upgrade-image-repo-warning")).toContainText(
    "names no registry host",
  );
  await discarded
    .getByRole("button", { name: "Move to latest (v3.6.1)" })
    .click();
  await expect(card.getByRole("combobox", { name: "Branch" })).toHaveValue(
    "latest",
  );

  // PostgreSQL: the panel says so and lists no SQLite migration.
  await expect(card.getByTestId("upgrade-database")).toContainText(
    "This deployment runs on PostgreSQL",
  );
  await expect(card.getByTestId("upgrade-database-commands")).toHaveCount(0);

  // Nothing on the pane is wider than the phone.
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  const cardBox = await card.boundingBox();
  for (const id of [
    "upgrade-discarded-rc",
    "upgrade-image-repo-warning",
    "upgrade-database",
  ]) {
    const b = await card.getByTestId(id).boundingBox();
    expect(b!.x).toBeGreaterThanOrEqual(cardBox!.x);
    expect(b!.x + b!.width).toBeLessThanOrEqual(
      cardBox!.x + cardBox!.width + 1,
    );
  }
});
