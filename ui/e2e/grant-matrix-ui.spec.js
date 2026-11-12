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

import fs from "node:fs";
import path from "node:path";
import { expect, test } from "./fixtures.js";
import {
  argVariants,
  deref,
  expectedRefusal,
  knownGap,
  listCapabilityMethods,
  policyFor,
  shapeKey,
  SURFACES,
} from "./grantMatrix.js";

// The grant matrix's UI half (grain/task-4401): every method GET
// /api/capability-methods lists, given and taken away through the real
// "Add grant…" form on the task pane, and one method of each argument
// shape (grantMatrix.js shapeKey) through the New task form -- picked from
// the menu, its required arguments filled in the fields the form draws,
// saved, reloaded, checked against what POST /api/capability-grants/check
// records for the same arguments, removed. A CI-only method must be
// listed disabled saying so, never granted; grain#ci must be refused
// with the CI Mode message. A cell in grantMatrix.js's KNOWN_GAPS is run
// and recorded but does not fail the spec.
//
// GRANT_MATRIX_OUT=<file> writes every cell's outcome as JSON and
// GRANT_MATRIX_SHOTS=<dir> a screenshot of each failing or known-gap
// form, which is what docs/test-runs/*-grant-ui-matrix.md is made from.

const OUT = process.env.GRANT_MATRIX_OUT;
const SHOTS = process.env.GRANT_MATRIX_SHOTS;
const TASK_KIND = SURFACES.find((s) => s.id === "task-pane");
// GRANT_MATRIX_ONLY=<regexp> narrows the walk to the methods it matches.
const ONLY = process.env.GRANT_MATRIX_ONLY
  ? new RegExp(process.env.GRANT_MATRIX_ONLY)
  : null;

async function methodsToWalk(request) {
  const all = await listCapabilityMethods(request);
  return ONLY ? all.filter((m) => ONLY.test(m.ref)) : all;
}

function heldLocator(grants, ref) {
  return grants.locator(
    `[data-grant="${ref}"], [data-grant^="${ref}="], [data-api-held="${ref}"]`,
  );
}

async function recordedFor(request, ref, args) {
  const res = await request.post("/api/capability-grants/check", {
    data: {
      grants: [args ? { capability: ref, with: args } : { capability: ref }],
    },
  });
  return (await res.json())[0];
}

async function held(request, id, ref) {
  const task = await (await request.get(`/api/tasks/${id}`)).json();
  return (task.capabilities || []).filter(
    (c) => c === ref || c.startsWith(`${ref}=`),
  );
}

// API_REF is the api server's reference prefix: its methods are listed
// under "Grain API presets/scopes" by their api:<name> id, not by ref.
const API_REF = "https://grain.dev/capabilities/api#";

async function openMenu(page, grants, method) {
  await grants.getByRole("button", { name: "Add grant…" }).click();
  const search = page.getByRole("textbox", { name: "Search grants" });
  if (method.ref.startsWith(API_REF)) {
    const name = method.ref.slice(API_REF.length);
    await search.fill(name);
    return page.locator(`[role^="menuitem"][data-api-grant="api:${name}"]`);
  }
  await search.fill(method.ref);
  return page.locator(`[role^="menuitem"][data-method="${method.ref}"]`);
}

// fillRequired types the required arguments of args into the draft's
// fields: enums through their select, everything else as text.
async function fillRequired(page, section, method, args) {
  const root = method.args;
  const schema = deref(root, root);
  for (const name of schema.required || []) {
    if (!(name in (args || {}))) continue;
    const sub = deref(root, schema.properties?.[name]) || {};
    const value = args[name];
    if (Array.isArray(sub.enum)) {
      const field = section.locator(`[id*='-args-'][id$='-${name}']`).first();
      const current = (await field.textContent().catch(() => "")) || "";
      if (current.trim() === String(value)) continue;
      await field.click();
      await page
        .getByRole("option", { name: String(value), exact: true })
        .click();
    } else if (typeof value === "string") {
      await section
        .getByLabel(new RegExp(`^${name}`))
        .first()
        .fill(value);
    }
  }
}

async function shoot(page, name) {
  if (!SHOTS) return "";
  fs.mkdirSync(SHOTS, { recursive: true });
  const file = path.join(
    SHOTS,
    `${name.replace(/[^A-Za-z0-9_.-]+/g, "_")}.png`,
  );
  await page.screenshot({ path: file, fullPage: false });
  return file;
}

