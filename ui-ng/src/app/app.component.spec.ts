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

import { TestBed, type ComponentFixture } from "@angular/core/testing";
import { Router, provideRouter } from "@angular/router";
import { NEVER } from "rxjs";
import { GrainApi } from "./api/grain-api.service";
import { GrainEvents } from "./api/grain-events.service";
import { AppComponent } from "./app.component";
import { DESTINATIONS, SETTINGS } from "./shell/nav";

const fakeApi = {
  config: () => Promise.resolve({ access: null }),
  tasks: () => Promise.resolve([]),
  openNotifications: () => Promise.resolve([]),
  localScriptApprovals: () => Promise.resolve([]),
};

async function render(): Promise<ComponentFixture<AppComponent>> {
  localStorage.clear();
  TestBed.configureTestingModule({
    imports: [AppComponent],
    providers: [
      provideRouter([{ path: "inbox", children: [] }]),
      { provide: GrainApi, useValue: fakeApi },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
    ],
  });
  const fixture = TestBed.createComponent(AppComponent);
  fixture.autoDetectChanges();
  await fixture.whenStable();
  return fixture;
}

function el(f: ComponentFixture<unknown>): HTMLElement {
  return f.nativeElement as HTMLElement;
}

function press(key: string) {
  document.body.dispatchEvent(
    new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }),
  );
}

describe("AppComponent shell", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("puts New task first, then the destinations in order, and Settings last", async () => {
    const f = await render();
    const rail = el(f).querySelector(".nav-rail")!;
    const fab = rail.querySelector('[data-testid="rail-new-task"]')!;
    expect(rail.firstElementChild).toBe(fab);
    expect(fab.getAttribute("aria-label")).toBe("New task");
    const labels = [...rail.querySelectorAll(".rail-label")].map((e) =>
      e.textContent?.trim(),
    );
    expect(labels).toEqual([
      ...DESTINATIONS.map((d) => d.label),
      SETTINGS.label,
    ]);
  });

  it("routes built pages inside the app and links the rest to the classic UI", async () => {
    const f = await render();
    const inbox = el(f).querySelector('[data-testid="rail-inbox"]')!;
    expect(inbox.getAttribute("href")).toBe("/inbox");
    const ci = el(f).querySelector('[data-testid="rail-ci"]')!;
    expect(ci.getAttribute("href")).toBe("/ci");
  });

  it("opens and closes the create menu", async () => {
    const f = await render();
    const btn = el(f).querySelector(
      '[data-testid="rail-create-menu"]',
    ) as HTMLButtonElement;
    btn.click();
    await f.whenStable();
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    const items = [
      ...el(f).querySelectorAll(
        '[data-testid="create-menu"] [role="menuitem"]',
      ),
    ].map((e) => e.textContent?.trim());
    expect(items).toEqual(["New task", "New session"]);
    document.body.click();
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="create-menu"]')).toBeNull();
  });

  it("number keys follow the rail; built pages navigate inside the app", async () => {
    const f = await render();
    const router = TestBed.inject(Router);
    const nav = vi.spyOn(router, "navigateByUrl").mockResolvedValue(true);
    const comp = f.componentInstance;
    const open = vi.spyOn(comp, "open");
    press("1");
    expect(open).toHaveBeenCalledWith(DESTINATIONS[0]);
    expect(nav).toHaveBeenCalledWith("/inbox");
    // Settings is still a classic page: stub the full navigation jsdom
    // cannot do.
    open.mockImplementation(() => undefined);
    press(",");
    expect(open).toHaveBeenLastCalledWith(SETTINGS);
  });
});
