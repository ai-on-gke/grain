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

import { Component, signal } from "@angular/core";
import { TestBed } from "@angular/core/testing";
import { provideRouter } from "@angular/router";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import type { CapabilityMethod, CapabilityOption } from "../api/models";
import { AccessService } from "../services/access.service";
import {
  GrantsPickerComponent,
  REPO_ACCESS_DOC,
  REPO_ACCESS_DOC_LABEL,
  policyVerdictText,
  resolveMethodPolicy,
  type GrantAttachEvent,
  type GrantRunKind,
} from "./grants-picker.component";

const GCP_KEY_REF = "https://grain.dev/capabilities/gcp#gcp-key";
const REPO_READ_REF = "https://grain.dev/capabilities/repo#read";
const API_ADMIN_REF = "https://grain.dev/capabilities/api#tasks-admin";
const SAFE_REF = "https://grain.dev/capabilities/docs#lookup";

interface ApiCall {
  method: "GET" | "POST";
  key: string;
  query?: string;
  body?: unknown;
}

class FakeApi {
  calls: ApiCall[] = [];
  methodsHandler: (query?: string) => Promise<CapabilityMethod[]> =
    async () => [];
  scopeSets = [
    { name: "tasks-read", description: "Read tasks" },
    { name: "tasks-admin", description: "Admin tasks" },
  ];
  checkResult: unknown[] = [{ ok: true, recorded: `${SAFE_REF}={}` }];

  get = (key: string, _params?: RouteParams, query?: string) => {
    this.calls.push({ method: "GET", key, query });
    if (key === "GET /api/capability-methods") {
      return this.methodsHandler(query);
    }
    if (key === "GET /api/api-scope-sets") {
      return Promise.resolve({ sets: this.scopeSets });
    }
    return Promise.resolve({});
  };

  post = (key: string, _params?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "POST", key, body });
    if (key === "POST /api/capability-grants/check") {
      return Promise.resolve(this.checkResult);
    }
    return Promise.resolve({});
  };
}

class FakeAccess {
  can = () => true;
}

@Component({
  imports: [GrantsPickerComponent],
  template: `
    <g-grants-picker
      testid="gp"
      [offered]="offered()"
      [held]="held()"
      [repo]="repo()"
      [runKind]="runKind()"
      [disabled]="disabled()"
      (heldChange)="heldChanges.push($event); held.set($event)"
      (attachGrant)="attached.push($event)"
    />
  `,
})
class HostComponent {
  readonly offered = signal<CapabilityOption[]>([]);
  readonly held = signal<string[]>([]);
  readonly repo = signal<string | undefined>("");
  readonly runKind = signal<GrantRunKind>("task");
  readonly disabled = signal(false);
  heldChanges: string[][] = [];
  attached: GrantAttachEvent[] = [];
}

async function mount(api: FakeApi, init?: (h: HostComponent) => void) {
  TestBed.configureTestingModule({
    imports: [HostComponent],
    providers: [
      provideRouter([]),
      { provide: GrainApi, useValue: api },
      { provide: AccessService, useValue: new FakeAccess() },
    ],
  });
  const fixture = TestBed.createComponent(HostComponent);
  init?.(fixture.componentInstance);
  fixture.autoDetectChanges(true);
  await fixture.whenStable();
  await Promise.resolve();
  await fixture.whenStable();
  const el = fixture.nativeElement as HTMLElement;
  const q = <T extends Element = HTMLElement>(sel: string) =>
    el.querySelector<T & Element>(sel) as T | null;
  return { fixture, el, q, host: fixture.componentInstance };
}

