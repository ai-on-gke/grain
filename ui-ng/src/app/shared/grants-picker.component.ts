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
  DestroyRef,
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
import { type PillOption, PillSelectComponent } from "./pill-select.component";

// What the capability policy says about one grant for this form's run,
// as the classic GrantsPicker words it (ui/src/components/GrantsPicker.jsx).
export interface GrantPolicyVerdict {
  badge: string;
  note: string;
}

const RUN_KIND_LABELS: Record<string, string> = {
  task: "agent tasks",
  process: "processes",
  ci: "CI jobs",
};

// A repository still being typed is no run to ask about.
const REPO_RE = /^[^/\s]+\/[^/\s]+$/;

const API_CAPABILITY = "https://grain.dev/capabilities/api#";

/**
 * grantRef is the method a held grant names: its text before any "=",
 * with an agent API grant in its stored spelling (`api:<name>` or
 * `api:<name>@<repo>`, model.StoredGrantSpelling) read as the
 * capability-method ref the listing names it by
 * (https://grain.dev/capabilities/api#<name>) -- the reference the
 * capability policy judges it under.
 */
export function grantRef(grant: string): string {
  const eq = grant.indexOf("=");
  const ref = eq < 0 ? grant : grant.slice(0, eq);
  if (ref.startsWith("api:")) {
    const name = ref.slice("api:".length).split("@")[0];
    return API_CAPABILITY + name;
  }
  return ref;
}

/**
 * policyVerdictFor is the capability policy's word on a grant of method m
 * for runKind: refused outright for this run (policyRefusal, on a listing
 * that named the repository), CI only, or -- where no repository is known
 * yet -- the kind's own "denied" / "may be refused".
 */
