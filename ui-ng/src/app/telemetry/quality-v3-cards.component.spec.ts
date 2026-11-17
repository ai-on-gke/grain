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
import type { QAExperimentResults } from "./qa-experiments.models";
import { QualityV3CardsComponent } from "./quality-v3-cards.component";

describe("QualityV3CardsComponent", () => {
  let fixture: ComponentFixture<QualityV3CardsComponent>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [QualityV3CardsComponent],
    }).compileComponents();

    fixture = TestBed.createComponent(QualityV3CardsComponent);
    fixture.detectChanges();
  });

  it("renders declarationDiff, stageFunnel, and S5 triageMetrics for version-vs-version comparisons", () => {
    const results: QAExperimentResults = {
      referenceArmId: "golden-v2",
      declarationDiff: {
        addedStages: ["s0-intent", "s2-plan-critic"],
        removedStages: [],
        changedSkills: ["intent", "plan-critic"],
        addedCustomChecks: ["custom:schema-needs-migration"],
        removedCustomChecks: [],
        changedChecks: ["golden:ui-budget-and-spec-floor"],
        changedVerifier: false,
        changedTriage: true,
      },
      stageFunnel: [
        {
          armId: "golden-v3",
          stage: "S2",
          runs: 8,
          applicablePct: 40,
          findingsCount: 3,
          repairedInNextShaPct: 67,
          durationP50Ms: 3200,
          durationP95Ms: 6800,
          tokensMedian: 4200,
          notHelpfulWilsonLB: 0,
        },
        {
          armId: "golden-v3",
          stage: "S3",
          runs: 8,
          applicablePct: 50,
          findingsCount: 4,
          repairedInNextShaPct: 25,
          durationP50Ms: 4100,
          durationP95Ms: 7900,
          tokensMedian: 5100,
          notHelpfulWilsonLB: 35,
        },
      ],
      triageMetrics: [
        {
          armId: "golden-v3",
          mode: "enforced",
          autoMergeEligiblePct: 50,
          humanRoutedPct: 50,
          autoMergedEscapeRate: 0,
          humanRoutedTruePositiveRate: 60,
        },
        {
          armId: "golden-v3-noisy",
          mode: "shadow",
          autoMergeEligiblePct: 40,
          humanRoutedPct: 60,
          autoMergedEscapeRate: 8,
          humanRoutedTruePositiveRate: 45,
        },
      ],
      verdicts: [],
      forestRows: [],
      provisional: false,
      assignmentAudit: { passed: true, agreementPct: 100, mismatches: [] },
      srm: { pValue: 1, counts: { "golden-v2": 20, "golden-v3": 20 } },
      modelMix: { balanced: true, pValue: 1, byArm: {} },
      guardrails: [],
      checkHealth: [],
      rubric: null,
      prShape: [],
      limitations: {
        sharedMain: false,
        memoryMode: "frozen",
        memoryWritesByArm: {},
        exposureBreaks: 0,
      },
    };

    fixture.componentRef.setInput("results", results);
    fixture.detectChanges();

    const root = fixture.nativeElement as HTMLElement;
    const scopedRoot = root.querySelector(
      '[data-testid="quality-v3-cards"].quality-v3-cards',
    );
    expect(scopedRoot).not.toBeNull();

    const diffStrip = root.querySelector(
      '[data-testid="v3-declaration-diff"]',
    )!;
    expect(diffStrip.textContent).toContain("Changed vs. golden-v2:");
    expect(diffStrip.textContent).toContain("+s0-intent");
    expect(diffStrip.textContent).toContain("+1 custom checks");
    expect(diffStrip.textContent).toContain("Triage changed");
    expect(
      diffStrip.querySelector(".status-pill.healthy")?.textContent,
    ).toContain("+s0-intent");
    expect(
      diffStrip.querySelector(".status-pill.blast")?.textContent,
    ).toContain("1 changed checks");
    expect(root.querySelector(".status-pill.success")).toBeNull();
    expect(root.querySelector(".status-pill.warning")).toBeNull();

    const funnel = root.querySelector('[data-testid="v3-stage-funnel"]')!;
    expect(funnel.textContent).toContain("67%");
    expect(funnel.textContent).toContain("6800 ms");

    const triage = root.querySelector('[data-testid="v3-triage-metrics"]')!;
    expect(triage.textContent).toContain("enforced");
    expect(triage.textContent).toContain("50%");
    expect(triage.textContent).toContain("60%");
  });
});