describe("resolveMethodPolicy & policyVerdictText", () => {
  it("formats verdict copy for each run kind and keeps ci_only distinct from capability policy", () => {
    expect(policyVerdictText("denied", "task")).toBe(
      "denied by the capability policy for agent tasks",
    );
    expect(policyVerdictText("may_be_refused", "ci")).toBe(
      "may be refused by the capability policy for CI jobs",
    );
    const ciOnlyText = policyVerdictText("ci_only", "task");
    expect(ciOnlyText).toContain("CI only");
    expect(ciOnlyText).not.toContain("capability policy");

    const ciOnlyInfo = resolveMethodPolicy(
      {
        ref: REPO_READ_REF,
        capability: "https://grain.dev/capabilities/repo",
        method: "read",
        policy: { task: "ci_only" },
        policyRefusal: "should not override ci_only",
      },
      "task",
      true,
    );
    expect(ciOnlyInfo).toMatchObject({
      verdict: "ci_only",
      refused: true,
      ciOnly: true,
      badge: "CI only",
      linkKind: "repo_access_doc",
    });
  });

  it("drops kind-level may_be_refused when kindVerdictShown is false unless policyRefusal is present", () => {
    const method: CapabilityMethod = {
      ref: GCP_KEY_REF,
      capability: "https://grain.dev/capabilities/gcp",
      method: "gcp-key",
      policy: { task: "may_be_refused" },
    };
    expect(resolveMethodPolicy(method, "task", true)?.verdict).toBe(
      "may_be_refused",
    );
    expect(resolveMethodPolicy(method, "task", false)).toBeNull();

    const refusedMethod: CapabilityMethod = {
      ...method,
      policyRefusal: "denied by rule #1 for acme/app",
    };
    expect(resolveMethodPolicy(refusedMethod, "task", false)).toMatchObject({
      verdict: "denied",
      refused: true,
      note: "denied by rule #1 for acme/app",
      linkKind: "policy",
    });
  });
});

