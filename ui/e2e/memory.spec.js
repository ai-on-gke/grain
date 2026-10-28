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

// What this deployment has learned about working here, read and corrected
// in a real browser: the team's Memory page in the rail (grain/task-333,
// grain/task-3100).
//
// Everything else about memory is tested away from the browser -- the
// component test next door renders MemoryPanel in jsdom with the API
// module mocked out, and pkg/ui/memory_test.go drives the handlers. What
// nothing covered is the route between them: that the tab is reachable
// from the rail, that what the daemon stores actually arrives on the
// pane, and that an edit made here is a PUT the same daemon reads back.
//
// Nothing is stubbed. `grain demo` seeds both layers of memory and a
// document under each (pkg/demo/demo.go's seedDemoMemory), precisely so
// this pane has something real behind it -- so these tests read the
// demo's own rows through the real API, and the edit below lands in the
// real store.
test("shows what this deployment remembers, and each repo's own", async ({
  page,
}) => {
  await page.goto("/settings/memory");

  // The two layers every dispatch is given, deployment first
  // (model.MemoryFor joins them in this order).
  await expect(
    page.getByText("This deployment", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByLabel("Memory carried by every run", { exact: true }),
  ).toContainText("make fmt");
  await expect(page.getByLabel("Memory about acme/widgets")).toContainText(
    "make frontend",
  );

  // And who wrote each of them. The seed writes the deployment's own
  // memory as the memory agent and the repo's as a person, precisely
  // because the two take the same route into the store and the row is
  // the only thing that can tell them apart afterwards
  // (model.Memory.UpdatedBy).
  // Exact, and the first of them: the agent's mark is on the seeded
  // documents as well as on this memory, and the paragraph at the top of
  // the pane says the same words in a sentence.
  await expect(
    page.getByText("written by the memory agent", { exact: true }).first(),
  ).toBeVisible();
  await expect(page.getByText("edited here")).toBeVisible();

  // And the documents no prompt carries: name, summary and size, with
  // the age of each -- which is the whole of what a run is told about
  // one before it decides to read it (model.MemoryIndex).
  await expect(page.getByText("release-checklist")).toBeVisible();
  await expect(
    page.getByText(
      "what a release of this deployment actually needs, in order",
    ),
  ).toBeVisible();

  // The stale mark, on the one seeded note old enough for it. It is the
  // only judgement this pane makes on its own, and it is the failure
  // mode memory has: nothing expires a note, and nothing else in grain
  // will ever notice that this one has stopped being true.
  await expect(page.getByText("flaky-e2e")).toBeVisible();
  await expect(page.getByText(/written \d+ months ago/)).toBeVisible();
});

test("opens a document in place, which the listing did not carry", async ({
  page,
}) => {
  await page.goto("/settings/memory");

  // The listing has every document's name and nobody's text, so this is
  // a read of its own -- GET /api/memory/documents/{name}, the same call
  // read_memory_document makes for a run.
  await page
    .getByText("release-checklist")
    .locator("xpath=ancestor::div[1]")
    .getByRole("button", { name: "Open" })
    .click();
  await expect(page.getByLabel("Document: release-checklist")).toContainText(
    "Tag from `main` only",
  );
  // Each row's buttons are named for its own document and scope, so a
  // screen reader's button list is not a column of identical "Delete"s
  // (grain/task-1532).
  await expect(
    page.getByRole("button", {
      name: "Close release-checklist (this deployment)",
      exact: true,
    }),
  ).toHaveAttribute("aria-expanded", "true");
  await expect(
    page.getByRole("button", {
      name: "Delete flaky-e2e (acme/widgets)",
      exact: true,
    }),
  ).toBeVisible();
});

test("corrects a memory, and the daemon has it on the next open", async ({
  page,
}) => {
  await page.goto("/settings/memory");

  const box = page.getByLabel("Memory about acme/widgets");
  await box.fill("The e2e suite needs a browser download on a cold runner.");
  await page.getByRole("button", { name: "Save acme/widgets memory" }).click();

  // Read back through a fresh load rather than from the box that was
  // typed into: the point is that the write landed in the store the
  // dispatch path reads its prompts out of, not that a controlled input
  // holds what was typed.
  // The page comes back with the reload -- it is a path, not a piece of
  // component state (ui/src/paths.js).
  await page.reload();
  await expect(page.getByLabel("Memory about acme/widgets")).toHaveValue(
    "The e2e suite needs a browser download on a cold runner.",
  );
  await expect(page.getByText("written today")).toBeVisible();
  // And recorded as a person's: the pane names no author, so the daemon
  // attributes the write to its own actor (ui.Client.PutMemory), which
  // is what keeps tonight's memory agent from reading a correction
  // somebody made here as one of its own notes.
  await expect(page.getByText("edited here")).toBeVisible();

  // Restore seeded memory so parallel specs don't observe mutated state.
  await box.fill("make frontend\n");
  await page.getByRole("button", { name: "Save acme/widgets memory" }).click();
});
