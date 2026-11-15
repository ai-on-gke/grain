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
import { Router, provideRouter } from "@angular/router";
import { firstValueFrom, of } from "rxjs";
import {
  NAV_PROGRESS_DELAY_MS,
  NavProgressComponent,
} from "./nav-progress.component";
import { IdleRoutePreload, ROUTE_PRELOAD_DELAY_MS } from "./route-preload";

@Component({ template: "page" })
class PageComponent {}

describe("NavProgressComponent", () => {
  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  it("shows while a lazy page's chunk is still loading and hides once it lands", async () => {
    vi.useFakeTimers();
    let release!: () => void;
    const chunk = new Promise<typeof PageComponent>((resolve) => {
      release = () => resolve(PageComponent);
    });
    TestBed.configureTestingModule({
      imports: [NavProgressComponent],
      providers: [
        provideRouter([
          { path: "", component: PageComponent },
          { path: "slow", loadComponent: () => chunk },
        ]),
      ],
    });
    const f = TestBed.createComponent(NavProgressComponent);
    f.autoDetectChanges();
    const el = f.nativeElement as HTMLElement;
    const router = TestBed.inject(Router);

    const nav = router.navigateByUrl("/slow");
    await vi.advanceTimersByTimeAsync(NAV_PROGRESS_DELAY_MS - 20);
    f.detectChanges();
    // A fast navigation never flashes the bar.
    expect(el.querySelector("[data-testid=nav-progress]")).toBeNull();

    await vi.advanceTimersByTimeAsync(40);
    f.detectChanges();
    const bar = el.querySelector<HTMLElement>("[data-testid=nav-progress]");
    expect(bar).not.toBeNull();
    expect(bar!.getAttribute("role")).toBe("progressbar");

    release();
    await vi.advanceTimersByTimeAsync(0);
    expect(await nav).toBe(true);
    f.detectChanges();
    expect(el.querySelector("[data-testid=nav-progress]")).toBeNull();
  });

  it("hides when the navigation fails", async () => {
    vi.useFakeTimers();
    let fail!: () => void;
    const chunk = new Promise<typeof PageComponent>((_, reject) => {
      fail = () => reject(new Error("chunk failed"));
    });
    TestBed.configureTestingModule({
      imports: [NavProgressComponent],
      providers: [
        provideRouter([{ path: "broken", loadComponent: () => chunk }]),
      ],
    });
    const f = TestBed.createComponent(NavProgressComponent);
    f.autoDetectChanges();
    const el = f.nativeElement as HTMLElement;
    const nav = TestBed.inject(Router)
      .navigateByUrl("/broken")
      .catch(() => false);
    await vi.advanceTimersByTimeAsync(NAV_PROGRESS_DELAY_MS + 10);
    f.detectChanges();
    expect(el.querySelector("[data-testid=nav-progress]")).not.toBeNull();
    fail();
    await nav;
    f.detectChanges();
    expect(el.querySelector("[data-testid=nav-progress]")).toBeNull();
  });
});

describe("IdleRoutePreload", () => {
  afterEach(() => vi.useRealTimers());

  it("loads a route's chunk after the start-up delay, not at once", async () => {
    vi.useFakeTimers();
    const load = vi.fn(() => of("loaded"));
    const done = firstValueFrom(new IdleRoutePreload().preload({}, load));
    await vi.advanceTimersByTimeAsync(ROUTE_PRELOAD_DELAY_MS - 1);
    expect(load).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(load).toHaveBeenCalledTimes(1);
    expect(await done).toBe("loaded");
  });
});
