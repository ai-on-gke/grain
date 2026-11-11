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

import { Component } from "@angular/core";
import { TestBed } from "@angular/core/testing";
import { TIP_DELAY_MS, TIP_ID, TipDirective } from "./tip.directive";

@Component({
  imports: [TipDirective],
  template: `<button type="button" gTip="Opens the backlog.">Tasks</button>`,
})
class HostComponent {}

function tip(): HTMLElement | null {
  return document.getElementById(TIP_ID);
}

describe("TipDirective", () => {
  let button: HTMLButtonElement;
  beforeEach(() => {
    document.getElementById(TIP_ID)?.remove();
    TestBed.configureTestingModule({ imports: [HostComponent] });
    const fixture = TestBed.createComponent(HostComponent);
    fixture.detectChanges();
    button = (fixture.nativeElement as HTMLElement).querySelector("button")!;
  });
  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  it("shows at once on focus, describes the control, and hides on blur", () => {
    button.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
    expect(tip()?.hidden).toBe(false);
    expect(tip()?.getAttribute("role")).toBe("tooltip");
    expect(tip()?.textContent).toBe("Opens the backlog.");
    expect(button.getAttribute("aria-describedby")).toBe(TIP_ID);
    expect(button.hasAttribute("title")).toBe(false);
    button.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    expect(tip()?.hidden).toBe(true);
    expect(button.hasAttribute("aria-describedby")).toBe(false);
  });

  it("waits 400 ms on hover and hides on Escape", () => {
    vi.useFakeTimers();
    button.dispatchEvent(new MouseEvent("mouseenter"));
    vi.advanceTimersByTime(TIP_DELAY_MS - 1);
    expect(tip()?.hidden ?? true).toBe(true);
    vi.advanceTimersByTime(1);
    expect(tip()?.hidden).toBe(false);
    button.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    expect(tip()?.hidden).toBe(true);
  });

  it("never shows when the pointer leaves before the delay", () => {
    vi.useFakeTimers();
    button.dispatchEvent(new MouseEvent("mouseenter"));
    button.dispatchEvent(new MouseEvent("mouseleave"));
    vi.advanceTimersByTime(TIP_DELAY_MS * 2);
    expect(tip()?.hidden ?? true).toBe(true);
  });
});
