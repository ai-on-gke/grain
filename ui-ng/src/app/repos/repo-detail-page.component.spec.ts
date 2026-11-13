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
  DISCARD_PROMPT,
  RepoDetailPageComponent,
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
  refusePost: string | null = null;
  postResults: Record<string, unknown> = {};

  configBody: GrainConfig = {
    version: "v3.6.0",
    access: {
      role: "grain/operator",
      roles: ["grain/operator"],
      full: true,
      scopes: [],
    },
  };

  config = () => Promise.resolve(structuredClone(this.configBody));

  get = (key: string, params?: RouteParams) => {
    this.calls.push({ method: "GET", key, params });
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
      return Promise.resolve([{ name: "v1.2", status: "open" }]);
    }
    if (key === "GET /api/repos/{owner}/{name}/releases/{release}/candidates") {
      return Promise.resolve([
        { id: "rc-1", version: "v1.2.0-rc1", status: "qualifying" },
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
    if (this.refusePost === key) {
      return new Promise((_, reject) =>
        setTimeout(() => reject(new Error("name: not a valid release name"))),
      );
    }
    return Promise.resolve(this.postResults[key] ?? { ok: true });
  };

  delete = (key: string, params?: RouteParams) => {
    this.calls.push({ method: "DELETE", key, params });
    return Promise.resolve({ ok: true });
  };
}

const leaveGuard = (c: RepoDetailPageComponent) => c.confirmLeave();

async function mountRepo(url: string, api = new FakeRepoApi()) {
  localStorage.clear();
  TestBed.configureTestingModule({
    providers: [
      provideRouter(
        [
          {
            path: "repos/:owner/:name",
            component: RepoDetailPageComponent,
            canDeactivate: [leaveGuard],
          },
          {
            path: "repos/:owner/:name/:tab",
            component: RepoDetailPageComponent,
            canDeactivate: [leaveGuard],
          },
          { path: "settings/repositories", children: [] },
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

    // Cut & promote RC + approve qualification
    el.querySelector<HTMLButtonElement>('[data-testid="cut-rc-v1.2"]')!.click();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="promote-rc-v1.2"]',
    )!.click();
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="approve-qualification-rc-1"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));

    expect(
      api.calls.some(
        (c) =>
          c.key ===
          "POST /api/repos/{owner}/{name}/releases/{release}/candidates",
      ),
    ).toBe(true);
    expect(
      api.calls.some(
        (c) =>
          c.key ===
          "POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote",
      ),
    ).toBe(true);
    expect(
      api.calls.some(
        (c) =>
          c.key ===
          "POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve",
      ),
    ).toBe(true);
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
  // 2026-11-13 repository walkthrough (task 5386): "Import issues now"
  // said only "Issue import triggered", whatever the pass read.
  it("says what an import pass read and imported, and that it read nothing when the source cannot be listed", async () => {
    const api = new FakeRepoApi();
    const key = "POST /api/repos/{owner}/{name}/import-issues";
    api.postResults[key] = {
      source: "acme/platform",
      open: 2,
      imported: [{ number: 7 }],
      skipped: 1,
    };
    const { el, harness } = await mountRepo("/repos/acme/platform", api);
    const btn = el.querySelector<HTMLButtonElement>(
      '[data-testid="import-issues-btn"]',
    )!;
    btn.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    const outcome = () =>
      el.querySelector('[data-testid="import-issues-outcome"]');
    expect(outcome()?.textContent?.trim()).toBe(
      "Read 2 open issues on acme/platform: imported 1, 1 already imported.",
    );

    api.postResults[key] = {
      source: "acme/nosuch",
      open: 0,
      skipped: 0,
      error: "GitHub answered 404: Not Found",
    };
    btn.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(outcome()?.textContent?.trim()).toBe(
      "Could not read the open issues on acme/nosuch: GitHub answered 404: Not Found",
    );
    expect(outcome()?.className).toContain("s-field-error");
  });

  it("clears a refused setting's banner once that setting saves cleanly", async () => {
    const api = new FakeRepoApi();
    api.refusePut = "PUT /api/repos/{owner}/{name}/ci-mode";
    const { el, harness } = await mountRepo("/repos/acme/platform/ci", api);
    const pick = async (mode: string) => {
      el.querySelector<HTMLButtonElement>(
        '[data-testid="select-repo-ci-mode"]',
      )!.click();
      harness.detectChanges();
      el.querySelector<HTMLButtonElement>(
        `[data-testid="select-repo-ci-mode-opt-${mode}"]`,
      )!.click();
      await new Promise((r) => setTimeout(r, 10));
      harness.detectChanges();
    };
    const banner = () => el.querySelector('[data-testid="repo-error-banner"]');
    await pick("github");
    expect(banner()?.textContent).toContain("ciMode");

    // Another setting saving does not speak for the refused one.
    el.querySelector<HTMLButtonElement>(
      '[data-testid="repo-tab-merge-queue"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      'button[aria-label="Require branch up to date"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(banner()).not.toBeNull();

    el.querySelector<HTMLButtonElement>('[data-testid="repo-tab-ci"]')!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    api.refusePut = null;
    await pick("github");
    expect(banner()).toBeNull();
  });

  // Promote, merge back and delete went through on one click; the
  // classic Releases pane asks first and names the branches.
  it("asks before promoting, merging back or removing a release", async () => {
    const { el, api, harness } = await mountRepo(
      "/repos/acme/platform/releases",
    );
    const click = async (sel: string) => {
      el.querySelector<HTMLButtonElement>(sel)!.click();
      await new Promise((r) => setTimeout(r, 10));
      harness.detectChanges();
    };
    const dialog = () => el.querySelector('[data-testid="confirm-dialog"]');
    const del = "DELETE /api/repos/{owner}/{name}/releases/{release}";
    const merge = "POST /api/repos/{owner}/{name}/releases/{release}/merge";
    const promote =
      "POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote";

    await click('[data-testid="delete-release-v1.2"]');
    expect(dialog()?.textContent).toContain(
      "Remove release v1.2? This drops the release and its candidates from grain.",
    );
    await click('[data-testid="confirm-cancel"]');
    expect(dialog()).toBeNull();

    await click('[data-testid="merge-release-v1.2"]');
    expect(dialog()?.textContent).toContain(
      "Merge v1.2 into acme/platform's default branch?",
    );
    await click('[data-testid="confirm-cancel"]');

    await click('[data-testid="promote-rc-v1.2"]');
    expect(dialog()?.textContent).toContain("Promote");
    await click('[data-testid="confirm-cancel"]');
    expect(
      api.calls.filter((c) => [del, merge, promote].includes(c.key)),
    ).toEqual([]);

    await click('[data-testid="delete-release-v1.2"]');
    await click('[data-testid="confirm-accept"]');
    expect(api.calls.some((c) => c.key === del)).toBe(true);
  });

  it("reports a refused release name on its box and clears it on edit", async () => {
    const api = new FakeRepoApi();
    api.refusePost = "POST /api/repos/{owner}/{name}/releases";
    const { el, harness } = await mountRepo(
      "/repos/acme/platform/releases",
      api,
    );
    const box = el.querySelector<HTMLInputElement>(
      '[data-testid="input-new-release-name"]',
    )!;
    box.value = "bad name";
    box.dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="create-release-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="new-release-name-error"]')?.textContent,
    ).toContain("not a valid release name");
    expect(box.getAttribute("aria-invalid")).toBe("true");
    expect(el.querySelector('[data-testid="repo-error-banner"]')).toBeNull();

    box.value = "v1.3";
    box.dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="new-release-name-error"]'),
    ).toBeNull();
    expect(box.getAttribute("aria-invalid")).toBeNull();
  });
  // Leaving with an unsaved setup command (or workflow, issue-import,
  // prompt or capabilities edit) dropped it silently; the classic page
  // asks first.
  it("asks before leaving with an unsaved agents edit, and not once it is saved", async () => {
    const { el, harness } = await mountRepo("/repos/acme/platform/agents");
    const router = TestBed.inject(Router);
    const setup = el.querySelector<HTMLInputElement | HTMLTextAreaElement>(
      "#rp-setup",
    )!;
    setup.value = "make deps";
    setup.dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();

    const nav = router.navigateByUrl("/settings/repositories");
    await new Promise((r) => setTimeout(r, 10));
    harness.detectChanges();
    expect(
      el.querySelector('[data-testid="confirm-dialog"]')?.textContent,
    ).toContain(DISCARD_PROMPT);
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-cancel"]',
    )!.click();
    expect(await nav).toBe(false);
    expect(router.url).toBe("/repos/acme/platform/agents");

    el.querySelector<HTMLButtonElement>(
      '[data-testid="save-repo-agents-btn"]',
    )!.click();
    await new Promise((r) => setTimeout(r, 20));
    harness.detectChanges();
    expect(await router.navigateByUrl("/settings/repositories")).toBe(true);
  });
  // Review of task 5386: a tab click with unsaved edits asks through the
  // leave guard; Cancel used to leave the Agents pane under a /ci URL,
  // and the click back on CI then reloaded the workflows over the edit.
  it("keeps the tab and the unsaved workflow edit when a tab switch is cancelled", async () => {
    const { el, harness } = await mountRepo("/repos/acme/platform/ci");
    const router = TestBed.inject(Router);
    const settle = async () => {
      await new Promise((r) => setTimeout(r, 20));
      harness.detectChanges();
    };
    const yaml = () =>
      el.querySelector<HTMLTextAreaElement>(
        '[data-testid="input-repo-workflow"]',
      );
    yaml()!.value = "name: unsaved";
    yaml()!.dispatchEvent(new Event("input", { bubbles: true }));
    harness.detectChanges();

    el.querySelector<HTMLButtonElement>(
      '[data-testid="repo-tab-agents"]',
    )!.click();
    await settle();
    expect(
      el.querySelector('[data-testid="confirm-dialog"]')?.textContent,
    ).toContain(DISCARD_PROMPT);
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-cancel"]',
    )!.click();
    await settle();
    expect(router.url).toBe("/repos/acme/platform/ci");
    expect(
      el
        .querySelector('[data-testid="repo-tab-ci"]')
        ?.getAttribute("aria-current"),
    ).toBe("page");
    expect(yaml()?.value).toBe("name: unsaved");

    el.querySelector<HTMLButtonElement>('[data-testid="repo-tab-ci"]')!.click();
    await settle();
    expect(yaml()?.value).toBe("name: unsaved");

    el.querySelector<HTMLButtonElement>(
      '[data-testid="repo-tab-agents"]',
    )!.click();
    await settle();
    el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!.click();
    await settle();
    expect(router.url).toBe("/repos/acme/platform/agents");
    expect(
      el
        .querySelector('[data-testid="repo-tab-agents"]')
        ?.getAttribute("aria-current"),
    ).toBe("page");
  });
});
