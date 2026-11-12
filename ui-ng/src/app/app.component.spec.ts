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
import type { GrainConfig, Task, WhoamiResponse } from "./api/models";
import { AppComponent } from "./app.component";
import { CreateTaskService } from "./create/create-task.service";
import {
  SIDEBAR_HIDDEN_KEY,
  SIDEBAR_ORDER_KEY,
} from "./services/nav-config.service";
import { HelpService } from "./services/help.service";
import { SnackbarService } from "./services/snackbar.service";
import { TaskFeedStore } from "./services/task-feed.service";
import { ThemeService } from "./services/theme.service";
import { CHLADNI_ROSETTE_PATH } from "./shared/brand-mark";
import { AccountPopoverComponent } from "./shell/account-popover.component";
import { DESTINATIONS, SETTINGS } from "./shell/nav";

class FakeShellApi {
  configBody: GrainConfig = {
    version: "v3.5.0-test",
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
  };
  tasksList: Task[] = [];
  whoamiBody: WhoamiResponse = {
    authenticated: true,
    source: "oidc",
    verified: true,
    actor: { kind: "human", id: "alice@example.com", label: "Alice Example" },
  };
  logoutRedirect = "/signed-out";
  logoutCalls = 0;

  config = () => Promise.resolve(this.configBody);
  tasks = () => Promise.resolve(this.tasksList);
  getIfChanged = (route: string) => {
    if (route === "GET /api/tasks") {
      return Promise.resolve({
        body: structuredClone(this.tasksList),
        changed: true,
      });
    }
    return Promise.resolve({ body: null, changed: false });
  };
  openNotifications = () => Promise.resolve([]);
  localScriptApprovals = () => Promise.resolve([]);
  whoami = () => Promise.resolve(this.whoamiBody);
  logout = () => {
    this.logoutCalls++;
    return Promise.resolve({ ok: true, redirect: this.logoutRedirect });
  };
}

