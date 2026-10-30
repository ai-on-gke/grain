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
  ASKED_STATES,
  DISPLAY_STATE_ORDER,
  askedCase,
  displayCounts,
  displayState,
  statesOf,
  advertisedModels,
  providerDefaultModel,
  teamDefaultLabel,
  connectionModels,
  outcomeLabel,
  RETIRED_CAPABILITY_HINT,
  activityTitle,
  awaitsHumanSubmit,
  blockedDescription,
  branchesInUse,
  capabilityName,
  capabilityRows,
  capabilityUnavailableHint,
  closablePullRequest,
  completionPhase,
  configuredCapabilities,
  defaultCapabilitiesFor,
  INBOX_STATES,
  inboxCount,
  inboxGateKey,
  inboxGates,
  inboxTasks,
  internalPullRequestUrl,
  isAgentRunning,
  isGrainActive,
  isTaskMerged,
  isTaskRepairing,
  knownRepos,
  lastBaseForRepo,
  mergeQueueActive,
  mergeQueueBatching,
  mergeQueueStall,
  mergeQueueStalled,
  orphanedPullRequest,
  pullRequestUrl,
  issueUrl,
  relativeAge,
  repoActivity,
  repoRows,
  repoTargetable,
  repoTasks,
  repoUrl,
  runActivity,
  stateLabel,
  stateTitle,
  isBlockedWaiting,
  taskBadgeModifier,
  taskBadgeState,
  unionCapabilities,
  waitsOnUser,
  targetableRepos,
  pickerRepos,
  repoReachable,
  connectionPause,
  connectionsServingModels,
  defaultModels,
  modelPause,
  modelPauses,
  taskModelOn,
  taskPause,
  reasonControl,
  reasonLabel,
  reasonTitle,
} from "./state.js";

describe("waitsOnUser", () => {
  it("admits the four states a task never leaves on its own", () => {
    for (const state of [
      "proposed",
      "awaiting_reply",
      "awaiting_submit",
      "failed",
    ]) {
      expect(waitsOnUser({ state })).toBe(true);
    }
  });

  // "deferred" among them: putting a proposal aside is already an
  // answer to it, and model.StateOf reads such a task as deferred rather
  // than as a proposal, so it stops being asked about.
  it("leaves out the states grain is still working through", () => {
    for (const state of [
      "queued",
      "running",
      "completed",
      "deferred",
      "closed",
    ]) {
      expect(waitsOnUser({ state })).toBe(false);
    }
  });

  // A standing session's task is parked on its own conversation between
  // exchanges, so it reads as awaiting a reply for as long as nobody is
  // typing into it -- and an inbox that admitted it would carry one row
  // per session that nothing ever clears (grain/task-415).
  it("leaves a standing session's own task out, however it is parked", () => {
    expect(
      waitsOnUser({ state: "awaiting_reply", consoleSession: "director" }),
    ).toBe(false);
    expect(
      inboxTasks([
        { id: "1", state: "awaiting_reply", consoleSession: "director" },
        { id: "2", state: "awaiting_reply" },
      ]).map((t) => t.id),
    ).toEqual(["2"]);
  });

  // Its badge still reads "Queued for merge" and it is on no queue --
  // the one case in the inbox that nothing else on screen says out loud.
  it("admits a completed task the merge queue has given up on", () => {
    expect(
      waitsOnUser({
        state: "completed",
        pullRequest: "acme/widgets#1",
        mergeQueueBlockedAt: "2026-08-01T00:00:00Z",
      }),
    ).toBe(true);
  });

  // grain/task-393: the stall detector's own verdict is a wait on a
  // person too. Nothing has escalated and no comment has been filed --
  // the queue still believes it is driving -- so unless the inbox says
  // it, a wait nothing is getting anywhere with is found only by
  // somebody scanning the list.
  it("admits a merge-queue wait grain has flagged as stalled", () => {
    expect(
      waitsOnUser({
        state: "completed",
        pullRequest: "acme/widgets#1",
        mergeQueueState: "waiting_ci",
        mergeQueueStalledAt: "2026-08-01T00:00:00Z",
      }),
    ).toBe(true);
  });

  // GitHub refused to merge it until somebody approves it, and nothing
  // in grain will: a wait on a person, though the queue marks it neither
  // blocked nor stalled.
  it("admits a merge-queue entry waiting on a GitHub approval", () => {
    expect(
      waitsOnUser({
        state: "completed",
        pullRequest: "acme/widgets#1",
        mergeQueueState: "waiting_github_approval",
      }),
    ).toBe(true);
    expect(
      waitsOnUser({
        state: "closed",
        pullRequest: "acme/widgets#1",
        mergeQueueState: "waiting_github_approval",
      }),
    ).toBe(false);
  });

  // The ordinary wait is not: the queue is driving it, it is inside
  // whatever patience liveness.go allows that state, and an inbox that
  // carried every change on the queue would carry nothing worth reading.
  it("leaves an ordinary merge-queue wait out", () => {
    expect(
      waitsOnUser({
        state: "completed",
        pullRequest: "acme/widgets#1",
        mergeQueueState: "waiting_ci",
      }),
    ).toBe(false);
  });

  // A blocked task starts on its own the moment what it depends on
  // lands, so there is nothing for a reader to do about it.
  it("leaves out a blocked task waiting on another task", () => {
    expect(waitsOnUser({ state: "queued", blocked: true })).toBe(false);
  });

  // The state is one grain drives and the badge says so, which is
  // exactly why nobody finds one of these by scrolling: the only thing
  // that distinguishes it from a healthy queued task is how long it has
  // said the same word (ui.Task.Stall).
  it("admits a task whose own state has stood longer than it should", () => {
    expect(
      waitsOnUser({
        state: "queued",
        stall: "this has stood here for more than 3d",
      }),
    ).toBe(true);
    expect(
      waitsOnUser({
        state: "running",
        stall: "this has stood here for more than 1d",
      }),
    ).toBe(true);
    // And a task with no note is an ordinary queued task, whatever else
    // is on it.
    expect(waitsOnUser({ state: "queued", stall: "" })).toBe(false);
  });
});

describe("inboxTasks", () => {
  // grain/task-296: "awaiting_submit" means grain has finished and is
  // waiting for a person, which is the same kind of fact as a run parked
  // on a question -- so both are counted here, together, wherever "needs
  // a person" is surfaced (the inbox page, the rail's count, the rail's
  // own state entries). A finished change that reads as a queue state is
  // a change nobody merges.
  it("counts a finished change and a parked question as the same kind of wait", () => {
    expect(INBOX_STATES).toContain("awaiting_submit");
    expect(INBOX_STATES).toContain("awaiting_reply");
    const waiting = inboxTasks([
      { id: "1", state: "awaiting_submit" },
      { id: "2", state: "awaiting_reply" },
      { id: "3", state: "running" },
      { id: "4", state: "queued" },
    ]);
    expect(waiting.map((t) => t.id)).toEqual(["1", "2"]);
    expect(waiting).toHaveLength(2);
  });

  it("keeps the order it was given", () => {
    const tasks = [
      { id: "1", state: "failed" },
      { id: "2", state: "running" },
      { id: "3", state: "proposed" },
    ];
    expect(inboxTasks(tasks).map((t) => t.id)).toEqual(["1", "3"]);
  });

  it("reads a missing list as an empty one", () => {
    expect(inboxTasks(undefined)).toEqual([]);
  });
});

// grain/task-470: the one thing the inbox holds that is not a task.
describe("inboxGates", () => {
  const gate = (over) => ({
    repo: "acme/widgets",
    branch: "main",
    judged: true,
    reason: "no-run-started",
    detail: "...",
    ...over,
  });

  // The health pane sorts by repository, so a redrawing pane does not
  // shuffle its own alerts; a reader working down a list of things to
  // deal with wants the worst first, which is the Go side's own
  // worseBaseBranchGate rule.
  it("puts a branch nothing judges above one that is merely late", () => {
    const rows = inboxGates([
      gate({ repo: "acme/other", lastRunAgeMS: 4 * 3600 * 1000 }),
      gate({ repo: "acme/widgets", judged: false, reason: "grain-ci-off" }),
      gate({ repo: "acme/third", lastRunAgeMS: 33 * 3600 * 1000 }),
    ]);
    expect(rows.map((g) => g.repo)).toEqual([
      "acme/widgets",
      "acme/third",
      "acme/other",
    ]);
  });

  // Every row the server sends is a finding -- a working gate produces
  // none -- so there is nothing to filter the way waitsOnUser filters
  // tasks. All three reasons end in the same sentence: the queue is
  // merging onto a commit nothing has judged.
  it("keeps every finding, whatever its reason", () => {
    const rows = inboxGates([
      gate({ reason: "run-unfinished", runInFlight: true }),
      gate({ repo: "acme/other", reason: "no-run-started" }),
      gate({ repo: "acme/third", judged: false, reason: "grain-ci-off" }),
    ]);
    expect(rows).toHaveLength(3);
  });

  it("does not disturb the list it was given", () => {
    const gates = [gate({ repo: "acme/z" }), gate({ repo: "acme/a" })];
    inboxGates(gates);
    expect(gates.map((g) => g.repo)).toEqual(["acme/z", "acme/a"]);
  });

  it("reads a missing reading as no findings at all", () => {
    expect(inboxGates(undefined)).toEqual([]);
  });

  // A branch grain has never resolved a name for is the one finding with
  // no branch on it, and two of those from one repository must still be
  // two rows rather than one.
  it("names a gate by its repository and branch", () => {
    expect(inboxGateKey(gate())).toBe("acme/widgets main");
    expect(inboxGateKey(gate({ branch: "" }))).toBe("acme/widgets ");
  });
});

describe("awaitsHumanSubmit", () => {
  const running = {
    id: "1",
    state: "running",
    autoMerge: false,
    pullRequest: "acme/widgets#12",
  };

  it("admits a task in awaiting_submit", () => {
    expect(awaitsHumanSubmit({ id: "1", state: "awaiting_submit" })).toBe(true);
  });

  // The run opened its pull request and is blocked in wait_for_checks:
  // still running, but nothing lands the change until somebody submits it.
  it("admits a running task whose open pull request is owed a Submit", () => {
    expect(awaitsHumanSubmit(running)).toBe(true);
  });

  it("leaves out a running task the merge queue will land by itself", () => {
    expect(awaitsHumanSubmit({ ...running, autoMerge: true })).toBe(false);
  });

  it("leaves out a running task with no pull request yet", () => {
    expect(awaitsHumanSubmit({ ...running, pullRequest: "" })).toBe(false);
    expect(awaitsHumanSubmit({ ...running, pullRequest: undefined })).toBe(
      false,
    );
  });

  it("leaves out a running task whose pull request merged or closed", () => {
    expect(
      awaitsHumanSubmit({ ...running, prMergedAt: "2026-10-25T00:00:00Z" }),
    ).toBe(false);
    expect(
      awaitsHumanSubmit({ ...running, prClosedAt: "2026-10-25T00:00:00Z" }),
    ).toBe(false);
  });

  it("leaves out every other state", () => {
    for (const state of ["queued", "completed", "failed", "closed"]) {
      expect(awaitsHumanSubmit({ ...running, state })).toBe(false);
    }
    expect(awaitsHumanSubmit(null)).toBe(false);
  });
});

describe("inboxCount", () => {
  it("counts a running task whose pull request is owed a Submit", () => {
    expect(
      inboxCount([
        {
          id: "1",
          state: "running",
          autoMerge: false,
          pullRequest: "acme/widgets#12",
        },
        {
          id: "2",
          state: "running",
          autoMerge: true,
          pullRequest: "acme/widgets#13",
        },
      ]),
    ).toBe(1);
  });

  it("counts only the waiting tasks, not the base branch gates", () => {
    expect(
      inboxCount(
        [
          { id: "1", state: "awaiting_reply" },
          { id: "2", state: "running" },
        ],
        [{ repo: "acme/widgets", branch: "main", judged: false }],
      ),
    ).toBe(1);
  });

  it("is the task count alone when no reading has arrived", () => {
    expect(inboxCount([{ id: "1", state: "proposed" }], undefined)).toBe(1);
  });
});

