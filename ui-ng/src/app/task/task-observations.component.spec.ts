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

import { vi } from "vitest";
import { TestBed } from "@angular/core/testing";
import { TaskObservationsComponent } from "./task-observations.component";
import type { GateObservations } from "../api/models";

const view = (): GateObservations => ({
  mode: "observation_only",
  authority: "legacy",
  source_revision: "v1",
  compared_at: "2026-11-17T00:00:00Z",
  legacy_state: "awaiting_reply",
  dirty: false,
  bindings: [
    {
      id: "report",
      type: "delivery",
      version: 1,
      config: { product: "report", semantics: "current_v1" },
      origin: { kind: "task", revision: "v1" },
    },
  ],
  results: [
    {
      evaluation_id: "shadow-1",
      revision: 1,
      disposition: "accepted",
      invalidation_reason: null,
      retry_at: null,
      result: {
        type: "delivery",
        version: 1,
        status: "met",
        summary: "Durable report delivered.",
        human_text: null,
        observed_at: "2026-11-17T00:00:00Z",
        valid_until: "2026-11-17T00:01:00Z",
        context_digest: "sha256:context",
        evidence: [{ kind: "report", id: "42", version: "r1" }],
        dependencies: [{ key: "task/1/report", version: "v1", complete: true }],
        waits: [],
        error: null,
      },
    },
  ],
});

describe("observation-only task display", () => {
  it("marks a met observation stale when time passes without a changed response", async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date("2026-11-17T00:00:00Z"));
      await TestBed.configureTestingModule({
        imports: [TaskObservationsComponent],
      }).compileComponents();
      const fixture = TestBed.createComponent(TaskObservationsComponent);
      const data = view();
      data.results[0].result.valid_until = "2026-11-17T00:00:02Z";
      fixture.componentRef.setInput("observations", data);
      fixture.detectChanges();
      expect(fixture.nativeElement.textContent).not.toContain(
        "Stale observation",
      );
      vi.advanceTimersByTime(3000);
      fixture.detectChanges();
      expect(fixture.nativeElement.textContent).toContain("Stale observation");
      fixture.destroy();
    } finally {
      vi.useRealTimers();
    }
  });

  it("renders provenance, staleness and observations without operational controls", async () => {
    await TestBed.configureTestingModule({
      imports: [TaskObservationsComponent],
    }).compileComponents();
    const fixture = TestBed.createComponent(TaskObservationsComponent);
    fixture.componentRef.setInput("observations", view());
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    expect(el.textContent).toContain("Observation only");
    expect(el.textContent).toContain("do not establish readiness");
    expect(el.textContent).toContain("delivery v1 · met");
    expect(el.textContent).toContain("Stale observation");
    expect(el.textContent).toContain("report #42");
    expect(el.textContent).toContain('"product": "report"');
    expect(el.querySelectorAll("button,a,input").length).toBe(0);
  });
  it("renders collection errors and pending empty results explicitly", async () => {
    await TestBed.configureTestingModule({
      imports: [TaskObservationsComponent],
    }).compileComponents();
    const fixture = TestBed.createComponent(TaskObservationsComponent);
    const data = view();
    data.dirty = true;
    data.results[0].result.status = "error";
    data.results[0].result.error = {
      code: "unavailable",
      message: "Collection failed",
    };
    data.results[0].disposition = "rejected";
    fixture.componentRef.setInput("observations", data);
    fixture.detectChanges();
    expect(fixture.nativeElement.textContent).toContain(
      "Observation error: unavailable",
    );
    expect(fixture.nativeElement.textContent).toContain("collection is dirty");
    fixture.componentRef.setInput("observations", { ...data, results: [] });
    fixture.detectChanges();
    expect(fixture.nativeElement.textContent).toContain(
      "pending evidence collection",
    );
  });
  it("labels selected outcomes and preserves every CL evidence row on refresh", async () => {
    await TestBed.configureTestingModule({
      imports: [TaskObservationsComponent],
    }).compileComponents();
    const fixture = TestBed.createComponent(TaskObservationsComponent);
    const data = view();
    data.bindings[0].type = "integration";
    data.bindings[0].config = {
      kind: "cl_submitted",
      semantics: "positive_v1",
    };
    data.results[0].result.type = "integration";
    data.results[0].result.evidence = [
      {
        kind: "integration",
        id: "cl/11=>cl/111 (external)",
        version: "r11",
        head: "snapshot/3",
      },
      { kind: "integration", id: "cl/12", version: "r12", head: "snapshot/4" },
    ];
    fixture.componentRef.setInput("observations", data);
    fixture.detectChanges();
    const text = () => fixture.nativeElement.textContent as string;
    expect(text()).toContain("shadow cl_submitted");
    expect(text()).toContain("cl/11=>cl/111 (external)");
    expect(text()).toContain("cl/12");
    expect(text()).toContain("head snapshot/4");
    data.results[0].result.evidence[1].version = "r12-new";
    data.results[0].result.evidence[1].head = "snapshot/5";
    fixture.componentRef.setInput("observations", structuredClone(data));
    fixture.detectChanges();
    expect(text()).toContain("head snapshot/5");
    expect(
      fixture.nativeElement.querySelectorAll("button,a,input").length,
    ).toBe(0);
    fixture.destroy();
  });
  it("does not show an unavailable projection as a passing gate", async () => {
    await TestBed.configureTestingModule({
      imports: [TaskObservationsComponent],
    }).compileComponents();
    const fixture = TestBed.createComponent(TaskObservationsComponent);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector("section")).toBeNull();
  });
});
