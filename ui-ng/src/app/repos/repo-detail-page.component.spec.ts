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

import { APP_BASE_HREF } from "@angular/common";
import { TestBed } from "@angular/core/testing";
import {
  Router,
  provideRouter,
  withComponentInputBinding,
} from "@angular/router";
import { RouterTestingHarness } from "@angular/router/testing";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import type { GrainConfig } from "../api/models";
import {
  RepoDetailPageComponent,
  declaredPushRef,
  importIntervalSeconds,
  importIntervalText,
  importOutcomeText,
  releaseActionReasons,
  removeRepoMessage,
  unjudgedGatesFor,
} from "./repo-detail-page.component";
import {
  planFormOf,
  planPayload,
  qualifiableTemplates,
  repeatError,
  withoutDanglingDependencies,
} from "./repo-qualification-plan.component";
import { saidLabel } from "./repo-work-ownership.component";

interface RepoCall {
  method: string;
  key: string;
  params?: RouteParams;
  body?: unknown;
  query?: string;
}

// The server's sentence for a watched default branch grain CI is not
// enabled for (pkg/ui/basebranchgate.go), cut short.
const CI_OFF_DETAIL =
  "Nothing is testing acme/platform's default branch, and the merge queue is merging onto it anyway.";

class FakeRepoApi {
  calls: RepoCall[] = [];
  ciMode = "grain";
  refusePut: string | null = null;
  refuseMessage = 'ciMode: must be "grain", "github", or "none"';
  issueImport: Record<string, unknown> = {
    enabled: true,
    label: "grain-ready",
    repo: "",
    approved: false,
    intervalSeconds: 600,
    effectiveIntervalSeconds: 600,
  };
  ciCapabilities: string[] = ["github-token"];
  plan: Record<string, unknown> = {
    configured: true,
    requireApproval: false,
    autoPromote: true,
    items: [{ templateId: "smoke", repeat: 1, dependsOn: [] }],
  };
  work: Record<string, unknown> = {
    repo: "acme/platform",
    default: "",
    defaultRuns: true,
    items: [
      { item: "release", kind: "release", override: "", runs: true },
      {
        item: "nightly",
        kind: "schedule",
        title: "Nightly sweep",
        override: "off",
        runs: false,
        declaration: { path: ".grain/schedules/nightly.yml" },
      },
    ],
  };
  // gates answers GET /api/base-branch-gates from the stored CI mode, as
  // the server does for a watched branch it has never enrolled.
  gates = (): unknown[] =>
    this.ciMode === "grain"
      ? []
      : [
          {
            repo: "acme/platform",
            judged: false,
            reason: "grain-ci-off",
            detail: CI_OFF_DETAIL,
          },
          // Another repository's gate is not this page's.
          {
            repo: "acme/other",
            judged: false,
            reason: "grain-ci-off",
            detail: "Nothing is testing acme/other's default branch.",
          },
        ];

  configBody: GrainConfig = {
    version: "v3.6.0",
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
    ciCapabilities: [
      { id: "github-token", name: "GitHub token", ready: true },
      { id: "docker-hub", name: "Docker Hub push", ready: true },
    ],
  } as GrainConfig;

  declarationsBody: Record<string, unknown> = {
    declarations: true,
    declarationsStatus: {
      commit: "0f1e2d3c4b5a69788796",
      templates: [".grain/templates/nightly.yml"],
      triggers: [".grain/triggers/payments.yml"],
      pushes: [
        {
          path: ".grain/templates/nightly.yml",
          refs: [{ ref: "refs/tags/nightly-*", force: true }],
        },
      ],
      warnings: ["port-types: main->candidtae is not paired"],
    },
    pairedBranches: ["main", "candidate"],
    portBranches: ["main", "candidate"],
    portDirections: [
      { from: "main", to: "candidate", paired: true, types: ["fix"] },
      { from: "main", to: "candidtae", paired: false, types: ["fix"] },
    ],
    portWarnings: [
      "port-types: main->candidtae is not paired",
      "operator: candidate->main names nothing",
    ],
  };

  config = () => Promise.resolve(structuredClone(this.configBody));

  get = (key: string, params?: RouteParams, query?: string) => {
    this.calls.push({ method: "GET", key, params, query });
    if (key === "GET /api/base-branch-gates") {
      return Promise.resolve({ gates: this.gates() });
    }
    if (key === "GET /api/repos/{owner}/{name}/declarations") {
      return Promise.resolve(structuredClone(this.declarationsBody));
    }
    if (key === "GET /api/deployment/repos/{owner}/{name}/config") {
      return Promise.resolve({
        baseBranch: "main",
        cloneUrl: "https://github.com/acme/platform.git",
        gitCredential: "github-main",
      });
    }
    if (key === "GET /api/repos/{owner}/{name}/settings") {
      return Promise.resolve({
        ciMode: this.ciMode,
        mergeOnGreen: true,
        requireBranchUpToDate: false,
        issueImport: this.issueImport,
      });
    }
    if (key === "GET /api/repos/{owner}/{name}/ci-capabilities") {
      return Promise.resolve({ ciCapabilities: this.ciCapabilities });
    }
    if (key === "GET /api/repos/{owner}/{name}/qualification-plan") {
      return Promise.resolve(structuredClone(this.plan));
    }
    if (key === "GET /api/repos/{owner}/{name}/work-ownership") {
      return Promise.resolve(structuredClone(this.work));
    }
    if (key === "GET /api/templates") {
      return Promise.resolve([
        { id: "smoke", name: "Smoke test" },
        { id: "soak", name: "Soak" },
        { id: "other-bound", name: "Bound elsewhere", repo: "acme/other" },
        { id: "builtin", name: "Built-in", system: true },
      ]);
    }
    if (key === "GET /api/deployment/git-connections") {
      return Promise.resolve([
        { credential: "github-main" },
        { credential: "github-enterprise" },
      ]);
    }
    if (key === "GET /api/repos/{owner}/{name}/branches") {
      return Promise.resolve([{ name: "main", sha: "1234567890abcdef" }]);
    }
    if (key === "GET /api/repos/{owner}/{name}/release") {
      return Promise.resolve({
        enabled: false,
        baseBranchJobs: ["unit"],
        pendingReleases: [
          { version: "v1.3.0", summary: "Trunk release candidate" },
        ],
      });
    }
    if (key === "GET /api/repos/{owner}/{name}/releases") {
      return Promise.resolve([
        { name: "v1.2", status: "active", prodBranch: "v1.2" },
      ]);
    }
    if (key === "GET /api/repos/{owner}/{name}/releases/{release}/candidates") {
      return Promise.resolve([
        {
          id: "rc-1",
          version: "v1.2.0-rc1",
          branch: "v1.2.rc.1",
          status: "active",
        },
      ]);
    }
    return Promise.resolve({});
  };

