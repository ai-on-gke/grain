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

import { ComponentFixture, TestBed } from "@angular/core/testing";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { SnackbarService } from "../services/snackbar.service";
import type { FlowDeclarationV3, FlowDeclarationV4 } from "./qa-flows.models";
import { QaV3StudioComponent } from "./qa-v3-studio.component";

const ROUTE_RESPONSES: Readonly<Record<string, unknown>> = {
  "POST /api/qa/flow-versions": {
    dryRun: true,
    valid: true,
    hash: "sha256:v3preview",
    sampledPRs: 12,
    wouldBlockPRs: 2,
    byCheck: [],
  },
  "POST /api/qa/flow-versions/adopt": {
    hash: "sha256:adopted123",
  },
  "POST /api/qa/probe": {
    complexity: {
      tier: "high",
      score: 80,
      signals: ["cross-subsystem"],
      hasDesignItem: false,
    },
    risk: {
      tier: "T3",
      score: 85,
      signals: ["schema-migration"],
      testTampered: false,
      trustBoundary: true,
    },
    stagesFired: ["s0-intent", "s1-risk", "s2-plan-critic", "s3", "s4"],
    triage: {
      decision: "require_human",
      enforced: true,
      mode: "enforced",
      tier: "T3",
      blastScore: 85,
      reasons: ["trust-boundary"],
      blockedByFloor: true,
    },
  },
};

