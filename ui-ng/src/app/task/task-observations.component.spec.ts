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
  it("does not show an unavailable projection as a passing gate", async () => {
    await TestBed.configureTestingModule({
      imports: [TaskObservationsComponent],
    }).compileComponents();
    const fixture = TestBed.createComponent(TaskObservationsComponent);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector("section")).toBeNull();
  });
});

it("labels current and legacy CI predictions with their resource and keeps remediation inert", async () => {
  const fixture = TestBed.createComponent(TaskObservationsComponent);
  const v = view();
  v.bindings = [
    {
      id: "checks",
      type: "checks",
      version: 1,
      config: {},
      origin: { imported: false },
      subject: {
        kind: "change",
        repository: "acme/widgets",
        change_kind: "pr",
        id: "7",
      },
    },
    {
      id: "checks-legacy",
      type: "checks",
      version: 1,
      config: {},
      origin: { imported: true },
      subject: {
        kind: "change",
        repository: "acme/widgets",
        change_kind: "pr",
        id: "7",
      },
    },
  ];
  const current = v.results[0];
  current.result.type = "checks";
  current.result.binding_id = "task/66/checks";
  current.result.agent_text =
    "Prediction only: retry checks under existing authorization.";
  current.result.human_text =
    "Merge conflicts remain an executor precondition.";
  const legacy = structuredClone(current);
  legacy.evaluation_id = "shadow-legacy";
  legacy.result.binding_id = "task/66/checks-legacy";
  legacy.result.status = "pending";
  v.results = [current, legacy];
  fixture.componentRef.setInput("observations", v);
  fixture.detectChanges();
  const el: HTMLElement = fixture.nativeElement;
  expect(el.textContent).toContain(
    "Shadow CI prediction · Current CI policy · acme/widgets · pr 7",
  );
  expect(el.textContent).toContain("Shadow CI prediction · Legacy CI import");
  expect(el.textContent).toContain("Prediction only: retry checks");
  expect(el.textContent).toContain(
    "Merge conflicts remain an executor precondition.",
  );
  expect(el.querySelectorAll("button,a,input").length).toBe(0);
});

describe("review and approval shadow scopes", () => {
  it("separates legacy comparison from current predictions and leaves proposals inert", async () => {
    await TestBed.configureTestingModule({
      imports: [TaskObservationsComponent],
    }).compileComponents();
    const fixture = TestBed.createComponent(TaskObservationsComponent);
    const data = view();
    data.bindings[0] = {
      id: "legacy-review",
      type: "review",
      version: 1,
      config: {
        category: "ordinary",
        requirement_id: "ordinary/task:42",
        semantics: "legacy_completion_v1",
      },
      origin: { imported: true },
    };
    data.results[0].result.type = "review";
    data.results[0].result.summary =
      "ordinary/task:42: legacy closed task is weaker than current-head proof.";
    data.results[0].result.agent_text =
      "Prediction only: integrate linked fixes or obtain a clean subsequent review.";
    data.results[0].result.human_text =
      "Prediction only: Accept findings requires this exact review/head/policy. Submit is separate.";
    const approval = structuredClone(data.results[0]);
    approval.evaluation_id = "shadow-approval";
    approval.result.type = "approval";
    approval.result.status = "pending";
    approval.result.summary =
      "Approval scopes: passed [alice]; pending [bob]; unknown [].";
    data.results.push(approval);
    data.bindings.push({
      id: "approval",
      type: "approval",
      version: 1,
      config: { requirement_ids: ["alice", "bob"], semantics: "exact_head_v1" },
      origin: { imported: false },
    });
    fixture.componentRef.setInput("observations", data);
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    expect(el.textContent).toContain(
      "Imported legacy comparison · Shadow only",
    );
    expect(el.textContent).toContain("Current-scope prediction · Shadow only");
    expect(el.textContent).toContain("integrate linked fixes");
    expect(el.textContent).toContain("passed [alice]; pending [bob]");
    expect(el.textContent).toContain("Submit is separate");
    expect(el.querySelectorAll("button,a,input,form").length).toBe(0);
    fixture.destroy();
  });
});
