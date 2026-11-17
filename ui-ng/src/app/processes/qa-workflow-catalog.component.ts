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
  ElementRef,
  ViewEncapsulation,
  effect,
  inject,
  input,
  output,
  signal,
} from "@angular/core";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import type { WorkflowFamilyGroup } from "./qa-flows.models";

@Component({
  selector: "g-qa-workflow-catalog",
  imports: [StatusIconComponent, TipDirective],
  changeDetection: ChangeDetectionStrategy.OnPush,
  styleUrl: "./qa-workflow-catalog.css",
  encapsulation: ViewEncapsulation.None,
  template: `
    <section
      class="qa-catalog-shell"
      [class.is-collapsed]="collapsed()"
      aria-label="Software workflow catalog"
      data-testid="qa-workflow-catalog"
    >
      <div class="qa-catalog-header">
        @if (!collapsed()) {
          <h2 class="qa-catalog-heading">
            Workflows ({{ primaryGroups().length }})
          </h2>
        }
        <button
          type="button"
          class="qa-catalog-collapse-btn"
          data-testid="qa-catalog-collapse-btn"
          [attr.aria-label]="
            collapsed()
              ? 'Expand workflow catalog'
              : 'Collapse workflow catalog'
          "
          [attr.aria-expanded]="!collapsed()"
          [gTip]="
            collapsed()
              ? 'Expand workflow catalog'
              : 'Collapse catalog to widen the stage workspace'
          "
          [gTipPlacement]="collapsed() ? 'right' : 'below'"
          (click)="toggleCollapsed.emit()"
        >
          {{ collapsed() ? "»" : "«" }}
        </button>
      </div>

      <div class="qa-catalog-scroll" [hidden]="collapsed()">
        <div
          class="qa-catalog-list"
          role="navigation"
          aria-label="Primary software workflows"
          data-testid="quality-flows-list"
        >
          @for (group of primaryGroups(); track group.key) {
            <button
              type="button"
              class="qa-flow-family-card"
              [class.is-selected]="selectedKey() === group.key"
              [attr.aria-current]="selectedKey() === group.key ? 'true' : null"
              [attr.data-testid]="'qa-flow-row-' + group.key"
              (click)="selectFamily.emit(group.latestActiveHash)"
            >
              <div class="qa-family-top">
                <span class="qa-family-title">{{ group.displayTitle }}</span>
                <span [class]="'status-pill ' + rolloutPillClass(group.key)">
                  <g-status-icon [tone]="rolloutPillClass(group.key)" />
                  {{ rolloutLabel(group.key) }}
                </span>
              </div>
              <p class="qa-family-summary">{{ group.summary }}</p>
              <div class="qa-family-meta">
                <span>{{ group.activeStageCount }}/8 stages</span>
                <span aria-hidden="true">·</span>
                <span>{{ group.activeSubstepCount }} substeps</span>
                <span aria-hidden="true">·</span>
                <span>{{ formatBytes(group.promptBytes) }}</span>
                @if (group.versions.length > 1) {
                  <span aria-hidden="true">·</span>
                  <span>v{{ group.latestVersion }}</span>
                }
              </div>
            </button>
          }
        </div>

        @if (archivedGroups().length > 0) {
          <div class="qa-archived-section">
            <button
              type="button"
              class="qa-archived-toggle"
              data-testid="qa-toggle-archived-flows"
              [attr.aria-expanded]="showArchived()"
              gTip="Show or hide zero-LLM benchmark ablations and retired workflow families."
              (click)="showArchived.set(!showArchived())"
            >
              <span>
                {{
                  showArchived()
                    ? "Hide benchmark & archived variants"
                    : "Show benchmark & archived variants"
                }}
                ({{ archivedGroups().length }})
              </span>
              <span aria-hidden="true">{{ showArchived() ? "▾" : "▸" }}</span>
            </button>

            <div
              class="qa-archived-list"
              role="navigation"
              aria-label="Benchmark and archived workflows"
              [hidden]="!showArchived()"
            >
              @for (group of archivedGroups(); track group.key) {
                <button
                  type="button"
                  class="qa-flow-family-card"
                  [class.is-selected]="selectedKey() === group.key"
                  [attr.aria-current]="
                    selectedKey() === group.key ? 'true' : null
                  "
                  [attr.data-testid]="'qa-flow-row-' + group.key"
                  (click)="selectFamily.emit(group.latestActiveHash)"
                >
                  <div class="qa-family-top">
                    <span class="qa-family-title">{{
                      group.displayTitle
                    }}</span>
                    <span
                      [class]="'status-pill ' + rolloutPillClass(group.key)"
                    >
                      <g-status-icon [tone]="rolloutPillClass(group.key)" />
                      {{ rolloutLabel(group.key) }}
                    </span>
                  </div>
                  <p class="qa-family-summary">{{ group.summary }}</p>
                  <div class="qa-family-meta">
                    <span>{{ group.activeStageCount }}/8 stages</span>
                    <span aria-hidden="true">·</span>
                    <span>{{ group.activeSubstepCount }} substeps</span>
                    <span aria-hidden="true">·</span>
                    <span>{{ formatBytes(group.promptBytes) }}</span>
                  </div>
                </button>
              }
            </div>
          </div>
        }
      </div>
    </section>
  `,
})
export class QaWorkflowCatalogComponent {
  private readonly elRef = inject<ElementRef<HTMLElement>>(ElementRef);

