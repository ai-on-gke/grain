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
import { DirectorContextService } from "./director-context.service";

describe("DirectorContextService", () => {
  beforeEach(() => TestBed.resetTestingModule());

  it("derives route context when no item selection is set and lets selection override it", () => {
    const svc = TestBed.inject(DirectorContextService);
    svc.setRoute("/workstreams/task%3A42?from=tasks");
    expect(svc.activeContext()).toEqual({
      kind: "workstream",
      id: "task:42",
    });
    expect(svc.contextLine()).toBe("Workstream task:42");

    svc.setSelection({
      kind: "task",
      id: "99",
      title: "Fix retry loop",
      state: "running",
      repo: "acme/web",
    });
    expect(svc.contextLine()).toBe(
      "Task #99: Fix retry loop (running · acme/web)",
    );

    expect(svc.formatMessageWithContext("Please check why it stalls.")).toBe(
      "> [Context: Task #99: Fix retry loop (running · acme/web)]\n\nPlease check why it stalls.",
    );

    svc.includeContext.set(false);
    expect(svc.formatMessageWithContext("Plain question")).toBe(
      "Plain question",
    );

    svc.clearSelection("inbox");
    expect(svc.selection()).not.toBeNull();
    svc.clearSelection("task");
    expect(svc.selection()).toBeNull();

    svc.setRoute("/workstreams/issue:acme/web#42?node=55");
    expect(svc.activeContext()).toEqual({
      kind: "workstream",
      id: "issue:acme/web#42",
    });
    svc.setRoute("/workstreams/issue%3Aacme%2Fweb%2342?pane=changes");
    expect(svc.activeContext()).toEqual({
      kind: "workstream",
      id: "issue:acme/web#42",
    });

    svc.setRoute("/workstreams#issue%3Aacme%2Fweb%2399");
    expect(svc.activeContext()).toEqual({
      kind: "workstream",
      id: "issue:acme/web#99",
    });

    svc.setRoute("/workstreams/%E0%A4%A");
    expect(svc.activeContext()).toEqual({
      kind: "workstream",
      id: "%E0%A4%A",
    });
  });

  it("toggles drawer state and restores focus to opener", async () => {
    const svc = TestBed.inject(DirectorContextService);
    const btn = document.createElement("button");
    document.body.appendChild(btn);
    btn.focus();
    const other = document.createElement("div");
    other.tabIndex = 0;
    document.body.appendChild(other);

    svc.toggle(btn);
    expect(svc.isOpen()).toBe(true);
    other.focus();

    svc.toggle();
    expect(svc.isOpen()).toBe(false);
    await Promise.resolve();
    expect(document.activeElement).toBe(btn);
    btn.remove();
    other.remove();
  });
});
