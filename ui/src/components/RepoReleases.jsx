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

import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  FormControl,
  FormControlLabel,
  InputLabel,
  Link,
  ListItemText,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { repoAdministered } from "../taskWrites.js";
import api from "../api.js";
import {
  stateTitle,
  taskBadgeModifier,
  taskBadgeState,
  isAgentRunning,
  isTaskRepairing,
} from "../state.js";
import ItemGlyph, { GlyphLabel } from "./ItemGlyph.jsx";
import StateDot from "./StateDot.jsx";
import { identifierField } from "../identifierField.js";
import { repoApiRoot } from "../repoApi.js";

// POLL_INTERVAL_MS mirrors App.jsx's own poll: a candidate can move
// through "cutting"/"promoting", a release through
// "provisioning"/"merge_requested", and a qualification run's tasks
// through queued/running/completed, entirely server-side (graind
// dispatch, a run finishing), so without a poll this pane only ever
// moves when a human takes some action that happens to re-render it.
//
// Exported for RepoReleases.test.jsx, whose poll test runs a fake clock
// forward by exactly this rather than by a 3000 written out again.
export const POLL_INTERVAL_MS = 3000;

const RELEASE_STATUS_LABELS = {
  provisioning: "Provisioning",
  active: "Active",
  merge_requested: "Merge requested",
  merged: "Merged",
};

// releaseActionReasons says why Cut new RC and Promote current RC are
// off, one sentence each, or null for a button that is on -- the same
// conditions canCut and canPromote test, in the order an operator can
// do something about them. A greyed-out button with no word of why
// reads as broken; this is the word.
export function releaseActionReasons({
  repo,
  release,
  candidate,
  manageRefused,
}) {
  if (!release) return { cut: null, promote: null };
  if (manageRefused) {
    const why = `Only ${repo}'s admins can cut or promote its release candidates.`;
    return { cut: why, promote: why };
  }
  if (release.status !== "active") {
    const status = (
      RELEASE_STATUS_LABELS[release.status] || release.status
    ).toLowerCase();
    const why =
      release.status === "provisioning"
        ? `${release.name} is still provisioning: grain cuts and promotes its candidates once its ${release.latestBranch} branch exists.`
        : `${release.name} is ${status}: only an active release cuts or promotes candidates.`;
    return { cut: why, promote: why };
  }
  let cut = null;
  if (candidate && candidate.status !== "promoted") {
    cut =
      candidate.status === "cutting"
        ? `${candidate.branch} is still being cut.`
        : `${candidate.branch} is ${candidate.status}: a new RC is cut once it is promoted.`;
  }
  let promote = null;
  if (!candidate) {
    promote = "No candidate to promote yet: cut one first.";
  } else if (candidate.status === "promoted") {
    promote = `${candidate.branch} is already promoted: a candidate is promoted once.`;
  } else if (candidate.status !== "active") {
    promote = `${candidate.branch} is ${candidate.status}: only an active candidate can be promoted.`;
  }
  return { cut, promote };
}

