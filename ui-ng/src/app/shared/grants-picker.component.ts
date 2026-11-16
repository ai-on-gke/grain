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
  output,
  signal,
  untracked,
} from "@angular/core";
import { NgTemplateOutlet } from "@angular/common";
import { GrainApi } from "../api/grain-api.service";
import type {
  APIScopeSetRow,
  APIScopeSetsResponse,
  CapabilityMethod,
  CapabilityOption,
  CapabilityRunKind,
  GrantCheck,
} from "../api/models";
import { RouterLink } from "@angular/router";
import { AccessService } from "../services/access.service";
import { RETIRED_IN_PROCESS } from "../settings/capability-retired";
import {
  errorText,
  grantName,
  isApiCapability,
} from "../sessions/session-helpers";
import {
  CAPABILITY_POLICY_FRAGMENT,
  CAPABILITY_POLICY_PATH,
  type CapabilityVerdict,
  grantRef,
  methodVerdict,
} from "./capability-verdict";
import { type PillOption, PillSelectComponent } from "./pill-select.component";

// RETIRED_GRANT_HINT is what a held capability id this build no longer
// offers says (classic's RETIRED_CAPABILITY_HINT, ui/src/state.js): the
// row is kept so the grant can be taken off, which is all it is for.
export const RETIRED_GRANT_HINT = "No longer offered — remove it";

export interface GrantAttachEvent {
  // Simple capability toggle id (when toggling offered capabilities).
  id?: string;
  // Structured method/API grant (capability + optional with args) or raw grant string.
  capability?: string;
  with?: Record<string, unknown>;
  grant?: string;
  recorded: string;
}

