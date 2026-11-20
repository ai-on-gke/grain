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
  it("explains withheld unsafe history without displaying its payload", async () => {
    await TestBed.configureTestingModule({
      imports: [TaskObservationsComponent],
    }).compileComponents();
    const fixture = TestBed.createComponent(TaskObservationsComponent);
    fixture.componentRef.setInput("observations", view());
    fixture.componentRef.setInput("diagnostics", {
      counts: { unknown_invalid_history: 1 },
      history: [],
      evaluations: 1,
      started_at: "2026-11-17T00:00:00Z",
    });
    fixture.detectChanges();
    expect(fixture.nativeElement.textContent).toContain("were withheld");
    expect(fixture.nativeElement.textContent).toContain(
      "Current observations remain separate",
    );
    fixture.destroy();
  });
  it.each([
    "delivery",
    "acknowledgment",
    "integration",
    "children",
    "checks",
    "review",
    "approval",
    "base_health",
    "nightly_health",
    "publishing_health",
    "pipeline_health",
    "qa_stage",
    "release_qualification",
  ])(
    "renders each %s status and its guidance once without mutation controls",
    async (type) => {
      await TestBed.configureTestingModule({
        imports: [TaskObservationsComponent],
      }).compileComponents();
      const fixture = TestBed.createComponent(TaskObservationsComponent);
      for (const status of ["met", "unmet", "pending", "unknown", "error"]) {
        const data = view();
        data.bindings[0].type = type;
        data.results[0].result.type = type;
        data.results[0].result.status = status;
        data.results[0].result.agent_text =
          "Qualification prediction only: inspect the current evidence.";
        data.results[0].result.evidence[0].head = "qualification-exact-head";
        data.results[0].result.waits = [
          {
            event: "source_changed",
            key: "qualification-source",
            owner: "legacy",
            deadline: "2026-11-17T00:01:00Z",
          },
        ];
        fixture.componentRef.setInput("observations", data);
        fixture.detectChanges();
        const el = fixture.nativeElement as HTMLElement;
        expect(el.textContent).toContain(type + " v1 · " + status);
        expect(
          el.textContent?.split("Qualification prediction only:").length,
        ).toBe(2);
        expect(el.textContent?.split("qualification-exact-head").length).toBe(
          2,
        );
        expect(el.textContent).toContain("No operational wait is installed");
        expect(
          el.querySelectorAll("button,a,input,select,textarea,[role=button]")
            .length,
        ).toBe(0);
      }
      fixture.destroy();
    },
  );
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
  it("uses h2/h3 standalone and steps headings down to aria-level 3/4 inside a split pane", async () => {
    await TestBed.configureTestingModule({
      imports: [TaskObservationsComponent],
    }).compileComponents();
    const fixture = TestBed.createComponent(TaskObservationsComponent);
    fixture.componentRef.setInput("observations", view());
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    const sectionHeading = el.querySelector("section > h2");
    const entryHeading = el.querySelector("article > h3");
    expect(sectionHeading).not.toBeNull();
    expect(entryHeading).not.toBeNull();
    expect(sectionHeading?.getAttribute("aria-level")).toBeNull();
    expect(entryHeading?.getAttribute("aria-level")).toBeNull();

    fixture.componentRef.setInput("pane", true);
    fixture.detectChanges();
    expect(sectionHeading?.getAttribute("aria-level")).toBe("3");
    expect(entryHeading?.getAttribute("aria-level")).toBe("4");
    fixture.destroy();
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
    expect(el.textContent).toContain("shadow ordinary");
    expect(el.textContent).toContain("shadow named approval");
    expect(el.textContent).toContain("integrate linked fixes");
    expect(el.textContent).toContain("passed [alice]; pending [bob]");
    expect(el.textContent).toContain("Submit is separate");
    expect(el.querySelectorAll("button,a,input,form").length).toBe(0);
    fixture.destroy();
  });
});

describe("health shadow profiles", () => {
  it("compares labeled legacy and current profiles without remediation controls", async () => {
    await TestBed.configureTestingModule({
      imports: [TaskObservationsComponent],
    }).compileComponents();
    const fixture = TestBed.createComponent(TaskObservationsComponent);
    const data = view();
    data.bindings[0].type = "base_health";
    data.bindings[0].config = {
      semantics: "legacy_recorded_v1",
      allow_nonreproduction: true,
    };
    data.results[0].result.type = "base_health";
    data.results[0].result.summary =
      "Legacy observation profile legacy_recorded_v1: the base verdict is successful.";
    const current = structuredClone(data.results[0]);
    current.evaluation_id = "shadow-current";
    current.result.status = "unknown";
    current.result.summary =
      "Current profile current_tip_v1: the recorded verdict is for an older base tip.";
    data.results.push(current);
    fixture.componentRef.setInput("observations", data);
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    expect(el.textContent).toContain("Shadow predictions");
    expect(el.textContent).toContain(
      "Legacy observation profile legacy_recorded_v1",
    );
    expect(el.textContent).toContain("older base tip");
    expect(el.textContent).toContain("no operational effects");
    expect(el.querySelectorAll("button,a,input,form").length).toBe(0);
  });
});

describe("QA and release shadow blockers", () => {
  it("renders every blocker and repeated stage provenance without controls", async () => {
    await TestBed.configureTestingModule({
      imports: [TaskObservationsComponent],
    }).compileComponents();
    const fixture = TestBed.createComponent(TaskObservationsComponent);
    const data = view();
    const result = data.results[0].result;
    result.type = "qa_stage";
    result.status = "unmet";
    result.summary =
      "Shadow QA s3: observed blockers; legacy QA remains authoritative.";
    result.blockers = [
      { code: "unsatisfied", summary: "integrity check failed", target: {} },
      { code: "person_required", summary: "human triage floor", target: {} },
    ];
    result.evidence = [
      { kind: "qa", id: "1", version: "v1", head: "exact-head" },
      { kind: "qa", id: "2", version: "v2", head: "exact-head" },
    ];
    result.human_text =
      "Shadow prediction only: use existing bounded QA repair or eligible human triage.";
    fixture.componentRef.setInput("observations", data);
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    expect(el.querySelectorAll("li").length).toBe(2);
    expect(el.textContent).toContain("integrity check failed");
    expect(el.textContent).toContain("human triage floor");
    expect(el.textContent).toContain("qa #1");
    expect(el.textContent).toContain("qa #2");
    expect(el.textContent).toContain("exact-head");
    expect(el.textContent).toContain("Shadow prediction only");
    expect(el.querySelectorAll("button,a,input").length).toBe(0);
  });
});