async function render(
  api = new FakeShellApi(),
): Promise<{ fixture: ComponentFixture<AppComponent>; api: FakeShellApi }> {
  localStorage.clear();
  TestBed.configureTestingModule({
    imports: [AppComponent],
    providers: [
      provideRouter([
        { path: "inbox", children: [] },
        { path: "workstreams", children: [] },
        { path: "tasks", children: [] },
        { path: "agents", children: [] },
        { path: "sessions", children: [] },
        { path: "processes", children: [] },
        { path: "processes/:tab", children: [] },
        { path: "ci", children: [] },
        { path: "telemetry", children: [] },
        { path: "context", children: [] },
        { path: "fleet", children: [] },
        { path: "settings", children: [] },
      ]),
      { provide: APP_BASE_HREF, useValue: "/next/" },
      { provide: GrainApi, useValue: api },
      { provide: GrainEvents, useValue: { events: () => NEVER } },
    ],
  });
  const fixture = TestBed.createComponent(AppComponent);
  fixture.autoDetectChanges();
  await fixture.whenStable();
  await new Promise((r) => setTimeout(r, 5));
  await fixture.whenStable();
  return { fixture, api };
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
  afterEach(() => {
    document
      .querySelectorAll("#grain-favicon-svg, #grain-favicon-png")
      .forEach((n) => n.remove());
    TestBed.resetTestingModule();
  });

  it("renders authentic Chladni brand mark first in rail, then New task FAB, visible destinations, More, and Settings last", async () => {
    const { fixture: f } = await render();
    const rail = el(f).querySelector(".nav-rail")!;
    const brand = rail.querySelector('[data-testid="brand-mark"]')!;
    const fab = rail.querySelector('[data-testid="rail-new-task"]')!;
    expect(rail.firstElementChild).toBe(brand);
    expect(brand.getAttribute("href")).toBe("/next/inbox");
    const brandPath = brand.querySelector("svg path")!;
    expect(brandPath.getAttribute("d")).toBe(CHLADNI_ROSETTE_PATH);
    expect(brand.nextElementSibling).toBe(fab);
    expect(fab.getAttribute("aria-label")).toBe("New task");

    const mobilePath = el(f).querySelector(".mobile-brand-mark path")!;
    expect(mobilePath.getAttribute("d")).toBe(CHLADNI_ROSETTE_PATH);

    const labels = [...rail.querySelectorAll(".rail-label")].map((e) =>
      e.textContent?.trim(),
    );
    expect(labels).toEqual([
      ...DESTINATIONS.map((d) => d.label),
      "More",
      SETTINGS.label,
    ]);
    expect(labels.indexOf("Workstreams")).toBe(1);
    expect(labels.indexOf("Tasks")).toBe(2);
  });

  it("switches the brand mark to the animated sprite sheet when agents are running", async () => {
    const api = new FakeShellApi();
    const { fixture: f } = await render(api);
    expect(
      el(f).querySelector('[data-testid="brand-mark-animated"]'),
    ).toBeNull();

    api.tasksList = [{ id: "11", title: "Build UI", state: "running" }];
    await TestBed.inject(TaskFeedStore).refresh();
    await f.whenStable();
    expect(
      el(f).querySelector('[data-testid="brand-mark-animated"]'),
    ).not.toBeNull();
  });

  it("routes built pages (including Settings) inside the app and omits Classic UI escape links", async () => {
    const { fixture: f } = await render();
    const inbox = el(f).querySelector('[data-testid="rail-inbox"]')!;
    expect(inbox.getAttribute("href")).toBe("/next/inbox");
    const processes = el(f).querySelector('[data-testid="rail-processes"]')!;
    expect(processes.getAttribute("href")).toBe("/next/processes");
    const telemetry = el(f).querySelector('[data-testid="rail-telemetry"]')!;
    expect(telemetry.getAttribute("href")).toBe("/next/telemetry");
    const context = el(f).querySelector('[data-testid="rail-context"]')!;
    expect(context.getAttribute("href")).toBe("/next/context");
    const fleet = el(f).querySelector('[data-testid="rail-fleet"]')!;
    expect(fleet.getAttribute("href")).toBe("/next/fleet");
    const settingsLink = el(f).querySelector('[data-testid="rail-settings"]')!;
    expect(settingsLink.getAttribute("href")).toBe("/next/settings");
    expect(el(f).querySelector(".next-chip")).toBeNull();
    expect(el(f).querySelector(".bottom-bar")?.textContent).not.toContain(
      "Classic UI",
    );
    const ci = el(f).querySelector('[data-testid="rail-ci"]')!;
    expect(ci.getAttribute("href")).toBe("/next/ci");
  });

  it("opens the 2-line rich create disclosure, and Escape closes it back onto its button", async () => {
    const { fixture: f } = await render();
    const btn = el(f).querySelector(
      '[data-testid="rail-create-menu"]',
    ) as HTMLButtonElement;
    btn.click();
    await f.whenStable();
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect(btn.hasAttribute("aria-haspopup")).toBe(false);
    const titles = [
      ...el(f).querySelectorAll<HTMLElement>(
        '[data-testid="create-menu"] .cm-title',
      ),
    ];
    const subs = [
      ...el(f).querySelectorAll<HTMLElement>(
        '[data-testid="create-menu"] .cm-sub',
      ),
    ];
    expect(titles.map((e) => e.textContent?.trim())).toEqual([
      "New task",
      "Start a workstream",
      "New session",
      "Task from template",
      "Run a suite",
      "New schedule",
      "New board",
    ]);
    expect(subs.length).toBe(7);
    expect(subs[0].textContent?.trim()).toContain("Describe work");

    const links = [
      ...el(f).querySelectorAll<HTMLElement>(
        '[data-testid="create-menu"] li > *',
      ),
    ];
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
    const { fixture: f } = await render();
    const btn = el(f).querySelector(
      '[data-testid="rail-create-menu"]',
    ) as HTMLButtonElement;
    btn.click();
    await f.whenStable();
    document.body.click();
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="create-menu"]')).toBeNull();
  });

  it("renders persistent NextUp, live-pill, / search keycap, and bottom factory telemetry in sync with TaskFeedStore", async () => {
    const api = new FakeShellApi();
    const { fixture: f } = await render(api);

    const nextUp = el(f).querySelector('[data-testid="next-up"]')!;
    expect(nextUp.classList.contains("is-empty")).toBe(true);

    const livePill = el(f).querySelector('[data-testid="live-pill"]')!;
    expect(livePill.classList.contains("is-idle")).toBe(true);
    expect(livePill.textContent).toContain("0 agents running");
    expect(livePill.getAttribute("href")).toBe("/next/agents");

    expect(el(f).querySelector('[data-testid="search-kbd"]')?.textContent).toBe(
      "/",
    );
    press("/");
    const searchInput = el(f).querySelector(
      '[data-testid="view-filter"]',
    ) as HTMLInputElement;
    expect(document.activeElement).toBe(searchInput);
    searchInput.value = "auth";
    searchInput.dispatchEvent(new Event("input"));
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="search-kbd"]')).toBeNull();

    // Blur input so global shortcuts work again, and clear filter.
    searchInput.value = "";
    searchInput.dispatchEvent(new Event("input"));
    searchInput.blur();

    api.tasksList = [
      { id: "21", title: "Run migration", state: "running" },
      { id: "22", title: "Audit IAM", state: "running" },
      { id: "23", title: "Queued follow-up", state: "queued", priority: 8 },
    ];
    await TestBed.inject(TaskFeedStore).refresh();
    await f.whenStable();

    expect(livePill.classList.contains("is-idle")).toBe(false);
    expect(livePill.textContent).toContain("2 agents running");
    expect(nextUp.classList.contains("is-empty")).toBe(false);
    expect(nextUp.textContent).toContain("#23");

    const telemetry = el(f).querySelector('[data-testid="factory-telemetry"]')!;
    expect(telemetry.textContent).toContain("2 running");
    expect(telemetry.textContent).toContain("Updated");
  });

  it("opens the 3-tab Help / Glossary / Shortcuts dialog via ? and switches tabs", async () => {
    const { fixture: f } = await render();
    await TestBed.inject(Router).navigateByUrl("/workstreams");
    await f.whenStable();

    press("?");
    await f.whenStable();
    await new Promise((r) => setTimeout(r, 10));
    await f.whenStable();

    const dialog = el(f).querySelector('[data-testid="help-dialog"]')!;
    expect(dialog).not.toBeNull();
    expect(dialog.textContent).toContain("About Workstreams");
    expect(
      dialog.querySelector('[role="tablist"]')?.getAttribute("aria-label"),
    ).toBe("Help sections");
    expect(
      dialog
        .querySelector('[data-testid="help-tab-about"]')
        ?.getAttribute("role"),
    ).toBe("tab");
    expect(
      dialog
        .querySelector('[data-testid="help-tab-about"]')
        ?.getAttribute("aria-selected"),
    ).toBe("true");
    expect(dialog.querySelector('[role="tabpanel"]')).not.toBeNull();

    el(f)
      .querySelector<HTMLButtonElement>('[data-testid="help-tab-glossary"]')!
      .click();
    await f.whenStable();
    const glossary = el(f).querySelector('[data-testid="help-glossary-list"]')!;
    expect(glossary.textContent).toContain("Workstreams");
    expect(glossary.textContent).toContain("Merge Gates");
    expect(glossary.textContent).toContain("Hostd");
    expect(glossary.textContent).toContain("Director");

    el(f)
      .querySelector<HTMLButtonElement>('[data-testid="help-tab-shortcuts"]')!
      .click();
    await f.whenStable();
    const shortcutsText =
      el(f).querySelector('[data-testid="help-shortcuts-list"]')?.textContent ??
      "";
    expect(shortcutsText).toContain("Open full task");
    expect(shortcutsText).toContain("Approve selected");
    const sw = el(f).querySelector<HTMLButtonElement>(
      '[data-testid="help-shortcuts-switch"]',
    )!;
    expect(sw.getAttribute("aria-checked")).toBe("true");
    sw.click();
    await f.whenStable();
    expect(sw.getAttribute("aria-checked")).toBe("false");

    // Even if a mounted page has a bubble-phase document:keydown Escape
    // handler calling preventDefault(), Help closes cleanly in capture phase.
    const pageBubble = (ev: KeyboardEvent) => {
      if (ev.key === "Escape" && !ev.defaultPrevented) ev.preventDefault();
    };
    document.addEventListener("keydown", pageBubble);
    try {
      press("Escape");
      await f.whenStable();
      expect(el(f).querySelector('[data-testid="help-dialog"]')).toBeNull();
    } finally {
      document.removeEventListener("keydown", pageBubble);
    }
  });

  it("opens Account avatar popover with identity/role/scopes and posts logout", async () => {
    const { fixture: f, api } = await render();
    const avatar = el(f).querySelector<HTMLButtonElement>(
      '[data-testid="avatar-btn"]',
    )!;
    expect(avatar.textContent?.trim()).toBe("AE");

    avatar.click();
    await f.whenStable();
    await new Promise((r) => setTimeout(r, 10));
    await f.whenStable();

    const pop = el(f).querySelector('[data-testid="account-popover"]')!;
    expect(pop).not.toBeNull();
    expect(
      pop
        .querySelector('[data-testid="account-identity"]')
        ?.textContent?.trim(),
    ).toBe("Alice Example");
    expect(
      pop.querySelector('[data-testid="account-role"]')?.textContent?.trim(),
    ).toBe("grain/operator");
    expect(
      pop.querySelector('[data-testid="account-scopes-summary"]')?.textContent,
    ).toContain("Full operator access");

    const navSpy = vi
      .spyOn(AccountPopoverComponent.prototype, "navigate")
      .mockImplementation(() => undefined);
    // While #account-popover is open, single-key shortcuts like "1" do not fire behind it,
    // while "0" toggles it closed and restores focus to the avatar button.
    const router = TestBed.inject(Router);
    const routerSpy = vi.spyOn(router, "navigateByUrl");
    press("1");
    expect(routerSpy).not.toHaveBeenCalled();

    press("0");
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="account-popover"]')).toBeNull();
    expect(document.activeElement).toBe(avatar);

    // Re-open with "0" and verify safe /next/ fallback when redirect is empty or protocol-relative.
    press("0");
    await f.whenStable();
    await new Promise((r) => setTimeout(r, 10));
    await f.whenStable();
    const pop2 = el(f).querySelector('[data-testid="account-popover"]')!;
    pop2
      .querySelector<HTMLButtonElement>('[data-testid="account-sign-out"]')!
      .click();
    await new Promise((r) => setTimeout(r, 10));
    await f.whenStable();
    expect(api.logoutCalls).toBe(1);
    expect(navSpy).toHaveBeenCalledWith("/signed-out");

    // Unsafe //evil.example or empty redirect falls back to /next/.
    api.logoutRedirect = "//evil.example/phish";
    avatar.click();
    await f.whenStable();
    avatar.click();
    await f.whenStable();
    await new Promise((r) => setTimeout(r, 10));
    await f.whenStable();
    el(f)
      .querySelector<HTMLButtonElement>('[data-testid="account-sign-out"]')!
      .click();
    await new Promise((r) => setTimeout(r, 10));
    await f.whenStable();
    expect(navSpy).toHaveBeenLastCalledWith("/next/");
    navSpy.mockRestore();
  });

  it("recovers from @defer chunk failure and stuck modalOpen without leaving the shell inert", async () => {
    const { fixture: f } = await render();
    const help = TestBed.inject(HelpService);
    const snacks = TestBed.inject(SnackbarService);
    const comp = f.componentInstance as unknown as {
      onDeferError(which: "create" | "help" | "account" | "customize"): void;
    };

    help.open("about");
    f.detectChanges();
    expect(el(f).querySelector("#main")?.hasAttribute("inert")).toBe(true);
    comp.onDeferError("help");
    await f.whenStable();
    expect(help.isOpen()).toBe(false);
    expect(el(f).querySelector("#main")?.hasAttribute("inert")).toBe(false);
    expect(snacks.current()?.message).toContain("Help could not load");

    // Safety net: if modalOpen() is true before any [aria-modal="true"] mounts, Escape clears it.
    TestBed.inject(CreateTaskService).open();
    f.detectChanges();
    document
      .querySelectorAll('[aria-modal="true"]')
      .forEach((n) => n.removeAttribute("aria-modal"));
    press("Escape");
    await f.whenStable();
    expect(TestBed.inject(CreateTaskService).isOpen()).toBe(false);
    expect(el(f).querySelector("#main")?.hasAttribute("inert")).toBe(false);
  });

  it("customizes rail destinations (hide/reorder/reset) persisted in localStorage while keeping Inbox first and Settings last", async () => {
    const { fixture: f } = await render();
    el(f)
      .querySelector<HTMLButtonElement>('[data-testid="rail-more"]')!
      .click();
    await f.whenStable();
    el(f)
      .querySelector<HTMLButtonElement>('[data-testid="rail-customize-nav"]')!
      .click();
    await f.whenStable();
    await new Promise((r) => setTimeout(r, 10));
    await f.whenStable();

    const dlg = el(f).querySelector('[data-testid="customize-nav-dialog"]')!;
    expect(dlg).not.toBeNull();
    // Inbox has no toggle or move buttons ("Always first").
    expect(dlg.querySelector('[data-testid="nav-toggle-inbox"]')).toBeNull();

    // Move Tasks above Workstreams (so Tasks becomes #2 on rail) and hide Fleet.
    dlg
      .querySelector<HTMLButtonElement>('[data-testid="nav-move-up-tasks"]')!
      .click();
    dlg
      .querySelector<HTMLButtonElement>('[data-testid="nav-toggle-fleet"]')!
      .click();
    dlg.querySelector<HTMLButtonElement>('[data-testid="nav-save"]')!.click();
    await f.whenStable();

    expect(JSON.parse(localStorage.getItem(SIDEBAR_HIDDEN_KEY)!)).toEqual([
      "fleet",
    ]);
    const savedOrder = JSON.parse(
      localStorage.getItem(SIDEBAR_ORDER_KEY)!,
    ) as string[];
    expect(savedOrder[0]).toBe("inbox");
    expect(savedOrder[1]).toBe("tasks");
    expect(savedOrder[2]).toBe("workstreams");

    const rail = el(f).querySelector(".nav-rail")!;
    expect(rail.querySelector('[data-testid="rail-fleet"]')).toBeNull();
    expect(
      rail.querySelector('[data-testid="rail-hidden-count"]')?.textContent,
    ).toBe("1");

    // Key "2" now navigates to Tasks instead of Workstreams!
    const router = TestBed.inject(Router);
    const nav = vi.spyOn(router, "navigateByUrl").mockResolvedValue(true);
    press("2");
    expect(nav).toHaveBeenCalledWith("/tasks");
  });

  it("swaps #grain-favicon-svg dynamically when ThemeService polarity changes", async () => {
    const link = document.createElement("link");
    link.id = "grain-favicon-svg";
    link.rel = "icon";
    link.href = "grain-favicon-light.svg";
    document.head.appendChild(link);

    await render();
    const theme = TestBed.inject(ThemeService);
    theme.setMode("dark");
    TestBed.flushEffects();
    expect(link.getAttribute("href")).toBe("grain-favicon-dark.svg");
    theme.setMode("light");
    TestBed.flushEffects();
    expect(link.getAttribute("href")).toBe("grain-favicon-light.svg");
  });

  it("the phone More sheet lists remaining destinations, New session, and Customize navigation", async () => {
    const { fixture: f } = await render();
    const more = el(f).querySelector(
      '[data-testid="compact-more"]',
    ) as HTMLButtonElement;
    more.click();
    await f.whenStable();
    expect(more.getAttribute("aria-expanded")).toBe("true");
    const text = el(f).querySelector('[data-testid="more-sheet"]')!.textContent;
    expect(text).toContain("New session");
    expect(text).toContain("Customize navigation");
    expect(text).toContain(SETTINGS.label);
    more.click();
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="more-sheet"]')).toBeNull();
  });

  it("the footer switch turns single-key shortcuts off", async () => {
    const { fixture: f } = await render();
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
    const { fixture: f } = await render();
    const router = TestBed.inject(Router);
    const nav = vi.spyOn(router, "navigateByUrl").mockResolvedValue(true);
    const comp = f.componentInstance;
    const open = vi.spyOn(comp, "open");
    press("1");
    expect(open).toHaveBeenCalledWith(DESTINATIONS[0]);
    expect(nav).toHaveBeenCalledWith("/inbox");
    press(",");
    expect(open).toHaveBeenLastCalledWith(SETTINGS);
    expect(nav).toHaveBeenLastCalledWith("/settings");
  });

  it("hides the phone FAB on Sessions, where the composer sits", async () => {
    const { fixture: f } = await render();
    const router = TestBed.inject(Router);
    await router.navigateByUrl("/inbox");
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="phone-new-task"]')).toBeTruthy();
    await router.navigateByUrl("/sessions");
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="phone-new-task"]')).toBeNull();
  });

  it("opens the New task sheet from the rail FAB and from c", async () => {
    const { fixture: f } = await render();
    const fab = el(f).querySelector(
      '[data-testid="rail-new-task"]',
    ) as HTMLButtonElement;
    fab.click();
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="new-task-sheet"]')).toBeTruthy();
    TestBed.inject(CreateTaskService).close();
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="new-task-sheet"]')).toBeNull();
    press("c");
    await f.whenStable();
    expect(el(f).querySelector('[data-testid="new-task-sheet"]')).toBeTruthy();
    TestBed.inject(CreateTaskService).close();
  });
});