// RepoReleases is a single repo's release pane (bwsalmon/agents#571): pick
// or create a named release, then cut and promote its own rc candidates
// against the latest/rc/prod branches that name derives -- "myfeat" gives
// "myfeat.latest", "myfeat.rc.N" and "myfeat"; "2.1" gives "2.1.latest",
// "2.1.rc.N" and "2.1". This replaces bwsalmon/agents#398's own single
// prod/rc/prefix/major-version form, since a repo may now have any number
// of releases going at once, each independent.
//
// bwsalmon/agents#518's qualification plan editor and, for the current
// candidate, its qualification run's own summary are unchanged from
// before -- see QualificationPlanEditor and QualificationSummary below --
// since a plan is still one repo-wide policy, not a per-release one.
//
// release is the release the address bar names (/repos/acme/widgets/
// releases/r2), and onSelectRelease is how a pick here gets into it, so a
// reload, Back, or a link somebody sent opens the release that was on
// screen rather than whichever is newest. A pane opened without one shows
// the newest and leaves the address alone. One naming a release this
// repo does not have shows the newest too, and calls
// onSelectRelease(null, name) so the address stops naming it.
export default function RepoReleases({
  repo,
  config = null,
  release = null,
  templates = [],
  baseBranchGates = [],
  onBack,
  onOpenTab = null,
  onOpenTask,
  onSelectRelease = () => {},
  showError,
}) {
  const root = repoApiRoot(repo);
  const [releases, setReleases] = useState(null);
  const [selected, setSelected] = useState(release);
  // loaded is the candidates and qualification run as fetched, together
  // with the release they were fetched for. Picking another release in
  // the picker changes `selected` at once, but its candidates are a
  // request away -- and until they arrive what is held here is still the
  // last release's. Keyed like this, the pane can tell the two apart and
  // say it is loading rather than show one release's candidate, its error
  // and its Cut/Promote state under another release's name.
  const [loaded, setLoaded] = useState({
    release: null,
    candidates: [],
    run: null,
  });
  const [qualificationPlan, setQualificationPlan] = useState(null);
  // creation is whether this deployment may create a release in repo at
  // all (ui.Client.ReleaseCreation): a repo off the target repo list is
  // refused by the API, so the pane says so in place of a create form
  // whose every submission would be turned away. null until read, and
  // read as allowed if it never answers -- the API still refuses.
  const [creation, setCreation] = useState(null);
  // loadError is the last read's failure, for the pane to say so in place
  // of "Loading releases…" when there is nothing loaded to show instead.
  // The poll keeps retrying, and the next read that lands clears it.
  const [loadError, setLoadError] = useState(null);
  const polling = useRef(false);
  const refreshSeq = useRef(0);
  const selectedRef = useRef(release);
  selectedRef.current = selected;
  const onSelectReleaseRef = useRef(onSelectRelease);
  onSelectReleaseRef.current = onSelectRelease;
  const releasesRef = useRef(releases);
  releasesRef.current = releases;
  // shownRelease is the last release prop this pane has acted on, so the
  // effect below can tell an address that moved under it (Back, Forward)
  // from one it moved itself. urlReleaseRef holds a release prop from
  // the address bar until the next refresh confirms whether it exists,
  // so only an unconfirmed URL release is reported as missing.
  const shownRelease = useRef(release);
  const urlReleaseRef = useRef(release);

  const refresh = useCallback(async () => {
    const seq = ++refreshSeq.current;
    try {
      const [list, plan, allowed] = await Promise.all([
        api(`${root}/releases`),
        api(`${root}/qualification-plan`),
        api(`${root}/release-creation`),
      ]);
      if (seq !== refreshSeq.current) return;
      setReleases(list);
      setQualificationPlan(plan);
      setCreation(allowed);
      setLoadError(null);

      const asked = selectedRef.current;
      const fromUrl = urlReleaseRef.current === asked;
      urlReleaseRef.current = null;
      const current =
        asked && list.some((r) => r.name === asked)
          ? asked
          : list.length > 0
            ? list[0].name
            : null;
      if (asked && current !== asked) {
        shownRelease.current = null;
        if (fromUrl) {
          onSelectReleaseRef.current(null, asked);
        } else {
          onSelectReleaseRef.current(null);
        }
      }
      selectedRef.current = current;
      setSelected(current);

      if (current) {
        const cs = await api(`${root}/releases/${current}/candidates`);
        const currentCandidate = cs.length > 0 ? cs[0] : null;
        const run = currentCandidate
          ? await api(`${root}/candidates/${currentCandidate.id}/qualification`)
          : null;
        // Somebody picked another release or started a newer refresh while
        // these were in flight: an older poll must not land stale state on
        // top of the newer read.
        if (seq !== refreshSeq.current || selectedRef.current !== current)
          return;
        setLoaded({ release: current, candidates: cs, run });
      } else {
        setLoaded({ release: null, candidates: [], run: null });
      }
    } catch (err) {
      if (seq !== refreshSeq.current) return;
      setLoadError(err?.message || String(err));
      showError(err);
    }
  }, [root, showError]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // Back and Forward change the release the address names under a pane
  // that is already showing, and the pane follows -- to the newest when
  // the address names none. A change this pane made itself already
  // matches what it shows, and is left alone.
  useEffect(() => {
    if (shownRelease.current === release) return;
    shownRelease.current = release;
    if (release !== null && release === selectedRef.current) return;
    urlReleaseRef.current = release;
    const next = release ?? releasesRef.current?.[0]?.name ?? null;
    selectedRef.current = next;
    setSelected(next);
    refresh();
  }, [release, refresh]);

  // Poll for the same reason App.jsx does: a release's or a candidate's
  // status, and a qualification run's tasks, all move server-side, with
  // no other event to tell this pane it should re-fetch. visibilitychange
  // catches up immediately on tab-back rather than waiting out a stale
  // interval.
  useEffect(() => {
    async function poll() {
      if (polling.current || document.visibilityState === "hidden") return;
      polling.current = true;
      try {
        await refresh();
      } finally {
        polling.current = false;
      }
    }
    const interval = setInterval(poll, POLL_INTERVAL_MS);
    const onVisible = () => {
      if (document.visibilityState === "visible") poll();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(interval);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [refresh]);

  const createRelease = async (evt) => {
    evt.preventDefault();
    const form = evt.target;
    const releaseName = form.elements.releaseName.value.trim();
    try {
      refreshSeq.current++;
      const created = await api(`${root}/releases`, {
        method: "POST",
        body: JSON.stringify({ name: releaseName }),
      });
      form.reset();
      selectedRef.current = created.name;
      setSelected(created.name);
      onSelectRelease(created.name);
      await refresh();
    } catch (err) {
      showError(err);
    }
  };

  const cut = async () => {
    try {
      await api(`${root}/releases/${selected}/candidates`, {
        method: "POST",
      });
      await refresh();
    } catch (err) {
      showError(err);
    }
  };

  // Promote and merge back both act outside grain -- one moves the prod
  // branch, the other opens a pull request against the default branch --
  // so each asks first and names the branches it is about to touch, the
  // way removing a repo or deleting a memory does.
  const promote = async () => {
    const rc = loaded.candidates[0]?.branch;
    const prod = releases.find((r) => r.name === selected)?.prodBranch;
    if (
      !confirm(
        `Promote ${rc} to ${prod}? This moves ${repo}'s ${prod} branch to ${rc}.`,
      )
    )
      return;
    try {
      await api(`${root}/releases/${selected}/candidates/promote`, {
        method: "POST",
      });
      await refresh();
    } catch (err) {
      showError(err);
    }
  };

  const requestMerge = async () => {
    const prod = releases.find((r) => r.name === selected)?.prodBranch;
    if (
      !confirm(
        `Merge ${prod} into ${repo}'s default branch? This opens a pull request on GitHub from ${prod}.`,
      )
    )
      return;
    try {
      await api(`${root}/releases/${selected}/merge`, {
        method: "POST",
      });
      await refresh();
    } catch (err) {
      showError(err);
    }
  };

  const removeRelease = async () => {
    if (
      !confirm(
        `Remove release ${selected}? This drops the release and its candidates from grain.`,
      )
    )
      return;
    try {
      refreshSeq.current++;
      await api(`${root}/releases/${selected}`, {
        method: "DELETE",
      });
      selectedRef.current = null;
      shownRelease.current = null;
      urlReleaseRef.current = null;
      setSelected(null);
      onSelectRelease(null);
      await refresh();
    } catch (err) {
      showError(err);
    }
  };

  const savePlan = async (payload) => {
    try {
      await api(`${root}/qualification-plan`, {
        method: "PUT",
        body: JSON.stringify(payload),
      });
      await refresh();
    } catch (err) {
      showError(err);
    }
  };

  const approveQualification = async (candidateId) => {
    try {
      await api(`${root}/candidates/${candidateId}/qualification/approve`, {
        method: "POST",
      });
      await refresh();
    } catch (err) {
      showError(err);
    }
  };

  // header is the part of the pane that needs nothing from the API: the
  // way back and the heading. It stands while the first read is still
  // out, and stays if that read fails, rather than leaving a blank page
  // with no way back but the browser's.
  const header = (
    <>
      {/* Back to the repo's own page, which is the only way in here
        now that the repo list's rows carry no buttons of their own
        (grain/task-111) -- so it names that repo rather than the
        list two steps up. */}
      <Button onClick={onBack} sx={{ mb: 1, ml: -0.9 }}>
        &larr; {repo}
      </Button>
      {/* Headed by the ring the repo's own page is (RepoPage.jsx,
        ItemGlyph.jsx) -- this is still that repo, one page deeper --
        ahead of the heading and decorative, since the repo's name is
        the first thing the heading says. */}
      <Stack direction="row" alignItems="center" spacing={1}>
        <ItemGlyph kind="repos" size={20} />
        <Typography variant="h6" component="h2" sx={{ m: 0 }}>
          {repo} releases
        </Typography>
      </Stack>
    </>
  );

  if (releases === null || qualificationPlan === null)
    return (
      <main>
        <Box sx={{ px: "1.5rem" }}>
          {header}
          {loadError ? (
            <Alert severity="error" sx={{ mt: 2 }}>
              Could not load {repo}&apos;s releases: {loadError}. Retrying.
            </Alert>
          ) : (
            <p className="empty">Loading releases…</p>
          )}
        </Box>
      </main>
    );

  const current = releases.find((r) => r.name === selected) || null;
  // Whether what loaded holds is this release's own. Until it is, there
  // is no current candidate to describe and nothing to cut or promote.
  const candidatesLoaded = current !== null && loaded.release === current.name;
  const candidates = candidatesLoaded ? loaded.candidates : [];
  const qualificationRun = candidatesLoaded ? loaded.run : null;
  const currentCandidate = candidates.length > 0 ? candidates[0] : null;
  // manageRefused is the caller not being one of repo's admins
  // (ReleaseCreation.manageable): the API refuses them a cut, a promote
  // and a merge back with a 403, so those buttons stay disabled.
  const manageRefused = creation !== null && creation.manageable === false;
  const canCut =
    !manageRefused &&
    candidatesLoaded &&
    current.status === "active" &&
    (!currentCandidate || currentCandidate.status === "promoted");
  const canPromote =
    !manageRefused &&
    candidatesLoaded &&
    current.status === "active" &&
    currentCandidate &&
    currentCandidate.status === "active";
  const canRequestMerge =
    !manageRefused && current && current.status === "active";
  const creationRefused = creation !== null && creation.allowed === false;
  const actionReasons = candidatesLoaded
    ? releaseActionReasons({
        repo,
        release: current,
        candidate: currentCandidate,
        manageRefused,
      })
    : { cut: null, promote: null };
  // Every template this repo's plan is allowed to schedule: the unbound
  // ones, plus any bound to this very repo (grain/task-285) -- and none
  // of grain's built-in ones, which are only filed from by hand.
  const qualifiableTemplates = templates.filter(
    (tmpl) => !tmpl.system && (!tmpl.repo || tmpl.repo === repo),
  );

  const unjudgedGates = baseBranchGates.filter(
    (g) => g.repo === repo && !g.judged,
  );

  return (
    <main>
      <Box sx={{ px: "1.5rem" }}>
        {header}

        {unjudgedGates.map((gate) => (
          <Alert
            key={`${gate.repo}/${gate.branch || ""}`}
            severity="error"
            sx={{ mt: 1 }}
            // Nothing on this pane judges a branch: the settings that do
            // are on the repository's own page. A gate held only by grain
            // CI being off is repaired by that page's CI Mode, so it goes
            // straight to the CI tab, as the page's own warning does;
            // landing on the Overview left the reader to find a second
            // "Go to CI Mode" there. Anything else is the page itself.
            action={
              gate.reason === "grain-ci-off" && onOpenTab ? (
                <Button
                  color="inherit"
                  size="small"
                  onClick={() => onOpenTab("ci")}
                >
                  Go to CI Mode
                </Button>
              ) : onBack ? (
                <Button color="inherit" size="small" onClick={onBack}>
                  Open repository settings
                </Button>
              ) : null
            }
          >
            {gate.detail}
          </Alert>
        ))}

        {creationRefused ? (
          <Alert severity="warning" sx={{ mt: 1 }}>
            {creation.reason}
          </Alert>
        ) : (
          <form onSubmit={createRelease}>
            <Stack
              direction="row"
              spacing={1}
              alignItems="flex-start"
              sx={{ mt: 1 }}
            >
              <TextField
                name="releaseName"
                label="New release name"
                placeholder="myfeat, or 2.1"
                helperText="Gives myfeat.latest, myfeat.rc.N and myfeat"
                autoComplete="off"
                inputProps={identifierField}
                required
                InputLabelProps={{ required: false }}
                size="small"
              />
              <Button type="submit" variant="contained" size="small">
                Create release
              </Button>
            </Stack>
          </form>
        )}

        {releases.length === 0 && !creationRefused && (
          <Alert severity="info" sx={{ mt: 2 }}>
            {repo} has no releases yet -- create one above to get a latest, rc
            and prod branch.
          </Alert>
        )}

        {releases.length > 0 && (
          // maxWidth: release names have no length cap, and a long one
          // would otherwise widen the picker past a phone's screen; the
          // Select ellipsizes what no longer fits.
          <FormControl
            size="small"
            sx={{ mt: 2, minWidth: 240, maxWidth: "100%" }}
          >
            <InputLabel id="release-picker-label">Release</InputLabel>
            <Select
              labelId="release-picker-label"
              label="Release"
              value={selected || ""}
              onChange={(e) => {
                selectedRef.current = e.target.value;
                setSelected(e.target.value);
                onSelectRelease(e.target.value);
                refresh();
              }}
            >
              {releases.map((r) => (
                <MenuItem key={r.name} value={r.name}>
                  {r.name} -- {RELEASE_STATUS_LABELS[r.status] || r.status}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
        )}

        {current && (
          // overflowWrap: the branch names below carry the release's
          // name, so a long one breaks rather than scrolling the pane.
          <Box sx={{ mt: 2, overflowWrap: "anywhere" }}>
            <p className="hint">
              latest: {current.latestBranch}, prod: {current.prodBranch}
              {current.error && (
                <span className="candidate-error"> ({current.error})</span>
              )}
            </p>

            <Typography variant="subtitle1" sx={{ mt: 1 }}>
              Current candidate
            </Typography>
            {!candidatesLoaded ? (
              <p className="empty">Loading candidates…</p>
            ) : currentCandidate ? (
              <div className="candidate-current">
                <p>
                  <strong>{currentCandidate.branch}</strong> --{" "}
                  {currentCandidate.status}
                  {currentCandidate.error && (
                    <span className="candidate-error">
                      {" "}
                      ({currentCandidate.error})
                    </span>
                  )}
                </p>
              </div>
            ) : (
              <p className="empty">No release candidate cut yet.</p>
            )}
            <Stack direction="row" spacing={1} sx={{ mt: 1, mb: 2 }}>
              <Button variant="contained" disabled={!canCut} onClick={cut}>
                Cut new RC
              </Button>
              <Button
                variant="outlined"
                disabled={!canPromote}
                onClick={promote}
              >
                Promote current RC
              </Button>
            </Stack>
            {(actionReasons.cut || actionReasons.promote) && (
              <Box data-testid="release-action-reasons" sx={{ mt: -1, mb: 2 }}>
                {actionReasons.cut &&
                actionReasons.cut === actionReasons.promote ? (
                  <p className="hint">{actionReasons.cut}</p>
                ) : (
                  <>
                    {actionReasons.cut && (
                      <p className="hint">Cut new RC: {actionReasons.cut}</p>
                    )}
                    {actionReasons.promote && (
                      <p className="hint">
                        Promote current RC: {actionReasons.promote}
                      </p>
                    )}
                  </>
                )}
              </Box>
            )}

            {currentCandidate && (
              <QualificationSummary
                run={qualificationRun}
                onApprove={
                  repoAdministered(config, repo)
                    ? () => approveQualification(currentCandidate.id)
                    : null
                }
                onOpenTask={onOpenTask}
              />
            )}

            <Typography variant="subtitle1" sx={{ mt: 2 }}>
              History
            </Typography>
            {candidatesLoaded && candidates.length === 0 && (
              <p className="empty">No candidates yet.</p>
            )}
            {candidates.length > 0 && (
              <ul className="candidate-history">
                {candidates.map((c) => (
                  <li key={c.id}>
                    <strong>{c.branch}</strong> -- {c.status}
                  </li>
                ))}
              </ul>
            )}

            <Typography variant="subtitle1" sx={{ mt: 2 }}>
              Merge back to default
            </Typography>
            {current.status === "merged" ? (
              current.mergeNote ? (
                // A release whose prod branch carried nothing the default
                // branch did not already have: merged, with no pull
                // request, and the note is what says so rather than
                // leaving "Merge requested." next to a missing link.
                <p className="hint">
                  Nothing to merge back -- {current.mergeNote}
                </p>
              ) : (
                <p className="hint">
                  Merge requested
                  {current.pullRequestUrl ? (
                    <>
                      {" "}
                      --{" "}
                      <a
                        href={current.pullRequestUrl}
                        target="_blank"
                        rel="noreferrer"
                      >
                        pull request
                      </a>
                    </>
                  ) : (
                    "."
                  )}
                </p>
              )
            ) : (
              <Stack direction="row" spacing={1} sx={{ mb: 2 }}>
                <Button
                  variant="outlined"
                  disabled={!canRequestMerge}
                  onClick={requestMerge}
                >
                  Merge {current.prodBranch} into the default branch
                </Button>
              </Stack>
            )}

            <Stack direction="row" spacing={1} sx={{ mt: 3, mb: 1 }}>
              <Button
                variant="outlined"
                color="error"
                disabled={manageRefused}
                onClick={removeRelease}
              >
                Delete release
              </Button>
            </Stack>
          </Box>
        )}

        <Typography variant="subtitle1" sx={{ mt: 3 }}>
          Qualification plan
        </Typography>
        <p className="hint">
          Templates a fresh, active release candidate schedules automatically --
          run this many times each, in dependency order, against the candidate's
          own branch. Pick from any template declared under Processes, except
          ones bound to another repo -- a qualification run always targets this
          repo's own candidate, so grain refuses those and this picker leaves
          them out.
        </p>
        {/* The server half of that refusal is ui.Client.PutQualificationPlan;
            the name belongs here, not in a sentence an operator reads. */}
        <QualificationPlanEditor
          plan={qualificationPlan}
          templates={qualifiableTemplates}
          onSave={savePlan}
        />
      </Box>
    </main>
  );
}

const QUALIFICATION_STATUS_LABELS = {
  pending_approval: "Pending approval",
  running: "Running",
  succeeded: "Succeeded",
  failed: "Failed",
};

const QUALIFICATION_STATUS_COLORS = {
  pending_approval: "warning",
  running: "info",
  succeeded: "success",
  failed: "error",
};

// QualificationSummary is the current candidate's own qualification
// progress: a status chip, an action or outcome banner, and every task
// instance a run has scheduled -- ordered failures first by the API, so
// the issue's own "show...any failures up front" needs no sort here.
// run is null both before a plan has ever scheduled one for this
// candidate and when the repo has no plan configured at all -- rendering
// nothing either way, since there is nothing yet to summarize.
function QualificationSummary({ run, onApprove, onOpenTask }) {
  if (!run) return null;
  const label = QUALIFICATION_STATUS_LABELS[run.status] || run.status;
  const color = QUALIFICATION_STATUS_COLORS[run.status] || "default";

  return (
    <Box sx={{ mb: 2 }}>
      <Stack direction="row" spacing={1} alignItems="center">
        <Typography variant="subtitle1" sx={{ mt: 0 }}>
          Qualification
        </Typography>
        <Chip size="small" color={color} label={label} />
      </Stack>
      {/* onApprove is null for a reader who is not this repo's admin
          (repoAdministered): ApproveQualificationRun refuses them with a
          403, so the banner says what is waiting without the button. */}
      {run.status === "pending_approval" && (
        <Alert
          severity="warning"
          sx={{ mt: 1 }}
          action={
            onApprove && (
              <Button size="small" onClick={onApprove}>
                Approve all
              </Button>
            )
          }
        >
          This candidate's qualification tasks need approval before any of them
          can run.{!onApprove && " A repository admin approves them."}
        </Alert>
      )}
      {run.status === "succeeded" && (
        <Alert severity="success" sx={{ mt: 1 }}>
          Qualification passed -- ready to promote.
        </Alert>
      )}
      {run.status === "failed" && (
        <Alert severity="error" sx={{ mt: 1 }}>
          Qualification failed -- see below.
        </Alert>
      )}
      <ul className="qualification-task-list">
        {run.tasks.map((t) => {
          const modifier = taskBadgeModifier(t);
          const repairing = isTaskRepairing(t);
          const interactive = !repairing && Boolean(t.interactive);
          return (
            <li key={t.taskId}>
              <span
                className={`badge badge-icon badge-${taskBadgeState(t)}${isAgentRunning(t) ? " badge-mark" : ""}${modifier ? ` ${modifier}` : ""}`}
                title={stateTitle(t)}
              >
                <StateDot
                  state={taskBadgeState(t)}
                  title={stateTitle(t)}
                  repairing={repairing}
                  interactive={interactive}
                  agentRunning={isAgentRunning(t)}
                />
              </span>
              {/* A qualification row already carries the real task id; make its
                name the same way into that task every other task reference is.
                Keep the href for open-in-new-tab, while an ordinary click uses
                App's in-place navigation so one browser Back returns here. */}
              <Link
                href={`/tasks/${encodeURIComponent(t.taskId)}`}
                underline="hover"
                onClick={(event) => {
                  if (
                    !onOpenTask ||
                    event.button !== 0 ||
                    event.metaKey ||
                    event.ctrlKey ||
                    event.shiftKey ||
                    event.altKey
                  )
                    return;
                  event.preventDefault();
                  onOpenTask(t.taskId);
                }}
              >
                {t.templateName}
              </Link>
              {t.repeat > 1 && (
                <span className="hint">
                  {" "}
                  ({t.instanceIndex}/{t.repeat})
                </span>
              )}
              {!t.approved && (
                <Chip size="small" label="Unapproved" sx={{ ml: 1 }} />
              )}
            </li>
          );
        })}
      </ul>
    </Box>
  );
}

function emptyItem() {
  return { templateId: "", repeat: 1, dependsOn: [] };
}

// withoutDanglingDependencies is items with every dependency on a
// template none of the other items names taken out.
export function withoutDanglingDependencies(items) {
  return items.map((it, i) => {
    const named = new Set(
      items
        .filter((other, j) => j !== i && other.templateId !== "")
        .map((other) => other.templateId),
    );
    const dependsOn = (it.dependsOn || []).filter((id) => named.has(id));
    return dependsOn.length === (it.dependsOn || []).length
      ? it
      : { ...it, dependsOn };
  });
}

// QualificationPlanEditor is the whole-plan form: the two switches
// bwsalmon/agents#518 asks for, and every item, added, edited and
// removed in place -- PutQualificationPlan replaces the whole plan
// wholesale on Save, so there is no separate per-item save action.
//
// Each item's own content -- title, body, reads, capabilities -- comes
// entirely from the template it names (bwsalmon/agents#516); this form
// only ever picks which template, how many times, and what it waits on,
// the same "template takes over the content fields" split ScheduleOverlay's
// own picker already draws.
function QualificationPlanEditor({ plan, templates, onSave }) {
  const [requireApproval, setRequireApproval] = useState(plan.requireApproval);
  const [autoPromote, setAutoPromote] = useState(plan.autoPromote);
  const [items, setItems] = useState(
    plan.items.map((it) => ({
      templateId: it.templateId,
      repeat: it.repeat,
      dependsOn: it.dependsOn || [],
    })),
  );

  // An item's "Depends on" can only offer the templates the plan's other
  // items name, so a dependency on a template no item names any more is
  // one the picker cannot show to untick -- and the server refuses the
  // whole plan for it. Taking an item out, or pointing it at another
  // template, therefore drops what the plan no longer has from every
  // other item's dependencies (grain/task-2414).
  const updateItem = (i, patch) => {
    setItems((prev) =>
      withoutDanglingDependencies(
        prev.map((it, idx) => (idx === i ? { ...it, ...patch } : it)),
      ),
    );
  };
  const removeItem = (i) => {
    setItems((prev) =>
      withoutDanglingDependencies(prev.filter((_, idx) => idx !== i)),
    );
  };
  const addItem = () => {
    setItems((prev) => [...prev, emptyItem()]);
  };

  const submit = async (evt) => {
    evt.preventDefault();
    const payload = {
      requireApproval,
      autoPromote,
      items: items
        .filter((it) => it.templateId !== "")
        .map((it) => ({
          templateId: it.templateId,
          repeat: Number(it.repeat) || 1,
          dependsOn: it.dependsOn || [],
        })),
    };
    await onSave(payload);
  };

  const templateName = (id) =>
    (templates.find((tmpl) => tmpl.id === id) || {}).name || id;

  return (
    <form onSubmit={submit}>
      <FormControlLabel
        control={
          <Checkbox
            checked={requireApproval}
            onChange={(e) => setRequireApproval(e.target.checked)}
          />
        }
        label="Require approval before running qualification tasks"
        sx={{ display: "flex", mt: 1 }}
      />
      <FormControlLabel
        control={
          <Checkbox
            checked={autoPromote}
            onChange={(e) => setAutoPromote(e.target.checked)}
          />
        }
        label="Promote automatically once qualification succeeds"
        sx={{ display: "flex" }}
      />

      {templates.length === 0 && (
        <Alert severity="info" sx={{ mt: 2 }}>
          No templates this repo can qualify yet -- create one under Processes
          first.
        </Alert>
      )}

      {items.map((it, i) => {
        const otherItems = items.filter(
          (_, idx) => idx !== i && items[idx].templateId !== "",
        );
        return (
          <Box
            key={i}
            className="qualification-item-row"
            sx={{
              border: "1px solid",
              borderColor: "divider",
              borderRadius: 1,
              p: 1.5,
              mt: 1.5,
            }}
          >
            <Stack direction="row" spacing={1} alignItems="flex-start">
              <FormControl size="small" sx={{ flex: 2 }}>
                <InputLabel id={`qualification-template-label-${i}`}>
                  Template
                </InputLabel>
                <Select
                  labelId={`qualification-template-label-${i}`}
                  label="Template"
                  value={it.templateId}
                  onChange={(e) =>
                    updateItem(i, { templateId: e.target.value })
                  }
                >
                  {/* A qualification plan is a list of templates, so
                      this picker carries their figure the way every
                      other template picker does (ItemGlyph.jsx). The
                      placeholder names no template and holds the slot
                      open instead. */}
                  <MenuItem value="" disabled>
                    <GlyphLabel kind={null}>Choose a template</GlyphLabel>
                  </MenuItem>
                  {templates.map((tmpl) => (
                    <MenuItem key={tmpl.id} value={tmpl.id}>
                      <GlyphLabel kind="templates">{tmpl.name}</GlyphLabel>
                    </MenuItem>
                  ))}
                </Select>
              </FormControl>
              <TextField
                label="Repeat"
                type="number"
                inputProps={{ min: 1 }}
                value={it.repeat}
                onChange={(e) => updateItem(i, { repeat: e.target.value })}
                size="small"
                sx={{ width: 90 }}
              />
            </Stack>
            <FormControl fullWidth size="small" margin="dense">
              <InputLabel id={`qualification-dependson-label-${i}`}>
                Depends on
              </InputLabel>
              <Select
                labelId={`qualification-dependson-label-${i}`}
                label="Depends on"
                multiple
                value={it.dependsOn}
                onChange={(e) => updateItem(i, { dependsOn: e.target.value })}
                renderValue={(selected) => (
                  <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5 }}>
                    {selected.map((id) => (
                      <Chip
                        key={id}
                        size="small"
                        icon={<ItemGlyph kind="templates" />}
                        label={templateName(id)}
                      />
                    ))}
                  </Box>
                )}
              >
                {otherItems.length === 0 && (
                  <MenuItem disabled value="">
                    No other items yet
                  </MenuItem>
                )}
                {/* Every row here is another template in this plan, so
                    each carries the figure the picker above it does --
                    after the checkbox, which is a control rather than a
                    label. */}
                {otherItems.map((other) => (
                  <MenuItem key={other.templateId} value={other.templateId}>
                    <Checkbox
                      checked={it.dependsOn.includes(other.templateId)}
                      size="small"
                    />
                    <ItemGlyph kind="templates" />
                    <ListItemText
                      primary={templateName(other.templateId)}
                      sx={{ ml: 0.9 }}
                    />
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
            <Stack direction="row" justifyContent="flex-end" sx={{ mt: 0.5 }}>
              <Button size="small" color="error" onClick={() => removeItem(i)}>
                Remove
              </Button>
            </Stack>
          </Box>
        );
      })}

      <Stack direction="row" spacing={1} sx={{ mt: 2 }}>
        <Button
          size="small"
          variant="outlined"
          onClick={addItem}
          disabled={templates.length === 0}
        >
          Add item
        </Button>
        <Button size="small" type="submit" variant="contained">
          Save qualification plan
        </Button>
      </Stack>
    </form>
  );
}
