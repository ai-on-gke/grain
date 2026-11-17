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
import { provideRouter, Router } from "@angular/router";
import { RouterTestingHarness } from "@angular/router/testing";
import { describe, expect, it } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import type {
  QAExperimentDetail,
  QAExperimentResults,
  QAExperimentSummary,
} from "./qa-experiments.models";
import { TelemetryQualityComponent } from "./quality-checks.component";

interface RecordedCall {
  method: string;
  route: string;
  params?: unknown;
  body?: unknown;
  query?: string;
}

function sampleDetail(
  overrides: Partial<QAExperimentDetail> = {},
): QAExperimentDetail {
  return {
    id: "exp-v2",
    revision: 1,
    name: "golden-v2 vs control",
    hypothesis: "Type-aware checks and mutation hardening cut review escapes.",
    state: "running",
    arms: [
      { id: "control", label: "Control", flowHash: "control", weight: 50 },
      {
        id: "treatment",
        label: "golden-v2@1",
        flowHash: "sha256-v2",
        weight: 50,
      },
    ],
    scope: { repos: ["acme/api"] },
    allocation: { mode: "explicit", design: "paired" },
    primaryMetric: "quality.panel_score",
    coPrimaries: [
      "quality.mutation_score.added_lines",
      "bench.hidden_acceptance_pass",
    ],
    primaryContrast: "treatment:control",
    secondaries: ["review.findings_sev.per_pr"],
    guardrails: ["flow.time_to_pr"],
    plan: {
      plannedOriginsPerArm: 30,
      maxDays: 14,
      alpha: 0.05,
      mdeAck: false,
      power: {
        decisionRule: "paired-sign-flip-holm",
        simulatedRuns: 2000,
        observedEffect: { power: 0.82, label: "+0.44 panel points" },
        meaningfulEffect: { power: 0.91, label: "+0.50 panel points" },
        mde80: { label: "+0.43 panel points" },
      },
    },
    options: { shadowOnControl: true, memory: "frozen" },
    segment: 1,
    enrollmentHeld: "",
    enrolledByArm: { control: 19, treatment: 19 },
    auditTrail: [],
    ...overrides,
  };
}

function sampleResults(
  overrides: Partial<QAExperimentResults> = {},
): QAExperimentResults {
  return {
    experimentId: "exp-v2",
    state: "running",
    periodLabel: "Experiment period: Nov 13 – now (day 1 of 14)",
    verdicts: [
      {
        armId: "treatment",
        status: "Provisional",
        title: "golden-v2@1 leading:",
        summary:
          "0.6 higher panel score than control (+0.1 to +1.1). 11 more pairs needed.",
      },
    ],
    forestRows: [
      {
        id: "quality.panel_score",
        label: "Senior review score (1–10)",
        sub: "primary · paired difference",
        tip: "Mean of 3 blind raters on a 1–10 rubric.",
        ciLowPct: 52,
        ciWidthPct: 20,
        meanPct: 62,
        minLabel: "-2.0",
        maxLabel: "+2.0",
        valueStr: "+0.60 (+0.10 to +1.10)",
      },
    ],
    provisional: true,
    assignmentAudit: {
      passed: true,
      agreementPct: 100,
      checkedCount: 38,
      mismatches: [],
    },
    srm: { pValue: 1, counts: { control: 19, treatment: 19 } },
    modelMix: {
      balanced: true,
      pValue: 1,
      byArm: {
        control: { "gemini-pro": 19 },
        treatment: { "gemini-pro": 19 },
      },
    },
    guardrails: [
      {
        id: "flow.time_to_pr",
        label: "Time to pull request",
        value: "+8% median shift",
        state: "ok",
      },
    ],
    checkHealth: [
      {
        id: "golden:mutation-hardening",
        precision: "91.0%",
        applies: "68.0%",
        p95: "18s",
      },
    ],
    rubric: {
      validated: false,
      icc: 0.84,
      probeAuc: 0.54,
      dimensions: [
        {
          id: "overall",
          label: "Overall engineering quality",
          arms: [
            { armId: "control", mean: 7.4, ciLow: 7.0, ciHigh: 7.8 },
            { armId: "treatment", mean: 8.0, ciLow: 7.6, ciHigh: 8.4 },
          ],
        },
      ],
    },
    prShape: [
      {
        armId: "control",
        prsPerWorkTree: "1.0",
        prodLines: "142",
        testLines: "88",
        testProdRatio: "0.62",
      },
      {
        armId: "treatment",
        prsPerWorkTree: "1.0",
        prodLines: "108",
        testLines: "114",
        testProdRatio: "1.06",
      },
    ],
    limitations: {
      sharedMain: false,
      memoryMode: "frozen",
      memoryWritesByArm: {},
      exposureBreaks: 0,
    },
    ...overrides,
  };
}

