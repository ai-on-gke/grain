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
  Fragment,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import {
  Alert,
  Badge,
  Box,
  Button,
  Checkbox,
  Chip,
  FormControl,
  FormControlLabel,
  FormHelperText,
  InputLabel,
  Link,
  ListItemText,
  MenuItem,
  Radio,
  RadioGroup,
  Select,
  Stack,
  Tab,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Tabs,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import GrantChip from "./GrantChip.jsx";
import GrantsPicker from "./GrantsPicker.jsx";
import { localGrantHandlers } from "./capabilityMethods.js";
import { isApiGrant, withoutApiRows } from "../apiGrants.js";
import { isCapabilityRef } from "../capabilityArgs.js";
import { buildPath } from "../paths.js";
import {
  LEGACY_CAPABILITIES_HINT,
  NO_CAPABILITIES_HINT,
  STATE_LABELS,
  displayCounts,
  capabilityName,
  capabilityRows,
  capabilityUnavailableHint,
  repoRows,
  repoUrl,
  unionCapabilities,
} from "../state.js";
import BranchField from "./BranchField.jsx";
import ItemGlyph, { GlyphLabel } from "./ItemGlyph.jsx";
import MergeBatchPanel from "./MergeBatchPanel.jsx";
import RepoReleaseWorkflowSettings from "./RepoReleaseWorkflowSettings.jsx";
import RepoRuns from "./RepoRuns.jsx";
import RepoWorkOwnership from "./RepoWorkOwnership.jsx";
import { identifierField } from "../identifierField.js";
import { repoAdministered } from "../taskWrites.js";
import { declaredPushRef } from "./Declared.jsx";
import { DISCARD_PROMPT } from "./Overlay.jsx";
import { guardLeaving, leavingLosesEdits } from "../unsavedEdits.js";
import { revealFocusedTab, useRevealSelectedTab } from "../tabStrip.js";
import { repoApiRoot } from "../repoApi.js";

// latestPerName keeps the first row the branch list gives for each name.
// The list is newest first, so that is the row that says where the name
// stands now. The server adds no second row for a name it already has,
// but a store written before it stopped can still hold some, and both
// the list and the compare form's datalist are keyed by name.
// sameWorkflowPath reports whether a path typed in the form names the
// stored workflow at path: the server fills a bare file name in under
// the workflows directory, so "tests.yml" is ".grain/workflows/tests.yml".
export function sameWorkflowPath(path, typed) {
  const t = (typed || "").trim();
  return t !== "" && (path === t || path.endsWith(`/${t}`));
}

// workflowEditsUnsaved reports whether the CI tab's workflow rows hold
// something the server has not stored: a row with only one of its two
// halves, which the boxes' blur-save holds back on purpose, or a row the
// server refused. Those used to go with the page without a word -- "←
// Repos", the rail and Back asked only about the Releases tab's job boxes
// (docs/test-runs/2026-10-15-repo-tabs-standing-work.md, finding 1). An
// empty row is nothing to lose, and a stored workflow's path matches the
// bare file name typed for it (sameWorkflowPath).
export function workflowEditsUnsaved(edits, saved) {
  const rows = (edits || []).filter(
    (w) => w.path.trim() !== "" || w.text.trim() !== "",
  );
  const stored = saved || [];
  if (rows.length !== stored.length) return true;
  return rows.some(
    (row) =>
      !stored.some(
        (w) => w.text === row.text && sameWorkflowPath(w.path, row.path),
      ),
  );
}

// settleWorkflowEdits is the form's rows once a save of sent has
// returned saved: each row that is still one of the objects that save
// sent becomes the server's copy of it, and every other row -- one added
// or edited while the request was out -- is kept exactly as it is.
export function settleWorkflowEdits(current, sent, saved) {
  const sentRows = new Set(sent);
  const unclaimed = [...saved];
  return current.map((row) => {
    if (!sentRows.has(row)) return row;
    const at = unclaimed.findIndex(
      (w) => w.text === row.text && sameWorkflowPath(w.path, row.path),
    );
    if (at < 0) return row;
    const [w] = unclaimed.splice(at, 1);
    return { path: w.path, text: w.text };
  });
}

function latestPerName(branches) {
  const seen = new Set();
  return (branches || []).filter((b) => {
    if (seen.has(b.name)) return false;
    seen.add(b.name);
    return true;
  });
}

// RepoPage is one repo's own page (grain/task-111), at /repos/{owner}/
// {name} -- the repo-side counterpart of a task's own /tasks/{id} page.
// Everything that is a property of one repo lives here: its task counts,
// the branches grain has been asked to create in it or to adopt from it,
// the default capabilities every task filed against it starts with, the
// standing instructions every run against it is given, the setup command
// grain runs in every checkout it makes here, the CI workflows grain
// declares for it and what those have actually run (grain/task-245,
// grain/task-351), the way into its releases, and removing it from the
// deployment's allowlist.
//
// That is the whole point of it. Each of those used to be a button on
// the repo *list* row (bwsalmon/agents#459, #473, #474, #638), which
// left that list carrying five controls per row and folding forms out
// between rows -- so a list of repos read as a toolbar rather than as a
// list. The list is now one row per repo, name and counts, and clicking
// it lands here; a repo's controls have a page with room for them, and
// each one is a URL somebody can link to.
//
// Tasks filed against the repo live on the task list, reached through the
// count beside the repo name. Keeping that route at the top of the page
// makes the tasks discoverable without placing a second, independently
// narrowed task view below all of the repo's settings and CI forms.
// CI_MODE_ID is the CI Mode control's anchor, which an unjudged gate's
// warning at the top of the page scrolls to: on a repository grain CI is
// not enabled for, that switch is what enrols its watched branch.
const CI_MODE_ID = "repo-ci-mode";

// REPO_TABS are the page's sections, in the order its tab strip shows
// them (grain/task-3333). The page used to be one column of a dozen
// settings forms with the live state -- batches in flight, recent runs --
// at the very bottom; each tab now answers one question: is this repo
// healthy (Overview), how is it tested (CI), how does work land (Merge
// queue), what does an agent here get (Agents), and what happens after
// merge (Releases). The selected tab is in the page's path
// (/repos/acme/widgets/settings/ci, docs/design/settings-panel.md §2.3,
// paths.js's REPO_TAB_IDS), so a tab is a link somebody can share and
// Settings → Repositories can point at. It used to ride in the hash
// (/repos/acme/widgets#ci), and a link from then still opens its tab.
export const REPO_TABS = [
  { id: "overview", label: "Overview" },
  { id: "ci", label: "CI" },
  { id: "merge-queue", label: "Merge queue" },
  { id: "agents", label: "Agents" },
  { id: "branches", label: "Branches" },
  { id: "releases", label: "Releases" },
  { id: "advanced", label: "Advanced" },
];

// declaredSummary counts what a declarations status says the repository
// declares -- "1 template(s), 1 suite(s), 0 schedule(s), 0 trigger(s)" --
// with its merge rules file when it has one.
function declaredSummary(status) {
  return (
    `${(status.templates || []).length} template(s), ` +
    `${(status.suites || []).length} suite(s), ` +
    `${(status.schedules || []).length} schedule(s), ` +
    `${(status.triggers || []).length} trigger(s)` +
    (status.mergeRules ? `, merge rules in ${status.mergeRules}` : "")
  );
}

function tabFromHash(hash) {
  const id = (hash || "").replace(/^#/, "");
  return REPO_TABS.some((t) => t.id === id) ? id : "overview";
}

// RepoTabPanel keeps every tab mounted and hides the others, so a box
// half-typed on one tab is still there after a look at another, and the
// unsaved-edits guard on leaving the page still sees it.
function RepoTabPanel({ id, tab, children }) {
  return (
    <Box
      role="tabpanel"
      id={`repo-tabpanel-${id}`}
      aria-labelledby={`repo-tab-${id}`}
      hidden={tab !== id}
      sx={{
        display: tab === id ? "flex" : "none",
        flexDirection: "column",
        gap: 3,
      }}
    >
      {children}
    </Box>
  );
}

export const CI_MODE_LABELS = {
  github: "GitHub CI",
  grain: "Grain CI",
  none: "No CI",
};

// repoCIMode is the CI mode a repository's RepoDefaults says it is on,
// reading the older ignoreGitHubCI switch for a row written before
// ciMode was.
export function repoCIMode(defaults) {
  if (!defaults) return null;
  return defaults.ciMode || (defaults.ignoreGitHubCI ? "grain" : "github");
}

// RepoOverview is the at-a-glance strip at the top of the Overview tab:
// the handful of facts somebody opening the page wants before any form,
// each a way into the tab that changes it.
function RepoOverview({ upToDate, branches, gates, onShowTab }) {
  const ciMode = repoCIMode(upToDate);
  const facts = [
    {
      label: "CI gate",
      value:
        gates.length > 0
          ? `${CI_MODE_LABELS[ciMode] || "…"} — base branch not judged`
          : CI_MODE_LABELS[ciMode] || "…",
      error: gates.length > 0,
      tab: "ci",
    },
    {
      label: "Up to date before merge",
      value: upToDate
        ? upToDate.requireBranchUpToDate
          ? "Required"
          : "Not required"
        : "…",
      tab: "merge-queue",
    },
    {
      label: "Merge when green",
      value: upToDate ? (upToDate.mergeOnGreen ? "On" : "Off") : "…",
      tab: "merge-queue",
    },
    {
      label: "Base repair bypass",
      value: upToDate
        ? upToDate.baseRepairBypassesHumanGates
          ? "On"
          : "Off"
        : "…",
      tab: "merge-queue",
    },
    {
      label: "Branches",
      value: String(branches.length),
      tab: "branches",
    },
  ];
  return (
    <Stack
      direction={{ xs: "column", sm: "row" }}
      spacing={1}
      useFlexGap
      flexWrap="wrap"
      aria-label="Summary"
    >
      {facts.map((f) => (
        <Button
          key={f.label}
          variant="outlined"
          color={f.error ? "error" : "inherit"}
          onClick={() => onShowTab(f.tab)}
          sx={{
            flexDirection: "column",
            alignItems: "flex-start",
            textTransform: "none",
            minWidth: 160,
            borderColor: f.error ? undefined : "divider",
          }}
        >
          <Typography variant="caption" color="text.secondary">
            {f.label}
          </Typography>
          <Typography variant="body2" sx={{ fontWeight: 600 }}>
            {f.value}
          </Typography>
        </Button>
      ))}
    </Stack>
  );
}

export default function RepoPage({
  repo,
  tasks,
  config,
  baseBranchGates = [],
  // onOpenComparison opens one comparison of two of this repo's refs --
  // grain's own pull request for them, which is a page of its own
  // (PullRequestPage.jsx, grain/task-375) rather than a diff folded in
  // below the form that asks for it.
  onOpenComparison,
  onBack,
  onNewTask,
  onOpenReleases,
  onOpenCIRun,
  onOpenTask,
  onRefreshConfig,
  // onRefreshGates re-reads GET /api/base-branch-gates in the shell, so
  // a setting changed here that judges (or stops judging) this repo's
  // base branch moves the warning above now rather than on the shell's
  // next minute-long poll.
  onRefreshGates = async () => {},
  // repoTab is the tab the path names (null for Overview), and onShowTab
  // how the page asks App to show another, which App writes into the
  // path. Without onShowTab -- the page rendered on its own -- the tab
  // is the page's own state, read from and written to the hash.
  repoTab = null,
  onShowTab,
  showError,
}) {
  const root = repoApiRoot(repo);
  // gateCheck counts the saves here that can change whether this repo's
  // base branch is judged; the release pane re-reads its own copy of
  // the gate when it moves.
  const [gateCheck, setGateCheck] = useState(0);
  // ownershipSaves and releaseToggles count the saves of the one setting
  // this page draws twice: the release's own work-ownership override is
  // the "Automatic releases" switch (model.RunsWorkItem), and the
  // repository default decides it while the release says nothing. Each
  // pane re-reads when the other one saves, so the page never says
  // "Automatic releases on" beside a release it has just turned off
  // (found in the 2026-10-13 repository walkthrough).
  const [ownershipSaves, setOwnershipSaves] = useState(0);
  const [releaseToggles, setReleaseToggles] = useState(0);
  const refreshGates = async () => {
    setGateCheck((n) => n + 1);
    try {
      await onRefreshGates();
    } catch {
      // The shell's poll reads them again within a minute anyway.
    }
  };
  const [branches, setBranches] = useState([]);
  // branchNote is what the last add said when it added nothing -- the
  // name was already on the list -- so the form says so rather than
  // leaving the list unchanged without a word.
  const [branchNote, setBranchNote] = useState("");
  const [compareBase, setCompareBase] = useState("main");
  const [compareHead, setCompareHead] = useState("");
  // caps is what GET /api/repos/{owner}/{name}/capabilities last said --
  // all three sets: the repo's own, the deployment's, and the union a
  // task filed here would actually start with -- and capsSelection the
  // unsaved ticks. caps is null while that read is in flight, which the
  // form renders a loading line for rather than an empty picker that
  // would look like "this repo adds nothing".
  const [caps, setCaps] = useState(null);
  const [capsSelection, setCapsSelection] = useState([]);
  const [ciCapsSelection, setCICapsSelection] = useState([]);
  // prompt/promptText are that same shape again, for this repo's own
  // prompt extension (grain/task-114): what GET /api/repos/{owner}/
  // {name}/prompt-extension last said -- the repo's own text, the
  // deployment's, and the two as a run would actually be given them --
  // and the unsaved edit. prompt is null while that read is in flight,
  // since an empty box would otherwise look like "this repo says
  // nothing", which is a real and different state.
  const [prompt, setPrompt] = useState(null);
  const [promptText, setPromptText] = useState("");
  // And once more for this repo's setup command (grain/task-154): what
  // GET /api/repos/{owner}/{name}/setup-command last said, and the
  // unsaved edit. null while in flight, for the same reason -- an empty
  // box would read as "grain runs nothing in this repo's checkouts",
  // which is a real and different answer.
  const [setup, setSetup] = useState(null);
  const [setupText, setSetupText] = useState("");
  // And whether grain reads this repository's own `.grain` directory for
  // the templates, suites and schedules it declares (grain/task-60):
  // what GET /api/repos/{owner}/{name}/declarations last said -- the
  // switch, and what has come of reading them. null while in flight, for
  // the reason the three above are null: an unticked box would read as
  // "grain does not read this repository's declarations", which is a
  // real and different answer.
  const [declarations, setDeclarations] = useState(null);
  // And whether the merge queue insists a branch contain its base before
  // it merges (grain/task-198): what GET /api/repos/{owner}/{name}/
  // require-branch-up-to-date last said. null while in flight, for the
  // reason the boxes above are null -- an unticked box would read as
  // "this repository merges branches that are behind their base", which
  // is a real and different answer, and the expensive one to get wrong
  // in either direction.
  const [upToDate, setUpToDate] = useState(null);
  // And the checks in this repository that will never pass for a pull
  // request grain's agents author, whatever is pushed to the branch --
  // `cla/google` and its kind (grain/task-291): what GET /api/repos/
  // {owner}/{name}/unfixable-checks last said, the half-typed name in the
  // box below it, and the reason the last one typed was refused.
  //
  // null while in flight, for the reason the boxes above are null. Here
  // the empty answer and the unread one are especially easy to confuse:
  // almost every repository declares nothing, so a list drawn empty
  // before the read lands looks exactly like the truth and would be
  // believed.
  const [unfixable, setUnfixable] = useState(null);
  const [unfixableDraft, setUnfixableDraft] = useState("");
  const [unfixableError, setUnfixableError] = useState("");
  // And the grain CI workflows this deployment declares for this repo
  // (grain/task-245): what GET /api/repos/{owner}/{name}/workflows last
  // said, and the unsaved edits -- a list of {path, text}, since the
  // format is one file per workflow and a repo may have several. null
  // while in flight, for the reason the three above are null: an empty
  // list would read as "grain declares nothing for this repo", which is
  // a real and different answer.
  const [workflows, setWorkflows] = useState(null);
  const [workflowEdits, setWorkflowEdits] = useState([]);
  // A repo's own defaults -- capabilities, prompt extension, setup
  // command -- are the deployment's, edited here on the /api/repos/...
  // rows. They had a team layer of their own on top until the
  // single-tenant reset (stages 3e-ii and 3e-iv).

  // writeRefusal is the sentence for a reader who is not a repository
  // admin of this repository: every setting here is checkRepoPerson's
  // (pkg/ui/repos.go), and what /api/config's adminRepos reports
  // (repoAdministered). The server refuses them with a 403, so the page
  // says why once and draws its controls disabled rather than offer ones
  // that can only fail. null is "offer everything".
  const writeRefusal = repoAdministered(config, repo)
    ? null
    : `you can read ${repo}'s settings but not change them: that needs a repository admin`;
  const readOnly = writeRefusal !== null;
  // The same sentence is each disabled control's own accessible
  // description, so a screen reader on one hears why it is dimmed
  // rather than having to find the page's one alert (2026-10-14
  // role-shaped sweep). refusedBy(off, ...more) is the aria-describedby
  // for a control disabled when off is: the alert's id, plus whatever
  // already describes the control -- a TextField's own helper text,
  // which setting the attribute would otherwise replace.
  const refusalId = useId();
  const refusedBy = (off, ...more) => {
    const ids = [off && writeRefusal ? refusalId : "", ...more].filter(Boolean);
    return ids.length ? ids.join(" ") : undefined;
  };

  // The row this repo would have on the list page: its per-state counts,
  // whether it is on the allowlist (which is what makes Remove worth
  // offering), and whether it carries defaults of its own. A repo this
  // deployment knows nothing about -- a hand-typed URL, or one whose
  // last task and allowlist entry both went away while this page was
  // open -- has no row, and gets an empty one rather than crashing the
  // page: its forms still work, since none of them needs the repo to be
  // known first.
  const row = repoRows(config, tasks).find((r) => r.repo === repo) || {
    repo,
    total: 0,
    counts: {},
    blocked: 0,
    configured: false,
    defaults: false,
  };

  const loadBranches = useCallback(async () => {
    try {
      setBranches(latestPerName(await api(`${root}/branches`)));
    } catch (err) {
      showError(err);
    }
  }, [root, showError]);

  // Comparing is navigation, not a read: the pair of refs names a page,
  // so asking for one here is the same gesture as following a task's own
  // Internal PR link, and lands in the same place.
  const compareBranches = (event) => {
    event.preventDefault();
    if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)) return;
    onOpenComparison({ base: compareBase.trim(), head: compareHead.trim() });
  };

  const loadCapabilities = useCallback(async () => {
    try {
      const loaded = await api(`${root}/capabilities`);
      setCaps(loaded);
      setCapsSelection(loaded.defaultCapabilities || []);
      setCICapsSelection(loaded.ciCapabilities || []);
    } catch (err) {
      showError(err);
    }
  }, [root, showError]);

  const loadPromptExtension = useCallback(async () => {
    try {
      const loaded = await api(`${root}/prompt-extension`);
      setPrompt(loaded);
      setPromptText(loaded.promptExtension || "");
    } catch (err) {
      showError(err);
    }
  }, [root, showError]);

  const loadSetupCommand = useCallback(async () => {
    try {
      const loaded = await api(`${root}/setup-command`);
      setSetup(loaded);
      setSetupText(loaded.setupCommand || "");
    } catch (err) {
      showError(err);
    }
  }, [root, showError]);

  const loadDeclarations = useCallback(async () => {
    try {
      setDeclarations(await api(`${root}/declarations`));
    } catch (err) {
      showError(err);
    }
  }, [root, showError]);

  const loadUpToDate = useCallback(async () => {
    try {
      setUpToDate(await api(`${root}/require-branch-up-to-date`));
    } catch (err) {
      showError(err);
    }
  }, [root, showError]);

  const loadUnfixableChecks = useCallback(async () => {
    try {
      setUnfixable(await api(`${root}/unfixable-checks`));
    } catch (err) {
      showError(err);
    }
  }, [root, showError]);

  const loadWorkflows = useCallback(async () => {
    try {
      const loaded = await api(`${root}/workflows`);
      setWorkflows(loaded);
      setWorkflowEdits(
        (loaded.workflows || []).map((w) => ({ path: w.path, text: w.text })),
      );
    } catch (err) {
      showError(err);
    }
  }, [root, showError]);

  // All of these reads happen on landing rather than behind a toggle the
  // way the list page's own fold-out forms did: this page is *about*
  // this repo, so there is nothing else the GETs could be competing
  // with.
  useEffect(() => {
    loadBranches();
  }, [loadBranches]);
  useEffect(() => {
    loadCapabilities();
  }, [loadCapabilities]);
  useEffect(() => {
    loadPromptExtension();
  }, [loadPromptExtension]);
  useEffect(() => {
    loadSetupCommand();
  }, [loadSetupCommand]);
  useEffect(() => {
    loadDeclarations();
  }, [loadDeclarations]);
  useEffect(() => {
    loadUpToDate();
  }, [loadUpToDate]);
  useEffect(() => {
    loadUnfixableChecks();
  }, [loadUnfixableChecks]);
  useEffect(() => {
    loadWorkflows();
  }, [loadWorkflows]);

  // What the prompt extension and setup command hold as stored, to tell
  // a blur that changed something from one that did not.
  const storedPromptText = prompt?.promptExtension || "";
  const storedSetupText = setup?.setupCommand || "";

  // capsEffective is what a task filed against this repo would actually
  // start out holding -- the line the capabilities form ends on. The
  // union is recomputed from the unsaved ticks rather than read back from
  // caps.effectiveDefaultCapabilities, so it describes the set Save is
  // about to make real rather than the one the last response described.
  //
  // Filtered to what this build still offers, the same filter
  // ui.(*Client).defaultCapabilities applies before any grant is written
  // -- and so the same one behind RepoDefaults.
  // EffectiveDefaultCapabilities, the server's own answer to this
  // question. Neither of the two sets GET reports is filtered: both come
  // back exactly as stored, retired ids included, deliberately, so an id
  // chosen before a build retired it stays visible in the picker above
  // to be unticked. But a task filed here does not start out holding
  // one, so this line must not say it does.
  const capsEffective = unionCapabilities(
    caps?.deploymentDefaultCapabilities,
    capsSelection,
  ).filter((id) => {
    return (
      isCapabilityRef(id) ||
      (config?.capabilities || []).some((cap) => cap.id === id)
    );
  });

  // The repo's own defaults hold all three kinds a task's capabilities
  // do: the legacy in-process ids are the checklist's, and the agent
  // API's grants and capability server methods (with their with:
  // arguments) are GrantsPicker's, each list saved whole with the
  // other's entries kept.
  const capsLegacy = capsSelection.filter(
    (id) => !isCapabilityRef(id) && !isApiGrant(id),
  );
  const capsApi = capsSelection.filter(isApiGrant);
  const capsRefs = capsSelection.filter(isCapabilityRef);
  const capsMethods = localGrantHandlers(capsRefs, (refs) => {
    const next = [...capsLegacy, ...capsApi, ...refs];
    setCapsSelection(next);
    return saveCapabilities(next);
  });
  // A CI job holds no agent API grant -- there is no agent in it -- so
  // CI's own GrantsPicker is offered capability server methods alone.
  const ciCapsLegacy = ciCapsSelection.filter((id) => !isCapabilityRef(id));
  const ciCapsRefs = ciCapsSelection.filter(isCapabilityRef);
  const ciCapsMethods = localGrantHandlers(ciCapsRefs, (refs) => {
    const next = [...ciCapsLegacy, ...refs];
    setCICapsSelection(next);
    return saveCICapabilities(next);
  });

  // The rows the default-capabilities picker below is drawn from, held
  // here so the picker and its own "nothing to pick" line agree about
  // what it is offering.
  const capsRows = withoutApiRows(
    capabilityRows(config?.capabilities, capsLegacy),
  );
  const allCICapabilities =
    config?.ciCapabilities || config?.capabilities || [];
  const ciCapsRows = capabilityRows(
    allCICapabilities.filter((c) =>
      (caps?.availableCICapabilities || []).some(
        (a) => (typeof a === "string" ? a : a?.id) === c.id,
      ),
    ),
    ciCapsLegacy,
  );

  // Removing the repo from the allowlist leaves this page describing a
  // repo the deployment no longer allows -- and, if nothing else here
  // mentions it, one the list page no longer shows either -- so it goes
  // back to that list rather than staying put.
  const removeRepo = async () => {
    // What this removal would strand is asked of the daemon before the
    // click is honoured, and asked as the very save it is: POST
    // /api/settings/plan with this repo dropped from targetRepos answers
    // with the tasks, schedules and qualification plan left behind in it
    // (ui.Settings.TargetReposStranded, grain/task-411).
    //
    // Asked rather than worked out here, for planChanges' own reason: the
    // work behind a repo is the store's to count, and this page holds
    // none of it. The removal itself is allowed either way -- it used to
    // be refused while any task there was still in play, and grain now
    // parks that work rather than running it into a repo the list no
    // longer names -- so this is a confirmation that says what it costs,
    // not a gate.
    let stranded = [];
    try {
      const targetRepos = (config?.targetRepos || []).filter(
        (r) => r.toLowerCase() !== repo.toLowerCase(),
      );
      const plan = await api("/api/settings/plan", {
        method: "POST",
        body: JSON.stringify({ targetRepos }),
      });
      stranded = plan?.targetReposStranded || [];
    } catch {
      // A plan this build cannot get -- an older daemon with no such
      // route, an unconfigured deployment whose settings have never been
      // saved -- must not stop a removal that never depended on one. The
      // confirmation then says what it has always said.
    }
    const cost = stranded.map((s) => s.summary).join("\n\n");
    if (
      !confirm(
        `Remove ${repo} from target repos? New tasks won't be able to target it until it's added back, and it will drop off the repos list.` +
          (cost === ""
            ? " Tasks already filed there are not affected."
            : `\n\n${cost}`),
      )
    )
      return;
    try {
      await api(root, { method: "DELETE" });
      await onRefreshConfig();
      onBack();
    } catch (err) {
      showError(err);
    }
  };

  // addBranch only ever records the request -- the branches reconciler
  // (pkg/orchestrator.SyncBranches) is what goes to GitHub, typically
  // within one cycle, so a freshly submitted name reappears below still
  // "pending" until the next loadBranches picks up "created" (grain cut
  // the ref), "adopted" (the name was already on the repo, taken as it
  // stands) or, if GitHub refused it, an error.
  const addBranch = async (evt) => {
    evt.preventDefault();
    const form = evt.target;
    const branchName = form.elements.branchName.value.trim();
    if (branchName === "") return;
    try {
      const added = await api(`${root}/branches`, {
        method: "POST",
        body: JSON.stringify({ name: branchName }),
      });
      setBranchNote(
        added && added.alreadyRequested
          ? `${branchName} is already on this repo's list (${added.status}); nothing new was added.`
          : "",
      );
      form.reset();
      await loadBranches();
    } catch (err) {
      showError(err);
    }
  };

  // saveCICapabilities replaces this repo's own set wholesale (PUT's whole
  // body is the new set, ui.SetRepoCapabilitiesRequest), then refreshes
  // the config the new-task form seeds its own picker from -- otherwise a
  // repo whose defaults just changed would keep filing tasks with the old
  // ones until the page was reloaded.
  const saveCICapabilities = async (nextSelection) => {
    const toSave =
      nextSelection !== undefined && !nextSelection?.preventDefault
        ? nextSelection
        : ciCapsSelection;
    try {
      const updated = await api(`${root}/ci-capabilities`, {
        method: "PUT",
        body: JSON.stringify({ ciCapabilities: toSave }),
      });
      setCaps(updated);
      setCICapsSelection(updated.ciCapabilities || []);
      await onRefreshConfig();
    } catch (err) {
      showError(err);
    }
  };

  const saveCapabilities = async (nextSelection) => {
    const toSave =
      nextSelection !== undefined && !nextSelection?.preventDefault
        ? nextSelection
        : capsSelection;
    try {
      const updated = await api(`${root}/capabilities`, {
        method: "PUT",
        body: JSON.stringify({ defaultCapabilities: toSave }),
      });
      setCaps(updated);
      setCapsSelection(updated.defaultCapabilities || []);
      await onRefreshConfig();
    } catch (err) {
      showError(err);
    }
  };

  // savePromptExtension replaces this repo's own text wholesale (PUT's
  // whole body is the new text, ui.SetRepoPromptExtensionRequest), then
  // refreshes the config, for the same reason saveCapabilities does.
  // Nothing the new-task form seeds itself from changes here -- a repo's
  // own text is read at dispatch, not written onto the task -- but
  // config.reposWithPromptExtension is one of the three things that make
  // a repo appear on the list page at all (repoRows, state.js). Writing
  // the first standing instructions for a repo with no tasks and no
  // allowlist entry is what puts it on that list, and clearing its last
  // ones is what takes it off; without this the list keeps whichever
  // answer it was given at mount until the page is reloaded, and the
  // repo whose text is the only thing keeping it reachable is the one
  // that would go missing.
  const savePromptExtension = async (textToSave) => {
    const val = typeof textToSave === "string" ? textToSave : promptText.trim();
    try {
      const updated = await api(`${root}/prompt-extension`, {
        method: "PUT",
        body: JSON.stringify({ promptExtension: val }),
      });
      setPrompt(updated);
      setPromptText(updated.promptExtension || "");
      await onRefreshConfig();
    } catch (err) {
      showError(err);
    }
  };

  // saveSetupCommand replaces this repo's setup command wholesale (PUT's
  // whole body is the new command, ui.SetRepoSetupCommandRequest), and
  // refreshes the config for the reason savePromptExtension does:
  // config.reposWithSetupCommand is one of the sources that put a repo on
  // the list page at all (repoRows, state.js), so writing the first setup
  // command for a repo with no tasks and no allowlist entry is what makes
  // it reachable, and clearing the last one is what takes it away.
  const saveSetupCommand = async (cmdToSave) => {
    const val = typeof cmdToSave === "string" ? cmdToSave : setupText.trim();
    try {
      const updated = await api(`${root}/setup-command`, {
        method: "PUT",
        body: JSON.stringify({ setupCommand: val }),
      });
      setSetup(updated);
      setSetupText(updated.setupCommand || "");
      await onRefreshConfig();
    } catch (err) {
      showError(err);
    }
  };

  // saveDeclarations turns reading this repository's own declarations on
  // or off. Nothing is read or removed by the request itself: the next
  // cycle does both (orchestrator.reconcileDeclarations), which is why
  // the status below can still be empty a moment after ticking the box.
  const saveDeclarations = async (on) => {
    try {
      setDeclarations(
        await api(`${root}/declarations`, {
          method: "PUT",
          body: JSON.stringify({ declarations: on }),
        }),
      );
      await onRefreshConfig();
    } catch (err) {
      showError(err);
    }
  };

  // saveUpToDate turns the merge queue's strict rule on or off for this
  // repository. Like the switch above it, the request does nothing
  // beyond the switch: no branch is brought up to date and no suite is
  // started here, the next merge-queue cycle reads the row and acts on
  // it. Turning it back off does not stop a suite already running
  // against a head the queue has already written.
  const saveUpToDate = async (on) => {
    try {
      setUpToDate(
        await api(`${root}/require-branch-up-to-date`, {
          method: "PUT",
          body: JSON.stringify({ requireBranchUpToDate: on }),
        }),
      );
      await onRefreshConfig();
    } catch (err) {
      showError(err);
    }
  };

  // saveMergeOnGreen turns merging green, clean pull requests ahead of
  // ones whose CI is still running on or off for this repository
  // (grain/task-545). The next merge-queue cycle reads the row.
  const saveMergeOnGreen = async (on) => {
    try {
      setUpToDate(
        await api(`${root}/merge-on-green`, {
          method: "PUT",
          body: JSON.stringify({ mergeOnGreen: on }),
        }),
      );
      await onRefreshConfig();
    } catch (err) {
      showError(err);
    }
  };

  const saveBaseRepairBypassesHumanGates = async (on) => {
    try {
      setUpToDate(
        await api(`${root}/base-repair-bypasses-human-gates`, {
          method: "PUT",
          body: JSON.stringify({ baseRepairBypassesHumanGates: on }),
        }),
      );
      await onRefreshConfig();
    } catch (err) {
      showError(err);
    }
  };

  // saveWatchDefaultBranch turns watching this repository's default
  // branch on or off (grain/task-331). Nothing runs here either: the
  // next base-branch cycle resolves the default branch and starts the
  // first suite, so the row this page would show can be a cycle behind
  // the tick.
  const saveWatchDefaultBranch = async (on) => {
    try {
      setUpToDate(
        await api(`${root}/watch-default-branch`, {
          method: "PUT",
          body: JSON.stringify({ watchDefaultBranch: on }),
        }),
      );
      await onRefreshConfig();
      await refreshGates();
    } catch (err) {
      showError(err);
    }
  };

  const saveCIMode = async (mode) => {
    try {
      setUpToDate(
        await api(`${root}/ci-mode`, {
          method: "PUT",
          body: JSON.stringify({ ciMode: mode }),
        }),
      );
      await onRefreshConfig();
      await refreshGates();
    } catch (err) {
      showError(err);
    }
  };

  // The checks a commit on a branch here cannot clear, as this repo
  // currently declares them, and what grain's own CI is called in this
  // build -- the one name the list may not hold (forge.Unfixable.Covers,
  // ui.SetRepoUnfixableChecks). The name is read from the response rather
  // than written in here so that renaming it in Go renames it in the
  // refusal below too.
  const unfixableChecks = unfixable?.unfixableChecks || [];
  const grainCIName = unfixable?.grainCICheckName || "grain CI";
  const sameCheck = (a, b) => a.trim().toLowerCase() === b.trim().toLowerCase();

  // saveUnfixableChecks replaces the whole list (PUT's whole body is the
  // new list, ui.SetRepoUnfixableChecksRequest), so a name removed here
  // is a declaration withdrawn -- there is no other route that would.
  //
  // Nothing is repaired or retried by the request itself: the next
  // merge-queue cycle reads the row and stops spending a base merge and a
  // repair run on a check this now says cannot pass
  // (orchestrator.escalateUnfixableChecks). Withdrawing a name is the
  // same shape backwards -- the queue goes back to repairing that red on
  // its next cycle, not immediately.
  const saveUnfixableChecks = async (checks) => {
    try {
      const updated = await api(`${root}/unfixable-checks`, {
        method: "PUT",
        body: JSON.stringify({ unfixableChecks: checks }),
      });
      setUnfixable(updated);
      setUnfixableError("");
      await onRefreshConfig();
      return true;
    } catch (err) {
      showError(err);
      return false;
    }
  };

  // addUnfixableCheck declares one more check unfixable here. The two
  // names it turns away it turns away itself rather than sending: grain
  // CI's own verdict, which no declaration covers however it is stored,
  // and a check this repo already names, since the matching is done
  // without case and a second spelling would be one declaration drawn
  // twice. The server refuses both as well (ui.SetRepoUnfixableChecks) --
  // this is here so the reason lands beside the box that was typed into,
  // and before a write, rather than as an error banner after one.
  const addUnfixableCheck = async (evt) => {
    evt.preventDefault();
    const name = unfixableDraft.trim();
    if (name === "") return;
    if (sameCheck(name, grainCIName)) {
      setUnfixableError(
        `${grainCIName} judges one exact commit on the branch, so its verdict is always that branch's own answer and declaring it here would change nothing. Grain would go on repairing it.`,
      );
      return;
    }
    if (unfixableChecks.some((check) => sameCheck(check, name))) {
      setUnfixableError(
        `${repo} already declares ${name} unfixable — check names are matched without case.`,
      );
      return;
    }
    if (await saveUnfixableChecks([...unfixableChecks, name]))
      setUnfixableDraft("");
  };

  // saveWorkflows replaces the whole set this deployment declares for
  // this repo (PUT's whole body is the new set, ui.
  // SetRepoWorkflowsRequest), so a workflow removed from the list below
  // is a workflow deleted -- there is no other way to delete one, and no
  // route that would.
  //
  // A row nobody typed anything into is dropped rather than sent: the
  // Add button leaves an empty pair behind, and an empty pair is an
  // error from the server ("a workflow needs a path") for something
  // somebody never meant to save. A row with only one half filled in
  // holds the whole save back instead: this runs on every blur, and
  // leaving the path box for the workflow box is the middle of writing
  // one, not a request to have it refused. Nothing is saved until every
  // row has both halves or neither.
  //
  // The server parses every file with the run's own parser before it
  // stores any of it, which is where a broken workflow is refused --
  // showError puts that message, naming the file and the key, above this
  // form. Nothing is written when it does, so the boxes still hold what
  // was typed.
  //
  // Then the config refresh, for the reason savePromptExtension does it:
  // config.reposWithWorkflows is one of the sources that put a repo on
  // the list page at all (repoRows, state.js), and a repo grain declares
  // workflows for is the likeliest of all of them to have nothing else
  // keeping it reachable.
  const saveWorkflows = async (editsToSave) => {
    const list = Array.isArray(editsToSave) ? editsToSave : workflowEdits;
    const hasIncomplete = list.some(
      (w) =>
        (w.path.trim() === "" && w.text.trim() !== "") ||
        (w.path.trim() !== "" && w.text.trim() === ""),
    );
    if (hasIncomplete) {
      return;
    }
    const body = list.filter(
      (w) => w.path.trim() !== "" && w.text.trim() !== "",
    );
    await putWorkflows(body, list);
  };

  // putWorkflows stores body as the whole set and settles the form on
  // what came back -- without throwing away anything the form gained
  // while the request was in flight (grain/task-2414). Leaving a box for
  // the Add workflow button is a blur, so the save and the new empty row
  // land together, and replacing the rows wholesale with the server's
  // used to take that row straight back out again. Only a row that is
  // still the very object this save sent is swapped for what the server
  // stored for it (its path filled in under the directory); a row added,
  // or typed into, since then is the operator's and stays as it is.
  const putWorkflows = async (body, sent) => {
    try {
      const updated = await api(`${root}/workflows`, {
        method: "PUT",
        body: JSON.stringify({ workflows: body }),
      });
      setWorkflows(updated);
      setWorkflowEdits((current) =>
        settleWorkflowEdits(current, sent, updated.workflows || []),
      );
      await onRefreshConfig();
      return true;
    } catch (err) {
      showError(err);
      return false;
    }
  };

  // Asked by "← Repos" and App's popstate (leavingLosesEdits) and by the
  // browser's beforeunload, like the Releases tab's job boxes. Read
  // through a ref, since guardLeaving holds the one function it is given.
  const workflowsUnsaved = useRef(() => false);
  workflowsUnsaved.current = () =>
    workflows !== null &&
    workflowEditsUnsaved(workflowEdits, workflows.workflows);
  useEffect(() => guardLeaving(() => workflowsUnsaved.current()), []);

  const editWorkflow = (index, field, value) => {
    setWorkflowEdits((edits) =>
      edits.map((w, i) => (i === index ? { ...w, [field]: value } : w)),
    );
  };

  // Removing deletes exactly the one stored workflow the row stands for,
  // computed from the set the server last returned rather than from the
  // form (grain/task-2414). Saving the form's rows instead held the
  // removal back whenever another row was half written -- the
  // one-half-filled guard above returns without a request -- and the row
  // vanished here while the workflow stayed stored, back on the next
  // load. A row the server never stored (a new one, or one whose save was
  // refused) is only dropped from the form, with nothing to ask.
  const removeWorkflow = async (index) => {
    const row = workflowEdits[index];
    const stored = (workflows?.workflows || []).filter(
      (w) => !sameWorkflowPath(w.path, row.path),
    );
    const isStored = stored.length < (workflows?.workflows || []).length;
    if (
      isStored &&
      !confirm(
        `Delete the workflow ${row.path.trim()} grain declares for ${repo}? Nothing else keeps a copy of it.`,
      )
    )
      return;
    if (
      isStored &&
      !(await putWorkflows(
        stored.map((w) => ({ path: w.path, text: w.text })),
        [],
      ))
    )
      return;
    setWorkflowEdits((current) => current.filter((w) => w !== row));
  };

  const addWorkflow = () => {
    setWorkflowEdits((edits) => [...edits, { path: "", text: "" }]);
  };

  const [ownTab, setOwnTab] = useState(() => tabFromHash(window.location.hash));
  const tabStripRef = useRevealSelectedTab();
  const tab = onShowTab
    ? REPO_TABS.some((t) => t.id === repoTab)
      ? repoTab
      : "overview"
    : ownTab;
  const showTab = useCallback(
    (next) => {
      if (onShowTab) {
        onShowTab(next === "overview" ? null : next);
        return;
      }
      setOwnTab(next);
      const { pathname, search } = window.location;
      window.history.replaceState(
        window.history.state,
        "",
        next === "overview"
          ? `${pathname}${search}`
          : `${pathname}${search}#${next}`,
      );
    },
    [onShowTab],
  );
  // Changing only the hash -- typing #ci over #agents in the address
  // bar, or opening an old link to another tab of the page already
  // showing -- is a same-document navigation that remounts nothing;
  // without this the tab stayed put. A hash that names no tab leaves a
  // tab the path chose where it is.
  useEffect(() => {
    const follow = () => {
      const id = window.location.hash.replace(/^#/, "");
      if (onShowTab) {
        if (REPO_TABS.some((t) => t.id === id)) showTab(id);
        return;
      }
      setOwnTab(tabFromHash(window.location.hash));
    };
    window.addEventListener("hashchange", follow);
    return () => window.removeEventListener("hashchange", follow);
  }, [onShowTab, showTab]);

  const unjudgedGates = baseBranchGates.filter(
    (g) => g.repo === repo && !g.judged,
  );

  return (
    <main className="main-column">
      <div className="repo-page-header">
        {/* Asks before a job box's unsaved value goes with the page
            (RepoReleaseWorkflowSettings's guard). Deleting the repository
            leaves through onBack directly: nothing here is left to save. */}
        <Button
          onClick={() => {
            if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)) return;
            onBack();
          }}
          sx={{ ml: -0.9, alignSelf: "flex-start" }}
        >
          &larr; Repos
        </Button>
        <Stack
          direction="row"
          alignItems="center"
          spacing={1}
          flexWrap="wrap"
          useFlexGap
        >
          {/* The ring in front of the name, at the size a heading holds
              it (ItemGlyph.jsx): this page is what a row on the Repos
              list opens, so it is headed by the same figure that list
              and the nav entry above it carry. Decorative -- the repo's
              own name is right beside it -- and outside the heading, so
              "owner/name" stays the whole of its accessible name. */}
          <ItemGlyph kind="repos" size={20} />
          {/* The name is also the way out to the repo itself (grain/
              task-143): everything on this page is grain's side of a repo
              that lives on GitHub, and the page had no link to the thing
              it is about -- somebody who wanted the code, the branch this
              page just created, or the pull requests behind those chips
              had to retype "github.com/owner/name" for themselves.
              Heading text is where a reader already looks for that, so it
              is the link rather than another button beside New task; it
              keeps the heading's colour and underlines on hover, and
              "owner/name" stays the whole of its accessible name. */}
          <Typography
            variant="h6"
            component="h2"
            sx={{ m: 0, fontSize: "1.15rem", fontWeight: 600 }}
          >
            <Link
              href={repoUrl(repo)}
              target="_blank"
              rel="noopener noreferrer"
              color="inherit"
              underline="hover"
              title={`Open ${repo} on GitHub`}
            >
              {repo}
            </Link>
          </Typography>
          <span className="chips">
            {/* Each chip counts this repo's tasks in one state, not one
                task's own status, so "running" keeps the plain CSS spin
                (style.css's .badge-running) rather than StateDot's grain
                mark (bwsalmon/agents#586) -- there is no single task for
                the mark to represent.

                These are repoRows' counts, which are of the tasks filed
                against this repo (and are the same numbers the Repos
                list and `grain repo list` show), so they can come to
                less than the list below: that list also holds the tasks
                filed elsewhere that may land work here (grain/task-116).
                Each of those rows carries the repo it was filed against
                and a "(write)" chip naming this one, which is what says
                so on the row rather than in this count. */}
            {displayCounts(row.counts).map(({ state: s, count }) => (
              <Chip
                key={s}
                size="small"
                className={`badge badge-${s}`}
                label={`${STATE_LABELS[s] || s} ${count}`}
              />
            ))}
            {row.blocked > 0 && (
              <Chip
                size="small"
                color="error"
                label={`Blocked ${row.blocked}`}
              />
            )}
          </span>
          <Link
            variant="caption"
            color="text.secondary"
            whiteSpace="nowrap"
            href={buildPath({
              view: "tasks",
              narrowing: {
                search: "",
                sort: [],
                filters: [{ attr: "repo", op: "any", values: [repo] }],
              },
            })}
          >
            {row.total} task{row.total === 1 ? "" : "s"} filed here
          </Link>
          <Box sx={{ flex: 1 }} />
          <Button
            size="small"
            variant="contained"
            onClick={() => onNewTask(repo)}
          >
            New task
          </Button>
          <Button
            size="small"
            variant="outlined"
            onClick={() => {
              if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT))
                return;
              onOpenReleases(repo);
            }}
          >
            Releases
          </Button>
        </Stack>

        {unjudgedGates.map((gate) => (
          <Alert
            key={`${gate.repo}/${gate.branch || ""}`}
            severity="error"
            // The header lowercases its own text; the server's sentence
            // names grain CI and a repository and is read as written.
            sx={{ textTransform: "none" }}
            action={
              gate.runID ? (
                <Button
                  color="inherit"
                  size="small"
                  onClick={() => onOpenCIRun(gate.runID, repo)}
                >
                  View CI run
                </Button>
              ) : gate.reason === "grain-ci-off" ? (
                // The repair is this page's own CI Mode, on the CI tab.
                <Button
                  color="inherit"
                  size="small"
                  onClick={() => {
                    showTab("ci");
                    setTimeout(() =>
                      document
                        .getElementById(CI_MODE_ID)
                        ?.scrollIntoView({ block: "center" }),
                    );
                  }}
                >
                  Go to CI Mode
                </Button>
              ) : null
            }
          >
            {gate.detail}
          </Alert>
        ))}

        {/* A repo with no tasks that is off the allowlist is known to
            this deployment only because it carries configuration of its
            own (repoRows' third source). Without saying so the page reads
            as being about a repo nothing here has anything to do with,
            and the two forms below are the only things that put it
            here. */}
        {row.defaults && !row.configured && row.total === 0 && (
          <Alert severity="info">
            No tasks, and not on this deployment&apos;s target repos -- {repo}{" "}
            is known here only because it carries configuration of its own --
            default capabilities, standing instructions, a setup command, or any
            of them -- below.
          </Alert>
        )}

        {writeRefusal && (
          // The header lowercases its own text; this is a sentence.
          <Alert id={refusalId} severity="info" sx={{ textTransform: "none" }}>
            {writeRefusal}
          </Alert>
        )}
        <Tabs
          ref={tabStripRef}
          value={tab}
          onChange={(_, next) => showTab(next)}
          variant="scrollable"
          scrollButtons="auto"
          allowScrollButtonsMobile
          onFocus={revealFocusedTab}
          aria-label={`${repo} sections`}
          sx={{ borderBottom: 1, borderColor: "divider", mt: 1 }}
        >
          {REPO_TABS.map((t) => (
            <Tab
              key={t.id}
              value={t.id}
              id={`repo-tab-${t.id}`}
              aria-controls={`repo-tabpanel-${t.id}`}
              label={
                t.id === "ci" && unjudgedGates.length > 0 ? (
                  <Badge color="error" variant="dot">
                    {t.label}
                  </Badge>
                ) : (
                  t.label
                )
              }
            />
          ))}
        </Tabs>
      </div>

      <div className="repo-page-body pane-form">
        <RepoTabPanel id="overview" tab={tab}>
          <RepoOverview
            repo={repo}
            upToDate={upToDate}
            branches={branches}
            gates={unjudgedGates}
            onShowTab={showTab}
          />

          {/* And what that queue is actually doing right now, under the
              settings that shape it (grain/task-301): the batches in
              flight, which is the one part of the queue's state that is
              not visible on any task's row. A member's row says it is
              being tested with others; only this says which others, which
              of them is first, and where the one run deciding all of them
              is. */}
          <MergeBatchPanel
            repo={repo}
            onOpenRun={onOpenCIRun}
            onOpenTask={onOpenTask}
            showError={showError}
          />

          {/* And what those workflows have actually done (grain/task-351),
              directly under the form that writes them: the two questions
              are asked in one sitting, and the answer used to be three
              panes away. RepoRuns owns the whole of it -- the read, the
              rows and the way into a run -- since none of it is this
              page's own state. */}
          <RepoRuns
            repo={repo}
            onOpenRun={(runId, r) => {
              if (
                !runId &&
                leavingLosesEdits() &&
                !window.confirm(DISCARD_PROMPT)
              )
                return;
              onOpenCIRun(runId, r);
            }}
            showError={showError}
          />
        </RepoTabPanel>

        <RepoTabPanel id="ci" tab={tab}>
          <Box>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              CI gate
            </Typography>
            {upToDate === null ? (
              <Typography variant="body2" color="text.secondary">
                Loading merge queue settings…
              </Typography>
            ) : (
              <Stack spacing={1.5}>
                <Stack spacing={1} id={CI_MODE_ID}>
                  <Typography variant="body2" sx={{ fontWeight: 600 }}>
                    CI Mode
                  </Typography>
                  <RadioGroup
                    value={
                      upToDate.ciMode ||
                      (upToDate.ignoreGitHubCI ? "grain" : "github")
                    }
                    onChange={(e) => saveCIMode(e.target.value)}
                  >
                    <FormControlLabel
                      value="github"
                      control={
                        <Radio
                          size="small"
                          disabled={readOnly}
                          inputProps={{
                            "aria-label": `Use GitHub CI for ${repo}`,
                            "aria-describedby": refusedBy(readOnly),
                          }}
                        />
                      }
                      label={
                        <Box>
                          <Typography variant="body2">Use GitHub CI</Typography>
                          <Typography
                            variant="caption"
                            color="text.secondary"
                            sx={{ display: "block" }}
                          >
                            External GitHub Actions check runs gate pull
                            requests before merging.
                          </Typography>
                        </Box>
                      }
                    />
                    <FormControlLabel
                      value="grain"
                      control={
                        <Radio
                          size="small"
                          disabled={readOnly}
                          inputProps={{
                            "aria-label": `Make grain CI the gate for ${repo} and stop reading GitHub's checks`,
                            "aria-describedby": refusedBy(readOnly),
                          }}
                        />
                      }
                      label={
                        <Box>
                          <Typography variant="body2">Use Grain CI</Typography>
                          <Typography
                            variant="caption"
                            color="text.secondary"
                            sx={{ display: "block" }}
                          >
                            GitHub&rsquo;s check runs are not a source of
                            verdicts for {repo}: grain CI (
                            <code>.grain/workflows</code>) is its gate. With
                            this on they are not read at all — the queue does
                            not count them, does not wait on them, no stall
                            clock runs on them, and a run&rsquo;s own CI reports
                            say so rather than listing reds it then has to
                            explain away.
                            <Box
                              component="span"
                              sx={{ display: "block", mt: 0.5 }}
                            >
                              It swaps the gate rather than removing it. Tasks
                              filed against {repo} while this is on are granted
                              the <code>grain-ci</code> capability, so their
                              branches are judged by {repo}&rsquo;s{" "}
                              <code>.grain/workflows</code>. A commit with no
                              grain CI verdict stays pending and is eventually
                              handed to a person, so if {repo} declares no
                              workflows, turning this on means {repo}{" "}
                              <strong>merges nothing</strong> rather than
                              merging ungated.
                            </Box>
                          </Typography>
                        </Box>
                      }
                    />
                    <FormControlLabel
                      value="none"
                      control={
                        <Radio
                          size="small"
                          disabled={readOnly}
                          inputProps={{
                            "aria-label": `Use No CI for ${repo}`,
                            "aria-describedby": refusedBy(readOnly),
                          }}
                        />
                      }
                      label={
                        <Box>
                          <Typography variant="body2">Use No CI</Typography>
                          <Typography
                            variant="caption"
                            color="text.secondary"
                            sx={{ display: "block" }}
                          >
                            CI verification is bypassed; pull requests merge
                            cleanly without waiting for CI checks.
                          </Typography>
                        </Box>
                      }
                    />
                  </RadioGroup>
                </Stack>
                <Stack direction="row" alignItems="flex-start" spacing={1}>
                  <Checkbox
                    size="small"
                    sx={{ mt: -0.5 }}
                    checked={Boolean(upToDate.watchDefaultBranch)}
                    onChange={(e) => saveWatchDefaultBranch(e.target.checked)}
                    disabled={readOnly}
                    inputProps={{
                      "aria-label": `Watch ${repo}'s default branch with grain CI even when grain has not merged into it`,
                      "aria-describedby": refusedBy(readOnly),
                    }}
                  />
                  <Typography variant="body2" color="text.secondary">
                    Keep grain CI&rsquo;s verdict about {repo}&rsquo;s default
                    branch current whether or not grain merged what moved it.
                    Without this, only a merge grain performed ever starts
                    watching a branch — so a repository whose pull requests are
                    merged by hand is watched by nothing, and the queue can
                    merge onto a red default branch.
                    <Box component="span" sx={{ display: "block", mt: 1 }}>
                      It costs one full suite on that branch shortly after you
                      tick it, and if that suite is red the queue for {repo}{" "}
                      stops and grain files one task to fix it — which is the
                      control working, on a branch nothing had judged before.
                      grain starts these one repository at a time, so ticking
                      several is several suites in a row rather than all at
                      once. It needs {repo}&rsquo;s CI mode set to &ldquo;Use
                      Grain CI&rdquo; above; unticking it stops grain watching
                      further branches but leaves one it is already watching
                      alone.
                    </Box>
                  </Typography>
                </Stack>
              </Stack>
            )}
          </Box>

          {/* The grain CI workflows this deployment declares for this repo
              (grain/task-245). The one place they can be written for a
              repository grain does not control: a `.grain/workflows`
              directory in somebody else's tree is a pull request they have
              to accept, and grain has nowhere else to look. What the
              repository declares in its own tree is not shown here and
              cannot be -- reading it means cloning a branch, which is a
              thing a run does and not a thing a settings form should wait
              on. A run's own job list is where the two are visible
              together. */}
          <Box>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              CI workflows
            </Typography>
            {workflows === null ? (
              <Typography variant="body2" color="text.secondary">
                Loading workflows…
              </Typography>
            ) : (
              <Stack spacing={2}>
                <Typography variant="body2" color="text.secondary">
                  Workflows grain declares for {repo}, in grain's own
                  GitHub-Actions-shaped format. They run alongside anything the
                  repository declares in its {workflows.dir}, and a workflow
                  here replaces one the repository declares at the same path.
                  Changes are saved automatically: a workflow that could not run
                  is refused here rather than at the run.
                </Typography>
                {workflowEdits.length === 0 ? (
                  <Typography variant="body2" color="text.secondary">
                    grain declares no workflows for this repo.
                  </Typography>
                ) : (
                  workflowEdits.map((w, i) => (
                    <Stack key={i} spacing={1}>
                      <Stack
                        direction="row"
                        spacing={1}
                        alignItems="flex-start"
                      >
                        <TextField
                          id={`${refusalId}-workflow-path-${i}`}
                          name={`repoWorkflowPath${i}`}
                          label="File"
                          placeholder="tests.yml"
                          helperText={`Under ${workflows.dir}, which is filled in for a bare file name.`}
                          value={w.path}
                          onChange={(e) =>
                            editWorkflow(i, "path", e.target.value)
                          }
                          onBlur={() => saveWorkflows(workflowEdits)}
                          autoComplete="off"
                          inputProps={{
                            ...identifierField,
                            "aria-describedby": refusedBy(
                              readOnly,
                              `${refusalId}-workflow-path-${i}-helper-text`,
                            ),
                          }}
                          fullWidth
                          size="small"
                          disabled={readOnly}
                        />
                        <Button
                          size="small"
                          color="error"
                          onClick={() => removeWorkflow(i)}
                          disabled={readOnly}
                          aria-describedby={refusedBy(readOnly)}
                          aria-label={`Remove workflow ${w.path.trim() || i + 1}`}
                          sx={{ mt: 0.5, whiteSpace: "nowrap" }}
                        >
                          Remove
                        </Button>
                      </Stack>
                      {/* A fixed label rather than the file's own path:
                          the path is being typed in the box above, and a
                          label that changed under somebody's cursor is a
                          field that reads as a different field every
                          keystroke. */}
                      <TextField
                        name={`repoWorkflowText${i}`}
                        label="Workflow"
                        value={w.text}
                        onChange={(e) =>
                          editWorkflow(i, "text", e.target.value)
                        }
                        onBlur={() => saveWorkflows(workflowEdits)}
                        multiline
                        minRows={6}
                        autoComplete="off"
                        inputProps={{
                          ...identifierField,
                          "aria-describedby": refusedBy(readOnly),
                        }}
                        fullWidth
                        size="small"
                        disabled={readOnly}
                      />
                    </Stack>
                  ))
                )}
                <Box>
                  <Button
                    size="small"
                    variant="outlined"
                    onClick={addWorkflow}
                    disabled={readOnly}
                    aria-describedby={refusedBy(readOnly)}
                  >
                    Add workflow
                  </Button>
                </Box>
              </Stack>
            )}
          </Box>

          <Box>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              CI capabilities
            </Typography>
            {caps === null ? (
              <Typography variant="body2" color="text.secondary">
                Loading CI capabilities…
              </Typography>
            ) : (
              <Stack spacing={1}>
                <FormControl fullWidth size="small">
                  <InputLabel id="repo-ci-capabilities-label">
                    CI capabilities
                  </InputLabel>
                  <Select
                    labelId="repo-ci-capabilities-label"
                    disabled={readOnly}
                    inputProps={{ "aria-describedby": refusedBy(readOnly) }}
                    label="CI capabilities"
                    multiple
                    value={ciCapsLegacy}
                    onChange={(e) => {
                      const picked =
                        typeof e.target.value === "string"
                          ? e.target.value.split(",")
                          : e.target.value;
                      const next = [...picked, ...ciCapsRefs];
                      setCICapsSelection(next);
                      saveCICapabilities(next);
                    }}
                    renderValue={(chosen) => (
                      <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5 }}>
                        {chosen.map((id) => (
                          <GrantChip
                            key={id}
                            grant={id}
                            config={config}
                            tabIndex={-1}
                          />
                        ))}
                      </Box>
                    )}
                  >
                    {ciCapsRows.length === 0 && (
                      <MenuItem disabled>{NO_CAPABILITIES_HINT}</MenuItem>
                    )}
                    {ciCapsRows.map((c) => (
                      <MenuItem key={c.id} value={c.id} title={c.description}>
                        <Checkbox
                          checked={ciCapsSelection.includes(c.id)}
                          size="small"
                        />
                        <ListItemText
                          primary={c.name}
                          secondary={
                            c.retired
                              ? c.description
                              : capabilityUnavailableHint(c)
                          }
                        />
                      </MenuItem>
                    ))}
                  </Select>
                  <FormHelperText>
                    Granted to every CI job in this repo, starting with the next
                    run. Each job gets its own credentials, which are revoked
                    when it finishes. Task defaults are configured separately.
                  </FormHelperText>
                </FormControl>
                <Box>
                  <Typography variant="body2" sx={{ mb: 0.5 }}>
                    CI grants
                  </Typography>
                  <GrantsPicker
                    config={{ ...config, capabilities: [] }}
                    grants={ciCapsSelection}
                    onApiChange={() => {}}
                    onSubmit={ciCapsMethods.onSubmit}
                    onRemove={ciCapsMethods.onRemove}
                    disabled={readOnly}
                    idPrefix="repo-ci"
                    runKind="ci"
                  />
                </Box>
              </Stack>
            )}
          </Box>
        </RepoTabPanel>

        <RepoTabPanel id="merge-queue" tab={tab}>
          <Box>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              Merge queue
            </Typography>
            {upToDate === null ? (
              <Typography variant="body2" color="text.secondary">
                Loading merge queue settings…
              </Typography>
            ) : (
              <Stack spacing={1.5}>
                <Stack direction="row" alignItems="flex-start" spacing={1}>
                  <Checkbox
                    size="small"
                    sx={{ mt: -0.5 }}
                    checked={Boolean(upToDate.requireBranchUpToDate)}
                    onChange={(e) => saveUpToDate(e.target.checked)}
                    disabled={readOnly}
                    inputProps={{
                      "aria-label": `Require branches to be up to date before merging into ${repo}`,
                      "aria-describedby": refusedBy(readOnly),
                    }}
                  />
                  <Typography variant="body2" color="text.secondary">
                    Require a branch to contain its base before the queue merges
                    it. Expensive: with this on, every branch open against{" "}
                    {repo} is re-tested each time its base moves — on this
                    deployment that was measured at four twenty-minute suites in
                    ninety minutes for one task. With it off, a branch that is
                    behind its base but merges cleanly is merged as it stands,
                    and grain stops the queue if its own CI on the base branch
                    goes red.
                  </Typography>
                </Stack>
                <Stack direction="row" alignItems="flex-start" spacing={1}>
                  <Checkbox
                    size="small"
                    sx={{ mt: -0.5 }}
                    checked={Boolean(upToDate.mergeOnGreen)}
                    onChange={(e) => saveMergeOnGreen(e.target.checked)}
                    disabled={readOnly}
                    inputProps={{
                      "aria-label": `Merge pull requests into ${repo} when green and clean`,
                      "aria-describedby": refusedBy(readOnly),
                    }}
                  />
                  <Typography variant="body2" color="text.secondary">
                    Merge when green and clean. A pull request whose required
                    checks have passed and that merges cleanly with its target
                    branch merges straight away, instead of waiting behind pull
                    requests ahead of it whose CI is still running. Those keep
                    their place and merge once their own checks pass. With it
                    off, {repo}&rsquo;s queue merges strictly in order.
                  </Typography>
                </Stack>
                <Stack direction="row" alignItems="flex-start" spacing={1}>
                  <Checkbox
                    size="small"
                    sx={{ mt: -0.5 }}
                    checked={Boolean(upToDate.baseRepairBypassesHumanGates)}
                    onChange={(e) =>
                      saveBaseRepairBypassesHumanGates(e.target.checked)
                    }
                    disabled={readOnly}
                    inputProps={{
                      "aria-label": `Base-branch repair PRs in ${repo} may merge without a human when their CI is green`,
                      "aria-describedby": refusedBy(readOnly),
                    }}
                  />
                  <Typography variant="body2" color="text.secondary">
                    Base-branch repair PRs may merge without a human when their
                    CI is green. While a base branch on {repo} is red, its
                    repair pull request merges on green CI even if it touches{" "}
                    <code>human: required</code> sensitive path triggers (such
                    as <code>.grain/workflows/**</code>) or trust-boundary
                    paths. Risk: an agent repairing a broken base branch can
                    land changes to workflow declarations or trust-boundary code
                    without human pre-merge sign-off; the security and trigger
                    review tasks are still filed, the bypass is recorded on the
                    task and pull request, and any review findings file a
                    follow-up task. Once the base branch is green again, the
                    exemption stops applying.
                  </Typography>
                </Stack>
                {/* The checks nothing pushed to a branch here clears
                    (grain/task-291). A list rather than a tick, because
                    what it names is individual checks -- and it is under
                    Merge queue rather than beside the CI forms below
                    because the only thing it changes is what the queue
                    spends on a red head. The sentence beside it has to say
                    the part the list cannot: that this is not an exemption.
                    An operator who reads it as "these stop holding the
                    merge" has been told the opposite of the truth, and
                    would reach for it for a flaky job. */}
                {unfixable === null ? (
                  <Typography variant="body2" color="text.secondary">
                    Loading unfixable checks…
                  </Typography>
                ) : (
                  <Stack spacing={1}>
                    <Typography variant="body2" sx={{ fontWeight: 600 }}>
                      Checks no commit here can clear
                    </Typography>
                    <Typography variant="body2" color="text.secondary">
                      Checks on {repo} that will never pass for a pull request
                      grain&rsquo;s agents author, whatever is pushed to the
                      branch — a CLA bot asserting who signed an agreement is
                      the case this exists for.{" "}
                      <strong>This does not exempt them.</strong> A pull request
                      with one of these red is still not merged, and a person
                      clears it or nothing merges; what grain stops spending is
                      the base merge and the repair run it would have made
                      trying to reach a green that is not reachable, saying so
                      on the row and in one comment instead. Not for a job that
                      is merely flaky: a real regression in a check named here
                      still holds the queue, which is exactly why naming one is
                      safe.
                    </Typography>
                    {unfixableChecks.length === 0 ? (
                      <Typography variant="body2" color="text.secondary">
                        grain treats every check {repo} reports as one a commit
                        could turn green.
                      </Typography>
                    ) : (
                      <Stack
                        direction="row"
                        spacing={1}
                        flexWrap="wrap"
                        useFlexGap
                      >
                        {unfixableChecks.map((check) => (
                          <Chip
                            key={check}
                            size="small"
                            label={check}
                            onDelete={
                              readOnly
                                ? undefined
                                : () =>
                                    saveUnfixableChecks(
                                      unfixableChecks.filter(
                                        (other) => !sameCheck(other, check),
                                      ),
                                    )
                            }
                            deleteIcon={
                              <span title={`Stop declaring ${check} unfixable`}>
                                ×
                              </span>
                            }
                            sx={{ fontFamily: "monospace" }}
                          />
                        ))}
                      </Stack>
                    )}
                    {unfixableError !== "" && (
                      <Alert severity="warning">{unfixableError}</Alert>
                    )}
                    <Stack
                      component="form"
                      direction={{ xs: "column", sm: "row" }}
                      spacing={1}
                      alignItems="flex-start"
                      onSubmit={addUnfixableCheck}
                    >
                      <TextField
                        id={`${refusalId}-unfixable`}
                        name="unfixableCheck"
                        label="Check that cannot pass"
                        placeholder="cla/google"
                        helperText={`The check's exact name, as the pull request reports it — matched without case, never by part of a name. ${grainCIName} cannot be named here: it judges this exact commit, so its verdict is always the branch's own.`}
                        value={unfixableDraft}
                        onChange={(e) => {
                          setUnfixableDraft(e.target.value);
                          setUnfixableError("");
                        }}
                        size="small"
                        autoComplete="off"
                        inputProps={{
                          ...identifierField,
                          "aria-describedby": refusedBy(
                            readOnly,
                            `${refusalId}-unfixable-helper-text`,
                          ),
                        }}
                        sx={{ flexGrow: 1 }}
                        disabled={readOnly}
                      />
                      <Button
                        type="submit"
                        variant="contained"
                        size="small"
                        disabled={readOnly || unfixableDraft.trim() === ""}
                        aria-describedby={refusedBy(readOnly)}
                        sx={{ mt: { sm: 0.5 }, whiteSpace: "nowrap" }}
                      >
                        Add
                      </Button>
                    </Stack>
                  </Stack>
                )}
              </Stack>
            )}
          </Box>
        </RepoTabPanel>

        <RepoTabPanel id="agents" tab={tab}>
          <Box>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              Default capabilities
            </Typography>
            {caps === null ? (
              <Typography variant="body2" color="text.secondary">
                Loading capabilities…
              </Typography>
            ) : (
              <Stack spacing={1}>
                <FormControl fullWidth size="small">
                  <InputLabel id="repo-capabilities-label">
                    Default capabilities
                  </InputLabel>
                  <Select
                    labelId="repo-capabilities-label"
                    disabled={readOnly}
                    inputProps={{
                      "aria-describedby": refusedBy(readOnly),
                    }}
                    label="Default capabilities"
                    multiple
                    value={capsLegacy}
                    onChange={(e) => {
                      const picked =
                        typeof e.target.value === "string"
                          ? e.target.value.split(",")
                          : e.target.value;
                      const next = [...picked, ...capsApi, ...capsRefs];
                      setCapsSelection(next);
                      saveCapabilities(next);
                    }}
                    renderValue={(chosen) => (
                      <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5 }}>
                        {chosen.map((id) => (
                          <GrantChip
                            key={id}
                            grant={id}
                            config={config}
                            tabIndex={-1}
                          />
                        ))}
                      </Box>
                    )}
                  >
                    {/* The same grantable-only listing Settings' own
                        default-capabilities picker offers, and for the same
                        reason: a default no task can be granted by hand
                        would fail at every filing, and PUT rejects one
                        anyway. It needs no filter of its own to say that,
                        unlike Settings' settings.capabilities:
                        config.capabilities *is* ui.OfferedCapabilities,
                        which is what "grantable" means, where that one
                        reports every provider grain ships and flags the
                        ungrantable ones. A capability the deployment
                        already defaults is still offered rather than
                        hidden -- ticking it here is how a repo keeps it if
                        the deployment-wide entry is later dropped -- and
                        says so in its own row, so nobody reads a blank box
                        as "not on here". What is left out is a capability
                        this deployment has not configured at all, which is
                        a default that would fail every task filed against
                        this repo (configuredCapabilities, state.js); one
                        this repo already names stays, warning and all. An
                        id this repo stored before the build retired it
                        gets a row too, purely so it can be unticked
                        (capabilityRows, state.js). */}
                    {capsRows.length === 0 && (
                      <MenuItem disabled>{NO_CAPABILITIES_HINT}</MenuItem>
                    )}
                    {capsRows.map((c) => (
                      <MenuItem key={c.id} value={c.id} title={c.description}>
                        <Checkbox
                          checked={capsSelection.includes(c.id)}
                          size="small"
                        />
                        <ListItemText
                          primary={c.name}
                          secondary={
                            c.retired
                              ? c.description
                              : /* A default this deployment cannot honour is worth
                               more warning here than on one task, not less: it
                               fails every task filed against this repo, and the
                               person who set it is not the one who sees them
                               fail. */
                                capabilityUnavailableHint(c) ||
                                ((
                                  caps.deploymentDefaultCapabilities || []
                                ).includes(c.id)
                                  ? "already a deployment default -- on here either way"
                                  : null)
                          }
                          secondaryTypographyProps={
                            capabilityUnavailableHint(c)
                              ? { color: "warning.main" }
                              : undefined
                          }
                        />
                      </MenuItem>
                    ))}
                  </Select>
                  <FormHelperText>
                    Added to this deployment&apos;s own defaults, never
                    subtracted from them -- a repo can only widen what a task
                    filed against it starts with. Whoever files one can still
                    untick any of these on the new-task form, and tasks already
                    filed keep what they were filed with.{" "}
                    {LEGACY_CAPABILITIES_HINT}
                  </FormHelperText>
                </FormControl>
                <Box>
                  <Typography variant="body2" sx={{ mb: 0.5 }}>
                    Default grants
                  </Typography>
                  <GrantsPicker
                    config={config}
                    grants={capsSelection}
                    onApiChange={(next) => {
                      const all = [...capsLegacy, ...next, ...capsRefs];
                      setCapsSelection(all);
                      saveCapabilities(all);
                    }}
                    onSubmit={capsMethods.onSubmit}
                    onRemove={capsMethods.onRemove}
                    disabled={readOnly}
                    idPrefix="repo-default"
                  />
                </Box>
                <Typography variant="body2" color="text.secondary">
                  A task filed against {repo} starts with:{" "}
                  {capsEffective.length === 0
                    ? "nothing -- only what whoever files it ticks"
                    : capsEffective
                        .map((id) => capabilityName(config, id))
                        .join(", ")}
                </Typography>
              </Stack>
            )}
          </Box>

          <Box>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              Prompt extension
            </Typography>
            {prompt === null ? (
              <Typography variant="body2" color="text.secondary">
                Loading prompt extension…
              </Typography>
            ) : (
              <Stack spacing={1}>
                <TextField
                  id={`${refusalId}-prompt`}
                  name="repoPromptExtension"
                  label={`Prompt extension for ${repo}`}
                  helperText="Added to this deployment's own standing instructions for a run against this repo, never replacing them. A task can replace both for itself (New task -> Advanced options). Leave empty for a repo that adds nothing."
                  value={promptText}
                  onChange={(e) => setPromptText(e.target.value)}
                  onBlur={(e) => {
                    const trimmed = e.target.value.trim();
                    if (trimmed !== storedPromptText) {
                      savePromptExtension(trimmed);
                    }
                  }}
                  multiline
                  minRows={4}
                  autoComplete="off"
                  inputProps={{
                    "aria-describedby": refusedBy(
                      readOnly,
                      `${refusalId}-prompt-helper-text`,
                    ),
                  }}
                  fullWidth
                  size="small"
                  disabled={readOnly}
                />
                {/* What the deployment already says, read-only and shown
                    whether or not this repo adds anything: text appended to
                    instructions nobody can see from here cannot be written
                    sensibly, and this is the layer somebody editing here is
                    appending to (ui.RepoDefaults.
                    DeploymentPromptExtension). */}
                <Typography variant="body2" color="text.secondary">
                  Deployment-wide, set in Settings &rarr; Agents:{" "}
                  {prompt.deploymentPromptExtension ? (
                    <Box component="span" sx={{ whiteSpace: "pre-wrap" }}>
                      {prompt.deploymentPromptExtension}
                    </Box>
                  ) : (
                    "nothing"
                  )}
                </Typography>
                {/* And the repository's own half, when it has one: text
                    the repository declared for itself in
                    `.grain/settings.yml` and grain appends after the box
                    above (ui.RepoDefaults.DeclaredPromptExtension,
                    grain/task-340). Shown for the same reason the
                    deployment layer above is -- a box whose effect is
                    being added to by a file nobody can see from here is
                    how a setting comes to look as though it is not
                    working -- and shown only when there is one, because
                    very nearly no repository declares anything and an
                    empty row for the absent case would be noise on every
                    other repo's page.

                    The composed text is worth its space only here too:
                    with one layer above the box, the order is just the
                    two in sequence, but with a declared layer as well
                    there are three and which one a run reads first stops
                    being obvious. It is what the server composed for the
                    text as stored (ui.RepoDefaults.
                    EffectivePromptExtension), not for whatever is being
                    typed into the box, and it is re-read on save. */}
                {prompt.declaredPromptExtension ? (
                  <>
                    <Typography variant="body2" color="text.secondary">
                      Declared by {repo} itself in .grain/settings.yml, added
                      after your text rather than replacing it, and not editable
                      here -- the file is, at the commit named under
                      Declarations below:{" "}
                      <Box component="span" sx={{ whiteSpace: "pre-wrap" }}>
                        {prompt.declaredPromptExtension}
                      </Box>
                    </Typography>
                    <Typography variant="body2" color="text.secondary">
                      What a run against {repo} is told, absent a task with
                      instructions of its own:{" "}
                      <Box component="span" sx={{ whiteSpace: "pre-wrap" }}>
                        {prompt.effectivePromptExtension}
                      </Box>
                    </Typography>
                  </>
                ) : null}
              </Stack>
            )}
          </Box>

          <Box>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              Setup command
            </Typography>
            {setup === null ? (
              <Typography variant="body2" color="text.secondary">
                Loading setup command…
              </Typography>
            ) : (
              <Stack spacing={1}>
                <TextField
                  id={`${refusalId}-setup`}
                  name="repoSetupCommand"
                  label={`Setup command for ${repo}`}
                  placeholder="make deps"
                  helperText="Shell run in the fresh checkout after the clone and before the agent's first turn -- and again whenever a run rebuilds its sandbox. Its exit status and the tail of its output go into the run's prompt; a run whose setup failed is told, not stopped. Leave empty for a repo that needs no setup."
                  value={setupText}
                  onChange={(e) => setSetupText(e.target.value)}
                  onBlur={(e) => {
                    const trimmed = e.target.value.trim();
                    if (trimmed !== storedSetupText) {
                      saveSetupCommand(trimmed);
                    }
                  }}
                  multiline
                  minRows={2}
                  autoComplete="off"
                  inputProps={{
                    ...identifierField,
                    "aria-describedby": refusedBy(
                      readOnly,
                      `${refusalId}-setup-helper-text`,
                    ),
                  }}
                  fullWidth
                  size="small"
                  disabled={readOnly}
                />
                {/* The repository's own setup command, when it declares
                    one (ui.RepoDefaults.DeclaredSetupCommand,
                    grain/task-340), shown read-only for the reason the
                    declared prompt extension above is shown. Both run,
                    in this order, as the two lines of one script
                    (model.RepoConfig.SetupScript): a repository cannot
                    discard the setup an operator decided it needs, and
                    the composed script is not shown here because nothing
                    in this response composes it -- putting the two
                    together in the browser would be a second composition
                    that could drift from the one a run is given. */}
                {setup.declaredSetupCommand ? (
                  <Typography variant="body2" color="text.secondary">
                    {repo} declares one of its own in .grain/settings.yml, run
                    after yours as the next line of the same script, and not
                    editable here -- the file is, at the commit named under
                    Declarations below:{" "}
                    <Box component="span" sx={{ whiteSpace: "pre-wrap" }}>
                      {setup.declaredSetupCommand}
                    </Box>
                  </Typography>
                ) : null}
              </Stack>
            )}
          </Box>
        </RepoTabPanel>

        <RepoTabPanel id="branches" tab={tab}>
          <Box>
            {/* A branch has a figure of its own now (grain/task-372,
                ItemGlyph.jsx) -- the stem that splits in two -- and this
                is the one page that keeps a list of them, so it is where
                the heading and every row below carry it. */}
            <Typography
              variant="subtitle2"
              sx={{ mb: 1, display: "flex", alignItems: "center", gap: 0.8 }}
            >
              <ItemGlyph kind="branches" size={20} />
              Branches
            </Typography>
            <Stack
              component="form"
              direction="row"
              spacing={1}
              alignItems="flex-start"
              onSubmit={addBranch}
            >
              <TextField
                id={`${refusalId}-branch`}
                name="branchName"
                label="Branch name"
                placeholder="feature/foo"
                helperText="Created from the repo's current default branch, or adopted as it stands if it already exists"
                autoComplete="off"
                inputProps={{
                  ...identifierField,
                  "aria-describedby": refusedBy(
                    readOnly,
                    `${refusalId}-branch-helper-text`,
                  ),
                }}
                required
                InputLabelProps={{ required: false }}
                size="small"
                fullWidth
                disabled={readOnly}
              />
              <Button
                type="submit"
                variant="outlined"
                size="small"
                disabled={readOnly}
                aria-describedby={refusedBy(readOnly)}
                sx={{ whiteSpace: "nowrap", mt: 0.25 }}
              >
                Add branch
              </Button>
            </Stack>
            {branchNote && (
              <Typography variant="body2" role="status" sx={{ mt: 1 }}>
                {branchNote}
              </Typography>
            )}
            {branches.length > 0 && (
              <ul
                className="candidate-history"
                style={{ marginTop: "0.75rem" }}
              >
                {branches.map((b) => (
                  <li key={b.name}>
                    <GlyphLabel kind="branches">
                      <strong>{b.name}</strong>
                    </GlyphLabel>{" "}
                    -- {b.status}
                    {b.error && (
                      <span className="candidate-error"> ({b.error})</span>
                    )}
                  </li>
                ))}
              </ul>
            )}

            <Typography variant="subtitle2" sx={{ mt: 2.5, mb: 1 }}>
              Compare branches
            </Typography>
            <Stack component="form" spacing={1} onSubmit={compareBranches}>
              <Stack direction={{ xs: "column", sm: "row" }} spacing={1}>
                {/* The repository's own branches, as GitHub lists them, ahead
                    of the ones grain created here (BranchField): a
                    comparison is between branches that exist, and grain's
                    list alone left out main and every release branch while
                    offering ones still pending creation. Only the head
                    warns about a missing branch: the base starts filled
                    in, and its check would ask GitHub on every visit to
                    this page, since every tab stays mounted. Each sits in
                    a Box taking its share of the row, as the two text boxes
                    it replaced did: BranchField's own root is a plain
                    block. */}
                <Box sx={{ flex: { sm: 1 }, minWidth: 0 }}>
                  <BranchField
                    name="compareBase"
                    label="Base branch"
                    repo={repo}
                    value={compareBase}
                    onChange={setCompareBase}
                    required
                  />
                </Box>
                <Box sx={{ flex: { sm: 1 }, minWidth: 0 }}>
                  <BranchField
                    name="compareHead"
                    label="Head branch"
                    placeholder=""
                    repo={repo}
                    value={compareHead}
                    onChange={setCompareHead}
                    required
                    warnMissing="there is nothing to compare"
                  />
                </Box>
                <Button
                  type="submit"
                  variant="contained"
                  size="small"
                  sx={{ whiteSpace: "nowrap", px: 2.5 }}
                >
                  Compare
                </Button>
              </Stack>
            </Stack>
          </Box>
        </RepoTabPanel>

        <RepoTabPanel id="releases" tab={tab}>
          {/* Who runs this repository's standing work, directly above the
              pane that writes one item of it (grain/task-542): the release
              switch below is the release item's own override, and this is
              the repository default it is an exception to, with every
              other item beside it. Reading them apart is how an operator
              finds out that publishing is off because the repository was
              handed to another deployment, rather than because somebody
              switched releases off here. */}
          <RepoWorkOwnership
            repo={repo}
            readOnly={readOnly}
            describedBy={refusedBy(readOnly)}
            refreshKey={releaseToggles}
            onSaved={() => setOwnershipSaves((n) => n + 1)}
            showError={showError}
          />

          <RepoReleaseWorkflowSettings
            repo={repo}
            config={config}
            readOnly={readOnly}
            describedBy={refusedBy(readOnly)}
            refreshKey={gateCheck + ownershipSaves}
            onOpenRun={onOpenCIRun}
            onRefreshConfig={onRefreshConfig}
            onToggled={() => setReleaseToggles((n) => n + 1)}
            showError={showError}
          />
        </RepoTabPanel>

        <RepoTabPanel id="advanced" tab={tab}>
          {/* The templates, suites and schedules this repository declares
              in its own `.grain` directory (grain/task-60). Off unless
              somebody here turns it on: a declared schedule is a standing
              claim on this deployment's agent capacity, written in a
              repository every run grain dispatches there can rewrite. What
              one may then say is bounded in grain itself -- what work to do
              in this repository and when, never what that work may touch --
              which is why this is one switch and not a set of permissions.
              The status beside it is the half the switch cannot say: a
              repository that could not be read, or one that declares
              nothing, looks exactly like one nobody has polled yet. */}
          <Box>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              Declarations
            </Typography>
            {declarations === null ? (
              <Typography variant="body2" color="text.secondary">
                Loading declarations…
              </Typography>
            ) : (
              <Stack spacing={1}>
                <Stack direction="row" alignItems="flex-start" spacing={1}>
                  <Checkbox
                    size="small"
                    sx={{ mt: -0.5 }}
                    checked={Boolean(declarations.declarations)}
                    onChange={(e) => saveDeclarations(e.target.checked)}
                    disabled={readOnly}
                    inputProps={{
                      "aria-label": `Read ${repo}'s own declarations`,
                      "aria-describedby": refusedBy(readOnly),
                    }}
                  />
                  <Typography variant="body2" color="text.secondary">
                    Read the templates, suites, schedules and path triggers{" "}
                    {repo} declares in its own .grain/templates, .grain/suites,
                    .grain/schedules and .grain/triggers. Grain re-reads them
                    within a couple of minutes of every change, and they cannot
                    be edited here -- the files are. A declaration files work in{" "}
                    {repo} on a cadence; it can never widen what that work
                    reads, writes, or holds.
                  </Typography>
                </Stack>
                {declarations.declarationsStatus ? (
                  <>
                    {/* pre-line: a read refused for several files says
                      so one file to a line (repodecl.Load joins them
                      with newlines), and run together they were one
                      paragraph nobody could tell the files apart in. */}
                    <Typography
                      variant="body2"
                      color="text.secondary"
                      sx={{ whiteSpace: "pre-line" }}
                    >
                      {declarations.declarationsStatus.error
                        ? `Last read failed: ${declarations.declarationsStatus.error}` +
                          (declarations.declarationsStatus.commit
                            ? ` — still running what ${declarations.declarationsStatus.commit.slice(0, 12)} declared.`
                            : "")
                        : `Read at ${(declarations.declarationsStatus.commit || "").slice(0, 12) || "no commit"}: ` +
                          declaredSummary(declarations.declarationsStatus)}
                    </Typography>
                    {/* The trigger files by path: a Processes trigger row
                      is listed only by its file and opens this tab (§5.1),
                      so the file it names has to be here to be found. */}
                    {(declarations.declarationsStatus.triggers || []).length >
                      0 && (
                      <Typography
                        variant="body2"
                        color="text.secondary"
                        className="declared-triggers"
                      >
                        Path triggers:{" "}
                        {declarations.declarationsStatus.triggers.map(
                          (path, i) => (
                            <Fragment key={path}>
                              {i > 0 && ", "}
                              <code>{path}</code>
                            </Fragment>
                          ),
                        )}
                      </Typography>
                    )}
                    {/* What a failed read leaves running: the last good
                      commit's declarations, counted the way a clean read
                      is, so a read error does not hide the merge rules
                      and schedules still in force. */}
                    {declarations.declarationsStatus.error &&
                      declarations.declarationsStatus.commit && (
                        <Typography
                          variant="body2"
                          color="text.secondary"
                          className="declared-still-running"
                        >
                          {`Still running from ${declarations.declarationsStatus.commit.slice(0, 12)}: ` +
                            declaredSummary(declarations.declarationsStatus)}
                        </Typography>
                      )}
                    {/* What the sync took but partly ignored (grain/task-2107):
                      a file that loads with warnings otherwise looks exactly
                      like one that loaded clean. */}
                    {/* What its templates' runs may push beyond their own
                      branch (grain/task-2536): an access a repository grants
                      its own standing work, so it is said where the rest of
                      what it declares is. */}
                    {(declarations.declarationsStatus.pushes || []).map((p) => (
                      <Typography
                        key={p.path}
                        variant="body2"
                        color="text.secondary"
                        className="declared-pushes"
                      >
                        Tasks from <code>{p.path}</code> may also push:{" "}
                        {p.refs.map(declaredPushRef).join(", ")}
                      </Typography>
                    ))}
                    {(declarations.declarationsStatus.warnings || []).map(
                      (w) => (
                        <Typography
                          key={w}
                          variant="body2"
                          color="warning.main"
                        >
                          Warning: {w}
                        </Typography>
                      ),
                    )}
                  </>
                ) : declarations.declarations ? (
                  <Typography variant="body2" color="text.secondary">
                    Not read yet — grain picks this up on its next cycle.
                  </Typography>
                ) : null}
              </Stack>
            )}
          </Box>

          {/* Which branches are ported to one another, and which merges
              each direction carries (RepoDefaults.portDirections). Only
              read here: the operator's half is written through the
              grain/config state repository and the repository's half is
              its own .grain/settings.yml, so this has no control for any
              role. portWarnings names the port-types entries that cannot
              take effect, the operator's included, which the declarations
              status above does not show. */}
          {declarations &&
          ((declarations.pairedBranches || []).length > 0 ||
            (declarations.portDirections || []).length > 0) ? (
            <Box className="repo-ports">
              <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
                Ports
              </Typography>
              <Typography variant="body2" color="text.secondary">
                {(declarations.pairedBranches || []).length > 0
                  ? `Grain ports each merge between ${declarations.pairedBranches.join(", ")}`
                  : "No branches are paired"}
                {" — "}operator:{" "}
                {(declarations.portBranches || []).join(", ") || "none"};
                declared in .grain/settings.yml:{" "}
                {(declarations.declaredPortBranches || []).join(", ") || "none"}
                . Set by the operator in grain/config and by {repo}&apos;s own
                port-types; not editable here.
              </Typography>
              <Table size="small" aria-label="Port directions">
                <TableHead>
                  <TableRow>
                    <TableCell>Direction</TableCell>
                    <TableCell>Operator</TableCell>
                    <TableCell>Declared</TableCell>
                    <TableCell>Ports</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {(declarations.portDirections || []).map((d) => (
                    <TableRow key={`${d.from}->${d.to}`}>
                      <TableCell>
                        <code>
                          {d.from}-&gt;{d.to}
                        </code>
                      </TableCell>
                      <TableCell>
                        {(d.operatorTypes || []).join(", ") || "—"}
                      </TableCell>
                      <TableCell>
                        {(d.declaredTypes || []).join(", ") || "—"}
                      </TableCell>
                      <TableCell>
                        {!d.paired
                          ? "nothing (not a paired branch)"
                          : (d.types || []).length > 0
                            ? (d.types || []).join(", ")
                            : "every merge"}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              {(declarations.portWarnings || []).map((w) => (
                <Typography
                  key={w}
                  variant="body2"
                  color="warning.main"
                  className="port-warning"
                >
                  Warning: {w}
                </Typography>
              ))}
            </Box>
          ) : null}

          <Box>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              Remove repository
            </Typography>
            {row.configured && !row.configRepo ? (
              <Stack direction="row" spacing={1} alignItems="center">
                <Typography
                  variant="body2"
                  color="text.secondary"
                  sx={{ flex: 1 }}
                >
                  Takes {repo} off this deployment&apos;s target repos. Its
                  tasks and history stay.
                </Typography>
                <Button
                  size="small"
                  variant="outlined"
                  color="error"
                  onClick={removeRepo}
                  disabled={readOnly}
                  aria-describedby={refusedBy(readOnly)}
                >
                  Remove
                </Button>
              </Stack>
            ) : (
              <Typography variant="body2" color="text.secondary">
                {row.configRepo
                  ? "This is the deployment's own configuration repository and cannot be removed here."
                  : `${repo} is not one of this deployment's target repos.`}
              </Typography>
            )}
          </Box>
        </RepoTabPanel>
      </div>
    </main>
  );
}