describe("pullRequestUrl", () => {
  it("turns a pull request ref into the GitHub URL it names", () => {
    expect(pullRequestUrl("acme/widgets#42")).toBe(
      "https://github.com/acme/widgets/pull/42",
    );
    expect(pullRequestUrl("grain/config#42")).toBe(
      "/repos/grain/config/compare/main...grain/task-42",
    );
    expect(pullRequestUrl("grain/config#42", "change/settings", "stable")).toBe(
      "/repos/grain/config/compare/stable...change/settings",
    );
  });

  it("keeps only grain/config on grain's own page", () => {
    // grain/config is the only configuration repository; a
    // grain/config-<team> name is an ordinary repo, linked like one.
    expect(pullRequestUrl("Grain/Config#6", "grain/552910a8/task-6")).toBe(
      "/repos/grain/config/compare/main...grain/552910a8/task-6",
    );
    expect(pullRequestUrl("grain/config-default#6")).toBe(
      "https://github.com/grain/config-default/pull/6",
    );
    expect(pullRequestUrl("grain/configuration#6")).toBe(
      "https://github.com/grain/configuration/pull/6",
    );
  });
});

describe("pullRequestUrl on another git host", () => {
  it("links a host-qualified repo's pull request on that host", () => {
    expect(pullRequestUrl("depot.code.corp.goog/experimental/grain#26")).toBe(
      "https://depot.code.corp.goog/experimental/grain/pull/26",
    );
  });
});

describe("issueUrl", () => {
  it("links an issue on GitHub, or on the repo's own host", () => {
    expect(issueUrl("acme/widgets#7")).toBe(
      "https://github.com/acme/widgets/issues/7",
    );
    expect(issueUrl("depot.code.corp.goog/experimental/grain#26")).toBe(
      "https://depot.code.corp.goog/experimental/grain/issues/26",
    );
  });
});

describe("internalPullRequestUrl", () => {
  it("links a task branch to its own pull request page, base and all", () => {
    expect(internalPullRequestUrl("acme/widgets", "grain/task-12")).toBe(
      "/repos/acme/widgets/compare/grain/task-12",
    );
    expect(
      internalPullRequestUrl("acme/widgets", "grain/task-12", "release/v1"),
    ).toBe("/repos/acme/widgets/compare/release/v1...grain/task-12");
  });
});

describe("repoUrl", () => {
  it("turns owner/name into the repo's own page on GitHub", () => {
    expect(repoUrl("acme/widgets")).toBe("https://github.com/acme/widgets");
  });

  it("turns host/owner/name into the repo's page on that host", () => {
    expect(repoUrl("ghe.example.com/acme/widgets")).toBe(
      "https://ghe.example.com/acme/widgets",
    );
  });
});

describe("completionPhase", () => {
  it("returns null for a task whose run is not over", () => {
    expect(
      completionPhase({ state: "queued", pullRequest: "acme/widgets#1" }),
    ).toBeNull();
  });

  it("returns null for a completed task with no pull request", () => {
    expect(completionPhase({ state: "completed", pullRequest: "" })).toBeNull();
  });

  // The other half of that: a task with nothing to land is
  // 'awaiting_reply' (STATE_LABELS.awaiting_reply), and its badge says the
  // wait itself, so there is no chip to add beside it either.
  it("puts up no chip for a task waiting on a reader", () => {
    expect(completionPhase({ state: "awaiting_reply" })).toBeNull();
  });

  // Waiting on a human's Submit click is a state now
  // (STATE_LABELS.awaiting_submit), not a chip: the badge says it, so
  // there is nothing left for a chip beside it to correct.
  it("puts up no chip for a task waiting on a Submit click", () => {
    expect(
      completionPhase({
        state: "awaiting_submit",
        pullRequest: "acme/widgets#1",
        autoMerge: false,
      }),
    ).toBeNull();
  });

  // The state badge beside it already reads "Queued for merge"
  // (STATE_LABELS.completed), so the ordinary case has no correction to
  // make and puts up no chip either.
  it("returns null once auto-merge is set and the queue has it", () => {
    expect(
      completionPhase({
        state: "completed",
        pullRequest: "acme/widgets#1",
        autoMerge: true,
      }),
    ).toBeNull();
  });

  it("reports merge blocked once the merge queue has given up, even with auto-merge set", () => {
    const phase = completionPhase({
      state: "completed",
      pullRequest: "acme/widgets#1",
      autoMerge: true,
      mergeQueueBlockedAt: "2026-08-01T00:00:00Z",
    });
    expect(phase.label).toBe("Merge blocked");
  });

  // A security hold is awaiting_submit as far as the badge goes, but what
  // it waits on is the security review grain filed, not a person -- and
  // that is the correction the chip makes.
  it("reports a security hold, naming the list version and the review", () => {
    const phase = completionPhase({
      state: "awaiting_submit",
      pullRequest: "acme/widgets#1",
      autoMerge: false,
      securityHold: "trust-boundary/v3",
      securityReviewTask: "1514",
    });
    expect(phase.label).toBe("Security hold");
    expect(phase.title).toContain("trust-boundary/v3");
    expect(phase.title).toContain("task 1514");
  });

  // The review outlives the hold (a hold ended on findings, or one only a
  // human may release, never had one): a task left with nothing but the
  // review is waiting on a Submit, which the badge already says.
  it("puts up no chip for a review whose hold has ended", () => {
    expect(
      completionPhase({
        state: "awaiting_submit",
        pullRequest: "acme/widgets#1",
        autoMerge: false,
        securityReviewTask: "1514",
      }),
    ).toBeNull();
  });
});

describe("capabilityName", () => {
  const config = { capabilities: [{ id: "web-search", name: "Web search" }] };

  it("returns the matching capability's name", () => {
    expect(capabilityName(config, "web-search")).toBe("Web search");
  });

  it("falls back to the id when no capability matches", () => {
    expect(capabilityName(config, "unknown")).toBe("unknown");
  });

  it("falls back to the id when config is null", () => {
    expect(capabilityName(null, "web-search")).toBe("web-search");
  });

  it("falls back to the id when config has no capabilities", () => {
    expect(capabilityName({}, "web-search")).toBe("web-search");
  });

  it("finds the name in config.ciCapabilities when absent from capabilities", () => {
    const cfg = {
      capabilities: [{ id: "gcp-key", name: "GCP key" }],
      ciCapabilities: [{ id: "package-publish", name: "Package publish" }],
    };
    expect(capabilityName(cfg, "package-publish")).toBe("Package publish");
  });
});

describe("capabilityRows", () => {
  const offered = [
    { id: "gcp-key", name: "GCP key" },
    { id: "gemini-key", name: "Gemini key" },
  ];

  it("is the offered listing untouched when everything selected has a row", () => {
    expect(capabilityRows(offered, ["gcp-key"])).toEqual(offered);
  });

  it("appends a row for a selected id with none, so it can be unticked", () => {
    expect(capabilityRows(offered, ["gcp-key", "scratch-repo"])).toEqual([
      ...offered,
      {
        id: "scratch-repo",
        name: "scratch-repo",
        description: RETIRED_CAPABILITY_HINT,
        retired: true,
      },
    ]);
  });

  it("appends nothing for an id that is offered but not selected", () => {
    expect(capabilityRows(offered, [])).toEqual(offered);
  });

  it("tolerates a missing listing or selection", () => {
    expect(capabilityRows(undefined, undefined)).toEqual([]);
    expect(capabilityRows(null, ["scratch-repo"])).toEqual([
      {
        id: "scratch-repo",
        name: "scratch-repo",
        description: RETIRED_CAPABILITY_HINT,
        retired: true,
      },
    ]);
  });

  // The filter every picker inherits by building its rows here.
  it("leaves out a capability this deployment has not configured", () => {
    const listing = [
      ...offered,
      { id: "console", name: "Console", ready: false },
    ];
    expect(capabilityRows(listing, [])).toEqual(offered);
  });
});

describe("configuredCapabilities", () => {
  const offered = [
    { id: "gcp-key", name: "GCP key", ready: true },
    { id: "gemini-key", name: "Gemini key", ready: false },
  ];

  it("offers only what this deployment can honour", () => {
    expect(configuredCapabilities(offered, [])).toEqual([offered[0]]);
  });

  // Hiding one already chosen would be a grant nothing could take off
  // again: a MUI Select deselects a value only through its own row.
  it("keeps an unready capability that is already selected", () => {
    expect(configuredCapabilities(offered, ["gemini-key"])).toEqual(offered);
  });

  // A listing from an endpoint that computes no readiness at all must
  // not render as an empty picker.
  it("offers a row whose readiness is unknown", () => {
    const unknown = [{ id: "self-debug", name: "Self debug" }];
    expect(configuredCapabilities(unknown, [])).toEqual(unknown);
  });

  it("tolerates a missing listing or selection", () => {
    expect(configuredCapabilities(undefined, undefined)).toEqual([]);
    expect(configuredCapabilities(offered, undefined)).toEqual([offered[0]]);
  });
});

describe("capabilityUnavailableHint", () => {
  it("names every gap a capability this deployment cannot honour still has", () => {
    const hint = capabilityUnavailableHint({
      id: "gemini-key",
      ready: false,
      needs: ["GCP project", "gcp-key-minter"],
    });
    expect(hint).toContain("GCP project");
    expect(hint).toContain("gcp-key-minter");
    expect(hint).toContain("fail to dispatch");
  });

  it("still warns when the gap has no name attached to it", () => {
    expect(
      capabilityUnavailableHint({ id: "gemini-key", ready: false }),
    ).toContain("Not ready");
  });

  it("says nothing about a ready capability", () => {
    expect(capabilityUnavailableHint({ id: "gemini-key", ready: true })).toBe(
      "",
    );
  });

  // A build or an endpoint that computes no readiness leaves the field
  // off entirely, and "unknown" must not read as a warning against every
  // capability on the list.
  it("says nothing when readiness is unknown", () => {
    expect(capabilityUnavailableHint({ id: "gemini-key" })).toBe("");
    expect(capabilityUnavailableHint({ id: "x", retired: true })).toBe("");
    expect(capabilityUnavailableHint(undefined)).toBe("");
  });
});

