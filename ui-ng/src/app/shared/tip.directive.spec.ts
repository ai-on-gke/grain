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
import {
  TIP_DELAY_MS,
  TIP_ID,
  TIP_LEAVE_MS,
  TipDirective,
} from "./tip.directive";

@Component({
  imports: [TipDirective],
  template: `<button type="button" gTip="Opens the backlog.">Tasks</button>
    <button type="button" id="b2" gTip="Opens CI.">CI</button>`,
})
class HostComponent {}

function tip(): HTMLElement | null {
  return document.getElementById(TIP_ID);
}

describe("TipDirective", () => {
  let button: HTMLButtonElement;
  let second: HTMLButtonElement;
  beforeEach(() => {
    document.getElementById(TIP_ID)?.remove();
    TestBed.configureTestingModule({ imports: [HostComponent] });
    const fixture = TestBed.createComponent(HostComponent);
    fixture.detectChanges();
    button = (fixture.nativeElement as HTMLElement).querySelector("button")!;
    second = (fixture.nativeElement as HTMLElement).querySelector("#b2")!;
  });
  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  it("shows at once on focus, describes the control, and hides on blur", () => {
    button.focus();
    expect(tip()?.hidden).toBe(false);
    expect(tip()?.getAttribute("role")).toBe("tooltip");
    expect(tip()?.textContent).toBe("Opens the backlog.");
    expect(button.getAttribute("aria-describedby")).toBe(TIP_ID);
    expect(button.hasAttribute("title")).toBe(false);
    button.blur();
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

  it("hides on Escape pressed anywhere while hover-shown", () => {
    vi.useFakeTimers();
    button.dispatchEvent(new MouseEvent("mouseenter"));
    vi.advanceTimersByTime(TIP_DELAY_MS);
    expect(tip()?.hidden).toBe(false);
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    expect(tip()?.hidden).toBe(true);
  });

  it("stays up while the pointer moves onto the tip itself", () => {
    vi.useFakeTimers();
    button.dispatchEvent(new MouseEvent("mouseenter"));
    vi.advanceTimersByTime(TIP_DELAY_MS);
    button.dispatchEvent(new MouseEvent("mouseleave"));
    tip()!.dispatchEvent(new MouseEvent("mouseenter"));
    vi.advanceTimersByTime(TIP_LEAVE_MS * 2);
    expect(tip()?.hidden).toBe(false);
    tip()!.dispatchEvent(new MouseEvent("mouseleave"));
    vi.advanceTimersByTime(TIP_LEAVE_MS);
    expect(tip()?.hidden).toBe(true);
  });

  it("hands the shared tip to the next control and keeps other describedby ids", () => {
    second.setAttribute("aria-describedby", "help-ci");
    button.focus();
    second.focus();
    expect(tip()?.textContent).toBe("Opens CI.");
    expect(button.hasAttribute("aria-describedby")).toBe(false);
    expect(second.getAttribute("aria-describedby")).toBe(`help-ci ${TIP_ID}`);
    second.blur();
    expect(second.getAttribute("aria-describedby")).toBe("help-ci");
  });
});
