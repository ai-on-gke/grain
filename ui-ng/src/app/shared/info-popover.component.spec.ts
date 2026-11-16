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
import { describe, expect, it } from "vitest";
import { InfoPopoverComponent } from "./info-popover.component";

@Component({
  imports: [InfoPopoverComponent],
  template: `
    <div class="pane" style="overflow:auto;height:50px">
      <g-info-popover text="First" label="one" testid="a" />
      <g-info-popover text="Second" label="two" testid="b" />
      <div style="height:500px"></div>
    </div>
  `,
})
class HostComponent {}

async function mount() {
  const fixture = TestBed.createComponent(HostComponent);
  fixture.autoDetectChanges(true);
  await fixture.whenStable();
  const el = fixture.nativeElement as HTMLElement;
  const q = (id: string) =>
    el.querySelector<HTMLElement>(`[data-testid="${id}"]`);
  return { fixture, el, q };
}

describe("InfoPopoverComponent", () => {
  it("keeps one popover open: opening another closes the first", async () => {
    const { fixture, q } = await mount();
    q("a")!.click();
    await fixture.whenStable();
    expect(q("a-panel")?.textContent?.trim()).toBe("First");
    q("b")!.click();
    await fixture.whenStable();
    expect(q("a-panel")).toBeNull();
    expect(q("b-panel")?.textContent?.trim()).toBe("Second");
  });

  it("follows a pane's scroll, and closes once its button scrolls out of view", async () => {
    const { fixture, el, q } = await mount();
    q("a")!.click();
    await fixture.whenStable();
    expect(q("a-panel")).not.toBeNull();
    const pane = el.querySelector(".pane")!;
    pane.dispatchEvent(new Event("scroll"));
    await fixture.whenStable();
    expect(q("a-panel")).not.toBeNull();
    q("a")!.getBoundingClientRect = () =>
      ({
        top: -60,
        bottom: -40,
        left: 0,
        right: 20,
        width: 20,
        height: 20,
      }) as DOMRect;
    pane.dispatchEvent(new Event("scroll"));
    await fixture.whenStable();
    expect(q("a-panel")).toBeNull();
  });
});
