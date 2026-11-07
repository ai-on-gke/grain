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

// Stop then Retry on a real orchestrator (docs/test-plans/2026-11-02.md
// §4, "What is NOT proved" 4; report docs/test-runs/2026-11-02-merge-walk-
// retry.md). consolerestserver holds the task's run open
// (/e2e/interactive-control), Stop interrupts it through the same
// canceller a daemon wires, and the stopped task's page offers Retry,
// which dispatches the task again at once rather than after the failure
// backoff a plain cancellation earns.

import { test, expect } from "./console-rest-fixtures.js";

const WIDTHS = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};

async function task(page, id) {
  const response = await page.request.get(`/api/tasks/${id}`);
  expect(response.ok()).toBeTruthy();
  return response.json();
}

// stopSettled waits out the whole of Stop, not only its first half. Stop
// (ui.Client.Stop) posts its comment and parks the task before it cancels
// the run, and the parked comment outranks a live run in model.StateOf --
// so the task reads awaiting_reply with turnStopped the moment /stop
// answers, while the cancelled run is still unwinding and its attempt is
// not finished yet. Reading the attempt then (or pressing Retry, which the
// page offers on the same two facts) raced the orchestrator recording
// it: on a busy runner the attempt came back with no detail at all, and
// rc.1's nightly failed this spec in all three of its shards.
async function stopSettled(page, id) {
  await expect
    .poll(
      async () => {
        const t = await task(page, id);
        const attempt = t.attempts?.[0];
        return {
          turnStopped: !!t.turnStopped,
          finished: !!attempt?.finishedAt,
          detail: attempt?.detail ?? "",
        };
      },
      { timeout: 15_000 },
    )
    .toEqual({
      turnStopped: true,
      finished: true,
      detail: "turn stopped by user",
    });
}

for (const [name, viewport] of Object.entries(WIDTHS)) {
  test(`a stopped turn is resumed by Retry at once (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const created = await page.request.post("/api/tasks", {
      data: { title: `Stop then Retry (${name})`, body: "hold this run" },
    });
    expect(created.ok()).toBeTruthy();
    const id = (await created.json()).id;
    try {
      const held = await page.request.post("/e2e/interactive-control", {
        data: { taskId: id, holdNext: true },
      });
      expect(held.status()).toBe(204);
      expect(
        (await page.request.post(`/api/tasks/${id}/approve`)).ok(),
      ).toBeTruthy();
      await expect
        .poll(async () => (await task(page, id)).state, { timeout: 15_000 })
        .toBe("running");

      expect(
        (await page.request.post(`/api/tasks/${id}/stop`)).ok(),
      ).toBeTruthy();
      await stopSettled(page, id);
      const stopped = await task(page, id);
      expect(stopped.state).toBe("awaiting_reply");
      expect(stopped.attempts[0].detail).toBe("turn stopped by user");

      await page.goto(`/tasks/${id}`);
      const retry = page.getByRole("button", { name: "Retry", exact: true });
      await expect(retry).toBeVisible();
      const box = await retry.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);

      const pressed = Date.now();
      await retry.click();
      await expect
        .poll(async () => (await task(page, id)).attempts.length, {
          timeout: 10_000,
        })
        .toBeGreaterThan(1);
      // The failure backoff's first step is a minute; a dispatch inside
      // ten seconds is not one that waited it out.
      expect(Date.now() - pressed).toBeLessThan(10_000);
      expect((await task(page, id)).turnStopped).toBeFalsy();
    } finally {
      await page.request.post(`/api/tasks/${id}/close`, {
        data: { reason: "stopped-turn-retry.spec.js" },
      });
    }
  });
}

test("Retry dispatches at once while another task holds a worker slot", async ({
  page,
}) => {
  await page.setViewportSize(WIDTHS.desktop);
  const heldBg = await page.request.post("/api/tasks", {
    data: { title: "Held interactive neighbour", body: "occupy one slot" },
  });
  expect(heldBg.ok()).toBeTruthy();
  const bgId = (await heldBg.json()).id;

  const created = await page.request.post("/api/tasks", {
    data: { title: "Stop then Retry beside held slot", body: "hold this run" },
  });
  expect(created.ok()).toBeTruthy();
  const id = (await created.json()).id;

  try {
    expect(
      (
        await page.request.post("/e2e/interactive-control", {
          data: { taskId: bgId, holdNext: true },
        })
      ).status(),
    ).toBe(204);
    expect(
      (await page.request.post(`/api/tasks/${bgId}/approve`)).ok(),
    ).toBeTruthy();
    await expect
      .poll(async () => (await task(page, bgId)).state, { timeout: 15_000 })
      .toBe("running");

    // Configuring holdNext for a second task must not strand bgId's release
    // channel or starve the remaining worker slot across Stop and Retry.
    expect(
      (
        await page.request.post("/e2e/interactive-control", {
          data: { taskId: id, holdNext: true },
        })
      ).status(),
    ).toBe(204);
    expect(
      (await page.request.post(`/api/tasks/${id}/approve`)).ok(),
    ).toBeTruthy();
    await expect
      .poll(async () => (await task(page, id)).state, { timeout: 15_000 })
      .toBe("running");

    expect(
      (await page.request.post(`/api/tasks/${id}/stop`)).ok(),
    ).toBeTruthy();
    await stopSettled(page, id);

    await page.goto(`/tasks/${id}`);
    const retry = page.getByRole("button", { name: "Retry", exact: true });
    await expect(retry).toBeVisible();

    const pressed = Date.now();
    await retry.click();
    await expect
      .poll(async () => (await task(page, id)).attempts.length, {
        timeout: 10_000,
      })
      .toBeGreaterThan(1);
    expect(Date.now() - pressed).toBeLessThan(10_000);
    expect((await task(page, bgId)).state).toBe("running");

    expect(
      (
        await page.request.post("/e2e/interactive-control", {
          data: { taskId: bgId, releaseRun: true },
        })
      ).status(),
    ).toBe(204);
    await expect
      .poll(async () => (await task(page, bgId)).state, { timeout: 15_000 })
      .not.toBe("running");
  } finally {
    await page.request
      .post("/e2e/interactive-control", {
        data: { taskId: bgId, releaseRun: true },
      })
      .catch(() => {});
    await page.request.post(`/api/tasks/${bgId}/close`, {
      data: { reason: "stopped-turn-retry.spec.js" },
    });
    await page.request.post(`/api/tasks/${id}/close`, {
      data: { reason: "stopped-turn-retry.spec.js" },
    });
  }
});
