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

import { describe, expect, it } from "vitest";
import {
  buildPath,
  narrowingFromQuery,
  newTaskPath,
  parsePath,
  processPath,
  queryFromNarrowing,
} from "./paths.js";
import { kindNarrowing } from "./processFilters.js";

describe("parsePath", () => {
  it("treats / as the tasks view", () => {
    expect(parsePath("/")).toEqual({ view: "tasks" });
  });

  it("parses each sidebar destination", () => {
    for (const view of ["inbox", "repos", "processes", "ci"]) {
      expect(parsePath(`/${view}`)).toEqual({ view });
    }
  });

  // The fixed board and timeline were two more of those until
  // grain/task-162 made both of them kinds of saved board instead. A
  // link somebody kept to either lands on the task list, which is what
  // every other path this build does not recognize already does.
  it("falls back to the task list for the board and timeline that were", () => {
    expect(parsePath("/board")).toEqual({ view: "tasks" });
    // The question such a link was asking still survives the fallback:
    // it lands on the same tasks, as the list.
    expect(parsePath("/gantt", "?q=ci")).toEqual({
      view: "tasks",
      narrowing: { search: "ci", sort: [], filters: [] },
    });
  });

  // The inbox shows tasks and still takes no narrowing: it is one fixed
  // question rather than a view of the backlog somebody refines
  // (paths.js's showsTaskView).
  // grain/task-1355: the rail's state view rides in the query beside
  // the narrowing, on the flat list only.
  it("reads the rail's state view off the flat list's query", () => {
    expect(parsePath("/", "?state=closed")).toEqual({
      view: "tasks",
      stateFilter: "closed",
    });
    expect(
      parsePath("/", "?state=blocked&f=repo:none:acme/widgets"),
    ).toMatchObject({
      stateFilter: "blocked",
      narrowing: {
        filters: [{ attr: "repo", op: "none", values: ["acme/widgets"] }],
      },
    });
    // A link written while task views defaulted to leaving interactive
    // tasks out still carries that clause; it is dropped, since those
    // tasks are never in a task view now.
    expect(parsePath("/", "?state=blocked&f=kind:none:interactive")).toEqual({
      view: "tasks",
      stateFilter: "blocked",
    });
    expect(parsePath("/", "?state=all")).toEqual({ view: "tasks" });
    expect(parsePath("/", "?state=%3Cx%3E")).toEqual({ view: "tasks" });
    expect(parsePath("/inbox", "?state=closed")).toEqual({ view: "inbox" });
    expect(parsePath("/tasks/7", "?state=closed")).toEqual({
      view: "tasks",
      taskId: "7",
    });
  });

  it("ignores a query on the inbox", () => {
    expect(parsePath("/inbox", "?q=ci&repo=acme/widgets")).toEqual({
      view: "inbox",
    });
  });

  it("sends /sandboxes to System while retired logs falls back to tasks", () => {
    expect(parsePath("/logs")).toEqual({ view: "tasks" });
    expect(parsePath("/sandboxes")).toEqual({
      view: "system",
      systemTab: "sandboxes",
    });
  });

  // The combined Caches page (grain/task-321, grain/task-357) is a System
  // tab now, and every path it had goes there.
  it("sends /caches, /cache and /actions-cache to System → Caches", () => {
    const caches = { view: "system", systemTab: "caches" };
    expect(parsePath("/caches")).toEqual(caches);
    expect(buildPath(caches)).toBe("/system/caches");
    expect(parsePath("/cache")).toEqual(caches);
    expect(parsePath("/actions-cache")).toEqual(caches);
  });

  it("parses a task detail path", () => {
    expect(parsePath("/tasks/42")).toEqual({ view: "tasks", taskId: "42" });
  });

  it("parses a standing session path, including the unqualified path and /director", () => {
    expect(parsePath("/session")).toEqual({ view: "session" });
    expect(parsePath("/session/director")).toEqual({
      view: "session",
      sessionId: "director",
    });
    expect(parsePath("/director")).toEqual({
      view: "session",
      sessionId: "director",
    });
  });

  it("parses a repo page path, and its releases pane", () => {
    expect(parsePath("/repos/acme/widgets")).toEqual({
      view: "repos",
      repo: "acme/widgets",
    });
    expect(parsePath("/repo/acme/widgets")).toEqual({
      view: "repos",
      repo: "acme/widgets",
    });
    expect(parsePath("/repos/acme/widgets/releases")).toEqual({
      view: "repos",
      repo: "acme/widgets",
      showReleases: true,
    });
    expect(parsePath("/repo/acme/widgets/releases")).toEqual({
      view: "repos",
      repo: "acme/widgets",
      showReleases: true,
    });
  });

  // docs/design/settings-panel.md §2.3: the repo page's tab is in its
  // path, so Settings → Repositories can link to one.
  it("parses and builds a repo page's tab", () => {
    for (const tab of [
      "ci",
      "merge-queue",
      "agents",
      "branches",
      "releases",
      "advanced",
    ]) {
      const path = `/repos/acme/widgets/settings/${tab}`;
      expect(parsePath(path)).toEqual({
        view: "repos",
        repo: "acme/widgets",
        repoTab: tab,
      });
      expect(buildPath(parsePath(path))).toBe(path);
    }
    // Overview is the page's own path.
    expect(parsePath("/repos/acme/widgets/settings/overview")).toEqual({
      view: "repos",
      repo: "acme/widgets",
      repoTab: "overview",
    });
    expect(
      buildPath({ view: "repos", repo: "acme/widgets", repoTab: "overview" }),
    ).toBe("/repos/acme/widgets");
    // A tab this build does not have opens the page itself.
    expect(parsePath("/repos/acme/widgets/settings/nope")).toEqual({
      view: "repos",
      repo: "acme/widgets",
    });
    expect(parsePath("/repos/acme/widgets/settings")).toEqual({
      view: "repos",
      repo: "acme/widgets",
    });
    // The settings tab is not the releases pane.
    expect(
      parsePath("/repos/acme/widgets/settings/releases"),
    ).not.toHaveProperty("showReleases");
    // Off the default host too.
    const depot = "depot.code.forge.test/team/widgets";
    expect(parsePath(`/repos/${depot}/settings/ci`)).toEqual({
      view: "repos",
      repo: depot,
      repoTab: "ci",
    });
    expect(buildPath({ view: "repos", repo: depot, repoTab: "ci" })).toBe(
      `/repos/${depot}/settings/ci`,
    );
    // A trigger opens its repository's Advanced tab, at that tab's path.
    expect(
      buildPath(
        parsePath("/processes/triggers/acme/widgets/.grain/triggers/docs.yml"),
      ),
    ).toBe("/repos/acme/widgets/settings/advanced");
  });

  // Which release the pane is showing is part of its address, so a reload
  // or a link opens that release and not whichever is newest.
  // A repo off the default host is host/owner/name, and its page is
  // /repos/host/owner/name -- not owner "depot.code.forge.test", name
  // "experimental".
  it("parses a host-qualified repo page path, its releases and its pull requests", () => {
    const depot = "depot.code.forge.test/experimental/bsalmon-test";
    expect(parsePath(`/repos/${depot}`)).toEqual({
      view: "repos",
      repo: depot,
    });
    expect(buildPath({ view: "repos", repo: depot })).toBe(`/repos/${depot}`);
    expect(parsePath(`/repos/${depot}/releases/r1`)).toEqual({
      view: "repos",
      repo: depot,
      showReleases: true,
      release: "r1",
    });
    const parsed = parsePath(`/repos/${depot}/compare/main...grain/task-6`);
    expect(parsed).toEqual({
      view: "repos",
      repo: depot,
      repoComparison: { base: "main", head: "grain/task-6" },
    });
    expect(buildPath(parsed)).toBe(
      `/repos/${depot}/compare/main...grain/task-6`,
    );
  });

  it("parses the release a releases path names", () => {
    expect(parsePath("/repos/acme/widgets/releases/r2-1004")).toEqual({
      view: "repos",
      repo: "acme/widgets",
      showReleases: true,
      release: "r2-1004",
    });
  });

  it("parses one of that repo's pull requests, refs and all", () => {
    const parsed = parsePath("/repos/grain/config/compare/main...grain/task-6");
    expect(parsed).toEqual({
      view: "repos",
      repo: "grain/config",
      repoComparison: { base: "main", head: "grain/task-6" },
    });
    expect(buildPath(parsed)).toBe(
      "/repos/grain/config/compare/main...grain/task-6",
    );
  });

  it("uses the repository default for a pull request with no explicit base", () => {
    const parsed = parsePath("/repos/acme/widgets/compare/grain/task-12");
    expect(parsed).toEqual({
      view: "repos",
      repo: "acme/widgets",
      repoComparison: { base: "", head: "grain/task-12" },
    });
    expect(buildPath(parsed)).toBe("/repos/acme/widgets/compare/grain/task-12");
  });

  it("reads a ref a link escaped, and writes it back plainly", () => {
    expect(
      parsePath("/repos/acme/widgets/compare/main...grain%2Ftask-12"),
    ).toEqual({
      view: "repos",
      repo: "acme/widgets",
      repoComparison: { base: "main", head: "grain/task-12" },
    });
  });

  it("falls back to the repo page for a comparison naming no head", () => {
    expect(parsePath("/repos/acme/widgets/compare")).toEqual({
      view: "repos",
      repo: "acme/widgets",
    });
    expect(parsePath("/repos/acme/widgets/compare/main...")).toEqual({
      view: "repos",
      repo: "acme/widgets",
    });
  });

  // The spelling links written before pull requests had a page of their
  // own use (grain/task-375). It still opens the same comparison, and
  // App's own canonical-URL effect rewrites the address bar to the path.
  it("still reads a comparison out of a repo page's own query", () => {
    const parsed = parsePath(
      "/repos/grain/config",
      "?base=main&head=grain%2Ftask-6",
    );
    expect(parsed).toEqual({
      view: "repos",
      repo: "grain/config",
      repoComparison: { base: "main", head: "grain/task-6" },
    });
    expect(buildPath(parsed)).toBe(
      "/repos/grain/config/compare/main...grain/task-6",
    );
  });

  it("falls back to the repo list for an owner with no repo name", () => {
    expect(parsePath("/repos/acme")).toEqual({ view: "repos" });
    expect(parsePath("/repo/acme")).toEqual({ view: "repos" });
  });

  it("parses an open schedule, template and suite under Processes", () => {
    expect(parsePath("/processes/schedules/sched-1")).toEqual({
      view: "processes",
      scheduleId: "sched-1",
    });
    expect(parsePath("/processes/templates/template-1")).toEqual({
      view: "processes",
      templateId: "template-1",
    });
    expect(parsePath("/processes/suites/suite-1")).toEqual({
      view: "processes",
      suiteId: "suite-1",
    });
  });

  // A declared row's id carries colons, and the row's own link writes it
  // percent-encoded (RowLink's href) -- which is the URL a new tab, a
  // copied link or a bookmark opens. Both spellings name the same item
  // (grain/task-1533).
  it("decodes a percent-encoded declared id in a schedule, template or suite path", () => {
    expect(
      parsePath(
        "/processes/templates/repo%3Atemplate%3Aacme%3Awidgets%3Anightly",
      ),
    ).toEqual({
      view: "processes",
      templateId: "repo:template:acme:widgets:nightly",
    });
    expect(
      parsePath(
        "/processes/schedules/repo%3Aschedule%3Aacme%3Awidgets%3Anightly",
      ),
    ).toEqual({
      view: "processes",
      scheduleId: "repo:schedule:acme:widgets:nightly",
    });
    expect(
      parsePath("/processes/suites/repo%3Asuite%3Aacme%3Awidgets%3Asmoke"),
    ).toEqual({
      view: "processes",
      suiteId: "repo:suite:acme:widgets:smoke",
    });
    expect(
      parsePath("/processes/templates/repo:template:acme:widgets:nightly"),
    ).toEqual({
      view: "processes",
      templateId: "repo:template:acme:widgets:nightly",
    });
    // Not valid percent-encoding: kept as written rather than thrown on.
    expect(parsePath("/processes/schedules/sched-%E0")).toEqual({
      view: "processes",
      scheduleId: "sched-%E0",
    });
  });

  // One run of a suite, two segments under the suites segment
  // (grain/task-322). A suite's own id is always "suite-N", so the
  // "runs" segment can never be mistaken for one.
  it("parses an open suite run", () => {
    expect(parsePath("/processes/suites/runs/7")).toEqual({
      view: "processes",
      suiteRunId: "7",
    });
    expect(parsePath("/processes/suites/runs")).toEqual({
      view: "processes",
      processQuery: kindNarrowing(["suite"]),
    });
  });

  // The pages Processes replaced (docs/design/settings-panel.md §5.4):
  // each old path parses to where it is now, by a prefix swap.
  it("redirects /schedules, /templates and /suites into Processes", () => {
    const cases = [
      ["/schedules", "/processes?kind=schedule"],
      ["/schedules/sched-1", "/processes/schedules/sched-1"],
      ["/templates", "/processes?kind=template"],
      ["/templates/template-1", "/processes/templates/template-1"],
      ["/suites", "/processes?kind=suite"],
      ["/suites/suite-1", "/processes/suites/suite-1"],
      ["/suites/runs/7", "/processes/suites/runs/7"],
      ["/suites/runs", "/processes?kind=suite"],
    ];
    for (const [old, now] of cases) {
      expect({ old, now: buildPath(parsePath(old)) }).toEqual({ old, now });
    }
    // A declared id keeps its own spelling through the redirect.
    expect(
      parsePath("/templates/repo%3Atemplate%3Aacme%3Awidgets%3Anightly"),
    ).toEqual({
      view: "processes",
      templateId: "repo:template:acme:widgets:nightly",
    });
  });

  // A link written before the toolbar had conditions still opens the
  // view it named, and is rewritten in the narrowing's own encoding
  // (processFilters.js): the kinds as the kind condition, the old sort
  // words as sort keys.
  it("reads the Processes list's old kind, search and sort", () => {
    const parsed = parsePath(
      "/processes",
      "?kind=suite,schedule&q=nightly&sort=name",
    );
    expect(parsed).toEqual({
      view: "processes",
      processQuery: {
        search: "nightly",
        sort: [{ attr: "name", dir: "asc" }],
        filters: [{ attr: "kind", op: "any", values: ["schedule", "suite"] }],
      },
    });
    expect(buildPath(parsed)).toBe(
      "/processes?kind=schedule,suite&q=nightly&sort=name:asc",
    );
    // A kind this build has no chip for is dropped; every kind is All.
    expect(parsePath("/processes", "?kind=bogus")).toEqual({
      view: "processes",
    });
    expect(
      buildPath({
        view: "processes",
        processQuery: kindNarrowing([
          "schedule",
          "workflow",
          "template",
          "suite",
          "trigger",
        ]),
      }),
    ).toBe("/processes");
    // The default sort is not written out; the custom order is.
    expect(
      buildPath({ view: "processes", processQuery: { sort: "next" } }),
    ).toBe("/processes");
    expect(
      buildPath({
        view: "processes",
        processQuery: { search: "", sort: [], filters: [] },
      }),
    ).toBe("/processes?sort=custom");
  });

  it("round-trips a narrowed Processes list", () => {
    const search =
      "?q=nightly&sort=next:asc,name:desc&f=source:any:bwsalmon/grain" +
      "&f=type:any:template&f=name:has:candidate&f=enabled:any:no";
    const parsed = parsePath("/processes", search);
    expect(parsed.processQuery).toEqual({
      search: "nightly",
      sort: [
        { attr: "next", dir: "asc" },
        { attr: "name", dir: "desc" },
      ],
      filters: [
        { attr: "source", op: "any", values: ["bwsalmon/grain"] },
        { attr: "type", op: "any", values: ["template"] },
        { attr: "name", op: "has", values: ["candidate"] },
        { attr: "enabled", op: "any", values: ["no"] },
      ],
    });
    expect(buildPath(parsed)).toBe(`/processes${search}`);
  });

  it("opens a trigger's repository on its Advanced tab", () => {
    expect(
      parsePath("/processes/triggers/acme/widgets/.grain/triggers/docs.yml"),
    ).toEqual({ view: "repos", repo: "acme/widgets", repoTab: "advanced" });
    expect(
      processPath("trigger", "acme/widgets/.grain/triggers/docs.yml"),
    ).toBe("/processes/triggers/acme/widgets/.grain/triggers/docs.yml");

    const depot = "depot.code.forge.test/team/widgets";
    expect(
      parsePath(`/processes/triggers/${depot}/.grain/triggers/docs.yml`),
    ).toEqual({ view: "repos", repo: depot, repoTab: "advanced" });
    expect(processPath("trigger", `${depot}/.grain/triggers/docs.yml`)).toBe(
      `/processes/triggers/${depot}/.grain/triggers/docs.yml`,
    );
  });

  it("builds each process kind's own link with processPath", () => {
    expect(processPath("schedule", "sched-1")).toBe(
      "/processes/schedules/sched-1",
    );
    expect(processPath("workflow", "sched-2")).toBe(
      "/processes/schedules/sched-2",
    );
    expect(processPath("template", "repo:template:a:b:c")).toBe(
      "/processes/templates/repo%3Atemplate%3Aa%3Ab%3Ac",
    );
    expect(processPath("suite", "suite-1")).toBe("/processes/suites/suite-1");
    expect(processPath("run", 7)).toBe("/processes/suites/runs/7");
  });

  // grain's own CI, list-then-item like the four above it
  // (grain/task-354). A run id is the daemon's own hex string, and a
  // link to one goes stale when that run is forgotten -- which the pane
  // answers rather than this file.
  it("parses the CI page, and one run of it", () => {
    expect(parsePath("/ci")).toEqual({ view: "ci" });
    expect(parsePath("/ci/9f2c1a3b8e4d5f60")).toEqual({
      view: "ci",
      ciRunId: "9f2c1a3b8e4d5f60",
    });
  });

  it("parses the settings and system paths", () => {
    expect(parsePath("/settings")).toEqual({
      view: "settings",
      settingsSection: "",
    });
    expect(parsePath("/settings/engine")).toEqual({
      view: "settings",
      settingsSection: "engine",
    });
    expect(parsePath("/settings/ENGINE")).toEqual({
      view: "settings",
      settingsSection: "engine",
    });
    expect(parsePath("/settings/nonsense")).toEqual({
      view: "settings",
      settingsSection: "",
    });
    // Capabilities is its own section (capability servers and policy);
    // the aliases land on the pane holding what they name rather than on
    // General, which the bare /settings opens.
    expect(parsePath("/settings/capabilities")).toEqual({
      view: "settings",
      settingsSection: "capabilities",
    });
    expect(parsePath("/settings/capability-servers")).toEqual({
      view: "settings",
      settingsSection: "capabilities",
    });
    // One capability server's page is a link: its capability_id, which
    // is a URL, rides in the path as one encoded segment.
    const cap = "https://github.com/my-org/capabilities/cloud";
    const capPath = buildPath({
      view: "settings",
      settingsSection: "capabilities",
      settingsItem: cap,
    });
    expect(capPath).toBe(`/settings/capabilities/${encodeURIComponent(cap)}`);
    expect(parsePath(capPath)).toEqual({
      view: "settings",
      settingsSection: "capabilities",
      settingsItem: cap,
    });
    // Another section takes no item.
    expect(parsePath("/settings/git/whatever")).toEqual({
      view: "settings",
      settingsSection: "git",
    });
    expect(parsePath("/settings/legacy-capabilities")).toEqual({
      view: "settings",
      settingsSection: "credentials",
    });
    expect(
      buildPath({ view: "settings", settingsSection: "credentials" }),
    ).toBe("/settings/credentials");
    expect(parsePath("/system")).toEqual({ view: "system", systemTab: "" });
    // Kept rather than folded into "": on a phone the bare /system is
    // the list of tabs (App folds it back on a wider screen).
    expect(parsePath("/system/health")).toEqual({
      view: "system",
      systemTab: "health",
    });
    expect(parsePath("/system/logs")).toEqual({
      view: "system",
      systemTab: "logs",
    });
  });

  // docs/design/settings-panel.md §3.5: every old path parses to where
  // it went, and buildPath writes the new path for it.
  it.each([
    ["/admin", "/settings/general"],
    ["/admin/general", "/settings/general"],
    ["/admin/agents", "/settings/agents"],
    ["/admin/models", "/settings/models"],
    ["/admin/git", "/settings/git"],
    ["/admin/workflow", "/settings/tasks"],
    ["/admin/access", "/settings/access"],
    ["/admin/accounts", "/settings/access"],
    ["/admin/engine", "/settings/engine"],
    ["/admin/sandbox", "/settings/sandboxes"],
    ["/admin/executors", "/settings/models"],
    ["/admin/integrations", "/settings/credentials"],
    ["/admin/state", "/settings/config-repo"],
    ["/admin/upgrade", "/settings/upgrade"],
    ["/admin/logs", "/system/logs"],
    ["/admin/sandboxes", "/system/sandboxes"],
    ["/admin/caches", "/system/caches"],
    ["/admin/top", "/system/top"],
    ["/admin/pipeline", "/system/pipeline"],
    ["/admin/power", "/system/power"],
    ["/admin/Logs", "/system/logs"],
    ["/admin/nonsense", "/settings/general"],
    ["/system/general", "/settings/general"],
    ["/system/agents", "/settings/agents"],
    ["/system/models", "/settings/models"],
    ["/system/git", "/settings/git"],
    ["/system/workflow", "/settings/tasks"],
    ["/system/access", "/settings/access"],
    ["/system/accounts", "/settings/access"],
    ["/system/engine", "/settings/engine"],
    ["/system/sandbox", "/settings/sandboxes"],
    ["/system/executors", "/settings/models"],
    ["/system/integrations", "/settings/credentials"],
    ["/system/state", "/settings/config-repo"],
    ["/system/Agents", "/settings/agents"],
    // Upgrade was a System tab until it moved into Settings: a bookmark
    // or a notification's link to the old tab lands on the section.
    ["/system/upgrade", "/settings/upgrade"],
    ["/system/Upgrade", "/settings/upgrade"],
    ["/system/nonsense", "/system"],
    ["/memory", "/settings/memory"],
    ["/metrics", "/system/metrics"],
    ["/sandboxes", "/system/sandboxes"],
    ["/caches", "/system/caches"],
    ["/cache", "/system/caches"],
    ["/actions-cache", "/system/caches"],
  ])("redirects %s to %s", (old, now) => {
    expect(buildPath(parsePath(old))).toBe(now);
  });

  // The Config Repo Browser (docs/design/config-repo-settings.md §2.3)
  // is Settings → Config repo, with the open file as its item.
  it("parses /config?path= to the Config Repo Browser with that file open", () => {
    expect(parsePath("/config", "?path=settings%2Fcapabilities.yml")).toEqual({
      view: "settings",
      settingsSection: "config-repo",
      settingsItem: "settings/capabilities.yml",
    });
    expect(parsePath("/config")).toEqual({
      view: "settings",
      settingsSection: "config-repo",
    });
    const built = buildPath({
      view: "settings",
      settingsSection: "config-repo",
      settingsItem: "tables/repo_config.json",
    });
    expect(built).toBe("/settings/config-repo/tables/repo_config.json");
    expect(parsePath(built)).toEqual({
      view: "settings",
      settingsSection: "config-repo",
      settingsItem: "tables/repo_config.json",
    });
  });

  // The bare /settings is Settings' own entry, not a redirect: on a
  // phone it is the section list, and elsewhere SettingsPage opens
  // General from it (settings-panel-walkthrough.spec.js checks both).
  it("keeps the bare /settings as Settings' own entry", () => {
    expect(parsePath("/settings")).toEqual({
      view: "settings",
      settingsSection: "",
    });
    expect(buildPath(parsePath("/settings"))).toBe("/settings");
  });

  it("keeps a /metrics link's scope and window on the way to System", () => {
    expect(buildPath(parsePath("/metrics", "?window=30d"))).toBe(
      "/system/metrics?window=30d",
    );
    expect(
      buildPath(parsePath("/metrics", "?f=repo:any:acme/widgets&window=7d")),
    ).toBe("/system/metrics?f=repo:any:acme/widgets&window=7d");
  });

  it("falls back to tasks for an unknown or retired path", () => {
    expect(parsePath("/nope")).toEqual({ view: "tasks" });
    expect(parsePath("/consoles")).toEqual({ view: "tasks" });
    expect(parsePath("/consoles/42")).toEqual({ view: "tasks" });
  });

  it("tolerates a trailing slash", () => {
    expect(parsePath("/repos/")).toEqual({ view: "repos" });
  });

  // grain/task-317: how a task view is narrowed rides in the query, so
  // "the failed gcp-key tasks on acme/widgets" is a link rather than a
  // set of gestures somebody repeats.
  it("writes the metrics pane's window after its scope", () => {
    expect(
      buildPath({ view: "system", systemTab: "metrics", metricsWindow: "30d" }),
    ).toBe("/system/metrics?window=30d");
    expect(
      buildPath({
        view: "system",
        systemTab: "metrics",
        metricsNarrowing: {
          search: "",
          sort: [],
          filters: [{ attr: "repo", op: "any", values: ["acme/widgets"] }],
        },
        metricsWindow: "24h",
      }),
    ).toBe("/system/metrics?f=repo:any:acme/widgets&window=24h");
    // Nobody has picked one: the bare path.
    expect(
      buildPath({ view: "system", systemTab: "metrics", metricsWindow: null }),
    ).toBe("/system/metrics");
  });

  it("round-trips the metrics pane's window with and without a scope", () => {
    for (const [path, search] of [
      ["/system/metrics", "?window=30d"],
      ["/system/metrics", "?f=repo:any:acme/widgets&window=7d"],
      ["/system/metrics", "?q=ci&f=author:any:ada&window=90d"],
      ["/system/metrics", "?f=repo:any:acme/widgets"],
    ]) {
      expect(buildPath(parsePath(path, search))).toBe(path + search);
    }
  });

  // grain/task-2393: a report board's picker rides in the board's own
  // URL the way the Metrics pane's does, after the board's narrowing.
  it("round-trips a report board's window with and without a narrowing", () => {
    for (const [path, search] of [
      ["/boards/board-9", "?window=30d"],
      ["/boards/board-9", "?f=repo:any:acme/widgets&window=30d"],
      ["/boards/board-9", "?f=repo:any:acme/widgets"],
    ]) {
      expect(buildPath(parsePath(path, search))).toBe(path + search);
    }
    // Nobody has picked one: the board's bare path.
    expect(
      buildPath({ view: "boards", boardId: "board-9", boardWindow: null }),
    ).toBe("/boards/board-9");
    // A window the picker does not offer is dropped, and the boards
    // list is not a board.
    expect(
      parsePath("/boards/board-9", "?window=fortnight").boardWindow,
    ).toBeUndefined();
    expect(parsePath("/boards", "?window=30d").boardWindow).toBeUndefined();
  });

  describe("a narrowed task view", () => {
    it("reads the search, the sort keys and every condition out of the query", () => {
      expect(
        parsePath(
          "/",
          "?q=ci&sort=state:asc,created:desc&f=repo:any:acme/widgets|acme/gadgets&f=capability:all:gcp-key",
        ),
      ).toEqual({
        view: "tasks",
        narrowing: {
          search: "ci",
          sort: [
            { attr: "state", dir: "asc" },
            { attr: "created", dir: "desc" },
          ],
          filters: [
            {
              attr: "repo",
              op: "any",
              values: ["acme/widgets", "acme/gadgets"],
            },
            { attr: "capability", op: "all", values: ["gcp-key"] },
          ],
        },
      });
    });

    it("reads a condition whose value is still to be filled in", () => {
      expect(parsePath("/", "?f=repo:any:").narrowing.filters).toEqual([
        { attr: "repo", op: "any", values: [] },
      ]);
    });

    it("ignores a condition that is not one", () => {
      // No comparison, an empty one, and nothing before the first colon:
      // none of the three is a question, so none of them narrows anything.
      expect(parsePath("/", "?f=repo&f=repo::x&f=:any:x")).toEqual({
        view: "tasks",
      });
    });

    it("keeps a comparison this build has never heard of", () => {
      // Kept, so the address survives and the view can say it is not
      // applying it (taskFilters.js's unknownConditions) -- parsing it
      // away left `/?f=state:bogus:running` showing every task, and
      // rewritten to `/`, without a word (grain/task-1531).
      expect(parsePath("/", "?f=repo:sideways:x").narrowing.filters).toEqual([
        { attr: "repo", op: "sideways", values: ["x"] },
      ]);
    });

    // The one-Select toolbar wrote a parameter per attribute and a sort
    // by name (grain/task-317). Links people kept still open what they
    // name.
    it("reads the narrowing an older build wrote", () => {
      expect(
        parsePath(
          "/",
          "?q=ci&sort=newest&repo=acme/widgets&capability=gcp-key",
        ),
      ).toEqual({
        view: "tasks",
        narrowing: {
          search: "ci",
          sort: [{ attr: "created", dir: "desc" }],
          filters: [
            { attr: "repo", op: "any", values: ["acme/widgets"] },
            { attr: "capability", op: "any", values: ["gcp-key"] },
          ],
        },
      });
      expect(parsePath("/", "?sort=manual")).toEqual({ view: "tasks" });
    });

    it("reports no narrowing at all for a query that asks for none", () => {
      expect(parsePath("/", "")).toEqual({ view: "tasks" });
      expect(parsePath("/", "?nothing=here")).toEqual({ view: "tasks" });
    });

    it("narrows a repo's own page the same way", () => {
      expect(parsePath("/repos/acme/widgets", "?f=author:any:grain")).toEqual({
        view: "repos",
        repo: "acme/widgets",
        narrowing: {
          search: "",
          sort: [],
          filters: [{ attr: "author", op: "any", values: ["grain"] }],
        },
      });
    });

    // A query on a path that isn't showing a task view means nothing --
    // the same treatment /settings already gives the view it was opened
    // over, which it doesn't carry either.
    // The Metrics pane is the one path whose query means something
    // without a task view under it (grain/task-379): it is the scope the
    // report is measured over, and it is reported as a field of its own
    // so that opening the pane over a narrowed list cannot hand the
    // deployment-wide report that list's question -- nor a link to a
    // scoped report narrow the list waiting behind the pane.
    it("reads the metrics pane's query as its own scope", () => {
      expect(parsePath("/metrics", "?f=repo:any:acme/widgets")).toEqual({
        view: "system",
        systemTab: "metrics",
        metricsNarrowing: {
          search: "",
          sort: [],
          filters: [{ attr: "repo", op: "any", values: ["acme/widgets"] }],
        },
      });
      // Never as the narrowing the task views share.
      expect(parsePath("/metrics", "?q=ci").narrowing).toBeUndefined();
      // And a pane nobody has scoped carries no scope at all, which is
      // the whole deployment's report.
      expect(parsePath("/metrics")).toEqual({
        view: "system",
        systemTab: "metrics",
      });
    });

    // The window picker rides beside the scope (grain/task-1584), so a
    // reload or a pasted link reopens the report over the span it was
    // copied at -- and a window this build does not offer is dropped
    // rather than handed to the picker as a value it has no option for.
    it("reads the metrics pane's window beside its scope", () => {
      expect(parsePath("/metrics", "?window=30d")).toEqual({
        view: "system",
        systemTab: "metrics",
        metricsWindow: "30d",
      });
      expect(
        parsePath("/metrics", "?f=repo:any:acme/widgets&window=90d"),
      ).toEqual({
        view: "system",
        systemTab: "metrics",
        metricsNarrowing: {
          search: "",
          sort: [],
          filters: [{ attr: "repo", op: "any", values: ["acme/widgets"] }],
        },
        metricsWindow: "90d",
      });
      expect(parsePath("/metrics", "?window=fortnight")).toEqual({
        view: "system",
        systemTab: "metrics",
      });
      // A window is the pane's own word, not a list's.
      expect(parsePath("/", "?window=30d").metricsWindow).toBeUndefined();
    });

    it("ignores a query on a path with no task view on it", () => {
      expect(parsePath("/settings", "?q=ci")).toEqual({
        view: "settings",
        settingsSection: "",
      });
      expect(parsePath("/tasks/42", "?q=ci")).toEqual({
        view: "tasks",
        taskId: "42",
      });
      expect(parsePath("/processes", "?repo=acme/widgets")).toEqual({
        view: "processes",
      });
      expect(parsePath("/repos", "?repo=acme/widgets")).toEqual({
        view: "repos",
      });
      expect(parsePath("/repos/acme/widgets/releases", "?q=ci")).toEqual({
        view: "repos",
        repo: "acme/widgets",
        showReleases: true,
      });
    });

    // Nothing here can tell whether "acme/gone" is still a repo any task
    // carries, or whether "custom.team" is an attribute this deployment
    // has -- both are questions about tasks and config, which paths.js
    // has never seen. A stale link parses as written and the view
    // resolves it (taskFilters.js's resolveConditions drops what it does
    // not have).
    it("takes an attribute and a value on trust, however stale", () => {
      expect(parsePath("/", "?f=repo:any:acme/gone")).toEqual({
        view: "tasks",
        narrowing: {
          search: "",
          sort: [],
          filters: [{ attr: "repo", op: "any", values: ["acme/gone"] }],
        },
      });
      expect(
        parsePath("/", "?f=custom.team:any:infra").narrowing.filters,
      ).toEqual([{ attr: "custom.team", op: "any", values: ["infra"] }]);
      expect(parsePath("/", "?sort=custom.team:desc").narrowing.sort).toEqual([
        { attr: "custom.team", dir: "desc" },
      ]);
    });

    it("reads 'has none of these' as the answer it is", () => {
      expect(parsePath("/", "?f=repo:any:__none__").narrowing.filters).toEqual([
        { attr: "repo", op: "any", values: ["__none__"] },
      ]);
    });

    // A value carrying one of the separators is escaped on the way out
    // (%3A, itself written as %253A once the query string has been
    // escaped in turn) and read back as itself, so a filter on a title
    // with a colon in it is not two thirds of a condition.
    it("reads back a value that looks like a separator", () => {
      expect(
        parsePath("/", "?f=title:has:ci%253A%20green%257Cred").narrowing
          .filters,
      ).toEqual([{ attr: "title", op: "has", values: ["ci: green|red"] }]);
    });
  });
});

