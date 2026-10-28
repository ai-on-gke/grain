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

import { test as base, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pageErrors } from "./pageerrors.js";
import { signIn, writeAccessCode } from "./accesscode.js";

// One `grain demo` per Playwright worker, on a port of its own, in place
// of the single shared server playwright.config.js's `webServer` used to
// start for the whole suite (grain/task-292).
//
// # What the shared one cost
//
// `fullyParallel: false` keeps the tests *within* a file serial, and
// nothing keeps two files apart: Playwright runs as many files at once
// as it has workers, which is half the machine's cores by default, and
// every one of them was driving one store seeded once. The specs here
// are not written for that. They assert on rows `grain demo` seeded --
// the backlog's order, how many tasks match a filter, which task is
// waiting on an answer -- and several of them reorder or edit exactly
// those rows. Two files doing that at the same time fail each other, and
// the failure reads as a bug in the pane under test rather than as
// another spec's write: grain/task-236 reproduced it as one copy of
// "moves a task to the top of the backlog" asserting on a backlog
// another copy had just reordered.
//
// A store per worker ends that whole class. What a worker sees is a
// freshly seeded store, then its own files run over it one after
// another -- which is an ordering the suite already had to survive when
// everything shared one server, minus every interleaving that only
// concurrency could produce.
//
// # What it does not fix
//
// `--repeat-each` is still not usable, and this is not a step towards
// it. A worker starts one server and keeps it for every test it is
// given, so the second repeat of a file inherits the first repeat's
// store however the repeats are spread across workers -- which is the
// deterministic failure e2e/inbox.spec.js documents, where answering
// the seeded parked question spends a fixture nothing can put back. The
// Makefile's test-e2e-long repeats the suite by running it three times
// over three fresh stores for that reason, and still should.
//
// # Cost
//
// One `grain demo` process per worker rather than one per run: a
// process, an SQLite store in a temp directory, and the seeding, all of
// which take well under a second now that global-setup.js has already
// built the binary. Everything expensive here is still the browsers.

// READY_MS is how long a worker waits for its server to say it is
// listening. Generous, because a cold CI runner starting several of
// these at once while also unpacking browsers is the slow case, and a
// short ceiling here would turn that into a flake of its own -- a
// passing run never spends it. `webServer` allowed 60s for the same
// wait, and had a `go run` compile inside its budget where this has
// only an exec.
const READY_MS = 60_000;

// accessCodeArgs gives the demo the run's access code (accesscode.js):
// every caller, loopback included, shows that code, and nobody is named
// in X-Grain-User, as on every grain daemon (docs/access-code.md). A
// spec that passes -access-code-file itself (access-code-walkthrough.spec.js)
// keeps its own file and code. Resolves to the arguments and the file the
// code is in.
async function accessCodeArgs(tmpDir, demoArgs) {
  const at = demoArgs.indexOf("-access-code-file");
  if (at >= 0) return { args: demoArgs, codeFile: demoArgs[at + 1] };
  const codeFile = path.join(tmpDir, "access-code");
  await writeAccessCode(codeFile);
  return { args: [...demoArgs, "-access-code-file", codeFile], codeFile };
}