class FakeExperimentsApi {
  calls: RecordedCall[] = [];
  summaries: QAExperimentSummary[] = [
    {
      id: "exp-v2",
      name: "golden-v2 vs control",
      state: "running",
      arms: sampleDetail().arms,
      primaryMetric: "quality.panel_score",
    },
  ];
  detail: QAExperimentDetail = sampleDetail();
  results: QAExperimentResults = sampleResults();

  async get(key: string, params?: unknown, query?: string): Promise<unknown> {
    this.calls.push({ method: "GET", route: key, params, query });
    if (key === "GET /api/qa/experiments") {
      return { experiments: this.summaries };
    }
    if (key === "GET /api/qa/experiments/{id}") {
      return this.detail;
    }
    if (key === "GET /api/qa/experiments/{id}/results") {
      return this.results;
    }
    if (key === "GET /api/qa/flow-versions") {
      return {
        flows: [
          { hash: "sha256-v2", key: "golden-v2", rev: 1, retired: false },
        ],
      };
    }
    if (key === "GET /api/qa/flow-versions/{hash}") {
      return {
        parentHash: "",
        spec: {
          skill: { text: "Verify callers and tests." },
          verifier: { enabled: true, tiers: ["T1", "T2", "T3"] },
        },
      };
    }
    return null;
  }

  async post(
    key: string,
    params?: unknown,
    body?: unknown,
    query?: string,
  ): Promise<unknown> {
    this.calls.push({ method: "POST", route: key, params, body, query });
    if (key === "POST /api/qa/experiments") {
      return { experiment: { id: "exp-new" } };
    }
    if (key === "POST /api/qa/flow-versions" && query === "dryRun=1") {
      return { wouldBlockPRs: 3 };
    }
    return { ok: true };
  }
}

async function mountQuality(
  url: string,
  api: FakeExperimentsApi,
  scopes: string[] = ["qa:read", "qa:admin"],
) {
  TestBed.configureTestingModule({
    providers: [
      provideRouter([
        { path: "telemetry", component: TelemetryQualityComponent },
      ]),
      { provide: GrainApi, useValue: api },
      {
        provide: SnackbarService,
        useValue: { show: () => undefined },
      },
    ],
  });
  const access = TestBed.inject(AccessService);
  access.setConfig({
    version: "v3.7.0-test",
    access: {
      role: "tester",
      roles: ["tester"],
      full: false,
      scopes,
      repoLimits: {},
    },
  });
  const harness = await RouterTestingHarness.create(url);
  await harness.fixture.whenStable();
  harness.fixture.detectChanges();
  return {
    harness,
    root: harness.routeNativeElement as HTMLElement,
    router: TestBed.inject(Router),
  };
}

