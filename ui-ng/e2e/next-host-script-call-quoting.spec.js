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

// A host-script call waiting for approval is approved on exactly what it
// shows (2026-11-20 intake walkthrough). The Inbox row and inspector
// joined the arguments with bare spaces -- `deploy a b $(rm -rf /)` for
// the two arguments "a b" and "$(rm -rf /)" -- so one argument with a
// space read as two, and a person approved a call that was not the one
// shown. And both the inspector and the task page's card hid the stdin
// line when the call carried stdin but no preview of it. Both surfaces
// now write the call as classic's LocalScriptApprovals.jsx does, every
// argument quoted, and say what stdin the script gets whenever it gets
// any.

async function api(baseURL, code, method, path, data) {
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

const SHA = "ab12cd34".repeat(8);
const CALL = 'deploy "a b" "$(rm -rf /)"';

test("a host-script call shows every argument quoted and its stdin in the Inbox and on the task page", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const call = (method, path, data) =>
    api(demoServer, accessCode, method, path, data);
  const task = await call("POST", "/api/tasks", {
    title: `Host script quoting ${Date.now()}`,
    description: "Uses the ops host scripts.",
    approved: true,
    capabilities: [
      'https://grain.dev/capabilities/host-script#host-script={"script":"ops"}',
    ],
  });
  const L = `/api/tasks/${task.id}/local-script-approvals`;
  const ask = (id) =>
    call("POST", L, {
      id,
      tool: "local_deploy",
      script: "deploy",
      args: ["a b", "$(rm -rf /)"],
      stdin_bytes: 4096,
      stdin_sha256: SHA,
      deadline: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
    });
  const first = await ask("1");
  try {
    // The Inbox: the row and the inspector the person approves from.
    await page.goto("inbox");
    const row = page.locator(
      `[data-testid="queue-row"][data-key="local-script:${task.id}:${first.id}"]`,
    );
    await expect(row).toContainText(CALL);
    await row.click();
    await expect(page.getByTestId("script-call")).toHaveText(CALL);
    await expect(page.getByTestId("script-stdin")).toHaveText(
      `On stdin: 4096 bytes, sha256 ${SHA}`,
    );

    // The task page's card says the same.
    await page.goto(`task/${task.id}`);
    const card = page.getByTestId(`local-script-${first.id}`);
    await expect(card.locator("pre").first()).toHaveText(CALL);
    await expect(card.getByTestId("lsa-stdin")).toHaveText(
      `On stdin: 4096 bytes, sha256 ${SHA}`,
    );

    // Deny it from the Inbox: the row leaves and the call is answered.
    await page.goto("inbox");
    await row.click();
    await page.getByRole("button", { name: "Deny", exact: true }).click();
    await expect(row).toHaveCount(0);
    const pending = await call("GET", "/api/local-script-approvals");
    expect(pending.filter((a) => a.taskId === task.id)).toEqual([]);
  } finally {
    await call("POST", `/api/tasks/${task.id}/close`, {}).catch(() => {});
  }
});