  put = (key: string, params?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "PUT", key, params, body });
    if (this.refusePut === key) {
      // Rejected on a later tick, as a real refusal is: a promise that
      // is already rejected when the component awaits it is reported
      // by zone.js as unhandled before the await attaches.
      return new Promise((_, reject) =>
        setTimeout(() => reject(new Error(this.refuseMessage))),
      );
    }
    if (key === "PUT /api/repos/{owner}/{name}/ci-mode") {
      this.ciMode = String((body as { ciMode?: string })?.ciMode);
    }
    return Promise.resolve({ ok: true });
  };

  post = (key: string, params?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "POST", key, params, body });
    if (key === "POST /api/repos/plan") {
      return Promise.resolve({
        targetReposStranded: [
          {
            repo: "acme/platform",
            summary:
              "`acme/platform` comes off this deployment's target repo list, so 2 tasks still in play there are parked.",
          },
        ],
      });
    }
    return Promise.resolve({ ok: true });
  };

  delete = (key: string, params?: RouteParams) => {
    this.calls.push({ method: "DELETE", key, params });
    return Promise.resolve({ ok: true });
  };
}

async function mountRepo(url: string, api = new FakeRepoApi()) {
  localStorage.clear();
  TestBed.configureTestingModule({
    providers: [
      provideRouter(
        [
          {
            path: "repos/:owner/:name",
            component: RepoDetailPageComponent,
          },
          {
            path: "repos/:owner/:name/:tab",
            component: RepoDetailPageComponent,
          },
          { path: "settings/repositories", children: [] },
          { path: "repos", children: [] },
          { path: "tasks", children: [] },
        ],
        withComponentInputBinding(),
      ),
      { provide: APP_BASE_HREF, useValue: "/next/" },
      { provide: GrainApi, useValue: api },
    ],
  });
  const harness = await RouterTestingHarness.create(url);
  harness.fixture.autoDetectChanges(true);
  await harness.fixture.whenStable();
  await new Promise((r) => setTimeout(r, 20));
  await harness.fixture.whenStable();
  return {
    harness,
    el: harness.fixture.nativeElement as HTMLElement,
    api,
  };
}

