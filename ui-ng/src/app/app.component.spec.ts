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

import { APP_BASE_HREF } from "@angular/common";
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
      provideRouter([
        { path: "inbox", children: [] },
        { path: "sessions", children: [] },
      ]),
      { provide: APP_BASE_HREF, useValue: "/next/" },
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
    expect(inbox.getAttribute("href")).toBe("/next/inbox");
    const ci = el(f).querySelector('[data-testid="rail-ci"]')!;
    expect(ci.getAttribute("href")).toBe("/ci");
  });

  it("opens the create disclosure, and Escape closes it back onto its button", async () => {
    const f = await render();
    const btn = el(f).querySelector(
      '[data-testid="rail-create-menu"]',
    ) as HTMLButtonElement;
    btn.click();
    await f.whenStable();
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect(btn.hasAttribute("aria-haspopup")).toBe(false);
    const links = [
      ...el(f).querySelectorAll<HTMLAnchorElement>(
        '[data-testid="create-menu"] a',
      ),
    ];
    expect(links.map((e) => e.textContent?.trim())).toEqual([
      "New task",
      "New session",
    ]);
    links[1].focus();
    links[1].dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Escape",
        bubbles: true,
        cancelable: true,
      }),
    );
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="create-menu"]')).toBeNull();
    expect(document.activeElement).toBe(btn);
  });

  it("closes the create menu on a click outside it", async () => {
    const f = await render();
    const btn = el(f).querySelector(
      '[data-testid="rail-create-menu"]',
    ) as HTMLButtonElement;
    btn.click();
    await f.whenStable();
    document.body.click();
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="create-menu"]')).toBeNull();
  });

  it("the phone More sheet lists the remaining destinations and New session", async () => {
    const f = await render();
    const more = el(f).querySelector(
      '[data-testid="compact-more"]',
    ) as HTMLButtonElement;
    more.click();
    await f.whenStable();
    expect(more.getAttribute("aria-expanded")).toBe("true");
    const text = el(f).querySelector('[data-testid="more-sheet"]')!.textContent;
    expect(text).toContain("New session");
    expect(text).toContain(SETTINGS.label);
    more.click();
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="more-sheet"]')).toBeNull();
  });

  it("the footer switch turns single-key shortcuts off", async () => {
    const f = await render();
    const toggle = el(f).querySelector(
      '[data-testid="shortcuts-toggle"]',
    ) as HTMLButtonElement;
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    toggle.click();
    await f.whenStable();
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    const open = vi.spyOn(f.componentInstance, "open");
    press("1");
    expect(open).not.toHaveBeenCalled();
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

  it("hides the phone FAB on Sessions, where the composer sits", async () => {
    const f = await render();
    const router = TestBed.inject(Router);
    await router.navigateByUrl("/inbox");
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="phone-new-task"]')).toBeTruthy();
    await router.navigateByUrl("/sessions");
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="phone-new-task"]')).toBeNull();
  });
});