describe("knownRepos", () => {
  it("unions targetRepos and repos already seen on tasks, sorted and deduped", () => {
    const config = { targetRepos: ["acme/widgets", "acme/other"] };
    const tasks = [
      { repo: "acme/other" },
      { repo: "acme/newer" },
      { repo: "" },
    ];
    expect(knownRepos(config, tasks)).toEqual([
      "acme/newer",
      "acme/other",
      "acme/widgets",
    ]);
  });

  // One repository, however the two sources spell it -- and the option
  // is named the way the allowlist names it, since that entry is read
  // first.
  it("dedupes an allowlist entry against another spelling of itself", () => {
    const config = { targetRepos: ["bwsalmon/grain-home"] };
    const tasks = [{ repo: "Bwsalmon/grain-home" }];
    expect(knownRepos(config, tasks)).toEqual(["bwsalmon/grain-home"]);
  });

  it("offers the config repository for read and write selection", () => {
    const config = {
      configRepo: "grain/config",
      targetRepos: ["acme/widgets"],
    };
    expect(knownRepos(config, [])).toEqual(["acme/widgets", "grain/config"]);
    expect(targetableRepos(config, [])).toEqual([
      "acme/widgets",
      "grain/config",
    ]);
  });

  describe("targetableRepos", () => {
    // The narrow list: filing against a repo off the allowlist parks the
    // task, so the target field must not offer one.
    it("offers the allowlist alone on a restricted deployment", () => {
      const config = { targetRepos: ["acme/widgets", "acme/other"] };
      const tasks = [{ repo: "acme/newer" }, { repo: "" }];
      expect(targetableRepos(config, tasks)).toEqual([
        "acme/other",
        "acme/widgets",
      ]);
    });

    // Where nothing is restricted the two lists are one list, so a
    // single-repo deployment still gets its dropdown.
    it("falls back to knownRepos when nothing is restricted", () => {
      const tasks = [{ repo: "acme/widgets" }, { repo: "acme/other" }];
      expect(targetableRepos({ targetRepos: [] }, tasks)).toEqual([
        "acme/other",
        "acme/widgets",
      ]);
      expect(targetableRepos(null, tasks)).toEqual([
        "acme/other",
        "acme/widgets",
      ]);
    });

    // grain/config is the only configuration repository: a deployment
    // row from an older server that still names grain/config-default
    // adds nothing to the target field.
    it("offers grain/config and never grain/config-default on a restricted deployment", () => {
      const config = {
        configRepo: "grain/config",
        targetRepos: ["acme/widgets"],
        deployment: { id: "default", configRepo: "grain/config-default" },
      };
      expect(targetableRepos(config, [{ repo: "acme/task-repo" }])).toEqual([
        "acme/widgets",
        "grain/config",
      ]);
    });

    // grain/task-1850: a deployment with no repos attached is offered
    // what its GitHub credential can push to.
    describe("scoped to what the deployment can reach", () => {
      const tasks = [{ repo: "bwsalmon/grain" }];
      const reachable = [
        { repo: "gke-labs/bouncer", push: true },
        { repo: "gke-labs/docs", push: false },
      ];

      it("offers an attached-repos deployment its own repos, whatever its credential reaches", () => {
        const config = {
          configRepo: "grain/config",
          targetRepos: ["acme/infra"],
          deployment: { id: "default", configRepo: "grain/config-default" },
        };
        expect(targetableRepos(config, tasks, reachable)).toEqual([
          "acme/infra",
          "grain/config",
        ]);
      });

      it("offers a no-repos deployment what its credential can push to", () => {
        const config = {
          configRepo: "grain/config",
          deployment: { id: "default", configRepo: "grain/config-default" },
        };
        expect(targetableRepos(config, tasks, reachable)).toEqual([
          "gke-labs/bouncer",
          "grain/config",
        ]);
        // Until the list answers, every repo anything has targeted.
        expect(targetableRepos(config, tasks)).toEqual([
          "bwsalmon/grain",
          "grain/config",
        ]);
      });

      it("offers reads anything readable and writes only what it can push to", () => {
        const single = { configRepo: "grain/config" };
        const filed = [{ repo: "bwsalmon/grain" }];
        expect(pickerRepos(single, filed, reachable)).toEqual([
          "bwsalmon/grain",
          "gke-labs/bouncer",
          "gke-labs/docs",
          "grain/config",
        ]);
        expect(pickerRepos(single, filed, reachable, { push: true })).toEqual([
          "bwsalmon/grain",
          "gke-labs/bouncer",
          "grain/config",
        ]);
      });

      it("says whether a typed repo is reachable, and nothing without a list", () => {
        expect(
          repoReachable(reachable, "GKE-Labs/Bouncer", { push: true }),
        ).toBe(true);
        expect(repoReachable(reachable, "gke-labs/docs", { push: true })).toBe(
          false,
        );
        expect(repoReachable(reachable, "gke-labs/docs")).toBe(true);
        expect(repoReachable(reachable, "bwsalmon/grain")).toBe(false);
        expect(
          repoReachable(reachable, "grain/config-bouncer", { push: true }),
        ).toBe(true);
        expect(repoReachable(null, "bwsalmon/grain")).toBe(null);
      });
    });
  });

  describe("repoTargetable", () => {
    it("admits the allowlist, whatever case an entry is spelled in", () => {
      const config = { targetRepos: ["acme/widgets", "acme/other"] };
      expect(repoTargetable(config, "acme/widgets")).toBe(true);
      expect(repoTargetable(config, "Acme/Widgets")).toBe(true);
      expect(repoTargetable(config, "acme/elsewhere")).toBe(false);
    });

    // The config repository is the controller's own checkout and cannot
    // be taken off a deployment (model.ConfigRepo), so a task may be
    // filed against it however narrow targetRepos is -- which is what
    // the server already answers (model.RepoAllowed) and what the
    // target picker already offers (targetableRepos above).
    it("admits the config repository on a restricted deployment", () => {
      const config = {
        configRepo: "grain/config",
        targetRepos: ["acme/widgets"],
      };
      expect(repoTargetable(config, "grain/config")).toBe(true);
    });

    // grain/config is targetable however narrow the list is
    // (model.IsConfigRepo), with or without /api/config naming it --
    // New board, Edit board and Keep file their tasks against it. A
    // grain/config-<team> name is an ordinary repo, off the list here.
    it("admits grain/config and no grain/config-<team>", () => {
      const config = {
        targetRepos: ["acme/widgets"],
        deployment: { id: "default", configRepo: "grain/config-default" },
      };
      expect(repoTargetable(config, "grain/config")).toBe(true);
      expect(repoTargetable(config, "Grain/Config")).toBe(true);
      expect(repoTargetable(config, "grain/config-default")).toBe(false);
      expect(repoTargetable(config, "grain/config-infra")).toBe(false);
      expect(repoTargetable(config, "grain/other")).toBe(false);
      expect(repoTargetable(config, "acme/config-default")).toBe(false);
    });

    it("restricts nothing where there is no list", () => {
      expect(repoTargetable({ targetRepos: [] }, "acme/elsewhere")).toBe(true);
      expect(repoTargetable(null, "acme/elsewhere")).toBe(true);
    });
  });

  it("returns an empty list when nothing is configured or targeted yet", () => {
    expect(knownRepos(null, [])).toEqual([]);
    expect(knownRepos(null, null)).toEqual([]);
  });

  it("falls back to tasks alone on an unrestricted deployment", () => {
    expect(knownRepos({ targetRepos: [] }, [{ repo: "acme/widgets" }])).toEqual(
      ["acme/widgets"],
    );
  });
});

describe("lastBaseForRepo", () => {
  it("returns the most recently created task's base for that repo", () => {
    const tasks = [
      {
        repo: "acme/widgets",
        base: "release/1",
        createdAt: "2026-08-01T00:00:00Z",
      },
      {
        repo: "acme/widgets",
        base: "release/2",
        createdAt: "2026-08-02T00:00:00Z",
      },
    ];
    expect(lastBaseForRepo(tasks, "acme/widgets")).toBe("release/2");
  });

  it("returns empty when the most recent task on record left base unset, even if an older one set one", () => {
    const tasks = [
      {
        repo: "acme/widgets",
        base: "release/1",
        createdAt: "2026-08-01T00:00:00Z",
      },
      { repo: "acme/widgets", base: "", createdAt: "2026-08-02T00:00:00Z" },
    ];
    expect(lastBaseForRepo(tasks, "acme/widgets")).toBe("");
  });

  it("skips a failed or closed task and falls back to an older one", () => {
    const tasks = [
      {
        repo: "acme/widgets",
        base: "release/1",
        createdAt: "2026-08-01T00:00:00Z",
        state: "completed",
      },
      {
        repo: "acme/widgets",
        base: "release/2",
        createdAt: "2026-08-02T00:00:00Z",
        state: "failed",
      },
    ];
    expect(lastBaseForRepo(tasks, "acme/widgets")).toBe("release/1");
  });

  // A schedule, a suite pass, the merge queue and an agent's own
  // propose_task all pick a base for their own reasons -- none of it is
  // the human's choice of where the next hand-filed task starts.
  it("skips a task an agent or automation filed and falls back to an older one", () => {
    const tasks = [
      {
        repo: "acme/widgets",
        base: "release/1",
        createdAt: "2026-08-01T00:00:00Z",
        authorKind: "human",
      },
      {
        repo: "acme/widgets",
        base: "suite/run-7",
        createdAt: "2026-08-02T00:00:00Z",
        authorKind: "automation",
      },
      {
        repo: "acme/widgets",
        base: "grain/task-3",
        createdAt: "2026-08-03T00:00:00Z",
        authorKind: "agent",
      },
    ];
    expect(lastBaseForRepo(tasks, "acme/widgets")).toBe("release/1");
  });

  it("returns empty when every task on record for the repo was filed by an agent or automation", () => {
    const tasks = [
      {
        repo: "acme/widgets",
        base: "suite/run-7",
        createdAt: "2026-08-02T00:00:00Z",
        authorKind: "automation",
      },
    ];
    expect(lastBaseForRepo(tasks, "acme/widgets")).toBe("");
  });

  it("still suggests a task carrying no authorKind at all", () => {
    const tasks = [
      {
        repo: "acme/widgets",
        base: "release/1",
        createdAt: "2026-08-01T00:00:00Z",
      },
    ];
    expect(lastBaseForRepo(tasks, "acme/widgets")).toBe("release/1");
  });

  it("returns empty when no repo is given", () => {
    expect(
      lastBaseForRepo([{ repo: "acme/widgets", base: "release/1" }], ""),
    ).toBe("");
  });

  it("returns empty when the repo has no tasks on record", () => {
    expect(lastBaseForRepo([], "acme/widgets")).toBe("");
  });
});

describe("branchesInUse", () => {
  it("lists the branches tasks in that repo are working off, newest first", () => {
    const tasks = [
      {
        repo: "acme/widgets",
        base: "release/1",
        createdAt: "2026-08-01T00:00:00Z",
      },
      {
        repo: "acme/widgets",
        base: "release/2",
        createdAt: "2026-08-03T00:00:00Z",
      },
      { repo: "acme/gadgets", base: "elsewhere" },
    ];
    expect(branchesInUse(tasks, "acme/widgets")).toEqual([
      "release/2",
      "release/1",
    ]);
  });

  // A branch picked for a second repo on a task filed elsewhere is just
  // as much a branch somebody picked for that repo.
  it("takes the bases named for repos a task may also write to", () => {
    const tasks = [
      {
        repo: "acme/gadgets",
        base: "main",
        writes: [{ repo: "acme/widgets", base: "publish" }],
      },
    ];
    expect(branchesInUse(tasks, "acme/widgets")).toEqual(["publish"]);
  });

  it("names each branch once, however many tasks are on it", () => {
    const tasks = [
      { repo: "acme/widgets", base: "release/2" },
      { repo: "acme/widgets", base: "release/2" },
    ];
    expect(branchesInUse(tasks, "acme/widgets")).toEqual(["release/2"]);
  });

  // An empty base means the repo's own default branch, which the empty
  // box already says: it is not a name to offer.
  it("leaves out a task that named no branch at all", () => {
    const tasks = [
      { repo: "acme/widgets", base: "" },
      { repo: "acme/widgets" },
      { repo: "acme/widgets", base: "release/2" },
    ];
    expect(branchesInUse(tasks, "acme/widgets")).toEqual(["release/2"]);
  });

  // Unlike lastBaseForRepo, which prefills a value nobody chose to
  // type: a suggestion is read before it is picked, so a branch a
  // failed or automated task used is still worth offering to somebody
  // looking for the name they half remember.
  it("offers a branch a failed or automated task used", () => {
    const tasks = [
      {
        repo: "acme/widgets",
        base: "release/2",
        state: "failed",
        authorKind: "automation",
      },
    ];
    expect(branchesInUse(tasks, "acme/widgets")).toEqual(["release/2"]);
  });

  it("has nothing to offer for no repo, or for a repo with no tasks", () => {
    const tasks = [{ repo: "acme/widgets", base: "release/2" }];
    expect(branchesInUse(tasks, "")).toEqual([]);
    expect(branchesInUse(tasks, "acme/gadgets")).toEqual([]);
    expect(branchesInUse(null, "acme/widgets")).toEqual([]);
  });
});

