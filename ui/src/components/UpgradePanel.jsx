import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  Autocomplete,
  Box,
  Button,
  Checkbox,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControlLabel,
  LinearProgress,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import GrainMark from "./GrainMark.jsx";
import UpgradeReleaseNotes from "./UpgradeReleaseNotes.jsx";
import {
  formatElapsed,
  stepElapsedMs,
  upgradeProgress,
} from "../upgradeProgress.js";
import { identifierField } from "../identifierField.js";
import { fetchUpgradeTargets } from "../upgradeTargets.js";

// STATUS_POLL_MS is how often this panel re-fetches /api/upgrade while
// an upgrade is running -- fast enough that a restart landing (the
// daemon process, and this connection to it, both dying mid-poll) is
// noticed quickly, no faster than App.jsx's own task-list poll.
const STATUS_POLL_MS = 3000;

// Once an upgrade reports success, the process answering that response is
// still the old one: pkg/upgrade deliberately records PhaseOK immediately
// before it starts the restart command. Keep checking the process build stamp
// until the replacement answers, then reload the document so its HTML and
// frontend bundle come from that replacement too. This matters especially for
// installed mobile web apps, where there is no browser refresh control to fall
// back to.
const RESTART_POLL_MS = 1000;

const reloadPage = () => window.location.reload();

// CLOCK_TICK_MS is how often the elapsed times redraw while an upgrade
// is running. Nothing is fetched on this tick -- it only moves the
// clock on the step in progress, so that a build sitting on the same
// output line for four minutes still visibly counts, which is the
// difference between "slow" and "hung" for whoever is watching
// (grain/task-37).
const CLOCK_TICK_MS = 1000;

// STEP_MARKS is the glyph each of pkg/upgrade's step states gets. Plain
// text rather than icons: these sit inline in a dense list, and the
// state is also written out beside them for anything reading this
// aloud.
const STEP_MARKS = {
  pending: "○",
  running: "◐",
  done: "✓",
  failed: "✗",
  skipped: "–",
};

// MARK_SIZE is how big the brand mark is while an upgrade is running.
// Three times the sidebar's 32px, and deliberately: an upgrade is the
// one thing grain does to itself, it takes minutes, and this pane is
// what an operator sits and watches for those minutes with nothing else
// to do. The mark elsewhere is a status light at the edge of a busy
// screen; here it is the subject, so it is sized like one -- the
// loading screen's hero at 320px is the only other mark this pane's
// nearest relative, and 96px is as much of that as fits in a settings
// tab above a step list without pushing the list off the bottom.
//
// No sheet is recorded at this size (mark-sheet.js's SHEET_SIZES), so
// this one runs the live particle renderer, which is what GrainMark
// does at any unrecorded size. That is the right trade here rather than
// a third sheet to fetch and keep: this is a single mark, on screen
// only while an upgrade is in flight, on a pane whose whole job is to
// be looked at -- the case the pack's renderer was written for.
const MARK_SIZE = 96;

// bwsalmon/agents#396: lets an operator pick a branch and kick off an
// in-place upgrade -- checkout, containerised build, install, and
// (if this deployment configured one) a restart to bring the new binary
// up. GET /api/upgrade's own "enabled" flag says whether that is wired
// up on this deployment at all; when it isn't, this shows a note instead
// of a form that could only ever 404, the same convention SettingsOverlay/
// SecretsPanel already use for their own optional pieces.
//
// bwsalmon/agents#456: lives as a tab inside SettingsOverlay rather than
// its own top-level overlay, so it renders its content only -- the
// shared Overlay/Dialog chrome is SettingsOverlay's.
//
// grain/task-39: there are two pipelines behind the one button
// (pkg/upgrade's ModeImage and ModeSource), and this pane used to
// describe the source one to everybody -- a checkout to fetch into and a
// containerised build -- on deployments that have neither, which is
// every deployment scripts/setup.sh installs. GET /api/upgrade's "mode"
// says which one this deployment actually runs, and MODE_DESCRIPTIONS
// below is keyed off it.

// ModeDescription is what this deployment's Upgrade button is about to
// do, in the terms of the pipeline it actually runs. An unknown or
// absent mode -- an older daemon serving this UI, or the shape a test
// stubs -- gets the one sentence that is true of both rather than a
// confident description of the wrong one.
function ModeDescription({ mode }) {
  if (mode === "image") {
    return (
      <>
        Pulls the image CI published for the branch below, runs it once to prove
        it works, and then points this deployment at it and restarts. Nothing is
        built here, so a branch CI has published no image for cannot be upgraded
        onto; and a pulled image that fails that check changes nothing, leaving
        this deployment on the image it is already running.
      </>
    );
  }
  if (mode === "kube") {
    return (
      <>
        Runs the image CI published for the branch below as a throwaway pod, to
        prove this cluster can pull it and that what is inside it starts, and
        then repoints this deployment&apos;s own Deployment at it. Kubernetes
        does the restart; because the rollout replaces the pod rather than
        adding one, the UI is briefly down. An image that fails that check
        changes nothing, leaving this deployment on the image it is already
        running.
      </>
    );
  }
  if (mode === "source") {
    return (
      <>
        Fetches the branch below into this deployment's own checkout, rebuilds
        bin/grain with the containerised build, installs it over the running
        binary, and restarts to run it. A newly installed binary that fails its
        check is rolled back to the previous one.
      </>
    );
  }
  return <>Upgrades this deployment onto the branch below and restarts it.</>;
}

// RunningNow is where this deployment already is -- GET /api/upgrade's
// "running" (grain/task-50), which is the image its service starts on
// the image path and the commit its installed binary was built from on
// the source one.
//
// The pane described where the button would take a deployment and never
// where it was: an operator had to infer that from the last successful
// attempt's Detail string, which says nothing at all on a deployment
// whose last cut-over came from scripts/setup.sh rather than from this
// button. Rendered above the box for that reason -- it is the half of
// "from X to Y" you already have when you arrive.
//
// Absent when the daemon sent nothing (an older one, or a deployment
// whose ref file or install path cannot be read): one line fewer, rather
// than a confident "unknown".
function RunningNow({ running }) {
  if (!running || (!running.image && !running.commit)) return null;
  // Seven characters is the same shortening the sidebar's build stamp
  // uses; the full hash rides along as the title, since that is the
  // value that gets pasted into `git show`.
  const build = running.commit
    ? running.commit.slice(0, 7) + (running.modified ? "-dirty" : "")
    : "";
  return (
    <Typography
      variant="body2"
      color="text.secondary"
      sx={{ mb: 2 }}
      // The handle the browser test locates this by, the same way it
      // scopes the status list to dl.upgrade-status: "Now running" is a
      // phrase, and a phrase is not a thing a test should have to match
      // an ancestor chain against.
      className="upgrade-running"
    >
      Now running{" "}
      <Box
        component="span"
        title={running.commit || undefined}
        sx={{ fontFamily: "monospace", fontSize: "0.9em" }}
      >
        {running.image || build}
      </Box>
      {running.image && build ? ` (${build})` : ""}.
    </Typography>
  );
}