describe("QaV3StudioComponent", () => {
  let fixture: ComponentFixture<QaV3StudioComponent>;
  let postSpy: ReturnType<
    typeof vi.fn<
      (
        route: string,
        params?: unknown,
        body?: unknown,
        query?: string,
      ) => Promise<unknown>
    >
  >;
  let showSpy: ReturnType<typeof vi.fn<(msg: string) => void>>;

  beforeEach(async () => {
    postSpy = vi.fn(async (route: string) => ROUTE_RESPONSES[route] ?? {});
    showSpy = vi.fn();

    await TestBed.configureTestingModule({
      imports: [QaV3StudioComponent],
      providers: [
        { provide: GrainApi, useValue: { post: postSpy } },
        { provide: SnackbarService, useValue: { show: showSpy } },
      ],
    }).compileComponents();

    fixture = TestBed.createComponent(QaV3StudioComponent);
    fixture.detectChanges();
  });

  it("hydrates stages and custom checks from a V3 flow declaration", () => {
    const decl: FlowDeclarationV3 = {
      schema: "grain.dev/qa-flow/v3",
      stages: {
        "s0-intent": { enabled: true },
        "s2-plan-critic": { enabled: true },
        "s5-merge-triage": { mode: "enforced" },
        custom: [
          {
            id: "custom:s3b",
            label: "Companion gate",
            "insert-after": "s3",
            "timeout-seconds": 30,
          },
        ],
      },
      "custom-checks": [
        {
          id: "custom:schema-migration",
          kind: "companion",
          fixtures: {
            fail: [{ name: "missing migration" }],
            pass: [{ name: "with migration" }],
          },
        },
      ],
    };

    fixture.componentRef.setInput("decl", decl);
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    expect(root.querySelector<HTMLInputElement>("#s0Intent")!.checked).toBe(
      true,
    );
    expect(root.querySelector<HTMLInputElement>("#s2PlanCritic")!.checked).toBe(
      true,
    );
    expect(
      root.querySelectorAll('[data-testid="qa-v3-custom-stage-row"]').length,
    ).toBe(1);
    expect(
      root.querySelectorAll('[data-testid="qa-v3-custom-check-row"]').length,
    ).toBe(1);
    expect(root.textContent).toContain("companion");
  });

  it("verifies inline fixtures via dryRun=1 with strict kebab-case YAML keys", async () => {
    const root = fixture.nativeElement as HTMLElement;

    root
      .querySelector<HTMLButtonElement>('[data-testid="qa-v3-add-stage"]')!
      .click();
    root
      .querySelector<HTMLButtonElement>('[data-testid="qa-v3-add-pattern"]')!
      .click();
    fixture.detectChanges();

    expect(
      root.querySelectorAll('[data-testid="qa-v3-custom-stage-row"]').length,
    ).toBe(1);
    expect(
      root.querySelectorAll('[data-testid="qa-v3-custom-check-row"]').length,
    ).toBe(1);

    root
      .querySelector<HTMLButtonElement>('[data-testid="qa-v3-verify-dry-run"]')!
      .click();
    await fixture.whenStable();
    fixture.detectChanges();

    expect(postSpy).toHaveBeenCalledTimes(1);
    expect(postSpy.mock.calls[0][0]).toBe("POST /api/qa/flow-versions");
    expect(postSpy.mock.calls[0][3]).toBe("dryRun=1");
    const yamlBody = (postSpy.mock.calls[0][2] as { yaml: string }).yaml;
    expect(yamlBody).toContain("insert-after: s3");
    expect(yamlBody).toContain("timeout-seconds: 30");
    expect(root.textContent).toContain("sha256:v3preview");
  });

  it("runs what-if probe and adopts repo-committed flow declarations", async () => {
    const root = fixture.nativeElement as HTMLElement;

    root
      .querySelector<HTMLButtonElement>('[data-testid="qa-v3-open-probe"]')!
      .click();
    fixture.detectChanges();

    expect(
      root.querySelector('[data-testid="qa-v3-probe-dialog"]'),
    ).toBeTruthy();

    const probeTitleInput =
      root.querySelector<HTMLInputElement>("#probeTitle")!;
    probeTitleInput.value = "Migrate schema 149";
    probeTitleInput.dispatchEvent(new Event("input"));
    const probeFilesInput =
      root.querySelector<HTMLTextAreaElement>("#probeFiles")!;
    probeFilesInput.value = "pkg/model/schema.go, pkg/model/migrations.go";
    probeFilesInput.dispatchEvent(new Event("input"));
    fixture.detectChanges();

    root
      .querySelector<HTMLButtonElement>('[data-testid="qa-v3-submit-probe"]')!
      .click();
    await fixture.whenStable();
    fixture.detectChanges();

    const probeResultEl = root.querySelector(
      '[data-testid="qa-v3-probe-result"]',
    );
    expect(probeResultEl?.textContent).toContain("Score: 85/100");
    expect(probeResultEl?.textContent).toContain("require_human (enforced)");

    root
      .querySelector<HTMLButtonElement>('[data-testid="qa-v3-open-adopt"]')!
      .click();
    fixture.detectChanges();

    expect(
      root.querySelector('[data-testid="qa-v3-adopt-dialog"]'),
    ).toBeTruthy();

    const repoInput = root.querySelector<HTMLInputElement>("#adoptRepo")!;
    repoInput.value = "sidthe/grain";
    repoInput.dispatchEvent(new Event("input"));
    const commitInput = root.querySelector<HTMLInputElement>("#adoptCommit")!;
    commitInput.value = "0123456789abcdef0123456789abcdef01234567";
    commitInput.dispatchEvent(new Event("input"));
    fixture.detectChanges();

    root
      .querySelector<HTMLButtonElement>('[data-testid="qa-v3-submit-adopt"]')!
      .click();
    await fixture.whenStable();
    fixture.detectChanges();

    expect(showSpy).toHaveBeenCalledWith("Adopted flow sha256:adopted123");
    expect(root.querySelector('[data-testid="qa-v3-adopt-dialog"]')).toBeNull();
  });

  it("renders all 8 Software Lifecycle Stages and filters by search query and kind chips", () => {
    const root = fixture.nativeElement as HTMLElement;

    const stageGroups = root.querySelectorAll(
      '[data-testid="qa-stage-tree"] .qa-stage-group',
    );
    expect(stageGroups.length).toBe(8);
    expect(root.textContent).toContain("1 · Goal & Subtask Breakdown");
    expect(root.textContent).toContain("3 · Plan & Fresh Critique");
    expect(root.textContent).toContain("4 · Coding & Readability Rules");
    expect(root.textContent).toContain("8 · Scorecard & Merge Triage");

    // Filter by kind chip: Sub-agents
    root
      .querySelector<HTMLButtonElement>(
        '[data-testid="qa-kind-filter-agents"]',
      )!
      .click();
    fixture.detectChanges();

    const subagentStages = root.querySelectorAll(
      '[data-testid="qa-stage-tree"] .qa-stage-group',
    );
    expect(subagentStages.length).toBeGreaterThanOrEqual(2);
    expect(subagentStages.length).toBeLessThan(8);

    // Reset to All and filter by search query
    root
      .querySelector<HTMLButtonElement>('[data-testid="qa-kind-filter-all"]')!
      .click();
    const searchInput = root.querySelector<HTMLInputElement>(
      '[data-testid="qa-step-search"]',
    )!;
    searchInput.value = "ts-angular";
    searchInput.dispatchEvent(new Event("input"));
    fixture.detectChanges();

    const searchedSteps = root.querySelectorAll(".qa-step-node");
    expect(searchedSteps.length).toBe(1);
    expect(searchedSteps[0].textContent).toContain("ts-angular");
  });

  it("inspects and edits Readability Packs, PR Contract, and Custom Checks in the 70% inspector", () => {
    const declV4: FlowDeclarationV4 = {
      schema: "grain.dev/qa-flow/v4",
      "readability-packs": [
        {
          id: "ts-angular",
          enabled: true,
          activation: "always",
          globs: ["ui-ng/**/*.ts"],
          "custom-text": "Use standalone OnPush components and signal inputs.",
        },
      ],
      "pr-contract": {
        enabled: true,
        mode: "blocking",
        "max-subject-chars": 72,
        "required-sections": ["Summary", "Testing"],
        "audit-lint-suppressions": true,
      },
      "custom-checks": [
        {
          id: "custom:no-console-log",
          kind: "pattern",
          severity: "blocking",
          paths: ["ui-ng/src/**/*.ts"],
          "forbid-added": "console\\.log",
          fixtures: {
            fail: [{ name: "leftover console.log", diff: "+console.log(x)" }],
            pass: [{ name: "clean code", diff: "+const x = 1;" }],
          },
        },
      ],
    };

    fixture.componentRef.setInput("declV4", declV4);
    fixture.detectChanges();

    const exportedChecks = fixture.componentInstance.exportCustomChecks();
    expect(exportedChecks[0]?.paths).toEqual(["ui-ng/src/**/*.ts"]);

    const root = fixture.nativeElement as HTMLElement;

    // Select ts-angular readability pack
    const packNode = root.querySelector<HTMLButtonElement>(
      '[data-testid="qa-step-node-pack:ts-angular"]',
    )!;
    expect(packNode).toBeTruthy();
    packNode.click();
    fixture.detectChanges();

    const packInspector = root.querySelector(
      '[data-testid="qa-readability-pack-inspector"]',
    );
    expect(packInspector).toBeTruthy();
    const packText = root.querySelector<HTMLTextAreaElement>(
      '[data-testid="qa-pack-custom-text"]',
    )!;
    expect(packText.value).toContain("Use standalone OnPush components");

    // Select PR contract step in Stage 6
    const prNode = root.querySelector<HTMLButtonElement>(
      '[data-testid="qa-step-node-pr:contract"]',
    )!;
    expect(prNode).toBeTruthy();
    prNode.click();
    fixture.detectChanges();

    expect(
      root.querySelector('[data-testid="qa-pr-contract-inspector"]'),
    ).toBeTruthy();
    const prSectionsInput = root.querySelector<HTMLInputElement>(
      '[data-testid="qa-pr-sections-input"]',
    )!;
    expect(prSectionsInput.value).toContain("Summary, Testing");

    // Select custom check step in Stage 5
    const customCheckNode = root.querySelector<HTMLButtonElement>(
      '[data-testid="qa-step-node-custom:no-console-log"]',
    )!;
    expect(customCheckNode).toBeTruthy();
    customCheckNode.click();
    fixture.detectChanges();

    expect(
      root.querySelector('[data-testid="qa-selected-step-card"]')?.textContent,
    ).toContain("custom:no-console-log");
  });

  it("evaluates Fail trap and Clean pass fixtures via the 1-click Inline Fixture Self-Test Runner", async () => {
    const declV4: FlowDeclarationV4 = {
      schema: "grain.dev/qa-flow/v4",
      "custom-checks": [
        {
          id: "custom:forbid-debugger",
          kind: "pattern",
          severity: "blocking",
          "forbid-added": "\\bdebugger\\b",
          fixtures: {
            fail: [{ name: "debugger statement", diff: "+debugger;" }],
            pass: [{ name: "clean statement", diff: "+return 42;" }],
          },
        },
      ],
    };

    fixture.componentRef.setInput("declV4", declV4);
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    const runBtn = root.querySelector<HTMLButtonElement>(
      '[data-testid="qa-run-fixtures-btn"]',
    )!;
    expect(runBtn).toBeTruthy();

    runBtn.click();
    await fixture.whenStable();
    fixture.detectChanges();

    const resultsBox = root.querySelector(
      '[data-testid="qa-fixture-test-result"]',
    );
    expect(resultsBox).toBeTruthy();
    expect(resultsBox?.textContent).toContain("Fail trap: PASS");
    expect(resultsBox?.textContent).toContain("Clean pass: PASS");
    expect(resultsBox?.textContent).toContain("debugger statement");
    expect(resultsBox?.textContent).toContain("clean statement");
    expect(postSpy).toHaveBeenCalledWith(
      "POST /api/qa/flow-versions",
      undefined,
      expect.objectContaining({
        yaml: expect.stringContaining('forbid-added: "\\\\bdebugger\\\\b"'),
      }),
      "dryRun=1",
    );
  });

  it("detects failing diff fixtures honestly, exports full V4 YAML, and resets state on flow switch", async () => {
    const declV4: FlowDeclarationV4 = {
      schema: "grain.dev/qa-flow/v4",
      decompose: {
        enabled: false,
        mode: "propose_dag",
        "max-subtasks": 6,
      },
      "skill-steps": [
        {
          id: "step:custom-coding-1",
          label: "Check error wrapping",
          stage: "coding",
          enabled: true,
          globs: ["pkg/**/*.go"],
          "max-bytes": 500,
          text: "Wrap errors with %w.",
        },
      ],
      "custom-checks": [
        {
          id: "custom:strict-debugger",
          kind: "pattern",
          severity: "blocking",
          paths: ["ui-ng/src/**/*.ts"],
          "forbid-added": "\\bdebugger\\b",
          fixtures: {
            fail: [
              { name: "missing debugger in fail trap", diff: "+const a = 1;" },
            ],
            pass: [{ name: "accidental debugger in pass", diff: "+debugger;" }],
          },
        },
      ],
    };

    fixture.componentRef.setInput("declV4", declV4);
    fixture.detectChanges();

    await fixture.componentInstance.runFixtureSelfTest();
    fixture.detectChanges();

    const rep = fixture.componentInstance.fixtureReport();
    expect(rep?.passed).toBe(false);
    expect(rep?.failTrapOk).toBe(false);
    expect(rep?.cleanPassOk).toBe(false);

    const yaml = fixture.componentInstance.buildYaml();
    expect(yaml).toContain("skill-steps:");
    expect(yaml).toContain("step:custom-coding-1");
    expect(yaml).toContain('paths: ["ui-ng/src/**/*.ts"]');
    expect(yaml).toContain('forbid-added: "\\\\bdebugger\\\\b"');

    // Switching to a V3 declaration resets V4 decompose, skillSteps, and fixtureReport
    const declV3: FlowDeclarationV3 = {
      schema: "grain.dev/qa-flow/v3",
      stages: {
        "s0-intent": { enabled: true },
      },
    };
    fixture.componentRef.setInput("declV4", undefined);
    fixture.componentRef.setInput("decl", declV3);
    fixture.detectChanges();

    expect(fixture.componentInstance.fixtureReport()).toBeNull();
    expect(fixture.componentInstance.skillSteps().length).toBe(0);
    expect(fixture.componentInstance.decomposeMode()).toBe("slice_plan");
  });
});