describe("TelemetryQualityComponent (M10 v2 experiments)", () => {
  it("renders running experiment arm counts from SRM data, forest rows, rubric, and limitations", async () => {
    const api = new FakeExperimentsApi();
    const { root } = await mountQuality("/telemetry?exp=exp-v2", api);

    expect(
      root.querySelector('[data-testid="qa-arm-control"]')?.textContent,
    ).toContain("19 of 30 work trees");
    expect(
      root.querySelector('[data-testid="qa-arm-treatment"]')?.textContent,
    ).toContain("19 of 30 work trees");
    expect(
      root.querySelector('[data-testid="qa-audit-pill"]')?.textContent,
    ).toContain("Assignment checked");
    expect(
      root.querySelector('[data-testid="qa-forest-quality.panel_score"]')
        ?.textContent,
    ).toContain("valid at planned n only");
    expect(
      root.querySelector('[data-testid="qa-exp-rubric-card"]')?.textContent,
    ).toContain("Not validated");
    expect(
      root.querySelector('[data-testid="qa-exp-limitations-card"]')
        ?.textContent,
    ).toContain("Repository memory mode: frozen");
  });

  it("blocks results readout and lists mismatches when assignmentAudit fails", async () => {
    const api = new FakeExperimentsApi();
    api.results = sampleResults({
      assignmentAudit: {
        passed: false,
        agreementPct: 95,
        checkedCount: 20,
        mismatches: [
          {
            rootOrigin: "task:77",
            expectedArm: "control",
            actualArm: "treatment",
          },
        ],
      },
    });
    const { root } = await mountQuality("/telemetry?exp=exp-v2", api);

    expect(
      root.querySelector('[data-testid="qa-exp-audit-failed-card"]')
        ?.textContent,
    ).toContain("task:77 (expected control, got treatment)");
    expect(
      root.querySelector('[data-testid="qa-exp-results-card"]'),
    ).toBeNull();
  });

  it("renders draft start dialog with flow diff and sends start transition", async () => {
    const api = new FakeExperimentsApi();
    api.summaries = [{ ...api.summaries[0], state: "draft" }];
    api.detail = sampleDetail({ state: "draft" });
    const { harness, root } = await mountQuality("/telemetry?exp=exp-v2", api);

    expect(
      root.querySelector('[data-testid="qa-exp-draft-card"]')?.textContent,
    ).toContain("82.0%");

    root
      .querySelector<HTMLButtonElement>('[data-testid="btn-exp-open-start"]')!
      .click();
    harness.fixture.detectChanges();
    await harness.fixture.whenStable();
    harness.fixture.detectChanges();

    const dialog = root.querySelector('[data-testid="qa-start-dialog"]')!;
    expect(dialog.textContent).toContain("would block 3 pull requests");
    expect(dialog.textContent).toContain("Verify callers and tests.");

    dialog
      .querySelector<HTMLButtonElement>(
        '[data-testid="btn-confirm-start-exp"]',
      )!
      .click();
    await harness.fixture.whenStable();

    expect(
      api.calls.some(
        (c) =>
          c.route === "POST /api/qa/experiments/{id}/transition" &&
          (c.body as { action?: string })?.action === "start",
      ),
    ).toBe(true);
  });

  it("opens Create Experiment sheet via ?createExp=1&flow=sha256-v2 and enforces MDE ack on second treatment", async () => {
    const api = new FakeExperimentsApi();
    const { harness, root } = await mountQuality(
      "/telemetry?createExp=1&flow=sha256-v2",
      api,
    );

    const sheet = root.querySelector('[data-testid="qa-experiment-sheet"]')!;
    expect(sheet).not.toBeNull();

    const nameInput = sheet.querySelector<HTMLInputElement>(
      '[data-testid="input-exp-name"]',
    )!;
    nameInput.value = "three-arm-eval";
    nameInput.dispatchEvent(new Event("input"));

    sheet
      .querySelector<HTMLButtonElement>(
        '[data-testid="btn-toggle-second-treatment"]',
      )!
      .click();
    harness.fixture.detectChanges();

    const t2Input = sheet.querySelector<HTMLInputElement>(
      '[data-testid="input-exp-treatment2-flow"]',
    )!;
    t2Input.value = "sha256-v2-ablation";
    t2Input.dispatchEvent(new Event("input"));
    harness.fixture.detectChanges();

    const createBtn = sheet.querySelector<HTMLButtonElement>(
      '[data-testid="btn-exp-create"]',
    )!;
    // Disabled until MDE penalty checkbox is acknowledged.
    expect(createBtn.disabled).toBe(true);

    const mdeCheckbox = sheet.querySelector<HTMLInputElement>(
      '[data-testid="checkbox-exp-mde-ack"]',
    )!;
    mdeCheckbox.checked = true;
    mdeCheckbox.dispatchEvent(new Event("change"));
    harness.fixture.detectChanges();

    expect(createBtn.disabled).toBe(false);
    createBtn.click();
    await harness.fixture.whenStable();

    const post = api.calls.find((c) => c.route === "POST /api/qa/experiments");
    expect(post).toBeDefined();
    expect((post?.body as { name?: string })?.name).toBe("three-arm-eval");
  });

  it("shows qa:read permission notice when caller lacks qa:read and summary is absent", async () => {
    const api = new FakeExperimentsApi();
    const { root } = await mountQuality("/telemetry", api, ["tasks:read"]);
    expect(
      root.querySelector('[data-testid="qa-exp-denied"]')?.textContent,
    ).toContain("qa:read");
  });
});