describe("RepoDetailPageComponent (/next/repos/:owner/:name)", () => {
  afterEach(() => {
    TestBed.resetTestingModule();
  });

  it("saves repository config, pins Git connection, imports issues, and renders zero native <select> elements", async () => {
    const { el, api } = await mountRepo("/repos/acme/platform");

    expect(
      el.querySelector('[data-testid="repo-detail-heading"]')?.textContent,
    ).toContain("acme/platform");
    expect(el.querySelectorAll("select").length).toBe(0);

    el.querySelector<HTMLButtonElement>(
      '[data-testid="save-repo-config-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    expect(
      api.calls.some(
        (c) =>
          c.key === "PUT /api/deployment/repos/{owner}/{name}/config" &&
          c.params?.["owner"] === "acme" &&
          c.params?.["name"] === "platform",
      ),
    ).toBe(true);

    // Import issues
    el.querySelector<HTMLButtonElement>(
      '[data-testid="import-issues-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    expect(
      api.calls.some(
        (c) => c.key === "POST /api/repos/{owner}/{name}/import-issues",
      ),
    ).toBe(true);
  });

  it("creates branches and manages releases, RC promotion, candidate qualification, and pending release approvals", async () => {
    const { el, api, harness } = await mountRepo(
      "/repos/acme/platform/branches",
    );

    const brInput = el.querySelector<HTMLInputElement>(
      '[data-testid="input-new-branch-name"]',
    )!;
    brInput.value = "release/v1.3";
    brInput.dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();

    el.querySelector<HTMLButtonElement>(
      '[data-testid="create-branch-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    expect(
      api.calls.some(
        (c) =>
          c.key === "POST /api/repos/{owner}/{name}/branches" &&
          (c.body as { name?: string })?.name === "release/v1.3",
      ),
    ).toBe(true);

    // Switch to Releases tab
    el.querySelector<HTMLButtonElement>(
      '[data-testid="repo-tab-releases"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 15));
    harness.detectChanges();

    // Approve pending trunk release v1.3.0
    el.querySelector<HTMLButtonElement>(
      '[data-testid="approve-pending-release-v1.3.0"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    expect(
      api.calls.some(
        (c) =>
          c.key ===
            "POST /api/repos/{owner}/{name}/pending-releases/{version}/approve" &&
          c.params?.["version"] === "v1.3.0",
      ),
    ).toBe(true);

    // Cut RC is off while the newest candidate is active, and says why
    // (the classic pane's rule); Promote RC asks before it moves the
    // prod branch.
    expect(
      el.querySelector<HTMLButtonElement>('[data-testid="cut-rc-v1.2"]')!
        .disabled,
    ).toBe(true);
    expect(
      el.querySelector('[data-testid="release-action-reasons"]')?.textContent,
    ).toContain("v1.2.rc.1 is active: a new RC is cut once it is promoted.");
    el.querySelector<HTMLButtonElement>(
      '[data-testid="promote-rc-v1.2"]',
    )!.click();
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="confirm-dialog"]')?.textContent,
    ).toContain("Promote v1.2.rc.1 to v1.2?");
    expect(api.calls.some((c) => c.key.endsWith("/candidates/promote"))).toBe(
      false,
    );
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    expect(
      api.calls.some(
        (c) =>
          c.key ===
          "POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote",
      ),
    ).toBe(true);
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="approve-qualification-rc-1"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    expect(
      api.calls.some(
        (c) =>
          c.key ===
          "POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve",
      ),
    ).toBe(true);
  });

  // 2026-11-14 repository walkthrough: Import issues now said "triggered"
  // whatever the pass did, and Cut/Promote gave no reason when off.
  it("words an import pass the way the classic page does", () => {
    expect(
      importOutcomeText(
        {
          source: "acme/nosuch",
          open: 0,
          imported: [],
          skipped: 0,
          error: "GitHub answered 404",
        },
        "acme/platform",
      ),
    ).toEqual({
      error: true,
      text: "Could not read the open issues on acme/nosuch: GitHub answered 404",
    });
    expect(
      importOutcomeText(
        { source: "acme/platform", open: 2, imported: ["7"], skipped: 1 },
        "acme/platform",
      ),
    ).toEqual({
      error: false,
      text: "Read 2 open issues on acme/platform: imported 1, 1 already imported.",
    });
    expect(
      importOutcomeText(
        {
          source: "acme/platform",
          open: 1,
          imported: [],
          skipped: 0,
          error: "boom",
        },
        "acme/platform",
      ).text,
    ).toBe(
      "Read 1 open issue on acme/platform: imported 0, 0 already imported. Some could not be imported: boom",
    );
  });

  it("says why Cut RC and Promote RC are off", () => {
    const rel = { name: "1.0", status: "active" };
    expect(
      releaseActionReasons(rel, { branch: "1.0.rc.1", status: "active" }, true),
    ).toEqual({
      cut: "1.0.rc.1 is active: a new RC is cut once it is promoted.",
      promote: null,
    });
    expect(releaseActionReasons(rel, undefined, true)).toEqual({
      cut: null,
      promote: "No candidate to promote yet: cut one first.",
    });
    expect(
      releaseActionReasons(
        rel,
        { branch: "1.0.rc.1", status: "promoted" },
        true,
      ),
    ).toEqual({
      cut: null,
      promote: "1.0.rc.1 is already promoted: a candidate is promoted once.",
    });
    expect(releaseActionReasons(rel, undefined, false)).toEqual({
      cut: null,
      promote: null,
    });
    expect(
      releaseActionReasons(
        { name: "2.0", status: "provisioning", latestBranch: "2.0.latest" },
        undefined,
        false,
      ).cut,
    ).toContain("still provisioning");
  });

  // 2026-11-13 repository walkthrough: the CI mode pill offered "both"
  // and "off", which PUT .../ci-mode refuses, and had no "none", so a
  // repository on no CI read as Grain CI.
  it("keeps only this repository's unjudged gates", () => {
    const gate = (repo: string, judged: boolean) => ({
      repo,
      judged,
      reason: judged ? "run-unfinished" : "grain-ci-off",
    });
    expect(
      unjudgedGatesFor(
        [
          gate("acme/platform", false),
          gate("Acme/Platform", false),
          gate("acme/platform", true),
          gate("acme/other", false),
        ],
        "acme/platform",
      ).map((g) => g.repo),
    ).toEqual(["acme/platform", "Acme/Platform"]);
  });

  // 2026-11-14 repository walkthrough, item 5: classic warns on Overview
  // and on Releases when CI mode leaves the watched base branch unjudged.
  it("warns on Overview and Releases while CI mode is github or none, and clears on grain without a reload", async () => {
    const api = new FakeRepoApi();
    api.ciMode = "github";
    const { el, harness } = await mountRepo("/repos/acme/platform", api);
    const warnings = () => [
      ...el.querySelectorAll<HTMLElement>(
        '[data-testid="repo-base-gate-warning"]',
      ),
    ];
    const settle = async () => {
      await new Promise((r) => setTimeout(r, 20));
      harness.detectChanges();
      await harness.fixture.whenStable();
    };

    expect(warnings()).toHaveLength(1);
    expect(warnings()[0]!.getAttribute("role")).toBe("status");
    expect(warnings()[0]!.textContent).toContain(CI_OFF_DETAIL);
    expect(warnings()[0]!.textContent).not.toContain("acme/other");
    const read = api.calls.find((c) => c.key === "GET /api/base-branch-gates");
    expect(new URLSearchParams(read!.query).get("repo")).toBe("acme/platform");
    expect(new URLSearchParams(read!.query).get("fields")).toBe("full");

    el.querySelector<HTMLButtonElement>(
      '[data-testid="repo-tab-releases"]',
    )!.click();
    await settle();
    expect(warnings()).toHaveLength(1);
    expect(
      el.querySelector('[data-testid="card-repo-release-settings"]'),
    ).not.toBeNull();

    // The CI tab carries the control, not the warning.
    el.querySelector<HTMLButtonElement>('[data-testid="repo-tab-ci"]')!.click();
    await settle();
    expect(warnings()).toHaveLength(0);
    const pick = async (mode: string) => {
      el.querySelector<HTMLButtonElement>(
        '[data-testid="select-repo-ci-mode"]',
      )!.click();
      harness.detectChanges();
      el.querySelector<HTMLButtonElement>(
        `[data-testid="select-repo-ci-mode-opt-${mode}"]`,
      )!.click();
      await settle();
    };
    const toTab = async (id: string) => {
      el.querySelector<HTMLButtonElement>(
        `[data-testid="repo-tab-${id}"]`,
      )!.click();
      await settle();
    };

    await pick("grain");
    await toTab("overview");
    expect(warnings()).toHaveLength(0);
    await toTab("releases");
    expect(warnings()).toHaveLength(0);

    await toTab("ci");
    await pick("none");
    await toTab("overview");
    expect(warnings()).toHaveLength(1);
  });

  it("links the warning to the CI tab's CI mode control", async () => {
    const api = new FakeRepoApi();
    api.ciMode = "none";
    const { el, harness } = await mountRepo(
      "/repos/acme/platform/releases",
      api,
    );
    const link = el.querySelector<HTMLAnchorElement>(
      '[data-testid="repo-base-gate-ci-mode-link"]',
    )!;
    expect(link.textContent?.trim()).toBe("Go to CI Mode");
    expect(link.getAttribute("href")).toBe(
      "/next/repos/acme/platform/ci#repo-ci-mode",
    );
    link.click();
    await new Promise((r) => setTimeout(r, 30));
    harness.detectChanges();
    await harness.fixture.whenStable();
    const control = el.querySelector<HTMLElement>("#repo-ci-mode");
    expect(control).not.toBeNull();
    expect(
      control!.querySelector('[data-testid="select-repo-ci-mode"]'),
    ).not.toBeNull();
    expect(document.activeElement).toBe(
      control!.querySelector('[data-testid="select-repo-ci-mode"]'),
    );
  });

  it("offers exactly the CI modes the server accepts and shows a repo on none as No CI", async () => {
    const api = new FakeRepoApi();
    api.ciMode = "none";
    const { el, harness } = await mountRepo("/repos/acme/platform/ci", api);
    const pill = el.querySelector<HTMLButtonElement>(
      '[data-testid="select-repo-ci-mode"]',
    )!;
    expect(pill.textContent).toContain("No CI");
    pill.click();
    harness.detectChanges();
    const opts = [
      ...el.querySelectorAll<HTMLElement>(
        '[data-testid^="select-repo-ci-mode-opt-"]',
      ),
    ].map((o) => o.dataset["testid"]!.replace("select-repo-ci-mode-opt-", ""));
    expect(opts).toEqual(["grain", "github", "none"]);
  });

  it("puts the CI mode pill back when the server refuses the new mode", async () => {
    const api = new FakeRepoApi();
    api.refusePut = "PUT /api/repos/{owner}/{name}/ci-mode";
    const { el, harness } = await mountRepo("/repos/acme/platform/ci", api);
    const pill = el.querySelector<HTMLButtonElement>(
      '[data-testid="select-repo-ci-mode"]',
    )!;
    expect(pill.textContent).toContain("Grain CI");
    pill.click();
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="select-repo-ci-mode-opt-github"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(
      api.calls.some(
        (c) =>
          c.key === "PUT /api/repos/{owner}/{name}/ci-mode" &&
          (c.body as { ciMode?: string })?.ciMode === "github",
      ),
    ).toBe(true);
    expect(pill.textContent).toContain("Grain CI");
  });
  // 2026-11-13 repository walkthrough: one click on the switch let
  // base-branch repair PRs merge with no human; the classic page asks.
  it("asks before letting base-branch repairs bypass the human gate, and not when turning it off", async () => {
    const { el, api, harness } = await mountRepo(
      "/repos/acme/platform/merge-queue",
    );
    const key =
      "PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates";
    const sw = () =>
      el.querySelector<HTMLButtonElement>(
        'button[aria-label="Base-branch repairs bypass human review gates"]',
      )!;
    sw().click();
    harness.detectChanges();
    const dialog = el.querySelector('[data-testid="confirm-dialog"]');
    expect(dialog?.textContent).toContain(
      "Let base-branch repair PRs in acme/platform merge without a human",
    );
    expect(api.calls.some((c) => c.key === key)).toBe(false);
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-cancel"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(el.querySelector('[data-testid="confirm-dialog"]')).toBeNull();
    expect(api.calls.some((c) => c.key === key)).toBe(false);
    expect(sw().getAttribute("aria-checked")).toBe("false");

    sw().click();
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(api.calls.filter((c) => c.key === key).map((c) => c.body)).toEqual([
      { baseRepairBypassesHumanGates: true },
    ]);
    expect(sw().getAttribute("aria-checked")).toBe("true");

    sw().click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(el.querySelector('[data-testid="confirm-dialog"]')).toBeNull();
    expect(api.calls.filter((c) => c.key === key).map((c) => c.body)).toEqual([
      { baseRepairBypassesHumanGates: true },
      { baseRepairBypassesHumanGates: false },
    ]);
  });
  // 2026-11-14 repository walkthrough parity gaps: the issue-import
  // interval, the CI capabilities card, work ownership and the
  // qualification plan were on the classic page only.
  it("reads, validates and saves the issue-import interval, and guards it", async () => {
    const { el, api, harness } = await mountRepo("/repos/acme/platform");
    const box = () =>
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-import-interval"]',
      )!;
    expect(box().value).toBe("10");
    expect(box().placeholder).toBe("10");

    box().value = "0";
    box().dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="save-issue-import-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(box().getAttribute("aria-invalid")).toBe("true");
    expect(box().getAttribute("aria-describedby")).toBe("imp-interval-err");
    expect(
      el.querySelector('[data-testid="import-interval-error"]')?.textContent,
    ).toContain("at least 1");
    expect(
      api.calls.some((c) => c.key === "PUT /api/repos/{owner}/{name}/settings"),
    ).toBe(false);

    // The next edit clears the refusal.
    box().value = "15";
    box().dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();
    expect(box().getAttribute("aria-invalid")).toBeNull();

    // An unsaved interval is an unsaved edit: switching tab asks.
    el.querySelector<HTMLButtonElement>('[data-testid="repo-tab-ci"]')!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="confirm-dialog"]')?.textContent,
    ).toContain("issue import");
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-cancel"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();

    el.querySelector<HTMLButtonElement>(
      '[data-testid="save-issue-import-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    const put = api.calls.find(
      (c) => c.key === "PUT /api/repos/{owner}/{name}/settings",
    );
    expect(put?.body).toMatchObject({ issueImportIntervalSeconds: 900 });
  });

  // Review of task 5868 (task 6153): saving the importer rounded a
  // stored interval that is not whole minutes even when untouched.
  it("leaves an untouched interval out of the issue-import save", async () => {
    const api = new FakeRepoApi();
    api.issueImport = { ...api.issueImport, intervalSeconds: 90 };
    const { el, harness } = await mountRepo("/repos/acme/platform", api);
    expect(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-import-interval"]',
      )!.value,
    ).toBe("2");
    const label = el.querySelector<HTMLInputElement>(
      '[data-testid="input-import-label"]',
    )!;
    label.value = "triage";
    label.dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="save-issue-import-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    const body = api.calls.find(
      (c) => c.key === "PUT /api/repos/{owner}/{name}/settings",
    )?.body as Record<string, unknown>;
    expect(body).toMatchObject({ issueImportLabel: "triage" });
    expect(body).not.toHaveProperty("issueImportIntervalSeconds");
  });

  // Review of task 5868 (task 6153): a work-ownership save re-read the
  // whole release card over its unsaved edits.
  it("keeps unsaved release-card edits when work ownership is saved", async () => {
    const { el, harness } = await mountRepo("/repos/acme/platform/releases");
    const jobs = el.querySelector<HTMLInputElement>(
      '[data-testid="input-release-base-jobs"]',
    )!;
    jobs.value = "unit, e2e";
    jobs.dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      'button[aria-label="Nightly sweep: on"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 20));
    harness.detectChanges();
    expect(
      el.querySelector<HTMLInputElement>(
        '[data-testid="input-release-base-jobs"]',
      )!.value,
    ).toBe("unit, e2e");
  });

  // Review of task 5868 (task 6153): an edit made while the plan's Save
  // was in flight was replaced by the snapshot that was sent.
  it("keeps a qualification-plan edit made while Save is in flight", async () => {
    const api = new FakeRepoApi();
    const { el, harness } = await mountRepo(
      "/repos/acme/platform/releases",
      api,
    );
    let release: (v: unknown) => void = () => undefined;
    const realPut = api.put;
    api.put = (key: string, params?: RouteParams, body?: unknown) => {
      if (key === "PUT /api/repos/{owner}/{name}/qualification-plan") {
        api.calls.push({ method: "PUT", key, params, body });
        return new Promise((r) => (release = r));
      }
      return realPut(key, params, body);
    };
    const card = el.querySelector(
      '[data-testid="card-repo-qualification-plan"]',
    )!;
    const sw = (id: string) =>
      card.querySelector<HTMLButtonElement>(`[data-testid="${id}"]`)!;
    sw("switch-qualification-require-approval").click();
    harness.detectChanges();
    sw("save-qualification-plan").click();
    await new Promise((r) => setTimeout(r, 5));
    sw("switch-qualification-auto-promote").click();
    harness.detectChanges();
    release({ ok: true });
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(
      sw("switch-qualification-auto-promote").getAttribute("aria-checked"),
    ).toBe("false");
    expect(
      card.querySelector('[data-testid="qualification-plan-unsaved"]'),
    ).not.toBeNull();
    expect(
      api.calls.find(
        (c) => c.key === "PUT /api/repos/{owner}/{name}/qualification-plan",
      )?.body as { requireApproval?: boolean; autoPromote?: boolean },
    ).toMatchObject({ requireApproval: true, autoPromote: true });
  });

  it("says a server-refused interval beside its box, not in the banner", async () => {
    const api = new FakeRepoApi();
    api.refusePut = "PUT /api/repos/{owner}/{name}/settings";
    api.refuseMessage =
      "issueImportIntervalSeconds: must be 0 for the default or at least 60";
    const { el, harness } = await mountRepo("/repos/acme/platform", api);
    el.querySelector<HTMLButtonElement>(
      '[data-testid="save-issue-import-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 20));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="import-interval-error"]')?.textContent,
    ).toContain("at least 60");
    expect(el.querySelector('[data-testid="repo-error-banner"]')).toBeNull();
  });

  it("toggles a CI capability, and puts it back with the reason when refused", async () => {
    const api = new FakeRepoApi();
    const { el, harness } = await mountRepo("/repos/acme/platform/ci", api);
    const card = el.querySelector('[data-testid="card-repo-ci-capabilities"]')!;
    expect(card).not.toBeNull();
    expect(el.querySelectorAll("select").length).toBe(0);
    const pill = (id: string) =>
      card.querySelector<HTMLButtonElement>(
        `[data-testid="repo-ci-caps-cap-${id}"]`,
      )!;
    expect(pill("github-token").getAttribute("aria-pressed")).toBe("true");
    expect(pill("docker-hub").getAttribute("aria-pressed")).toBe("false");

    pill("docker-hub").click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(
      api.calls.filter(
        (c) => c.key === "PUT /api/repos/{owner}/{name}/ci-capabilities",
      )[0]?.body,
    ).toEqual({ ciCapabilities: ["github-token", "docker-hub"] });

    api.refusePut = "PUT /api/repos/{owner}/{name}/ci-capabilities";
    api.refuseMessage = "capability github-token is not offered to CI";
    pill("github-token").click();
    await new Promise((r) => setTimeout(r, 20));
    harness.detectChanges();
    expect(
      card.querySelector('[data-testid="ci-capabilities-error"]')?.textContent,
    ).toContain("not offered to CI");
    expect(
      card
        .querySelector('[role="group"][aria-label="CI capabilities"]')!
        .getAttribute("aria-invalid"),
    ).toBe("true");
    expect(pill("github-token").getAttribute("aria-pressed")).toBe("true");
  });

  it("draws work ownership's three states and saves one item at a time", async () => {
    const api = new FakeRepoApi();
    const { el, harness } = await mountRepo(
      "/repos/acme/platform/releases",
      api,
    );
    const card = el.querySelector('[data-testid="card-repo-work-ownership"]')!;
    expect(card).not.toBeNull();
    expect(
      card.querySelector('[data-testid="work-item-said-nightly"]')?.textContent,
    ).toContain("Off, set here");
    expect(
      card.querySelector('[data-testid="work-item-runs-nightly"]')?.textContent,
    ).toContain("Not run");
    expect(
      card.querySelector('[data-testid="work-item-said-release"]')?.textContent,
    ).toContain("Nobody has said");
    const off = card.querySelector<HTMLButtonElement>(
      'button[aria-label="Nightly sweep: off"]',
    )!;
    expect(off.getAttribute("aria-pressed")).toBe("true");

    card
      .querySelector<HTMLButtonElement>(
        'button[aria-label="Nightly sweep: say nothing"]',
      )!
      .click();
    await new Promise((r) => setTimeout(r, 10));
    card
      .querySelector<HTMLButtonElement>(
        '[data-testid="switch-work-ownership-default"]',
      )!
      .click();
    await new Promise((r) => setTimeout(r, 10));
    expect(
      api.calls
        .filter((c) => c.key === "PUT /api/repos/{owner}/{name}/work-ownership")
        .map((c) => c.body),
    ).toEqual([{ item: "nightly", state: "" }, { default: "off" }]);

    api.refusePut = "PUT /api/repos/{owner}/{name}/work-ownership";
    api.refuseMessage = "work ownership: unknown state";
    card
      .querySelector<HTMLButtonElement>('button[aria-label="release: on"]')!
      .click();
    await new Promise((r) => setTimeout(r, 20));
    harness.detectChanges();
    const group = card.querySelector('[role="group"][aria-label="release"]')!;
    expect(group.getAttribute("aria-invalid")).toBe("true");
    expect(
      card.querySelector(`#${group.getAttribute("aria-describedby")}`)
        ?.textContent,
    ).toContain("unknown state");
  });

  it("edits, validates, guards and saves the qualification plan", async () => {
    const api = new FakeRepoApi();
    const { el, harness } = await mountRepo(
      "/repos/acme/platform/releases",
      api,
    );
    const card = () =>
      el.querySelector('[data-testid="card-repo-qualification-plan"]')!;
    const save = () =>
      card().querySelector<HTMLButtonElement>(
        '[data-testid="save-qualification-plan"]',
      )!;
    expect(
      card().querySelector('[data-testid="qualification-item-template-0"]')
        ?.textContent,
    ).toContain("Smoke test");
    expect(save().disabled).toBe(true);
    expect(el.querySelectorAll("select").length).toBe(0);

    card()
      .querySelector<HTMLButtonElement>(
        '[data-testid="qualification-add-item"]',
      )!
      .click();
    harness.detectChanges();
    card()
      .querySelector<HTMLButtonElement>(
        '[data-testid="qualification-item-template-1"]',
      )!
      .click();
    harness.detectChanges();
    const opts = [
      ...card().querySelectorAll<HTMLElement>(
        '[data-testid^="qualification-item-template-1-opt-"]',
      ),
    ].map((o) => o.dataset["testid"]!.replace(/.*-opt-/, ""));
    // Neither a built-in template nor one bound to another repo.
    expect(opts).toEqual(["", "smoke", "soak"]);
    card()
      .querySelector<HTMLButtonElement>(
        '[data-testid="qualification-item-template-1-opt-soak"]',
      )!
      .click();
    harness.detectChanges();
    card()
      .querySelector<HTMLButtonElement>(
        '[data-testid="qualification-item-dep-1-smoke"]',
      )!
      .click();
    harness.detectChanges();

    const repeat = card().querySelector<HTMLInputElement>(
      '[data-testid="qualification-item-repeat-1"]',
    )!;
    repeat.value = "0";
    repeat.dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();
    expect(repeat.getAttribute("aria-invalid")).toBe("true");
    expect(save().disabled).toBe(true);
    repeat.value = "3";
    repeat.dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();
    expect(repeat.getAttribute("aria-invalid")).toBeNull();

    // An unsaved plan is an unsaved edit; Discard puts the saved plan back.
    el.querySelector<HTMLButtonElement>(
      '[data-testid="repo-tab-branches"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="confirm-dialog"]')?.textContent,
    ).toContain("qualification plan");
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-cancel"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();

    save().click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(
      api.calls.find(
        (c) => c.key === "PUT /api/repos/{owner}/{name}/qualification-plan",
      )?.body,
    ).toEqual({
      requireApproval: false,
      autoPromote: true,
      items: [
        { templateId: "smoke", repeat: 1, dependsOn: [] },
        { templateId: "soak", repeat: 3, dependsOn: ["smoke"] },
      ],
    });
    expect(save().disabled).toBe(true);

    // A refusal is said under the form and the edit stays on screen.
    api.refusePut = "PUT /api/repos/{owner}/{name}/qualification-plan";
    api.refuseMessage = "qualification plan has a dependency cycle";
    card()
      .querySelector<HTMLButtonElement>(
        '[data-testid="switch-qualification-require-approval"]',
      )!
      .click();
    harness.detectChanges();
    save().click();
    await new Promise((r) => setTimeout(r, 20));
    harness.detectChanges();
    expect(
      card().querySelector('[data-testid="qualification-plan-error"]')
        ?.textContent,
    ).toContain("dependency cycle");
    expect(
      card()
        .querySelector('[data-testid="switch-qualification-require-approval"]')!
        .getAttribute("aria-checked"),
    ).toBe("true");
  });

  it("shows the new cards read-only, with the reason, to a role that cannot write them", async () => {
    const api = new FakeRepoApi();
    api.configBody = {
      ...api.configBody,
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: [
          "repos:read",
          "releases:read",
          "capabilities:read",
          "templates:read",
          "settings:read",
        ],
      },
    } as GrainConfig;
    const { el, harness } = await mountRepo(
      "/repos/acme/platform/releases",
      api,
    );
    const work = el.querySelector('[data-testid="card-repo-work-ownership"]')!;
    expect(
      [...work.querySelectorAll<HTMLButtonElement>("button")].every(
        (b) => b.disabled,
      ),
    ).toBe(true);
    expect(
      work.querySelector('[data-testid="work-ownership-denied"]')?.textContent,
    ).toContain("repos:write");
    const plan = el.querySelector(
      '[data-testid="card-repo-qualification-plan"]',
    )!;
    expect(
      plan.querySelector<HTMLButtonElement>(
        '[data-testid="save-qualification-plan"]',
      )!.disabled,
    ).toBe(true);
    expect(
      plan.querySelector('[data-testid="qualification-plan-denied"]')
        ?.textContent,
    ).toContain("releases:write");

    el.querySelector<HTMLButtonElement>('[data-testid="repo-tab-ci"]')!.click();
    await new Promise((r) => setTimeout(r, 20));
    harness.detectChanges();
    const ci = el.querySelector('[data-testid="card-repo-ci-capabilities"]')!;
    expect(
      ci.querySelector<HTMLButtonElement>(
        '[data-testid="repo-ci-caps-cap-docker-hub"]',
      )!.disabled,
    ).toBe(true);
    expect(
      ci.querySelector('[data-testid="ci-capabilities-denied"]')?.textContent,
    ).toContain("capabilities:admin");
    expect(api.calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("keeps the plan, interval and ownership helpers to the classic rules", () => {
    expect(importIntervalText(0)).toBe("");
    expect(importIntervalText(900)).toBe("15");
    expect(importIntervalSeconds("")).toEqual({ seconds: 0, error: null });
    expect(importIntervalSeconds("5")).toEqual({ seconds: 300, error: null });
    expect(importIntervalSeconds("0.5").error).toContain("at least 1");
    expect(importIntervalSeconds("abc").error).toContain("at least 1");

    expect(saidLabel("on")).toBe("On, set here");
    expect(saidLabel("off")).toBe("Off, set here");
    expect(saidLabel("")).toBe("Nobody has said");

    expect(repeatError("1")).toBe("");
    expect(repeatError("0")).not.toBe("");
    expect(repeatError("1.5")).not.toBe("");

    expect(
      withoutDanglingDependencies([
        { templateId: "a", repeat: "1", dependsOn: ["b", "gone"] },
        { templateId: "b", repeat: "1", dependsOn: [] },
      ])[0].dependsOn,
    ).toEqual(["b"]);

    const form = planFormOf({
      requireApproval: true,
      items: [{ templateId: "a", repeat: 2, dependsOn: null }],
    });
    form.items.push({ templateId: "", repeat: "1", dependsOn: [] });
    expect(planPayload(form)).toEqual({
      requireApproval: true,
      autoPromote: false,
      items: [{ templateId: "a", repeat: 2, dependsOn: [] }],
    });

    expect(
      qualifiableTemplates(
        [
          { id: "x", name: "x", title: "", description: "" },
          { id: "y", name: "y", title: "", description: "", repo: "Acme/P" },
          { id: "z", name: "z", title: "", description: "", repo: "o/q" },
          { id: "s", name: "s", title: "", description: "", system: true },
        ],
        "acme/p",
      ).map((t) => t.id),
    ).toEqual(["x", "y"]);
  });

  it("shows what acme/platform declares on the Advanced tab and switches declarations off", async () => {
    const { el, api, harness } = await mountRepo("/repos/acme/platform");
    el.querySelector<HTMLButtonElement>(
      '[data-testid="repo-tab-advanced"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 15));
    harness.detectChanges();
    expect(TestBed.inject(Router).url).toBe("/repos/acme/platform/advanced");

    const card = (id: string) =>
      el.querySelector(`[data-testid="card-repo-${id}"]`)?.textContent ?? "";
    expect(card("declarations")).toContain(
      "Read at 0f1e2d3c4b5a: 1 template(s), 0 suite(s), 0 schedule(s), 1 trigger(s)",
    );
    expect(card("triggers")).toContain(".grain/triggers/payments.yml");
    expect(card("push-refs")).toContain("refs/tags/nightly-* (force)");
    expect(card("ports")).toContain(
      "Grain ports each merge between main, candidate",
    );
    expect(card("ports")).toContain("nothing (not a paired branch)");
    // The repository's own port finding is said once, in the
    // declarations card; the operator's is said under Ports.
    expect(card("ports")).not.toContain("main->candidtae is not paired");
    expect(card("ports")).toContain("operator: candidate->main names nothing");

    const sw = el.querySelector<HTMLButtonElement>(
      '[data-testid="switch-repo-declarations"]',
    )!;
    expect(sw.getAttribute("aria-checked")).toBe("true");
    sw.click();
    await new Promise((r) => setTimeout(r, 10));
    expect(
      api.calls
        .filter((c) => c.key === "PUT /api/repos/{owner}/{name}/declarations")
        .map((c) => c.body),
    ).toEqual([{ declarations: false }]);
  });

  it("asks before Remove, naming what it strands, then removes and goes to the Repos list", async () => {
    const api = new FakeRepoApi();
    api.configBody.targetRepos = ["acme/platform", "acme/other"];
    const { el, harness } = await mountRepo(
      "/repos/acme/platform/advanced",
      api,
    );
    el.querySelector<HTMLButtonElement>(
      '[data-testid="remove-repo-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 15));
    harness.detectChanges();
    expect(
      api.calls.find((c) => c.key === "POST /api/repos/plan")?.body,
    ).toEqual({ targetRepos: ["acme/other"] });
    const dialog = el.querySelector('[data-testid="confirm-dialog"]');
    expect(dialog?.textContent).toContain(
      "Remove acme/platform from target repos?",
    );
    expect(dialog?.textContent).toContain(
      "2 tasks still in play there are parked",
    );
    expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);

    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 15));
    expect(
      api.calls.filter((c) => c.key === "DELETE /api/repos/{owner}/{name}"),
    ).toEqual([
      {
        method: "DELETE",
        key: "DELETE /api/repos/{owner}/{name}",
        params: { owner: "acme", name: "platform" },
      },
    ]);
    expect(TestBed.inject(Router).url).toBe("/repos");
  });

  it("offers no Remove on the config repo or an untargeted repo, and disables it with the reason for a role without repos:write", async () => {
    const api = new FakeRepoApi();
    api.configBody.targetRepos = ["acme/platform"];
    api.configBody.configRepo = "acme/platform";
    const { el } = await mountRepo("/repos/acme/platform/advanced", api);
    expect(el.querySelector('[data-testid="remove-repo-btn"]')).toBeNull();
    expect(
      el.querySelector('[data-testid="remove-repo-desc"]')?.textContent,
    ).toContain("configuration repository and cannot be removed");
    TestBed.resetTestingModule();

    const other = new FakeRepoApi();
    other.configBody.targetRepos = ["acme/elsewhere"];
    const untargeted = await mountRepo("/repos/acme/platform/advanced", other);
    expect(
      untargeted.el.querySelector('[data-testid="remove-repo-btn"]'),
    ).toBeNull();
    expect(
      untargeted.el.querySelector('[data-testid="remove-repo-desc"]')
        ?.textContent,
    ).toContain("not one of this deployment's target repos");
    TestBed.resetTestingModule();

    const viewer = new FakeRepoApi();
    viewer.configBody.targetRepos = ["acme/platform"];
    viewer.configBody.access = {
      role: "grain/viewer",
      roles: ["grain/viewer"],
      full: false,
      scopes: ["repos:read"],
    };
    const limited = await mountRepo("/repos/acme/platform/advanced", viewer);
    const btn = limited.el.querySelector<HTMLButtonElement>(
      '[data-testid="remove-repo-btn"]',
    )!;
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-describedby")).toBe("remove-locked-reason");
    expect(
      limited.el.querySelector('[data-testid="remove-locked-reason"]')
        ?.textContent,
    ).toContain("Your role, grain/viewer, cannot remove repositories");
    expect(
      limited.el.querySelector<HTMLButtonElement>(
        '[data-testid="switch-repo-declarations"]',
      )!.disabled,
    ).toBe(true);
    expect(
      limited.el.querySelector('[data-testid="declarations-locked-reason"]')
        ?.textContent,
    ).toContain("cannot switch declarations on or off");
  });
});

describe("removeRepoMessage and declaredPushRef", () => {
  it("says removing the only allowed repo unrestricts the deployment", () => {
    expect(removeRepoMessage("acme/a", ["Acme/A"], [])).toContain(
      "An empty allowlist is what means unrestricted",
    );
    expect(removeRepoMessage("acme/a", ["acme/a", "acme/b"], [])).toBe(
      "New tasks won't be able to target acme/a until it's added back, and it drops off the repos list. Tasks already filed there are not affected.",
    );
    expect(
      removeRepoMessage("acme/a", ["acme/a", "acme/b"], ["3 tasks parked."]),
    ).toMatch(/repos list\. 3 tasks parked\.$/);
  });

  it("words a push ref with its flags", () => {
    expect(declaredPushRef({ ref: "refs/tags/v*" })).toBe("refs/tags/v*");
    expect(
      declaredPushRef({ ref: "refs/heads/rel", force: true, delete: true }),
    ).toBe("refs/heads/rel (force, delete)");
  });
});
