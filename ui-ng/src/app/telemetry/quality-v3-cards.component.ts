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

import {
  ChangeDetectionStrategy,
  Component,
  ViewEncapsulation,
  input,
} from "@angular/core";
import type { QAExperimentResults } from "./qa-experiments.models";

@Component({
  selector: "g-quality-v3-cards",
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./quality-v3-cards.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="quality-v3-cards" data-testid="quality-v3-cards">
      @if (results()?.declarationDiff; as diff) {
        <div class="diff-strip" data-testid="v3-declaration-diff">
          <strong
            >Changed vs. {{ results()?.referenceArmId ?? "control" }}:</strong
          >
          @for (s of diff.addedStages ?? []; track s) {
            <span class="status-pill healthy">+{{ s }}</span>
          }
          @for (s of diff.removedStages ?? []; track s) {
            <span class="status-pill error">-{{ s }}</span>
          }
          @if ((diff.addedCustomChecks ?? []).length) {
            <span class="status-pill healthy"
              >+{{ (diff.addedCustomChecks ?? []).length }} custom checks</span
            >
          }
          @if ((diff.removedCustomChecks ?? []).length) {
            <span class="status-pill error"
              >-{{ (diff.removedCustomChecks ?? []).length }} custom
              checks</span
            >
          }
          @if ((diff.changedChecks ?? []).length) {
            <span class="status-pill blast"
              >{{ (diff.changedChecks ?? []).length }} changed checks</span
            >
          }
          @if (diff.changedVerifier) {
            <span class="status-pill blast">Verifier changed</span>
          }
          @if (diff.changedTriage) {
            <span class="status-pill blast">Triage changed</span>
          }
        </div>
      }

      @if ((results()?.stageFunnel ?? []).length) {
        <div class="ops-card" data-testid="v3-stage-funnel">
          <h3>Stage Funnel &amp; Cost Attribution</h3>
          <div
            class="ops-table-wrap"
            tabindex="0"
            role="region"
            aria-label="Stage funnel and cost attribution"
          >
            <table class="ops-table">
              <thead>
                <tr>
                  <th scope="col">Stage</th>
                  <th scope="col">Arm</th>
                  <th scope="col">Runs / Applies</th>
                  <th scope="col">Findings</th>
                  <th scope="col">Repaired next SHA</th>
                  <th scope="col">p95 latency</th>
                  <th scope="col">Median tokens</th>
                </tr>
              </thead>
              <tbody>
                @for (
                  row of results()?.stageFunnel ?? [];
                  track row.armId + "-" + row.stage
                ) {
                  <tr>
                    <td>{{ row.stage }}</td>
                    <td class="mono">{{ row.armId }}</td>
                    <td class="tabular">
                      {{ row.runs }}
                      <span class="muted">({{ row.applicablePct }}%)</span>
                    </td>
                    <td class="tabular">{{ row.findingsCount }}</td>
                    <td class="tabular">{{ row.repairedInNextShaPct }}%</td>
                    <td class="tabular">{{ row.durationP95Ms }} ms</td>
                    <td class="tabular">{{ row.tokensMedian }}</td>
                  </tr>
                }
              </tbody>
            </table>
          </div>
        </div>
      }

      @if ((results()?.triageMetrics || []).length) {
        <div class="ops-card" data-testid="v3-triage-metrics">
          <h3>Human-Review Triage (S5)</h3>
          <div
            class="ops-table-wrap"
            tabindex="0"
            role="region"
            aria-label="Human-review triage metrics"
          >
            <table class="ops-table">
              <thead>
                <tr>
                  <th scope="col">Arm</th>
                  <th scope="col">Mode</th>
                  <th scope="col">Reviews saved</th>
                  <th scope="col">Human-routed</th>
                  <th scope="col">Auto-merged escape rate</th>
                  <th scope="col">Human-routed precision</th>
                </tr>
              </thead>
              <tbody>
                @for (row of results()?.triageMetrics || []; track row.armId) {
                  <tr>
                    <td class="mono">{{ row.armId }}</td>
                    <td>
                      <span class="status-pill">{{ row.mode }}</span>
                    </td>
                    <td class="tabular">{{ row.autoMergeEligiblePct }}%</td>
                    <td class="tabular">{{ row.humanRoutedPct }}%</td>
                    <td class="tabular">{{ row.autoMergedEscapeRate }}%</td>
                    <td class="tabular">
                      {{ row.humanRoutedTruePositiveRate }}%
                    </td>
                  </tr>
                }
              </tbody>
            </table>
          </div>
        </div>
      }
    </div>
  `,
})
export class QualityV3CardsComponent {
  readonly results = input<QAExperimentResults>();
}
