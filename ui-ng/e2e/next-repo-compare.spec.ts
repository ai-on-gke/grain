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

// The /next/ repository Compare page (2026-11-14 repository walkthrough,
// docs/test-runs/2026-11-14-repository-release-memory-ui.md, item 2):
// /next/repos/<owner>/<name>/compare/<base>...<head> (and the hosted
// /next/repos/<host>/<owner>/<name>/compare/... form), linked from the
// repository page's Branches tab, over a real `grain demo -github-sim
// -qualification`. Covers branch names containing "/" and "#", reload,
// browser Back, omitted base, identical branches, unknown refs, and phone
// layout.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification"] });

async function findWorkerBareRepo(
  demoServer: string,
  accessCode: string,
): Promise<string> {
  const tmpRoot = os.tmpdir();
  const candidates: string[] = [];
  for (const entry of fs.readdirSync(tmpRoot)) {
    if (!entry.startsWith("grain-e2e-run-")) continue;
    const workerDir = path.join(tmpRoot, entry);
    let subs: string[];
    try {
      subs = fs.readdirSync(workerDir);
    } catch {
      continue;
    }
    for (const sub of subs) {
      const bare = path.join(
        workerDir,
        sub,
        "githubsim",
        "acme",
        "widgets.git",
      );
      if (fs.existsSync(bare)) candidates.push(bare);
    }
  }

  const probe = `e2e-probe-${process.pid}-${Math.random().toString(36).slice(2, 8)}`;
  for (const bare of candidates) {
    try {
      const mainSha = execFileSync(
        "git",
        ["--git-dir", bare, "rev-parse", "refs/heads/main"],
        { encoding: "utf8" },
      ).trim();
      execFileSync("git", [
        "--git-dir",
        bare,
        "update-ref",
        `refs/heads/${probe}`,
        mainSha,
      ]);
      try {
        const res = await fetch(
          `${demoServer}/api/repos/acme/widgets/github-branches?refresh=1`,
          { headers: { Authorization: `Bearer ${accessCode}` } },
        );
        if (res.ok) {
          const data = await res.json();
          const list: string[] = Array.isArray(data)
            ? data
            : (data?.branches ?? data?.items ?? []);
          if (list.includes(probe)) return bare;
        }
      } finally {
        execFileSync("git", [
          "--git-dir",
          bare,
          "update-ref",
          "-d",
          `refs/heads/${probe}`,
        ]);
      }
    } catch {
      // Candidate belonged to a worker tearing down concurrently.
    }
  }
  throw new Error(
    `could not locate githubsim/acme/widgets.git for ${demoServer}`,
  );
}

function withPushedBranches<T>(
  bareRepo: string,
  branches: Record<string, Record<string, string>>,
  fn: () => Promise<T>,
): Promise<T> {
  const work = fs.mkdtempSync(path.join(os.tmpdir(), "grain-cmp-seed-"));
  const created = Object.keys(branches);
  try {
    execFileSync("git", ["clone", "-q", bareRepo, work]);
    for (const [branch, files] of Object.entries(branches)) {
      execFileSync("git", [
        "-C",
        work,
        "checkout",
        "-qb",
        branch,
        "origin/main",
      ]);
      for (const [rel, content] of Object.entries(files)) {
        const full = path.join(work, rel);
        fs.mkdirSync(path.dirname(full), { recursive: true });
        fs.writeFileSync(full, content);
      }
      execFileSync("git", ["-C", work, "add", "."]);
      execFileSync("git", [
        "-C",
        work,
        "-c",
        "user.name=Compare Seed",
        "-c",
        "user.email=seed@example.com",
        "commit",
        "-qm",
        `Seed ${branch}`,
      ]);
      execFileSync("git", [
        "-C",
        work,
        "push",
        "-q",
        "-f",
        "origin",
        `HEAD:refs/heads/${branch}`,
      ]);
    }
  } finally {
    fs.rmSync(work, { recursive: true, force: true });
  }

  return fn().finally(() => {
    for (const branch of created) {
      try {
        execFileSync("git", [
          "--git-dir",
          bareRepo,
          "update-ref",
          "-d",
          `refs/heads/${branch}`,
        ]);
      } catch {
        // Ignore cleanup errors if ref was already removed.
      }
    }
  });
}

test("Branches tab opens Compare for slash and # branch names, and reload and Back keep the comparison", async ({
  page,
  demoServer,
  accessCode,
  pageErrors,
}) => {
  const bare = await findWorkerBareRepo(demoServer, accessCode);
  await withPushedBranches(
    bare,
    {
      "feature/walk-5865": {
        "WALK5865.md": "# Slash branch\nAdded on feature/walk-5865.\n",
      },
      "fix/#5865-hash": {
        "WALK5865.md": "# Hash branch\nFixed on fix/#5865-hash.\n",
      },
    },
    async () => {
      await page.goto("/next/repos/acme/widgets/branches");
      const baseInput = page.getByLabel("Base branch");
      const headInput = page.getByLabel("Head branch");
      const compareBtn = page.getByRole("button", {
        name: "Compare",
        exact: true,
      });

      await expect(baseInput).toHaveValue("main");
      await expect(compareBtn).toBeDisabled();

      // Compare a branch with "/" in its name.
      await headInput.fill("feature/walk-5865");
      await expect(compareBtn).toBeEnabled();
      await compareBtn.click();

      await expect(page).toHaveURL(
        /\/next\/repos\/acme\/widgets\/compare\/main\.\.\.feature\/walk-5865$/,
      );
      await expect(page.getByRole("heading", { level: 1 })).toHaveText(
        "feature/walk-5865",
      );
      const changes = page.getByRole("region", { name: "Branch changes" });
      await expect(changes).toContainText("WALK5865.md");
      await expect(changes).toContainText("Added on feature/walk-5865.");

      // Browser Back returns to the Branches tab.
      await page.goBack();
      await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/branches$/);

      // Compare a branch with "#" in its name; "%" encoding keeps "#" out of location.hash.
      await headInput.fill("fix/#5865-hash");
      await compareBtn.click();

      await expect(page).toHaveURL(
        /\/next\/repos\/acme\/widgets\/compare\/main\.\.\.fix\/%235865-hash$/,
      );
      expect(new URL(page.url()).hash).toBe("");
      await expect(page.getByRole("heading", { level: 1 })).toHaveText(
        "fix/#5865-hash",
      );
      await expect(changes).toContainText("Fixed on fix/#5865-hash.");

      // Reload must compare the same "#"-bearing branch rather than truncating at "fix/".
      await page.reload();
      await expect(page).toHaveURL(
        /\/next\/repos\/acme\/widgets\/compare\/main\.\.\.fix\/%235865-hash$/,
      );
      await expect(page.getByRole("heading", { level: 1 })).toHaveText(
        "fix/#5865-hash",
      );
      await expect(changes).toContainText("Fixed on fix/#5865-hash.");

      await page.getByRole("link", { name: "← acme/widgets" }).click();
      await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets$/);
      expect(pageErrors).toEqual([]);
    },
  );
});

