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
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import type { Template } from "../api/models";
import { AccessService } from "../services/access.service";
import { errText } from "../settings/settings-state.service";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";

export interface PlanItem {
  templateId: string;
  // repeat is the box's own text, so a half-typed or refused number
  // stays on screen until it is fixed.
  repeat: string;
  dependsOn: string[];
}

export interface PlanForm {
  requireApproval: boolean;
  autoPromote: boolean;
  items: PlanItem[];
}

interface PlanWire {
  requireApproval?: boolean;
  autoPromote?: boolean;
  items?:
    | { templateId: string; repeat?: number; dependsOn?: string[] | null }[]
    | null;
}

// planFormOf is a plan as GET .../qualification-plan sends it, in the
// form's own shape.
export function planFormOf(plan: PlanWire | null | undefined): PlanForm {
  return {
    requireApproval: Boolean(plan?.requireApproval),
    autoPromote: Boolean(plan?.autoPromote),
    items: (plan?.items ?? []).map((it) => ({
      templateId: it.templateId,
      repeat: String(it.repeat ?? 1),
      dependsOn: [...(it.dependsOn ?? [])],
    })),
  };
}

export function planFormKey(f: PlanForm): string {
  return JSON.stringify([
    f.requireApproval,
    f.autoPromote,
    f.items.map((it) => [it.templateId, it.repeat.trim(), it.dependsOn]),
  ]);
}

// withoutDanglingDependencies drops, from every item, a dependency on a
// template no other item names any more. "Depends on" only offers the
// other items' templates, so such a dependency is one the editor could not
// show to untick, and the server refuses the whole plan for it
// (grain/task-2414; the classic editor's rule in RepoReleases.jsx).
export function withoutDanglingDependencies(items: PlanItem[]): PlanItem[] {
  return items.map((it, i) => {
    const named = new Set(
      items
        .filter((o, j) => j !== i && o.templateId !== "")
        .map((o) => o.templateId),
    );
    const dependsOn = it.dependsOn.filter((id) => named.has(id));
    return dependsOn.length === it.dependsOn.length ? it : { ...it, dependsOn };
  });
}

// repeatError says what is wrong with a typed repeat count, or "" when it
// is a whole number of at least one (model.QualificationPlan.Validate).
export function repeatError(text: string): string {
  const t = text.trim();
  if (!/^\d+$/.test(t) || Number(t) < 1) {
    return "Repeat must be a whole number of at least 1.";
  }
  return "";
}

// planPayload is the form in PUT .../qualification-plan's shape: the
// whole plan, which the server stores wholesale. A row with no template
// chosen is left out, as the classic editor does.
export function planPayload(f: PlanForm): {
  requireApproval: boolean;
  autoPromote: boolean;
  items: { templateId: string; repeat: number; dependsOn: string[] }[];
} {
  return {
    requireApproval: f.requireApproval,
    autoPromote: f.autoPromote,
    items: f.items
      .filter((it) => it.templateId !== "")
      .map((it) => ({
        templateId: it.templateId,
        repeat: Number(it.repeat.trim()) || 1,
        dependsOn: it.dependsOn,
      })),
  };
}

// qualifiableTemplates is every template repo's plan may schedule: the
// unbound ones and those bound to repo itself, never a built-in one
// (grain/task-285) -- a qualification run always targets this repo's own
// candidate, so the server refuses a template bound elsewhere.
export function qualifiableTemplates(
  templates: readonly Template[],
  repo: string,
): Template[] {
  const want = repo.toLowerCase();
  return templates.filter(
    (t) => !t.system && (!t.repo || t.repo.toLowerCase() === want),
  );
}

const PLAN_PATHS = [
  "tables/qualification_config.json",
  "tables/qualification_item.json",
];

