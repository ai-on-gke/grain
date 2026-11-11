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
import { KeyboardService, isTypingTarget } from "./keyboard.service";

function press(key: string, target: EventTarget = document.body, init = {}) {
  const ev = new KeyboardEvent("keydown", {
    key,
    bubbles: true,
    cancelable: true,
    ...init,
  });
  target.dispatchEvent(ev);
  return ev;
}

describe("KeyboardService", () => {
  let kb: KeyboardService;
  beforeEach(() => {
    document.body.innerHTML = "";
    kb = TestBed.inject(KeyboardService);
  });
  afterEach(() => TestBed.resetTestingModule());

  it("runs a registered shortcut and stops after it is removed", () => {
    const run = vi.fn();
    const off = kb.register({ key: "c", description: "New task", run });
    const ev = press("c");
    expect(run).toHaveBeenCalledTimes(1);
    expect(ev.defaultPrevented).toBe(true);
    expect(kb.list().map((s) => s.key)).toEqual(["c"]);
    off();
    press("c");
    expect(run).toHaveBeenCalledTimes(1);
    expect(kb.list()).toEqual([]);
  });

  it("ignores keys typed into fields, with modifiers, or under a modal", () => {
    const run = vi.fn();
    kb.register({ key: "c", description: "New task", run });
    const input = document.createElement("input");
    document.body.appendChild(input);
    press("c", input);
    press("c", document.body, { ctrlKey: true });
    press("c", document.body, { metaKey: true });
    const dialog = document.createElement("div");
    dialog.setAttribute("aria-modal", "true");
    document.body.appendChild(dialog);
    press("c");
    expect(run).not.toHaveBeenCalled();
    dialog.remove();
    press("c");
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("restores an earlier shortcut when a later one for the same key is removed", () => {
    const first = vi.fn();
    const second = vi.fn();
    kb.register({ key: "1", description: "first", run: first });
    const off = kb.register({ key: "1", description: "second", run: second });
    press("1");
    expect(second).toHaveBeenCalledTimes(1);
    off();
    press("1");
    expect(first).toHaveBeenCalledTimes(1);
  });

  it("treats inputs, textareas, selects and editable content as typing", () => {
    for (const tag of ["input", "textarea", "select"]) {
      expect(isTypingTarget(document.createElement(tag))).toBe(true);
    }
    const div = document.createElement("div");
    expect(isTypingTarget(div)).toBe(false);
    Object.defineProperty(div, "isContentEditable", { value: true });
    expect(isTypingTarget(div)).toBe(true);
    expect(isTypingTarget(null)).toBe(false);
  });
});