// UpgradeMark is the animated brand mark, big, while the upgrade is
// actually in flight -- the same "work is happening" figure the sidebar
// and the running task rows show, at the size this pane can afford.
//
// It says nothing the step list does not, and that is the point: the
// list is text that has to be read, and for most of an upgrade it is
// text that does not change -- a build sits on one output line for
// minutes at a time. The mark moves the whole while, so a glance says
// the deployment is still working on it rather than wedged, and the
// moment it stops moving is the moment the upgrade is over. It is also
// the last thing on screen before the daemon restarts under this page,
// which is the stretch with no answers coming at all.
//
// Only while running: a finished or failed upgrade is not work in
// progress, and a mark still cycling over a failure would say it was.
function UpgradeMark({ branch }) {
  return (
    <Box
      // The handle the tests locate this by, same convention as
      // .upgrade-running above.
      className="upgrade-mark"
      sx={{ display: "flex", justifyContent: "center", mt: 3, mb: 1 }}
    >
      <GrainMark
        size={MARK_SIZE}
        animated
        title={branch ? `grain — upgrading to ${branch}` : "grain — upgrading"}
      />
    </Box>
  );
}

// InFlightRuns is what pressing the button costs right now
// (grain/task-213). An upgrade ends in a restart, and a restart discards
// every run under way -- the agent's work so far is lost and the task is
// queued again -- so an operator deciding whether to do this now or in
// ten minutes is entitled to see the number before they decide.
//
// Nothing here prevents it: this is what upgrading means, and the runs
// that are already going cannot be saved by waiting for a better moment
// grain picks on its own. What grain does do is stop *new* runs starting
// for the duration, which is the sentence above the button rather than
// this warning.
//
// atRisk (task 1389) splits the count by what the restart does to each
// run: the ones whose CLI is a child of the daemon, or that are still
// setting up, are lost, and it says on which frameworks; the ones in a
// run host or detached in grain hostd are adopted by the next daemon.
// A daemon too old to send it gets the old sentence, which assumed every
// run was lost.
//
// Absent when nothing is running, and on a daemon too old to send the
// count at all: no warning is the honest answer to both.
function InFlightRuns({ count, atRisk }) {
  if (!count) return null;
  return (
    <Alert
      severity={atRisk && !atRisk.lost ? "info" : "warning"}
      sx={{ mb: 2 }}
      className="upgrade-in-flight"
    >
      {inFlightSentence(count, atRisk)}
    </Alert>
  );
}

// UpgradeInterrupts is the standing fact behind InFlightRuns: the agent
// CLIs whose runs, where they are set to run now, an upgrade interrupts
// rather than carries across the restart (codex, until a daemon can
// adopt its runs; any CLI set to run in the container), each with the
// daemon's own sentence saying why. Shown whether or not a run is in
// flight, so an operator learns it before the upgrade rather than from
// the last restart's report. Absent when every CLI survives, and on a
// daemon too old to say.
function UpgradeInterrupts({ notes }) {
  if (!notes?.length) return null;
  return (
    <Alert
      severity="info"
      role="note"
      sx={{ mb: 2 }}
      className="upgrade-interrupts"
    >
      <Box component="ul" sx={{ pl: 2.5, my: 0 }}>
        {notes.map((n) => (
          <li key={n.framework}>{n.note}</li>
        ))}
      </Box>
    </Alert>
  );
}

// byFramework is a list of {framework, count} as "claude 2, setup 1".
function byFramework(counts) {
  return (counts || []).map((c) => `${c.framework} ${c.count}`).join(", ");
}

// inFlightSentence is what InFlightRuns and the confirm dialog both say.
function inFlightSentence(count, atRisk) {
  const runs = count === 1 ? "1 run is" : `${count} runs are`;
  if (!atRisk) {
    return (
      `${runs} in flight. Upgrading restarts this deployment, which ` +
      `discards ${count === 1 ? "it" : "them"} \u2014 the work is lost and ` +
      `the ${count === 1 ? "task is" : "tasks are"} queued again.`
    );
  }
  const parts = [`${runs} in flight. Upgrading restarts this deployment.`];
  if (atRisk.lost) {
    parts.push(
      `${atRisk.lost} would be lost (${byFramework(atRisk.lostByFramework)}): ` +
        `${atRisk.lost === 1 ? "its CLI runs" : "their CLIs run"} inside ` +
        "the daemon, or have not started yet, so the restart stops " +
        `${atRisk.lost === 1 ? "it" : "them"} \u2014 the work is lost and ` +
        `the ${atRisk.lost === 1 ? "task is" : "tasks are"} queued again.`,
    );
  }
  if (atRisk.survive) {
    parts.push(
      `${atRisk.survive} ${atRisk.survive === 1 ? "is" : "are"} in a run ` +
        "host or grain hostd and would be adopted by the new daemon.",
    );
  }
  return parts.join(" ");
}

// inFlightWarning is the same fact as a sentence for confirm(), which
// takes text and not elements.
function inFlightWarning(count, atRisk) {
  return `${inFlightSentence(count, atRisk)} Upgrade anyway?`;
}

// sessionLabel names a run of a console session after its task, as the
// post-upgrade check does (orchestrator.SessionLabel): a lost one is a
// conversation somebody was in the middle of.
function sessionLabel(session) {
  if (!session) return "";
  if (session === "director") return " (the Director session)";
  return ` (console session ${session})`;
}

// LastRestart is what the restart that started this daemon did to the
// runs in flight across it (task 1389): the report the post-upgrade
// check is told, adopted and lost, the lost ones counted by framework
// -- including the runs whose CLI stopped with the daemon and the ones
// still in setup -- with each run and its reason one click further.
//
// Absent when the daemon sent none: nothing ran across the restart, or
// the daemon is too old to say.
function LastRestart({ report }) {
  if (!report) return null;
  const adopted = report.adopted || [];
  const lost = report.lost || [];
  const runs = [
    ...lost.map((r) => ({ ...r, kind: "lost" })),
    ...adopted.map((r) => ({ ...r, kind: "adopted" })),
  ];
  return (
    <Alert
      severity={lost.length ? "warning" : "success"}
      sx={{ mb: 2 }}
      className="upgrade-last-restart"
    >
      <strong>Last restart</strong>
      {report.at ? ` (${new Date(report.at).toLocaleString()})` : ""}:{" "}
      <span className="upgrade-last-restart-summary">
        {adopted.length} adopted, {lost.length} lost
        {lost.length > 0 && ` (${byFramework(report.lostByFramework)})`}
        {report.freed > 0 &&
          `, ${report.freed} unclaimed run host${report.freed === 1 ? "" : "s"} freed`}
        .
      </span>
      {runs.length > 0 && (
        <details className="upgrade-last-restart-runs">
          <summary>Runs</summary>
          <Box component="ul" sx={{ pl: 2.5, my: 0.5 }}>
            {runs.map((r) => (
              <li key={`${r.kind}-${r.run}`}>
                {r.kind}: run{" "}
                <a href={`/tasks/${encodeURIComponent(r.task)}`}>{r.run}</a>
                {sessionLabel(r.session)}
                {r.framework
                  ? ` on ${r.framework}`
                  : r.setup
                    ? " in setup"
                    : ""}
                {r.reason ? ` \u2014 ${r.reason}` : ""}
              </li>
            ))}
          </Box>
        </details>
      )}
    </Alert>
  );
}