// closePopovers presses Escape only while a menu or listbox is open,
// waiting after each press for the one it closed to go: an Escape with
// nothing open is the surface's own, and closes the task pane (or the New
// task form) the next cell needs -- which is how one cell left every
// later one timing out on an "Add grant…" that was no longer there.
// "Open" is what Overlay's own Escape guard reads: the grants menu's
// [data-open-popover] goes the moment it closes, where its .MuiPopover-root
// outlives it for a tick, and an Escape in that tick is the surface's.
async function closePopovers(page) {
  const open = page.locator(
    "[data-open-popover], .MuiPopper-root, [role=listbox]",
  );
  for (let i = 0; i < 3; i++) {
    const before = await open.count().catch(() => 0);
    if (before === 0) return;
    await page.keyboard.press("Escape").catch(() => {});
    await expect
      .poll(() => open.count().catch(() => 0), { timeout: 2_000 })
      .toBeLessThan(before)
      .catch(() => {});
  }
}

// walkCell gives method on the grants region of one surface and takes it
// away again, returning {outcome, detail}. onSaved runs once the grant
// is held, for the surface's own save-and-reload.
async function walkCell(page, request, grants, method, surface, onSaved) {
  const ref = method.ref;
  const [{ variant, args }] = argVariants(method);
  const policy = policyFor(method, surface);
  const refusal = expectedRefusal(ref, variant);
  const item = await openMenu(page, grants, method);
  if (policy === "ci_only") {
    // Listed, disabled, saying it is a CI capability -- or not listed at
    // all where a task holds the same access another way (repo#read is
    // a task's reads list, offered as "Read a repository").
    if ((await item.count()) === 0) {
      await page.keyboard.press("Escape");
      return { outcome: "refused", variant, detail: "not offered: CI-only" };
    }
    await expect(item).toHaveCount(1);
    await expect(item).toHaveAttribute("data-ci-only", "true");
    await expect(item).toHaveAttribute("aria-disabled", "true");
    await page.keyboard.press("Escape");
    return { outcome: "refused", variant, detail: "listed disabled: CI-only" };
  }
  if ((await item.count()) === 0) {
    await page.keyboard.press("Escape");
    if (refusal) return { outcome: "refused", variant, detail: "not offered" };
    return { outcome: "fail", variant, detail: "not offered in Add grant…" };
  }
  await item.first().click();
  // An api preset or scope is a checkbox in the menu, which stays open
  // for the next pick; left open it is a modal, and the form behind it is
  // aria-hidden and out of reach.
  await closePopovers(page);
  if (refusal) {
    await expect(grants.getByText(refusal).first()).toBeVisible();
    await expect(heldLocator(grants, ref)).toHaveCount(0);
    return { outcome: "refused", variant, detail: `refused: ${refusal}` };
  }
  const draft = grants.locator(`[data-method="${ref}"] [data-grant-draft]`);
  if (method.requiresArgs) {
    await expect(draft).toBeVisible();
    await fillRequired(page, draft, method, args);
  }
  const line = heldLocator(grants, ref);
  await expect(line).toHaveCount(1);
  await expect(draft).toHaveCount(0);
  const stored = await line.getAttribute("data-grant");

  // One line, in the canonical spelling: what check records for the
  // arguments the form saved.
  const checked = await recordedFor(
    request,
    ref,
    stored && stored.includes("=")
      ? JSON.parse(stored.slice(stored.indexOf("=") + 1))
      : null,
  );
  if (stored && checked.recorded !== stored)
    return {
      outcome: "fail",
      variant,
      detail: `form holds ${stored}, check records ${checked.recorded}`,
    };
  const after = await onSaved(stored || checked.recorded);
  await expect(heldLocator(after, ref)).toHaveCount(1);

  // An invalid value in a pattern-checked field is refused inline,
  // naming the field, and not saved.
  const schema = deref(method.args, method.args) || {};
  const patterned = Object.entries(schema.properties || {}).find(
    ([, s]) => deref(method.args, s)?.pattern && !deref(method.args, s)?.enum,
  );
  let detail = "";
  if (patterned) {
    const section = heldLocator(after, ref);
    const field = section.getByLabel(new RegExp(`^${patterned[0]}`)).first();
    if (await field.isVisible().catch(() => false)) {
      await field.fill("Not valid!");
      await expect(section.getByText(/must match/).first()).toBeVisible();
      detail = `invalid ${patterned[0]} refused inline`;
      await field.fill((args || {})[patterned[0]] || "");
    }
  }

  await heldLocator(after, ref)
    .first()
    .getByRole("button", { name: /^Remove / })
    .first()
    .click();
  await expect(heldLocator(after, ref)).toHaveCount(0);
  return { outcome: "pass", variant, detail };
}

