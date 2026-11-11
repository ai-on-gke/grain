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
import { expect, test } from "./fixtures.js";
import {
  argVariants,
  expectedRefusal,
  grantString,
  invalidArgs,
  knownAPIGap,
  listCapabilityMethods,
  policyFor,
  SURFACES,
} from "./grantMatrix.js";

// The grant matrix's API half (grain/task-4401): every method GET
// /api/capability-methods lists, every argument variant its schema
// admits (grantMatrix.js), given to a task and taken away again through
// each write the UI's surfaces make -- POST /api/tasks/{id}/capabilities,
// as the task pane's chips do, and PATCH /api/tasks/{id} capabilities, as
// a form saving a whole list does -- and checked by POST
// /api/capability-grants/check, as every Add grant… form checks before it
// saves. The three must agree cell by cell: what check records is what
// the task holds, once, and what check refuses is refused by both writes
// with the field it is about.
//
// No browser: this is the half that runs on every pull request. The UI
// half, grant-matrix-ui.spec.js, drives the same matrix through the forms.
// GRANT_MATRIX_OUT=<file> writes every cell's outcome as JSON, which is
// what docs/test-runs/*-grant-ui-matrix.md's table is made from.

const TASK_KIND = SURFACES.find((s) => s.id === "task-pane");

async function heldOf(request, id, ref) {
  const res = await request.get(`/api/tasks/${id}`);
  const task = await res.json();
  return (task.capabilities || []).filter(
    (c) => c === ref || c.startsWith(`${ref}=`),
  );
}

async function check(request, ref, args) {
  const res = await request.post("/api/capability-grants/check", {
    data: {
      grants: [args ? { capability: ref, with: args } : { capability: ref }],
    },
  });
  expect(res.ok(), await res.text()).toBeTruthy();
  return (await res.json())[0];
}

function attachBody(ref, args, attach) {
  return args
    ? { capability: ref, with: args, attach }
    : { capability: ref, attach };
}