// waitingFor is what a scheduled upgrade is still waiting on, in words:
// the runs an agent is working in and the jobs of grain's own CI, which
// are the two things a restart would throw away and make somebody do
// again. Empty when there is nothing left -- the upgrade starts on the
// deployment's next tick, which is seconds.
//
// It deliberately does not count the runs InFlightRuns counts above.
// That number is every live run, console sessions included, because it
// answers "what would pressing Upgrade right now cost"; a session is a
// run waiting for somebody to type, and waiting for one to end could
// mean waiting forever, so a scheduled upgrade does not.
function waitingFor({ busyRuns = 0, busyCiRuns = 0 }) {
  const parts = [];
  if (busyRuns) parts.push(busyRuns === 1 ? "1 run" : `${busyRuns} runs`);
  if (busyCiRuns)
    parts.push(busyCiRuns === 1 ? "1 CI run" : `${busyCiRuns} CI runs`);
  return parts.join(" and ");
}

// askedBy is who asked for the scheduled upgrade, in words, or null for
// one this deployment did not record a requester for (an older row, or a
// deployment whose actor is unset).
//
// It is here because the answer stopped being "the operator" once a task
// could schedule an upgrade for itself (grain/task-353, the self-upgrade
// capability): "asked for by operator" on a restart no operator asked
// for is worse than saying nothing, and this pane exists precisely to
// answer "why is this deployment about to restart". An agent is named by
// its task -- "task 353" -- since a bare id beside a branch name says
// nothing a reader can follow up.
//
// Plain text rather than a link to the task: this pane is inside the
// Settings overlay, which has no route to one, and the id is enough to
// search for.
function askedBy({ byKind, byId }) {
  if (!byId) return null;
  return byKind === "agent" ? `task ${byId}` : byId;
}

// ScheduledUpgrade is the upgrade this deployment is holding its queue
// and waiting to apply (grain/task-156) -- the thing the "when this
// deployment is idle" checkbox produces instead of an upgrade.
//
// It is the only part of this pane that describes an upgrade that has
// not started, so it says the three things nothing else can: who asked
// for it, what it is still waiting for, and how to call it off. The
// banner elsewhere already says the queue is held and why; this says
// when that ends.
function ScheduledUpgrade({ scheduled, onCancel, cancelling }) {
  if (!scheduled) return null;
  const waiting = waitingFor(scheduled);
  const asked = askedBy(scheduled);
  return (
    <Alert
      severity="info"
      className="upgrade-scheduled"
      sx={{ mb: 2 }}
      action={
        <Button
          color="inherit"
          size="small"
          onClick={onCancel}
          disabled={cancelling}
        >
          Cancel
        </Button>
      }
    >
      Waiting to upgrade to <strong>{scheduled.branch}</strong>
      {asked ? <>, asked for by {asked}</> : null}. No new task is being picked
      up;{" "}
      {waiting
        ? `${waiting} still to finish, and the upgrade starts when they do.`
        : "nothing is running, so the upgrade starts within seconds."}
    </Alert>
  );
}

const defaultShowError = (err) => console.error(err);

// TARGET_GROUPS is the heading each kind of GET /api/upgrade/targets
// entry is listed under (pkg/upgrade/targets.go's TargetKind).
const TARGET_GROUPS = {
  channel: "Standard branches",
  version: "Released versions",
  allowed: "Allowed on this deployment",
};

// CHANNEL_NOTES is what each moving name means, beside it in the list --
// the same three facts the box's helper text states (grain/task-1681).
const CHANNEL_NOTES = {
  latest: "the last promoted release",
  candidate: "the current release candidate (alias: rc)",
  main: "the head of main",
};

function nextRCVersion(running) {
  const m = /^(.+-rc\.)(\d+)(.*)$/.exec(running || "");
  if (!m) return "the next candidate";
  return `${m[1]}${Number(m[2]) + 1}${m[3]}`;
}

function DiscardedRCAlert({ discardedRc, onSelectBranch, disabled }) {
  if (!discardedRc?.running) return null;
  const { running, latest, replacementRc } = discardedRc;
  const latestLabel = latest ? `latest (${latest})` : "latest";
  return (
    <Alert severity="warning" className="upgrade-discarded-rc" sx={{ mb: 2 }}>
      <Box>
        {replacementRc ? (
          <>
            <strong>Release candidate {running} was discarded</strong> and
            replaced by <strong>{replacementRc}</strong>. This deployment is
            staying on <code>{running}</code> (it will never downgrade on its
            own). Upgrade to <strong>candidate</strong> (
            <code>{replacementRc}</code>) or move to <strong>latest</strong>
            {latest ? (
              <>
                {" "}
                (<code>{latest}</code>)
              </>
            ) : null}
            .
          </>
        ) : (
          <>
            <strong>Release candidate {running} was discarded.</strong> It
            failed qualification during soak testing and will not be promoted,
            and no replacement candidate has been cut yet. This deployment is
            staying on <code>{running}</code> (it will never downgrade on its
            own). You can wait for <code>{nextRCVersion(running)}</code>, or
            move to <strong>latest</strong>
            {latest ? (
              <>
                {" "}
                (<code>{latest}</code>)
              </>
            ) : null}
            .
          </>
        )}
      </Box>
      <Stack
        direction="row"
        spacing={1}
        useFlexGap
        sx={{ mt: 1, flexWrap: "wrap", alignItems: "center" }}
      >
        {replacementRc && (
          <Button
            color="inherit"
            size="small"
            variant="outlined"
            disabled={disabled}
            onClick={() => onSelectBranch("candidate")}
          >
            {`Upgrade to candidate (${replacementRc})`}
          </Button>
        )}
        <Button
          color="inherit"
          size="small"
          variant={replacementRc ? "text" : "outlined"}
          disabled={disabled}
          onClick={() => onSelectBranch("latest")}
        >
          {`Move to ${latestLabel}`}
        </Button>
      </Stack>
    </Alert>
  );
}

// targetName is an Autocomplete value's branch, whether it is one of
// the listed targets or a branch typed by hand (freeSolo hands over the
// bare string for those).
const targetName = (option) =>
  typeof option === "string" ? option : option?.name || "";

