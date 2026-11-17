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
import { provideRouter } from "@angular/router";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  buildClientWorkflowStagesView,
  evaluateFixturesClientSide,
  matchesGlobPattern,
  synthesizeDiffBudgetFixtures,
  validateSafeRe2Pattern,
  type FlowDeclarationV3,
  type FlowDeclarationV4,
} from "./qa-flows.models";
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

function mustEl<T extends Element>(root: ParentNode, selector: string): T {
  const el = root.querySelector<T>(selector);
  if (!el) {
    throw new Error(`Expected element matching ${selector}`);
  }
  return el;
}

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
        provideRouter([]),
        { provide: GrainApi, useValue: { post: postSpy } },
        { provide: SnackbarService, useValue: { show: showSpy } },
      ],
    }).compileComponents();

    fixture = TestBed.createComponent(QaV3StudioComponent);
    fixture.detectChanges();
  });

  it("starts all 8 stages collapsed by default and hides skipped stages in read-only partial workflows", () => {
    const partialDecl: FlowDeclarationV3 = {
      schema: "grain.dev/qa-flow/v3",
      stages: {
        "s0-intent": { enabled: false },
        "s2-plan-critic": { enabled: false },
        "s4-verify": { enabled: false },
        "s5-merge-triage": { mode: "off" },
      },
      skill: { mode: "off", text: "" },
    };

    fixture.componentRef.setInput("editing", false);
    fixture.componentRef.setInput("decl", partialDecl);
    fixture.detectChanges();

    const comp = fixture.componentInstance;
    const root = fixture.nativeElement as HTMLElement;

    // Every stage starts collapsed by default so active stages fit above the fold
    for (const stg of comp.workflowStages()) {
      expect(comp.effectiveStageCollapsed(stg.id)).toBe(true);
    }

    // Only active stages render in read-only partial workflow; skipped summary bar shows the count
    const visibleGroups = root.querySelectorAll(
      '[data-testid="qa-stage-tree"] .qa-stage-group',
    );
    expect(visibleGroups.length).toBeLessThan(8);
    const skippedToggle = mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-toggle-skipped-stages"]',
    );
    expect(skippedToggle.textContent).toContain("skipped in this workflow");
    expect(skippedToggle.textContent).toContain("Show all 8");

    // Clicking Show all 8 reveals all 8 canonical stages
    skippedToggle.click();
    fixture.detectChanges();
    expect(
      root.querySelectorAll('[data-testid="qa-stage-tree"] .qa-stage-group')
        .length,
    ).toBe(8);

    // Expand all / Collapse all toggles stage expansion cleanly
    const expandAllBtn = mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-expand-all-stages"]',
    );
    expect(expandAllBtn.textContent?.trim()).toBe("Expand all");
    expandAllBtn.click();
    fixture.detectChanges();
    expect(expandAllBtn.textContent?.trim()).toBe("Collapse all");
    expect(comp.effectiveStageCollapsed("s3-checks")).toBe(false);
  });

  it("hydrates stages and custom checks from a V3 flow declaration in focused substep drawer", () => {
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
          "when-changed": "pkg/model/schema.go",
          "require-changed": "pkg/model/migrations.go",
          fixtures: {
            fail: [{ name: "missing migration" }],
            pass: [{ name: "with migration" }],
          },
        },
      ],
    };

    fixture.componentRef.setInput("editing", true);
    fixture.componentRef.setInput("decl", decl);
    fixture.detectChanges();

    const comp = fixture.componentInstance;
    const root = fixture.nativeElement as HTMLElement;
    expect(comp.s0Intent()).toBe(true);
    expect(comp.s2PlanCritic()).toBe(true);
    expect(comp.customStages().length).toBe(1);
    expect(comp.customChecks().length).toBe(1);

    // Select s0:intent substep -> focused single-substep editor shows #s0Intent
    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-step-row-s0:intent"]',
    ).click();
    fixture.detectChanges();
    expect(mustEl<HTMLInputElement>(root, "#s0Intent").checked).toBe(true);

    // Select custom stage substep
    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-step-row-custom:s3b"]',
    ).click();
    fixture.detectChanges();
    expect(
      root.querySelector('[data-testid="qa-v3-custom-stage-row"]'),
    ).toBeTruthy();

    // Select companion custom check substep -> zero-JSON companion test cases
    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-step-row-custom:schema-migration"]',
    ).click();
    fixture.detectChanges();
    expect(
      root.querySelector('[data-testid="qa-v3-custom-check-row"]'),
    ).toBeTruthy();
    expect(
      root.querySelector('[data-testid="qa-companion-examples"]'),
    ).toBeTruthy();
  });

  it("adds zero-JSON pattern, companion, and diff-budget checks and verifies via dryRun=1", async () => {
    fixture.componentRef.setInput("editing", true);
    fixture.detectChanges();
    const comp = fixture.componentInstance;
    const root = fixture.nativeElement as HTMLElement;

    // Expand Stage 5 and click + Add substep...
    comp.requestAddSubstep("s3-checks");
    fixture.detectChanges();
    expect(
      root.querySelector('[data-testid="qa-add-step-catalog"]'),
    ).toBeTruthy();

    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-v3-add-pattern"]',
    ).click();
    fixture.detectChanges();

    // Zero raw JSON textareas in custom check editor; failure message uses a 2-row textarea and examples use single-line inputs
    expect(
      root.querySelector(
        '[data-testid="qa-v3-custom-check-row"] textarea:not([data-testid="qa-check-message-input"])',
      ),
    ).toBeNull();
    expect(
      root
        .querySelector('[data-testid="qa-check-message-input"]')
        ?.getAttribute("rows"),
    ).toBe("2");
    const failInput = mustEl<HTMLInputElement>(
      root,
      '[data-testid="qa-check-fail-line"]',
    );
    const passInput = mustEl<HTMLInputElement>(
      root,
      '[data-testid="qa-check-pass-line"]',
    );
    expect(failInput.value).toContain("console.log");
    expect(passInput.value).toContain("const ready");
    expect(root.textContent).toContain("Caught");
    expect(root.textContent).toContain("Allowed");

    // Also add custom stage via catalog and run dryRun=1 verification
    comp.requestAddSubstep("s4-verify");
    fixture.detectChanges();
    mustEl<HTMLButtonElement>(root, '[data-testid="qa-v3-add-stage"]').click();
    fixture.detectChanges();

    // Switch to Empirical stats & dry-run tab to trigger verifyDryRun button
    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-insp-tab-empirical"]',
    ).click();
    fixture.detectChanges();
    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-v3-verify-dry-run"]',
    ).click();
    await fixture.whenStable();
    fixture.detectChanges();

    expect(postSpy).toHaveBeenCalledTimes(1);
    expect(postSpy.mock.calls[0][0]).toBe("POST /api/qa/flow-versions");
    expect(postSpy.mock.calls[0][3]).toBe("dryRun=1");
    const yamlBody = (postSpy.mock.calls[0][2] as { yaml: string }).yaml;
    expect(yamlBody).toContain('insert-after: "s3"');
    expect(yamlBody).toContain("timeout-seconds: 30");
    expect(root.textContent).toContain("sha256:v3preview");

    // Companion and diff-budget auto-synthesize fail/pass fixtures without JSON
    comp.addCheck("companion");
    comp.addCheck("diff-budget");
    fixture.detectChanges();
    const exported = comp.exportCustomChecks();
    const companionCheck = exported.find((c) => c.kind === "companion");
    const budgetCheck = exported.find((c) => c.kind === "diff-budget");
    expect(companionCheck?.fixtures?.fail?.length).toBe(1);
    expect(companionCheck?.fixtures?.pass?.length).toBe(1);
    expect(budgetCheck?.fixtures?.fail?.length).toBe(1);
    expect(budgetCheck?.fixtures?.pass?.length).toBe(1);
  });

  it("runs what-if probe and adopts repo-committed flow declarations", async () => {
    const comp = fixture.componentInstance;
    const root = fixture.nativeElement as HTMLElement;

    comp.showProbe.set(true);
    fixture.detectChanges();

    expect(
      root.querySelector('[data-testid="qa-v3-probe-dialog"]'),
    ).toBeTruthy();

    const probeTitleInput = mustEl<HTMLInputElement>(root, "#probeTitle");
    probeTitleInput.value = "Migrate schema 149";
    probeTitleInput.dispatchEvent(new Event("input"));
    const probeFilesInput = mustEl<HTMLTextAreaElement>(root, "#probeFiles");
    probeFilesInput.value = "pkg/model/schema.go, pkg/model/migrations.go";
    probeFilesInput.dispatchEvent(new Event("input"));
    fixture.detectChanges();

    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-v3-submit-probe"]',
    ).click();
    await fixture.whenStable();
    fixture.detectChanges();

    const probeResultEl = root.querySelector(
      '[data-testid="qa-v3-probe-result"]',
    );
    expect(probeResultEl?.textContent).toContain("score 85/100");
    expect(probeResultEl?.textContent).toContain("require_human (enforced)");

    comp.showAdopt.set(true);
    fixture.detectChanges();

    expect(
      root.querySelector('[data-testid="qa-v3-adopt-dialog"]'),
    ).toBeTruthy();

    const repoInput = mustEl<HTMLInputElement>(root, "#adoptRepo");
    repoInput.value = "sidthe/grain";
    repoInput.dispatchEvent(new Event("input"));
    const commitInput = mustEl<HTMLInputElement>(root, "#adoptCommit");
    commitInput.value = "0123456789abcdef0123456789abcdef01234567";
    commitInput.dispatchEvent(new Event("input"));
    fixture.detectChanges();

    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-v3-submit-adopt"]',
    ).click();
    await fixture.whenStable();
    fixture.detectChanges();

    expect(showSpy).toHaveBeenCalledWith("Adopted flow sha256:adopted123");
    expect(root.querySelector('[data-testid="qa-v3-adopt-dialog"]')).toBeNull();
  });

  it("renders all 8 Software Lifecycle Stages in Customize mode and auto-expands stages on search or kind filter", () => {
    fixture.componentRef.setInput("editing", true);
    fixture.detectChanges();
    const comp = fixture.componentInstance;
    const root = fixture.nativeElement as HTMLElement;

    const stageGroups = root.querySelectorAll(
      '[data-testid="qa-stage-tree"] .qa-stage-group',
    );
    expect(stageGroups.length).toBe(8);
    expect(root.textContent).toContain("1 · Goal & subtask breakdown");
    expect(root.textContent).toContain("3 · Plan & fresh critique");
    expect(root.textContent).toContain("4 · Coding & readability rules");
    expect(root.textContent).toContain("8 · Scorecard & merge triage");

    // Filter by kind chip: Critics auto-expands matching stages
    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-kind-filter-agents"]',
    ).click();
    fixture.detectChanges();

    const subagentStages = root.querySelectorAll(
      '[data-testid="qa-stage-tree"] .qa-stage-group',
    );
    expect(subagentStages.length).toBeGreaterThanOrEqual(2);
    expect(subagentStages.length).toBeLessThan(8);
    expect(comp.effectiveStageCollapsed("s4-verify")).toBe(false);

    // Reset to All and filter by search query
    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-kind-filter-all"]',
    ).click();
    const searchInput = mustEl<HTMLInputElement>(
      root,
      '[data-testid="qa-step-search"]',
    );
    searchInput.value = "ts-angular";
    searchInput.dispatchEvent(new Event("input"));
    fixture.detectChanges();

    const searchedSteps = root.querySelectorAll(".qa-step-node");
    expect(searchedSteps.length).toBe(1);
    expect(searchedSteps[0].textContent).toContain("ts-angular");
    expect(comp.visibleStages().map((s) => s.id)).toContain("s2-coding");
    expect(comp.effectiveStageCollapsed("s2-coding")).toBe(false);
  });

  it("inspects and edits Readability Packs, PR Contract, and Custom Checks in the focused substep drawer", () => {
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

    fixture.componentRef.setInput("editing", true);
    fixture.componentRef.setInput("declV4", declV4);
    fixture.detectChanges();

    const exportedChecks = fixture.componentInstance.exportCustomChecks();
    expect(exportedChecks[0]?.paths).toEqual(["ui-ng/src/**/*.ts"]);

    const root = fixture.nativeElement as HTMLElement;

    // Select ts-angular readability pack
    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-step-row-pack:ts-angular"]',
    ).click();
    fixture.detectChanges();

    expect(
      root.querySelector('[data-testid="qa-readability-pack-inspector"]'),
    ).toBeTruthy();
    const packText = mustEl<HTMLTextAreaElement>(
      root,
      '[data-testid="qa-pack-custom-text"]',
    );
    expect(packText.value).toContain("Use standalone OnPush components");

    // Select PR contract step in Stage 6
    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-step-row-pr:contract"]',
    ).click();
    fixture.detectChanges();

    expect(
      root.querySelector('[data-testid="qa-pr-contract-inspector"]'),
    ).toBeTruthy();
    const prSectionsInput = mustEl<HTMLInputElement>(
      root,
      '[data-testid="qa-pr-sections-input"]',
    );
    expect(prSectionsInput.value).toContain("Summary, Testing");

    // Select custom check step in Stage 5
    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-step-row-custom:no-console-log"]',
    ).click();
    fixture.detectChanges();

    expect(
      root.querySelector('[data-testid="qa-selected-step-card"]')?.textContent,
    ).toContain("custom:no-console-log");
  });

  it("evaluates Fail trap and Clean pass fixtures via the 1-click self-test runner", async () => {
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

    fixture.componentRef.setInput("editing", true);
    fixture.componentRef.setInput("declV4", declV4);
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-step-row-custom:forbid-debugger"]',
    ).click();
    fixture.detectChanges();

    const runBtn = mustEl<HTMLButtonElement>(
      root,
      '[data-testid="qa-run-fixtures-btn"]',
    );
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

  it("round-trips exclude/requireAdded/maxFiles/allowWaiver/schemaJson losslessly and preserves server_test.go pass fixture on single-side pattern edits", async () => {
    const declV4: FlowDeclarationV4 = {
      schema: "grain.dev/qa-flow/v4",
      "custom-checks": [
        {
          id: "custom:no-fmt-println-pkg",
          label: "No debug fmt.Println in pkg/",
          kind: "pattern",
          severity: "warn",
          paths: ["pkg/**/*.go"],
          exclude: ["**/*_test.go"],
          "forbid-added": "\\bfmt\\.Println\\(",
          "require-added": "fmt|slog",
          "max-files": 5,
          "allow-waiver": true,
          fixtures: {
            fail: [
              {
                name: "added fmt.Println in handler",
                file: "pkg/ui/server.go",
                "added-lines": ['\tfmt.Println("debug", req.URL)'],
              },
            ],
            pass: [
              {
                name: "fmt.Println in unit test ignored",
                file: "pkg/ui/server_test.go",
                "added-lines": ['\tfmt.Println("test output")'],
              },
            ],
          },
        },
      ],
    };

    fixture.componentRef.setInput("editing", true);
    fixture.componentRef.setInput("draftKey", "custom-v4");
    fixture.componentRef.setInput("draftExtends", "engineering-workflow-v4@1");
    fixture.componentRef.setInput("draftLabel", "Custom V4 Flow");
    fixture.componentRef.setInput("declV4", declV4);
    fixture.detectChanges();

    const comp = fixture.componentInstance;
    const cc = comp.customChecks()[0];
    expect(cc).toBeTruthy();
    if (cc) {
      // Editing ONLY the fail example line keeps server_test.go pass fixture intact
      comp.updatePatternExample(cc.uid, "fail", '\tfmt.Println("oops")');
    }
    fixture.detectChanges();

    await comp.runFixtureSelfTest();
    expect(comp.fixtureReport()?.passed).toBe(true);
    expect(comp.fixtureReport()?.failTrapOk).toBe(true);
    expect(comp.fixtureReport()?.cleanPassOk).toBe(true);

    const exported = comp.exportCustomChecks()[0];
    expect(exported?.exclude).toEqual(["**/*_test.go"]);
    expect(exported?.["require-added"]).toBe("fmt|slog");
    expect(exported?.["max-files"]).toBe(5);
    expect(exported?.["allow-waiver"]).toBe(true);
    expect(JSON.stringify(exported?.fixtures?.pass)).toContain(
      "pkg/ui/server_test.go",
    );

    const yml = comp.buildYaml();
    expect(yml).toContain('key: "custom-v4"');
    expect(yml).toContain('extends: "engineering-workflow-v4@1"');
    expect(yml).toContain('label: "Custom V4 Flow"');
    expect(yml).toContain('exclude: ["**/*_test.go"]');
    expect(yml).toContain("allow-waiver: true");
  });

  it("supports zero-JSON jsonschema checks, monotonic substep IDs, stage timeout bounds, s5:merge-triage customize CTA, and s1:blast-radius safety card", () => {
    const comp = fixture.componentInstance;
    const root = fixture.nativeElement as HTMLElement;

    // 1. Read-only mode: s5:merge-triage shows Customize workflow to edit... CTA
    fixture.componentRef.setInput("editing", false);
    fixture.detectChanges();
    comp.selectStepFromGuide({
      stageId: "s5-triage",
      stepId: "s5:merge-triage",
    });
    fixture.detectChanges();
    expect(
      root.querySelector('[data-testid="qa-step-customize-cta"]'),
    ).toBeTruthy();

    // 2. Customize mode: s1:blast-radius renders safety-floor explanation card (never empty)
    fixture.componentRef.setInput("editing", true);
    fixture.detectChanges();
    comp.selectStep("s1-context", "s1:blast-radius");
    fixture.detectChanges();
    expect(
      root.querySelector('[data-testid="qa-locked-floor-card"]'),
    ).toBeTruthy();

    // 3. Zero-JSON jsonschema check synthesis & client-side evaluation
    comp.addCheck("jsonschema");
    fixture.detectChanges();
    const schemaCheck = comp
      .customChecks()
      .find((c) => c.engine === "jsonschema");
    expect(schemaCheck?.paths).toBe("**/*.json");
    expect(schemaCheck?.requiredKeys).toBe("name, version");
    expect(schemaCheck?.schemaJson).toEqual({
      type: "object",
      required: ["name", "version"],
    });
    expect(
      root.querySelector('[data-testid="qa-jsonschema-examples"]')?.textContent,
    ).toContain("Caught");
    expect(
      root.querySelector('[data-testid="qa-jsonschema-examples"]')?.textContent,
    ).toContain("Allowed");

    // 4. Monotonic IDs across add -> remove -> add, and custom stage timeout clamping 10..120
    comp.addCustomStage();
    const firstStage = comp.customStages()[0];
    comp.addCustomStage();
    const secondStage = comp.customStages()[1];
    if (firstStage) {
      comp.removeCustomStage(firstStage.uid);
    }
    comp.addCustomStage();
    fixture.detectChanges();
    const thirdStage = comp.customStages()[1];
    expect(thirdStage?.id).not.toBe(secondStage?.id);

    if (thirdStage) {
      const timeoutInput = mustEl<HTMLInputElement>(
        root,
        '[data-testid="qa-custom-stage-timeout"]',
      );
      expect(timeoutInput.min).toBe("10");
      expect(timeoutInput.max).toBe("120");
      timeoutInput.value = "500";
      timeoutInput.dispatchEvent(new Event("input"));
      fixture.detectChanges();
      expect(
        comp.customStages().find((s) => s.uid === thirdStage.uid)
          ?.timeoutSeconds,
      ).toBe(120);
    }
  });

  it("rejects unsafe non-RE2/ReDoS regexes, syncs pattern fail fixture path scope, preserves jsonschema properties & diff-budget max-files-only, and quotes YAML scalars", async () => {
    const declV4: FlowDeclarationV4 = {
      schema: "grain.dev/qa-flow/v4",
      "pr-contract": {
        enabled: true,
        "conventional-commits": true,
        "max-subject-chars": 72,
        "required-sections": ["Summary", "Verification"],
        "guidance-text": "Include #ticket: summary & rollback plan.",
      },
      "custom-checks": [
        {
          id: "custom:strict-schema",
          label: "Strict package schema",
          kind: "jsonschema",
          severity: "blocking",
          paths: ["configs/**/*.json"],
          "schema-json": {
            type: "object",
            additionalProperties: false,
            properties: {
              version: { type: "number" },
              enabled: { type: "boolean" },
            },
            required: [],
          },
          fixtures: {
            fail: [{ name: "bad", files: { "configs/a.json": "[]" } }],
            pass: [
              {
                name: "good",
                files: { "configs/a.json": '{"version":1,"enabled":true}' },
              },
            ],
          },
        },
        {
          id: "custom:files-only-budget",
          label: "Max touched files only",
          kind: "diff-budget",
          severity: "warn",
          "max-files": 8,
          fixtures: {
            fail: [{ name: "too many", "changed-files": 9 }],
            pass: [{ name: "ok", "changed-files": 3 }],
          },
        },
      ],
    };

    fixture.componentRef.setInput("editing", true);
    fixture.componentRef.setInput("draftKey", "safe-flow");
    fixture.componentRef.setInput("draftSummary", "Guardrail: #1 safety");
    fixture.componentRef.setInput("builtinCheckOverrides", {
      "golden:mutation-hardening": { enabled: false, severity: "warn" },
    });
    fixture.componentRef.setInput("declV4", declV4);
    fixture.detectChanges();

    const comp = fixture.componentInstance;
    const root = fixture.nativeElement as HTMLElement;

    // 1. jsonschema with empty required[] stays "" (not "name, version") and preserves properties on paths edit
    const jsCheck = comp
      .customChecks()
      .find((c) => c.id === "custom:strict-schema");
    expect(jsCheck?.requiredKeys).toBe("");
    if (jsCheck) {
      comp.updateCheckField(jsCheck.uid, "paths", "manifests/**/*.json");
      comp.updateCheckField(jsCheck.uid, "requiredKeys", "version");
    }
    const updatedJs = comp
      .customChecks()
      .find((c) => c.id === "custom:strict-schema");
    expect(updatedJs?.schemaJson).toEqual({
      type: "object",
      additionalProperties: false,
      properties: {
        version: { type: "number" },
        enabled: { type: "boolean" },
      },
      required: ["version"],
    });

    // 2. diff-budget with max-files only keeps maxChangedLines === 0 (never inflated to 250)
    const budgetCheck = comp
      .customChecks()
      .find((c) => c.id === "custom:files-only-budget");
    expect(budgetCheck?.maxFiles).toBe(8);
    expect(budgetCheck?.maxChangedLines).toBe(0);

    // 3. pattern check syncs fail[0].file when paths/exclude change, and rejects ReDoS / lookahead
    comp.addCheck("pattern");
    fixture.detectChanges();
    const patCheck = comp.customChecks().find((c) => c.engine === "pattern");
    expect(patCheck).toBeTruthy();
    if (patCheck) {
      comp.updateCheckField(patCheck.uid, "paths", "ui-ng/**/*.ts");
      const afterPaths = comp
        .customChecks()
        .find((c) => c.uid === patCheck.uid);
      expect(afterPaths?.failJson).toContain('"ui-ng/sub/sample.ts"');

      comp.updateCheckField(patCheck.uid, "exclude", "ui-ng/sub/**");
      const afterExclude = comp
        .customChecks()
        .find((c) => c.uid === patCheck.uid);
      expect(afterExclude?.failJson).toContain('"ui-ng/sample.ts"');

      // ReDoS nested quantifier and PCRE lookahead must fail client-side validation safely
      comp.updateCheckField(patCheck.uid, "forbidAdded", "(a+)+b");
      await comp.runFixtureSelfTest();
      const redosEval = comp.fixtureReport();
      expect(redosEval?.passed).toBe(false);
      expect(redosEval?.failTrapOk).toBe(false);
      expect(redosEval?.summary).toContain("Nested repetition quantifiers");

      comp.updateCheckField(patCheck.uid, "forbidAdded", "foo(?=bar)");
      await comp.runFixtureSelfTest();
      expect(comp.fixtureReport()?.passed).toBe(false);
      expect(comp.fixtureReport()?.failTrapOk).toBe(false);
    }

    // 4. Disabled built-in check disables severity pill-select button
    comp.selectStep("s3-checks", "golden:mutation-hardening");
    fixture.detectChanges();
    const sevBtn = root.querySelector<HTMLButtonElement>(
      '[data-testid="qa-builtin-check-severity"]',
    );
    expect(sevBtn?.disabled).toBe(true);

    // 5. buildYaml quotes special scalars and emits summary, skill, checks, pr-contract.guidance-text, stop-nudge, verifier
    const yaml = comp.buildYaml();
    expect(yaml).toContain('summary: "Guardrail: #1 safety"');
    expect(yaml).toContain("skill:");
    expect(yaml).toContain("checks:");
    expect(yaml).toContain(
      'guidance-text: "Include #ticket: summary & rollback plan."',
    );
    expect(yaml).toContain("stop-nudge:");
    expect(yaml).toContain("verifier:");
  });

  it("round-trips stage options, custom stage fields, empty extraPaths, prGuidanceText reset, disabled s0/s2 complexities, exclude-aware diff-budget/jsonschema fixtures, and honest diff-budget status pills", async () => {
    const richV4: FlowDeclarationV4 = {
      schema: "grain.dev/qa-flow/v4",
      key: "team-strict-v4",
      extends: "engineering-workflow-v4@1",
      stages: {
        "s0-intent": {
          enabled: true,
          complexities: ["low", "high"],
          "max-seconds": 45,
          "max-tokens": 4000,
          "on-clarification": "proceed",
        },
        "s2-plan-critic": {
          enabled: true,
          complexities: ["medium", "high"],
          tiers: ["T2", "T3"],
          "max-seconds": 90,
          "max-tokens": 12000,
          "max-bounces": 2,
        },
        "s5-merge-triage": {
          mode: "enforced",
          "auto-merge-tiers": ["T1"],
          "require-human-tiers": ["T3"],
        },
        custom: [
          {
            id: "custom:s3b-companion-checks",
            label: "Companion checks",
            kind: "check-bundle",
            "insert-after": "s3",
            "when-paths": ["pkg/**"],
            "min-tier": "T2",
            "timeout-seconds": 45,
            "check-ids": ["custom:pair"],
            "skill-ref": "skills/companion.md",
          },
        ],
      },
      decompose: {
        enabled: true,
        mode: "propose_dag",
        complexities: ["high"],
        "max-subtasks": 5,
        "target-diff-lines": 320,
      },
      context: {
        enabled: true,
        "include-callers": true,
        "include-memory": false,
        "max-tokens": 9000,
        "extra-paths": [],
      },
      "pr-contract": {
        enabled: true,
        mode: "blocking",
        "max-subject-chars": 72,
        "required-sections": ["Summary", "Verification"],
        "audit-lint-suppressions": true,
        "guidance-text": "Old guidance text",
      },
    };

    fixture.componentRef.setInput("editing", true);
    fixture.componentRef.setInput("declV4", richV4);
    fixture.detectChanges();

    const comp = fixture.componentInstance;
    const root = fixture.nativeElement as HTMLElement;

    // 1. Lossless round-trip of s0-intent, s2-plan-critic, s5-merge-triage, stages.custom, decompose.complexities, empty context.extraPaths
    const exportedEnabled = comp.exportDeclarationV4();
    expect(exportedEnabled.stages?.["s0-intent"]).toEqual({
      enabled: true,
      complexities: ["low", "high"],
      "max-seconds": 45,
      "max-tokens": 4000,
      "on-clarification": "proceed",
    });
    expect(exportedEnabled.stages?.["s2-plan-critic"]).toEqual({
      enabled: true,
      complexities: ["medium", "high"],
      tiers: ["T2", "T3"],
      "max-seconds": 90,
      "max-tokens": 12000,
      "max-bounces": 2,
    });
    expect(exportedEnabled.stages?.["s5-merge-triage"]).toEqual({
      mode: "enforced",
      "auto-merge-tiers": ["T1"],
      "require-human-tiers": ["T3"],
    });
    expect(exportedEnabled.stages?.custom).toEqual([
      {
        id: "custom:s3b-companion-checks",
        label: "Companion checks",
        kind: "check-bundle",
        "insert-after": "s3",
        "when-paths": ["pkg/**"],
        "min-tier": "T2",
        "timeout-seconds": 45,
        "check-ids": ["custom:pair"],
        "skill-ref": "skills/companion.md",
      },
    ]);
    expect(exportedEnabled.decompose?.complexities).toEqual(["high"]);
    expect(exportedEnabled.context?.extraPaths).toEqual([]);

    // Disabling s0-intent and s2-plan-critic must still emit non-empty complexities so inheritance does not re-enable them
    comp.s0Intent.set(false);
    comp.s2PlanCritic.set(false);
    fixture.detectChanges();
    const exportedDisabled = comp.exportDeclarationV4();
    const s0Disabled = exportedDisabled.stages?.["s0-intent"];
    const s2Disabled = exportedDisabled.stages?.["s2-plan-critic"];
    expect(typeof s0Disabled === "object" && s0Disabled?.enabled).toBe(false);
    expect(typeof s0Disabled === "object" && s0Disabled?.complexities).toEqual([
      "low",
      "high",
    ]);
    expect(typeof s2Disabled === "object" && s2Disabled?.enabled).toBe(false);
    expect(typeof s2Disabled === "object" && s2Disabled?.complexities).toEqual([
      "medium",
      "high",
    ]);

    const yaml = comp.buildYaml();
    expect(yaml).toContain('kind: "check-bundle"');
    expect(yaml).toContain('when-paths: ["pkg/**"]');
    expect(yaml).toContain('min-tier: "T2"');
    expect(yaml).toContain('check-ids: ["custom:pair"]');
    expect(yaml).toContain('skill-ref: "skills/companion.md"');
    expect(yaml).toContain('complexities: ["low","high"]');
    expect(yaml).toContain("max-bounces: 2");
    expect(yaml).toContain('on-clarification: "proceed"');
    expect(yaml).toContain('auto-merge-tiers: ["T1"]');
    expect(yaml).toContain("extra-paths: []");

    // 2. diff-budget exclude glob + max-files-only synthesis + honest status pill labels
    comp.addCheck("diff-budget");
    fixture.detectChanges();
    const budget = comp.customChecks().find((c) => c.engine === "diff-budget");
    expect(budget).toBeTruthy();
    if (budget) {
      comp.updateCheckField(budget.uid, "paths", "pkg/**/*.go");
      comp.updateCheckField(budget.uid, "exclude", "pkg/sub/**");
      fixture.detectChanges();
      const afterExclude = comp
        .customChecks()
        .find((c) => c.uid === budget.uid);
      expect(afterExclude?.failJson).toContain('"pkg/sample.go"');
      await comp.runFixtureSelfTest();
      expect(comp.fixtureReport()?.passed).toBe(true);
      const okText =
        root.querySelector('[data-testid="qa-budget-examples"]')?.textContent ??
        "";
      expect(okText).toContain("Caught");
      expect(okText).toContain("Allowed");

      // Max-files-only budget synthesizes maxFiles + 1 distinct in-scope non-excluded files
      comp.updateCheckMaxLines(budget.uid, 0);
      comp.updateCheckMaxFiles(budget.uid, 3);
      fixture.detectChanges();
      const filesOnly = comp.customChecks().find((c) => c.uid === budget.uid);
      const parsedFail = JSON.parse(filesOnly?.failJson ?? "[]") as {
        "changed-files"?: string[];
      }[];
      expect(parsedFail[0]?.["changed-files"]?.length).toBe(4);
      expect(
        parsedFail[0]?.["changed-files"]?.every(
          (p) => p.startsWith("pkg/") && !p.startsWith("pkg/sub/"),
        ),
      ).toBe(true);
      await comp.runFixtureSelfTest();
      expect(comp.fixtureReport()?.passed).toBe(true);

      // Corrupt fixtures -> pills must show Not caught / Blocked (not hardcoded Caught / Allowed)
      comp.updateFixture({
        uid: budget.uid,
        kind: "fail",
        val: JSON.stringify([
          { name: "too small", "changed-files": ["pkg/a.go"] },
        ]),
      });
      comp.updateFixture({
        uid: budget.uid,
        kind: "pass",
        val: JSON.stringify([
          {
            name: "too large",
            "changed-files": ["pkg/a.go", "pkg/b.go", "pkg/c.go", "pkg/d.go"],
          },
        ]),
      });
      fixture.detectChanges();
      const badText =
        root.querySelector('[data-testid="qa-budget-examples"]')?.textContent ??
        "";
      expect(badText).toContain("Not caught");
      expect(badText).toContain("Blocked");
    }

    // 3. jsonschema exclude glob re-synthesizes non-excluded fixture file path
    comp.addCheck("jsonschema");
    fixture.detectChanges();
    const js = comp.customChecks().find((c) => c.engine === "jsonschema");
    expect(js).toBeTruthy();
    if (js) {
      comp.updateCheckField(js.uid, "paths", "configs/**/*.json");
      comp.updateCheckField(js.uid, "exclude", "configs/sub/**");
      fixture.detectChanges();
      const afterExclude = comp.customChecks().find((c) => c.uid === js.uid);
      expect(afterExclude?.failJson).toContain('"configs/sample.json"');
      await comp.runFixtureSelfTest();
      expect(comp.fixtureReport()?.passed).toBe(true);
    }

    // 4. Switching to a declaration without pr-contract.guidance-text resets prGuidanceText to ""
    fixture.componentRef.setInput("editing", false);
    fixture.componentRef.setInput("declV4", {
      schema: "grain.dev/qa-flow/v4",
      "pr-contract": {
        enabled: true,
        mode: "warn",
      },
    });
    fixture.detectChanges();
    expect(comp.prGuidanceText()).toBe("");
  });

  it("preserves omitted context.extraPaths from Go omitempty, matches ** globs across nested slashes, and evaluates zeroed or out-of-scope diff-budget fixtures honestly", () => {
    const comp = fixture.componentInstance;

    // 1. Hydrating declarationV4.context with extraPaths omitted (Go json:",omitempty") keeps [] and renders "callers & memory"
    const declWithOmittedExtra: FlowDeclarationV4 = {
      schema: "grain.dev/qa-flow/v4",
      key: "omitempty-ctx-v4",
      context: {
        enabled: true,
        includeCallers: true,
        includeMemory: true,
        maxTokens: 8000,
      },
    };
    fixture.componentRef.setInput("editing", true);
    fixture.componentRef.setInput("declV4", declWithOmittedExtra);
    fixture.detectChanges();

    expect(comp.exportDeclarationV4().context?.extraPaths).toEqual([]);
    const studioCtxStep = comp
      .workflowStages()
      .find((s) => s.id === "s1-context")
      ?.steps.find((st) => st.id === "s1:context-discovery");
    expect(studioCtxStep?.scope).toBe("callers & memory");

    const directStages = buildClientWorkflowStagesView({
      key: "omitempty-ctx-v4",
      version: 1,
      hash: "sha256:omitempty-ctx",
      label: "OmitEmpty Context",
      summary: "Test",
      schema: "grain.dev/qa-flow/v4",
      declarationV4: declWithOmittedExtra,
    });
    const directCtxStep = directStages
      .find((s) => s.id === "s1-context")
      ?.steps.find((st) => st.id === "s1:context-discovery");
    expect(directCtxStep?.scope).toBe("callers & memory");

    // 2. Single-pass ** glob matching and max-files-only synthesis with exclude: "pkg/core/**"
    expect(
      matchesGlobPattern("pkg/core/**", "pkg/core/sub_1/handler_1.go"),
    ).toBe(true);
    expect(matchesGlobPattern("pkg/core/**", "pkg/ui/handler_1.go")).toBe(
      false,
    );

    const syn = synthesizeDiffBudgetFixtures(
      0,
      "pkg/**/*.go",
      "pkg/core/**",
      3,
    );
    const parsedFail = JSON.parse(syn.failJson) as {
      "changed-files"?: string[];
    }[];
    const failFiles = parsedFail[0]?.["changed-files"] ?? [];
    expect(failFiles.length).toBe(4);
    expect(failFiles.some((p) => p.startsWith("pkg/core/"))).toBe(false);
    expect(failFiles.every((p) => matchesGlobPattern("pkg/**/*.go", p))).toBe(
      true,
    );
    const maxFilesEval = evaluateFixturesClientSide({
      id: "custom:pkg-files-budget",
      engine: "diff-budget",
      paths: ["pkg/**/*.go"],
      exclude: ["pkg/core/**"],
      maxChangedLines: 0,
      maxFiles: 3,
      failJson: syn.failJson,
      passJson: syn.passJson,
    });
    expect(maxFilesEval.passed).toBe(true);

    // 3. diff-budget with both maxChangedLines: 0 and maxFiles: 0 must fail failTrapOk (never false green)
    const zeroBudgetEval = evaluateFixturesClientSide({
      id: "custom:zero-budget",
      engine: "diff-budget",
      paths: ["pkg/**/*.go"],
      maxChangedLines: 0,
      maxFiles: 0,
      failJson: JSON.stringify([
        {
          name: "would fail if budget set",
          "changed-files": ["pkg/a.go", "pkg/b.go"],
          "added-lines": ["+line1", "+line2"],
        },
      ]),
      passJson: JSON.stringify([
        {
          name: "small diff",
          "changed-files": ["pkg/a.go"],
          "added-lines": ["+line1"],
        },
      ]),
    });
    expect(zeroBudgetEval.failTrapOk).toBe(false);
    expect(zeroBudgetEval.passed).toBe(false);

    // Out-of-scope changed-files must not falsely trigger line-budget findings
    const outOfScopeLinesEval = evaluateFixturesClientSide({
      id: "custom:pkg-lines-budget",
      engine: "diff-budget",
      paths: ["pkg/**/*.go"],
      maxChangedLines: 2,
      maxFiles: 0,
      failJson: JSON.stringify([
        {
          name: "out-of-scope docs file with 5 lines",
          "changed-files": ["docs/notes.md"],
          "added-lines": ["1", "2", "3", "4", "5"],
        },
      ]),
      passJson: JSON.stringify([
        {
          name: "in-scope small diff",
          "changed-files": ["pkg/a.go"],
          "added-lines": ["1"],
        },
      ]),
    });
    expect(outOfScopeLinesEval.failTrapOk).toBe(false);
    expect(outOfScopeLinesEval.passed).toBe(false);
  });

  it("rejects nested-group, bounded-quantifier, and alternation ReDoS patterns in linear time and reflects draftSchema in buildYaml()", () => {
    const hostileInput = `${"a".repeat(32)}!`;
    const redosPatterns = [
      "((a)+)+$",
      "(a{1,2})+$",
      "(a|a)+$",
      "((a|a))+$",
      ".*.*foo",
    ];
    for (const pat of redosPatterns) {
      expect(validateSafeRe2Pattern(pat)).toContain(
        "Nested repetition quantifiers",
      );
      const res = evaluateFixturesClientSide({
        id: "custom:redos-check",
        engine: "pattern",
        forbidAdded: pat,
        failJson: JSON.stringify([
          { name: "hostile", "added-lines": [hostileInput] },
        ]),
        passJson: JSON.stringify([{ name: "clean", "added-lines": ["ok"] }]),
      });
      expect(res.passed).toBe(false);
      expect(res.failTrapOk).toBe(false);
      expect(res.summary).toContain("Nested repetition quantifiers");
    }

    // Legitimate RE2 patterns (non-repeated alternation, escaped parens, non-capturing literal repeats, char classes) remain allowed
    expect(validateSafeRe2Pattern("(?:foo|bar)")).toBeNull();
    expect(validateSafeRe2Pattern("(?:abc)+")).toBeNull();
    expect(validateSafeRe2Pattern("\\bfmt\\.Println\\(")).toBeNull();
    expect(validateSafeRe2Pattern("([a+b])+")).toBeNull();

    // draftSchema input controls buildYaml() schema header and emits strictly FlowDeclarationV2 keys for v2
    fixture.componentRef.setInput("editing", true);
    fixture.componentRef.setInput("draftSchema", "grain.dev/qa-flow/v2");
    fixture.detectChanges();
    const v2Yaml = fixture.componentInstance.buildYaml();
    expect(v2Yaml).toContain('schema: "grain.dev/qa-flow/v2"');
    expect(v2Yaml).toContain("skill:");
    expect(v2Yaml).toContain("stop-nudge:");
    expect(v2Yaml).toContain("verifier:");
    expect(v2Yaml).not.toContain("decompose:");
    expect(v2Yaml).not.toContain("context:");
    expect(v2Yaml).not.toContain("readability-packs:");
    expect(v2Yaml).not.toContain("skill-steps:");
    expect(v2Yaml).not.toContain("pr-contract:");
    expect(v2Yaml).not.toContain("stages:");
    expect(v2Yaml).not.toContain("custom-checks:");
    fixture.componentRef.setInput("draftSchema", "grain.dev/qa-flow/v4");
    fixture.detectChanges();
    const v4Yaml = fixture.componentInstance.buildYaml();
    expect(v4Yaml).toContain('schema: "grain.dev/qa-flow/v4"');
    expect(v4Yaml).toContain("decompose:");
    expect(v4Yaml).toContain("stages:");
    expect(v4Yaml).toContain("pr-contract:");
  });
});
