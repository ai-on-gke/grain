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
import { chooseOption } from "./searchSelect.js";
import {
  argVariants,
  deref,
  expectedRefusal,
  isSecret,
  knownGap,
  listCapabilityMethods,
  policyFor,
  shapeKey,
  SURFACES,
} from "./grantMatrix.js";

// The grant matrix's UI half (grain/task-4401, grain/task-4428):
//   - every method and every argVariants set on the task pane;
//   - one method of each argument shape (plus CI-only methods on repo-ci),
//     across every argVariants set, on each remaining surface in SURFACES:
//     New task form, Template editor, Schedule editor, Console and Director
//     session pages, Settings → Tasks defaults, Repo default capabilities,
//     Repo CI capabilities, and the Access-request card's Grant button.
//
// Each surface test is independent (no serial skip across tests), so a
// failure on one surface never skips the others.

const OUT = process.env.GRANT_MATRIX_OUT;
const SHOTS = process.env.GRANT_MATRIX_SHOTS;
const TASK_KIND = SURFACES.find((s) => s.id === "task-pane");
const SHORT_LIST = 8;
// GRANT_MATRIX_ONLY=<regexp> narrows the walk to the methods it matches.
const ONLY = process.env.GRANT_MATRIX_ONLY
  ? new RegExp(process.env.GRANT_MATRIX_ONLY)
  : null;

async function methodsToWalk(request) {
  const all = await listCapabilityMethods(request);
  return ONLY ? all.filter((m) => ONLY.test(m.ref)) : all;
}