@Component({
  selector: "g-grants-picker",
  imports: [NgTemplateOutlet, PillSelectComponent, RouterLink],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./grants-picker.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="grants-picker" [attr.data-testid]="testid() || 'grants-picker'">
      @if (toggleItems().length) {
        <div class="grants-toggle-list" role="group" aria-label="Capabilities">
          @for (c of toggleItems(); track c.id) {
            @let on = heldSet().has(c.id);
            <button
              type="button"
              class="grant-pill"
              [attr.aria-pressed]="on"
              [disabled]="disabled()"
              [attr.data-testid]="(testid() || 'gp') + '-cap-' + c.id"
              (click)="onToggle(c.id, !on)"
            >
              {{ on ? "✓ " : "" }}{{ c.name || c.id }}
            </button>
            @if (verdictFor(c.id); as v) {
              <ng-container
                *ngTemplateOutlet="verdictLink; context: { $implicit: v }"
              />
            }
          }
        </div>
      }

      @if (extraHeld().length) {
        <ul
          class="grants-held-list"
          aria-label="Attached method and API grants"
        >
          @for (g of extraHeld(); track g) {
            <li
              class="grant-held-chip"
              [attr.data-testid]="'held-grant-' + $index"
            >
              <span [title]="g">{{ shortGrant(g) }}</span>
              @if (verdictFor(g); as v) {
                <ng-container
                  *ngTemplateOutlet="verdictLink; context: { $implicit: v }"
                />
              } @else if (isRetired(g)) {
                <span
                  class="grant-retired"
                  [attr.data-testid]="'retired-grant-' + $index"
                  >{{ retiredHint }}</span
                >
              }
              @if (!disabled()) {
                <button
                  type="button"
                  class="grant-held-remove"
                  [attr.aria-label]="'Remove grant ' + g"
                  [attr.data-testid]="'remove-grant-' + $index"
                  (click)="onRemoveGrant(g)"
                >
                  ×
                </button>
              }
            </li>
          }
        </ul>
      }

      @if (!disabled() && canReadCatalogue()) {
        @if (!builderOpen()) {
          <div>
            <button
              type="button"
              class="btn-text"
              [attr.data-testid]="(testid() || 'gp') + '-add-open'"
              (click)="openBuilder()"
            >
              + Add API scope or method grant
            </button>
          </div>
        } @else {
          <div
            class="grants-builder"
            [attr.data-testid]="(testid() || 'gp') + '-builder'"
          >
            <div class="grants-mode-tabs" role="group" aria-label="Grant kind">
              <button
                type="button"
                class="grant-pill"
                [attr.aria-pressed]="mode() === 'api'"
                [attr.data-testid]="(testid() || 'gp') + '-mode-api'"
                (click)="mode.set('api')"
              >
                API scope
              </button>
              <button
                type="button"
                class="grant-pill"
                [attr.aria-pressed]="mode() === 'method'"
                [attr.data-testid]="(testid() || 'gp') + '-mode-method'"
                (click)="mode.set('method')"
              >
                Capability method
              </button>
              <button
                type="button"
                class="grant-pill"
                [attr.aria-pressed]="mode() === 'raw'"
                [attr.data-testid]="(testid() || 'gp') + '-mode-raw'"
                (click)="mode.set('raw')"
              >
                Custom grant
              </button>
            </div>

            @switch (mode()) {
              @case ("api") {
                <div class="grants-builder-row">
                  <g-pill-select
                    [options]="apiOptions()"
                    [value]="selectedApiSet()"
                    placeholder="Select API scope set"
                    ariaLabel="API scope set"
                    align="left"
                    [testid]="(testid() || 'gp') + '-api-set'"
                    (valueChange)="selectedApiSet.set($event)"
                  />
                  <input
                    type="text"
                    class="grants-builder-input"
                    placeholder="Optional @repo (e.g. owner/name)"
                    aria-label="Scope repository filter"
                    [attr.data-testid]="(testid() || 'gp') + '-api-repo'"
                    [value]="apiRepoFilter()"
                    (input)="apiRepoFilter.set($any($event.target).value)"
                  />
                </div>
              }
              @case ("method") {
                <div class="grants-builder-row">
                  <g-pill-select
                    [options]="methodOptions()"
                    [value]="selectedMethod()"
                    placeholder="Select capability method"
                    ariaLabel="Capability method"
                    align="left"
                    [testid]="(testid() || 'gp') + '-method-ref'"
                    (valueChange)="selectedMethod.set($event)"
                  />
                  <input
                    type="text"
                    class="grants-builder-input"
                    placeholder='Optional JSON args, e.g. {"space":"ops"}'
                    aria-label="Method JSON arguments"
                    [attr.data-testid]="(testid() || 'gp') + '-method-args'"
                    [value]="methodArgsJson()"
                    (input)="methodArgsJson.set($any($event.target).value)"
                  />
                </div>
                @if (verdictFor(selectedMethod()); as v) {
                  <p
                    class="grant-verdict-note"
                    role="status"
                    [attr.data-testid]="(testid() || 'gp') + '-method-verdict'"
                    [attr.data-policy-verdict]="v.verdict"
                  >
                    This grant is {{ v.badge }}: {{ v.text }}.
                    <a [routerLink]="policyPath" [fragment]="policyFragment"
                      >Capability policy</a
                    >
                  </p>
                }
              }
              @case ("raw") {
                <div class="grants-builder-row">
                  <input
                    type="text"
                    class="grants-builder-input"
                    placeholder="e.g. api:tasks-read or server#method"
                    aria-label="Grant string"
                    [attr.data-testid]="(testid() || 'gp') + '-raw-input'"
                    [value]="rawGrant()"
                    (input)="rawGrant.set($any($event.target).value)"
                  />
                </div>
              }
            }

            @if (checkError(); as err) {
              <p
                class="banner banner-error"
                role="alert"
                [attr.data-testid]="(testid() || 'gp') + '-error'"
              >
                {{ err }}
              </p>
            }

            <div class="grants-builder-row">
              <button
                type="button"
                class="btn-tonal"
                [disabled]="checking()"
                [attr.data-testid]="(testid() || 'gp') + '-attach'"
                (click)="attachFromBuilder()"
              >
                Add grant
              </button>
              <button
                type="button"
                class="btn-text"
                (click)="builderOpen.set(false)"
              >
                Done
              </button>
            </div>
          </div>
        }
      }
    </div>

    <ng-template #verdictLink let-v>
      <a
        class="grant-verdict"
        [class.is-warning]="v.verdict === 'may_be_refused'"
        [attr.data-policy-verdict]="v.verdict"
        [attr.aria-label]="v.text + ' — open the Capability policy'"
        [title]="v.text"
        [routerLink]="policyPath"
        [fragment]="policyFragment"
        >{{ v.badge }}</a
      >
    </ng-template>
  `,
})
export class GrantsPickerComponent {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);

  readonly offered = input<readonly CapabilityOption[]>([]);
  readonly held = input<readonly string[]>([]);
  readonly repo = input<string | undefined>(undefined);
  readonly disabled = input<boolean>(false);
  readonly testid = input<string>("");
  // runKind is the kind of run these grants are for; every verdict is the
  // policy's for that kind.
  readonly runKind = input<CapabilityRunKind>("task");
  // judgeRun asks the listing about the one run these grants go to (a
  // task on repo, or one with no repo) so a verdict is that run's. Turn
  // it off where the grants serve runs on any repository -- a template
  // with no repository, the deployment's defaults -- to show the kind's
  // verdict instead.
  readonly judgeRun = input<boolean>(true);

  // Emits full updated string[] for state-list forms (Sessions, New Task),
  // AND granular attach/detach events for TaskDetail's POST .../capabilities!
  readonly heldChange = output<string[]>();
  readonly attachGrant = output<GrantAttachEvent>();
  readonly detachGrant = output<{ id?: string; grant?: string }>();

  protected readonly builderOpen = signal(false);
  protected readonly mode = signal<"api" | "method" | "raw">("api");
  protected readonly scopeSets = signal<APIScopeSetRow[]>([]);
  protected readonly methods = signal<CapabilityMethod[]>([]);
  protected readonly selectedApiSet = signal("");
  protected readonly apiRepoFilter = signal("");
  protected readonly selectedMethod = signal("");
  protected readonly methodArgsJson = signal("");
  protected readonly rawGrant = signal("");
  protected readonly checking = signal(false);
  protected readonly checkError = signal<string | null>(null);
  private loadedCatalogue = false;
  private methodsKey = "";

  protected readonly policyPath = CAPABILITY_POLICY_PATH;
  protected readonly policyFragment = CAPABILITY_POLICY_FRAGMENT;
  protected readonly retiredHint = RETIRED_GRANT_HINT;

  constructor() {
    // The methods (and the policy's verdict on each) are read as soon as
    // the picker is shown, and again whenever the run they are judged for
    // changes, so a held grant says it is refused before anybody opens
    // the builder.
    effect(() => {
      const repo = this.repo();
      const kind = this.runKind();
      const judge = this.judgeRun();
      untracked(() => void this.loadMethods(repo, kind, judge));
    });
  }

  protected readonly methodsByRef = computed(
    () => new Map(this.methods().map((m) => [m.ref, m])),
  );

  protected verdictFor(grant: string): CapabilityVerdict | null {
    if (!grant) return null;
    return methodVerdict(
      this.methodsByRef().get(grantRef(grant)),
      this.runKind(),
      this.judgeRun(),
    );
  }

  // isRetired is a held bare capability id this build no longer offers
  // (classic's capabilityRows): neither a toggle, a method, nor an API
  // grant, kept as a row only so it can be removed.
  protected isRetired(grant: string): boolean {
    if (!grant || isApiCapability(grant) || grant.startsWith("api:")) {
      return false;
    }
    if (grant.includes("#") || grant.includes("://")) return false;
    if (this.offered().some((c) => c.id === grant)) return false;
    // Where the picker was handed no offered list, only an id grain is
    // known to have retired is called one.
    return (
      Object.hasOwn(RETIRED_IN_PROCESS, grant) || this.offered().length > 0
    );
  }

  protected readonly heldSet = computed(() => new Set(this.held()));

  protected readonly toggleItems = computed(() => {
    const has = this.heldSet();
    return this.offered().filter(
      (c) => (c.ready !== false || has.has(c.id)) && !isApiCapability(c.id),
    );
  });

  protected readonly extraHeld = computed(() => {
    const shown = new Set(this.toggleItems().map((c) => c.id));
    return this.held().filter((g) => !shown.has(g));
  });

  protected readonly apiOptions = computed<PillOption[]>(() => {
    const sets = this.scopeSets();
    if (!sets.length) {
      return [
        { value: "tasks-read", label: "api:tasks-read" },
        { value: "ci-read", label: "api:ci-read" },
        { value: "repos-read", label: "api:repos-read" },
      ];
    }
    return sets
      .filter((s) => !s.catalogue?.disabled)
      .map((s) => ({
        value: s.name,
        label: s.description
          ? `api:${s.name} — ${s.description}`
          : `api:${s.name}`,
      }));
  });

  // Every method is listed: one the policy refuses this run (or that only
  // a CI job may hold) stays in the list, disabled, with the reason under
  // it, rather than vanishing; one it may refuse says so.
  protected readonly methodOptions = computed<PillOption[]>(() =>
    this.methods().map((m) => {
      const v = this.verdictFor(m.ref);
      return {
        value: m.ref,
        label: m.description ? `${m.ref} — ${m.description}` : m.ref,
        ...(v
          ? {
              disabled: v.verdict !== "may_be_refused",
              hint: `${v.badge}: ${v.text}`,
            }
          : {}),
      };
    }),
  );

  protected canReadCatalogue(): boolean {
    return (
      this.access.can("GET /api/capability-methods", this.repo()) ||
      this.access.can("GET /api/api-scope-sets", this.repo()) ||
      this.access.can("POST /api/capability-grants/check", this.repo())
    );
  }

  protected shortGrant(g: string): string {
    if (g.startsWith("api:")) return g;
    return grantName(g);
  }

  protected onToggle(id: string, nextOn: boolean): void {
    if (this.disabled()) return;
    const cur = this.held();
    const method = this.methods().find((m) => m.ref === id);
    const idsToAdd =
      method?.aliasOf?.length && method.aliasOf.length > 1
        ? method.aliasOf
        : [id];
    let next: string[] = [...cur];
    if (nextOn) {
      for (const alias of idsToAdd) {
        if (!next.includes(alias)) next.push(alias);
      }
    } else {
      next = next.filter((x) => !idsToAdd.includes(x));
    }
    this.heldChange.emit(next);
    if (nextOn) {
      this.attachGrant.emit({ id, recorded: id });
    } else {
      this.detachGrant.emit({ id });
    }
  }

  protected onRemoveGrant(g: string): void {
    if (this.disabled()) return;
    this.heldChange.emit(this.held().filter((x) => x !== g));
    this.detachGrant.emit({ grant: g });
  }

  protected async openBuilder(): Promise<void> {
    this.builderOpen.set(true);
    this.checkError.set(null);
    if (this.loadedCatalogue) return;
    this.loadedCatalogue = true;
    const repo = this.repo();
    const [setsRes] = await Promise.allSettled([
      this.access.can("GET /api/api-scope-sets", repo)
        ? this.api.get<APIScopeSetsResponse>("GET /api/api-scope-sets")
        : Promise.resolve({ sets: [] }),
    ]);
    if (setsRes.status === "fulfilled") {
      const sets = setsRes.value?.sets ?? [];
      this.scopeSets.set(sets);
      if (!this.selectedApiSet() && sets[0]) {
        this.selectedApiSet.set(sets[0].name);
      }
    }
    this.pickDefaultMethod();
    if (!this.selectedApiSet()) {
      this.selectedApiSet.set(this.apiOptions()[0]?.value ?? "tasks-read");
    }
  }

  private async loadMethods(
    repo: string | undefined,
    kind: CapabilityRunKind,
    judge: boolean,
  ): Promise<void> {
    if (!this.access.can("GET /api/capability-methods", repo)) return;
    const query = judge
      ? repo
        ? `kind=${kind}&repo=${encodeURIComponent(repo)}`
        : `kind=${kind}`
      : undefined;
    const key = query ?? "";
    this.methodsKey = key;
    try {
      const val = (await this.api.get<unknown>(
        "GET /api/capability-methods",
        undefined,
        query,
      )) as
        | CapabilityMethod[]
        | { items?: CapabilityMethod[]; methods?: CapabilityMethod[] }
        | null;
      // A slower answer for a repo since left behind must not overwrite
      // the verdicts of the one now chosen.
      if (this.methodsKey !== key) return;
      const list = Array.isArray(val)
        ? val
        : (val?.items ?? val?.methods ?? []);
      this.methods.set(list);
      this.pickDefaultMethod();
    } catch {
      // The listing is a nicety here: without it the picker offers raw
      // grants and the server still judges each one when it is added.
    }
  }

  // pickDefaultMethod preselects the first method a grant can name, never
  // one the builder lists disabled.
  private pickDefaultMethod(): void {
    const cur = this.methodOptions().find(
      (o) => o.value === this.selectedMethod(),
    );
    if (cur && !cur.disabled) return;
    const first = this.methodOptions().find((o) => !o.disabled);
    if (first) this.selectedMethod.set(first.value);
  }

  protected async attachFromBuilder(): Promise<void> {
    if (this.checking()) return;
    this.checkError.set(null);
    const mode = this.mode();
    let reqItem: {
      capability?: string;
      with?: Record<string, unknown>;
      grant?: string;
    };
    let fallbackRecorded = "";

    if (mode === "api") {
      const set = (
        this.selectedApiSet() ||
        this.apiOptions()[0]?.value ||
        ""
      ).trim();
      if (!set) {
        this.checkError.set("Choose an API scope set.");
        return;
      }
      const repoSuffix = this.apiRepoFilter().trim().replace(/^@/, "");
      const grant = repoSuffix ? `api:${set}@${repoSuffix}` : `api:${set}`;
      reqItem = { grant };
      fallbackRecorded = grant;
    } else if (mode === "method") {
      const ref = (
        this.selectedMethod() ||
        this.methodOptions().find((o) => !o.disabled)?.value ||
        ""
      ).trim();
      if (!ref) {
        this.checkError.set("Choose a capability method.");
        return;
      }
      const v = this.verdictFor(ref);
      if (v && v.verdict !== "may_be_refused") {
        this.checkError.set(`${ref} is ${v.badge}: ${v.text}.`);
        return;
      }
      let parsedWith: Record<string, unknown> | undefined;
      const rawArgs = this.methodArgsJson().trim();
      if (rawArgs) {
        try {
          const obj = JSON.parse(rawArgs) as unknown;
          if (!obj || typeof obj !== "object" || Array.isArray(obj)) {
            this.checkError.set("Method arguments must be a JSON object.");
            return;
          }
          parsedWith = obj as Record<string, unknown>;
        } catch {
          this.checkError.set("Method arguments must be valid JSON.");
          return;
        }
      }
      reqItem = {
        capability: ref,
        ...(parsedWith ? { with: parsedWith } : {}),
      };
      fallbackRecorded = parsedWith
        ? `${ref}=${JSON.stringify(parsedWith)}`
        : `${ref}={}`;
    } else {
      const g = this.rawGrant().trim();
      if (!g) {
        this.checkError.set("Enter a capability or API grant.");
        return;
      }
      reqItem = { grant: g };
      fallbackRecorded = g;
    }

    this.checking.set(true);
    try {
      let recorded = fallbackRecorded;
      if (this.access.can("POST /api/capability-grants/check", this.repo())) {
        const res = await this.api.post<
          GrantCheck[] | { checks?: GrantCheck[] }
        >("POST /api/capability-grants/check", undefined, {
          grants: [reqItem],
          ...(this.repo() ? { subject: { repo: this.repo() } } : {}),
        });
        const list = Array.isArray(res) ? res : (res?.checks ?? []);
        const check = list[0];
        if (check?.error) {
          this.checkError.set(check.error);
          return;
        }
        if (check?.recorded) recorded = check.recorded;
      }
      const cur = this.held();
      if (!cur.includes(recorded)) {
        this.heldChange.emit([...cur, recorded]);
      }
      this.attachGrant.emit({
        ...reqItem,
        recorded,
      });
      this.rawGrant.set("");
      this.methodArgsJson.set("");
    } catch (err) {
      this.checkError.set(errorText(err));
    } finally {
      this.checking.set(false);
    }
  }
}
