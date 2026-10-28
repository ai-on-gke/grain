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

import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(__dirname, "../..");
const SCREENSHOTS_DIR = path.resolve(__dirname, "screenshots");

// Dynamically resolve playwright from ui/node_modules
const { chromium } = await import("../../ui/node_modules/@playwright/test/index.mjs");

async function main() {
  await fs.mkdir(SCREENSHOTS_DIR, { recursive: true });

  const port = 8438;
  const url = `http://127.0.0.1:${port}`;

  console.log(`Starting grain demo cluster on port ${port}...`);
  const child = spawn("./bin/grain", ["demo", `-addr=127.0.0.1:${port}`, "-open=false", "-observability"], {
    cwd: ROOT,
    stdio: ["ignore", "pipe", "pipe"],
  });

  child.stderr.on("data", (d) => process.stderr.write(d));
  await new Promise((resolve) => setTimeout(resolve, 2000));

  console.log("Launching headless browser at high resolution...");
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1920, height: 1080 },
    deviceScaleFactor: 2,
  });

  const page = await context.newPage();
  await page.addInitScript(() => {
    window.localStorage.setItem("grain.username", "operator");
    window.localStorage.setItem("grain.themeMode", "dark");
  });

  // 1. Create a Task (Task filing dialog with custom fields, capabilities, auto-merge)
  console.log("Capturing 01: Create a Task...");
  await page.goto(`${url}/`);
  await page.waitForTimeout(1000);
  const newTaskBtn = page.locator('button:has-text("+ New task")').first();
  if (await newTaskBtn.isVisible()) {
    await newTaskBtn.click();
    await page.waitForTimeout(800);
    const titleInput = page.locator('input[placeholder="What needs doing?"], input[name="title"], textarea[name="title"]').first();
    if (await titleInput.isVisible()) {
      await titleInput.fill("Implement telemetry export for Kontur VM metrics");
    }
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(SCREENSHOTS_DIR, "01-create-a-task.png") });
    await page.keyboard.press("Escape");
    await page.waitForTimeout(500);
  }

  // 2. Task Picked Up by Agent (Limited Concurrency & Queue Dispatch)
  console.log("Capturing 02: Task Picked Up by Agent...");
  await page.goto(`${url}/`);
  await page.waitForTimeout(1000);
  await page.screenshot({ path: path.join(SCREENSHOTS_DIR, "02-task-picked-up-by-agent.png") });

  // 3. Agent Sandbox Isolation & Scoped Credentials
  console.log("Capturing 03: Sandboxes & Ephemeral Credentials...");
  await page.request.post(`${url}/api/e2e/sandboxes/other`);
  await page.goto(`${url}/sandboxes`);
  await page.waitForTimeout(1000);
  await page.screenshot({ path: path.join(SCREENSHOTS_DIR, "03-agent-sandbox-credentials.png") });

  // 4. Agent Executes Task (Live Transcripts & Tool Execution)
  console.log("Capturing 04: Agent Executes Task...");
  await page.goto(`${url}/`);
  await page.waitForTimeout(800);
  const runningTask = page.locator("text=Investigate the merge queue stall").first();
  if (await runningTask.isVisible()) {
    await runningTask.click();
    await page.waitForTimeout(1200);
    await page.screenshot({ path: path.join(SCREENSHOTS_DIR, "04-agent-executes-task.png") });
    await page.keyboard.press("Escape");
    await page.waitForTimeout(500);
  }

  // 5. CI / Merge Pipeline & Autonomous Interposition
  console.log("Capturing 05: CI & Autonomous Merge Pipeline...");
  await page.goto(`${url}/ci`);
  await page.waitForTimeout(1000);
  const startBtn = page.getByRole("button", { name: "Start a run" });
  if (await startBtn.isVisible()) {
    await startBtn.click();
    await page.waitForTimeout(500);
    const repoSelect = page.locator('select[name="repo"]');
    if (await repoSelect.count()) {
      await repoSelect.selectOption("acme/widgets");
    }
    await page.getByLabel("Branch").fill("main");
    await page.getByRole("button", { name: "Load workflows" }).click();
    await page.waitForTimeout(500);
    await page.getByRole("button", { name: "Start run", exact: true }).click();
    await page.waitForTimeout(1000);
    await page.request.post(`${url}/api/e2e/ci/finish`);
    await page.waitForTimeout(1000);
  }
  await page.screenshot({ path: path.join(SCREENSHOTS_DIR, "05-ci-merge-pipeline.png") });

  // 6. Agents Schedule Sub-Tasks & Follow-Ups
  console.log("Capturing 06: Agent Sub-Tasks & Follow-Ups...");
  await page.goto(`${url}/boards`);
  await page.waitForTimeout(800);
  const kanbanRow = page.locator("text=Board").first();
  if (await kanbanRow.isVisible()) {
    await kanbanRow.click();
    await page.waitForTimeout(1000);
  }
  const subtaskRow = page.locator("text=Extract pagination query helper").first();
  if (await subtaskRow.isVisible()) {
    await subtaskRow.click();
    await page.waitForTimeout(1000);
  }
  await page.screenshot({ path: path.join(SCREENSHOTS_DIR, "06-agent-subtasks-followups.png") });
  await page.keyboard.press("Escape");
  await page.waitForTimeout(500);

  // 7. Director Interactive Planning Session
  console.log("Capturing 07: Director Interactive Planning Session...");
  await page.goto(`${url}/session/director`);
  await page.waitForTimeout(1200);
  await page.screenshot({ path: path.join(SCREENSHOTS_DIR, "07-director-planning-session.png") });

  // 8. Scheduled Agents & Test Suites
  console.log("Capturing 08: Schedules & Test Suites...");
  await page.goto(`${url}/schedules`);
  await page.waitForTimeout(1000);
  await page.screenshot({ path: path.join(SCREENSHOTS_DIR, "08-schedules-test-suites.png") });

  // 9. GitOps Cluster Configuration via Repo Check-ins
  console.log("Capturing 09: GitOps Cluster Configuration...");
  await page.goto(`${url}/settings`);
  await page.waitForTimeout(1000);
  await page.screenshot({ path: path.join(SCREENSHOTS_DIR, "09-cluster-configuration-gitops.png") });

  await browser.close();
  child.kill();
  console.log("All 9 high-resolution screenshots captured successfully in docs/slides/screenshots/");
}

main().catch((err) => {
  console.error("Screenshot capture failed:", err);
  process.exit(1);
});
