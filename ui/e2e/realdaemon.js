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

import { expect } from "@playwright/test";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { writeAccessCode } from "./accesscode.js";

// A real `grain daemon` for the specs that need one rather than the
// per-worker `grain demo` e2e/fixtures.js starts: config.spec.js and
// host-sandboxes-alert.spec.js. It runs over its own data directory, with a
// local e2e sandbox backend (cmd/grain/local_sandboxes_e2e.go). The model
// provider (agy) is the stubbed boundary -- the config check needs none,
// since the daemon runs it itself (pkg/daemon's StateCheckSandboxes): the
// agent stub keeps its sandbox alive until the
// test writes agent-finish there, so the test makes the run's edit in
// that sandbox's checkout itself.

export const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

export async function eventually(fn, message, timeout = 30_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try {
      const value = await fn();
      if (value) return value;
    } catch (err) {
      last = err;
    }
    await sleep(100);
  }
  throw new Error(`${message}${last ? `: ${last.message}` : ""}`);
}

async function writeExecutable(file, text) {
  await fs.writeFile(file, text, { mode: 0o755 });
}

export async function startDaemon(root, dataDir, sandboxDir, extraArgs = []) {
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  if (!bin) throw new Error("global setup did not provide a Grain binary");

  const fakeBin = path.join(root, "bin");
  await fs.mkdir(fakeBin, { recursive: true });
  const agy = path.join(fakeBin, "agy");
  await writeExecutable(
    agy,
    `#!/bin/sh
set -eu
root=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--add-dir" ]; then
    root="$2"
    shift 2
  else
    shift
  fi
done
test -n "$root"
touch "$root/agent-ready"
while [ ! -e "$root/agent-finish" ]; do sleep 0.05; done
printf '%s\\n' \
  '{"event":"init","init":{"cwd":"/w","tools":["mcp__grain-sandbox__run_command"],"permission_mode":"bypass"}}' \
  '{"event":"step_update","step_update":{"step_index":0,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"command":"edit, commit and push the config checkout"}}}}' \
  '{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","output":"branch pushed"}}}' \
  '{"event":"result","result":{"status":"SUCCESS","response":"Pushed the native config change."}}'
`,
  );

  // The run's access code as the daemon's own (accesscode.js), which it
  // keeps rather than making one: the worker's access session then
  // signs the browser in here too.
  await writeAccessCode(path.join(dataDir, "local-api-token"));
  // These specs configure the daemon through its API, which a deployment
  // with a grain/config repository otherwise answers with a config pull
  // request to open (docs/design/config-repo-settings.md, section
  // 4.1.1); the host-local opt-out keeps those writes direct.
  await fs.writeFile(
    path.join(dataDir, "state-repo.json"),
    JSON.stringify({ directConfigWrites: true }, null, 2) + "\n",
  );

  const proc = spawn(
    bin,
    [
      "daemon",
      "-data-dir",
      dataDir,
      "-sandbox-dir",
      sandboxDir,
      "-ui-addr",
      "127.0.0.1:0",
      "-sandbox-port-base=0",
      "-as",
      "alice",
      "-poll-interval",
      "100ms",
      "-max-workers",
      "1",
      "-max-mergers",
      "0",
      "-agy-path",
      agy,
      "-gemini-api-key-file",
      path.join(root, "gemini-key"),
      "-github-host",
      "127.0.0.1:9",
      "-github-insecure-http",
      ...extraArgs,
    ],
    {
      cwd: root,
      env: {
        ...process.env,
        PATH: `${fakeBin}${path.delimiter}${process.env.PATH || ""}`,
      },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );

  let output = "";
  const append = (chunk) => {
    output += chunk.toString();
  };
  proc.stdout.on("data", append);
  proc.stderr.on("data", append);

  const url = await eventually(() => {
    const match = output.match(
      /serving the UI\/API on (http:\/\/127\.0\.0\.1:\d+)/,
    );
    if (match) return match[1];
    if (proc.exitCode !== null)
      throw new Error(`daemon exited ${proc.exitCode}:\n${output}`);
    return null;
  }, "the Grain daemon did not start");

  // The model connection runs are dispatched onto, recorded the way an
  // install records it: through `grain connections import`, once the
  // daemon has seeded its settings. The model is named explicitly: grain
  // has no default model of its own, and a connection with none to run
  // waits rather than guessing one. Importing it again on a restart
  // changes nothing.
  const seed = path.join(root, "connections.yaml");
  await fs.writeFile(
    seed,
    "models:\n" +
      "  - id: gemini\n" +
      "    framework: antigravity\n" +
      "    models: [gemini-3.1-pro]\n" +
      `    credential_file: ${JSON.stringify(path.join(root, "gemini-key"))}\n`,
  );
  await new Promise((resolve, reject) => {
    const imp = spawn(
      bin,
      ["connections", "-data-dir", dataDir, "import", seed],
      {
        cwd: root,
        stdio: ["ignore", "pipe", "pipe"],
      },
    );
    let said = "";
    imp.stdout.on("data", (chunk) => (said += chunk.toString()));
    imp.stderr.on("data", (chunk) => (said += chunk.toString()));
    imp.once("error", reject);
    imp.once("exit", (code) =>
      code === 0
        ? resolve()
        : reject(
            new Error(`grain connections import exited ${code}:\n${said}`),
          ),
    );
  });

  return {
    proc,
    url,
    output: () => output,
    async stop() {
      if (proc.exitCode !== null) return;
      proc.kill("SIGTERM");
      const stopped = new Promise((resolve) => proc.once("exit", resolve));
      const graceful = await Promise.race([
        stopped.then(() => true),
        sleep(5_000).then(() => false),
      ]);
      if (!graceful) {
        proc.kill("SIGKILL");
        await stopped;
      }
    },
  };
}

export async function taskByTitle(request, url, title) {
  const response = await request.get(`${url}/api/tasks`);
  expect(response.ok()).toBeTruthy();
  const tasks = await response.json();
  return tasks.find((task) => task.title === title);
}

export async function taskDetail(request, url, id) {
  const response = await request.get(`${url}/api/tasks/${id}`);
  expect(response.ok()).toBeTruthy();
  return response.json();
}

export async function activeSandbox(sandboxDir, excluded = new Set()) {
  let names = [];
  try {
    names = await fs.readdir(sandboxDir);
  } catch {
    return null;
  }
  for (const name of names) {
    if (excluded.has(name)) continue;
    const root = path.join(sandboxDir, name);
    try {
      await fs.access(path.join(root, "agent-ready"));
      await fs.access(path.join(root, "work", ".git"));
      return root;
    } catch {
      // This run has not reached its agent yet.
    }
  }
  return null;
}