// BranchPicker is the one field that says where an upgrade goes
// (grain/task-1914): a drop-down of the standard places -- latest,
// candidate, main and the released versions -- for the ordinary case,
// and still a box that takes any other branch typed into it, for the
// branch somebody wants to try out.
//
// The list is fetched the first time it is opened rather than with the
// pane: listing versions asks the image registry, and most visits to
// this tab never open it. A target this deployment's
// -upgrade-allowed-branches would refuse is listed but cannot be picked,
// so its absence from the allowlist is said where it is noticed rather
// than as a 400 after pressing Upgrade.
function BranchPicker({
  branch,
  onChange,
  placeholder,
  disabled,
  onTargetsLoaded,
}) {
  const [targets, setTargets] = useState(null);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const fetched = useRef(false);

  const load = async () => {
    if (fetched.current) return;
    fetched.current = true;
    setLoading(true);
    try {
      const next = await fetchUpgradeTargets();
      setTargets(next);
      onTargetsLoaded?.(next);
    } catch (err) {
      // An older daemon has no such route. The box still takes a typed
      // branch, which is all this pane offered before the list existed.
      // Anything else -- a proxy's 502, a dropped connection -- is said,
      // as a registry that could not be asked is, rather than leaving an
      // empty list and an invitation to pick from it.
      setTargets({
        targets: [],
        listError: err?.status === 404 ? "" : err?.message || String(err),
      });
    } finally {
      setLoading(false);
    }
  };

  const options = targets?.targets || [];
  const allowed = targets?.allowedBranches || [];
  const anyBranch = allowed.includes("**");
  let helperText =
    "latest is the last promoted release, main is the head of main, and candidate (or rc) is the current release candidate.";
  if (targets?.listError) {
    helperText += ` The list of branches could not be loaded (${targets.listError}); type a branch instead.`;
  } else if (targets && !anyBranch && allowed.length > 0) {
    helperText += ` Pick one from the list or type any branch; this deployment only upgrades to ${allowed.join(", ")} (-upgrade-allowed-branches).`;
  } else if (targets) {
    helperText += " Pick one from the list or type any branch.";
  }
  if (targets?.versionsError) {
    helperText += ` Released versions could not be listed: ${targets.versionsError}`;
  }

  return (
    <Autocomplete
      freeSolo
      // Without this, typing a branch that happens to be a prefix of a
      // listed one and pressing Enter would pick the listed one.
      autoHighlight={false}
      open={open}
      onOpen={() => {
        setOpen(true);
        load();
      }}
      onClose={() => setOpen(false)}
      options={options}
      loading={loading}
      loadingText="Listing branches…"
      groupBy={(option) => TARGET_GROUPS[option.kind] || ""}
      getOptionLabel={targetName}
      getOptionDisabled={(option) => !option.allowed}
      isOptionEqualToValue={(option, value) =>
        option.name === targetName(value)
      }
      // Opened on a box that already holds a branch -- the one this
      // deployment is on, say -- the list shows everything rather than
      // only the entries that branch is a prefix of: the point of opening
      // it is to see the other choices.
      filterOptions={(opts, { inputValue }) => {
        const q = inputValue.trim().toLowerCase();
        if (q === "" || opts.some((o) => o.name.toLowerCase() === q)) {
          return opts;
        }
        return opts.filter((o) => o.name.toLowerCase().includes(q));
      }}
      renderOption={(props, option) => {
        const { key, ...rest } = props;
        const note = !option.allowed
          ? "not allowed on this deployment"
          : CHANNEL_NOTES[option.name];
        return (
          <li key={key} {...rest}>
            <Box component="span" sx={{ fontFamily: "monospace" }}>
              {option.name}
            </Box>
            {note && (
              <Typography
                component="span"
                variant="body2"
                color="text.secondary"
                sx={{ ml: 1 }}
              >
                — {note}
              </Typography>
            )}
          </li>
        );
      }}
      value={branch}
      inputValue={branch}
      onInputChange={(_event, value, reason) => onChange(value, reason)}
      onChange={(_event, value, reason) => onChange(targetName(value), reason)}
      disabled={disabled}
      fullWidth
      renderInput={(params) => (
        <TextField
          {...params}
          name="branch"
          label="Branch"
          placeholder={placeholder}
          helperText={helperText}
          inputProps={{
            ...params.inputProps,
            ...identifierField,
            autoComplete: "off",
          }}
          required
          InputLabelProps={{ ...params.InputLabelProps, required: false }}
          margin="normal"
        />
      )}
    />
  );
}

// tagForBranch is pkg/upgrade's TagForBranch: the tag CI publishes for a
// branch, each '/' written as "..".
function tagForBranch(branch) {
  return branch.replaceAll("/", "..");
}

// hasTagOrDigest reports whether an image reference already names a build
// (repo:tag or repo@sha256:...) rather than only a repository.
function hasTagOrDigest(ref) {
  return ref.includes("@") || imageTagOf(ref) !== "";
}

// resolvedImage is the reference an upgrade onto branch would pull: the
// operator's image as typed when it names a build, otherwise branch's tag
// in the chosen repository, or in the one this deployment pulls from.
export function resolvedImage(image, repo, branch) {
  const chosen = (image || "").trim();
  if (chosen && hasTagOrDigest(chosen)) return chosen;
  const from = chosen || repo || "";
  const target = (branch || "").trim();
  if (!from || !target) return from;
  return `${from}:${tagForBranch(target)}`;
}

// ImageChooser is where an image deployment says what the Upgrade button
// will pull -- GET /api/upgrade's imageRepo, with the branch's tag -- and
// lets an operator pull from another repository the daemon allows
// (imageRepos), or name one build outright. Left empty, the upgrade pulls
// what it always did.
function ImageChooser({
  status,
  branch,
  image,
  onChange,
  disabled,
  scheduling,
}) {
  if (!status?.imageRepo) return null;
  const repos = status.imageRepos || [];
  const will = resolvedImage(scheduling ? "" : image, status.imageRepo, branch);
  return (
    <Box className="upgrade-image" sx={{ mt: 1 }}>
      <TextField
        name="image"
        label="Image (optional)"
        value={image}
        onChange={(e) => onChange(e.target.value)}
        placeholder={status.imageRepo}
        helperText={
          scheduling
            ? "A scheduled upgrade pulls from the repository above; choose an image only for an upgrade started now."
            : "A repository to pull this branch's tag from, or one build: repo/image:tag or repo/image@sha256:…"
        }
        inputProps={{ ...identifierField, autoComplete: "off" }}
        disabled={disabled || scheduling}
        fullWidth
        margin="dense"
      />
      {repos.length > 1 && (
        <Stack
          direction="row"
          spacing={1}
          useFlexGap
          flexWrap="wrap"
          sx={{ mt: 0.5 }}
        >
          {repos.map((repo) => (
            <Chip
              key={repo}
              size="small"
              variant={
                (image.trim() || status.imageRepo) === repo
                  ? "filled"
                  : "outlined"
              }
              label={
                repo === status.defaultImageRepo ? `${repo} (default)` : repo
              }
              onClick={() => onChange(repo === status.imageRepo ? "" : repo)}
              disabled={disabled || scheduling}
            />
          ))}
        </Stack>
      )}
      {will && (
        <Typography
          variant="body2"
          className="upgrade-image-ref"
          sx={{ mt: 1, overflowWrap: "anywhere" }}
        >
          Will pull <code>{will}</code>
        </Typography>
      )}
    </Box>
  );
}

function imageTagOf(ref) {
  if (!ref || typeof ref !== "string") return "";
  const [withoutDigest] = ref.split("@");
  const slash = withoutDigest.lastIndexOf("/");
  const colon = withoutDigest.lastIndexOf(":");
  if (colon > slash) return withoutDigest.slice(colon + 1);
  return "";
}

function imageDigestOf(ref) {
  if (!ref || typeof ref !== "string") return "";
  const idx = ref.indexOf("@");
  if (idx < 0) return "";
  const digest = ref.slice(idx + 1).trim();
  return digest.startsWith("sha256:") && digest.length > "sha256:".length
    ? digest
    : "";
}