  readonly primaryGroups = input<readonly WorkflowFamilyGroup[]>([]);
  readonly archivedGroups = input<readonly WorkflowFamilyGroup[]>([]);
  readonly selectedKey = input<string>("");
  readonly selectedHash = input<string>("");
  readonly flowQueryParam = input<string>("");
  readonly qaEnabled = input<boolean>(false);
  readonly activeFlowId = input<string>("");
  readonly activeFlowFamilyKey = input<string>("");
  readonly activeRolloutPct = input<number>(0);
  readonly collapsed = input<boolean>(false);

  readonly selectFamily = output<string>();
  readonly toggleCollapsed = output<void>();

  readonly showArchived = signal(false);

  constructor() {
    effect(() => {
      const archived = this.archivedGroups();
      if (archived.length === 0) return;
      const key = this.selectedKey();
      const hash = this.selectedHash();
      const qp = this.flowQueryParam().trim();
      const qpKey = qp.includes("@") ? qp.split("@")[0] : qp;
      const matchesArchived = archived.some(
        (g) =>
          g.key === key ||
          (qpKey !== "" && g.key === qpKey) ||
          g.versions.some(
            (v) => v.hash === hash || (qp !== "" && v.hash === qp),
          ),
      );
      if (matchesArchived) {
        const wasHidden = !this.showArchived();
        this.showArchived.set(true);
        if (wasHidden) {
          setTimeout(() => {
            const card = this.elRef.nativeElement.querySelector<HTMLElement>(
              ".qa-archived-list .qa-flow-family-card.is-selected",
            );
            card?.scrollIntoView?.({ block: "nearest" });
          }, 0);
        }
      }
    });
  }

  private isFamilyActiveDefault(flowKey: string): boolean {
    const familyKey = this.activeFlowFamilyKey() || this.activeFlowId();
    return Boolean(familyKey) && familyKey === flowKey;
  }

  protected rolloutPillClass(flowKey: string): string {
    if (this.isFamilyActiveDefault(flowKey)) {
      if (this.qaEnabled() && this.activeRolloutPct() > 0) {
        return "healthy";
      }
      return "warn";
    }
    return "neutral";
  }

  protected rolloutLabel(flowKey: string): string {
    if (this.isFamilyActiveDefault(flowKey)) {
      if (this.qaEnabled() && this.activeRolloutPct() > 0) {
        return `Active (${this.activeRolloutPct()}%)`;
      }
      return "Default · Paused (0%)";
    }
    return "Available";
  }

  protected formatBytes(bytes: number): string {
    if (bytes <= 0) return "0 B";
    if (bytes < 1000) return `${bytes} B`;
    return `${(bytes / 1000).toFixed(1)} kB`;
  }
}
