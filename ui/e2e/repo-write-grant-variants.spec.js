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

// repo#write's arguments are a top-level oneOf -- {repo}, {repo, base}
// or {refs, force} -- which the task pane's Add grant form once drew as
// no fields at all and "matches none of the options" (grain/task-4400).
// It is a variant picker now, and {refs, force} grants as soon as it is
// picked: force a fixed ticked box, refs two fixed chips.

const WRITE = "https://grain.dev/capabilities/repo#write";
const ANY_REF = `${WRITE}={"force":true,"refs":["refs/heads/*","refs/tags/*"]}`;

test("grants repo#write {refs, force} from a task's Add grant", async ({
  page,
  request,
}) => {
  const created = await request.post("/api/tasks", {
    data: {
      title: `E2E repo#write variants ${Date.now()}`,
      repo: "acme/widgets",
    },
  });
  expect(created.ok()).toBeTruthy();
  const task = await created.json();
  await page.goto(`/tasks/${task.id}`);

  const grants = page.getByTestId(`task-${task.id}-grants`);
  await grants.getByRole("button", { name: "Add grant…" }).click();
  await page.getByRole("menuitem", { name: /repo server › write/ }).click();

  const draft = grants.locator(`[data-method="${WRITE}"] [data-grant-draft]`);
  await expect(draft).toBeVisible();
  // The first variant is drawn with its field, and the error names
  // every variant and what it lacks.
  await expect(draft.getByLabel(/^repo/)).toBeVisible();
  await expect(draft).toContainText(
    "Push any branch/tag (refs + force): refs is required, force is required",
  );

  const picker = draft.getByRole("combobox", { name: "Kind of write" });
  await picker.click();
  await expect(page.getByRole("option")).toHaveText([
    "CI push to another repo",
    "Write target (repo + base)",
    "Push any branch/tag (refs + force)",
  ]);
  await page
    .getByRole("option", { name: "Push any branch/tag (refs + force)" })
    .click();

  const held = grants.locator(`[data-grant="${ANY_REF.replace(/"/g, '\\"')}"]`);
  await expect(held).toHaveCount(1);
  await expect(draft).toHaveCount(0);
  const force = held.getByRole("checkbox", { name: /force/ });
  await expect(force).toBeChecked();
  await expect(force).toBeDisabled();
  const refs = held.getByRole("group", { name: /refs/ });
  await expect(refs.getByText("refs/heads/*")).toBeVisible();
  await expect(refs.getByText("refs/tags/*")).toBeVisible();
  await expect(held).not.toContainText("matches none");

  const detail = await (await request.get(`/api/tasks/${task.id}`)).json();
  expect(detail.capabilities).toContain(ANY_REF);
});