function shapeMethods(methods, { includeCIOnly = false } = {}) {
  const seen = new Set();
  return methods.filter((m) => {
    if (
      includeCIOnly &&
      (policyFor(m, TASK_KIND) === "ci_only" ||
        m.ref === "https://grain.dev/capabilities/repo#write")
    ) {
      seen.add(shapeKey(m));
      return true;
    }
    const key = shapeKey(m);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
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

function matchingRef(list, ref) {
  return (list || []).filter((c) => c === ref || c.startsWith(`${ref}=`));
}

async function heldOnTask(request, id, ref) {
  const task = await (await request.get(`/api/tasks/${id}`)).json();
  return matchingRef(task.capabilities, ref);
}

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

function fieldKindOf(s) {
  if (!s || typeof s !== "object") return "string";
  if (s.oneOf || s.anyOf) return "variant";
  if ("enum" in s || "const" in s) return "choice";
  const t = Array.isArray(s.type) ? s.type[0] : s.type;
  if (t === "boolean") return "boolean";
  if (t === "integer" || t === "number") return "number";
  if (t === "array") return "array";
  if (t === "object" || (t === undefined && s.properties)) return "object";
  return "string";
}

function isFixedList(root, s) {
  const items = deref(root, s?.items);
  return Boolean(
    s?.uniqueItems &&
    Array.isArray(items?.enum) &&
    s.minItems === items.enum.length &&
    s.maxItems === items.enum.length,
  );
}

function isStringMap(root, s) {
  if (!s || Object.keys(s.properties || {}).length > 0) return false;
  const ap = deref(root, s.additionalProperties);
  return Boolean(ap && typeof ap === "object" && ap.type === "string");
}

async function pickMuiSelect(page, section, selector, wantValue) {
  const hidden = section.locator(`input[${selector}]`).first();
  const current = await hidden.inputValue();
  if (current === wantValue) return;
  const combo = hidden.locator("xpath=..").locator('[role="combobox"]');
  // A menu left open by an earlier pick (seen once on the Template editor's
  // gcp#gcp-key permission_set) puts its invisible backdrop over the combo,
  // so a click to open it again never lands: pick from the open one instead.
  if ((await combo.getAttribute("aria-expanded")) !== "true") {
    await combo.click();
  }
  await page.locator(`[role="option"][data-value="${wantValue}"]`).click();
  await expect(combo).not.toHaveAttribute("aria-expanded", "true");
}

// fillField sets one schema field inside section (either a draft or a held
// grant's ArgsSection) so its value becomes want. Passing undefined resets an
// optional field to its unset/default state.
async function fillField(
  page,
  section,
  root,
  name,
  rawSchema,
  required,
  want,
  argPath,
) {
  const schema = deref(root, rawSchema) || {};
  const kind = fieldKindOf(schema);
  switch (kind) {
    case "choice": {
      if (typeof schema.const === "boolean") {
        const box = section.locator(`input[data-arg="${argPath}"]`).first();
        if (want === schema.const && !(await box.isChecked())) {
          await box.click();
        }
        return;
      }
      if ("const" in schema) return;
      const options = schema.enum || [];
      const isTop = !argPath.includes(".") && !argPath.includes("[");
      const effectiveWant =
        want === undefined && isTop && "default" in schema
          ? schema.default
          : want;
      const idx =
        effectiveWant === undefined
          ? -1
          : options.findIndex(
              (o) => JSON.stringify(o) === JSON.stringify(effectiveWant),
            );
      const wantVal = idx < 0 ? "" : String(idx);
      if (options.length > SHORT_LIST) {
        const box = section.locator(`[data-arg="${argPath}"]`).first();
        const enumLabels = schema["x-grain-enum-labels"] || {};
        const labelText =
          idx < 0
            ? "Not set"
            : typeof options[idx] === "string"
              ? enumLabels[options[idx]] || options[idx]
              : JSON.stringify(options[idx]);
        await chooseOption(box, wantVal === "" ? "Not set" : labelText);
        return;
      }
      await pickMuiSelect(page, section, `data-arg="${argPath}"`, wantVal);
      return;
    }
    case "boolean": {
      const sw = section.locator(`input[data-arg="${argPath}"]`).first();
      const desired =
        want === undefined ? Boolean(schema.default) : Boolean(want);
      if ((await sw.isChecked()) !== desired) {
        await sw.click();
      }
      return;
    }
    case "number": {
      const input = section.locator(`input[data-arg="${argPath}"]`).first();
      const desired =
        want === undefined
          ? schema.default !== undefined
            ? String(schema.default)
            : ""
          : String(want);
      if ((await input.inputValue()) !== desired) {
        await input.fill(desired);
      }
      return;
    }
    case "array": {
      if (required && isFixedList(root, schema)) {
        const group = section.locator(`[data-arg-fixed-list="${argPath}"]`);
        const useBtn = group.getByRole("button", { name: "Use these" });
        if ((await useBtn.count()) > 0) await useBtn.click();
        return;
      }
      const list = section.locator(`[data-arg-list="${argPath}"]`);
      const desired = Array.isArray(want)
        ? want
        : Array.isArray(schema.default)
          ? schema.default
          : [];
      const removeButtons = list.locator(
        `button[aria-label^="Remove ${name} "]`,
      );
      while ((await removeButtons.count()) > desired.length) {
        await removeButtons.last().click();
      }
      while ((await removeButtons.count()) < desired.length) {
        await list
          .getByRole("button", { name: `Add ${name}`, exact: true })
          .click();
      }
      const itemSchema = deref(root, schema.items) || { type: "string" };
      for (let i = 0; i < desired.length; i++) {
        await fillField(
          page,
          section,
          root,
          `${name} ${i + 1}`,
          itemSchema,
          true,
          desired[i],
          `${argPath}[${i}]`,
        );
      }
      return;
    }
    case "object": {
      if (isStringMap(root, schema)) {
        const mapBox = section.locator(`[data-arg-map="${argPath}"]`);
        const entries = Object.entries(want || {});
        const nameInputs = mapBox.locator(
          `input[data-arg-map-name^="${argPath}["]`,
        );
        while ((await nameInputs.count()) > entries.length) {
          await mapBox.locator('button[aria-label^="Remove "]').last().click();
        }
        while ((await nameInputs.count()) < entries.length) {
          await mapBox
            .getByRole("button", { name: "Add entry", exact: true })
            .click();
        }
        for (let i = 0; i < entries.length; i++) {
          const [k, v] = entries[i];
          const kIn = mapBox.locator(
            `input[data-arg-map-name="${argPath}[${i}]"]`,
          );
          const vIn = mapBox.locator(
            `input[data-arg-map-value="${argPath}[${i}]"]`,
          );
          if ((await kIn.inputValue()) !== k) await kIn.fill(k);
          if ((await vIn.inputValue()) !== String(v)) await vIn.fill(String(v));
        }
        return;
      }
      await fillObject(page, section, root, schema, want || {}, argPath);
      return;
    }
    case "variant": {
      const options = (schema.oneOf || schema.anyOf).map((s) => deref(root, s));
      let branchIdx = options.findIndex((o) => {
        const req = o?.required || [];
        return req.every((k) => want && k in want);
      });
      if (branchIdx < 0) branchIdx = 0;
      await pickMuiSelect(
        page,
        section,
        `data-arg-variant="${argPath}"`,
        String(branchIdx),
      );
      await fillField(
        page,
        section,
        root,
        options[branchIdx]?.title || name,
        options[branchIdx],
        required,
        want,
        argPath,
      );
      return;
    }
    case "string":
    default: {
      const input = section.locator(`[data-arg="${argPath}"]`).first();
      const desired =
        want === undefined
          ? schema.default !== undefined
            ? String(schema.default)
            : ""
          : String(want);
      if ((await input.inputValue()) !== desired) {
        await input.fill(desired);
      }
    }
  }
}

async function fillObject(page, section, root, schema, args, prefix = "") {
  const s = deref(root, schema) || {};
  const required = new Set(s.required || []);
  for (const [name, sub] of Object.entries(s.properties || {})) {
    const d = deref(root, sub) || {};
    if (isSecret(name, d)) continue;
    // Skip hidden const string properties if not drawn.
    const childPath = prefix ? `${prefix}.${name}` : name;
    await fillField(
      page,
      section,
      root,
      name,
      d,
      required.has(name),
      args ? args[name] : undefined,
      childPath,
    );
  }
}

async function fillVariantArgs(page, section, method, variantName, args) {
  const root = method.args;
  if (!root) return;
  const top = deref(root, root) || {};
  const branches = top.oneOf || top.anyOf;
  if (branches) {
    const m = variantName.match(/^(?:oneOf|anyOf)\[(\d+)\]/);
    const idx = m ? Number(m[1]) : 0;
    await pickMuiSelect(page, section, 'data-arg-variant=""', String(idx));
    const branch = deref(root, branches[idx]) || {};
    if (fieldKindOf(branch) === "object") {
      await fillObject(page, section, root, branch, args || {}, "");
    }
    return;
  }
  await fillObject(page, section, root, top, args || {}, "");
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

// logFailure prints a failing (not known-gap) cell's whole error and stack: the matrix
// records only the first line, which for an expect() names the matcher but
// not which of walkCell's several toHaveCount calls it was.
function logFailure(surface, ref, variant, err) {
  console.log(
    `grant matrix: ${surface.id} ${ref} [${variant}] error:\n${err?.stack || err}`,
  );
}

// walkCell walks every argVariants(method) set on one surface:
//   - picks the method from "Add grant…" to reach a held grant on its first
//     grantable variant (while checking any refused variants, such as
//     repo#write oneOf[0] on task surfaces, are refused as expected),
//   - edits the held grant in place through every remaining grantable variant
//     (nested objects, arrays, enums, booleans, numbers), saving and
//     reloading via onSaved on each one and verifying data-grant matches
//     POST /api/capability-grants/check's recorded string,
//   - verifies inline refusal on a pattern-checked field, and removes it.
async function walkCell(
  page,
  request,
  getGrants,
  method,
  surface,
  onSaved,
  { ciSelectChip = null, submitForRefusal = null } = {},
) {
  const ref = method.ref;
  const variants = argVariants(method);
  const outcomes = [];
  const policy = policyFor(method, surface);

  const clearLeftovers = async () => {
    if ((await page.locator("[data-open-popover]").count()) > 0) {
      await page.keyboard.press("Escape").catch(() => {});
    }
    const g = getGrants();
    const draftRemove = g
      .locator(`[data-method="${ref}"] [data-grant-draft]`)
      .getByRole("button", { name: /^Remove / });
    while ((await draftRemove.count()) > 0) {
      await draftRemove.first().click();
    }
    const heldRemove = heldLocator(g, ref).getByRole("button", {
      name: /^Remove /,
    });
    while ((await heldRemove.count()) > 0) {
      await heldRemove.first().click();
    }
  };

  // 1. Policy CI-only refusal on non-CI surfaces applies to every variant.
  if (policy === "ci_only") {
    const grants = getGrants();
    const item = await openMenu(page, grants, method);
    let detail = "listed disabled: CI-only";
    if ((await item.count()) === 0) {
      await page.keyboard.press("Escape");
      detail = "not offered: CI-only";
    } else {
      await expect(item).toHaveCount(1);
      await expect(item).toHaveAttribute("data-ci-only", "true");
      await expect(item).toHaveAttribute("aria-disabled", "true");
      await page.keyboard.press("Escape");
    }
    for (const { variant } of variants) {
      outcomes.push({ variant, outcome: "refused", detail });
    }
    return outcomes;
  }

  let currentGrants = getGrants();
  let isHeld = false;
  let inLegacyCISelect = false;
  let lastValidArgs = null;
  let lastRecorded = "";

  for (const { variant, args } of variants) {
    const refusal = expectedRefusal(ref, variant, surface);
    const gap = knownGap(ref, variant, surface.id);

    try {
      if (!isHeld) {
        const draftExisting = currentGrants.locator(
          `[data-method="${ref}"] [data-grant-draft]`,
        );
        if ((await draftExisting.count()) === 0) {
          const item = await openMenu(page, currentGrants, method);
          if ((await item.count()) === 0) {
            await page.keyboard.press("Escape");
            if (refusal) {
              outcomes.push({
                variant,
                outcome: "refused",
                detail: "not offered",
              });
              continue;
            }
            outcomes.push({
              variant,
              outcome: gap ? "known" : "fail",
              knownGap: gap || undefined,
              detail: "not offered in Add grant…",
            });
            continue;
          }
          await item.first().click();
          if (ref.startsWith(API_REF)) {
            await expect(item.first()).toHaveAttribute("aria-checked", "true");
            await page.keyboard.press("Escape");
            await expect(page.locator("[data-open-popover]")).toHaveCount(0);
          }
        }

        const draft = currentGrants.locator(
          `[data-method="${ref}"] [data-grant-draft]`,
        );
        if (refusal) {
          const hadDraft = (await draft.count()) > 0;
          if (hadDraft) {
            await fillVariantArgs(page, draft, method, variant, args);
          }
          // If check admitted the variant into local state (e.g. repo#write
          // {repo} on a local-state form after SAVE_DELAY_MS), wait for the
          // local grant line and submit the form to surface the refusal.
          if (submitForRefusal && hadDraft) {
            await expect(heldLocator(currentGrants, ref)).toHaveCount(1);
            await submitForRefusal();
          }
          await expect(page.getByText(refusal).first()).toBeVisible();
          await clearLeftovers();
          currentGrants = getGrants();
          outcomes.push({
            variant,
            outcome: "refused",
            detail: `refused: ${refusal}`,
          });
          continue;
        }

        const expectedCheck = await recordedFor(request, ref, args);
        if ((await draft.count()) > 0) {
          await expect(draft).toBeVisible();
          await fillVariantArgs(page, draft, method, variant, args);
        }

        // On repo-ci, release#* and github#sandbox-repo move into the CI
        // capabilities select above repo-ci-grants when granted (task 2167).
        if (ciSelectChip && (await ciSelectChip(expectedCheck.recorded))) {
          inLegacyCISelect = true;
          isHeld = true;
          lastRecorded = expectedCheck.recorded;
          lastValidArgs = args;
          currentGrants = await onSaved(expectedCheck.recorded);
          expect(await ciSelectChip(expectedCheck.recorded)).toBe(true);
          outcomes.push({
            variant,
            outcome: "pass",
            detail: "held in CI capabilities picker",
          });
          continue;
        }

        const line = heldLocator(currentGrants, ref);
        await expect(line).toHaveCount(1);
        await expect(draft).toHaveCount(0);
        // If initial pick granted defaults (e.g. empty variant or default enum)
        // and this variant specifies further fields, edit the held grant now.
        if (args && Object.keys(args).length > 0 && !method.requiresArgs) {
          await fillVariantArgs(page, line.first(), method, variant, args);
        }
        await expect(line.first()).toHaveAttribute(
          "data-grant",
          expectedCheck.recorded,
        );
        const stored = await line.first().getAttribute("data-grant");
        isHeld = true;
        lastRecorded = stored;
        lastValidArgs = args;
        currentGrants = await onSaved(stored);
        await expect(heldLocator(currentGrants, ref)).toHaveCount(1);
        outcomes.push({ variant, outcome: "pass", detail: "" });
      } else {
        // Already held: edit the held grant in place to the next variant!
        const section = heldLocator(currentGrants, ref).first();
        if (refusal) {
          await fillVariantArgs(page, section, method, variant, args);
          if (submitForRefusal) await submitForRefusal();
          await expect(page.getByText(refusal).first()).toBeVisible();
          // Restore last valid args so subsequent variants stay clean.
          await fillVariantArgs(
            page,
            section,
            method,
            "restore",
            lastValidArgs,
          );
          await expect(section).toHaveAttribute("data-grant", lastRecorded);
          outcomes.push({
            variant,
            outcome: "refused",
            detail: `refused on edit: ${refusal}`,
          });
          continue;
        }

        const expectedCheck = await recordedFor(request, ref, args);
        await fillVariantArgs(page, section, method, variant, args);
        try {
          await expect(section).toHaveAttribute(
            "data-grant",
            expectedCheck.recorded,
            { timeout: 3_000 },
          );
        } catch {
          // On auto-saving pages (e.g. ConsoleSessionPage), an earlier field's
          // debounced save can resolve mid-way through a multi-field edit and
          // overwrite a later field; re-applying the target values settles it.
          await fillVariantArgs(
            page,
            heldLocator(currentGrants, ref).first(),
            method,
            variant,
            args,
          );
          await expect(heldLocator(currentGrants, ref).first()).toHaveAttribute(
            "data-grant",
            expectedCheck.recorded,
          );
        }
        const stored = await heldLocator(currentGrants, ref)
          .first()
          .getAttribute("data-grant");
        lastRecorded = stored;
        lastValidArgs = args;
        currentGrants = await onSaved(stored);
        await expect(heldLocator(currentGrants, ref).first()).toHaveAttribute(
          "data-grant",
          stored,
        );
        outcomes.push({
          variant,
          outcome: "pass",
          detail: "edited held grant",
        });
      }
    } catch (err) {
      const msg = String(err.message || err).split("\n")[0];
      if (!gap) logFailure(surface, ref, variant, err);
      const shot = await shoot(page, `${surface.id}-${ref}-${variant}`);
      if (!isHeld) {
        await clearLeftovers().catch(() => {});
        currentGrants = getGrants();
      } else if ((await page.locator("[data-open-popover]").count()) > 0) {
        await page.keyboard.press("Escape").catch(() => {});
      }
      outcomes.push({
        variant,
        outcome: gap ? "known" : "fail",
        knownGap: gap || undefined,
        detail: msg,
        shot: shot || undefined,
      });
    }
  }

  // Inline pattern refusal + Remove on the held grant.
  if (isHeld) {
    try {
      if (inLegacyCISelect) {
        const select = page.getByLabel("CI capabilities");
        await select.click();
        await page
          .locator(`[role="option"][data-value="${lastRecorded}"]`)
          .click();
        await page.keyboard.press("Escape");
        expect(await ciSelectChip(lastRecorded)).toBe(false);
      } else {
        const schema = deref(method.args, method.args) || {};
        const patterned = Object.entries(schema.properties || {}).find(
          ([, s]) =>
            deref(method.args, s)?.pattern && !deref(method.args, s)?.enum,
        );
        if (patterned) {
          const section = heldLocator(currentGrants, ref).first();
          const field = section.locator(`[data-arg="${patterned[0]}"]`).first();
          if (await field.isVisible().catch(() => false)) {
            await field.fill("Not valid!");
            await expect(section.getByText(/must match/).first()).toBeVisible();
            const validVal = (lastValidArgs || {})[patterned[0]] || "";
            await field.fill(validVal);
            await expect(section).toHaveAttribute("data-grant", lastRecorded);
          }
        }
        await heldLocator(currentGrants, ref)
          .first()
          .getByRole("button", { name: /^Remove / })
          .first()
          .click();
        await expect(heldLocator(currentGrants, ref)).toHaveCount(0);
      }
    } catch (err) {
      const msg = String(err.message || err).split("\n")[0];
      const shot = await shoot(page, `${surface.id}-${ref}-remove`);
      const last = outcomes[outcomes.length - 1];
      const gap = knownGap(ref, last?.variant || "*", surface.id);
      if (!gap) logFailure(surface, ref, "remove", err);
      outcomes.push({
        variant: "remove",
        outcome: gap ? "known" : "fail",
        knownGap: gap || undefined,
        detail: `remove: ${msg}`,
        shot: shot || undefined,
      });
    }
  }

  return outcomes;
}

// Each Playwright worker runs its own isolated `grain demo` server and store
// (fixtures.js), so running the surface tests in parallel across workers
// keeps every surface independent (a failure in one never skips another) and
// finishes the whole matrix in a few minutes.
test.describe.configure({ mode: "parallel" });
test.skip(
  !process.env.GRANT_MATRIX_UI,
  "the UI grant matrix runs nightly; set GRANT_MATRIX_UI=1 to run it",
);
test.use({ actionTimeout: 5_000 });

const RUN_ID =
  process.env.GRANT_MATRIX_RUN_ID || String(process.ppid || process.pid);
const shardResults = new Map();

function flushOut(shardKey, list) {
  if (!OUT) return;
  shardResults.set(shardKey, list);
  const shardFile = `${OUT}.${RUN_ID}.${shardKey.replace(/[^A-Za-z0-9_-]+/g, "_")}.part`;
  fs.writeFileSync(shardFile, JSON.stringify(list));
  const dir = path.dirname(OUT);
  const prefix = `${path.basename(OUT)}.${RUN_ID}.`;
  const merged = [];
  for (const name of fs.readdirSync(dir).sort()) {
    if (!name.startsWith(prefix) || !name.endsWith(".part")) continue;
    try {
      merged.push(...JSON.parse(fs.readFileSync(path.join(dir, name), "utf8")));
    } catch {
      // Another worker is mid-write; its own flush writes the merged file next.
    }
  }
  const tmp = `${OUT}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify({ results: merged }, null, 2));
  fs.renameSync(tmp, OUT);
}

function recordOutcomes(
  method,
  shardKey,
  outcomes,
  failures,
  surfaceId = shardKey,
) {
  let bucket = shardResults.get(shardKey);
  if (!bucket) {
    bucket = [];
    shardResults.set(shardKey, bucket);
  }
  for (const o of outcomes) {
    const cell = {
      ref: method.ref,
      surface: surfaceId,
      variant: o.variant,
      outcome: o.outcome,
      ...(o.knownGap ? { knownGap: o.knownGap } : {}),
      ...(o.detail ? { detail: o.detail } : {}),
      ...(o.shot ? { shot: o.shot } : {}),
    };
    bucket.push(cell);
    console.log(
      `grant matrix: ${cell.surface} ${cell.ref} [${cell.variant}] ${cell.outcome} ${cell.detail || ""}`,
    );
    if (cell.outcome === "fail") {
      failures.push(`${method.ref} [${cell.variant}]: ${cell.detail}`);
    }
  }
  flushOut(shardKey, bucket);
}

test("every method and argument variant is given, edited and removed through the task pane's Add grant…", async ({
  page,
  request,
}) => {
  test.setTimeout(25 * 60_000);
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
    const outcomes = await walkCell(
      page,
      request,
      grantsOf,
      method,
      surface,
      async (stored) => {
        await expect
          .poll(() => heldOnTask(request, task.id, method.ref))
          .toEqual([stored]);
        await page.reload();
        await expect(grantsOf()).toBeVisible();
        return grantsOf();
      },
    );
    recordOutcomes(method, surface.id, outcomes, failures);
    await request.patch(`/api/tasks/${task.id}`, {
      data: { capabilities: [] },
    });
    if (outcomes.some((o) => o.outcome !== "pass")) {
      await page.goto(`/tasks/${task.id}`);
      await expect(grantsOf()).toBeVisible();
    }
  }
  expect(failures, failures.join("\n")).toEqual([]);
});

test("one method of each argument shape and all its variants are walked through the New task form", async ({
  page,
  request,
}) => {
  test.setTimeout(10 * 60_000);
  const shapes = shapeMethods(await methodsToWalk(request));
  const surface = SURFACES.find((s) => s.id === "new-task");
  const failures = [];
  for (const method of shapes) {
    await page.goto("/");
    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    const grantsOf = () => page.getByTestId("new-task-grants");
    const outcomes = await walkCell(
      page,
      request,
      grantsOf,
      method,
      surface,
      async () => grantsOf(),
      {
        submitForRefusal: async () => {
          const title = page.getByLabel("Title").first();
          if (!(await title.inputValue())) {
            await title.fill(`Matrix refusal ${Date.now()}`);
            await chooseOption(page.getByLabel(/^Repos/), "none");
          }
          await page.getByRole("button", { name: "Create task" }).click();
        },
      },
    );
    recordOutcomes(method, surface.id, outcomes, failures);
    await page.keyboard.press("Escape").catch(() => {});
  }
  expect(failures, failures.join("\n")).toEqual([]);
});

test("one method of each argument shape and all its variants are walked through the Template editor", async ({
  page,
  request,
}) => {
  test.setTimeout(10 * 60_000);
  const shapes = shapeMethods(await methodsToWalk(request));
  const surface = SURFACES.find((s) => s.id === "template");
  const tmplName = `matrix-tmpl-${Date.now()}`;
  const created = await request.post("/api/templates", {
    data: {
      name: tmplName,
      title: "Grant matrix template",
      capabilities: [],
    },
  });
  expect(created.ok(), await created.text()).toBe(true);
  const tmpl = await created.json();
  const url = `/processes/templates/${tmpl.id}`;
  const grantsOf = () => page.getByTestId("template-grants");
  const failures = [];

  try {
    for (const method of shapes) {
      await request.put(`/api/templates/${tmpl.id}`, {
        data: {
          name: tmplName,
          title: "Grant matrix template",
          capabilities: [],
        },
      });
      await page.goto(url);
      await expect(
        page.getByRole("heading", { name: "Edit template" }),
      ).toBeVisible();
      const outcomes = await walkCell(
        page,
        request,
        grantsOf,
        method,
        surface,
        async (stored) => {
          await page.getByRole("button", { name: "Save", exact: true }).click();
          await expect(
            page.getByRole("heading", { name: "Edit template" }),
          ).toHaveCount(0);
          const list = await (await request.get("/api/templates")).json();
          const row = list.find((t) => t.id === tmpl.id);
          expect(matchingRef(row?.capabilities, method.ref)).toEqual([stored]);
          await page.goto(url);
          await expect(grantsOf()).toBeVisible();
          return grantsOf();
        },
        {
          submitForRefusal: () =>
            page.getByRole("button", { name: "Save", exact: true }).click(),
        },
      );
      if (
        await page
          .getByRole("button", { name: "Save", exact: true })
          .isVisible()
          .catch(() => false)
      ) {
        await page.getByRole("button", { name: "Save", exact: true }).click();
      }
      recordOutcomes(method, surface.id, outcomes, failures);
    }
  } finally {
    await request.delete(`/api/templates/${tmpl.id}`);
  }
  expect(failures, failures.join("\n")).toEqual([]);
});

test("one method of each argument shape and all its variants are walked through the Schedule editor", async ({
  page,
  request,
}) => {
  test.setTimeout(10 * 60_000);
  const shapes = shapeMethods(await methodsToWalk(request));
  const surface = SURFACES.find((s) => s.id === "schedule");
  const schedTitle = `Grant matrix schedule ${Date.now()}`;
  const created = await request.post("/api/schedules", {
    data: {
      title: schedTitle,
      description: "Grant matrix schedule",
      repo: "acme/widgets",
      recurrence: { kind: "weekly", timeOfDay: "06:00", weekday: "monday" },
      capabilities: [],
    },
  });
  expect(created.ok(), await created.text()).toBe(true);
  const sched = await created.json();
  const url = `/processes/schedules/${sched.id}`;
  const grantsOf = () => page.getByTestId("schedule-grants");
  const failures = [];

  try {
    for (const method of shapes) {
      await request.put(`/api/schedules/${sched.id}`, {
        data: {
          title: schedTitle,
          description: "Grant matrix schedule",
          repo: "acme/widgets",
          recurrence: { kind: "weekly", timeOfDay: "06:00", weekday: "monday" },
          capabilities: [],
        },
      });
      await page.goto(url);
      await expect(
        page.getByRole("heading", { name: "Edit schedule" }),
      ).toBeVisible();
      const outcomes = await walkCell(
        page,
        request,
        grantsOf,
        method,
        surface,
        async (stored) => {
          await page.getByRole("button", { name: "Save", exact: true }).click();
          await expect(
            page.getByRole("heading", { name: "Edit schedule" }),
          ).toHaveCount(0);
          const list = await (await request.get("/api/schedules")).json();
          const row = list.find((s) => s.id === sched.id);
          expect(matchingRef(row?.capabilities, method.ref)).toEqual([stored]);
          await page.goto(url);
          await expect(grantsOf()).toBeVisible();
          return grantsOf();
        },
        {
          submitForRefusal: () =>
            page.getByRole("button", { name: "Save", exact: true }).click(),
        },
      );
      if (
        await page
          .getByRole("button", { name: "Save", exact: true })
          .isVisible()
          .catch(() => false)
      ) {
        await page.getByRole("button", { name: "Save", exact: true }).click();
      }
      recordOutcomes(method, surface.id, outcomes, failures);
    }
  } finally {
    await request.delete(`/api/schedules/${sched.id}`);
  }
  expect(failures, failures.join("\n")).toEqual([]);
});

async function openSessionGrants(page, sessionId) {
  await page.goto(`/session/${sessionId}`);
  const content = page.locator(`#${sessionId}-capabilities-content`);
  if (!(await content.isVisible().catch(() => false))) {
    await page.locator(`#${sessionId}-capabilities-header`).click();
  }
  const grants = page.getByTestId(`session-${sessionId}-grants`);
  await expect(grants).toBeVisible();
  return grants;
}

test("one method of each argument shape and all its variants are walked through a Console session page", async ({
  page,
  request,
}) => {
  test.setTimeout(10 * 60_000);
  const shapes = shapeMethods(await methodsToWalk(request));
  const surface = SURFACES.find((s) => s.id === "session");
  const createdRes = await request.post("/api/console-sessions", {
    data: { message: `Grant matrix console ${Date.now()}` },
  });
  expect(createdRes.ok(), await createdRes.text()).toBe(true);
  const createdBody = await createdRes.json();
  const sessionId =
    typeof createdBody.created === "string"
      ? createdBody.created
      : createdBody.created?.id || createdBody.id;
  expect(sessionId).toBeTruthy();
  const grantsOf = () => page.getByTestId(`session-${sessionId}-grants`);
  const failures = [];

  try {
    await request.put(`/api/console-sessions/${sessionId}`, {
      data: { capabilities: [] },
    });
    await openSessionGrants(page, sessionId);

    for (const method of shapes) {
      const outcomes = await walkCell(
        page,
        request,
        grantsOf,
        method,
        surface,
        async (stored) => {
          await expect
            .poll(async () => {
              const { sessions } = await (
                await request.get("/api/console-sessions")
              ).json();
              const row = sessions.find((s) => s.id === sessionId);
              return matchingRef(row?.capabilities, method.ref);
            })
            .toEqual([stored]);
          await openSessionGrants(page, sessionId);
          return grantsOf();
        },
      );
      recordOutcomes(method, surface.id, outcomes, failures);
      await request.put(`/api/console-sessions/${sessionId}`, {
        data: { capabilities: [] },
      });
      await openSessionGrants(page, sessionId);
    }
  } finally {
    await request.delete(`/api/console-sessions/${sessionId}`);
  }
  expect(failures, failures.join("\n")).toEqual([]);
});

test("one method of each argument shape and all its variants are walked through the Director session page", async ({
  page,
  request,
}) => {
  test.setTimeout(10 * 60_000);
  const shapes = shapeMethods(await methodsToWalk(request));
  const surface = {
    ...SURFACES.find((s) => s.id === "session"),
    id: "director",
  };
  const beforeList = (await (await request.get("/api/console-sessions")).json())
    .sessions;
  const directorBefore = beforeList.find((s) => s.id === "director");
  expect(directorBefore).toBeTruthy();
  const grantsOf = () => page.getByTestId("session-director-grants");
  const failures = [];

  try {
    await request.put("/api/console-sessions/director", {
      data: { capabilities: [] },
    });
    await openSessionGrants(page, "director");

    for (const method of shapes) {
      const outcomes = await walkCell(
        page,
        request,
        grantsOf,
        method,
        surface,
        async (stored) => {
          await expect
            .poll(async () => {
              const { sessions } = await (
                await request.get("/api/console-sessions")
              ).json();
              const row = sessions.find((s) => s.id === "director");
              return matchingRef(row?.capabilities, method.ref);
            })
            .toEqual([stored]);
          await openSessionGrants(page, "director");
          return grantsOf();
        },
      );
      recordOutcomes(method, "director", outcomes, failures);
      await request.put("/api/console-sessions/director", {
        data: { capabilities: [] },
      });
      await openSessionGrants(page, "director");
    }
  } finally {
    await request.put("/api/console-sessions/director", {
      data: { capabilities: directorBefore.capabilities || [] },
    });
  }
  expect(failures, failures.join("\n")).toEqual([]);
});

test("one method of each argument shape and all its variants are walked through Settings → Tasks defaults", async ({
  page,
  request,
}) => {
  test.setTimeout(10 * 60_000);
  const shapes = shapeMethods(await methodsToWalk(request));
  const surface = SURFACES.find((s) => s.id === "settings-defaults");
  const settingsBefore = await (await request.get("/api/settings")).json();
  const grantsOf = () => page.getByTestId("settings-default-grants");
  const failures = [];

  try {
    await request.put("/api/settings", {
      data: { defaultCapabilities: [] },
    });
    await page.goto("/settings/tasks");
    await expect(grantsOf()).toBeVisible();

    for (const method of shapes) {
      const outcomes = await walkCell(
        page,
        request,
        grantsOf,
        method,
        surface,
        async (stored) => {
          await expect
            .poll(async () => {
              const s = await (await request.get("/api/settings")).json();
              return matchingRef(s.defaultCapabilities, method.ref);
            })
            .toEqual([stored]);
          await page.reload();
          await expect(grantsOf()).toBeVisible();
          return grantsOf();
        },
      );
      recordOutcomes(method, surface.id, outcomes, failures);
      await request.put("/api/settings", {
        data: { defaultCapabilities: [] },
      });
      await page.goto("/settings/tasks");
      await expect(grantsOf()).toBeVisible();
    }
  } finally {
    await request.put("/api/settings", {
      data: {
        defaultCapabilities: settingsBefore.defaultCapabilities || [],
      },
    });
  }
  expect(failures, failures.join("\n")).toEqual([]);
});

test("one method of each argument shape and all its variants are walked through Repo default capabilities", async ({
  page,
  request,
}) => {
  test.setTimeout(10 * 60_000);
  const shapes = shapeMethods(await methodsToWalk(request));
  const surface = SURFACES.find((s) => s.id === "repo-defaults");
  const repoBefore = await (
    await request.get("/api/repos/acme/widgets/capabilities")
  ).json();
  const grantsOf = () => page.getByTestId("repo-default-grants");
  const failures = [];

  try {
    await request.put("/api/repos/acme/widgets/capabilities", {
      data: { defaultCapabilities: [] },
    });
    await page.goto("/repos/acme/widgets#agents");
    await expect(grantsOf()).toBeVisible();

    for (const method of shapes) {
      const outcomes = await walkCell(
        page,
        request,
        grantsOf,
        method,
        surface,
        async (stored) => {
          await expect
            .poll(async () => {
              const r = await (
                await request.get("/api/repos/acme/widgets/capabilities")
              ).json();
              return matchingRef(r.defaultCapabilities, method.ref);
            })
            .toEqual([stored]);
          await page.reload();
          await expect(grantsOf()).toBeVisible();
          return grantsOf();
        },
      );
      recordOutcomes(method, surface.id, outcomes, failures);
      await request.put("/api/repos/acme/widgets/capabilities", {
        data: { defaultCapabilities: [] },
      });
      await page.goto("/repos/acme/widgets#agents");
      await expect(grantsOf()).toBeVisible();
    }
  } finally {
    await request.put("/api/repos/acme/widgets/capabilities", {
      data: { defaultCapabilities: repoBefore.defaultCapabilities || [] },
    });
  }
  expect(failures, failures.join("\n")).toEqual([]);
});

test("every argument shape and every CI-only method is walked through Repo CI capabilities", async ({
  page,
  request,
}) => {
  test.setTimeout(10 * 60_000);
  const shapes = shapeMethods(await methodsToWalk(request), {
    includeCIOnly: true,
  });
  const surface = SURFACES.find((s) => s.id === "repo-ci");
  const repoBefore = await (
    await request.get("/api/repos/acme/widgets/capabilities")
  ).json();
  const grantsOf = () => page.getByTestId("repo-ci-grants");
  const ciSelectChip = async (recorded) => {
    const chip = page
      .getByLabel("CI capabilities")
      .locator("xpath=..")
      .locator(`[title="${recorded.replace(/"/g, '\\"')}"]`);
    try {
      await expect(chip).toHaveCount(1, { timeout: 1_500 });
      return true;
    } catch {
      return false;
    }
  };
  const failures = [];

  try {
    await request.put("/api/repos/acme/widgets/ci-capabilities", {
      data: { ciCapabilities: [] },
    });
    await page.goto("/repos/acme/widgets#ci");
    await expect(grantsOf()).toBeVisible();

    for (const method of shapes) {
      const outcomes = await walkCell(
        page,
        request,
        grantsOf,
        method,
        surface,
        async (stored) => {
          await expect
            .poll(async () => {
              const r = await (
                await request.get("/api/repos/acme/widgets/capabilities")
              ).json();
              return matchingRef(r.ciCapabilities, method.ref);
            })
            .toEqual([stored]);
          await page.reload();
          await expect(grantsOf()).toBeVisible();
          return grantsOf();
        },
        { ciSelectChip },
      );
      // Every CI-only method must have been granted ("pass"), never refused!
      if (policyFor(method, TASK_KIND) === "ci_only") {
        for (const o of outcomes) {
          if (o.outcome !== "pass") {
            o.outcome = "fail";
            o.detail = `CI-only method was not granted on repo-ci: ${o.detail || o.outcome}`;
          }
        }
      }
      recordOutcomes(method, surface.id, outcomes, failures);
      await request.put("/api/repos/acme/widgets/ci-capabilities", {
        data: { ciCapabilities: [] },
      });
      await page.goto("/repos/acme/widgets#ci");
      await expect(grantsOf()).toBeVisible();
    }
  } finally {
    await request.put("/api/repos/acme/widgets/ci-capabilities", {
      data: { ciCapabilities: repoBefore.ciCapabilities || [] },
    });
  }
  expect(failures, failures.join("\n")).toEqual([]);
});

test("one method of each argument shape and all its variants are walked through the Access-request card's Grant button", async ({
  page,
  request,
}) => {
  test.setTimeout(10 * 60_000);
  const shapes = shapeMethods(await methodsToWalk(request));
  const surface = SURFACES.find((s) => s.id === "access-card");
  const created = await request.post("/api/tasks", {
    data: {
      title: `E2E grant matrix access card ${Date.now()}`,
      approved: true,
      noRepo: true,
      capabilities: [],
    },
  });
  expect(created.ok(), await created.text()).toBe(true);
  const task = await created.json();
  const grantsOf = () => page.getByTestId(`task-${task.id}-grants`);
  const cardOf = () =>
    page.getByRole("region", {
      name: "Access request waiting for your decision",
    });
  const failures = [];

  for (const method of shapes) {
    const ref = method.ref;
    const policy = policyFor(method, surface);
    const outcomes = [];

    for (const { variant, args } of argVariants(method)) {
      const refusal = expectedRefusal(ref, variant, surface);
      const gap = knownGap(ref, variant, surface.id);

      await request.patch(`/api/tasks/${task.id}`, {
        data: { capabilities: [] },
      });
      const park = await request.post(
        `/api/e2e/tasks/${task.id}/pending-access`,
        {
          data: {
            summary: `Grant ${ref} (${variant})`,
            reason: "Grant matrix access-card walkthrough",
            items: [
              args
                ? { kind: "grant", capability: ref, with: args }
                : { kind: "grant", capability: ref },
            ],
          },
        },
      );
      expect(park.ok(), await park.text()).toBe(true);

      try {
        await page.goto(`/tasks/${task.id}`);
        const card = cardOf();
        await expect(card).toBeVisible();

        if (policy === "ci_only" || refusal) {
          await expect(card.getByTestId("access-item-error")).toBeVisible();
          if (refusal) {
            await expect(card.getByTestId("access-item-error")).toContainText(
              refusal,
            );
          }
          await expect(
            card.getByRole("button", { name: /^Grant/ }),
          ).toBeDisabled();
          // Deny so the request clears cleanly before the next variant.
          await card.getByRole("button", { name: "Deny", exact: true }).click();
          await card.getByLabel(/^Why\?/).fill("Refused as expected.");
          await card
            .getByRole("button", { name: "Deny request", exact: true })
            .click();
          await expect(card).toHaveCount(0);
          outcomes.push({
            variant,
            outcome: "refused",
            detail: "access-card item disabled with error",
          });
          continue;
        }

        const expectedCheck = await recordedFor(request, ref, args);
        const grantBtn = card.getByRole("button", {
          name: "Grant all",
          exact: true,
        });
        await expect(grantBtn).toBeEnabled();
        await grantBtn.click();
        await expect(card).toHaveCount(0);

        await expect
          .poll(() => heldOnTask(request, task.id, ref))
          .toEqual([expectedCheck.recorded]);
        await page.reload();
        await expect(grantsOf()).toBeVisible();
        await expect(heldLocator(grantsOf(), ref)).toHaveCount(1);

        await heldLocator(grantsOf(), ref)
          .first()
          .getByRole("button", { name: /^Remove / })
          .first()
          .click();
        await expect(heldLocator(grantsOf(), ref)).toHaveCount(0);
        outcomes.push({ variant, outcome: "pass", detail: "" });
      } catch (err) {
        const msg = String(err.message || err).split("\n")[0];
        const shot = await shoot(page, `${surface.id}-${ref}-${variant}`);
        outcomes.push({
          variant,
          outcome: gap ? "known" : "fail",
          knownGap: gap || undefined,
          detail: msg,
          shot: shot || undefined,
        });
      }
    }

    recordOutcomes(method, surface.id, outcomes, failures);
  }
  expect(failures, failures.join("\n")).toEqual([]);
});