export function policyVerdictFor(
  m: CapabilityMethod | undefined,
  runKind: string,
  repoKnown: boolean,
): GrantPolicyVerdict | null {
  if (!m) return null;
  const forKind = RUN_KIND_LABELS[runKind]
    ? ` for ${RUN_KIND_LABELS[runKind]}`
    : "";
  const kindVerdict = m.policy?.[runKind] ?? "";
  if (kindVerdict === "ci_only") {
    return {
      badge: "CI only",
      note: `CI only: not a grant${forKind}; a CI job declares it in its workflow's capabilities:.`,
    };
  }
  if (m.policyRefusal) {
    return {
      badge: "denied by policy",
      note: `${m.policyRefusal}. Remove this grant, or change the capability policy (Settings → Capabilities).`,
    };
  }
  if (repoKnown && runKind !== "ci") return null;
  if (kindVerdict === "denied") {
    return {
      badge: "denied by policy",
      note: `Denied by the capability policy${forKind} (Settings → Capabilities).`,
    };
  }
  if (kindVerdict === "may_be_refused") {
    return {
      badge: "may be refused",
      note: `May be refused by the capability policy${forKind} (Settings → Capabilities).`,
    };
  }
  return null;
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
  imports: [PillSelectComponent],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./grants-picker.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="grants-picker" [attr.data-testid]="testid() || 'grants-picker'">
      @if (toggleItems().length) {
        <div class="grants-toggle-list" role="group" aria-label="Capabilities">
          @for (c of toggleItems(); track c.id) {
            @let on = heldSet().has(c.id);
            @let pv = verdictOf(c.id);
            <button
              type="button"
              class="grant-pill"
              [class.has-policy]="!!pv"
              [attr.aria-pressed]="on"
              [disabled]="
                disabled() || (!on && pv?.badge === 'denied by policy')
              "
              [attr.title]="pv?.note || null"
              [attr.data-testid]="(testid() || 'gp') + '-cap-' + c.id"
              (click)="onToggle(c.id, !on)"
            >
              {{ on ? "✓ " : "" }}{{ c.name || c.id }}
              @if (pv) {
                <span
                  class="grant-policy-badge"
                  [attr.data-testid]="(testid() || 'gp') + '-policy-' + c.id"
                  >{{ pv.badge }}</span
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
            @let hv = verdictOf(g);
            <li
              class="grant-held-chip"
              [class.has-policy]="!!hv"
              [attr.data-testid]="'held-grant-' + $index"
            >
              <span [title]="g">{{ shortGrant(g) }}</span>
              @if (hv) {
                <span
                  class="grant-policy-badge"
                  [attr.data-testid]="'held-grant-policy-' + $index"
                  >{{ hv.badge }}</span
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

      @if (heldVerdicts().length) {
        <ul
          class="grant-policy-notes"
          aria-label="Capability policy"
          [attr.data-testid]="(testid() || 'gp') + '-policy-notes'"
        >
          @for (n of heldVerdicts(); track n.grant) {
            <li>
              <code>{{ shortGrant(n.grant) }}</code
              >: {{ n.verdict.note }}
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
  /**
   * policyVerdicts has the picker read the capability policy's verdict on
   * each method for runKind on repo, flag a held grant the policy denies
   * or may refuse, and list a refused method disabled with the policy's
   * reason -- the classic GrantsPicker's policyRepo/runKind.
   */
  readonly policyVerdicts = input<boolean>(false);
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
  private methodsFor: string | null = null;
  private methodsTimer: ReturnType<typeof setTimeout> | null = null;

  protected readonly subjectRepo = computed(() => {
    const r = (this.repo() ?? "").trim();
    return REPO_RE.test(r) ? r : "";
  });

  constructor() {
    // Read the method listing for this run as soon as the form knows
    // the repository, so a held grant is flagged before anybody opens
    // "Add". Debounced: the repository box is typed a letter at a time.
    effect(() => {
      if (!this.policyVerdicts()) return;
      const repo = this.subjectRepo();
      const kind = this.runKind();
      untracked(() => {
        if (!this.access.can("GET /api/capability-methods", repo || undefined))
          return;
        const key = `${kind}|${repo}`;
        if (key === this.methodsFor) return;
        if (this.methodsTimer) clearTimeout(this.methodsTimer);
        this.methodsTimer = setTimeout(() => {
          this.methodsTimer = null;
          void this.loadMethods(kind, repo);
        }, 250);
      });
    });
    // A repository chosen after a method was picked can make the policy
    // refuse it: move the selection to one that may still be granted.
    effect(() => {
      const opts = this.methodOptions();
      const cur = this.selectedMethod();
      if (!cur || !opts.find((o) => o.value === cur)?.disabled) return;
      const next = this.firstEnabledMethod();
      untracked(() => this.selectedMethod.set(next));
    });
    inject(DestroyRef).onDestroy(() => {
      if (this.methodsTimer) clearTimeout(this.methodsTimer);
    });
  }

  /**
   * methodsQuery is the listing's question for this run. With a whole
   * owner/name the listing judges that one run (policyRefusal). With
   * none, a form that has policyVerdicts on (a schedule or template whose
   * repository is not chosen yet) asks nothing of the run: `kind` alone
   * would be judged as a repository-less task and refuse a method a
   * repo-scoped rule admits, where the kind's own "may be refused" is
   * the truth (GrantsPicker.jsx's `subject`). Other pickers keep asking
   * for `kind` alone, as they always have.
   */
  private methodsQuery(kind: string, repo: string): string | undefined {
    const k = encodeURIComponent(kind);
    if (repo) return `kind=${k}&repo=${encodeURIComponent(repo)}`;
    return this.policyVerdicts() ? undefined : `kind=${k}`;
  }

  private async loadMethods(kind: string, repo: string): Promise<void> {
    const key = `${kind}|${repo}`;
    this.methodsFor = key;
    const query = this.methodsQuery(kind, repo);
    try {
      const val = (await this.api.get<unknown>(
        "GET /api/capability-methods",
        undefined,
        query,
      )) as
        | CapabilityMethod[]
        | { items?: CapabilityMethod[]; methods?: CapabilityMethod[] };
      if (this.methodsFor !== key) return;
      const list = Array.isArray(val)
        ? val
        : (val?.items ?? val?.methods ?? []);
      this.methods.set(list.filter((m) => typeof m?.ref === "string"));
    } catch {
      // No verdicts beats a picker that will not open: filing still
      // reports a refused grant.
      if (this.methodsFor === key) this.methodsFor = null;
    }
  }

  /** verdictOf is the policy's word on grant g, or null. */
  protected verdictOf(g: string): GrantPolicyVerdict | null {
    if (!this.policyVerdicts()) return null;
    const ref = grantRef(g);
    const m = this.methods().find(
      (x) => x.ref === ref || (x.aliasOf?.includes(ref) ?? false),
    );
    return policyVerdictFor(m, this.runKind(), !!this.subjectRepo());
  }

  protected readonly heldVerdicts = computed(() => {
    if (!this.policyVerdicts()) return [];
    this.methods();
    const out: { grant: string; verdict: GrantPolicyVerdict }[] = [];
    for (const g of this.held()) {
      const v = this.verdictOf(g);
      if (v) out.push({ grant: g, verdict: v });
    }
    return out;
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

  protected readonly methodOptions = computed<PillOption[]>(() =>
    this.methods()
      .filter((m) => this.policyVerdicts() || !m.policyRefusal)
      .map((m) => {
        const v = this.policyVerdicts()
          ? policyVerdictFor(m, this.runKind(), !!this.subjectRepo())
          : null;
        return {
          value: m.ref,
          label: m.description ? `${m.ref} — ${m.description}` : m.ref,
          ...(v ? { hint: v.note } : {}),
          ...(v && v.badge !== "may be refused" ? { disabled: true } : {}),
        };
      }),
  );

  /** The first method a grant may be made of here, or "". */
  protected readonly firstEnabledMethod = computed(
    () => this.methodOptions().find((o) => !o.disabled)?.value ?? "",
  );

  protected canReadCatalogue(): boolean {
    const repo = this.subjectRepo() || undefined;
    return (
      this.access.can("GET /api/capability-methods", repo) ||
      this.access.can("GET /api/api-scope-sets", repo) ||
      this.access.can("POST /api/capability-grants/check", repo)
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
    // Set while the read is in flight so a second click does not ask
    // again; cleared below if it fails, so the next open retries.
    this.loadedCatalogue = true;
    const repo = this.subjectRepo() || undefined;
    const query = this.methodsQuery(this.runKind(), repo ?? "");
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
      if (!this.selectedMethod() && this.firstEnabledMethod()) {
        this.selectedMethod.set(this.firstEnabledMethod());
      }
    }
    if (setsRes.status === "rejected" || methodsRes.status === "rejected") {
      this.loadedCatalogue = false;
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
      const ref = (this.selectedMethod() || this.firstEnabledMethod()).trim();
      if (!ref) {
        this.checkError.set("Choose a capability method.");
        return;
      }
      // A method the policy refuses here is listed so the reader can see
      // it exists, never granted: the grant check does not judge policy.
      const opt = this.methodOptions().find((o) => o.value === ref);
      if (opt?.disabled) {
        this.checkError.set(opt.hint || `${ref} cannot be granted here.`);
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
      const subject = this.subjectRepo();
      if (
        this.access.can(
          "POST /api/capability-grants/check",
          subject || undefined,
        )
      ) {
        const res = await this.api.post<
          GrantCheck[] | { checks?: GrantCheck[] }
        >("POST /api/capability-grants/check", undefined, {
          grants: [reqItem],
          ...(subject ? { subject: { repo: subject } } : {}),
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