test("Compare resolves an omitted base, shows identical-branch empty state, and renders a designed error state for unknown refs and hosted URLs", async ({
  page,
  demoServer,
  accessCode,
  pageErrors,
}) => {
  const bare = await findWorkerBareRepo(demoServer, accessCode);
  await withPushedBranches(
    bare,
    {
      "feature/omitted-base-5865": {
        "OMITTED.md": "Compared against server-resolved default branch.\n",
      },
    },
    async () => {
      // Omitting "<base>..." asks the server for the repository's default branch ("main").
      await page.goto(
        "/next/repos/acme/widgets/compare/feature/omitted-base-5865",
      );
      await expect(page.getByRole("heading", { level: 1 })).toHaveText(
        "feature/omitted-base-5865",
      );
      await expect(page.getByTestId("compare-subtitle")).toContainText("main");
      await expect(
        page.getByRole("region", { name: "Branch changes" }),
      ).toContainText("Compared against server-resolved default branch.");

      // main and release/1.0 share the same initial tree in -github-sim.
      await page.goto("/next/repos/acme/widgets/compare/main...release/1.0");
      await expect(
        page.getByRole("heading", {
          name: "There are no changes between these branches.",
        }),
      ).toBeVisible();

      // Unknown ref renders the designed error alert with the server's explanation and a link back to Branches.
      await page.goto("/next/repos/acme/widgets/compare/main...nosuch-5865");
      const alert = page.getByRole("alert");
      await expect(alert).toContainText("Could not compare main...nosuch-5865");
      await expect(alert).toContainText(
        "GitHub has nothing to compare for main...nosuch-5865 in acme/widgets: one of the two branches does not exist",
      );
      await alert.getByRole("link", { name: "Choose another branch" }).click();
      await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/branches$/);

      // Hosted repository form: /next/repos/<host>/<owner>/<name>/compare/<base>...<head>
      await page.goto(
        "/next/repos/git.example.com/acme/widgets/compare/main...feature/hosted-5865",
      );
      await expect(page.getByRole("heading", { level: 1 })).toHaveText(
        "feature/hosted-5865",
      );
      await expect(
        page.getByRole("link", { name: "← git.example.com/acme/widgets" }),
      ).toBeVisible();
      await expect(page.getByRole("alert")).toBeVisible();
      expect(pageErrors).toEqual([]);
    },
  );
});

test("Compare fits a phone screen without sideways scroll and passes axe in both themes @phone @dark", async ({
  page,
  demoServer,
  accessCode,
  axe,
}) => {
  const bare = await findWorkerBareRepo(demoServer, accessCode);
  await withPushedBranches(
    bare,
    {
      "fix/#5865-phone": {
        "src/very/long/path/widget-renderer.ts": [
          "export function renderWidgetTitle(name: string): string {",
          "  return `widget:${name}:long-unbroken-identifier-to-check-hunk-scroll-containment`;",
          "}",
          "",
        ].join("\n"),
      },
    },
    async () => {
      await page.goto(
        "/next/repos/acme/widgets/compare/main...fix/%235865-phone",
      );
      const heading = page.getByRole("heading", {
        level: 1,
        name: "fix/#5865-phone",
      });
      await expect(heading).toBeVisible();

      const changes = page.getByRole("region", { name: "Branch changes" });
      await expect(changes).toContainText("widget-renderer.ts");

      const vw = page.viewportSize()!.width;
      const hBox = (await heading.boundingBox())!;
      expect(hBox.x).toBeGreaterThanOrEqual(0);
      expect(hBox.x + hBox.width).toBeLessThanOrEqual(vw);

      const pill = page.getByTestId("compare-status-pill");
      await expect(pill).toBeVisible();
      const pBox = (await pill.boundingBox())!;
      expect(pBox.x + pBox.width).toBeLessThanOrEqual(vw);
      expect(
        Math.abs(pBox.y + pBox.height / 2 - (hBox.y + hBox.height / 2)),
      ).toBeLessThanOrEqual(2);

      const cBox = (await changes.boundingBox())!;
      expect(cBox.x).toBeGreaterThanOrEqual(0);
      expect(cBox.x + cBox.width).toBeLessThanOrEqual(vw);

      const scrollW = await page.evaluate(
        () => document.documentElement.scrollWidth,
      );
      expect(scrollW).toBeLessThanOrEqual(vw);
      expect(await axe()).toEqual([]);
    },
  );
});
