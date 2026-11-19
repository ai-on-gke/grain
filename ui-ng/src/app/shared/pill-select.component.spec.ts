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

import { TestBed } from "@angular/core/testing";
import { type PillOption, PillSelectComponent } from "./pill-select.component";

describe("PillSelectComponent viewport menu positioning", () => {
  afterEach(() => TestBed.resetTestingModule());

  function mountWithRect(
    options: PillOption[],
    rect: Partial<DOMRect>,
    viewport = { width: 1024, height: 860 },
  ) {
    Object.defineProperty(window, "innerWidth", {
      value: viewport.width,
      configurable: true,
    });
    Object.defineProperty(window, "innerHeight", {
      value: viewport.height,
      configurable: true,
    });
    const fixture = TestBed.createComponent(PillSelectComponent);
    fixture.componentRef.setInput("options", options);
    fixture.componentRef.setInput("testid", "test-pill");
    fixture.componentRef.setInput("align", "left");
    fixture.detectChanges();
    const btn = fixture.nativeElement.querySelector(
      '[data-testid="test-pill"]',
    ) as HTMLButtonElement;
    btn.getBoundingClientRect = () =>
      ({
        x: 595,
        y: 533,
        left: 595,
        top: 533,
        right: 715,
        bottom: 569,
        width: 120,
        height: 36,
        toJSON: () => ({}),
        ...rect,
      }) as DOMRect;
    btn.click();
    fixture.detectChanges();
    return fixture.nativeElement.querySelector(
      '[data-testid="test-pill-menu"]',
    ) as HTMLElement;
  }

  it("flips tall menus upward when space below is smaller than the menu height", () => {
    const codexOptions: PillOption[] = [
      { value: "", label: "Model default" },
      ...["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"].map(
        (e) => ({ value: e, label: e }),
      ),
    ];
    // 1024x860: button bottom=569 leaves 279px below, 9 options need ~322px, 521px above.
    const menu = mountWithRect(codexOptions, { top: 533, bottom: 569 });
    expect(menu.classList.contains("open-up")).toBe(true);
  });

  it("keeps menus opening downward when space below fits the options", () => {
    const shortOptions: PillOption[] = [
      { value: "", label: "Model default" },
      { value: "low", label: "low" },
      { value: "high", label: "high" },
    ];
    const menu = mountWithRect(shortOptions, { top: 533, bottom: 569 });
    expect(menu.classList.contains("open-up")).toBe(false);
  });

  it("stays downward when near the top of the viewport with more room below than above", () => {
    const codexOptions: PillOption[] = [
      { value: "", label: "Model default" },
      ...["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"].map(
        (e) => ({ value: e, label: e }),
      ),
    ];
    const menu = mountWithRect(
      codexOptions,
      { top: 100, bottom: 136 },
      { width: 390, height: 300 },
    );
    expect(menu.classList.contains("open-up")).toBe(false);
  });
});