test("every method and argument variant is granted, round-trips and is removed through the API", async ({
  request,
}) => {
  test.setTimeout(10 * 60_000);
  const methods = await listCapabilityMethods(request);
  expect(methods.length).toBeGreaterThan(0);
  const results = [];
  const failures = [];
  const fail = (cell, what) => {
    cell.detail = what;
    if (cell.knownGap) {
      cell.outcome = "known";
      return;
    }
    cell.outcome = "fail";
    failures.push(`${cell.ref} [${cell.variant}] ${cell.route}: ${what}`);
  };

  const created = await request.post("/api/tasks", {
    data: {
      title: `E2E grant matrix ${Date.now()}`,
      approved: true,
      noRepo: true,
      capabilities: [],
    },
  });
  expect(created.ok(), await created.text()).toBeTruthy();
  const task = await created.json();
  const patch = (capabilities) =>
    request.patch(`/api/tasks/${task.id}`, { data: { capabilities } });
  const base =
    (await (await request.get(`/api/tasks/${task.id}`)).json()).capabilities ||
    [];
  expect(base).toEqual([]);

  for (const method of methods) {
    const ref = method.ref;
    for (const { variant, args } of argVariants(method)) {
      const refusal = expectedRefusal(ref, variant);
      const policy = policyFor(method, TASK_KIND) || (refusal ? "refused" : "");
      const checked = await check(request, ref, args);
      const cells = ["check", "attach", "patch"].map((route) => ({
        ref,
        variant,
        args,
        route,
        policy,
        knownGap: knownAPIGap(ref, variant),
        outcome: "pass",
      }));
      results.push(...cells);
      const [cCheck, cAttach, cPatch] = cells;
      // A grant grain refuses on every task may be refused by check
      // too, as long as it says the same thing the writes do: then the
      // form and the save agree.
      const checkRefused = Boolean(checked.error || !checked.recorded);
      if (checkRefused && refusal && refusal.test(checked.error || "")) {
        cCheck.outcome = "refused";
      } else if (checkRefused) {
        fail(cCheck, `check refused a valid set: ${checked.error}`);
        continue;
      }
      const recorded = checked.recorded || "";
      if (!checkRefused && !recorded.startsWith(ref))
        fail(cCheck, `recorded ${recorded}`);

      // Attach, as a chip does.
      let res = await request.post(`/api/tasks/${task.id}/capabilities`, {
        data: attachBody(ref, args, true),
      });
      if (policy) {
        // A CI-only or denied method is refused on a task, saying why.
        const says = refusal || (policy === "ci_only" ? /\bCI\b/ : /./);
        const body = await res.text();
        if (res.ok()) fail(cAttach, `granted a ${policy} method to a task`);
        else if (!says.test(body))
          fail(cAttach, `refusal does not say ${says}: ${body}`);
        res = await patch([...base, grantString(ref, args)]);
        const pbody = await res.text();
        if (res.ok()) fail(cPatch, `granted a ${policy} method to a task`);
        else if (!says.test(pbody))
          fail(cPatch, `refusal does not say ${says}: ${pbody}`);
        for (const c of [cAttach, cPatch])
          if (c.outcome === "pass") c.outcome = "refused";
        if (res.ok()) await patch(base);
        continue;
      }
      if (!res.ok()) {
        fail(cAttach, `attach: ${res.status()} ${await res.text()}`);
      } else {
        // Attaching it twice leaves one: equal grants are one grant.
        await request.post(`/api/tasks/${task.id}/capabilities`, {
          data: attachBody(ref, args, true),
        });
        const held = await heldOf(request, task.id, ref);
        if (held.length !== 1 || held[0] !== recorded)
          fail(
            cAttach,
            `held ${JSON.stringify(held)}, check recorded ${recorded}`,
          );
        res = await request.post(`/api/tasks/${task.id}/capabilities`, {
          data: { grant: held[0] || recorded, attach: false },
        });
        if (!res.ok())
          fail(cAttach, `detach: ${res.status()} ${await res.text()}`);
        else if ((await heldOf(request, task.id, ref)).length !== 0)
          fail(cAttach, "still held after removing it");
      }

      // PATCH the whole list, as a form saving its grants does.
      res = await patch([
        ...base,
        args ? { capability: ref, with: args } : ref,
      ]);
      if (!res.ok()) {
        fail(cPatch, `PATCH: ${res.status()} ${await res.text()}`);
        continue;
      }
      const held = await heldOf(request, task.id, ref);
      if (held.length !== 1 || held[0] !== recorded)
        fail(
          cPatch,
          `held ${JSON.stringify(held)}, check recorded ${recorded}`,
        );
      res = await patch(base);
      if (!res.ok()) fail(cPatch, `PATCH back: ${res.status()}`);
      else if ((await heldOf(request, task.id, ref)).length !== 0)
        fail(cPatch, "still held after PATCHing it away");
    }

    // An invalid set: refused by check and by both writes, naming the
    // field (or, for a schema of alternatives, the arguments as a whole).
    const bad = invalidArgs(method);
    if (!bad || policyFor(method, TASK_KIND) || expectedRefusal(ref, "invalid"))
      continue;
    const cell = {
      ref,
      variant: "invalid",
      args: bad.args,
      route: "check+attach",
      policy: "",
      knownGap: knownAPIGap(ref, "invalid"),
      outcome: "pass",
    };
    results.push(cell);
    const refused = await check(request, ref, bad.args);
    const named =
      (refused.fields && bad.field in refused.fields) ||
      (bad.field && (refused.error || "").includes(bad.field));
    if (!refused.error) fail(cell, `check passed ${JSON.stringify(bad.args)}`);
    else if (!named)
      fail(
        cell,
        `check refused without naming "${bad.field}": ${refused.error} ${JSON.stringify(refused.fields)}`,
      );
    const res = await request.post(`/api/tasks/${task.id}/capabilities`, {
      data: attachBody(ref, bad.args, true),
    });
    if (res.status() !== 400)
      fail(cell, `attach of an invalid set answered ${res.status()}`);
    else if (bad.field && !(await res.text()).includes(bad.field))
      fail(cell, "attach refusal does not name the field");
  }

  if (process.env.GRANT_MATRIX_OUT)
    fs.writeFileSync(
      process.env.GRANT_MATRIX_OUT,
      JSON.stringify({ methods: methods.length, results }, null, 2),
    );
  expect(failures, failures.join("\n")).toEqual([]);
});
