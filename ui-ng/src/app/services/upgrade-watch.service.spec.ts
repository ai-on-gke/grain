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

import { TestBed } from "@angular/core/testing";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import type { GrainConfig } from "../api/models";
import {
  failedStepError,
  formatElapsed,
  upgradeProgress,
  upgradeStepLine,
} from "../settings/upgrade-progress";
import {
  hasBreakingChange,
  releaseNotesOf,
} from "../settings/settings-upgrade.component";
import { AccessService } from "./access.service";
import { UpgradeWatchService } from "./upgrade-watch.service";

const cfg = (commit: string, release = "v3.8.0") =>
  ({ version: { release, commit } }) as unknown as GrainConfig;

describe("upgrade progress", () => {
  it("numbers the current step and times it, as the classic pane does", () => {
    const steps = [
      { name: "check target", state: "done" },
      {
        name: "pull image",
        state: "running",
        startedAt: "2026-11-15T10:00:00Z",
      },
      { name: "restart", state: "pending" },
    ];
    const now = Date.parse("2026-11-15T10:01:05Z");
    expect(upgradeProgress(steps)).toMatchObject({
      total: 3,
      settled: 1,
      percent: 33,
      position: 2,
    });
    expect(upgradeStepLine(steps, now)).toBe("step 2/3 pull image (1m 5s)");
    expect(formatElapsed(3_700_000)).toBe("1h 1m");
    expect(upgradeProgress(undefined).total).toBe(0);
  });

  it("names a failed step's own error before the status detail", () => {
    expect(
      failedStepError({
        detail: "upgrade failed",
        steps: [
          { name: "pull image", state: "failed", detail: "unauthorized" },
        ],
      }),
    ).toBe("unauthorized");
    expect(failedStepError({ detail: "upgrade failed" })).toBe(
      "upgrade failed",
    );
  });
});

describe("release notes", () => {
  it("reads a preview or plain Markdown and finds a breaking change", () => {
    expect(releaseNotesOf("## v2.4.0\n\n**Breaking:** x").markdown).toContain(
      "**Breaking:**",
    );
    expect(
      releaseNotesOf({
        message: "release notes are not in this build",
        notes: [{ version: "v2.4.0", markdown: "# v2.4.0\n- y" }],
      }),
    ).toEqual({
      message: "Release notes are not in this build.",
      markdown: "# v2.4.0\n- y",
    });
    expect(hasBreakingChange("## v2.4.0\n\n**Breaking:** drops v1")).toBe(true);
    expect(hasBreakingChange("## Breaking changes\n- a")).toBe(true);
    expect(hasBreakingChange("- a non-breaking tweak")).toBe(false);
  });
});

describe("UpgradeWatchService", () => {
  let api: { get: ReturnType<typeof vi.fn> };

  beforeEach(() => {
    vi.useFakeTimers();
    api = { get: vi.fn() };
    TestBed.configureTestingModule({
      providers: [{ provide: GrainApi, useValue: api }],
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  it("reloads once the daemon answers on another commit, after the countdown", async () => {
    const watch = TestBed.inject(UpgradeWatchService);
    const reload = vi.fn();
    watch.reloadPage = reload;
    watch.observeConfig(cfg("aaaa111"));
    watch.observeConfig(cfg("aaaa111"));
    expect(watch.upgradedTo()).toBeNull();
    watch.observeConfig(cfg("bbbb222", "v3.9.0"));
    expect(watch.upgradedTo()).toBe("v3.9.0");
    expect(watch.reloadIn()).toBe(5);
    vi.advanceTimersByTime(4000);
    expect(reload).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1000);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("holds the reload while a page has unsaved work", () => {
    const watch = TestBed.inject(UpgradeWatchService);
    const reload = vi.fn();
    watch.reloadPage = reload;
    const guard = (ev: Event) => ev.preventDefault();
    window.addEventListener("beforeunload", guard);
    try {
      watch.observeConfig(cfg("aaaa111"));
      watch.observeConfig(cfg("bbbb222"));
      vi.advanceTimersByTime(10_000);
      expect(watch.reloadIn()).toBeNull();
      expect(reload).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("beforeunload", guard);
    }
    vi.advanceTimersByTime(6000);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("sees the shell's config reads through AccessService", () => {
    const watch = TestBed.inject(UpgradeWatchService);
    watch.reloadPage = vi.fn();
    const access = TestBed.inject(AccessService);
    access.setConfig(cfg("aaaa111"));
    TestBed.flushEffects();
    access.setConfig(cfg("cccc333", "v4.0.0"));
    TestBed.flushEffects();
    expect(watch.upgradedTo()).toBe("v4.0.0");
  });

  it("polls a running upgrade and probes for the new build once it is done", async () => {
    const watch = TestBed.inject(UpgradeWatchService);
    watch.reloadPage = vi.fn();
    watch.observeConfig(cfg("aaaa111"));
    api.get.mockResolvedValueOnce({ phase: "running", branch: "latest" });
    await watch.refresh();
    expect(watch.sawRunning()).toBe(true);
    api.get.mockResolvedValueOnce({ phase: "ok", branch: "latest" });
    await vi.advanceTimersByTimeAsync(3000);
    expect(watch.status()?.phase).toBe("ok");
    api.get.mockResolvedValue(cfg("dddd444", "v3.9.1"));
    await vi.advanceTimersByTimeAsync(1000);
    expect(api.get).toHaveBeenCalledWith(
      "GET /api/config",
      undefined,
      expect.stringMatching(/^after-upgrade=/),
    );
    expect(watch.upgradedTo()).toBe("v3.9.1");
  });
});
