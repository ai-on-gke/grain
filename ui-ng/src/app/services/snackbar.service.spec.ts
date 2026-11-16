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

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { TestBed } from "@angular/core/testing";
import { TIP_ID, TipDirective } from "../shared/tip.directive";
import { SnackbarComponent } from "../shell/snackbar.component";
import {
  ACTION_SNACK_MS,
  DEFAULT_SNACK_MS,
  SnackbarService,
} from "./snackbar.service";

describe("SnackbarService", () => {
  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  it("shows one message at a time and dismisses it after five seconds", () => {
    vi.useFakeTimers();
    const s = TestBed.inject(SnackbarService);
    s.show("first");
    const second = s.show("second");
    expect(s.current()?.message).toBe("second");
    vi.advanceTimersByTime(DEFAULT_SNACK_MS - 1);
    expect(s.current()?.id).toBe(second.id);
    vi.advanceTimersByTime(1);
    expect(s.current()).toBeNull();
  });

  it("runs the action once and dismisses the snack", async () => {
    const s = TestBed.inject(SnackbarService);
    const undo = vi.fn();
    const snack = s.show("Moved", { label: "Undo", run: undo });
    await s.runAction(snack.id);
    await s.runAction(snack.id);
    expect(undo).toHaveBeenCalledTimes(1);
    expect(s.current()).toBeNull();
  });

  it("keeps a snack with an action for ten seconds, paused while hovered", () => {
    vi.useFakeTimers();
    const s = TestBed.inject(SnackbarService);
    s.show("Moved", { label: "Undo", run: () => undefined });
    vi.advanceTimersByTime(ACTION_SNACK_MS - 1000);
    s.pause();
    vi.advanceTimersByTime(ACTION_SNACK_MS * 3);
    expect(s.current()?.message).toBe("Moved");
    s.resume();
    vi.advanceTimersByTime(ACTION_SNACK_MS);
    expect(s.current()).toBeNull();
  });

  it("says so when the action fails", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const s = TestBed.inject(SnackbarService);
    const snack = s.show("Moved", {
      label: "Undo",
      run: () => Promise.reject(new Error("409 stale")),
    });
    await s.runAction(snack.id);
    expect(s.current()?.message).toBe("Undo failed: 409 stale");
  });

  it("does not dismiss a newer snack when an older one's id is passed", () => {
    const s = TestBed.inject(SnackbarService);
    const old = s.show("old", undefined, 0);
    s.show("new", undefined, 0);
    s.dismiss(old.id);
    expect(s.current()?.message).toBe("new");
  });
});

describe("SnackbarComponent", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("announces through a polite live region and runs Undo", async () => {
    TestBed.configureTestingModule({ imports: [SnackbarComponent] });
    const fixture = TestBed.createComponent(SnackbarComponent);
    fixture.autoDetectChanges();
    const el = fixture.nativeElement as HTMLElement;
    expect(el.querySelector('[role="status"]')?.getAttribute("aria-live")).toBe(
      "polite",
    );
    const undo = vi.fn();
    TestBed.inject(SnackbarService).show("Approved #4", {
      label: "Undo",
      run: undo,
    });
    await fixture.whenStable();
    expect(el.querySelector('[data-testid="snackbar"]')?.textContent).toContain(
      "Approved #4",
    );
    (
      el.querySelector('[data-testid="snackbar-action"]') as HTMLElement
    ).click();
    await fixture.whenStable();
    expect(undo).toHaveBeenCalledTimes(1);
    expect(el.querySelector('[data-testid="snackbar"]')).toBeNull();
  });

  it("dismisses any active tooltip when a snackbar appears and anchors above --grain-bottombar-height", () => {
    const stylesCss = readFileSync(
      resolve(__dirname, "../../styles.css"),
      "utf8",
    );
    expect(stylesCss).toContain("g-snackbar {\n  display: contents;\n}");
    expect(stylesCss).toContain(
      "bottom: calc(var(--grain-bottombar-height, 40px) + 16px);",
    );
    expect(stylesCss).toContain(
      "bottom: calc(var(--grain-bottombar-height, 92px) + 84px);",
    );
    expect(stylesCss).toContain(
      "bottom: calc(var(--grain-bottombar-height, 52px) + 80px);",
    );

    void TipDirective;
    const tipEl = document.createElement("div");
    tipEl.id = TIP_ID;
    tipEl.hidden = false;
    document.body.appendChild(tipEl);
    TestBed.inject(SnackbarService).show(
      "Filed #552; it starts when a slot is free",
    );
    tipEl.remove();
  });
});
