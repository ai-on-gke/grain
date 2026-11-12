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
  isPersonGrantedArg,
  knownAPIGap,
  listCapabilityMethods,
  policyFor,
  refusedOnlyForTasks,
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

async function check(request, ref, args, subject = null) {
  const grants = [args ? { capability: ref, with: args } : { capability: ref }];
  const res = await request.post("/api/capability-grants/check", {
    data: subject ? { grants, subject } : { grants },
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
      // A grant grain refuses on every task must be refused by check
      // too, with the same reason the writes give: then the form and the
      // save agree. One the repos page's CI capabilities list grants is
      // refused only when the check names a task.
      const checkRefused = Boolean(checked.error || !checked.recorded);
      if (refusal && !checkRefused && refusedOnlyForTasks(ref, variant)) {
        const forTask = await check(request, ref, args, { repo: "" });
        if (!refusal.test(forTask.catalogueRefusal || ""))
          fail(
            cCheck,
            `check for a task does not say ${refusal}: ${forTask.catalogueRefusal}`,
          );
        else cCheck.outcome = "refused";
      } else if (refusal) {
        if (!checkRefused)
          fail(
            cCheck,
            `check passed ${JSON.stringify(args)}: ${checked.recorded}`,
          );
        else if (!refusal.test(checked.error || ""))
          fail(
            cCheck,
            `check refusal does not say ${refusal}: ${checked.error}`,
          );
        else cCheck.outcome = "refused";
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

test("person-only arguments (x-grain-person-granted) are refused when an agent token attempts them", async ({
  request,
  playwright,
  baseURL,
}) => {
  const methods = await listCapabilityMethods(request);
  const personOnly = [];
  for (const method of methods) {
    for (const { variant, args } of argVariants(method)) {
      if (isPersonGrantedArg(method, args)) {
        personOnly.push({ method, ref: method.ref, variant, args });
      }
    }
  }
  expect(
    personOnly.some(
      (c) =>
        c.ref === "https://grain.dev/capabilities/gcp#gcp-key" &&
        c.args?.permission_set === "org-deploy",
    ),
    `expected gcp#gcp-key {permission_set: "org-deploy"} in x-grain-person-granted cells, got ${JSON.stringify(personOnly.map((c) => [c.ref, c.variant]))}`,
  ).toBe(true);

  const settingsBefore = await (await request.get("/api/settings")).json();
  let tokenID = "";
  let extReq = null;
  try {
    expect(
      (
        await request.put("/api/settings", {
          data: { externalAgentApi: true },
        })
      ).ok(),
    ).toBe(true);

    const issue = await request.post("/api/external-agents/tokens", {
      data: {
        name: `matrix-agent-${Date.now()}`,
        scopes: ["tasks:write", "tasks:admin", "capabilities:admin"],
      },
    });
    expect(issue.ok(), await issue.text()).toBe(true);
    const issued = await issue.json();
    tokenID = issued.id;

    extReq = await playwright.request.newContext({
      baseURL,
      storageState: { cookies: [], origins: [] },
      extraHTTPHeaders: {
        Authorization: `Bearer ${issued.token}`,
        "X-Grain-Acting-Reason": "grant matrix person-only check",
      },
    });

    const personGrants = [];
    for (const c of personOnly) {
      const checked = await check(request, c.ref, c.args);
      expect(checked.error || "").toBe("");
      personGrants.push(checked.recorded);
    }
    const allowedGCP =
      'https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent"}';

    // A person creates an acting task that ALREADY holds every person-only
    // grant plus capabilities:admin and tasks:write, and a target task to
    // attach grants to. Even holding the grant itself must not let an agent
    // pass a person-only argument on.
    const actingRes = await request.post("/api/tasks", {
      data: {
        title: `E2E matrix acting agent ${Date.now()}`,
        noRepo: true,
        capabilities: [
          "api:tasks:write",
          "api:tasks:admin",
          "api:capabilities:admin",
          allowedGCP,
          ...personGrants,
        ],
      },
    });
    expect(actingRes.ok(), await actingRes.text()).toBe(true);
    const acting = await actingRes.json();

    const targetRes = await request.post("/api/tasks", {
      data: {
        title: `E2E matrix target ${Date.now()}`,
        noRepo: true,
        capabilities: [],
      },
    });
    expect(targetRes.ok(), await targetRes.text()).toBe(true);
    const target = await targetRes.json();

    const actingHeaders = {
      "X-Grain-Acting-Task": String(acting.id),
      "X-Grain-Acting-Reason": "grant matrix person-only check",
    };

    // Positive control: the acting task CAN attach the non-person-granted
    // gcp#gcp-key {permission_set: sandbox-agent} variant it holds.
    const controlRes = await request.post(
      `/api/tasks/${target.id}/capabilities`,
      {
        headers: actingHeaders,
        data: attachBody(
          "https://grain.dev/capabilities/gcp#gcp-key",
          { permission_set: "sandbox-agent" },
          true,
        ),
      },
    );
    expect(controlRes.ok(), await controlRes.text()).toBe(true);
    await request.patch(`/api/tasks/${target.id}`, {
      data: { capabilities: [] },
    });

    for (const { ref, variant, args } of personOnly) {
      const spec = grantString(ref, args);

      // 1. Acting-task agent token (X-Grain-Acting-Task) on attach, patch, create.
      for (const [label, call] of [
        [
          "acting attach",
          () =>
            request.post(`/api/tasks/${target.id}/capabilities`, {
              headers: actingHeaders,
              data: attachBody(ref, args, true),
            }),
        ],
        [
          "acting patch",
          () =>
            request.patch(`/api/tasks/${target.id}`, {
              headers: actingHeaders,
              data: { capabilities: [spec] },
            }),
        ],
        [
          "acting create",
          () =>
            request.post("/api/tasks", {
              headers: actingHeaders,
              data: {
                title: `Refused ${variant}`,
                noRepo: true,
                capabilities: [spec],
              },
            }),
        ],
      ]) {
        const res = await call();
        const body = await res.text();
        expect(res.status(), `${ref} [${variant}] ${label}: ${body}`).toBe(403);
        expect(body, `${ref} [${variant}] ${label}`).toMatch(
          /only a person may grant/i,
        );
      }

      // 2. External agent bearer token on attach, patch, create, and /mcp.
      for (const [label, call] of [
        [
          "external attach",
          () =>
            extReq.post(`/api/tasks/${target.id}/capabilities`, {
              data: attachBody(ref, args, true),
            }),
        ],
        [
          "external patch",
          () =>
            extReq.patch(`/api/tasks/${target.id}`, {
              data: { capabilities: [spec] },
            }),
        ],
        [
          "external create",
          () =>
            extReq.post("/api/tasks", {
              data: {
                title: `Refused external ${variant}`,
                noRepo: true,
                capabilities: [spec],
              },
            }),
        ],
      ]) {
        const res = await call();
        const body = await res.text();
        expect(res.status(), `${ref} [${variant}] ${label}: ${body}`).toBe(403);
        expect(body, `${ref} [${variant}] ${label}`).toMatch(
          /only a person may grant/i,
        );
      }

      const mcpRes = await extReq.post("/mcp", {
        data: {
          jsonrpc: "2.0",
          id: 1,
          method: "tools/call",
          params: {
            name: "api_create_task",
            arguments: {
              body: {
                title: `Refused mcp ${variant}`,
                body: "x",
                noRepo: true,
                capabilities: [spec],
              },
            },
          },
        },
      });
      const mcpText = await mcpRes.text();
      if (mcpRes.ok()) {
        const rpc = JSON.parse(mcpText);
        expect(
          Boolean(rpc.error || rpc.result?.isError),
          `${ref} [${variant}] mcp allowed: ${mcpText}`,
        ).toBe(true);
      }
      expect(mcpText, `${ref} [${variant}] mcp`).toMatch(
        /only a person may grant/i,
      );

      expect(await heldOf(request, target.id, ref)).toEqual([]);
    }
  } finally {
    if (extReq) await extReq.dispose();
    if (tokenID) await request.delete(`/api/external-agents/tokens/${tokenID}`);
    await request.put("/api/settings", {
      data: { externalAgentApi: Boolean(settingsBefore.externalAgentApi) },
    });
  }
});