// RepoQualificationPlanComponent is the /next/ port of the classic
// Releases pane's QualificationPlanEditor (bwsalmon/agents#518): the two
// switches and every item, edited in place and stored together by one
// Save. The parent page reads dirty() for its unsaved-edit guard and
// calls revert() when the person discards.
@Component({
  selector: "g-repo-qualification-plan",
  imports: [PillSelectComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="s-card" data-testid="card-repo-qualification-plan">
      <div class="s-card-head">
        <div>
          <h2 class="s-card-title">Qualification plan</h2>
          <p class="s-card-desc">
            Templates a fresh, active release candidate schedules automatically
            — run this many times each, in dependency order, against the
            candidate's own branch. Templates bound to another repository are
            left out: grain refuses them, since a qualification run always
            targets this repository's own candidate.
          </p>
        </div>
      </div>

      @if (configRepoOnly()) {
        <p class="s-hint" data-testid="qualification-plan-config-managed">
          {{ repo() }}'s qualification plan is managed in the config repository
          ({{ planPaths.join(", ") }}); edit it there.
        </p>
      } @else if (denied()) {
        @let why = denied()!;
        <p
          class="s-hint"
          id="qualification-plan-denied"
          data-testid="qualification-plan-denied"
        >
          {{ why }}
        </p>
      }

      @if (loadError(); as err) {
        <p class="s-field-error" role="alert">
          Could not read the qualification plan: {{ err }}
        </p>
      } @else if (!loaded()) {
        <p class="s-hint">Loading qualification plan…</p>
      } @else {
        <div class="s-toggle-row">
          <div class="s-toggle-copy">
            <span class="s-toggle-name"
              >Require approval before running qualification tasks</span
            >
          </div>
          <button
            type="button"
            class="g-switch"
            role="switch"
            data-testid="switch-qualification-require-approval"
            aria-label="Require approval before running qualification tasks"
            [attr.aria-checked]="form().requireApproval"
            [attr.aria-describedby]="describedBy()"
            [disabled]="readOnly()"
            (click)="patch({ requireApproval: !form().requireApproval })"
          ></button>
        </div>
        <div class="s-toggle-row">
          <div class="s-toggle-copy">
            <span class="s-toggle-name"
              >Promote automatically once qualification succeeds</span
            >
          </div>
          <button
            type="button"
            class="g-switch"
            role="switch"
            data-testid="switch-qualification-auto-promote"
            aria-label="Promote automatically once qualification succeeds"
            [attr.aria-checked]="form().autoPromote"
            [attr.aria-describedby]="describedBy()"
            [disabled]="readOnly()"
            (click)="patch({ autoPromote: !form().autoPromote })"
          ></button>
        </div>

        @if (templates().length === 0) {
          <p class="s-hint" data-testid="qualification-no-templates">
            No templates this repository can qualify yet — create one under
            Processes first.
          </p>
        }

        @if (form().items.length === 0) {
          <p class="s-hint" data-testid="qualification-plan-empty">
            No items: a new candidate schedules nothing.
          </p>
        }

        <ol class="repo-plan-items" aria-label="Qualification items">
          @for (it of form().items; track $index; let i = $index) {
            @let rErr = repeatErrors()[i];
            <li
              class="repo-plan-item"
              [attr.data-testid]="'qualification-item-' + i"
            >
              <div class="repo-plan-item-main">
                <div class="s-field repo-plan-template">
                  <span class="s-label">Template</span>
                  <g-pill-select
                    align="left"
                    placeholder="Choose a template"
                    [testid]="'qualification-item-template-' + i"
                    [ariaLabel]="'Item ' + (i + 1) + ' template'"
                    [options]="templateOptions(it.templateId)"
                    [value]="it.templateId"
                    [disabled]="readOnly()"
                    (valueChange)="updateItem(i, { templateId: $event })"
                  />
                </div>
                <div class="s-field repo-plan-repeat">
                  <label class="s-label" [for]="'qualification-repeat-' + i"
                    >Repeat</label
                  >
                  <input
                    class="s-input s-num"
                    type="number"
                    min="1"
                    [id]="'qualification-repeat-' + i"
                    [attr.data-testid]="'qualification-item-repeat-' + i"
                    [value]="it.repeat"
                    [disabled]="readOnly()"
                    [attr.aria-invalid]="rErr ? 'true' : null"
                    [attr.aria-describedby]="
                      rErr ? 'qualification-repeat-err-' + i : null
                    "
                    (input)="
                      updateItem(i, { repeat: $any($event.target).value })
                    "
                  />
                  @if (rErr) {
                    <span
                      class="s-field-error"
                      [id]="'qualification-repeat-err-' + i"
                      >{{ rErr }}</span
                    >
                  }
                </div>
              </div>
              <div class="s-field">
                <span class="s-label">Depends on</span>
                @let others = otherTemplates(i);
                @if (others.length === 0) {
                  <span class="s-hint">No other items yet</span>
                } @else {
                  <div
                    class="repo-plan-deps"
                    role="group"
                    [attr.aria-label]="'Item ' + (i + 1) + ' depends on'"
                  >
                    @for (dep of others; track dep) {
                      @let on = it.dependsOn.includes(dep);
                      <button
                        type="button"
                        class="repo-toggle-pill"
                        [attr.aria-pressed]="on"
                        [attr.data-testid]="
                          'qualification-item-dep-' + i + '-' + dep
                        "
                        [disabled]="readOnly()"
                        (click)="toggleDep(i, dep)"
                      >
                        {{ on ? "✓ " : "" }}{{ templateName(dep) }}
                      </button>
                    }
                  </div>
                }
              </div>
              <div class="repo-plan-item-foot">
                <button
                  type="button"
                  class="btn-text s-btn-sm"
                  [attr.data-testid]="'qualification-item-remove-' + i"
                  [attr.aria-label]="
                    'Remove item ' +
                    (i + 1) +
                    (it.templateId
                      ? ' (' + templateName(it.templateId) + ')'
                      : '')
                  "
                  [disabled]="readOnly()"
                  (click)="removeItem(i)"
                >
                  Remove
                </button>
              </div>
            </li>
          }
        </ol>

        <div class="s-inline">
          <button
            type="button"
            class="btn-tonal s-btn-sm"
            data-testid="qualification-add-item"
            [disabled]="readOnly() || templates().length === 0"
            (click)="addItem()"
          >
            Add item
          </button>
          <button
            type="button"
            class="btn-primary s-btn-sm"
            data-testid="save-qualification-plan"
            [disabled]="readOnly() || saving() || !dirty() || hasFieldErrors()"
            [attr.aria-describedby]="
              saveError() ? 'qualification-plan-error' : describedBy()
            "
            (click)="save()"
          >
            Save qualification plan
          </button>
          @if (dirty()) {
            <span class="s-caption" data-testid="qualification-plan-unsaved"
              >Unsaved changes</span
            >
          }
        </div>
        @if (saveError(); as err) {
          <p
            id="qualification-plan-error"
            class="s-field-error"
            role="alert"
            data-testid="qualification-plan-error"
          >
            {{ err }}
          </p>
        }
      }
    </div>
  `,
})
export class RepoQualificationPlanComponent {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);

  readonly repo = input.required<string>();
  readonly configRepoOnly = input(false);
  // reloadKey moves when the page reloads its Releases tab.
  readonly reloadKey = input(0);

  protected readonly planPaths = PLAN_PATHS;
  protected readonly loaded = signal(false);
  protected readonly loadError = signal<string | null>(null);
  protected readonly saving = signal(false);
  // saveError is the server's refusal of the last Save, said under the
  // form and cleared by the next edit.
  protected readonly saveError = signal<string | null>(null);
  private readonly allTemplates = signal<Template[]>([]);
  protected readonly form = signal<PlanForm>(planFormOf(null));
  private readonly baseline = signal(planFormKey(planFormOf(null)));

  protected readonly templates = computed(() =>
    qualifiableTemplates(this.allTemplates(), this.repo()),
  );

  readonly dirty = computed(
    () => !this.readOnly() && planFormKey(this.form()) !== this.baseline(),
  );

  protected readonly repeatErrors = computed(() =>
    this.form().items.map((it) => repeatError(it.repeat)),
  );

  protected readonly hasFieldErrors = computed(() =>
    this.repeatErrors().some(Boolean),
  );

  constructor() {
    effect(() => {
      this.repo();
      this.reloadKey();
      untracked(() => void this.load());
    });
  }

  protected readOnly(): boolean {
    return (
      this.configRepoOnly() ||
      !this.access.can(
        "PUT /api/repos/{owner}/{name}/qualification-plan",
        this.repo(),
      )
    );
  }

  protected denied(): string {
    return this.access.deniedReason(
      "PUT /api/repos/{owner}/{name}/qualification-plan",
      this.repo(),
    );
  }

  protected describedBy(): string | null {
    return !this.configRepoOnly() && this.denied()
      ? "qualification-plan-denied"
      : null;
  }

  private async load(): Promise<void> {
    const p = splitRepo(this.repo());
    if (
      !this.access.can(
        "GET /api/repos/{owner}/{name}/qualification-plan",
        this.repo(),
      )
    ) {
      this.loadError.set(
        this.access.deniedReason(
          "GET /api/repos/{owner}/{name}/qualification-plan",
          this.repo(),
        ),
      );
      return;
    }
    try {
      const [plan, templates] = await Promise.all([
        this.api.get<PlanWire>(
          "GET /api/repos/{owner}/{name}/qualification-plan",
          p,
        ),
        this.access.can("GET /api/templates")
          ? this.api
              .get<Template[] | { templates?: Template[] }>(
                "GET /api/templates",
              )
              .catch(() => [] as Template[])
          : Promise.resolve([] as Template[]),
      ]);
      this.allTemplates.set(
        Array.isArray(templates) ? templates : (templates?.templates ?? []),
      );
      // A reload never overwrites an edit nobody has saved.
      if (!this.dirty()) this.takeOn(planFormOf(plan));
      this.loadError.set(null);
      this.loaded.set(true);
    } catch (err) {
      this.loadError.set(errText(err));
    }
  }

  private takeOn(f: PlanForm): void {
    this.form.set(f);
    this.baseline.set(planFormKey(f));
  }

  // revert puts the form back on what the server last said: the page's
  // unsaved-edit guard calls it when the person chooses Discard.
  revert(): void {
    const saved = JSON.parse(this.baseline()) as [
      boolean,
      boolean,
      [string, string, string[]][],
    ];
    this.form.set({
      requireApproval: saved[0],
      autoPromote: saved[1],
      items: saved[2].map(([templateId, repeat, dependsOn]) => ({
        templateId,
        repeat,
        dependsOn,
      })),
    });
    this.saveError.set(null);
  }

  protected patch(p: Partial<PlanForm>): void {
    this.saveError.set(null);
    this.form.update((f) => ({ ...f, ...p }));
  }

  protected updateItem(i: number, p: Partial<PlanItem>): void {
    this.patch({
      items: withoutDanglingDependencies(
        this.form().items.map((it, idx) => (idx === i ? { ...it, ...p } : it)),
      ),
    });
  }

  protected removeItem(i: number): void {
    this.patch({
      items: withoutDanglingDependencies(
        this.form().items.filter((_, idx) => idx !== i),
      ),
    });
  }

  protected addItem(): void {
    this.patch({
      items: [
        ...this.form().items,
        { templateId: "", repeat: "1", dependsOn: [] },
      ],
    });
  }

  protected toggleDep(i: number, dep: string): void {
    const cur = this.form().items[i]?.dependsOn ?? [];
    this.updateItem(i, {
      dependsOn: cur.includes(dep)
        ? cur.filter((d) => d !== dep)
        : [...cur, dep],
    });
  }

  protected otherTemplates(i: number): string[] {
    const items = this.form().items;
    return items
      .filter((o, j) => j !== i && o.templateId !== "")
      .map((o) => o.templateId);
  }

  protected templateName(id: string): string {
    const t = this.allTemplates().find((x) => x.id === id);
    return t ? t.name || t.title || t.id : id;
  }

  protected templateOptions(current: string): PillOption[] {
    const opts: PillOption[] = [
      { value: "", label: "Choose a template", disabled: true },
      ...this.templates().map((t) => ({
        value: t.id,
        label: t.name || t.title || t.id,
        ...(t.repo ? { hint: `bound to ${t.repo}` } : {}),
      })),
    ];
    // A plan can name a template that has since gone, or one this list
    // leaves out; draw it rather than show the slot as empty.
    if (current && !opts.some((o) => o.value === current)) {
      opts.push({
        value: current,
        label: this.templateName(current),
        hint: "not a template this repository can qualify",
        disabled: true,
      });
    }
    return opts;
  }

  protected async save(): Promise<void> {
    if (this.hasFieldErrors()) return;
    const sent = this.form();
    this.saving.set(true);
    this.saveError.set(null);
    try {
      await this.api.put(
        "PUT /api/repos/{owner}/{name}/qualification-plan",
        splitRepo(this.repo()),
        planPayload(sent),
      );
      // What was sent is the new baseline, less the rows Save dropped
      // for having no template. An edit made while the request was in
      // flight stays on screen, dirty against that baseline.
      const stored = planFormOf(planPayload(sent));
      if (this.form() === sent) {
        this.takeOn(stored);
      } else {
        this.baseline.set(planFormKey(stored));
      }
    } catch (err) {
      this.saveError.set(errText(err));
    } finally {
      this.saving.set(false);
    }
  }
}