// startDemoServer runs the binary global-setup.js built and resolves to
// the URL it says it is listening on, and the access code it serves.
async function startDemoServer(tmpDir, demoArgs) {
  const { args, codeFile } = await accessCodeArgs(tmpDir, demoArgs);
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  if (!bin) {
    throw new Error(
      "GRAIN_E2E_DEMO_BIN is unset: e2e/global-setup.js builds `grain` and " +
        "puts its path there, so this suite has to run through " +
        "playwright.config.js rather than with its globalSetup overridden.",
    );
  }
  // -addr 127.0.0.1:0 asks the kernel for a free port and reads back
  // which one it gave, rather than picking one and hoping. With a server
  // per worker there is no fixed port that could be right anyway, and
  // guessing would put the suite back in the business of colliding --
  // with its own other workers, with a second copy of the suite, or with
  // whatever a developer already has listening.
  //
  // TMPDIR is the worker's own directory so that the throwaway SQLite
  // store `grain demo` creates for itself (os.MkdirTemp, in demo.go)
  // lands somewhere this fixture can delete when the worker is done.
  const proc = spawn(
    bin,
    ["demo", "-addr", "127.0.0.1:0", "-open=false", ...args],
    {
      stdio: ["ignore", "pipe", "pipe"],
      env: { ...process.env, TMPDIR: tmpDir },
    },
  );

  let output = "";
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      reject(
        new Error(
          `grain demo did not start within ${READY_MS}ms. Output:\n${output}`,
        ),
      );
    }, READY_MS);
    const done = (fn, arg) => {
      clearTimeout(timer);
      fn(arg);
    };
    const read = (chunk) => {
      output += chunk;
      // demo.go logs "... at http://127.0.0.1:PORT -- nothing here is
      // real" once its listener is open, which is the only place the
      // port it was given actually exists. A file whose demoArgs move it
      // onto every interface (-addr 0.0.0.0:0, which Go reports as
      // "[::]") is still reached over loopback here, which it also
      // answers on.
      const at = output.match(
        /at http:\/\/(127\.0\.0\.1|0\.0\.0\.0|\[::\]):(\d+)/,
      );
      if (at) done(resolve, `http://127.0.0.1:${at[2]}`);
    };
    proc.stdout.on("data", read);
    proc.stderr.on("data", read);
    proc.on("error", (err) => done(reject, err));
    proc.on("exit", (code, signal) =>
      done(
        reject,
        new Error(
          `grain demo exited (code ${code}, signal ${signal}) before it was ` +
            `listening. Output:\n${output}`,
        ),
      ),
    );
  });

  // The listener is open by the time that line is printed, so this is
  // not really a wait -- it is the same check `webServer` made before
  // any test ran, kept because a server that is listening and cannot
  // answer should fail here, once, rather than in whichever test drew
  // the worker first. The demo is single-tenant (see accessCodeArgs), so
  // it is asked the way the grain CLI asks a daemon: with its access code
  // as a bearer token.
  const code = (await fs.readFile(codeFile, "utf8")).trim();
  const res = await fetch(`${url}/api/config`, {
    headers: { Authorization: `Bearer ${code}` },
  });
  if (!res.ok) {
    proc.kill("SIGKILL");
    throw new Error(`GET ${url}/api/config: ${res.status}`);
  }

  return { url, proc, code };
}

export const test = base.extend({
  ...pageErrors,
  // An opt-in executable scenario for specs that need more than the seeded
  // store. It is worker-scoped so every file assigned to this worker sees the
  // same server shape, just as it sees the same store.
  demoArgs: [[], { option: true, scope: "worker" }],
  // Worker-scoped: started once for the worker, torn down when
  // Playwright retires it, and shared by every test that worker runs.
  demo: [
    async ({ demoArgs }, use) => {
      const tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "grain-e2e-run-"));
      const { url, proc, code } = await startDemoServer(tmpDir, demoArgs);
      // Nothing here handles a signal, so SIGKILL rather than a polite
      // SIGTERM the process would only be killed for failing to answer.
      // The store is a temp directory nobody else has the path to.
      const stopped = new Promise((resolve) => proc.on("exit", resolve));
      try {
        await use({ url, code });
      } finally {
        proc.kill("SIGKILL");
        await stopped;
        await fs.rm(tmpDir, { recursive: true, force: true });
      }
    },
    { scope: "worker" },
  ],
  // The worker's server's URL.
  demoServer: [
    async ({ demo }, use) => {
      await use(demo.url);
    },
    { scope: "worker" },
  ],
  // The access code the worker's server serves, for a spec that has to
  // present it itself (a bearer token to a CLI, say).
  accessCode: [
    async ({ demo }, use) => {
      await use(demo.code);
    },
    { scope: "worker" },
  ],
  // Whether the tests start signed in with the access code. Only a spec
  // whose subject is signing in (access-code-walkthrough.spec.js) turns
  // it off.
  signedIn: [true, { option: true, scope: "worker" }],
  // accessSession signs in once per worker, as a person does on the
  // "Enter access code" page, and keeps the session cookie it is given.
  accessSession: [
    async ({ demo }, use) => {
      await use(await signIn(demo.url, demo.code));
    },
    { scope: "worker" },
  ],
  // Overrides Playwright's own `storageState` option, which seeds every
  // browser context and API request context a test opens -- `page`,
  // `request`, and the ones a spec makes with `browser.newContext()` or
  // `playwright.request.newContext()` -- so all of them carry the access
  // session. A cookie rather than an `Authorization` header because
  // specs replace `extraHTTPHeaders` (`test.use({ extraHTTPHeaders: {}
  // })`), and because the browser itself signs in that way.
  storageState: async ({ signedIn, accessSession }, use) => {
    await use(signedIn ? accessSession : undefined);
  },
  // Overrides Playwright's own `baseURL` option, which is what makes
  // `page.goto("/inbox")` and `page.request.get("/api/tasks")` in the
  // specs address this worker's server without any of them naming it.
  baseURL: async ({ demoServer }, use) => {
    await use(demoServer);
  },
});

export { expect };