describe("buildPath", () => {
  it("builds / for the tasks view", () => {
    expect(buildPath({ view: "tasks" })).toBe("/");
  });

  it("builds a plain path for the other sidebar views", () => {
    expect(buildPath({ view: "repos" })).toBe("/repos");
    expect(buildPath({ view: "boards" })).toBe("/boards");
    expect(buildPath({ view: "inbox" })).toBe("/inbox");
  });

  it("writes the rail's state view ahead of the narrowing", () => {
    const narrowing = {
      search: "",
      sort: [],
      filters: [{ attr: "repo", op: "none", values: ["acme/widgets"] }],
    };
    expect(buildPath({ view: "tasks", stateFilter: "queued" })).toBe(
      "/?state=queued",
    );
    expect(buildPath({ view: "tasks", stateFilter: "all", narrowing })).toBe(
      "/?f=repo:none:acme/widgets",
    );
    const path = buildPath({ view: "tasks", stateFilter: "closed", narrowing });
    expect(path).toBe("/?state=closed&f=repo:none:acme/widgets");
    const [pathname, search] = path.split("?");
    expect(parsePath(pathname, `?${search}`)).toEqual({
      view: "tasks",
      stateFilter: "closed",
      narrowing,
    });
    // Not on an open task, nor on another view.
    expect(
      buildPath({ view: "tasks", taskId: "7", stateFilter: "closed" }),
    ).toBe("/tasks/7");
    expect(buildPath({ view: "inbox", stateFilter: "closed" })).toBe("/inbox");
  });

  it("leaves a narrowing off the inbox's own path", () => {
    expect(buildPath({ view: "inbox", narrowing: { search: "ci" } })).toBe(
      "/inbox",
    );
  });

  it("builds a standing session's stable path", () => {
    expect(buildPath({ view: "session" })).toBe("/session");
    expect(buildPath({ view: "session", sessionId: "director" })).toBe(
      "/session/director",
    );
  });

  it("builds a repo's own path, and its releases pane's", () => {
    expect(buildPath({ view: "repos", repo: "acme/widgets" })).toBe(
      "/repos/acme/widgets",
    );
    expect(
      buildPath({ view: "repos", repo: "acme/widgets", showReleases: true }),
    ).toBe("/repos/acme/widgets/releases");
    expect(
      buildPath({
        view: "repos",
        repo: "acme/widgets",
        showReleases: true,
        release: "r2-1004",
      }),
    ).toBe("/repos/acme/widgets/releases/r2-1004");
    expect(
      parsePath(
        buildPath({
          view: "repos",
          repo: "acme/widgets",
          showReleases: true,
          release: "odd name/x",
        }),
      ).release,
    ).toBe("odd name/x");
  });

  it("ignores an open repo outside the repos view", () => {
    expect(buildPath({ view: "tasks", repo: "acme/widgets" })).toBe("/");
  });

  it("builds an open schedule's, template's and suite's own path", () => {
    expect(buildPath({ view: "processes", scheduleId: "sched-1" })).toBe(
      "/processes/schedules/sched-1",
    );
    expect(buildPath({ view: "processes", templateId: "template-1" })).toBe(
      "/processes/templates/template-1",
    );
    expect(buildPath({ view: "processes", suiteId: "suite-1" })).toBe(
      "/processes/suites/suite-1",
    );
  });

  it("builds an open suite run's own path, ahead of an open suite", () => {
    expect(buildPath({ view: "processes", suiteRunId: "7" })).toBe(
      "/processes/suites/runs/7",
    );
    expect(
      buildPath({ view: "processes", suiteId: "suite-1", suiteRunId: "7" }),
    ).toBe("/processes/suites/runs/7");
  });

  it("builds the CI page's path, and one run's", () => {
    expect(buildPath({ view: "ci" })).toBe("/ci");
    expect(buildPath({ view: "ci", ciRunId: "9f2c1a" })).toBe("/ci/9f2c1a");
    // ...and only within its own view, like every other open item here.
    expect(buildPath({ view: "tasks", ciRunId: "9f2c1a" })).toBe("/");
  });

  it("ignores an open item outside its own view", () => {
    expect(buildPath({ view: "repos", scheduleId: "sched-1" })).toBe("/repos");
  });

  it("prefers an open task over the underlying view", () => {
    expect(buildPath({ view: "repos", taskId: "42" })).toBe("/tasks/42");
  });

  // Settings and System are views: a task opened over one is the
  // address, and the view is what is behind it.
  it("prefers an open task over the settings and system views", () => {
    expect(
      buildPath({ view: "settings", settingsSection: "engine", taskId: "42" }),
    ).toBe("/tasks/42");
    expect(
      buildPath({ view: "system", systemTab: "metrics", taskId: "42" }),
    ).toBe("/tasks/42");
  });

  // The open section and tab are part of the path, so a reload or a
  // copied link reopens it (grain/task-1359).
  it("keeps the open settings section and system tab in the path", () => {
    expect(buildPath({ view: "settings", settingsSection: "ci" })).toBe(
      "/settings/ci",
    );
    expect(buildPath({ view: "settings", settingsSection: "" })).toBe(
      "/settings",
    );
    expect(buildPath({ view: "system", systemTab: "logs" })).toBe(
      "/system/logs",
    );
    expect(buildPath({ view: "system", systemTab: "" })).toBe("/system");
  });

  // The metrics pane's own scope rides on its own path, and the
  // narrowing beside it -- the question the list behind the pane is
  // asking -- is left behind the way it is under every other overlay.
  it("writes the metrics pane's scope onto its path", () => {
    expect(
      buildPath({
        view: "system",
        systemTab: "metrics",
        metricsNarrowing: {
          search: "",
          sort: [],
          filters: [{ attr: "repo", op: "any", values: ["acme/widgets"] }],
        },
        narrowing: { search: "flake", sort: [], filters: [] },
      }),
    ).toBe("/system/metrics?f=repo:any:acme/widgets");
  });

  describe("a narrowed task view", () => {
    const narrowing = {
      search: "ci",
      sort: [{ attr: "title", dir: "asc" }],
      filters: [
        { attr: "repo", op: "any", values: ["acme/widgets", "acme/gadgets"] },
        { attr: "capability", op: "all", values: ["gcp-key"] },
      ],
    };

    it("writes the narrowing into the query: the search, then the sort, then the conditions in their own order", () => {
      expect(buildPath({ view: "tasks", narrowing })).toBe(
        "/?q=ci&sort=title:asc&f=repo:any:acme/widgets|acme/gadgets&f=capability:all:gcp-key",
      );
    });

    it("escapes a value that would otherwise read as a separator", () => {
      const path = buildPath({
        view: "tasks",
        narrowing: {
          search: "",
          sort: [],
          filters: [{ attr: "title", op: "has", values: ["ci: green|red"] }],
        },
      });
      expect(path).toBe("/?f=title:has:ci%253A+green%257Cred");
      expect(parsePath("/", path.slice(1)).narrowing.filters).toEqual([
        { attr: "title", op: "has", values: ["ci: green|red"] },
      ]);
    });

    it("leaves a list nobody has narrowed at its own path", () => {
      expect(buildPath({ view: "tasks", narrowing: null })).toBe("/");
      expect(parsePath("/", "").narrowing).toBeUndefined();
    });

    it("writes a cleared narrowing so a reload keeps it cleared", () => {
      const cleared = { search: "", sort: [], filters: [] };
      expect(buildPath({ view: "tasks", narrowing: cleared })).toBe("/?f=none");
      expect(parsePath("/", "?f=none").narrowing).toEqual(cleared);
      expect(
        buildPath({ view: "boards", boardId: "board-3", narrowing: cleared }),
      ).toBe("/boards/board-3?f=none");
      expect(parsePath("/boards/board-3", "?f=none").narrowing).toEqual(
        cleared,
      );
      expect(
        buildPath({
          view: "boards",
          boardId: "board-3",
          narrowing: cleared,
          boardWindow: "30d",
        }),
      ).toBe("/boards/board-3?f=none&window=30d");
      expect(
        buildPath({ view: "repos", repo: "acme/widgets", narrowing: cleared }),
      ).toBe("/repos/acme/widgets?f=none");
      expect(
        buildPath({ view: "tasks", stateFilter: "closed", narrowing: cleared }),
      ).toBe("/?state=closed&f=none");
      const parsed = parsePath("/", "?state=closed&f=none");
      expect(parsed.stateFilter).toBe("closed");
      expect(parsed.narrowing).toEqual(cleared);
    });

    it("reads the empty marker only when nothing else is asked", () => {
      expect(parsePath("/", "?f=none&q=ci").narrowing).toEqual({
        search: "ci",
        sort: [],
        filters: [],
      });
      expect(parsePath("/", "?f=bogus").narrowing).toBeUndefined();
    });

    it("leaves the metrics pane and a stored board query unmarked", () => {
      const cleared = { search: "", sort: [], filters: [] };
      expect(
        buildPath({
          view: "system",
          systemTab: "metrics",
          metricsNarrowing: cleared,
        }),
      ).toBe("/system/metrics");
      expect(queryFromNarrowing(cleared)).toBe("");
      expect(narrowingFromQuery("")).toBeNull();
      expect(narrowingFromQuery("f=none")).toEqual(cleared);
    });

    it("narrows a saved board and a repo's own page too", () => {
      expect(
        buildPath({
          view: "boards",
          boardId: "board-3",
          narrowing: { search: "ci" },
        }),
      ).toBe("/boards/board-3?q=ci");
      expect(
        buildPath({
          view: "repos",
          repo: "acme/widgets",
          narrowing: { search: "ci" },
        }),
      ).toBe("/repos/acme/widgets?q=ci");
    });

    // The narrowing is dropped for as long as something covers the list
    // -- App goes on holding it, and closing the pane puts it back in
    // the address bar, the same way the view behind /settings does.
    it("drops the narrowing from a path with no task view on it", () => {
      expect(
        buildPath({ view: "settings", narrowing, settingsSection: "ci" }),
      ).toBe("/settings/ci");
      expect(buildPath({ view: "tasks", narrowing, taskId: "42" })).toBe(
        "/tasks/42",
      );
      expect(buildPath({ view: "processes", narrowing })).toBe("/processes");
      expect(buildPath({ view: "repos", narrowing })).toBe("/repos");
      expect(
        buildPath({
          view: "repos",
          repo: "acme/widgets",
          showReleases: true,
          narrowing,
        }),
      ).toBe("/repos/acme/widgets/releases");
    });

    it("round-trips every narrowed path parsePath recognizes", () => {
      const paths = [
        "/?q=ci",
        "/?sort=title:asc",
        "/?sort=state:asc,created:desc",
        "/?f=repo:any:acme/widgets&f=capability:any:gcp-key",
        "/?q=ci&sort=created:asc&f=repo:any:__none__|acme/widgets",
        "/?f=created:within:7&f=title:has:flake&f=base:empty:",
        "/boards/board-3?q=ci&f=repo:none:acme/widgets",
        "/repos/acme/widgets?q=ci&sort=created:desc",
      ];
      for (const path of paths) {
        const [pathname, search] = path.split("?");
        expect(buildPath(parsePath(pathname, `?${search}`))).toBe(path);
      }
    });
  });

  it("round-trips every path parsePath recognizes", () => {
    const paths = [
      "/",
      "/boards",
      "/repos",
      "/processes",
      "/tasks/42",
      "/session",
      "/session/director",
      "/settings",
      "/settings/general",
      "/settings/models",
      "/settings/memory",
      "/settings/config-repo",
      "/settings/capabilities",
      "/settings/credentials",
      "/settings/preferences",
      "/system",
      "/settings/upgrade",
      "/system/logs",
      "/system/metrics",
      "/repos/acme/widgets",
      "/repos/acme/widgets/releases",
      "/processes/schedules/sched-1",
      "/processes/templates/template-1",
      "/processes/suites/suite-1",
      "/processes/suites/runs/7",
      "/new",
    ];
    for (const path of paths) {
      expect(buildPath(parsePath(path))).toBe(path);
    }
  });

  // The new-task form's own path (grain/task-53), which carries the
  // custom field values the form opens prefilled with -- the link a
  // task's own pane offers, and so something somebody can copy, keep
  // and send.
  describe("the new-task path", () => {
    it("reads the fields a link carries", () => {
      expect(
        parsePath("/new", "?field.priority=high&field.customer=acme"),
      ).toEqual({
        view: "tasks",
        showNewTask: true,
        newTaskFields: { priority: "high", customer: "acme" },
      });
    });

    it("ignores a query parameter that is not a field", () => {
      expect(parsePath("/new", "?q=ci")).toEqual({
        view: "tasks",
        showNewTask: true,
      });
    });

    it("writes the fields back in a stable order", () => {
      expect(
        buildPath({
          view: "tasks",
          showNewTask: true,
          newTaskFields: { priority: "high", customer: "acme" },
        }),
      ).toBe("/new?field.customer=acme&field.priority=high");
    });

    it("round-trips the values it writes", () => {
      const path = newTaskPath({ priority: "needs review", customer: "acme" });
      expect(parsePath(path.split("?")[0], `?${path.split("?")[1]}`)).toEqual({
        view: "tasks",
        showNewTask: true,
        newTaskFields: { priority: "needs review", customer: "acme" },
      });
    });

    // A field that holds a set (model.CustomField.Multiple) rides as the
    // parameter repeated, which is how a query string has always said
    // several -- and one value is still one value, so every link written
    // before there were sets reads exactly as it did.
    it("carries a field holding several as the parameter repeated", () => {
      const path = newTaskPath({
        areas: ["ui", "daemon"],
        customer: "acme",
      });
      expect(path).toBe(
        "/new?field.areas=ui&field.areas=daemon&field.customer=acme",
      );
      expect(parsePath("/new", `?${path.split("?")[1]}`)).toEqual({
        view: "tasks",
        showNewTask: true,
        newTaskFields: { areas: ["ui", "daemon"], customer: "acme" },
      });
    });
  });
});

// The single-tenant reset (stage 3e-ii) took the team out of the
// address, but a link an older build wrote under /t/<team>/ still opens
// the page it named, at the address that page has now.
describe("a link written under a team", () => {
  it("opens the page it named without the team", () => {
    expect(parsePath("/t/default/settings/models")).toEqual(
      parsePath("/settings/models"),
    );
    expect(buildPath(parsePath("/t/infra/tasks/42"))).toBe(
      buildPath(parsePath("/tasks/42")),
    );
    expect(buildPath(parsePath("/t/infra"))).toBe("/");
  });

  it("keeps a new-task link's prefilled fields", () => {
    const parsed = parsePath("/t/default/new", "?field.priority=high");
    expect(parsed).toEqual({
      view: "tasks",
      showNewTask: true,
      newTaskFields: { priority: "high" },
    });
    expect(buildPath(parsed)).toBe("/new?field.priority=high");
  });
});
