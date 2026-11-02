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

import { test, expect } from "./fixtures.js";

// Settings → Repos' "New repository" (POST /api/repos/create): the form
// sits beside "Add repo", its host picker lists only hosts a creator is
// registered for, and its answer is shown step by step, with the manual
// fix for a step grain could not do. The demo has no GitHub to create
// on, so the daemon's own answer is asserted for a request it refuses
// before asking GitHub anything, and GitHub's two interesting answers
// -- done, and a personal account with no user token -- are routed in.

async function openDialog(page) {
  await page.goto("/");
  await page.getByRole("button", { name: /^Repos/ }).click();
  const addRepo = page.getByRole("button", { name: "Add repo" });
  const newRepo = page.getByRole("button", { name: "New repository" });
  await expect(newRepo).toBeVisible();
  // Beside "Add repo" (connect existing), on the same row.
  const [a, b] = [await addRepo.boundingBox(), await newRepo.boundingBox()];
  expect(Math.abs(a.y + a.height / 2 - (b.y + b.height / 2))).toBeLessThan(4);
  expect(b.x).toBeGreaterThan(a.x);
  await newRepo.click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("New repository")).toBeVisible();
  return dialog;
}

test("lists only hosts with a creator and shows the daemon's refusal", async ({
  page,
}) => {
  const dialog = await openDialog(page);
  await dialog.getByLabel("Host").click();
  const options = page.getByRole("option");
  await expect(options).toHaveText(["github.com"]);
  await page.keyboard.press("Escape");

  await dialog.getByLabel("Owner").fill("acme");
  await dialog.getByLabel("Name").fill("bad name");
  await dialog.getByRole("button", { name: "Create and connect" }).click();
  await expect(dialog.getByTestId("new-repo-error")).toContainText(
    "not a valid repository owner or name",
  );
});

test("shows each step, and the gh command when a personal account has no token", async ({
  page,
}) => {
  const answers = [
    {
      repo: "sklodowska-dev/radium",
      host: "github.com",
      created: false,
      existed: false,
      connected: false,
      complete: false,
      steps: [
        {
          name: "create",
          status: "failed",
          detail:
            "sklodowska-dev is a personal account, and GitHub lets only that account's own token create a repository there.",
          fix: 'Either store a user token for sklodowska-dev as github-user-token/sklodowska-dev, or create the repository yourself with the command below. Then use "Add repo".',
          command:
            "gh repo create sklodowska-dev/radium --private --add-readme",
        },
      ],
    },
    {
      repo: "acme/widgets",
      host: "github.com",
      created: true,
      existed: false,
      connected: true,
      complete: false,
      default_branch: "main",
      steps: [
        {
          name: "create",
          status: "done",
          detail: "Created a private repository acme/widgets.",
        },
        {
          name: "default-branch",
          status: "done",
          detail: "The default branch main exists.",
        },
        {
          name: "access",
          status: "failed",
          detail:
            "The GitHub App's installation on acme is limited to selected repositories and does not include acme/widgets.",
          fix: "Add acme/widgets to the installation's selected repositories on its settings page.",
          link: "https://github.com/organizations/acme/settings/installations/11",
        },
        {
          name: "connect",
          status: "done",
          detail: "Added acme/widgets to the deployment's target repos.",
        },
      ],
    },
  ];
  const bodies = [];
  await page.route("**/api/repos/create", async (route) => {
    bodies.push(JSON.parse(route.request().postData() || "{}"));
    await route.fulfill({ json: answers[bodies.length - 1] });
  });

  const dialog = await openDialog(page);
  await dialog.getByLabel("Owner").fill("sklodowska-dev");
  await dialog.getByLabel("Name").fill("radium");
  await dialog.getByRole("button", { name: "Create and connect" }).click();

  const create = dialog.getByTestId("new-repo-step-create");
  await expect(create).toHaveAttribute("data-status", "failed");
  await expect(create).toContainText("personal account");
  await expect(dialog.getByTestId("new-repo-command")).toHaveText(
    "gh repo create sklodowska-dev/radium --private --add-readme",
  );
  await expect(dialog.getByTestId("new-repo-result")).toContainText(
    "sklodowska-dev/radium was not created.",
  );
  expect(bodies[0]).toMatchObject({
    host: "github.com",
    owner: "sklodowska-dev",
    name: "radium",
    visibility: "private",
    init: true,
  });

  await dialog.getByLabel("Owner").fill("acme");
  await dialog.getByLabel("Name").fill("widgets");
  await dialog.getByRole("button", { name: "Create and connect" }).click();
  await expect(dialog.getByTestId("new-repo-result")).toContainText(
    "acme/widgets is connected, with a step left for you below.",
  );
  await expect(dialog.getByTestId("new-repo-step-access")).toHaveAttribute(
    "data-status",
    "failed",
  );
  await expect(
    dialog.getByRole("link", {
      name: "https://github.com/organizations/acme/settings/installations/11",
    }),
  ).toBeVisible();
  await expect(dialog.getByTestId("new-repo-step-connect")).toHaveAttribute(
    "data-status",
    "done",
  );
  // The panel stays inside the dialog at a phone's width too.
  await page.setViewportSize({ width: 390, height: 844 });
  const panel = await dialog.getByTestId("new-repo-result").boundingBox();
  const box = await page.getByRole("dialog").boundingBox();
  expect(panel.x + panel.width).toBeLessThanOrEqual(box.x + box.width + 1);
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});
