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

import { Component, signal } from "@angular/core";
import { TestBed } from "@angular/core/testing";
import { InfoPopoverComponent } from "./info-popover.component";

@Component({
  imports: [InfoPopoverComponent],
  template: `
    <div>
      <g-info-popover [title]="title()" [label]="label()" testId="field-info" />
      <button type="button" data-testid="outside">Outside</button>
    </div>
  `,
})
class HostComponent {
  readonly title = signal("Explains what this field configures.");
  readonly label = signal("Permission set *");
}

async function mount() {
  TestBed.configureTestingModule({ imports: [HostComponent] });
  const fixture = TestBed.createComponent(HostComponent);
  fixture.autoDetectChanges(true);
  await fixture.whenStable();
  const el = fixture.nativeElement as HTMLElement;
  const btn = () =>
    el.querySelector<HTMLButtonElement>('[data-testid="field-info"]');
  const panel = () => el.querySelector<HTMLElement>('[role="tooltip"]');
  return { fixture, el, btn, panel, host: fixture.componentInstance };
}

describe("InfoPopoverComponent", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("renders nothing when title is empty and strips trailing asterisk from aria-label", async () => {
    const { btn, host, fixture } = await mount();
    expect(btn()?.getAttribute("aria-label")).toBe("About Permission set");
    expect(btn()?.getAttribute("aria-expanded")).toBe("false");

    host.title.set("");
    await fixture.whenStable();
    expect(btn()).toBeNull();
  });

  it("opens on click and closes on a second click or outside click", async () => {
    const { btn, panel, el, fixture } = await mount();
    btn()!.click();
    await fixture.whenStable();
    expect(panel()?.textContent?.trim()).toBe(
      "Explains what this field configures.",
    );
    expect(btn()?.getAttribute("aria-expanded")).toBe("true");
    expect(btn()?.getAttribute("aria-describedby")).toBe(panel()?.id);

    // Second click closes.
    btn()!.click();
    await fixture.whenStable();
    expect(panel()).toBeNull();
    expect(btn()?.getAttribute("aria-expanded")).toBe("false");

    // Re-open and close by clicking outside.
    btn()!.click();
    await fixture.whenStable();
    expect(panel()).not.toBeNull();
    el.querySelector<HTMLButtonElement>('[data-testid="outside"]')!.click();
    await fixture.whenStable();
    expect(panel()).toBeNull();
  });

  it("pins open on first Enter or Space after Tab focus instead of toggling shut", async () => {
    const { btn, panel, fixture } = await mount();

    // Simulate Tab focus opening transiently.
    btn()!.dispatchEvent(new FocusEvent("focus"));
    await fixture.whenStable();
    expect(panel()).not.toBeNull();

    // First Enter pins open (does NOT close what focus just opened).
    const enter1 = new KeyboardEvent("keydown", {
      key: "Enter",
      cancelable: true,
    });
    btn()!.dispatchEvent(enter1);
    await fixture.whenStable();
    expect(enter1.defaultPrevented).toBe(true);
    expect(panel()).not.toBeNull();

    // Second Enter closes.
    btn()!.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", cancelable: true }),
    );
    await fixture.whenStable();
    expect(panel()).toBeNull();

    // Space opens while focused, and Escape closes.
    const space1 = new KeyboardEvent("keydown", { key: " ", cancelable: true });
    btn()!.dispatchEvent(space1);
    await fixture.whenStable();
    expect(space1.defaultPrevented).toBe(true);
    expect(panel()).not.toBeNull();

    btn()!.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", cancelable: true }),
    );
    await fixture.whenStable();
    expect(panel()).toBeNull();
  });

  it("closes on focusout to another element outside the popover", async () => {
    const { btn, panel, el, fixture } = await mount();
    const outside = el.querySelector<HTMLButtonElement>(
      '[data-testid="outside"]',
    )!;
    btn()!.click();
    await fixture.whenStable();
    expect(panel()).not.toBeNull();

    btn()!.dispatchEvent(
      new FocusEvent("focusout", { bubbles: true, relatedTarget: outside }),
    );
    await fixture.whenStable();
    expect(panel()).toBeNull();
  });
});