test.describe.configure({ mode: "serial" });
// About ten minutes over every method, so it runs in nightly
// (.github/workflows/nightly.yml, grant-matrix-ui) rather than on every
// pull request, where grant-matrix-api.spec.js covers the same matrix.
test.skip(
  !process.env.GRANT_MATRIX_UI,
  "the UI grant matrix runs nightly; set GRANT_MATRIX_UI=1 to run it",
);
// A cell whose field never appears is a failing cell, not a stalled run:
// without a bound an action waits out the whole walk.
test.use({ actionTimeout: 5_000 });

const results = [];

test.afterAll(() => {
  if (OUT) fs.writeFileSync(OUT, JSON.stringify({ results }, null, 2));
});

async function runCell(page, request, method, surface, run) {
  const cell = { ref: method.ref, surface: surface.id };
  const gap = knownGap(method.ref, argVariants(method)[0].variant);
  try {
    Object.assign(cell, await run());
  } catch (err) {
    cell.outcome = "fail";
    cell.detail = String(err.message || err).split("\n")[0];
  }
  if (cell.outcome === "fail") {
    cell.shot = await shoot(page, `${surface.id}-${method.ref}`);
    await closePopovers(page);
    if (gap) {
      cell.outcome = "known";
      cell.knownGap = gap;
    }
  }
  results.push(cell);
  console.log(
    `grant matrix: ${cell.surface} ${cell.ref} ${cell.outcome} ${cell.detail || ""}`,
  );
  if (OUT) fs.writeFileSync(OUT, JSON.stringify({ results }, null, 2));
  return cell;
}

test("every method is given and removed through the task pane's Add grant…", async ({
  page,
  request,
}) => {
  test.setTimeout(20 * 60_000);
  const methods = await methodsToWalk(request);
  const surface = TASK_KIND;
  const created = await request.post("/api/tasks", {
    data: {
      title: `E2E grant matrix UI ${Date.now()}`,
      approved: true,
      noRepo: true,
      capabilities: [],
    },
  });
  const task = await created.json();
  const grantsOf = () => page.getByTestId(`task-${task.id}-grants`);
  await page.goto(`/tasks/${task.id}`);
  await expect(grantsOf()).toBeVisible();

  const failures = [];
  for (const method of methods) {
    const cell = await runCell(page, request, method, surface, () =>
      walkCell(page, request, grantsOf(), method, surface, async (stored) => {
        expect(await held(request, task.id, method.ref)).toEqual([stored]);
        await page.reload();
        await expect(grantsOf()).toBeVisible();
        return grantsOf();
      }),
    );
    if (cell.outcome === "fail") failures.push(`${method.ref}: ${cell.detail}`);
    // Whatever the cell did, the next starts from a task holding nothing,
    // on a freshly loaded task pane: a cell that did not pass may have
    // left a menu, a draft or a closed pane behind, and reloading
    // whatever page that left is not the same as opening the task again.
    const reset = await request.patch(`/api/tasks/${task.id}`, {
      data: { capabilities: [] },
    });
    expect(reset.ok(), await reset.text()).toBe(true);
    if (cell.outcome !== "pass") {
      await page.goto(`/tasks/${task.id}`);
      await expect(grantsOf()).toBeVisible();
    }
  }
  expect(failures, failures.join("\n")).toEqual([]);
});

test("one method of each argument shape is given and removed through the New task form", async ({
  page,
  request,
}) => {
  test.setTimeout(10 * 60_000);
  const methods = await methodsToWalk(request);
  const seen = new Set();
  const shapes = methods.filter((m) => {
    const key = shapeKey(m);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  const surface = SURFACES.find((s) => s.id === "new-task");
  const failures = [];
  for (const method of shapes) {
    await page.goto("/");
    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    const grants = page.getByTestId("new-task-grants");
    const cell = await runCell(page, request, method, surface, () =>
      walkCell(page, request, grants, method, surface, async () => grants),
    );
    if (cell.outcome === "fail") failures.push(`${method.ref}: ${cell.detail}`);
    await page.keyboard.press("Escape").catch(() => {});
  }
  expect(failures, failures.join("\n")).toEqual([]);
});