describe("repoTasks", () => {
  const tasks = [
    { id: "1", repo: "acme/widgets" },
    { id: "2", repo: "acme/gadgets", writes: [{ repo: "acme/widgets" }] },
    { id: "3", repo: "acme/gadgets", reads: ["acme/widgets"] },
    { id: "4", repo: "acme/gadgets" },
    { id: "5", repo: "" },
  ];

  it("takes the tasks filed against the repo and the ones that may land work in it", () => {
    expect(repoTasks(tasks, "acme/widgets").map((t) => t.id)).toEqual([
      "1",
      "2",
    ]);
  });

  // A read-only repo grants a run no way to change anything in it, so a
  // task that reads acme/widgets is not work happening in acme/widgets.
  it("leaves out a task that only reads the repo", () => {
    expect(repoTasks(tasks, "acme/widgets").map((t) => t.id)).not.toContain(
      "3",
    );
  });

  it("counts a task once when it is filed against the repo and writes to it too", () => {
    const both = [
      { id: "1", repo: "acme/widgets", writes: [{ repo: "acme/widgets" }] },
    ];
    expect(repoTasks(both, "acme/widgets")).toHaveLength(1);
  });

  it("survives tasks with no writes, no repo, and no list at all", () => {
    expect(repoTasks(undefined, "acme/widgets")).toEqual([]);
    expect(repoTasks([{ id: "1", writes: null }], "acme/widgets")).toEqual([]);
    expect(repoTasks([{ id: "1", writes: [null] }], "acme/widgets")).toEqual(
      [],
    );
  });
});

describe("repoRows", () => {
  it("gives a targetRepos entry with no tasks a zero-count, configured row", () => {
    const config = { targetRepos: ["acme/widgets"] };
    const rows = repoRows(config, []);
    expect(rows).toEqual([
      {
        repo: "acme/widgets",
        total: 0,
        counts: {},
        blocked: 0,
        configured: true,
        defaults: false,
      },
    ]);
  });

  // The two spellings GitHub resolves to one repository resolve to one
  // row here too, and the row is named the way the allowlist names it --
  // the deployment's own canon (ui.(*Client).canonicalRepo).
  it("folds two spellings of one repo into the allowlist's own", () => {
    const config = { targetRepos: ["bwsalmon/grain-home"] };
    const tasks = [
      { repo: "Bwsalmon/grain-home", state: "queued", blocked: false },
      { repo: "bwsalmon/grain-home", state: "completed", blocked: false },
    ];
    const rows = repoRows(config, tasks);
    expect(rows).toEqual([
      {
        repo: "bwsalmon/grain-home",
        total: 2,
        counts: { queued: 1, completed: 1 },
        blocked: 0,
        configured: true,
        defaults: false,
      },
    ]);
  });

  // The count is a link to the task list narrowed to the repo, which
  // leaves standing sessions out (taskFilters.js's isSessionTask) -- so the count
  // has to as well, or "12 tasks filed here" opens a list of 11.
  it("leaves standing sessions out of a repo's counts, as the list it links to does", () => {
    const tasks = [
      { repo: "acme/widgets", state: "queued", blocked: false },
      {
        repo: "acme/widgets",
        state: "queued",
        blocked: false,
        interactive: true,
        consoleSession: "director",
      },
      // A session alone does not raise a row on an unrestricted deployment.
      {
        repo: "acme/chat-only",
        state: "awaiting_reply",
        consoleSession: "director",
      },
    ];
    expect(repoRows({}, tasks)).toEqual([
      {
        repo: "acme/widgets",
        total: 1,
        counts: { queued: 1 },
        blocked: 0,
        configured: false,
        defaults: false,
      },
    ]);
  });

  it("includes the config repo and marks it as non-removable", () => {
    const rows = repoRows(
      { configRepo: "acme/config", targetRepos: ["acme/widgets"] },
      [],
    );
    expect(rows).toEqual([
      {
        repo: "acme/config",
        total: 0,
        counts: {},
        blocked: 0,
        configured: false,
        defaults: false,
        configRepo: true,
      },
      {
        repo: "acme/widgets",
        total: 0,
        counts: {},
        blocked: 0,
        configured: true,
        defaults: false,
      },
    ]);
  });

  // Removing the last thing that names a repo removes the repo: before
  // this, its old tasks held a `configured: false` row up forever and
  // the pane offers no Remove for one of those.
  it("drops a repo's row once it leaves targetRepos", () => {
    const tasks = [{ repo: "acme/gone", state: "completed", blocked: false }];
    expect(
      repoRows({ targetRepos: ["acme/gone"] }, tasks).map((r) => r.repo),
    ).toEqual(["acme/gone"]);
    expect(
      repoRows({ targetRepos: ["acme/widgets"] }, tasks).map((r) => r.repo),
    ).toEqual(["acme/widgets"]);
  });

  // The other side of that rule: a repo that carries configuration of
  // its own is not "gone" just because it is off the allowlist, and its
  // configuration is editable nowhere else.
  it("keeps a row for an off-allowlist repo that still carries defaults", () => {
    const config = {
      targetRepos: ["acme/widgets"],
      reposWithSetupCommand: ["acme/tools"],
    };
    const tasks = [{ repo: "acme/tools", state: "queued", blocked: false }];
    const rows = repoRows(config, tasks);
    expect(rows.map((r) => r.repo)).toEqual(["acme/tools", "acme/widgets"]);
    const tools = rows.find((r) => r.repo === "acme/tools");
    expect(tools.defaults).toBe(true);
    expect(tools.configured).toBe(false);
    expect(tools.total).toBe(1);
  });

  it("marks a repo only known through its tasks as unconfigured", () => {
    const tasks = [{ repo: "acme/other", state: "queued", blocked: false }];
    const rows = repoRows({ targetRepos: [] }, tasks);
    expect(rows).toEqual([
      {
        repo: "acme/other",
        total: 1,
        counts: { queued: 1 },
        blocked: 0,
        configured: false,
        defaults: false,
      },
    ]);
  });

  it("gives no row to a repo a restricted deployment no longer names", () => {
    const config = { targetRepos: ["acme/widgets", "acme/other"] };
    const tasks = [
      { repo: "acme/widgets", state: "queued", blocked: true },
      { repo: "acme/newer", state: "completed", blocked: false },
      { repo: "", state: "proposed", blocked: false },
    ];
    const rows = repoRows(config, tasks);
    expect(rows.map((r) => r.repo)).toEqual(["acme/other", "acme/widgets"]);

    const widgets = rows.find((r) => r.repo === "acme/widgets");
    expect(widgets).toEqual({
      repo: "acme/widgets",
      total: 1,
      counts: { queued: 1 },
      blocked: 1,
      configured: true,
      defaults: false,
    });
  });

  // A repo can hold defaults of its own without being allow-listed and
  // without any task targeting it (ui.(*Client).SetRepoDefaultCapabilities
  // deliberately doesn't require either), and this page is the only place
  // that set can be edited -- so it has to have a row here.
  it("gives a repo that only carries defaults of its own a row of its own", () => {
    const config = {
      targetRepos: [],
      repoDefaultCapabilities: { "acme/orphan": ["gcp-key"] },
    };
    const rows = repoRows(config, []);
    expect(rows).toEqual([
      {
        repo: "acme/orphan",
        total: 0,
        counts: {},
        blocked: 0,
        configured: false,
        defaults: true,
      },
    ]);
  });

  // The same argument for the other kind of per-repo configuration: a
  // repo whose only presence here is standing instructions of its own
  // (ui.configResponse.ReposWithPromptExtension, grain/task-114), which
  // reach every run against it and can only be read or edited here.
  it("gives a repo that only carries a prompt extension a row of its own", () => {
    const config = {
      targetRepos: [],
      reposWithPromptExtension: ["acme/orphan"],
    };
    const rows = repoRows(config, []);
    expect(rows).toEqual([
      {
        repo: "acme/orphan",
        total: 0,
        counts: {},
        blocked: 0,
        configured: false,
        defaults: true,
      },
    ]);
  });

  // And the third kind (grain/task-154): a repo whose only presence here
  // is the setup command grain runs in every checkout it makes there
  // (ui.configResponse.ReposWithSetupCommand), which is as invisible
  // from anywhere else as the two above.
  it("gives a repo that only carries a setup command a row of its own", () => {
    const config = { targetRepos: [], reposWithSetupCommand: ["acme/orphan"] };
    const rows = repoRows(config, []);
    expect(rows).toEqual([
      {
        repo: "acme/orphan",
        total: 0,
        counts: {},
        blocked: 0,
        configured: false,
        defaults: true,
      },
    ]);
  });

  // And the fourth (grain/task-245): a repo whose only presence here is
  // the CI workflows grain declares for it
  // (ui.configResponse.ReposWithWorkflows). The likeliest of the four to
  // be a repo's whole presence, since declaring workflows on grain's
  // side is what you do for a repository grain does not control -- and
  // this page is the only place they can be edited.
  it("gives a repo that only carries CI workflows a row of its own", () => {
    const config = { targetRepos: [], reposWithWorkflows: ["acme/theirs"] };
    const rows = repoRows(config, []);
    expect(rows).toEqual([
      {
        repo: "acme/theirs",
        total: 0,
        counts: {},
        blocked: 0,
        configured: false,
        defaults: true,
      },
    ]);
  });

  it("does not duplicate a repo that carries defaults and is also allow-listed or targeted", () => {
    const config = {
      targetRepos: ["acme/widgets"],
      repoDefaultCapabilities: {
        "acme/widgets": ["gcp-key"],
        "acme/other": ["gcp-key"],
      },
    };
    const tasks = [{ repo: "acme/other", state: "queued", blocked: false }];
    const rows = repoRows(config, tasks);
    expect(rows.map((r) => r.repo)).toEqual(["acme/other", "acme/widgets"]);
    expect(rows.find((r) => r.repo === "acme/widgets")).toEqual({
      repo: "acme/widgets",
      total: 0,
      counts: {},
      blocked: 0,
      configured: true,
      defaults: true,
    });
    expect(rows.find((r) => r.repo === "acme/other")).toEqual({
      repo: "acme/other",
      total: 1,
      counts: { queued: 1 },
      blocked: 0,
      configured: false,
      defaults: true,
    });
  });

  // An absent key and an empty list mean the same thing on /api/config --
  // this repo adds nothing -- so an empty one is not a repo to list.
  it("gives no row to a repoDefaultCapabilities key with an empty set", () => {
    expect(
      repoRows({ repoDefaultCapabilities: { "acme/orphan": [] } }, []),
    ).toEqual([]);
  });

  it("returns an empty list when nothing is configured or targeted yet", () => {
    expect(repoRows(null, [])).toEqual([]);
  });

  // grain/config is the only configuration repository, so it is the
  // only config row -- even beside a deployment row from an older server
  // that still names grain/config-default.
  it("gives grain/config the only config repository row", () => {
    const config = {
      configRepo: "grain/config",
      targetRepos: ["acme/global"],
      deployment: { id: "default", configRepo: "grain/config-default" },
    };
    const tasks = [{ repo: "acme/global", state: "queued" }];
    const rows = repoRows(config, tasks);
    expect(rows.map((r) => r.repo)).toEqual(["acme/global", "grain/config"]);
    expect(rows.find((r) => r.repo === "grain/config").configRepo).toBe(true);
    expect(rows[0].total).toBe(1);
  });
});

// The dot the repos list draws ahead of a repo's name (grain/task-327).
describe("repoActivity", () => {
  const row = (counts) => ({
    repo: "acme/widgets",
    counts,
    total: Object.values(counts).reduce((a, b) => a + b, 0),
    blocked: 0,
  });

  it("calls a repo with a run in flight active, counting the runs", () => {
    expect(repoActivity(row({ running: 2, queued: 1 }))).toMatchObject({
      key: "running",
      state: "running",
      title: "Active -- 2 tasks running now",
    });
  });

  it("calls a repo with work but nothing running open", () => {
    expect(repoActivity(row({ queued: 1, closed: 3 }))).toMatchObject({
      key: "open",
      state: "queued",
      title: "1 open task, none running right now",
    });
  });

  // "Queued for merge" reads like an ending and is not one: the task is
  // waiting on the merge queue and can still come back needing a human.
  it("counts a task waiting on the merge queue as open work", () => {
    expect(repoActivity(row({ completed: 1 })).key).toBe("open");
  });

  it("calls a repo whose every task is closed idle", () => {
    expect(repoActivity(row({ closed: 2 }))).toMatchObject({
      key: "idle",
      state: "closed",
      title: "Idle -- every task here is closed",
    });
  });

  // A deferred task is work nobody is waiting on, so a repo holding
  // nothing else is idle rather than open -- and the title names both
  // states when both are there, so "idle" is never a claim that
  // everything here is finished.
  it("counts a deferred task as set aside rather than as open work", () => {
    expect(repoActivity(row({ deferred: 2 }))).toMatchObject({
      key: "idle",
      title: "Idle -- every task here is deferred",
    });
    expect(repoActivity(row({ deferred: 1, closed: 1 })).title).toBe(
      "Idle -- every task here is deferred or closed",
    );
    expect(repoActivity(row({ deferred: 1, queued: 1 })).key).toBe("open");
  });

  it("calls a repo with no tasks at all idle, and says which kind", () => {
    expect(repoActivity(row({})).title).toBe("Idle -- no tasks here yet");
  });
});

