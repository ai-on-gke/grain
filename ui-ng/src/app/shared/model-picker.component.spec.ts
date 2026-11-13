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
import { type ModelGroup, modelGroups } from "./model-catalog";
import { ModelPickerComponent } from "./model-picker.component";

@Component({
  imports: [ModelPickerComponent],
  template: `
    <g-model-picker
      testid="mp"
      ariaLabel="Default model"
      [groups]="groups()"
      [value]="value()"
      [exclude]="exclude()"
      [refreshable]="true"
      (valueChange)="chosen.push($event); value.set($event)"
      (refresh)="refreshes = refreshes + 1"
    />
  `,
})
class HostComponent {
  readonly groups = signal<ModelGroup[]>(
    modelGroups([
      {
        id: "agy",
        framework: "antigravity",
        discovered: { models: ["gemini-pro", "gemini-flash"] },
      },
      { id: "claude", framework: "claude", models: ["opus-5"] },
    ]),
  );
  readonly value = signal("");
  readonly exclude = signal<string[]>([]);
  chosen: string[] = [];
  refreshes = 0;
}

async function mount(init?: (h: HostComponent) => void) {
  TestBed.configureTestingModule({ imports: [HostComponent] });
  const fixture = TestBed.createComponent(HostComponent);
  init?.(fixture.componentInstance);
  fixture.autoDetectChanges(true);
  await fixture.whenStable();
  const el = fixture.nativeElement as HTMLElement;
  const q = <T extends Element = HTMLElement>(sel: string) =>
    el.querySelector<T & Element>(sel) as T | null;
  const openPanel = async () => {
    q<HTMLButtonElement>('[data-testid="mp"]')!.click();
    await new Promise((r) => setTimeout(r, 5));
    await fixture.whenStable();
  };
  const options = () =>
    [...el.querySelectorAll<HTMLElement>('[data-testid="mp-opt"]')].map(
      (o) => `${o.dataset["group"]}:${o.dataset["model"]}`,
    );
  return {
    fixture,
    el,
    q,
    openPanel,
    options,
    host: fixture.componentInstance,
  };
}

describe("ModelPickerComponent", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("offers the advertised models grouped by connection, never a free-text field", async () => {
    const { el, openPanel, options } = await mount();
    await openPanel();
    expect(options()).toEqual([
      "agy:gemini-pro",
      "agy:gemini-flash",
      "claude:opus-5",
    ]);
    const heads = [...el.querySelectorAll(".mp-group-head")].map((h) => [
      h.querySelector(".mp-group-name")?.textContent?.trim(),
      h.querySelector(".mp-group-note")?.textContent?.trim(),
    ]);
    expect(heads).toEqual([
      ["agy", "antigravity"],
      ["claude", "claude · pinned list"],
    ]);
    // The only text box is the search; there is no native select.
    expect(el.querySelectorAll("select").length).toBe(0);
    expect(el.querySelectorAll('input[type="text"]').length).toBe(1);
    expect(
      el.querySelector('[role="listbox"] [role="group"] [role="option"]'),
    ).not.toBeNull();
  });

  it("filters by search and chooses with the keyboard", async () => {
    const { q, openPanel, options, host, fixture } = await mount();
    await openPanel();
    const search = q<HTMLInputElement>('[data-testid="mp-search"]')!;
    expect(document.activeElement).toBe(search);
    search.value = "FLASH";
    search.dispatchEvent(new Event("input"));
    await fixture.whenStable();
    expect(options()).toEqual(["agy:gemini-flash"]);
    const optEl = q<HTMLElement>('[data-testid="mp-opt"]')!;
    expect(optEl.id).toMatch(/^mp-\d+-o-0-0$/);
    expect(search.getAttribute("aria-activedescendant")).toBe(optEl.id);
    search.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" }));
    await fixture.whenStable();
    expect(host.chosen).toEqual(["gemini-flash"]);
    expect(q('[data-testid="mp-panel"]')).toBeNull();
    expect(q('[data-testid="mp"]')?.textContent).toContain("gemini-flash");
  });

  it("keeps a current value no connection advertises, flagged", async () => {
    const { q, openPanel, options } = await mount((h) =>
      h.value.set("legacy-model"),
    );
    const trigger = q<HTMLButtonElement>('[data-testid="mp"]')!;
    expect(trigger.textContent).toContain("legacy-model");
    expect(q('[data-testid="mp-flag"]')?.textContent).toContain(
      "not advertised by any connection",
    );
    expect(trigger.getAttribute("aria-label")).toBe(
      "Default model: legacy-model (not advertised by any connection)",
    );
    await openPanel();
    expect(options()[0]).toBe(":legacy-model");
    expect(q(".mp-group-head")?.textContent).toContain("Current setting");
    expect(
      q('[data-model="legacy-model"]')?.getAttribute("aria-selected"),
    ).toBe("true");
  });

  it("hides excluded models, closes on Escape back to the trigger, and asks for a refresh", async () => {
    const { q, openPanel, options, host, fixture } = await mount((h) =>
      h.exclude.set(["GEMINI-PRO"]),
    );
    await openPanel();
    expect(options()).toEqual(["agy:gemini-flash", "claude:opus-5"]);
    q<HTMLButtonElement>('[data-testid="mp-refresh"]')!.click();
    expect(host.refreshes).toBe(1);
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    await fixture.whenStable();
    expect(q('[data-testid="mp-panel"]')).toBeNull();
    expect(document.activeElement).toBe(q('[data-testid="mp"]'));
    expect(host.chosen).toEqual([]);
  });

  it("says so when nothing is advertised or nothing matches", async () => {
    const { q, openPanel, fixture } = await mount((h) => h.groups.set([]));
    await openPanel();
    expect(q(".mp-empty")?.textContent).toContain(
      "No connection advertises a model yet.",
    );
    const search = q<HTMLInputElement>('[data-testid="mp-search"]')!;
    search.value = "zzz";
    search.dispatchEvent(new Event("input"));
    await fixture.whenStable();
    expect(q(".mp-empty")?.textContent).toContain(
      "No advertised model matches",
    );
  });
});
