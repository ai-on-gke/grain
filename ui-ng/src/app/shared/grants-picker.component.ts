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
import { GrainApi } from "../api/grain-api.service";
import type {
  APIScopeSetRow,
  APIScopeSetsResponse,
  CapabilityMethod,
  CapabilityOption,
  GrantCheck,
} from "../api/models";
import { AccessService } from "../services/access.service";
import {
  errorText,
  grantName,
  isApiCapability,
} from "../sessions/session-helpers";
import { RouterLink } from "@angular/router";
import { type PillOption, PillSelectComponent } from "./pill-select.component";

const RUN_KIND_LABELS: Record<string, string> = {
  task: "agent tasks",
  process: "processes",
  ci: "CI jobs",
};

// policyVerdictText is a method's capability-policy verdict for a kind
// of run in words (classic GrantsPicker's policyVerdictText).
export function policyVerdictText(verdict: string, runKind: string): string {
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

// grantRef is the <capability>#<method> a recorded method grant names,
// without its =<with> arguments.
function grantRef(grant: string): string {
  const eq = grant.indexOf("=");
  return eq >= 0 ? grant.slice(0, eq) : grant;
}

// A method's standing with the policy, as a picker shows it.
export interface GrantVerdict {
  verdict: "denied" | "may_be_refused" | "ci_only" | "retired";
  badge: string;
  note: string;
}

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
              @let v = heldVerdict(g);
              <span [title]="v ? g + ' — ' + v.note : g">{{
                shortGrant(g)
              }}</span>
              @if (v) {
                <span
                  class="grant-verdict-badge"
                  [class.is-denied]="v.verdict === 'denied'"
                  [attr.data-testid]="'held-grant-verdict-' + $index"
                  [attr.data-verdict]="v.verdict"
                  >{{ v.badge }}</span
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
        @for (g of extraHeld(); track g) {
          @let v = heldVerdict(g);
          @if (v) {
            <p
              class="grant-verdict-note"
              role="status"
              [attr.data-testid]="'held-grant-note-' + $index"
            >
              <code>{{ shortGrant(g) }}</code
              >: {{ v.note }}
              @if (v.verdict === "denied" || v.verdict === "may_be_refused") {
                (<a
                  routerLink="/settings/capabilities"
                  fragment="capability-policy"
                  >Settings → Capabilities</a
                >)
              }
            </p>
          }
        }
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
                @if (!selectedVerdict() && flaggedMethods() > 0) {
                  <p
                    class="grant-verdict-note"
                    [attr.data-testid]="(testid() || 'gp') + '-method-policy'"
                  >
                    {{ flaggedMethods() }}
                    {{ flaggedMethods() === 1 ? "method is" : "methods are" }}
                    refused or may be refused by the capability policy, as each
                    says under its name (<a
                      routerLink="/settings/capabilities"
                      fragment="capability-policy"
                      >Settings → Capabilities</a
                    >).
                  </p>
                }
                @if (selectedVerdict(); as v) {
                  <p
                    class="grant-verdict-note"
                    role="status"
                    [attr.data-testid]="(testid() || 'gp') + '-method-verdict'"
                    [attr.data-verdict]="v.verdict"
                  >
                    This grant is {{ v.note }} (<a
                      routerLink="/settings/capabilities"
                      fragment="capability-policy"
                      >Settings → Capabilities</a
                    >).
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
  // runKind is the kind of run the grants are for ("task", "process" or
  // "ci"): the capability policy's verdict for it is shown on a method.
  readonly runKind = input<string>("task");

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

  // A method the policy denies the one run this picker is for (the repo
  // is known) stays listed, disabled, with the policy's own reason, so
  // the reader sees it exists and why it cannot be granted; one the
  // policy may refuse some run of the kind carries that verdict.
  protected readonly methodOptions = computed<PillOption[]>(() =>
    this.methods().map((m) => {
      const v = this.methodVerdict(m);
      const refused = v?.verdict === "denied" || v?.verdict === "ci_only";
      return {
        value: m.ref,
        label: m.description ? `${m.ref} — ${m.description}` : m.ref,
        ...(v ? { hint: v.note } : {}),
        ...(refused && (m.policyRefusal || v?.verdict === "ci_only")
          ? { disabled: true }
          : {}),
      };
    }),
  );

  // With the repo known the listing judged this task's one run, so a
  // method it does not refuse is admitted and the kind's "may be
  // refused" (true of some other repository) would only mislead. A CI
  // job's trigger event is still unknown, so it keeps the kind's verdict.
  private readonly kindVerdictShown = computed(
    () => !this.repo() || this.runKind() === "ci",
  );

  protected methodVerdict(m: CapabilityMethod): GrantVerdict | null {
    const kind = this.runKind();
    const policy = m.policy?.[kind] ?? "";
    if (policy === "ci_only") {
      return {
        verdict: "ci_only",
        badge: "CI only",
        note: m.policyRefusal || policyVerdictText("ci_only", kind),
      };
    }
    if (m.policyRefusal) {
      return {
        verdict: "denied",
        badge: "denied by policy",
        note: m.policyRefusal,
      };
    }
    if (!this.kindVerdictShown()) return null;
    if (policy === "denied" || policy === "may_be_refused") {
      return {
        verdict: policy,
        badge: policy === "denied" ? "denied by policy" : "may be refused",
        note: policyVerdictText(policy, kind),
      };
    }
    return null;
  }

  // flaggedMethods counts the listed methods the policy refuses or may
  // refuse, so the builder can point at the policy behind them.
  protected readonly flaggedMethods = computed(
    () =>
      this.methods().filter((m) => {
        const v = this.methodVerdict(m);
        return v && v.verdict !== "ci_only";
      }).length,
  );

  protected readonly selectedVerdict = computed(() => {
    const m = this.methods().find((x) => x.ref === this.selectedMethod());
    return m ? this.methodVerdict(m) : null;
  });

  // The grants checked against the server for being retired aliases
  // (POST /api/capability-grants/check), by grant.
  private readonly aliasChecks = signal<Record<string, string[]>>({});

  protected heldVerdict(g: string): GrantVerdict | null {
    const alias = this.aliasChecks()[g];
    if (alias?.length) {
      return {
        verdict: "retired",
        badge: "retired alias",
        note: `retired alias of ${alias.join(" and ")}. Remove it, or replace it with ${alias.length > 1 ? "those grants" : "that grant"}.`,
      };
    }
    const ref = grantRef(g);
    const m = this.methods().find((x) => x.ref === ref);
    const v = m ? this.methodVerdict(m) : null;
    if (!v) return null;
    return { ...v, note: `${v.note}.` };
  }

  constructor() {
    // A held method or bare grant is judged without opening the builder:
    // read the listing once, and ask the server which bare names are
    // retired aliases.
    effect(() => {
      const extra = this.extraHeld();
      if (!extra.length || this.disabled()) return;
      untracked(() => {
        void this.loadCatalogue();
        void this.checkAliases(extra);
      });
    });
  }

  private checkedAliases = new Set<string>();

  private async checkAliases(grants: readonly string[]): Promise<void> {
    const bare = grants.filter(
      (g) =>
        !g.includes("#") &&
        !g.startsWith("api:") &&
        !this.checkedAliases.has(g),
    );
    if (
      !bare.length ||
      !this.access.can("POST /api/capability-grants/check", this.repo())
    ) {
      return;
    }
    for (const g of bare) this.checkedAliases.add(g);
    try {
      const res = await this.api.post<GrantCheck[] | { checks?: GrantCheck[] }>(
        "POST /api/capability-grants/check",
        undefined,
        {
          grants: bare.map((grant) => ({ grant })),
        },
      );
      const list = Array.isArray(res) ? res : (res?.checks ?? []);
      const next = { ...this.aliasChecks() };
      list.forEach((c, i) => {
        if (c?.aliasOf?.length) next[c.grant || bare[i]] = c.aliasOf;
      });
      this.aliasChecks.set(next);
    } catch {
      // The badge is a hint; the held list works without it.
    }
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
    await this.loadCatalogue();
  }

  private async loadCatalogue(): Promise<void> {
    if (this.loadedCatalogue) return;
    this.loadedCatalogue = true;
    const repo = this.repo();
    const kind = encodeURIComponent(this.runKind());
    const query = repo
      ? `kind=${kind}&repo=${encodeURIComponent(repo)}`
      : `kind=${kind}`;
    const [setsRes, methodsRes] = await Promise.allSettled([
      this.access.can("GET /api/api-scope-sets", repo)
        ? this.api.get<APIScopeSetsResponse>("GET /api/api-scope-sets")
        : Promise.resolve({ sets: [] }),
      this.access.can("GET /api/capability-methods", repo)
        ? this.api.get<{ items?: CapabilityMethod[] }>(
            "GET /api/capability-methods",
            undefined,
            query,
          )
        : Promise.resolve({ items: [] }),
    ]);
    if (setsRes.status === "fulfilled") {
      const sets = setsRes.value?.sets ?? [];
      this.scopeSets.set(sets);
      if (!this.selectedApiSet() && sets[0]) {
        this.selectedApiSet.set(sets[0].name);
      }
    }
    if (methodsRes.status === "fulfilled") {
      const val = methodsRes.value as {
        items?: CapabilityMethod[];
        methods?: CapabilityMethod[];
      };
      const list = Array.isArray(val)
        ? val
        : (val?.items ?? val?.methods ?? []);
      this.methods.set(list);
      const first = list.find((m) => !m.policyRefusal) ?? null;
      if (!this.selectedMethod() && first) {
        this.selectedMethod.set(first.ref);
      }
    }
    if (!this.selectedApiSet()) {
      this.selectedApiSet.set(this.apiOptions()[0]?.value ?? "tasks-read");
    }
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