describe("unionCapabilities", () => {
  it("appends the second layer to the first, deduped and in order", () => {
    expect(
      unionCapabilities(["gemini-key"], ["gcp-key", "gemini-key"]),
    ).toEqual(["gemini-key", "gcp-key"]);
  });

  it("treats a missing layer as nothing to add", () => {
    expect(unionCapabilities(undefined, undefined)).toEqual([]);
    expect(unionCapabilities(["gcp-key"], null)).toEqual(["gcp-key"]);
  });
});

describe("defaultCapabilitiesFor", () => {
  const config = {
    defaultCapabilities: ["gemini-key"],
    repoDefaultCapabilities: { "acme/widgets": ["gcp-key"] },
  };

  it("adds the repo's own defaults to the deployment's", () => {
    expect(defaultCapabilitiesFor(config, "acme/widgets")).toEqual([
      "gemini-key",
      "gcp-key",
    ]);
  });

  it("gives a repo with none of its own the deployment's alone", () => {
    expect(defaultCapabilitiesFor(config, "acme/gadgets")).toEqual([
      "gemini-key",
    ]);
  });

  // No repo picked, or a deliberately repo-less task: there is no second
  // layer to key on, the same answer CreateTask gives a task whose
  // Target is nil.
  it("gives a task with no repo the deployment's alone", () => {
    expect(defaultCapabilitiesFor(config, "")).toEqual(["gemini-key"]);
  });

  it("survives a config that has never been loaded", () => {
    expect(defaultCapabilitiesFor(null, "acme/widgets")).toEqual([]);
  });
});

describe("orphanedPullRequest", () => {
  it("names the pull request a closed task has left open", () => {
    expect(
      orphanedPullRequest({ state: "closed", pullRequest: "acme/widgets#1" }),
    ).toBe("acme/widgets#1");
  });

  // The ordinary ending, not an orphan: a merged pull request closes its
  // own task (orchestrator.recordPullRequestEvents sets ClosedAt
  // alongside PrMergedAt), so the state and the link alone cannot tell
  // the two apart.
  it("says nothing about a task closed because its pull request merged", () => {
    expect(
      orphanedPullRequest({
        state: "closed",
        pullRequest: "acme/widgets#1",
        pullRequestEvents: [{ kind: "opened" }, { kind: "merged" }],
      }),
    ).toBeNull();
  });

  it("says nothing about a pull request that closed without merging", () => {
    expect(
      orphanedPullRequest({
        state: "closed",
        pullRequest: "acme/widgets#1",
        pullRequestEvents: [{ kind: "closed" }],
      }),
    ).toBeNull();
  });

  it("says nothing about a task that is not closed, or has no pull request", () => {
    expect(
      orphanedPullRequest({
        state: "completed",
        pullRequest: "acme/widgets#1",
      }),
    ).toBeNull();
    expect(orphanedPullRequest({ state: "closed" })).toBeNull();
  });
});

// The same pull request a moment earlier, while closing the task is still
// a choice: this is what decides whether the Close button is offered the
// checkbox that closes it too.
describe("closablePullRequest", () => {
  it("names the pull request closing this task would orphan", () => {
    expect(
      closablePullRequest({
        state: "completed",
        pullRequest: "acme/widgets#1",
      }),
    ).toBe("acme/widgets#1");
    expect(
      closablePullRequest({ state: "running", pullRequest: "acme/widgets#1" }),
    ).toBe("acme/widgets#1");
  });

  it("names nothing when there is no open pull request to close", () => {
    expect(closablePullRequest({ state: "completed" })).toBeNull();
    expect(
      closablePullRequest({
        state: "completed",
        pullRequest: "acme/widgets#1",
        pullRequestEvents: [{ kind: "merged" }],
      }),
    ).toBeNull();
    expect(
      closablePullRequest({
        state: "completed",
        pullRequest: "acme/widgets#1",
        pullRequestEvents: [{ kind: "closed" }],
      }),
    ).toBeNull();
  });

  // A task already closed has no close to make the choice at: the warning
  // orphanedPullRequest drives is what that task gets instead.
  it("names nothing on a task that is already closed", () => {
    expect(
      closablePullRequest({ state: "closed", pullRequest: "acme/widgets#1" }),
    ).toBeNull();
  });
});

describe("isTaskMerged", () => {
  it("returns true when task has merged flag or prMergedAt", () => {
    expect(isTaskMerged({ merged: true })).toBe(true);
    expect(isTaskMerged({ prMergedAt: "2026-09-26T00:00:00Z" })).toBe(true);
  });

  it("returns true when task closedReason is merged", () => {
    expect(isTaskMerged({ closedReason: "merged" })).toBe(true);
  });

  it("returns true when pullRequestEvents contains a merged event", () => {
    expect(
      isTaskMerged({
        pullRequestEvents: [{ kind: "opened" }, { kind: "merged" }],
      }),
    ).toBe(true);
  });

  it("returns false for non-merged task or falsy input", () => {
    expect(isTaskMerged(null)).toBe(false);
    expect(isTaskMerged(undefined)).toBe(false);
    expect(isTaskMerged({ state: "running" })).toBe(false);
    expect(
      isTaskMerged({
        pullRequestEvents: [{ kind: "opened" }, { kind: "closed" }],
      }),
    ).toBe(false);
  });
});

// A task the merge queue is repairing is running (or queued) like any
// other attempt, and the state alone cannot say which kind of work it is
// -- so the label does, and StateDot colours the mark to match.
describe("stateLabel", () => {
  it("uses the plain state label when nothing is being repaired", () => {
    expect(stateLabel({ state: "running" })).toBe("Running");
    expect(stateLabel({ state: "running", interactive: true })).toBe(
      "Interactive",
    );
    expect(stateLabel({ state: "queued", interactive: true })).toBe("Waiting");
    expect(stateLabel({ state: "queued" })).toBe("Waiting");
    expect(stateLabel({ state: "completed" })).toBe("Merging");
  });

  it("calls an unapproved GitHub issue import Imported", () => {
    expect(
      stateLabel({
        state: "proposed",
        imported: true,
        awaitingImportApproval: true,
      }),
    ).toBe("Imported");
    // Approved, it reads as its state like any other task.
    expect(stateLabel({ state: "queued", imported: true })).toBe("Waiting");
    expect(stateLabel({ state: "proposed" })).toBe("Proposed");
  });

  it("says what a merge-queue repair is doing instead", () => {
    expect(stateLabel({ state: "running", repairing: true })).toBe("Repairing");
    expect(stateLabel({ state: "queued", repairing: true })).toBe(
      "Waiting for repair",
    );
  });

  // Only those two states are ever a repair in flight, so anything else
  // arriving with the flag set keeps the badge honest rather than
  // inventing a phrase for a combination the backend does not produce.
  it("leaves any other state's label alone", () => {
    expect(stateLabel({ state: "awaiting_reply", repairing: true })).toBe(
      "Asked",
    );
  });

  it("distinguishes merged from other closed tasks", () => {
    expect(
      stateLabel({
        state: "closed",
        prMergedAt: "2026-09-26T00:00:00Z",
      }),
    ).toBe("Merged");
    expect(
      stateLabel({
        state: "closed",
        pullRequestEvents: [{ kind: "merged", at: "2026-09-26T00:00:00Z" }],
      }),
    ).toBe("Merged");
    expect(
      stateLabel({
        state: "closed",
        endedAt: null,
      }),
    ).toBe("Closed");
    expect(
      stateLabel({
        state: "closed",
        endedAt: "2026-09-26T00:00:00Z",
      }),
    ).toBe("Closed");
  });

  it("falls back to the raw state for one it has no label for", () => {
    expect(stateLabel({ state: "invented" })).toBe("invented");
  });
});

describe("stateTitle", () => {
  it("appends the wait reason on hover for a queued task waiting on setup", () => {
    expect(
      stateTitle({
        state: "queued",
        activity:
          "waiting: all connections serving claude-sonnet-4-6 are busy (1 busy: 4/4 in use)",
        activityBySetup: true,
      }),
    ).toBe(
      "Waiting: all connections serving claude-sonnet-4-6 are busy (1 busy: 4/4 in use)",
    );
  });

  it("uses stateLabel when a queued task is not waiting or when running", () => {
    expect(stateTitle({ state: "queued" })).toBe("Waiting");
    expect(stateTitle({ state: "queued", activity: "leftover" })).toBe(
      "Waiting",
    );
    expect(
      stateTitle({
        state: "running",
        activity: "building a sandbox",
        activityBySetup: true,
      }),
    ).toBe("Running");
  });
});

describe("isTaskMerged", () => {
  it("identifies merged tasks", () => {
    expect(
      isTaskMerged({ state: "closed", prMergedAt: "2026-09-26T00:00:00Z" }),
    ).toBe(true);
    expect(
      isTaskMerged({
        state: "closed",
        pullRequestEvents: [{ kind: "merged" }],
      }),
    ).toBe(true);
    expect(isTaskMerged({ state: "closed" })).toBe(false);
    expect(isTaskMerged({ state: "running" })).toBe(false);
  });
});

describe("blockedDescription", () => {
  it("returns 'Blocked' when there are no blockers", () => {
    expect(blockedDescription({ blockedBy: [] })).toBe("Blocked");
    expect(blockedDescription({})).toBe("Blocked");
  });

  it("says 'Waiting on' for a task its own run parked", () => {
    const byId = new Map([
      ["4944", { id: "4944", title: "Publish image", state: "running" }],
    ]);
    expect(
      blockedDescription({ blockedBy: ["4944"], parked: true }, byId),
    ).toBe("Waiting on: #4944 (running)");
  });

  it("formats single blocker with resolved state", () => {
    const byId = new Map([
      ["105", { id: "105", title: "Setup auth", state: "closed" }],
    ]);
    expect(blockedDescription({ blockedBy: ["105"] }, byId)).toBe(
      "Blocked on: #105 (cancelled)",
    );
  });

  it("formats failed blocker", () => {
    const byId = new Map([
      ["102", { id: "102", title: "Build api", state: "failed" }],
    ]);
    expect(blockedDescription({ blockedBy: ["102"] }, byId)).toBe(
      "Blocked on: #102 (failed)",
    );
  });

  it("formats multiple blockers with states", () => {
    const byId = new Map([
      ["105", { id: "105", title: "Setup auth", state: "closed" }],
      ["102", { id: "102", title: "Build api", state: "running" }],
    ]);
    expect(blockedDescription({ blockedBy: ["105", "102"] }, byId)).toBe(
      "Blocked on: #105 (cancelled), #102 (running)",
    );
  });

  it("falls back to ID when blocker is not in lookup map", () => {
    expect(blockedDescription({ blockedBy: ["105"] }, new Map())).toBe(
      "Blocked on: #105",
    );
  });
});