function isImmutableTarget(target) {
  return (
    /^v\d+\.\d+\.\d+(-rc\.\d+)?$/.test(target) ||
    /^candidate-\d{8}-[0-9a-f]{7,40}$/.test(target) ||
    target.startsWith("sha-")
  );
}

// isSameRunningVersion reports whether the selected target is already
// known on the client to carry the version this deployment runs.
function isSameRunningVersion(target, status, version, preview) {
  if (!target) return false;
  if (preview && preview.target === target && preview.data) {
    const { from, version: targetVersion, ref } = preview.data;
    if (from && targetVersion && from === targetVersion) {
      return true;
    }
    const runningDigest = imageDigestOf(status?.running?.image);
    const previewDigest = imageDigestOf(ref);
    if (runningDigest && previewDigest && runningDigest === previewDigest) {
      return true;
    }
  }
  const runningRelease = status?.running?.release || version?.release || "";
  if (runningRelease && target === runningRelease) {
    return true;
  }
  const runningTag = imageTagOf(status?.running?.image);
  if (runningTag && target === runningTag && isImmutableTarget(target)) {
    return true;
  }
  const runningCommit =
    (!status?.running?.modified && status?.running?.commit) ||
    version?.commit ||
    "";
  if (
    runningCommit &&
    (target === runningCommit ||
      (runningCommit.length >= 7 &&
        target === `sha-${runningCommit.slice(0, 7)}`))
  ) {
    return true;
  }
  return false;
}

