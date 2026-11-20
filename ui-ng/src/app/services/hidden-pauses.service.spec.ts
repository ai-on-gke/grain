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
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { summarizeDispatchStatus } from "../shared/dispatch-status";
import {
  HIDDEN_PAUSES_KEY,
  HiddenPausesService,
  hidePausesWithUndo,
} from "./hidden-pauses.service";
import { SnackbarService } from "./snackbar.service";

describe("HiddenPausesService", () => {
  const banner = () =>
    summarizeDispatchStatus(
      {
        agentPause: {
          paused: true,
          pauses: [
            {
              connection: "claude",
              model: "opus",
              until: new Date(Date.now() + 3600_000).toISOString(),
            },
          ],
        },
      },
      null,
    )!;

  beforeEach(() => localStorage.clear());
  afterEach(() => localStorage.clear());

  it("hides until reset, persists, and undoes", async () => {
    const svc = TestBed.inject(HiddenPausesService);
    const snackbar = TestBed.inject(SnackbarService);
    expect(svc.isHidden(banner())).toBe(false);

    hidePausesWithUndo(svc, snackbar, banner());
    expect(svc.isHidden(banner())).toBe(true);
    expect(localStorage.getItem(HIDDEN_PAUSES_KEY)).toContain("opus");
    expect(snackbar.current()?.message).toBe(
      "Model pause warning hidden until reset",
    );

    // A fresh page reads the same choice back.
    TestBed.resetTestingModule();
    expect(TestBed.inject(HiddenPausesService).isHidden(banner())).toBe(true);

    const svc2 = TestBed.inject(HiddenPausesService);
    const snack2 = TestBed.inject(SnackbarService);
    svc2.show(banner());
    hidePausesWithUndo(svc2, snack2, banner());
    expect(svc2.isHidden(banner())).toBe(true);
    await snack2.runAction(snack2.current()!.id);
    expect(svc2.isHidden(banner())).toBe(false);
    expect(localStorage.getItem(HIDDEN_PAUSES_KEY)).toBeNull();
  });

  it("show un-hides, and a corrupt stored value is ignored", () => {
    localStorage.setItem(HIDDEN_PAUSES_KEY, "{not json");
    const svc = TestBed.inject(HiddenPausesService);
    expect(svc.hidden()).toEqual([]);
    svc.hide(banner());
    expect(svc.isHidden(banner())).toBe(true);
    svc.show(banner());
    expect(svc.isHidden(banner())).toBe(false);
  });
});