describe("runActivity", () => {
  it.each([
    ["waiting_ci", "Waiting for CI"],
    ["waiting_queue", "Waiting on Queue"],
    ["waiting_review", "Waiting for its review"],
    ["merging", "Merging"],
    ["updating_branch", "Bringing the branch up to date"],
    ["batching", "Testing Batch"],
    ["gathering", "Held to be tested with the next change"],
    ["waiting_github_approval", "Waiting for an approval GitHub requires"],
    ["github_merge_queue", "In GitHub's merge queue"],
    ["repair_needed", "Needs repairing"],
    ["repair_queued", "Queued for repair"],
    ["repairing", "Resolving Conflicts"],
    ["agent_working", "An agent is on this branch"],
    ["merge_failed", "Merge failed"],
    ["unreadable", "Can't read the pull request"],
  ])(
    "shows grain's own %s merge activity after the run",
    (mergeQueueState, note) => {
      expect(runActivity({ state: "completed", mergeQueueState })).toEqual({
        note,
        detail: null,
        age: null,
        since: null,
        bySetup: true,
        byQueue: true,
      });
    },
  );

  // The queue's own account of the wait is the point of the whole
  // exercise: "Merging" that has stood for three hours, with GitHub's
  // reason beside it, is a different thing from "Merging" said a moment
  // ago -- and before this the row could say neither.
  it("carries the queue's note and how long the wait has stood", () => {
    const now = Date.UTC(2026, 8, 4, 12, 0, 0);
    expect(
      runActivity(
        {
          state: "completed",
          mergeQueueState: "merge_failed",
          mergeQueueNote: "GitHub has refused this merge 12 times: no",
          mergeQueueStateAt: new Date(now - 3 * 3600 * 1000).toISOString(),
        },
        now,
      ),
    ).toEqual({
      note: "Merge failed",
      detail: "GitHub has refused this merge 12 times: no",
      age: "3h ago",
      since: "3h",
      bySetup: true,
      byQueue: true,
    });
  });

  // ...and a queue state that has only just changed is not dated at all:
  // "Waiting for CI" said a minute ago is a suite that has just started,
  // which is exactly what a reader assumes of an undated line.
  it("leaves a merge wait that has only just started undated", () => {
    const now = Date.UTC(2026, 8, 4, 12, 0, 0);
    expect(
      runActivity(
        {
          state: "completed",
          mergeQueueState: "waiting_ci",
          mergeQueueStateAt: new Date(now - 60_000).toISOString(),
        },
        now,
      ).age,
    ).toBeNull();
  });

  // A state only a newer daemon writes renders as nothing rather than as
  // its raw name: the two halves are deployed separately.
  it("says nothing about a merge state it has no phrase for", () => {
    expect(
      runActivity({ state: "completed", mergeQueueState: "waiting_thing" }),
    ).toBeNull();
  });

  // Active merge operations where grain is working (merging, CI, updating branch,
  // batching) are active, while waiting in queue, waiting for review, or stalled
  // states are not.
  it.each([
    ["waiting_ci", true],
    ["merging", true],
    ["updating_branch", true],
    ["batching", true],
    ["waiting_queue", false],
    ["waiting_review", false],
    // grain is fixing the base branch, not this change: a live mark here
    // would claim work is happening to a row nothing is happening to.
    // a person on GitHub owes it an approval: nothing is happening to it.
    ["waiting_github_approval", false],
    // GitHub's own merge queue is landing it, not grain.
    ["github_merge_queue", false],
    ["gathering", false],
    ["repair_needed", false],
    ["repair_queued", false],
    ["repairing", false],
    ["agent_working", false],
    ["merge_failed", false],
    ["unreadable", false],
  ])("treats %s as active merge: %s", (mergeQueueState, live) => {
    expect(mergeQueueActive({ state: "completed", mergeQueueState })).toBe(
      live,
    );
  });

  describe("isAgentRunning and isGrainActive and taskBadgeState", () => {
    it("recognizes agent running when state is running and not in setup", () => {
      const task = { state: "running" };
      expect(isAgentRunning(task)).toBe(true);
      expect(isGrainActive(task)).toBe(false);
      expect(taskBadgeState(task)).toBe("running");
    });

    it("recognizes grain active when state is running with setup activity", () => {
      const task = {
        state: "running",
        activityBySetup: true,
        activity: "building a sandbox",
      };
      expect(isAgentRunning(task)).toBe(false);
      expect(isGrainActive(task)).toBe(true);
      expect(taskBadgeState(task)).toBe("running");
    });

    it("recognizes grain active when task is completed and merge queue is active", () => {
      const task = { state: "completed", mergeQueueState: "merging" };
      expect(isAgentRunning(task)).toBe(false);
      expect(isGrainActive(task)).toBe(true);
      expect(taskBadgeState(task)).toBe("running");
    });

    it("does not treat queued tasks or merge queue waiting as agent running or grain active", () => {
      const queuedTask = { state: "queued" };
      expect(isAgentRunning(queuedTask)).toBe(false);
      expect(isGrainActive(queuedTask)).toBe(false);
      expect(taskBadgeState(queuedTask)).toBe("queued");

      const waitQueueTask = {
        state: "completed",
        mergeQueueState: "waiting_queue",
      };
      expect(isAgentRunning(waitQueueTask)).toBe(false);
      expect(isGrainActive(waitQueueTask)).toBe(false);
      expect(taskBadgeState(waitQueueTask)).toBe("completed");
    });

    it("identifies tasks undergoing merge-queue repair or active merge as isTaskRepairing", () => {
      expect(isTaskRepairing({ state: "running", repairing: true })).toBe(true);
      expect(
        isTaskRepairing({ state: "completed", mergeQueueState: "merging" }),
      ).toBe(true);
      expect(
        isTaskRepairing({ state: "completed", mergeQueueState: "waiting_ci" }),
      ).toBe(true);
      expect(
        isTaskRepairing({ state: "completed", mergeQueueState: "batching" }),
      ).toBe(true);
      expect(
        isTaskRepairing({
          state: "completed",
          mergeQueueState: "updating_branch",
        }),
      ).toBe(true);
      expect(isTaskRepairing({ state: "running" })).toBe(false);
      expect(isTaskRepairing({ state: "queued" })).toBe(false);
      expect(
        isTaskRepairing({
          state: "completed",
          mergeQueueState: "waiting_queue",
        }),
      ).toBe(false);
    });

    it("assigns appropriate badge modifier classes for repair, merge, and interactive tasks", () => {
      expect(
        taskBadgeModifier({ state: "completed", mergeQueueState: "merging" }),
      ).toBe("badge-repair");
      expect(taskBadgeModifier({ state: "running", repairing: true })).toBe(
        "badge-repair",
      );
      expect(taskBadgeModifier({ state: "running", interactive: true })).toBe(
        "badge-interactive",
      );
      expect(taskBadgeModifier({ state: "running" })).toBe("");
      expect(taskBadgeModifier({ state: "queued" })).toBe("");
    });
  });

  it("ignores stale merge activity after a task is closed", () => {
    const task = { state: "closed", mergeQueueState: "merging" };
    expect(mergeQueueActive(task)).toBe(false);
    expect(runActivity(task)).toBeNull();
  });

  // The static half of the question is which states grain drives; the
  // other half is whether it is getting anywhere, which the daemon
  // answers (Task.MergeQueueStalledAt, orchestrator's liveness.go). A
  // flagged row kept the live mark until now, which is the one thing the
  // set above exists to stop.
  it("takes the live mark off a state grain has flagged as stalled", () => {
    const stalled = {
      state: "completed",
      mergeQueueState: "waiting_ci",
      mergeQueueStalledAt: at(60 * 60_000),
    };
    expect(mergeQueueStalled(stalled)).toBe(true);
    expect(mergeQueueActive(stalled)).toBe(false);
    // The words stay: it is still nominally waiting for CI, and the note
    // beside them is the account of why that has stopped meaning
    // anything.
    expect(runActivity(stalled, now).note).toBe("Waiting for CI");
  });

  it("leaves an unflagged wait alone", () => {
    const waiting = { state: "completed", mergeQueueState: "waiting_ci" };
    expect(mergeQueueStalled(waiting)).toBe(false);
    expect(mergeQueueActive(waiting)).toBe(true);
    expect(mergeQueueStall(waiting)).toBeNull();
  });

  // A closed task's own terminal state wins over the stamp exactly as it
  // wins over the state: the observation can outlive the pull request it
  // describes, and there is no live queue row left to flag.
  it("flags nothing on a closed task", () => {
    const closed = {
      state: "closed",
      mergeQueueState: "waiting_ci",
      mergeQueueStalledAt: at(60 * 60_000),
    };
    expect(mergeQueueStalled(closed)).toBe(false);
    expect(mergeQueueStall(closed)).toBeNull();
  });

  // And the chip, which is the half of this a person reads: the missing
  // mark stops the row claiming progress, and only this says outright
  // that somebody should look.
  it("offers a chip for a stalled row", () => {
    const chip = mergeQueueStall({
      state: "completed",
      mergeQueueState: "waiting_ci",
      mergeQueueStalledAt: at(60 * 60_000),
    });
    expect(chip.label).toBe("Stalled");
    expect(chip.color).toBe("warning");
    expect(chip.title).toMatch(/stuck rather than waiting/);
  });

  const at = (msAgo) =>
    new Date(Date.UTC(2026, 8, 4, 12, 0, 0) - msAgo).toISOString();
  const now = Date.UTC(2026, 8, 4, 12, 0, 0);

  it("gives back what a running task's own agent said, and how old it is", () => {
    expect(
      runActivity(
        {
          state: "running",
          activity: "waiting for CI",
          activityAt: at(22 * 60_000),
        },
        now,
      ),
    ).toEqual({
      note: "waiting for CI",
      age: "22m ago",
      since: "22m",
      bySetup: false,
    });
  });

  // A run narrates every tool call it makes now
  // (mcp.Registry.AnnounceActivity), so a line a few minutes old is an
  // ordinary one: the age is what tells a run wedged inside one call from
  // a run getting on with it, and it is spent only once there is
  // something to tell.
  it("does not date an agent's line until it has stood a while", () => {
    const line = (msAgo) =>
      runActivity(
        {
          state: "running",
          activity: "running the tests",
          activityAt: at(msAgo),
        },
        now,
      ).age;
    expect(line(30_000)).toBeNull();
    expect(line(4 * 60_000)).toBeNull();
    expect(line(5 * 60_000)).toBe("5m ago");
  });

  // grain's own phrases get longer to stand in: "building a sandbox" is a
  // VM boot and "running the repo's setup command" has ten minutes of its
  // own before the orchestrator gives up on it, so dating them at five
  // would put an age on every dispatch that was working perfectly well.
  it("gives grain's own setup phrase longer before it is dated", () => {
    const line = (msAgo) =>
      runActivity(
        {
          state: "running",
          activity: "building a sandbox",
          activityAt: at(msAgo),
          activityBySetup: true,
        },
        now,
      ).age;
    expect(line(6 * 60_000)).toBeNull();
    expect(line(12 * 60_000)).toBe("12m ago");
  });

  // grain writes this field too, for the stretch before the agent's
  // first turn (orchestrator.setupNotes) -- and says so, since every
  // other phrase that appears here is something an agent wrote.
  it("carries whether the phrase is grain's own rather than the run's", () => {
    expect(
      runActivity(
        {
          state: "running",
          activity: "building a sandbox",
          activityAt: at(30_000),
          activityBySetup: true,
        },
        now,
      ),
    ).toEqual({
      note: "building a sandbox",
      age: null,
      since: "now",
      bySetup: true,
    });
  });

  it("shows grain's grant wait while the task is still queued", () => {
    expect(
      runActivity(
        {
          state: "queued",
          activity: "waiting for Gemini API keys (gemini-key grant)",
          activityAt: at(40 * 60_000),
          activityBySetup: true,
        },
        now,
      ),
    ).toEqual({
      note: "waiting for Gemini API keys (gemini-key grant)",
      age: "40m ago",
      since: "40m",
      bySetup: true,
    });
  });

  // The API only carries a synopsis for a live run, but a poll's answer
  // can be a few seconds old -- and a phrase left beside a "Completed"
  // badge reads as a run still going.
  it("says nothing once the task has stopped running", () => {
    expect(
      runActivity(
        { state: "completed", activity: "waiting for CI", activityAt: at(0) },
        now,
      ),
    ).toBeNull();
    expect(
      runActivity(
        { state: "failed", activity: "waiting for CI", activityAt: at(0) },
        now,
      ),
    ).toBeNull();
  });

  it("says nothing for a running task whose agent has not said anything", () => {
    expect(runActivity({ state: "running" }, now)).toBeNull();
  });

  // grain/task-256: the merge queue's account of a task is written by a
  // cycle and taken back by a cycle, so a task the queue has stopped
  // watching -- one somebody put an agent back on themselves, whose
  // cleared completion drops it out of Store.OpenPullRequestLinks -- can
  // carry a state from the round before. A run is happening now, and what
  // is happening outranks what was last said.
  it("lets a live run's own line outrank a merge queue phrase", () => {
    expect(
      runActivity(
        {
          state: "running",
          mergeQueueState: "merging",
          mergeQueueNote: "merging this change now",
          mergeQueueStateAt: at(20 * 60_000),
          activity: "pushing the branch",
          activityAt: at(60_000),
        },
        now,
      ),
    ).toEqual({
      note: "pushing the branch",
      age: null,
      since: "1m",
      bySetup: false,
    });
  });

  // The same for the states the queue writes *about* a run: the badge
  // beside the row already says "Repairing" (stateLabel), so the line has
  // room to say what the repair is actually doing.
  it("prefers the agent's line to the queue's during a repair", () => {
    expect(
      runActivity(
        {
          state: "running",
          repairing: true,
          mergeQueueState: "repairing",
          activity: "merging main into the branch",
          activityAt: at(60_000),
        },
        now,
      ).note,
    ).toBe("merging main into the branch");
  });

  // Only where there is a live run's line to prefer. A run that has not
  // said anything yet, and a task still queued behind grain's own setup,
  // both leave the queue's phrase as the truest thing on the row.
  it("keeps the queue's phrase where no live run is saying anything", () => {
    expect(
      runActivity({ state: "running", mergeQueueState: "repairing" }, now).note,
    ).toBe("Resolving Conflicts");
    expect(
      runActivity(
        {
          state: "queued",
          mergeQueueState: "repair_queued",
          activity: "building a sandbox",
          activityAt: at(60_000),
          activityBySetup: true,
        },
        now,
      ).note,
    ).toBe("Queued for repair");
  });

  // A row written before the timestamp column existed has a note and no
  // time; showing the note alone beats inventing an age for it.
  it("shows a note with no timestamp on its own", () => {
    expect(
      runActivity({ state: "running", activity: "reading the code" }, now),
    ).toEqual({
      note: "reading the code",
      age: null,
      since: null,
      bySetup: false,
    });
  });
});