export default function UpgradePanel({
  showError = defaultShowError,
  settings,
  onSaveSettings,
  version,
  onRestart = reloadPage,
}) {
  const reportError =
    typeof showError === "function" ? showError : defaultShowError;
  const [status, setStatus] = useState(null);
  const [targetsData, setTargetsData] = useState(null);
  const [branch, setBranch] = useState("");
  // image is the operator's choice of what to pull instead of the
  // branch's tag in the configured repository (ImageChooser); "" for
  // the usual.
  const [image, setImage] = useState("");
  // chosen is whether the operator has picked or typed a target, which
  // is when what it brings is previewed (UpgradeReleaseNotes).
  const [chosen, setChosen] = useState(false);
  const [preview, setPreview] = useState(null);
  const [unchangedBranch, setUnchangedBranch] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  // Whether the button schedules an upgrade rather than starting one.
  // Not remembered across visits: "upgrade when this is quiet" is a
  // decision about this deployment right now, and a checkbox that was
  // still ticked from last week would be one nobody meant to tick.
  const [afterTasksFinish, setAfterTasksFinish] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  // Why the last status read failed, shown only while there is no status
  // at all: a read that fails mid-upgrade is the restart it ends in, and
  // the pane keeps what it last knew through that.
  const [loadError, setLoadError] = useState("");
  const [now, setNow] = useState(() => Date.now());
  const polling = useRef(false);
  const sawRunning = useRef(false);

  const refresh = useCallback(async () => {
    if (polling.current) return;
    polling.current = true;
    try {
      const next = await api("/api/upgrade");
      if (next.phase === "running") sawRunning.current = true;
      setStatus(next);
      setLoadError("");
      // The channel this deployment follows (latest, candidate, main, or
      // a branch), read off the image it runs, is what the next upgrade
      // defaults to: a deployment installed on latest is offered the
      // next release, not main's head. Without one -- a deployment
      // pinned to a single build -- a successful status names the branch
      // its current version came from, which is the next best guess. A
      // failed attempt names a version that never landed, so it must not
      // become the default.
      if (next.channel) {
        setBranch((prev) => prev || next.channel);
      } else if (next.phase === "ok") {
        setBranch((prev) => prev || next.branch || "");
      } else if (next.running?.branch) {
        setBranch((prev) => prev || next.running.branch);
      }
    } catch (err) {
      setLoadError(err?.message || String(err));
      reportError(err);
    } finally {
      polling.current = false;
    }
  }, [reportError]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // Polled while an upgrade is running, and while one is merely waiting
  // to: a scheduled upgrade's countdown is the run counts in that
  // reading, and the moment it does start is a "running" this pane has
  // to notice for itself -- nothing on this page asked for it, so
  // nothing else would.
  useEffect(() => {
    if (!status || (status.phase !== "running" && !status.scheduled)) return;
    const interval = setInterval(refresh, STATUS_POLL_MS);
    return () => clearInterval(interval);
  }, [status, refresh]);

  useEffect(() => {
    const currentCommit = version?.commit;
    if (
      !sawRunning.current ||
      status?.phase !== "ok" ||
      status?.unchanged ||
      !currentCommit
    ) {
      return;
    }
    let stopped = false;

    const probe = async () => {
      try {
        // The query makes this a fresh request even through an eager mobile
        // browser or proxy cache. api() otherwise leaves caching to fetch for
        // ordinary (non-poll) GETs.
        const config = await api(`/api/config?after-upgrade=${Date.now()}`);
        const replacementCommit = config?.version?.commit;
        const replacement =
          replacementCommit && replacementCommit !== currentCommit;
        if (!stopped && replacement) {
          stopped = true;
          onRestart();
        }
      } catch {
        // Downtime is the expected middle of a restart. The next interval
        // keeps probing until the replacement process is ready.
      }
    };

    probe();
    const interval = setInterval(probe, RESTART_POLL_MS);
    return () => {
      stopped = true;
      clearInterval(interval);
    };
  }, [status?.phase, status?.unchanged, version?.commit, onRestart]);

  useEffect(() => {
    if (!status || status.phase !== "running") return;
    const tick = setInterval(() => setNow(Date.now()), CLOCK_TICK_MS);
    return () => clearInterval(tick);
  }, [status]);

  const executeUpgrade = async (targetBranch) => {
    const trimmed = (targetBranch ?? branch).trim();
    if (trimmed === "") return;
    setUnchangedBranch("");
    try {
      const chosenImage = afterTasksFinish ? "" : image.trim();
      const next = await api("/api/upgrade", {
        method: "POST",
        body: JSON.stringify(
          chosenImage
            ? { branch: trimmed, image: chosenImage, afterTasksFinish }
            : { branch: trimmed, afterTasksFinish },
        ),
      });
      if (next.phase === "running") sawRunning.current = true;
      setStatus(next);
    } catch (err) {
      reportError(err);
    }
  };

  const submit = async (evt) => {
    evt.preventDefault();
    const trimmed = branch.trim();
    if (trimmed === "") return;
    // An image named outright is the daemon's to compare, by digest.
    const chosenImage = afterTasksFinish ? "" : image.trim();
    if (
      !chosenImage &&
      isSameRunningVersion(trimmed, status, version, preview)
    ) {
      setUnchangedBranch(trimmed);
      return;
    }
    // Only when there is something to lose, and only when the upgrade is
    // about to happen: an upgrade on an idle deployment costs nothing
    // but the downtime, a scheduled one costs nothing at all because
    // waiting for those runs to finish is exactly what it does, and a
    // confirmation dialog that always appears is one nobody reads by the
    // time it matters.
    if (!afterTasksFinish && status?.inFlightRuns > 0) {
      setConfirmOpen(true);
      return;
    }
    await executeUpgrade(trimmed);
  };

  const cancelScheduled = async () => {
    setCancelling(true);
    try {
      setStatus(await api("/api/upgrade/scheduled", { method: "DELETE" }));
    } catch (err) {
      reportError(err);
    } finally {
      setCancelling(false);
    }
  };

  if (status === null) {
    return loadError ? (
      <Alert severity="error">
        This deployment&apos;s upgrade status could not be read ({loadError}).
        Reopen this tab to try again.
      </Alert>
    ) : null;
  }

  const running = status.phase === "running";
  const progress = upgradeProgress(status.steps);
  const unchangedTarget =
    unchangedBranch || (!running && status.unchanged ? status.branch : "");

  return (
    <>
      {!status.enabled && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Not available: this deployment was started with neither -upgrade-image
          nor -upgrade-src-dir configured (bwsalmon/agents#396, #645), so there
          is nothing here that can cut it over to another branch.
        </Alert>
      )}
      {status.enabled && (
        <>
          <Typography
            variant="body2"
            color="text.secondary"
            sx={{ mt: -1, mb: 2 }}
          >
            <ModeDescription mode={status.mode} /> One upgrade at a time; each
            step below reports itself as it goes. A release that changes the
            database schema is carried across rather than refused: this
            deployment&rsquo;s rows are written out, the arriving build is asked
            to import them before anything cuts over, and it rebuilds the store
            from them at its first start &mdash; keeping the old one, moved
            aside, for whatever the new schema has nowhere to put. The queue is
            held for the duration &mdash; the banner will say this deployment is
            paused, and the restart, or the upgrade failing, lifts it &mdash;
            since a run started now would only be discarded by the restart this
            ends in.
          </Typography>
          <RunningNow running={status.running} />
          <DiscardedRCAlert
            discardedRc={status.discardedRc || targetsData?.discardedRc}
            disabled={running}
            onSelectBranch={(next) => {
              setBranch(next);
              setUnchangedBranch("");
              setChosen(true);
            }}
          />
          <LastRestart report={status.lastRestart} />
          <ScheduledUpgrade
            scheduled={status.scheduled}
            onCancel={cancelScheduled}
            cancelling={cancelling}
          />
          {!afterTasksFinish && (
            <InFlightRuns count={status.inFlightRuns} atRisk={status.atRisk} />
          )}
          <UpgradeInterrupts notes={status.upgradeInterrupts} />
          <Dialog
            open={confirmOpen}
            onClose={() => setConfirmOpen(false)}
            aria-labelledby="confirm-upgrade-dialog-title"
          >
            <DialogTitle id="confirm-upgrade-dialog-title">
              Confirm upgrade
            </DialogTitle>
            <DialogContent>
              <DialogContentText>
                {inFlightWarning(status.inFlightRuns, status.atRisk)}
              </DialogContentText>
            </DialogContent>
            <DialogActions>
              <Button onClick={() => setConfirmOpen(false)}>Cancel</Button>
              <Button
                variant="contained"
                color="primary"
                onClick={async () => {
                  setConfirmOpen(false);
                  await executeUpgrade(branch);
                }}
              >
                Upgrade
              </Button>
            </DialogActions>
          </Dialog>
          <form onSubmit={submit}>
            <BranchPicker
              branch={branch}
              onTargetsLoaded={setTargetsData}
              onChange={(next, reason) => {
                setBranch(next);
                setUnchangedBranch("");
                // Only a branch the operator chose is previewed: the one
                // the box is filled with on opening is where this
                // deployment already is, and previewing it pulls an image.
                if (reason !== "reset") setChosen(true);
              }}
              placeholder={
                status?.channel || status?.running?.branch || "latest"
              }
              disabled={running}
            />
            <ImageChooser
              status={status}
              branch={branch}
              image={image}
              onChange={(next) => {
                setImage(next);
                setUnchangedBranch("");
              }}
              disabled={running}
              scheduling={afterTasksFinish}
            />
            {!running && chosen && (
              <UpgradeReleaseNotes
                branch={branch}
                onPreview={(target, data) => setPreview({ target, data })}
              />
            )}
            <FormControlLabel
              control={
                <Checkbox
                  name="afterTasksFinish"
                  checked={afterTasksFinish}
                  disabled={running}
                  onChange={(e) => setAfterTasksFinish(e.target.checked)}
                />
              }
              label={
                <>
                  Wait until this deployment is idle
                  <span className="hint">
                    off: upgrade now, discarding whatever is running. on: stop
                    picking new tasks up, let the work already under way finish,
                    and upgrade then. A task waiting on you, sitting in the
                    merge queue or already done is not waited for; neither is an
                    open session. Cancel it at any time.
                  </span>
                </>
              }
              sx={{ display: "flex", mt: 1 }}
            />
            <Stack direction="row" justifyContent="flex-end" sx={{ mt: 2 }}>
              <Button type="submit" variant="contained" disabled={running}>
                {running
                  ? "Upgrading…"
                  : afterTasksFinish
                    ? "Schedule upgrade"
                    : "Upgrade"}
              </Button>
            </Stack>
          </form>
          {running && <UpgradeMark branch={status.branch} />}
          {unchangedTarget && (
            <Alert severity="info" className="upgrade-unchanged" sx={{ mt: 2 }}>
              The version on {unchangedTarget} has not changed, so this
              deployment was not upgraded.
            </Alert>
          )}
          {status.phase && (
            <dl className="upgrade-status">
              <dt>Branch</dt>
              <dd>{status.branch}</dd>
              <dt>Status</dt>
              <dd className={`upgrade-phase upgrade-phase-${status.phase}`}>
                {status.phase}
              </dd>
              {status.detail && (
                <>
                  <dt>Detail</dt>
                  <dd>{status.detail}</dd>
                </>
              )}
            </dl>
          )}
          {progress.total > 0 && (
            <UpgradeSteps steps={status.steps} progress={progress} now={now} />
          )}
        </>
      )}
      {settings && onSaveSettings && (
        <UpgradeCheckSettings
          settings={settings}
          onSave={(payload) => onSaveSettings(payload, { close: false })}
        />
      )}
      <PostgresDatabaseUpgrade
        showError={showError}
        databaseStatus={status?.database}
      />
    </>
  );
}

// UpgradeCheckSettings is what grain does *after* an upgrade lands
// (grain/task-38): whether it files a task to go and check that the
// deployment the new binary brought up actually works, whether that task
// is queued straight away or waits for approval, and whether the rest of
// the queue is held until the check is done.
//
// It sits on this tab rather than with the other deployment defaults on
// the General one, and below the button that starts an upgrade, because
// it is the same operator's same decision one step later: whoever is
// about to press Upgrade is exactly who has an opinion about what should
// happen when it comes back up.
//
// Shown even on a deployment with no upgrader wired: the setting is
// stored either way, and a deployment configured for upgrading later
// should not have to discover afterwards that this choice existed.
export function UpgradeCheckSettings({ settings, onSave }) {
  const modes = settings.upgradeCheckModes || ["off", "approve", "auto"];
  const [mode, setMode] = useState(settings.upgradeCheckMode || "auto");
  const [pauses, setPauses] = useState(!!settings.upgradeCheckPausesPipeline);

  return (
    <Box>
      <Typography variant="subtitle2" sx={{ mt: 3 }}>
        After an upgrade
      </Typography>
      <TextField
        select
        name="upgradeCheckMode"
        data-setting-id="upgradeCheckMode"
        label="Check the upgrade"
        value={mode}
        onChange={(e) => {
          const next = e.target.value;
          setMode(next);
          onSave({ upgradeCheckMode: next });
        }}
        fullWidth
        margin="normal"
        helperText={UPGRADE_CHECK_HELP[mode] || ""}
      >
        {modes.map((m) => (
          <MenuItem key={m} value={m}>
            {UPGRADE_CHECK_LABELS[m] || m}
          </MenuItem>
        ))}
      </TextField>
      <FormControlLabel
        control={
          <Checkbox
            name="upgradeCheckPausesPipeline"
            data-setting-id="upgradeCheckPausesPipeline"
            checked={pauses}
            disabled={mode === "off"}
            onChange={(e) => {
              const next = e.target.checked;
              setPauses(next);
              onSave({ upgradeCheckPausesPipeline: next });
            }}
          />
        }
        label={
          <>
            Hold the queue until the check is done
            <span className="hint">
              off (default): the check runs at the head of the queue with
              everything else still going. on: nothing else is picked up until
              it finishes -- runs already in flight are left alone, and the hold
              is given up after six hours whatever the check is doing.
            </span>
          </>
        }
        sx={{ display: "flex", mt: 1 }}
      />
      <AutoUpgradeSettings settings={settings} onSave={onSave} />
      <Typography variant="caption" color="text.secondary">
        Changes to these choices save immediately.
      </Typography>
    </Box>
  );
}

// AUTO_UPGRADE_CHANNELS are the moving names a daily upgrade can follow
// -- pkg/upgrade/targets.go's standardChannels, in the same order.
const AUTO_UPGRADE_CHANNELS = ["latest", "candidate", "main"];

// AutoUpgradeSettings is the daily automatic upgrade: a channel to
// follow, or Off, and the time of day on this deployment's own clock at
// which grain schedules the upgrade onto it (orchestrator's
// reconcileAutoUpgrade). The upgrade is a scheduled one -- the queue is
// held and the runs in flight finish before the restart -- so the time
// is when that starts, not when the restart happens.
//
// A channel saved some other way (the CLI, a settings import) that is
// not one of the three is offered too, so the picker can show what is
// actually set.
export function AutoUpgradeSettings({ settings, onSave }) {
  const [channel, setChannel] = useState(settings.autoUpgradeChannel || "");
  const [time, setTime] = useState(settings.autoUpgradeTime || "03:00");
  const zone = settings.timeZone || "UTC";
  const channels =
    AUTO_UPGRADE_CHANNELS.includes(channel) || !channel
      ? AUTO_UPGRADE_CHANNELS
      : [...AUTO_UPGRADE_CHANNELS, channel];

  return (
    <Box className="auto-upgrade-settings">
      <Typography variant="subtitle2" sx={{ mt: 3 }}>
        Automatic upgrades
      </Typography>
      <Stack direction={{ xs: "column", sm: "row" }} spacing={{ xs: 0, sm: 2 }}>
        <TextField
          select
          name="autoUpgradeChannel"
          data-setting-id="autoUpgradeChannel"
          label="Channel"
          value={channel}
          slotProps={{
            select: { displayEmpty: true },
            inputLabel: { shrink: true },
          }}
          onChange={(e) => {
            const next = e.target.value;
            setChannel(next);
            onSave(
              next
                ? { autoUpgradeChannel: next, autoUpgradeTime: time }
                : { autoUpgradeChannel: "" },
            );
          }}
          fullWidth
          margin="normal"
          helperText={
            channel
              ? `Upgrade onto ${channel} once a day, after the work in flight finishes.`
              : "grain upgrades only when somebody presses Upgrade."
          }
        >
          <MenuItem value="">Off (manual only)</MenuItem>
          {channels.map((c) => (
            <MenuItem key={c} value={c}>
              {c}
            </MenuItem>
          ))}
        </TextField>
        <TextField
          type="time"
          name="autoUpgradeTime"
          data-setting-id="autoUpgradeTime"
          label="Time of day"
          value={time}
          disabled={!channel}
          onChange={(e) => setTime(e.target.value)}
          onBlur={() => {
            if (channel && time && time !== settings.autoUpgradeTime) {
              onSave({ autoUpgradeTime: time });
            }
          }}
          fullWidth
          margin="normal"
          slotProps={{ inputLabel: { shrink: true }, htmlInput: { step: 60 } }}
          helperText={`24-hour, ${zone}. A day whose time has already passed upgrades at once.`}
        />
      </Stack>
    </Box>
  );
}

const UPGRADE_CHECK_LABELS = {
  off: "Don't",
  approve: "File a proposal to approve",
  auto: "File it and queue it (default)",
};

const UPGRADE_CHECK_HELP = {
  off: "An upgrade lands and grain says nothing further about it.",
  approve:
    "A task is filed at the head of the backlog after each upgrade, and runs " +
    "once somebody approves it.",
  auto:
    "A task is filed at the head of the backlog after each upgrade and is the " +
    "next thing dispatched. It reads this host's own source checkout and this " +
    "deployment's config repository, and reports what it found in a comment.",
};

// UpgradeSteps is what an upgrade in flight actually looks like: a bar
// showing how much of its plan is behind it, a line naming the step it
// is on and how long that step has been going, and the plan itself with
// each step's own state, duration and latest word from the command it
// runs (grain/task-37).
//
// The plan arrives complete from the first poll -- pkg/upgrade decides
// it before it starts anything -- so the list does not grow as the
// upgrade proceeds; only the states, times and details in it change.
// That is deliberate: a build that has said the same thing for four
// minutes still shows the four minutes, and a reader can see what is
// still to come rather than only what has already happened.
function UpgradeSteps({ steps, progress, now }) {
  const { current, position, total, settled, percent } = progress;
  const currentElapsed = formatElapsed(stepElapsedMs(current, now));

  return (
    <section className="upgrade-steps" aria-label="Upgrade progress">
      <LinearProgress
        variant="determinate"
        value={percent}
        sx={{ mt: 2, mb: 1 }}
      />
      <Typography variant="body2" color="text.secondary">
        {current
          ? `Step ${position} of ${total}: ${current.name}` +
            (currentElapsed ? ` (${currentElapsed})` : "")
          : `${settled} of ${total} steps finished`}
      </Typography>
      <ol className="upgrade-steps-list">
        {steps.map((step) => {
          const elapsed = formatElapsed(stepElapsedMs(step, now));
          return (
            <li
              key={step.name}
              className={`upgrade-step upgrade-step-${step.state}`}
            >
              <span className="upgrade-step-mark" aria-hidden="true">
                {STEP_MARKS[step.state] ?? STEP_MARKS.pending}
              </span>
              <span className="upgrade-step-name">{step.name}</span>
              <span className="upgrade-step-state">{step.state}</span>
              <span className="upgrade-step-time">{elapsed}</span>
              {step.detail && (
                <span className="upgrade-step-detail">{step.detail}</span>
              )}
            </li>
          );
        })}
      </ol>
    </section>
  );
}

// PostgresDatabaseUpgrade lets an operator upgrade an existing SQLite database
// into PostgreSQL and migrate all tables and data.
export function PostgresDatabaseUpgrade({
  showError = defaultShowError,
  databaseStatus,
}) {
  const reportError =
    typeof showError === "function" ? showError : defaultShowError;
  const [dbStatus, setDbStatus] = useState(databaseStatus || null);
  const [dsn, setDsn] = useState("");
  const [migrateData, setMigrateData] = useState(true);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState(null);
  const [upgrading, setUpgrading] = useState(false);
  const [upgradeResult, setUpgradeResult] = useState(null);
  // confirmation is the server's 202 answer to a first request: nothing
  // has been done, and a single-use token bound to this DSN and these
  // options has been written to the daemon's log (pkg/ui/
  // database_confirm.go). The token is read there, on the daemon host,
  // and typed back here; GET /api/logs blanks it out on purpose.
  const [confirmation, setConfirmation] = useState(null);
  const [confirmationToken, setConfirmationToken] = useState("");

  // A token is bound to exactly the request it was issued for, so a
  // changed DSN or option needs a new one rather than a doomed 403.
  const forgetConfirmation = () => {
    setConfirmation(null);
    setConfirmationToken("");
  };

  useEffect(() => {
    if (databaseStatus) {
      setDbStatus(databaseStatus);
    }
  }, [databaseStatus]);

  const fetchDbStatus = useCallback(async () => {
    try {
      const res = await api("/api/database");
      setDbStatus(res);
    } catch {
      // Keep current state on fetch error
    }
  }, []);

  const handleTestConnection = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const res = await api("/api/database/test", {
        method: "POST",
        body: JSON.stringify({ dsn }),
      });
      setTestResult({
        ok: true,
        message: res?.message || "PostgreSQL connection successful",
      });
    } catch (err) {
      reportError(err);
      setTestResult({
        ok: false,
        message: err?.message || "Connection failed",
      });
    } finally {
      setTesting(false);
    }
  };

  const handleUpgrade = async (e) => {
    if (e) e.preventDefault();
    setUpgrading(true);
    setUpgradeResult(null);
    const token = confirmation ? confirmationToken.trim() : "";
    try {
      const res = await api("/api/database/upgrade", {
        method: "POST",
        body: JSON.stringify(
          token
            ? { dsn, migrateData, confirmationToken: token }
            : { dsn, migrateData },
        ),
      });
      if (res && res.confirmationRequired) {
        setConfirmation(res);
        setConfirmationToken("");
        return;
      }
      forgetConfirmation();
      setUpgradeResult(res);
      if (res && res.driver === "postgres") {
        setDbStatus({
          driver: "postgres",
          dsn: res.dsn || dsn || "",
          isSqlite: false,
          postgresAvailable: true,
        });
      }
      await fetchDbStatus();
    } catch (err) {
      reportError(err);
      // Any attempt spends the token, right or wrong.
      forgetConfirmation();
      setUpgradeResult({
        ok: false,
        message: err?.message || "Migration failed",
      });
    } finally {
      setUpgrading(false);
    }
  };

  const currentStatus = dbStatus || {
    driver: "sqlite",
    isSqlite: true,
    postgresAvailable: false,
  };

  return (
    <Box sx={{ mt: 4 }} className="database-upgrade-section">
      <Typography variant="subtitle2" sx={{ mb: 1 }}>
        Database Backend
      </Typography>

      {!currentStatus.isSqlite && currentStatus.driver === "postgres" ? (
        <Alert
          severity="success"
          sx={{ mb: 2 }}
          className="database-postgres-active"
        >
          This deployment is running on <strong>PostgreSQL</strong>
          {currentStatus.dsn ? ` (${currentStatus.dsn})` : ""}.
        </Alert>
      ) : (
        <>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            Currently running on <strong>SQLite</strong>. Upgrade this
            deployment to a PostgreSQL database by migrating all tasks,
            configurations, runs, and conversation history.
          </Typography>

          {upgradeResult && upgradeResult.ok && (
            <Alert
              severity="success"
              sx={{ mb: 2 }}
              className="database-upgrade-success"
            >
              {upgradeResult.message ||
                `Successfully migrated database to PostgreSQL (${upgradeResult.tablesMigrated} tables migrated).`}
            </Alert>
          )}

          {upgradeResult && !upgradeResult.ok && (
            <Alert
              severity="error"
              sx={{ mb: 2 }}
              className="database-upgrade-error"
            >
              {upgradeResult.message}
            </Alert>
          )}

          {confirmation && (
            <Alert
              severity="info"
              sx={{ mb: 2 }}
              className="database-upgrade-confirm"
            >
              {confirmation.message ||
                "A confirmation token has been written to the daemon log."}{" "}
              Find the line on the daemon host (for example{" "}
              <code>journalctl -u grain-daemon.service</code>) and enter its
              token below.
            </Alert>
          )}

          {testResult && (
            <Alert
              severity={testResult.ok ? "success" : "error"}
              sx={{ mb: 2 }}
              className={
                testResult.ok ? "database-test-success" : "database-test-error"
              }
            >
              {testResult.message}
            </Alert>
          )}

          <form onSubmit={handleUpgrade}>
            <TextField
              name="postgresDSN"
              label="PostgreSQL DSN / Connection URL"
              value={dsn}
              onChange={(e) => {
                setDsn(e.target.value);
                forgetConfirmation();
              }}
              placeholder="postgres://user:password@localhost:5432/grain?sslmode=disable"
              autoComplete="off"
              disabled={upgrading}
              fullWidth
              margin="normal"
              helperText="Target PostgreSQL connection string (or leave blank if local embedded PostgreSQL is available)."
            />

            <FormControlLabel
              control={
                <Checkbox
                  name="migrateData"
                  checked={migrateData}
                  disabled={upgrading}
                  onChange={(e) => {
                    setMigrateData(e.target.checked);
                    forgetConfirmation();
                  }}
                />
              }
              label="Migrate existing SQLite data (tasks, runs, settings, and secrets)"
              sx={{ display: "flex", mt: 1 }}
            />

            {confirmation && (
              <TextField
                name="confirmationToken"
                label="Confirmation token"
                value={confirmationToken}
                onChange={(e) => setConfirmationToken(e.target.value)}
                autoComplete="off"
                disabled={upgrading}
                required
                fullWidth
                margin="normal"
                helperText="The token from the daemon log line that names this request."
              />
            )}

            <Stack
              direction="row"
              spacing={2}
              justifyContent="flex-end"
              sx={{ mt: 2 }}
            >
              <Button
                variant="outlined"
                disabled={testing || upgrading}
                onClick={handleTestConnection}
                className="test-db-button"
              >
                {testing ? "Testing…" : "Test Connection"}
              </Button>
              <Button
                type="submit"
                variant="contained"
                disabled={upgrading}
                className="upgrade-db-button"
              >
                {upgrading
                  ? "Migrating Database…"
                  : confirmation
                    ? "Confirm Migration"
                    : "Migrate Database to PostgreSQL"}
              </Button>
            </Stack>
          </form>
        </>
      )}
    </Box>
  );
}
