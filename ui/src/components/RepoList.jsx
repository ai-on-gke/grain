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

import { useState, useEffect } from "react";
import {
  Alert,
  Autocomplete,
  Button,
  Chip,
  Link,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { buildPath } from "../paths.js";
import {
  ACTIVITY_RANK,
  STATE_LABELS,
  displayCounts,
  repoActivity,
  repoRows,
} from "../state.js";
import { useListOrder } from "../listOrder.js";
import {
  focusRowLink,
  ListEmpty,
  ListHeader,
  ListSearchField,
  ListSortSelect,
  ListToolbar,
  ReorderableList,
  RowLink,
} from "./ListPrimitives.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import { identifierField } from "../identifierField.js";
import NoGitConnectionNotice from "./NoGitConnectionNotice.jsx";
import { useInPageHeader } from "./PageHeader.jsx";
import ChipStrip from "./ChipStrip.jsx";

// SORTS is this page's own toolbar Select, TaskList's own shape
// (taskFilters.js) with "custom" standing where the task list's
// "Backlog order" does: a repo has no backlog position, so the order a
// drag stores here is this browser's own display order and nothing more
// (listOrder.js). It is still the default, because a list nobody has
// dragged is in the alphabetical order it always was -- applyOrder falls
// back to exactly the comparator "Name (A–Z)" uses -- so the only thing
// the default costs is the handles that make the order draggable.
const byName = (a, b) => a.repo.localeCompare(b.repo);
const OTHER = "Other…";

// repoHostOf is the git host a typed repo names -- "git.example.com"
// for git.example.com/team/repo, the host of a URL or scp-style
// remote -- lowercased, or "" for a bare owner/name, which is the
// deployment's default host. It is model.ParseRepo's host rule, read only
// as far as deciding whether the Clone URL field is needed.
export function repoHostOf(text) {
  let spec = (text || "").trim();
  if (spec.includes("://")) {
    spec = spec.slice(spec.indexOf("://") + 3);
    const authority = spec.split("/")[0];
    return authority.slice(authority.lastIndexOf("@") + 1).toLowerCase();
  }
  const at = spec.indexOf("@");
  if (at >= 0 && spec.indexOf(":", at) >= 0) {
    return spec.slice(at + 1, spec.indexOf(":", at)).toLowerCase();
  }
  const [first, ...rest] = spec.split("/");
  if (rest.length > 0 && /[.:]/.test(first)) return first.toLowerCase();
  return "";
}

// discoveryStatus is the line under the Add repo picker: what
// GET /api/deployment/reachable-repos found and from how many of the
// team's Git connections, or why it found nothing -- so a picker with no
// options says why rather than passing for a plain text box. kind is
// "loading", "found", "none" or "error"; failures is the connections
// that could not be listed -- a lapsed login to the host's front end says
// what to run -- each a sentence, shown under any of the three.
export function discoveryStatus(reach) {
  if (!reach || reach.loading) {
    return { kind: "loading", text: "Discovering repositories…", failures: [] };
  }
  if (reach.requestError) {
    return {
      kind: "error",
      text: `Couldn't discover repositories: ${reach.requestError}`,
      failures: [],
    };
  }
  const conns = Array.isArray(reach.connections) ? reach.connections : [];
  const failures = conns
    .filter((c) => !c.ok)
    .map((c) => `${c.credential} on ${c.host}: ${c.error || "not listed"}`);
  const repos = Array.isArray(reach.repos) ? reach.repos : [];
  if (reach.available && repos.length > 0) {
    const listed = conns.filter((c) => c.ok).length || 1;
    return {
      kind: "found",
      text:
        `${repos.length} repo${repos.length === 1 ? "" : "s"} discovered from ` +
        `${listed} Git connection${listed === 1 ? "" : "s"}`,
      failures,
    };
  }
  if (!reach.available && conns.some((c) => !c.ok && c.error)) {
    return {
      kind: "error",
      text: "Couldn't discover repositories from the deployment's Git connections",
      failures,
    };
  }
  return {
    kind: "none",
    text: "No repositories discovered",
    reason: reach.available ? "" : reach.reason || "",
    failures,
  };
}

const SORTS = {
  custom: { label: "Custom order", cmp: byName },
  name: { label: "Name (A–Z)", cmp: byName },
  active: {
    label: "Active first",
    cmp: (a, b) =>
      ACTIVITY_RANK[repoActivity(a).key] - ACTIVITY_RANK[repoActivity(b).key] ||
      byName(a, b),
  },
  tasks: {
    label: "Most tasks",
    cmp: (a, b) => b.total - a.total || byName(a, b),
  },
};

// RepoList is the repo index: one row per known repo -- every
// config.targetRepos entry, plus any repo tasks target that isn't one,
// plus any repo carrying configuration of its own, which today is a
// default capability set, standing instructions, or both (repoRows has
// why all three sources) -- each showing how many tasks sit in every state so a
// repo with something stuck (awaiting_reply, or a pile of blocked work)
// stands out before anyone opens it. Clicking a row opens that repo's
// own page (RepoPage, grain/task-111), which is where everything about
// one repo now lives.
//
// A row is a name and its counts, and nothing else. It used to carry a
// chevron that folded the repo's tasks out in place plus four buttons --
// New branch, Capabilities, Releases, Remove -- and a "+" for filing a
// task, each with its own stopPropagation so it wouldn't also open the
// row, and two of them folding a form out between rows (bwsalmon/agents
// #459, #473, #474, #638). All of it moved onto the repo page: a list of
// repos is for finding a repo, and each of those controls is about one
// repo, which is a thing that now has a page of its own to be on.
//
// Adding a target repo (bwsalmon/agents#473) stays here, since it is the
// one control that isn't about a repo this page is already listing --
// Remove, its counterpart, lives on the repo page beside everything else
// about that repo.
//
// "Add repo" reads backwards the first time (grain/task-45). An empty
// config.targetRepos means *unrestricted* everywhere it appears --
// CreateTask enforces nothing, `grain settings` prints "unrestricted",
// every row here is `configured: false` -- so adding the first repo does
// not widen anything, it narrows the deployment to exactly that repo,
// and the next task filed against any other repo parks off the allowlist
// instead of dispatching. Two things say so, one before the click and
// one at it: the note below the header stands on the page for as long
// as the allowlist is empty (and, once it holds one entry, says that
// too -- keyed on the list rather than on the transition, the way `grain
// repo add`'s own one-line note is), and addRepo confirms the first add
// naming the repos that would fall off. The confirmation is the one that
// can be specific, since only it knows which repo is being added; the
// standing note is what somebody reads while deciding to click at all.
//
// The header/toolbar/row shape below mirrors TaskList's own
// (bwsalmon/agents#561): a .content-header with title, count, and this
// page's own primary action (the add-repo form, in place of TaskList's
// filter title or TemplatesList/SchedulesList's "+ New X" button) above
// a .task-list-toolbar search box, then flat divider rows -- so the four
// list pages read as one design instead of four.
//
// The add form's name field is a picker over what the team's Git
// connections reach (GET /api/deployment/reachable-repos), and says so:
// it carries a dropdown arrow and opens on focus whatever it holds, and
// the line under it says how many repos discovery found from how many
// connections -- or that it found none, linking to where connections
// are set up, or which connection failed and why. A discovered repo
// carries its own host (git.example.com/team/repo) and is cloned
// through the connection that found it, so the Clone URL field is only
// offered for "Other…" or a name on a host none of the team's
// connections covers. "Your repos" heads the list below, whose search
// box filters what is already added and is not part of the form.
export default function RepoList({
  tasks,
  config,
  onOpenRepo,
  onOpenSettings,
  onRefreshConfig,
  showError,
}) {
  const [newRepo, setNewRepo] = useState("");
  const [cloneUrl, setCloneUrl] = useState("");
  const [other, setOther] = useState(false);
  const [search, setSearch] = useState("");
  const [sortBy, setSortBy] = useState("custom");
  const [reach, setReach] = useState({ loading: true });

  useEffect(() => {
    let live = true;
    setReach({ loading: true });
    Promise.resolve()
      .then(() => api("/api/deployment/reachable-repos"))
      .then((res) => {
        if (live) setReach({ loading: false, ...(res || {}) });
      })
      .catch((err) => {
        if (live) {
          setReach({
            loading: false,
            requestError: err?.message || String(err),
          });
        }
      });
    return () => {
      live = false;
    };
  }, []);

  const reachableRepos =
    reach.available && Array.isArray(reach.repos)
      ? reach.repos.map((r) => r.repo).filter(Boolean)
      : [];
  const repoOptions = [...reachableRepos, OTHER];
  const status = discoveryStatus(reach);
  const inRail = useInPageHeader();
  // The hosts a typed name may name without a clone URL: the default
  // host, every host one of the team's connections is on, and every
  // host a discovered repo is on.
  const coveredHosts = new Set(["github.com"]);
  for (const c of reach.connections || []) {
    if (c.host) coveredHosts.add(c.host.toLowerCase());
  }
  for (const r of reachableRepos) {
    const h = repoHostOf(r);
    if (h) coveredHosts.add(h);
  }
  const typedHost = repoHostOf(newRepo);
  const needsCloneUrl =
    other || (typedHost !== "" && !coveredHosts.has(typedHost));
  const settingsHref = buildPath({ view: "settings", settingsSection: "git" });
  const repos = repoRows(config, tasks);
  // Every repo, not just the ones the search leaves showing -- what a
  // drag stores has to know where the hidden rows sit too (listOrder.js).
  const [ordered, move] = useListOrder("repos", repos, (r) => r.repo, byName);

  const q = search.trim().toLowerCase();
  const sorted =
    sortBy === "custom" ? ordered : [...repos].sort(SORTS[sortBy].cmp);
  const visible = sorted.filter(
    (r) => q === "" || r.repo.toLowerCase().includes(q),
  );
  const targetRepos = config?.targetRepos || [];

  // firstAddWarning is what the confirmation says before the first repo
  // is added to an empty (== unrestricted) allowlist, or null when this
  // add can only ever widen an allowlist that already restricts.
  //
  // The repos it names are the rows this page is already showing. With
  // targetRepos empty none of them is `configured`, so every one is here
  // because a task targets it or because it carries default capabilities
  // of its own -- repoRows' other two sources -- and either way the
  // allowlist this click creates leaves it off, so the next task filed
  // against it parks. Worth listing, since that is the cost of the click
  // and nothing else on the page states it.
  //
  // Compared as typed rather than as the server will store it
  // (AddTargetRepo runs the string through model.ParseRepo first), so an
  // oddly-spelled repo may list itself among the fallers; the sentence
  // is still true, and the alternative is a second parse here that could
  // drift from that one.
  const firstAddWarning = (repo) => {
    if (targetRepos.length > 0) return null;
    const falling = repos.map((r) => r.repo).filter((r) => r !== repo);
    let msg =
      `Add ${repo} as the only repo this deployment allows?\n\n` +
      "This deployment doesn't restrict target repos today -- an empty allowlist is what means " +
      `unrestricted -- so adding ${repo} narrows it to that one repo rather than widening anything.`;
    if (falling.length > 0) {
      msg +=
        `\n\nOff the allowlist as of this click: ${falling.join(", ")}. ` +
        "Tasks already filed against them keep their target, but each one is parked instead of " +
        "dispatched until the repo is added back here.";
    }
    return msg;
  };

  // strandedByAdding is what the work already stored loses to this click,
  // in the daemon's own words: POST /api/settings/plan with the list this
  // add would write answers with the tasks, schedules and qualification
  // plans left behind in the repos it leaves out
  // (ui.Settings.TargetReposStranded, grain/task-411).
  //
  // The sentence above says which repos fall off, which this page can
  // work out for itself; how much work falls with them is the store's to
  // count, and asking is the only way to say it. Empty when the plan
  // cannot be had -- an older daemon has no such route, and a deployment
  // whose settings have never been saved cannot be planned against --
  // which costs the confirmation the paragraph and nothing else.
  const strandedByAdding = async (repo) => {
    try {
      const plan = await api("/api/settings/plan", {
        method: "POST",
        body: JSON.stringify({ targetRepos: [...targetRepos, repo] }),
      });
      return (plan?.targetReposStranded || []).map((s) => s.summary);
    } catch {
      return [];
    }
  };

  const addRepo = async (evt) => {
    evt.preventDefault();
    const repo = newRepo.trim();
    if (repo === "") return;
    let warning = firstAddWarning(repo);
    if (warning !== null) {
      const stranded = await strandedByAdding(repo);
      if (stranded.length > 0) warning += `\n\n${stranded.join("\n\n")}`;
    }
    if (warning !== null && !confirm(warning)) return;
    try {
      await api("/api/repos", {
        method: "POST",
        body: JSON.stringify({
          repo,
          ...(needsCloneUrl && cloneUrl.trim()
            ? { cloneUrl: cloneUrl.trim() }
            : {}),
        }),
      });
      setNewRepo("");
      setCloneUrl("");
      setOther(false);
      await onRefreshConfig();
    } catch (err) {
      showError(err);
    }
  };

  // What discovery found, under the picker. In the header rail the
  // picker is one row of the rail and has no room under it, so the
  // caption is drawn at the top of the page instead (PageHeader.jsx).
  const discoveryCaption = (
    <Typography
      variant="caption"
      color={status.kind === "error" ? "error" : "text.secondary"}
      data-testid="repo-discovery-status"
      data-status={status.kind}
      role="status"
      sx={{
        maxWidth: 560,
        ...(inRail ? { mx: "1.75rem", mt: 2, display: "block" } : {}),
      }}
    >
      {status.text}
      {status.kind === "none" && (
        <>
          {" — add or check a "}
          <Link
            href={settingsHref}
            onClick={(evt) => {
              if (
                !onOpenSettings ||
                evt.button !== 0 ||
                evt.metaKey ||
                evt.ctrlKey ||
                evt.shiftKey ||
                evt.altKey
              )
                return;
              evt.preventDefault();
              onOpenSettings("git");
            }}
          >
            Git connection in Settings
          </Link>
          {status.reason ? ` (${status.reason})` : ""}
        </>
      )}
      {status.failures.map((f) => (
        <span key={f} style={{ display: "block" }}>
          {f}
        </span>
      ))}
    </Typography>
  );

  return (
    <main>
      <ListHeader
        title="Repos"
        icon={<ItemGlyph kind="repos" size={20} />}
        count={visible.length}
        style={{ alignItems: "center", flexWrap: "wrap" }}
        action={
          <Stack
            component="form"
            spacing={0.5}
            onSubmit={addRepo}
            // On a phone the header is drawn in the page rather than in
            // the rail's slot, and a 300px field beside the title pushed
            // Add repo past the viewport's right edge, where nothing
            // could reach it (2026-11-01 walkthrough). There the form
            // takes a line of its own and its field gives up width.
            sx={{
              ml: "auto",
              minWidth: 0,
              flexBasis: { xs: "100%", sm: "auto" },
            }}
            aria-label="Add repo"
          >
            <Stack direction="row" spacing={1} sx={{ minWidth: 0 }}>
              <Autocomplete
                freeSolo
                forcePopupIcon
                openOnFocus
                size="small"
                options={repoOptions}
                loading={reach.loading}
                loadingText="Discovering repositories…"
                filterOptions={(opts, { inputValue }) => {
                  const q = inputValue.trim().toLowerCase();
                  return opts.filter(
                    (o) => o === OTHER || o.toLowerCase().includes(q),
                  );
                }}
                value={newRepo}
                inputValue={newRepo}
                onChange={(_evt, next) => {
                  if (next === OTHER) {
                    setOther(true);
                    setNewRepo("");
                  } else {
                    setNewRepo(next || "");
                  }
                }}
                onInputChange={(_evt, next, reason) => {
                  if (reason === "input") {
                    setNewRepo(next);
                  }
                }}
                renderInput={(params) => (
                  <TextField
                    {...params}
                    placeholder={
                      other ? "host/owner/name or owner/name" : "owner/name"
                    }
                    size="small"
                    autoComplete="off"
                    inputProps={{
                      ...params.inputProps,
                      ...identifierField,
                      "aria-label": "Repo to add",
                    }}
                  />
                )}
                sx={{ minWidth: { xs: 0, sm: 300 }, flex: "1 1 auto" }}
              />
              {needsCloneUrl && (
                <TextField
                  value={cloneUrl}
                  onChange={(evt) => setCloneUrl(evt.target.value)}
                  placeholder="Clone URL (GitHub by default)"
                  size="small"
                  autoComplete="off"
                  inputProps={{ "aria-label": "Clone URL" }}
                />
              )}
              <Button
                type="submit"
                variant="outlined"
                size="small"
                sx={{ flex: "none" }}
              >
                Add repo
              </Button>
            </Stack>
            {!inRail && discoveryCaption}
          </Stack>
        }
      />
      {/* The standing half of grain/task-45's answer, keyed on the
          allowlist as it stands rather than on any transition: empty is
          the state in which "Add repo" is about to restrict rather than
          widen, and one entry is the state that leaves. Neither is an
          error or a warning -- both are working deployments -- so both
          are severity="info". */}
      {/* The Add repo picker discovers nothing for a deployment with no
          git connection, and a repo added anyway has its tasks wait: say
          so, and where to add one, rather than leave an empty picker. */}
      {inRail && discoveryCaption}
      <NoGitConnectionNotice
        deployment={config?.deployment}
        onOpenSettings={onOpenSettings}
        sx={{ mx: "1.75rem", mt: 2 }}
      />
      {targetRepos.length === 0 && (
        <Alert severity="info" sx={{ mx: "1.75rem", mt: 2 }}>
          This deployment allows a task to target any repo: an empty allowlist
          is what means unrestricted. Adding the first repo here restricts it to
          that one repo rather than widening anything, and a task filed against
          any other repo is parked instead of dispatched until that repo is
          added too.
        </Alert>
      )}
      {targetRepos.length === 1 && (
        <Alert severity="info" sx={{ mx: "1.75rem", mt: 2 }}>
          This deployment allows only {targetRepos[0]}. A task filed against any
          other repo is parked instead of dispatched until that repo is added
          here; removing this one allows any repo again.
        </Alert>
      )}
      {/* A section label, not a heading: it marks the list below as the
          repos already added, apart from the add form above it, without
          adding a second "Repos" heading beside the page's own. */}
      <Typography
        variant="subtitle2"
        component="div"
        data-testid="your-repos-label"
        sx={{ mx: "1.75rem", mt: 2 }}
      >
        Your repos
      </Typography>
      {repos.length > 0 && (
        <ListToolbar>
          <ListSearchField
            placeholder="Filter added repos…"
            value={search}
            onChange={setSearch}
          />
          <ListSortSelect
            id="repo-sort"
            value={sortBy}
            onChange={setSortBy}
            options={SORTS}
          />
        </ListToolbar>
      )}
      <ReorderableList
        className="repo-list"
        items={visible}
        idOf={(r) => r.repo}
        nameOf={(r) => r.repo}
        noun="repo"
        reorder={sortBy === "custom" ? move : null}
      >
        {(r, { handle, dragging }) => {
          // A row with no tasks, off the allowlist, is on this page only
          // because the repo carries configuration of its own -- a
          // default capability set, standing instructions, or both
          // (repoRows' third source). It has no counts to show, so
          // without saying so it would read as an empty row nobody asked
          // for -- and its page is the only way to reach what put it
          // here.
          const defaultsOnly = r.defaults && !r.configured && r.total === 0;
          const activity = repoActivity(r);
          return (
            <div
              className={`repo-list-row${dragging ? " task-row-dragging" : ""}`}
              onClick={(e) => {
                focusRowLink(e);
                onOpenRepo(r.repo);
              }}
            >
              {handle}
              {/* The row's own status, ahead of the name, where a task
                  row carries its state badge -- so the two lists' rows
                  start the same way and a repo with something happening
                  in it is visible without reading its counts
                  (grain/task-327). The same .badge figure the count
                  chips after it use, in the icon-only column width
                  .badge-icon fixes, and the same argument as those
                  chips for why "running" is the plain CSS spin rather
                  than StateDot's grain mark: this is an aggregate, not
                  one task's live status. */}
              <span
                className={`badge badge-icon badge-${activity.state} repo-status repo-status-${activity.key}`}
                title={activity.title}
                aria-label={activity.title}
                role="img"
              />
              {/* title: a long owner/name is cut to an ellipsis on a narrow
                  row (style.css .repo-list-name), and hovering is the only
                  way to read the rest of it without opening the repo. */}
              <RowLink
                className="repo-list-name"
                title={r.repo}
                href={`/repos/${r.repo}`}
                onOpen={() => onOpenRepo(r.repo)}
              >
                {r.repo}
              </RowLink>
              <ChipStrip>
                {/* Each chip here counts a repo's tasks in one state, not one
                      task's own status, so "running" keeps the plain CSS spin
                      (style.css's .badge-running) rather than StateDot's grain
                      mark (bwsalmon/agents#586) -- there is no single task for
                      the mark to represent. */}
                {displayCounts(r.counts).map(({ state: s, count }) => (
                  <Chip
                    key={s}
                    size="small"
                    className={`badge badge-${s}`}
                    label={`${STATE_LABELS[s] || s} ${count}`}
                  />
                ))}
                {r.blocked > 0 && (
                  <Chip
                    size="small"
                    color="error"
                    label={`Blocked ${r.blocked}`}
                  />
                )}
                {defaultsOnly && (
                  <Chip
                    size="small"
                    variant="outlined"
                    label="Defaults only"
                    title="No tasks, and not on this deployment's target repos -- listed here because it has configuration of its own, which its own page edits."
                  />
                )}
              </ChipStrip>
              <Typography
                variant="caption"
                color="text.secondary"
                whiteSpace="nowrap"
              >
                {r.total} task{r.total === 1 ? "" : "s"}
              </Typography>
            </div>
          );
        }}
      </ReorderableList>
      {repos.length === 0 && (
        <ListEmpty>
          No repos yet -- add one above, or file a task with a target repo.
        </ListEmpty>
      )}
      {repos.length > 0 && visible.length === 0 && (
        <ListEmpty>No repos match your search.</ListEmpty>
      )}
    </main>
  );
}