// The hover is where "who is saying this, and since when?" is asked
// outright, so it answers in full even where the row has decided the age
// is not worth the width (grain/task-204).
describe("activityTitle", () => {
  it("dates a phrase the row leaves undated", () => {
    expect(
      activityTitle({ note: "reading the code", age: null, since: "2m" }),
    ).toBe("What this run says it is doing, as of 2m ago");
  });

  it("says just now for a phrase written in the last minute", () => {
    expect(
      activityTitle({ note: "reading the code", age: null, since: "now" }),
    ).toBe("What this run says it is doing, as of just now");
  });

  it("names the voice, and puts the queue's own reason first", () => {
    expect(
      activityTitle({
        note: "Merge failed",
        detail: "GitHub has refused this merge 12 times",
        age: "3h ago",
        since: "3h",
        bySetup: true,
        byQueue: true,
      }),
    ).toBe(
      "GitHub has refused this merge 12 times\n\nWhat grain's merge queue is doing about landing this change, as of 3h ago",
    );
  });

  // A row written before the timestamp column existed has nothing to
  // date, and an invented age is worse than none.
  it("leaves out the age of a phrase with no timestamp", () => {
    expect(
      activityTitle({ note: "reading the code", age: null, since: null }),
    ).toBe("What this run says it is doing");
  });
});

describe("relativeAge", () => {
  const now = Date.UTC(2026, 8, 4, 12, 0, 0);
  const ago = (ms) => new Date(now - ms).toISOString();

  it("is coarse on purpose: now, minutes, hours, days", () => {
    expect(relativeAge(ago(5_000), now)).toBe("now");
    expect(relativeAge(ago(90_000), now)).toBe("1m");
    expect(relativeAge(ago(3 * 3_600_000), now)).toBe("3h");
    expect(relativeAge(ago(50 * 3_600_000), now)).toBe("2d");
  });

  // A browser clock a second ahead of the daemon's is not worth showing
  // anybody a negative age over.
  it("never renders a future timestamp as negative", () => {
    expect(relativeAge(new Date(now + 5_000).toISOString(), now)).toBe("now");
  });
});

// grain/task-1549: a run that ended on nothing_needed is labelled apart
// from both a success and a run that produced nothing.
describe("outcomeLabel", () => {
  it("labels a nothing_needed run apart from succeeded and no_action", () => {
    expect(outcomeLabel("nothing_needed")).toBe("Nothing needed");
    expect(outcomeLabel("succeeded")).toBe("Succeeded");
    expect(outcomeLabel("no_action")).toBe("No action");
  });
});

describe("mergeQueueBatching", () => {
  it("returns true for an overlap re-check in waiting_ci and false for ordinary waiting_ci", () => {
    expect(
      mergeQueueBatching({
        state: "completed",
        mergeQueueState: "waiting_ci",
        mergeQueueNote:
          "grain CI is re-checking this change on top of main before it merges, on `grain-merge-queue/dep/main` at abcdef012345",
      }),
    ).toBe(true);
    expect(
      mergeQueueBatching({
        state: "completed",
        mergeQueueState: "waiting_ci",
        mergeQueueNote: "grain CI run 42 is running",
      }),
    ).toBe(false);
    expect(
      mergeQueueBatching({
        state: "closed",
        mergeQueueState: "waiting_ci",
        mergeQueueNote:
          "grain CI is re-checking this change on top of main before it merges",
      }),
    ).toBe(false);
  });
});

describe("connectionPause and taskPause", () => {
  const entry = (connection, team, extra = {}) => ({
    connection,
    team,
    framework: "claude",
    until: "2026-10-04T22:58:16Z",
    ...extra,
  });
  const pause = {
    paused: true,
    until: "2026-10-04T22:58:16Z",
    pauses: [entry("alpha-a", "alpha")],
  };

  it("finds a connection's pause by the connection owning its key", () => {
    expect(connectionPause(pause, { id: "alpha-a", team: "alpha" })).toBe(
      pause.pauses[0],
    );
    // Borrowing alpha-a's key, so paused with it.
    expect(
      connectionPause(pause, {
        id: "alpha-borrow",
        team: "alpha",
        credentialBackendId: "alpha-a",
      }),
    ).toBe(pause.pauses[0]);
    expect(connectionPause(pause, { id: "alpha-b", team: "alpha" })).toBeNull();
    // Another team's connection of the same id is not the same connection.
    expect(connectionPause(pause, { id: "alpha-a", team: "beta" })).toBeNull();
  });

  it("covers nothing when nothing is paused", () => {
    expect(connectionPause(null, { id: "alpha-a" })).toBeNull();
    expect(connectionPause({ paused: false }, { id: "alpha-a" })).toBeNull();
    expect(taskPause({ team: "alpha" }, {})).toBeNull();
  });

  it("holds a task only while nothing its team could run it on is free", () => {
    const config = {
      agentFramework: "claude",
      agentPause: pause,
      agentBackends: [
        { id: "alpha-a", framework: "claude", team: "alpha" },
        { id: "alpha-b", framework: "claude", team: "alpha" },
        { id: "beta-a", framework: "claude", team: "beta" },
      ],
    };
    // alpha-b is free, so the rotation runs it there.
    expect(taskPause({ team: "alpha" }, config)).toBeNull();
    // Pinned to the paused connection: held.
    expect(
      taskPause({ team: "alpha", agentBackendId: "alpha-a" }, config),
    ).toBe(pause.pauses[0]);
    // Another team is never held by alpha's pause.
    expect(taskPause({ team: "beta" }, config)).toBeNull();
    // Every alpha connection paused: held until the first lifts.
    const both = {
      ...config,
      agentPause: {
        ...pause,
        pauses: [
          entry("alpha-a", "alpha", { until: "2026-10-04T23:00:00Z" }),
          entry("alpha-b", "alpha", { until: "2026-10-04T21:00:00Z" }),
        ],
      },
    };
    expect(taskPause({ team: "alpha" }, both)?.connection).toBe("alpha-b");
  });

  it("holds only the paused model's tasks on a connection", () => {
    const proPause = {
      paused: true,
      pauses: [
        entry("alpha-a", "alpha", {
          model: "gemini-3-pro",
          labels: ["auto"],
        }),
      ],
    };
    const conn = { id: "alpha-a", team: "alpha", framework: "antigravity" };
    // One model's pause leaves the connection serving its others.
    expect(connectionPause(proPause, conn)).toBeNull();
    expect(connectionPause(proPause, conn, "gemini-3-pro")).toBe(
      proPause.pauses[0],
    );
    expect(connectionPause(proPause, conn, "Gemini-3-Pro")).toBe(
      proPause.pauses[0],
    );
    expect(connectionPause(proPause, conn, "auto")).toBe(proPause.pauses[0]);
    expect(connectionPause(proPause, conn, "gemini-3-flash")).toBeNull();
    expect(modelPauses(proPause, conn)).toEqual(proPause.pauses);
    expect(modelPause(proPause, conn, "gemini-3-flash")).toBeNull();

    const config = {
      agentPause: proPause,
      agentBackends: [{ ...conn, models: ["gemini-3-pro", "gemini-3-flash"] }],
      deployment: { id: "default", defaultModel: "gemini-3-flash" },
    };
    expect(taskPause({ team: "alpha", model: "gemini-3-pro" }, config)).toBe(
      proPause.pauses[0],
    );
    expect(
      taskPause({ team: "alpha", model: "gemini-3-flash" }, config),
    ).toBeNull();
    // No model of its own: the deployment's default, which is not paused.
    expect(taskPause({ team: "alpha" }, config)).toBeNull();
    expect(
      taskPause(
        { team: "alpha", agentBackendId: "alpha-a", model: "gemini-3-pro" },
        config,
      ),
    ).toBe(proPause.pauses[0]);
  });

  it("holds a team's runs on no connection by team and framework", () => {
    const config = {
      agentFramework: "claude",
      agentPause: {
        paused: true,
        pauses: [
          {
            team: "default",
            framework: "claude",
            until: "2026-10-04T22:58:16Z",
          },
        ],
      },
    };
    expect(taskPause({}, config)).toBe(config.agentPause.pauses[0]);
    expect(taskPause({ agentFramework: "codex" }, config)).toBeNull();
    expect(taskPause({ team: "beta" }, config)).toBeNull();
  });
});