describe("GrantsPickerComponent policy verdicts", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("marks denied capabilities, warns on may_be_refused, marks repo#read CI only, and refuses denied add", async () => {
    const api = new FakeApi();
    api.methodsHandler = async () => [
      {
        ref: GCP_KEY_REF,
        capability: "https://grain.dev/capabilities/gcp",
        method: "gcp-key",
        policy: { task: "denied" },
      },
      {
        ref: REPO_READ_REF,
        capability: "https://grain.dev/capabilities/repo",
        method: "read",
        policy: { task: "ci_only" },
      },
      {
        ref: API_ADMIN_REF,
        capability: "https://grain.dev/capabilities/api",
        method: "tasks-admin",
        policy: { task: "may_be_refused" },
      },
      {
        ref: SAFE_REF,
        capability: "https://grain.dev/capabilities/docs",
        method: "lookup",
      },
    ];

    const { q, el, fixture, host } = await mount(api, (h) => {
      h.offered.set([{ id: GCP_KEY_REF, name: "GCP key" }]);
      h.held.set([`${GCP_KEY_REF}={}`, `${REPO_READ_REF}={}`]);
    });

    // Offered toggle pill for denied gcp-key is disabled and tagged denied.
    const toggle = q<HTMLButtonElement>(
      `[data-testid="gp-cap-${GCP_KEY_REF}"]`,
    )!;
    expect(toggle.disabled).toBe(true);
    expect(toggle.dataset["policyVerdict"]).toBe("denied");
    expect(toggle.dataset["policyDenied"]).toBe("true");
    toggle.click();
    expect(host.heldChanges.length).toBe(0);

    // Held policy warnings link gcp-key to #capability-policy and repo#read to Repository access docs.
    const heldGcpNote = q<HTMLElement>('[data-testid="gp-held-policy-0"]')!;
    expect(heldGcpNote.dataset["policyVerdict"]).toBe("denied");
    const policyLink = heldGcpNote.querySelector<HTMLAnchorElement>("a")!;
    expect(policyLink.getAttribute("href")).toContain(
      "/settings/capabilities#capability-policy",
    );

    const heldRepoNote = q<HTMLElement>('[data-testid="gp-held-policy-1"]')!;
    expect(heldRepoNote.dataset["ciOnly"]).toBe("true");
    expect(heldRepoNote.dataset["policyDenied"]).toBeUndefined();
    expect(heldRepoNote.textContent).not.toContain("capability policy");
    const docLink = heldRepoNote.querySelector<HTMLAnchorElement>("a")!;
    expect(docLink.getAttribute("href")).toBe(REPO_ACCESS_DOC);
    expect(docLink.textContent?.trim()).toBe(REPO_ACCESS_DOC_LABEL);

    // Open builder and switch to Capability method mode.
    q<HTMLButtonElement>('[data-testid="gp-add-open"]')!.click();
    await fixture.whenStable();
    await Promise.resolve();
    await fixture.whenStable();

    q<HTMLButtonElement>('[data-testid="gp-mode-method"]')!.click();
    await fixture.whenStable();

    // Flagged methods summary shows denied, ci_only, and may_be_refused rows.
    const verdictsList = q<HTMLElement>('[data-testid="gp-policy-verdicts"]')!;
    expect(verdictsList).not.toBeNull();
    const gcpRow = verdictsList.querySelector<HTMLElement>(
      `[data-method="${GCP_KEY_REF}"]`,
    )!;
    expect(gcpRow.dataset["policyVerdict"]).toBe("denied");
    expect(gcpRow.querySelector("a")?.getAttribute("href")).toContain(
      "/settings/capabilities#capability-policy",
    );

    const repoRow = verdictsList.querySelector<HTMLElement>(
      `[data-method="${REPO_READ_REF}"]`,
    )!;
    expect(repoRow.dataset["ciOnly"]).toBe("true");
    expect(repoRow.textContent).not.toContain("capability policy");
    expect(repoRow.querySelector("a")?.getAttribute("href")).toBe(
      REPO_ACCESS_DOC,
    );

    const mayRow = verdictsList.querySelector<HTMLElement>(
      `[data-method="${API_ADMIN_REF}"]`,
    )!;
    expect(mayRow.dataset["policyVerdict"]).toBe("may_be_refused");

    // In raw mode, typing a denied ref shows selected policy and disables Add grant.
    q<HTMLButtonElement>('[data-testid="gp-mode-raw"]')!.click();
    await fixture.whenStable();
    const rawInput = q<HTMLInputElement>('[data-testid="gp-raw-input"]')!;
    rawInput.value = GCP_KEY_REF;
    rawInput.dispatchEvent(new Event("input"));
    await fixture.whenStable();

    const selPol = q<HTMLElement>('[data-testid="gp-selected-policy"]')!;
    expect(selPol.dataset["policyVerdict"]).toBe("denied");
    const addBtn = q<HTMLButtonElement>('[data-testid="gp-attach"]')!;
    expect(addBtn.disabled).toBe(true);

    // Typing a may_be_refused API grant warns but still allows Add grant.
    rawInput.value = "api:tasks-admin";
    rawInput.dispatchEvent(new Event("input"));
    await fixture.whenStable();
    expect(
      el.querySelector<HTMLElement>('[data-testid="gp-selected-policy"]')
        ?.dataset["policyVerdict"],
    ).toBe("may_be_refused");
    expect(addBtn.disabled).toBe(false);
  });

  it("queries repo-scoped policyRefusal, drops unrelated may_be_refused for tasks, and keeps kind verdict for ci", async () => {
    const api = new FakeApi();
    api.methodsHandler = async (query) => {
      if (query === "kind=task&repo=acme%2Fapp") {
        return [
          {
            ref: GCP_KEY_REF,
            capability: "https://grain.dev/capabilities/gcp",
            method: "gcp-key",
            policy: { task: "may_be_refused" },
          },
          {
            ref: SAFE_REF,
            capability: "https://grain.dev/capabilities/docs",
            method: "lookup",
            policyRefusal: "blocked for acme/app",
          },
        ];
      }
      if (query === "kind=ci&repo=acme%2Fapp") {
        return [
          {
            ref: GCP_KEY_REF,
            capability: "https://grain.dev/capabilities/gcp",
            method: "gcp-key",
            policy: { ci: "may_be_refused" },
          },
        ];
      }
      return [];
    };

    const { q, fixture, host } = await mount(api, (h) => {
      h.repo.set("acme/app");
      h.held.set([`${GCP_KEY_REF}={}`, `${SAFE_REF}={}`]);
    });

    // For task with repo, gcp-key's kind-wide may_be_refused is suppressed, while SAFE_REF's policyRefusal is shown.
    expect(q<HTMLElement>(`[data-method="${GCP_KEY_REF}"]`)).toBeNull();
    const refusedNote = q<HTMLElement>(`[data-method="${SAFE_REF}"]`)!;
    expect(refusedNote.textContent).toContain("blocked for acme/app");

    // Switching runKind to "ci" keeps kind-level may_be_refused even with repo set,
    // and POST /api/capability-grants/check omits subject.repo for CI.
    host.runKind.set("ci");
    await fixture.whenStable();
    await Promise.resolve();
    await fixture.whenStable();

    expect(
      q<HTMLElement>(`[data-method="${GCP_KEY_REF}"]`)?.dataset[
        "policyVerdict"
      ],
    ).toBe("may_be_refused");

    q<HTMLButtonElement>('[data-testid="gp-add-open"]')!.click();
    await fixture.whenStable();
    await Promise.resolve();
    await fixture.whenStable();
    q<HTMLButtonElement>('[data-testid="gp-mode-raw"]')!.click();
    await fixture.whenStable();
    const rawInput = q<HTMLInputElement>('[data-testid="gp-raw-input"]')!;
    rawInput.value = `${GCP_KEY_REF}={}`;
    rawInput.dispatchEvent(new Event("input"));
    await fixture.whenStable();
    q<HTMLButtonElement>('[data-testid="gp-attach"]')!.click();
    await fixture.whenStable();
    await Promise.resolve();

    const checkCall = api.calls.find(
      (c) => c.key === "POST /api/capability-grants/check",
    );
    expect(checkCall?.body).toEqual({
      grants: [{ grant: `${GCP_KEY_REF}={}` }],
    });
  });

  it("ignores stale out-of-order /api/capability-methods responses and invalid repo strings", async () => {
    let resolveFirst!: (v: CapabilityMethod[]) => void;
    let resolveSecond!: (v: CapabilityMethod[]) => void;
    let callCount = 0;

    const api = new FakeApi();
    api.methodsHandler = (query) => {
      callCount += 1;
      if (callCount === 1) {
        expect(query).toBeUndefined();
        return new Promise((r) => {
          resolveFirst = r;
        });
      }
      expect(query).toBe("kind=task&repo=acme%2Fapp");
      return new Promise((r) => {
        resolveSecond = r;
      });
    };

    const { q, fixture, host } = await mount(api, (h) => {
      h.repo.set("not a valid repo");
      h.held.set([`${GCP_KEY_REF}={}`]);
    });

    // Change to a valid repo before the first request finishes.
    host.repo.set("acme/app");
    await fixture.whenStable();

    // Resolve second (fresh) before first (stale).
    resolveSecond([
      {
        ref: GCP_KEY_REF,
        capability: "https://grain.dev/capabilities/gcp",
        method: "gcp-key",
        policyRefusal: "fresh refusal",
      },
    ]);
    await fixture.whenStable();
    await Promise.resolve();
    await fixture.whenStable();

    resolveFirst([
      {
        ref: GCP_KEY_REF,
        capability: "https://grain.dev/capabilities/gcp",
        method: "gcp-key",
        policyRefusal: "stale refusal",
      },
    ]);
    await fixture.whenStable();
    await Promise.resolve();
    await fixture.whenStable();

    expect(
      q<HTMLElement>('[data-testid="gp-held-policy-0"]')?.textContent,
    ).toContain("fresh refusal");
  });
});
