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
  importOutcomeText,
  releaseActionReasons,
  removeRepoMessage,
} from "./repo-detail-page.component";

interface RepoCall {
  method: string;
  key: string;
  params?: RouteParams;
  body?: unknown;
}

class FakeRepoApi {
  calls: RepoCall[] = [];
  ciMode = "grain";
  refusePut: string | null = null;

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

  // GET /api/tasks?repo=..., for the Auto-merge tab's list.
  tasks: Record<string, unknown>[] = [];

  get = (key: string, params?: RouteParams) => {
    this.calls.push({ method: "GET", key, params });
    if (key === "GET /api/tasks") {
      return Promise.resolve(structuredClone(this.tasks));
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
        setTimeout(() =>
          reject(new Error('ciMode: must be "grain", "github", or "none"')),
        ),
      );
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
  // Merge ordering was removed: the tab is Auto-merge, the old
  // /merge-queue link still lands on it, and its list says what each pull
  // request waits for with no position in any queue -- and leaves out
  // tasks already closed, which `grain merge-queue` listed (#4538).
  it("renames Merge Queue to Auto-merge, redirects the old tab, and lists only open auto-merging work", async () => {
    const api = new FakeRepoApi();
    api.tasks = [
      {
        id: "11",
        title: "Bump Go",
        repo: "acme/platform",
        state: "completed",
        mergeQueueState: "waiting_ci",
        mergeQueueNote: "waiting on checks that have not finished (`go`)",
      },
      {
        id: "12",
        title: "Fix parser",
        repo: "acme/platform",
        state: "completed",
        mergeQueueState: "repairing",
        mergeQueueNote:
          "an agent is repairing this branch now (it has conflicts with `main`; automatic repair 1 of 2)",
      },
      {
        id: "730",
        title: "Long since closed",
        repo: "acme/platform",
        state: "closed",
        mergeQueueState: "waiting_ci",
      },
      {
        id: "798",
        title: "Merged already",
        repo: "acme/platform",
        state: "completed",
        mergeQueueState: "merging",
        prMergedAt: "2026-11-14T00:00:00Z",
      },
      {
        id: "13",
        title: "Still running",
        repo: "acme/platform",
        state: "running",
      },
    ];
    const { el, harness } = await mountRepo(
      "/repos/acme/platform/merge-queue",
      api,
    );
    await harness.fixture.whenStable();
    await new Promise((r) => setTimeout(r, 20));
    harness.detectChanges();
    const router = TestBed.inject(Router);
    expect(router.url).toBe("/repos/acme/platform/auto-merge");
    const tab = el.querySelector('[data-testid="repo-tab-auto-merge"]');
    expect(tab?.textContent?.trim()).toBe("Auto-merge");
    expect(el.textContent).not.toContain("Merge Queue");
    expect(el.textContent).not.toMatch(/merge queue/i);
    const rows = [
      ...el.querySelectorAll<HTMLElement>('[data-testid^="auto-merging-"]'),
    ].filter((r) => r.tagName === "LI");
    expect(rows.map((r) => r.dataset["testid"])).toEqual([
      "auto-merging-11",
      "auto-merging-12",
    ]);
    expect(rows[0].textContent).toContain("Auto-merging · waiting for CI");
    expect(rows[1].textContent).toContain(
      "Auto-merging · branch conflicted, repair 1 of 2",
    );
  });

  // 2026-11-13 repository walkthrough: one click on the switch let
  // base-branch repair PRs merge with no human; the classic page asks.
  it("asks before letting base-branch repairs bypass the human gate, and not when turning it off", async () => {
    const { el, api, harness } = await mountRepo(
      "/repos/acme/platform/auto-merge",
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