// grain/task-4320: a connection with no saved models advertises what its
// provider last reported, minus its exclusions -- mirroring
// model.AgentBackend.SupportedModels.
// grain/task-4527: what a connection runs while nothing chose a model --
// model.AgentBackend.EffectiveModel.
describe("providerDefaultModel", () => {
  it("is auto for agy, the first listed model otherwise, and a pin over both", () => {
    const discovered = { models: ["m-1", "m-2"] };
    expect(providerDefaultModel({ framework: "antigravity", discovered })).toBe(
      "auto",
    );
    expect(providerDefaultModel({ framework: "codex", discovered })).toBe(
      "m-1",
    );
    expect(
      providerDefaultModel({ framework: "antigravity", models: ["pinned"] }),
    ).toBe("pinned");
  });
  it("is empty for a connection with no models", () => {
    expect(providerDefaultModel({ framework: "antigravity" })).toBe("");
    expect(providerDefaultModel({ framework: "custom", models: [] })).toBe("");
  });
  it("labels the team default as unset when there is none", () => {
    expect(teamDefaultLabel("")).toBe(
      "Deployment default (provider default (unset))",
    );
    expect(teamDefaultLabel("m-1")).toBe("Deployment default (m-1)");
    expect(teamDefaultLabel("m-1, m-2")).toBe("Deployment default (m-1 → m-2)");
    expect(teamDefaultLabel({ defaultModels: ["m-1", "m-2"] })).toBe(
      "Deployment default (m-1 → m-2)",
    );
  });

  it("parses multiple default models and falls back when the first is paused", () => {
    expect(
      defaultModels(" gemini-3-pro , gemini-3-flash , GEMINI-3-PRO "),
    ).toEqual(["gemini-3-pro", "gemini-3-flash"]);
    const conn = {
      id: "alpha-a",
      team: "alpha",
      framework: "antigravity",
      models: ["gemini-3-pro", "gemini-3-flash"],
    };
    const proPause = {
      paused: true,
      pauses: [
        {
          connection: "alpha-a",
          team: "alpha",
          framework: "antigravity",
          model: "gemini-3-pro",
          until: "2026-10-04T23:00:00Z",
        },
      ],
    };
    const config = {
      agentPause: proPause,
      agentBackends: [conn],
      deployment: {
        id: "default",
        defaultModel: "gemini-3-pro,gemini-3-flash",
      },
    };
    expect(taskModelOn({ team: "alpha" }, conn, config)).toBe("gemini-3-flash");
    expect(taskPause({ team: "alpha" }, config)).toBeNull();
    expect(
      connectionsServingModels(
        [
          { id: "gemini-3-pro", connections: [{ id: "c1" }] },
          { id: "claude-opus-5", connections: [{ id: "c2" }] },
        ],
        [{ id: "c1" }, { id: "c2" }, { id: "c3" }],
        ["gemini-3-pro", "claude-opus-5"],
      ),
    ).toEqual([{ id: "c1" }, { id: "c2" }]);
  });
  // 2026-10-23 recurring/composed walkthrough: with the deployment's only
  // connection disabled, every explicit model in the picker said
  // "(disabled connections only)" while the default entry -- the same
  // model, and the one left selected -- read as servable.
  it("says when only disabled connections serve the team default", () => {
    const models = connectionModels([
      {
        id: "a",
        framework: "antigravity",
        models: ["m-1", "m-2"],
        disabled: true,
      },
      { id: "b", framework: "antigravity", models: ["m-2"] },
    ]);
    expect(teamDefaultLabel("m-1", models)).toBe(
      "Deployment default (m-1, disabled connections only)",
    );
    expect(teamDefaultLabel("m-2", models)).toBe("Deployment default (m-2)");
    // A default no connection lists (a provider's "auto") is not flagged.
    expect(teamDefaultLabel("auto", models)).toBe("Deployment default (auto)");
    // A fallback chain flags only its disabled-only links, or the whole
    // chain when every link is.
    expect(teamDefaultLabel("m-1, m-2", models)).toBe(
      "Deployment default (m-1 [disabled connections only] → m-2)",
    );
    expect(teamDefaultLabel({ defaultModels: ["m-1"] }, models)).toBe(
      "Deployment default (m-1, disabled connections only)",
    );
  });
});

describe("advertisedModels", () => {
  const discovered = { models: ["claude-opus-5-5", "claude-haiku-4-5"] };
  it("advertises the discovered list minus exclusions", () => {
    expect(
      advertisedModels({
        framework: "claude",
        discovered,
        excludedModels: ["claude-haiku-4-5"],
      }),
    ).toEqual(["claude-opus-5-5"]);
  });
  it("keeps a pinned list over what the provider reports", () => {
    expect(
      advertisedModels({
        framework: "claude",
        models: ["claude-x"],
        discovered,
      }),
    ).toEqual(["claude-x"]);
  });
  // grain/task-4527: no hardcoded model name stands in for a provider
  // that has not answered.
  it("advertises nothing before the provider answers", () => {
    for (const framework of ["claude", "codex", "antigravity", "gemini"]) {
      expect(advertisedModels({ framework })).toEqual([]);
    }
  });
  it("never reads a discovered list for a custom command", () => {
    expect(
      advertisedModels({ framework: "custom", models: [], discovered }),
    ).toEqual([]);
  });
});

describe("liveness sub-cases", () => {
  it("names a sub-case after its state, in words", () => {
    expect(reasonLabel({ reason: "queued/usage_limit_paused" })).toBe(
      "usage limit paused",
    );
    expect(reasonLabel(null)).toBe(null);
  });

  it("explains which condition a sub-case is in and why", () => {
    expect(
      reasonTitle({
        reason: "running/live_run",
        class: "driven",
        driver: "ProcessResult",
        resource: "the run in flight",
        control: "Cancel",
      }),
    ).toBe(
      'running/live_run\nDriven by ProcessResult\nWaiting on the run in flight\n"Cancel" ends this wait',
    );
    expect(
      reasonTitle({
        reason: "proposed/unapproved",
        class: "awaiting a person",
      }),
    ).toBe("proposed/unapproved\nAwaiting a person");
  });

  it("offers the control a row can press, the merge lane's where the task has none", () => {
    expect(
      reasonControl({
        id: "3",
        reason: { reason: "deferred/wake_time", control: "Undefer" },
      }),
    ).toMatchObject({ label: "Undefer", post: "/undefer" });
    expect(
      reasonControl({
        id: "3",
        reason: { reason: "completed/on_merge_queue" },
        mergeQueueReason: {
          reason: "merge_failed/given_up",
          control: "Retry merge",
        },
      }),
    ).toMatchObject({ label: "Retry merge", post: "/retry-merge" });
    expect(
      reasonControl({
        id: "3",
        reason: { reason: "proposed/unapproved", control: "Approve" },
      }),
    ).toBe(null);
  });

  it("carries the merge-queue sub-case on the queue's activity", () => {
    const r = {
      reason: "waiting_ci/grain_running",
      class: "driven",
      driver: "reconcileTaskCI",
    };
    const a = runActivity({
      state: "completed",
      mergeQueueState: "waiting_ci",
      mergeQueueReason: r,
    });
    expect(a.reason).toBe(r);
    expect(activityTitle(a)).toContain("Driven by reconcileTaskCI");
  });
});

describe("blocked state badge", () => {
  const byId = new Map([[7, { id: 7, state: "running" }]]);

  it("wears the blocked oval only while the task is still waiting", () => {
    for (const state of ["queued", "proposed"]) {
      const t = { state, blocked: true, blockedBy: [7] };
      expect(isBlockedWaiting(t)).toBe(true);
      expect(taskBadgeState(t)).toBe("blocked");
      expect(stateLabel(t)).toBe("Blocked");
    }
    for (const state of [
      "running",
      "completed",
      "failed",
      "awaiting_reply",
      "closed",
      "deferred",
    ]) {
      const t = { state, blocked: true, blockedBy: [7] };
      expect(isBlockedWaiting(t)).toBe(false);
      expect(taskBadgeState(t)).toBe(state);
    }
    expect(taskBadgeState({ state: "queued", blocked: false })).toBe("queued");
  });

  it("lets grain's own work, a merge and a repair win over blocked", () => {
    expect(
      taskBadgeState({
        state: "queued",
        blocked: true,
        prMergedAt: "2026-01-01T00:00:00Z",
      }),
    ).toBe("merged");
    expect(
      taskBadgeState({ state: "queued", blocked: true, repairing: true }),
    ).toBe("queued");
    expect(
      stateLabel({ state: "queued", blocked: true, repairing: true }),
    ).toBe("Waiting for repair");
  });

  it("titles the oval with what the task is blocked or waiting on", () => {
    expect(
      stateTitle({ state: "queued", blocked: true, blockedBy: [7] }, byId),
    ).toBe("Blocked on: #7 (running)");
    expect(
      stateTitle(
        { state: "queued", blocked: true, parked: true, blockedBy: [7] },
        byId,
      ),
    ).toBe("Waiting on: #7 (running)");
    expect(
      stateTitle({ state: "proposed", blocked: true, blockedBy: [7] }),
    ).toBe("Blocked on: #7");
    expect(stateTitle({ state: "queued", blocked: true })).toBe("Blocked");
  });

  it("names a leftover open link after the label of a task not shown blocked", () => {
    expect(
      stateTitle({ state: "failed", blocked: true, blockedBy: [7] }, byId),
    ).toBe("Failed — Blocked on: #7 (running)");
    expect(
      stateTitle({ state: "running", blocked: true, blockedBy: [7] }, byId),
    ).toBe("Running");
    expect(stateTitle({ state: "failed", blocked: true })).toBe("Failed");
    expect(
      stateTitle({
        state: "completed",
        blocked: true,
        blockedBy: [7],
        prMergedAt: "2026-01-01T00:00:00Z",
      }),
    ).toBe("Merged");
  });
});

// "Asked" is one label for awaiting_reply and awaiting_submit, so the
// tooltip and the action say which ask it is (grain/task-1238).
describe("askedCase", () => {
  it("is nothing for a task that is not asked", () => {
    expect(askedCase({ state: "queued" })).toBeNull();
    expect(askedCase(null)).toBeNull();
  });

  it("names each case with its tooltip and its action", () => {
    expect(askedCase({ state: "awaiting_reply" })).toEqual({
      kind: "question",
      title: "Asked a question",
      action: "reply",
    });
    expect(askedCase({ state: "awaiting_reply", interactive: true })).toEqual({
      kind: "message",
      title: "Waiting for your message",
      action: "reply",
    });
    expect(
      askedCase({ state: "awaiting_reply", consoleSession: "s1" }).kind,
    ).toBe("message");
    expect(
      askedCase({
        state: "awaiting_reply",
        interactive: true,
        turnStopped: true,
      }),
    ).toEqual({ kind: "stopped", title: "Stopped by you", action: "reply" });
    expect(
      askedCase({ state: "awaiting_submit", pullRequest: "acme/widgets#12" }),
    ).toEqual({
      kind: "submit",
      title: "Asked you to submit acme/widgets#12",
      action: "submit",
    });
  });

  it("says what holds a change waiting on Submit", () => {
    expect(
      askedCase({
        state: "awaiting_submit",
        pullRequest: "acme/widgets#12",
        securityHold: "touches .github/workflows",
        securityReviewTask: "40",
      }).title,
    ).toBe(
      "Asked you to submit acme/widgets#12 — on security hold (touches .github/workflows) for security review #40",
    );
    expect(
      askedCase({
        state: "awaiting_submit",
        pullRequest: "acme/widgets#12",
        pathTriggerHold: "infra/**",
      }).title,
    ).toBe(
      "Asked you to submit acme/widgets#12 — held by repository gating triggers (human: infra/**)",
    );
  });

  it("is the asked chip's tooltip, under the one Asked label", () => {
    const t = { state: "awaiting_reply", turnStopped: true };
    expect(stateLabel(t)).toBe("Asked");
    expect(stateTitle(t)).toBe("Stopped by you");
    expect(stateLabel({ state: "awaiting_submit" })).toBe("Asked");
    expect(stateLabel({ state: "queued" })).toBe("Waiting");
  });
});

describe("the asked display state", () => {
  it("folds both asked states into one", () => {
    expect(ASKED_STATES.map(displayState)).toEqual(["asked", "asked"]);
    expect(displayState("queued")).toBe("queued");
    expect(statesOf("asked")).toEqual(["awaiting_reply", "awaiting_submit"]);
    expect(statesOf("awaiting_reply")).toEqual(["awaiting_reply"]);
    expect(DISPLAY_STATE_ORDER).toContain("asked");
    expect(DISPLAY_STATE_ORDER).not.toContain("awaiting_reply");
    expect(DISPLAY_STATE_ORDER).not.toContain("awaiting_submit");
  });

  it("sums both asked states' counts into one legend entry", () => {
    expect(
      displayCounts({ queued: 2, awaiting_reply: 1, awaiting_submit: 3 }),
    ).toEqual([
      { state: "queued", count: 2 },
      { state: "asked", count: 4 },
    ]);
    expect(
      displayCounts([
        { state: "awaiting_submit", count: 1 },
        { state: "running", count: 1 },
      ]),
    ).toEqual([
      { state: "running", count: 1 },
      { state: "asked", count: 1 },
    ]);
  });
});
