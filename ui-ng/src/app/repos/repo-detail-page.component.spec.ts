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
import { routes as appRoutes } from "../app.routes";
import {
  RepoDetailPageComponent,
  declaredPushRef,
  importOutcomeText,
  releaseActionReasons,
  removeRepoMessage,
  unjudgedGatesFor,
  unknownReleaseText,
} from "./repo-detail-page.component";

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
  releasesList: { name: string; status: string; prodBranch: string }[] = [
    { name: "v1.2", status: "active", prodBranch: "v1.2" },
  ];
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
  };

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
      });
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
        pendingReleases: [
          { version: "v1.3.0", summary: "Trunk release candidate" },
        ],
      });
    }
    if (key === "GET /api/repos/{owner}/{name}/releases") {
      return Promise.resolve(structuredClone(this.releasesList));
    }
    if (key === "GET /api/repos/{owner}/{name}/releases/{release}/candidates") {
      const rel = String(params?.["release"] ?? "v1.2");
      return Promise.resolve([
        {
          id: rel === "v1.2" ? "rc-1" : `rc-${rel}`,
          version: `${rel}.0-rc1`,
          branch: `${rel}.rc.1`,
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
        setTimeout(() =>
          reject(new Error('ciMode: must be "grain", "github", or "none"')),
        ),
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
            path: "repos/:owner/:name/releases/:release",
            component: RepoDetailPageComponent,
          },
          {
            path: "repos/:owner/:name/:tab",
            component: RepoDetailPageComponent,
          },
          {
            path: "repos/:host/:owner/:name",
            component: RepoDetailPageComponent,
          },
          {
            path: "repos/:host/:owner/:name/releases/:release",
            component: RepoDetailPageComponent,
          },
          {
            path: "repos/:host/:owner/:name/:tab",
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

  it("deep-links to a release, scrolls its row into view, puts selected releases in the URL, and guards leaving via app.routes", async () => {
    const api = new FakeRepoApi();
    api.releasesList = [
      { name: "v2.0", status: "active", prodBranch: "v2.0" },
      { name: "v1.2", status: "active", prodBranch: "v1.2" },
    ];
    localStorage.clear();
    TestBed.configureTestingModule({
      providers: [
        provideRouter(appRoutes, withComponentInputBinding()),
        { provide: APP_BASE_HREF, useValue: "/next/" },
        { provide: GrainApi, useValue: api },
      ],
    });
    const scrollSpy = vi.fn();
    const origScroll = HTMLElement.prototype.scrollIntoView;
    HTMLElement.prototype.scrollIntoView = scrollSpy;
    try {
      const harness = await RouterTestingHarness.create(
        "/repos/acme/platform/releases/v1.2",
      );
      harness.fixture.autoDetectChanges(true);
      await harness.fixture.whenStable();
      await new Promise((r) => setTimeout(r, 25));
      await harness.fixture.whenStable();
      const el = harness.fixture.nativeElement as HTMLElement;
      const router = TestBed.inject(Router);

      expect(router.url).toBe("/repos/acme/platform/releases/v1.2");
      expect(
        el
          .querySelector('[data-testid="repo-tab-releases"]')
          ?.getAttribute("aria-current"),
      ).toBe("page");
      expect(
        el
          .querySelector('[data-testid="release-row-v1.2"]')
          ?.getAttribute("aria-current"),
      ).toBe("true");
      expect(
        el
          .querySelector('[data-testid="release-row-v2.0"]')
          ?.getAttribute("aria-current"),
      ).toBeNull();
      // Loaded v1.2's candidates directly, never v2.0's.
      expect(
        api.calls
          .filter(
            (c) =>
              c.key ===
              "GET /api/repos/{owner}/{name}/releases/{release}/candidates",
          )
          .map((c) => c.params?.["release"]),
      ).toEqual(["v1.2"]);
      expect(scrollSpy).toHaveBeenCalled();

      // Selecting v2.0 puts its URL in the address bar.
      el.querySelector<HTMLButtonElement>(
        '[data-testid="inspect-rc-v2.0"]',
      )!.click();
      await new Promise((r) => setTimeout(r, 20));
      await harness.fixture.whenStable();
      expect(router.url).toBe("/repos/acme/platform/releases/v2.0");
      expect(
        el
          .querySelector('[data-testid="release-row-v2.0"]')
          ?.getAttribute("aria-current"),
      ).toBe("true");

      // Hosted per-release route resolves host/owner/name and release, and
      // keeps repoLeaveGuard active.
      await harness.navigateByUrl(
        "/repos/git.example.com/acme/platform/releases/v1.2",
      );
      await new Promise((r) => setTimeout(r, 25));
      await harness.fixture.whenStable();
      const hostedEl = harness.fixture.nativeElement as HTMLElement;
      expect(
        hostedEl.querySelector('[data-testid="repo-detail-heading"]')
          ?.textContent,
      ).toContain("git.example.com/acme/platform");
      expect(
        hostedEl
          .querySelector('[data-testid="release-link-v2.0"]')
          ?.getAttribute("href"),
      ).toBe("/next/repos/git.example.com/acme/platform/releases/v2.0");
    } finally {
      HTMLElement.prototype.scrollIntoView = origScroll;
    }
  });

  it("shows the classic notice and links back to Releases when the URL names an unknown release", async () => {
    const api = new FakeRepoApi();
    api.releasesList = [
      { name: "v2.0", status: "active", prodBranch: "v2.0" },
      { name: "v1.2", status: "active", prodBranch: "v1.2" },
    ];
    const { el, harness } = await mountRepo(
      "/repos/acme/platform/releases/nosuch",
      api,
    );
    const banner = el.querySelector('[data-testid="unknown-release-banner"]');
    expect(banner?.textContent).toContain(
      "acme/platform has no release named \u201cnosuch\u201d -- showing v2.0 instead.",
    );
    const backLink = el.querySelector<HTMLAnchorElement>(
      '[data-testid="unknown-release-back-link"]',
    )!;
    expect(backLink.getAttribute("href")).toBe(
      "/next/repos/acme/platform/releases",
    );
    backLink.click();
    await new Promise((r) => setTimeout(r, 20));
    await harness.fixture.whenStable();
    expect(TestBed.inject(Router).url).toBe("/repos/acme/platform/releases");
    expect(
      harness.fixture.nativeElement.querySelector(
        '[data-testid="unknown-release-banner"]',
      ),
    ).toBeNull();
  });
});

describe("removeRepoMessage, declaredPushRef and unknownReleaseText", () => {
  it("words an unknown release with or without a fallback release", () => {
    expect(unknownReleaseText("acme/widgets", "gone", "1.0")).toBe(
      "acme/widgets has no release named \u201cgone\u201d -- showing 1.0 instead. The link may be stale, or the release deleted; pick another below.",
    );
    expect(unknownReleaseText("acme/widgets", "gone", "")).toBe(
      "acme/widgets has no release named \u201cgone\u201d -- the link may be stale, or the release deleted.",
    );
  });
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
