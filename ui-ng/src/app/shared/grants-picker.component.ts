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
import { RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import type {
  APIScopeSetRow,
  APIScopeSetsResponse,
  CapabilityMethod,
  CapabilityOption,
  CapabilityPolicyVerdict,
  GrantCheck,
} from "../api/models";
import { AccessService } from "../services/access.service";
import {
  errorText,
  grantName,
  isApiCapability,
} from "../sessions/session-helpers";
import { methodMenuLabel, parseGrant } from "./capability-args";
import { type PillOption, PillSelectComponent } from "./pill-select.component";

export type GrantRunKind = "task" | "process" | "ci";

export interface GrantAttachEvent {
  // Simple capability toggle id (when toggling offered capabilities).
  id?: string;
  // Structured method/API grant (capability + optional with args) or raw grant string.
  capability?: string;
  with?: Record<string, unknown>;
  grant?: string;
  recorded: string;
}

export interface MethodPolicyInfo {
  verdict: CapabilityPolicyVerdict;
  refused: boolean;
  ciOnly: boolean;
  badge: string;
  note: string;
  linkKind: "policy" | "repo_access_doc";
}

const VALID_REPO_RE = /^([^/\s]+\/)?[^/\s]+\/[^/\s]+$/;

const RUN_KIND_LABELS: Readonly<Record<GrantRunKind, string>> = {
  task: "agent tasks",
  process: "processes",
  ci: "CI jobs",
};

const API_CAPABILITY_SERVER = "https://grain.dev/capabilities/api";

export const REPO_ACCESS_DOC =
  "https://github.com/ai-on-gke/grain/blob/main/docs/ci-capabilities.md#repository-access-declared-in-the-repository-enforced-by-the-teams-credential";
export const REPO_ACCESS_DOC_LABEL = "Repository access docs";

export function policyVerdictText(
  verdict: CapabilityPolicyVerdict | undefined,
  runKind: GrantRunKind,
): string {
  if (!verdict) return "";
  const forKind = RUN_KIND_LABELS[runKind]
    ? ` for ${RUN_KIND_LABELS[runKind]}`
    : "";
  if (verdict === "ci_only") {
    return `CI only: not a grant${forKind}; a CI job declares it in its workflow's capabilities:`;
  }
  if (verdict === "denied") {
    return `denied by the capability policy${forKind}`;
  }
  if (verdict === "may_be_refused") {
    return `may be refused by the capability policy${forKind}`;
  }
  return "";
}

export function resolveMethodPolicy(
  method: CapabilityMethod | undefined,
  runKind: GrantRunKind,
  kindVerdictShown: boolean,
): MethodPolicyInfo | null {
  if (!method) return null;
  if (method.policy?.[runKind] === "ci_only") {
    return {
      verdict: "ci_only",
      refused: true,
      ciOnly: true,
      badge: "CI only",
      note: policyVerdictText("ci_only", runKind),
      linkKind: "repo_access_doc",
    };
  }
  if (method.policyRefusal) {
    return {
      verdict: "denied",
      refused: true,
      ciOnly: false,
      badge: "denied by policy",
      note: method.policyRefusal,
      linkKind: "policy",
    };
  }
  const verdict = kindVerdictShown ? method.policy?.[runKind] : undefined;
  if (!verdict) return null;
  if (verdict === "denied") {
    return {
      verdict: "denied",
      refused: true,
      ciOnly: false,
      badge: "denied by policy",
      note: policyVerdictText("denied", runKind),
      linkKind: "policy",
    };
  }
  if (verdict === "may_be_refused") {
    return {
      verdict: "may_be_refused",
      refused: false,
      ciOnly: false,
      badge: "may be refused",
      note: policyVerdictText("may_be_refused", runKind),
      linkKind: "policy",
    };
  }
  return null;
}

@Component({
  selector: "g-grants-picker",
  imports: [PillSelectComponent, RouterLink],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./grants-picker.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="grants-picker" [attr.data-testid]="testid() || 'grants-picker'">
      @if (toggleItems().length) {
        <div class="grants-toggle-list" role="group" aria-label="Capabilities">
          @for (c of toggleItems(); track c.id) {
            @let on = heldSet().has(c.id);
            @let pol = policyForGrant(c.id);
            <button
              type="button"
              class="grant-pill"
              [attr.aria-pressed]="on"
              [disabled]="disabled() || (!on && !!pol?.refused)"
              [attr.data-testid]="(testid() || 'gp') + '-cap-' + c.id"
              [attr.data-policy-verdict]="pol?.verdict || null"
              [attr.data-policy-denied]="
                pol && pol.refused && !pol.ciOnly ? 'true' : null
              "
              [attr.data-ci-only]="pol?.ciOnly ? 'true' : null"
              [attr.title]="pol ? pol.note : c.description || null"
              (click)="onToggle(c.id, !on)"
            >
              {{ on ? "✓ " : "" }}{{ c.name || c.id }}
              @if (pol) {
                <span
                  class="grant-policy-badge"
                  [class.is-denied]="pol.verdict === 'denied'"
                  >{{ pol.badge }}</span
                >
              }
            </button>
          }
        </div>
      }

      @if (extraHeld().length) {
        <ul
          class="grants-held-list"
          aria-label="Attached method and API grants"
        >
          @for (g of extraHeld(); track g) {
            @let pol = policyForGrant(g);
            <li
              class="grant-held-chip"
              [class.is-policy-denied]="pol?.verdict === 'denied'"
              [class.is-policy-warn]="
                pol?.verdict === 'may_be_refused' || pol?.verdict === 'ci_only'
              "
              [attr.data-testid]="'held-grant-' + $index"
              [attr.data-policy-verdict]="pol?.verdict || null"
              [attr.data-policy-denied]="
                pol && pol.refused && !pol.ciOnly ? 'true' : null
              "
              [attr.data-ci-only]="pol?.ciOnly ? 'true' : null"
            >
              <span [title]="pol ? g + ' — ' + pol.note : g">{{
                shortGrant(g)
              }}</span>
              @if (pol) {
                <span
                  class="grant-policy-badge"
                  [class.is-denied]="pol.verdict === 'denied'"
                  >{{ pol.badge }}</span
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

      @for (item of heldPolicyWarnings(); track item.grant) {
        <p
          class="grant-policy-note"
          [class.is-denied]="item.policy.verdict === 'denied'"
          [attr.data-testid]="(testid() || 'gp') + '-held-policy-' + $index"
          [attr.data-method]="item.ref"
          [attr.data-policy-verdict]="item.policy.verdict"
          [attr.data-policy-denied]="
            item.policy.refused && !item.policy.ciOnly ? 'true' : null
          "
          [attr.data-ci-only]="item.policy.ciOnly ? 'true' : null"
        >
          <strong>{{ shortGrant(item.grant) }}:</strong>
          @if (item.policy.ciOnly) {
            This grant is {{ item.policy.note }}. Remove this grant: a CI job
            that needs it declares it in its own
            <code>.grain/workflows</code> file (<a
              [href]="repoAccessDoc"
              target="_blank"
              rel="noreferrer"
              >{{ repoAccessDocLabel }}</a
            >).
          } @else if (item.policy.refused) {
            {{ item.policy.note }}. Remove this grant, or change the capability
            policy (<a
              [routerLink]="['/settings', 'capabilities']"
              fragment="capability-policy"
              >Settings → Capabilities</a
            >).
          } @else {
            This grant is {{ item.policy.note }} (<a
              [routerLink]="['/settings', 'capabilities']"
              fragment="capability-policy"
              >Settings → Capabilities</a
            >).
          }
        </p>
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

            @if (activeBuilderPolicy(); as pol) {
              <p
                class="grant-policy-note"
                [class.is-denied]="pol.verdict === 'denied'"
                [attr.data-testid]="(testid() || 'gp') + '-selected-policy'"
                [attr.data-method]="activeBuilderRef()"
                [attr.data-policy-verdict]="pol.verdict"
                [attr.data-policy-denied]="
                  pol.refused && !pol.ciOnly ? 'true' : null
                "
                [attr.data-ci-only]="pol.ciOnly ? 'true' : null"
              >
                <span
                  class="grant-policy-badge"
                  [class.is-denied]="pol.verdict === 'denied'"
                  >{{ pol.badge }}</span
                >
                {{ " " }}{{ pol.note }}
                @if (pol.linkKind === "repo_access_doc") {
                  (<a [href]="repoAccessDoc" target="_blank" rel="noreferrer">{{
                    repoAccessDocLabel
                  }}</a
                  >)
                } @else {
                  (<a
                    [routerLink]="['/settings', 'capabilities']"
                    fragment="capability-policy"
                    >Settings → Capabilities</a
                  >)
                }
              </p>
            }

            @if (mode() === "method" && flaggedMethods().length > 0) {
              <ul
                class="grant-policy-list"
                aria-label="Capability policy notices"
                [attr.data-testid]="(testid() || 'gp') + '-policy-verdicts'"
              >
                @for (row of flaggedMethods(); track row.method.ref) {
                  <li
                    class="grant-policy-item"
                    [attr.data-method]="row.method.ref"
                    [attr.data-policy-verdict]="row.policy.verdict"
                    [attr.data-policy-denied]="
                      row.policy.refused && !row.policy.ciOnly ? 'true' : null
                    "
                    [attr.data-ci-only]="row.policy.ciOnly ? 'true' : null"
                  >
                    <div class="grant-policy-item-head">
                      <span
                        class="grant-policy-item-name"
                        [class.is-refused]="row.policy.refused"
                        >{{
                          methodLabel(row.method.ref, row.method.method)
                        }}</span
                      >
                      <span
                        class="grant-policy-badge"
                        [class.is-denied]="row.policy.verdict === 'denied'"
                        >{{ row.policy.badge }}</span
                      >
                    </div>
                    <span class="grant-policy-item-note">
                      {{ row.policy.note }}
                      @if (row.policy.linkKind === "repo_access_doc") {
                        (<a
                          [href]="repoAccessDoc"
                          target="_blank"
                          rel="noreferrer"
                          >{{ repoAccessDocLabel }}</a
                        >)
                      } @else {
                        (<a
                          [routerLink]="['/settings', 'capabilities']"
                          fragment="capability-policy"
                          >Settings → Capabilities</a
                        >)
                      }
                    </span>
                  </li>
                }
              </ul>
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
                [disabled]="checking() || !!activeBuilderPolicy()?.refused"
                [attr.data-testid]="(testid() || 'gp') + '-attach'"
                (click)="attachFromBuilder()"
              >
                {{ checking() ? "Checking…" : "Add grant" }}
              </button>
              <button
                type="button"
                class="btn-text"
                [attr.data-testid]="(testid() || 'gp') + '-add-close'"
                (click)="builderOpen.set(false)"
              >
                Done
              </button>
            </div>
          </div>
        }
      }
    </div>
  `,
})
export class GrantsPickerComponent {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);

  protected readonly repoAccessDoc = REPO_ACCESS_DOC;
  protected readonly repoAccessDocLabel = REPO_ACCESS_DOC_LABEL;

  readonly offered = input<readonly CapabilityOption[]>([]);
  readonly held = input<readonly string[]>([]);
  readonly repo = input<string | undefined>("");
  readonly runKind = input<GrantRunKind>("task");
  readonly disabled = input<boolean>(false);
  readonly testid = input<string>("");

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
  private loadedScopeSets = false;
  private methodsSeq = 0;

  protected readonly subjectRepo = computed(() => {
    const r = (this.repo() ?? "").trim();
    return VALID_REPO_RE.test(r) ? r : "";
  });

  // With the repo known, the listing has judged a task's or process's one
  // run, so a method without policyRefusal is admitted: a kind-wide
  // "may_be_refused" from some other repo would only mislead. A CI job's
  // trigger event is still unknown, so it keeps the kind verdict.
  protected readonly kindVerdictShown = computed(() => {
    const kind = this.runKind();
    return Boolean(kind) && (!this.subjectRepo() || kind === "ci");
  });

  protected readonly methodsByRef = computed(() => {
    const map = new Map<string, CapabilityMethod>();
    for (const m of this.methods()) {
      map.set(m.ref, m);
    }
    return map;
  });

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

  protected readonly heldPolicyWarnings = computed(() => {
    const out: { grant: string; ref: string; policy: MethodPolicyInfo }[] = [];
    for (const g of this.held()) {
      const ref = this.grantRef(g);
      const policy = this.policyForRef(ref);
      if (policy) {
        out.push({ grant: g, ref, policy });
      }
    }
    return out;
  });

  protected readonly apiOptions = computed<PillOption[]>(() => {
    const sets = this.scopeSets();
    const list: APIScopeSetRow[] = sets.length
      ? sets.filter((s) => !s.catalogue?.disabled)
      : [{ name: "tasks-read" }, { name: "ci-read" }, { name: "repos-read" }];
    return list.map((s) => {
      const pol = this.policyForRef(`${API_CAPABILITY_SERVER}#${s.name}`);
      return {
        value: s.name,
        label: s.description
          ? `api:${s.name} — ${s.description}`
          : `api:${s.name}`,
        ...(pol ? { hint: `${pol.badge} — ${pol.note}` } : {}),
      };
    });
  });

  protected readonly methodOptions = computed<PillOption[]>(() =>
    this.methods().map((m) => {
      const pol = resolveMethodPolicy(
        m,
        this.runKind(),
        this.kindVerdictShown(),
      );
      return {
        value: m.ref,
        label: m.description ? `${m.ref} — ${m.description}` : m.ref,
        ...(pol ? { hint: `${pol.badge} — ${pol.note}` } : {}),
      };
    }),
  );

  protected readonly flaggedMethods = computed(() => {
    const kind = this.runKind();
    const showKind = this.kindVerdictShown();
    const out: { method: CapabilityMethod; policy: MethodPolicyInfo }[] = [];
    for (const m of this.methods()) {
      const policy = resolveMethodPolicy(m, kind, showKind);
      if (policy) out.push({ method: m, policy });
    }
    return out;
  });

  protected readonly activeBuilderRef = computed(() => {
    const mode = this.mode();
    if (mode === "api") {
      const set = (
        this.selectedApiSet() ||
        this.apiOptions()[0]?.value ||
        ""
      ).trim();
      return set ? `${API_CAPABILITY_SERVER}#${set}` : "";
    }
    if (mode === "method") {
      return (
        this.selectedMethod() ||
        this.methodOptions()[0]?.value ||
        ""
      ).trim();
    }
    const raw = this.rawGrant().trim();
    return raw ? this.grantRef(raw) : "";
  });

  protected readonly activeBuilderPolicy = computed<MethodPolicyInfo | null>(
    () => {
      const ref = this.activeBuilderRef();
      return ref ? this.policyForRef(ref) : null;
    },
  );

  constructor() {
    effect(() => {
      const kind = this.runKind();
      const repo = this.subjectRepo();
      const want =
        this.builderOpen() ||
        this.held().length > 0 ||
        this.offered().length > 0;
      if (!want) return;
      untracked(() => void this.loadMethods(kind, repo));
    });
  }

  protected methodLabel(ref: string, method: string): string {
    return methodMenuLabel(ref, method);
  }

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

  protected grantRef(g: string): string {
    const trimmed = g.trim();
    if (trimmed.startsWith("api:")) {
      const body = trimmed.slice(4);
      const name = body.split("@")[0];
      return name ? `${API_CAPABILITY_SERVER}#${name}` : trimmed;
    }
    return parseGrant(trimmed).ref;
  }

  protected policyForRef(ref: string): MethodPolicyInfo | null {
    if (!ref) return null;
    const m = this.methodsByRef().get(ref);
    return resolveMethodPolicy(m, this.runKind(), this.kindVerdictShown());
  }

  protected policyForGrant(g: string): MethodPolicyInfo | null {
    return this.policyForRef(this.grantRef(g));
  }

  protected onToggle(id: string, nextOn: boolean): void {
    if (this.disabled()) return;
    if (nextOn && this.policyForGrant(id)?.refused) return;
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
    const repo = this.subjectRepo();
    const jobs: Promise<unknown>[] = [this.loadMethods(this.runKind(), repo)];
    if (!this.loadedScopeSets) {
      this.loadedScopeSets = true;
      if (this.access.can("GET /api/api-scope-sets", repo || this.repo())) {
        jobs.push(
          this.api
            .get<APIScopeSetsResponse>("GET /api/api-scope-sets")
            .then((res) => {
              const sets = res?.sets ?? [];
              this.scopeSets.set(sets);
              if (!this.selectedApiSet() && sets[0]) {
                this.selectedApiSet.set(sets[0].name);
              }
            })
            .catch(() => {
              // Fall back to default API set list.
            }),
        );
      }
    }
    await Promise.allSettled(jobs);
    if (!this.selectedApiSet()) {
      this.selectedApiSet.set(this.apiOptions()[0]?.value ?? "tasks-read");
    }
  }

  private async loadMethods(kind: GrantRunKind, repo: string): Promise<void> {
    if (!this.access.can("GET /api/capability-methods", repo || this.repo())) {
      return;
    }
    const seq = ++this.methodsSeq;
    const query = repo
      ? `kind=${encodeURIComponent(kind)}&repo=${encodeURIComponent(repo)}`
      : undefined;
    try {
      const val = await this.api.get<
        | CapabilityMethod[]
        | { items?: CapabilityMethod[]; methods?: CapabilityMethod[] }
      >("GET /api/capability-methods", undefined, query);
      if (seq !== this.methodsSeq) return;
      const list = Array.isArray(val)
        ? val
        : (val?.items ?? val?.methods ?? []);
      this.methods.set(list.filter((m) => typeof m?.ref === "string"));
      const cur = this.selectedMethod();
      if (!cur || !list.some((m) => m.ref === cur)) {
        const firstAllowed = list.find(
          (m) =>
            !resolveMethodPolicy(m, kind, this.kindVerdictShown())?.refused,
        );
        this.selectedMethod.set(firstAllowed?.ref ?? list[0]?.ref ?? "");
      }
    } catch {
      // Leave existing list untouched on transient load error.
    }
  }

  protected async attachFromBuilder(): Promise<void> {
    if (this.checking()) return;
    this.checkError.set(null);
    const activePol = this.activeBuilderPolicy();
    if (activePol?.refused) {
      this.checkError.set(activePol.note);
      return;
    }
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
        this.methodOptions()[0]?.value ||
        ""
      ).trim();
      if (!ref) {
        this.checkError.set("Choose a capability method.");
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
      const repo = this.subjectRepo();
      if (
        this.access.can(
          "POST /api/capability-grants/check",
          repo || this.repo(),
        )
      ) {
        const res = await this.api.post<
          GrantCheck[] | { checks?: GrantCheck[] }
        >("POST /api/capability-grants/check", undefined, {
          grants: [reqItem],
          ...(repo && this.runKind() !== "ci" ? { subject: { repo } } : {}),
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
