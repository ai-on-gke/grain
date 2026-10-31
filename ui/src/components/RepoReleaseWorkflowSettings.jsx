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
  Chip,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { identifierField } from "../identifierField.js";
import { repoAdministered } from "../taskWrites.js";
import { guardLeaving } from "../unsavedEdits.js";
import { repoApiRoot } from "../repoApi.js";

// The job ids a comma-separated box names, the way its Save reads them.
function jobList(value) {
  return value
    .split(",")
    .map((job) => job.trim())
    .filter(Boolean);
}

// readOnly draws every write here disabled for a reader who is not a
// repo admin of repo (RepoPage's writeRefusal, which says why): the
// switch, the job lists and a manual release are a repo admin's and
// refused with a 403, and so is asking for a base branch's check
// (handleRequestBaseBranchRun's requireRepoPerson). The secret buttons are
// already drawn only for a repo admin (canEditSecrets). describedBy is
// the id of the page's sentence saying why, each disabled write's
// accessible description.
//
// configReadOnly draws the release settings -- the automatic-release
// switch and the job lists -- disabled for everybody: they are
// config-repo only (docs/design/config-repo-settings.md §2.1). The
// operational actions, a manual release and a base branch's check, stay
// a repo admin's.
export default function RepoReleaseWorkflowSettings({
  repo,
  config = null,
  readOnly = false,
  configReadOnly = false,
  describedBy,
  // refreshKey moves when the page this sits on changed a setting that
  // can judge or unjudge the base branch, so the gate alert below is
  // re-read rather than left saying what was true before the change.
  refreshKey = 0,
  onOpenRun,
  onRefreshConfig,
  // onToggled says the release switch below was saved: it is the release
  // item's work-ownership override, which RepoWorkOwnership draws too.
  onToggled,
  showError,
}) {
  const root = repoApiRoot(repo);
  const [settings, setSettings] = useState(null);
  const [adding, setAdding] = useState(false);
  const [running, setRunning] = useState(false);
  // starting is a manual release's POST in flight. Run release stays
  // disabled for it: each submit dispatches a release of its own, and a
  // second click while the first is still on its way is a second release.
  const [starting, setStarting] = useState(false);
  const manualToggle = useRef(null);
  // What grain said it would do about the last base-branch run asked for
  // here, or null before anybody has asked for one.
  const [baseBranchRun, setBaseBranchRun] = useState(null);
  const [editing, setEditing] = useState("");
  const releaseEnabled = settings?.enabled !== false;
  const secrets = settings?.secrets || [];
  // Setting and removing a release secret is a repo admin's
  // (requireReleaseSecretAdmin), so anybody else sees the list without
  // the buttons the server would answer with a 403.
  const canEditSecrets = repoAdministered(config, repo);
  const baseBranchJobs = settings?.baseBranchJobs || [];
  const postMergeDispatchJobs = settings?.postMergeDispatchJobs || [];
  // The branches grain is watching here, which is exactly the set a
  // base-branch run can be asked for. Empty for a repository grain has
  // never merged into and is not watching the default branch of, where
  // there is nothing to re-run and so no button.
  const baseBranches = settings?.baseBranches || [];
  // Absent while this repository's base-branch check is working, which
  // is most of the time and one alert fewer rather than a reassurance.
  // An unjudged gate is the repository page's header's to say (from GET
  // /api/base-branch-gates, read from the same finding), with its link to
  // CI Mode; saying it here as well put the same sentence on the page
  // twice. What is left for this pane is the judged-but-late gate, which
  // the header does not show.
  const baseBranchGate =
    settings?.baseBranchGate && settings.baseBranchGate.judged
      ? settings.baseBranchGate
      : null;
  // And absent while this repository is actually shipping, which is the
  // other half of the same distinction: the switch below says whether a
  // merge is meant to publish, and this says whether one has
  // (grain/task-511).
  const baseBranchPublishing = settings?.baseBranchPublishing || null;

  // The two job boxes save only on their own button, and this page is
  // not an Overlay, so nothing asked before leaving it dropped a typed
  // value (found in the 2026-10-12 repository walkthrough). Leaving asks
  // whether either box names jobs other than the saved ones: App asks it
  // from "← Repos", the rail and Back, and the browser from a reload.
  const baseBranchJobsForm = useRef(null);
  const postMergeDispatchJobsForm = useRef(null);
  const savedJobs = useRef({});
  savedJobs.current = { baseBranchJobs, postMergeDispatchJobs };
  useEffect(
    () =>
      guardLeaving(() =>
        [
          [baseBranchJobsForm, "baseBranchJobs"],
          [postMergeDispatchJobsForm, "postMergeDispatchJobs"],
        ].some(([form, field]) => {
          const box = form.current?.elements[field];
          if (!box) return false;
          return (
            jobList(box.value).join(",") !== savedJobs.current[field].join(",")
          );
        }),
      ),
    [],
  );

  const load = useCallback(async () => {
    try {
      setSettings(await api(`${root}/release`));
    } catch (err) {
      showError(err);
    }
  }, [root, showError]);

  useEffect(() => {
    setSettings(null);
    load();
  }, [load]);

  useEffect(() => {
    if (refreshKey) load();
  }, [refreshKey, load]);

  const toggle = async () => {
    try {
      const updated = await api(`${root}/release`, {
        method: "PUT",
        body: JSON.stringify({ enabled: !releaseEnabled }),
      });
      setSettings(updated);
      onToggled?.();
      await onRefreshConfig?.();
    } catch (err) {
      showError(err);
    }
  };

  // The other half of the switch above: the merge queue tests a base
  // branch with the release run, so turning releases off used to stop
  // `main` being tested at all. Naming the jobs that judge it keeps that
  // check, run on push and publishing nothing.
  const saveBaseBranchJobs = async (event) => {
    event.preventDefault();
    const jobs = jobList(event.currentTarget.elements.baseBranchJobs.value);
    try {
      const updated = await api(`${root}/release`, {
        method: "PUT",
        body: JSON.stringify({ baseBranchJobs: jobs }),
      });
      setSettings(updated);
      await onRefreshConfig?.();
    } catch (err) {
      showError(err);
    }
  };

  // The piece of a release workflow that is a build input rather than a
  // release, named so that it follows the base branch whatever the
  // switch above is doing (grain/task-409). Grain dispatches these on
  // the base branch after every merge; a failure of one is a cache that
  // stayed cold, never a held merge queue.
  const savePostMergeDispatchJobs = async (event) => {
    event.preventDefault();
    const jobs = jobList(
      event.currentTarget.elements.postMergeDispatchJobs.value,
    );
    try {
      const updated = await api(`${root}/release`, {
        method: "PUT",
        body: JSON.stringify({ postMergeDispatchJobs: jobs }),
      });
      setSettings(updated);
      await onRefreshConfig?.();
    } catch (err) {
      showError(err);
    }
  };

  const saveSecret = async (event) => {
    event.preventDefault();
    const form = event.currentTarget;
    const secretName = form.elements.releaseSecretName.value.trim();
    const value = form.elements.releaseSecretValue.value;
    if (!secretName || !value) return;
    try {
      const updated = await api(
        `${root}/release-secrets/${encodeURIComponent(secretName)}`,
        { method: "PUT", body: JSON.stringify({ value }) },
      );
      setSettings(updated);
      await onRefreshConfig?.();
      setAdding(false);
      setEditing("");
      form.reset();
    } catch (err) {
      showError(err);
    }
  };

  const removeSecret = async (secretName) => {
    if (!confirm(`Remove ${secretName} from ${repo}'s release workflows?`))
      return;
    try {
      const updated = await api(
        `${root}/release-secrets/${encodeURIComponent(secretName)}`,
        { method: "DELETE" },
      );
      setSettings(updated);
      await onRefreshConfig?.();
    } catch (err) {
      showError(err);
    }
  };

  // The other by-hand control, and the one that is not a release
  // (grain/task-480): run a base branch's own check now, without waiting
  // for a merge to move its tip. It asks rather than starts -- the
  // request goes on the branch's row and the base-branch reconciler is
  // what starts the run, which is what keeps one run in flight per branch
  // -- so what comes back is a sentence about when, and it is shown
  // rather than swallowed.
  const runBaseBranchCheck = async (branch) => {
    try {
      const asked = await api(`${root}/base-branch-runs`, {
        method: "POST",
        body: JSON.stringify({ branch }),
      });
      setBaseBranchRun(asked?.detail || null);
    } catch (err) {
      showError(err);
    }
  };

  const startRelease = async (event) => {
    event.preventDefault();
    const branch = event.currentTarget.elements.releaseBranch.value.trim();
    if (!branch || starting) return;
    setStarting(true);
    try {
      const run = await api(`${root}/release-runs`, {
        method: "POST",
        body: JSON.stringify({ branch }),
      });
      setRunning(false);
      // The form -- Run release, which had focus -- closes here, so focus
      // goes back to the button that opened it before the run's pane
      // opens: that pane hands focus back on close to whatever held it
      // when it opened, and an unmounted button left it on the page body
      // (grain/task-2414).
      manualToggle.current?.focus();
      if (run?.id) onOpenRun(run.id, repo);
    } catch (err) {
      showError(err);
    } finally {
      setStarting(false);
    }
  };

  return (
    <Box>
      <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
        Release workflows
      </Typography>
      {settings === null ? (
        <Typography variant="body2" color="text.secondary">
          Loading release settings…
        </Typography>
      ) : (
        <Stack spacing={1.5}>
          <Typography variant="body2" color="text.secondary">
            Grain runs workflows triggered by <code>release</code> after a pull
            request merges. Custom secrets are repository-scoped and appear only
            in trusted release or manually dispatched jobs, as mode-0600 files
            under {settings.secretsDir}.
          </Typography>
          <Stack
            direction="row"
            spacing={1}
            alignItems="center"
            flexWrap="wrap"
          >
            <Chip
              size="small"
              color={releaseEnabled ? "success" : "default"}
              label={
                releaseEnabled
                  ? "Automatic releases on"
                  : "Automatic releases off"
              }
            />
            <Button
              size="small"
              disabled={readOnly || configReadOnly}
              aria-describedby={describedBy}
              onClick={toggle}
            >
              {releaseEnabled
                ? "Turn off automatic releases"
                : "Turn on automatic releases"}
            </Button>
            <Button
              size="small"
              ref={manualToggle}
              disabled={readOnly}
              aria-describedby={describedBy}
              onClick={() => setRunning((value) => !value)}
            >
              {running ? "Cancel manual release" : "Run release by hand"}
            </Button>
            {/*
              The second of the two operator controls the merge queue's
              account of a red base branch is meant to carry
              (docs/merge-queue-behind.md, "The two operator controls").
              One button per branch grain is watching, rather than a box
              to type a branch name into: the set is grain's own rows,
              and a name that is not one of them has nothing here to
              re-run. It is what re-asks the one question grain will not
              re-ask by itself -- a run an operator cancelled, which is
              left alone because they meant it about that commit.
            */}
            {baseBranches.map((branch) => (
              <Button
                key={branch}
                size="small"
                disabled={readOnly}
                title={`Ask grain to run ${branch}'s own check now, without waiting for a merge to move its tip. The run starts on the next base-branch cycle, and waits if one is already judging this branch.`}
                onClick={() => runBaseBranchCheck(branch)}
              >
                {baseBranches.length === 1
                  ? "Run the base-branch check now"
                  : `Run the check on ${branch} now`}
              </Button>
            ))}
          </Stack>
          {/*
            What grain said it would do about it. The request is honoured
            by a reconciler rather than by the click, so "asked for" and
            "waiting for the run already going" are different answers and
            a button that said neither would look like one that did
            nothing.
          */}
          {baseBranchRun && <Alert severity="info">{baseBranchRun}</Alert>}
          {/*
            What has actually become of the base-branch check, as opposed
            to what the setting below says should be checking it
            (grain/task-453). It is outside the releases-off block on
            purpose, and since grain/task-461 that is the whole of it:
            the release switch decides nothing about judging any more, so
            every way this check stops working is a way it stops with
            every switch on this pane in the position somebody chose — a
            run going five hours with no verdict, a merge nothing has
            judged since yesterday, a watched branch nothing will ever
            enrol. A branch nothing is judging at all is an error, and
            since it is a control that is off rather than a setting that
            is unusual it is said in the page header instead, where it is
            seen on arrival; what reaches this alert is only ever late.
          */}
          {baseBranchGate && (
            <Alert severity="warning">{baseBranchGate.detail}</Alert>
          )}
          {/* What the switch below has actually come to, as opposed to
              what position it is in. It is outside the releases-off
              block for the gate alert's reason and one of its own: a
              repository whose releases are switched off publishes
              nothing on purpose and is never reported here at all
              (pkg/ui/basebranchrelease.go), so a line that does appear
              is always about a repository trying to publish and
              failing. A warning rather than an error, because nothing
              is held up by it — that is the whole of grain/task-461's
              ruling, and this pane must not talk anybody into putting
              the hold back. */}
          {baseBranchPublishing && (
            <Alert severity="warning">{baseBranchPublishing.detail}</Alert>
          )}
          {!releaseEnabled && (
            <>
              <Alert severity="info">
                Merges will not start release workflows. A manual release can
                still be run below.
                {baseBranchJobs.length
                  ? ` The merge queue still checks this repository's base
                      branches: ${baseBranchJobs.join(", ")} runs on push after
                      every merge, and nothing is published.`
                  : ` The merge queue still checks this repository's base
                      branches: everything a push would run runs on push after
                      every merge, and nothing is published. Name the jobs that
                      judge a base branch below to pay for fewer of them — or
                      if a publish job of this repository's is in its own push
                      plan and would fail for want of release credentials.`}
              </Alert>
              {/*
                The other thing that stops, and the one nothing said
                (grain/task-397). This switch is named for releases, but
                this repository's release workflow is also what publishes
                the sandbox guest every task and every CI job in this
                deployment runs in -- and that guest is a CI input, not a
                release artifact. With it unpublished the content-addressed
                cache stops following the base branch, and each branch
                builds a ~1GB guest inline in a nested VM instead: tens of
                minutes on the longest job in the suite, on every branch in
                flight, against a fixed pool of CI sandboxes.
              */}
              {settings.publishesSandboxGuest &&
                (settings.sandboxGuestCacheAutomatic ||
                postMergeDispatchJobs.includes(
                  settings.sandboxGuestCacheJob,
                ) ? (
                  <Alert severity="info">
                    This repository&apos;s release workflow is also what
                    publishes the sandbox guest this deployment runs — but{" "}
                    <code>{settings.sandboxGuestCacheJob}</code> is dispatched
                    on the base branch after every merge, so the
                    content-addressed guest cache goes on following it with
                    releases off. What stops is the moving tags a deployment
                    resolves, not the cache every branch&apos;s CI reads.
                    {settings.sandboxGuestCacheAutomatic &&
                      !postMergeDispatchJobs.includes(
                        settings.sandboxGuestCacheJob,
                      ) &&
                      " Grain does that on its own here, because this is the" +
                        " guest its own sandboxes are built from; there is" +
                        " nothing to set, and turning releases back on hands" +
                        " the work to the release run again."}
                  </Alert>
                ) : (
                  /*
                    Grain keeps the cache warm by itself wherever it can
                    (grain/task-457), so what is left to warn about is the
                    case where it cannot: this repository publishes the
                    guest, but its CI holds no package-publish, and a job
                    grain dispatched would stop on its first step for want
                    of the credential. That is an operator decision, not a
                    field to fill in.
                  */
                  <Alert severity="warning">
                    This repository&apos;s release workflow is also what
                    publishes the sandbox guest this deployment runs, and a
                    guest is a CI input rather than a release artifact. With
                    releases off, nothing fills the content-addressed guest
                    cache after a merge, so every branch whose CI resolves a
                    guest builds one inline — around a gigabyte inside a nested
                    VM, tens of minutes on the longest job in the suite, on
                    every branch at once. Grain would keep filling it for you,
                    but this repository&apos;s CI is not granted{" "}
                    <code>package-publish</code>, so a job it dispatched could
                    not push. Grant that, or name{" "}
                    <code>{settings.sandboxGuestCacheJob}</code> among the jobs
                    dispatched after a merge, below.
                  </Alert>
                ))}
              <Stack
                component="form"
                direction={{ xs: "column", sm: "row" }}
                spacing={1}
                ref={baseBranchJobsForm}
                onSubmit={saveBaseBranchJobs}
                key={baseBranchJobs.join(",")}
              >
                <TextField
                  name="baseBranchJobs"
                  label="Jobs that judge base branches"
                  placeholder="go, lint"
                  defaultValue={baseBranchJobs.join(", ")}
                  size="small"
                  fullWidth
                  autoComplete="off"
                  disabled={readOnly || configReadOnly}
                  inputProps={{
                    "aria-describedby":
                      readOnly || configReadOnly ? describedBy : undefined,
                  }}
                  helperText="Grain CI job ids, comma-separated, run on push against the base branch after every merge. Empty means every job a push would run."
                />
                <Button
                  type="submit"
                  variant="contained"
                  size="small"
                  disabled={readOnly || configReadOnly}
                  aria-describedby={describedBy}
                >
                  Save jobs
                </Button>
              </Stack>
            </>
          )}
          {/*
            Outside the block above on purpose: these jobs run whether or
            not releases do, which is the whole of what they are for
            (grain/task-409). They are the piece of a release workflow
            that is a build input — this deployment's sandbox guest cache
            — named separately from the publishing it happened to live
            in, so that turning publishing off does not turn it off too.
          */}
          <Stack
            component="form"
            direction={{ xs: "column", sm: "row" }}
            spacing={1}
            ref={postMergeDispatchJobsForm}
            onSubmit={savePostMergeDispatchJobs}
            key={postMergeDispatchJobs.join(",")}
          >
            <TextField
              name="postMergeDispatchJobs"
              label="Jobs dispatched after every merge"
              placeholder=".grain/workflows/image.yml:guest-cache"
              defaultValue={postMergeDispatchJobs.join(", ")}
              size="small"
              fullWidth
              autoComplete="off"
              disabled={readOnly || configReadOnly}
              inputProps={{
                "aria-describedby":
                  readOnly || configReadOnly ? describedBy : undefined,
              }}
              helperText="Grain CI job ids, comma-separated, dispatched on the base branch once it moves — whatever releases are doing. For the part of a release workflow that is a build input, such as a cache later branches read. A failure here never holds the merge queue."
            />
            <Button
              type="submit"
              variant="contained"
              size="small"
              disabled={readOnly || configReadOnly}
              aria-describedby={describedBy}
            >
              Save dispatched jobs
            </Button>
          </Stack>
          {running && (
            <Stack
              component="form"
              direction={{ xs: "column", sm: "row" }}
              spacing={1}
              onSubmit={startRelease}
            >
              <TextField
                name="releaseBranch"
                label="Release branch"
                placeholder="main"
                required
                size="small"
                autoComplete="off"
                inputProps={identifierField}
              />
              <Button
                type="submit"
                variant="contained"
                size="small"
                disabled={starting}
              >
                Run release
              </Button>
            </Stack>
          )}
          <Typography variant="body2">Custom secrets</Typography>
          {!settings.available && (
            <Alert severity="info">
              This UI is not connected to the deployment&apos;s encrypted
              secrets store.
            </Alert>
          )}
          {secrets.length === 0 ? (
            <Typography variant="body2" color="text.secondary">
              No custom release secrets configured.
            </Typography>
          ) : (
            secrets.map((secret) => (
              <Stack key={secret.name} spacing={1}>
                <Stack direction="row" spacing={1} alignItems="center">
                  <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
                    {secret.name}
                  </Typography>
                  <Chip
                    size="small"
                    color={secret.set ? "success" : "warning"}
                    label={secret.set ? "Set" : "Missing"}
                  />
                  {canEditSecrets && (
                    <>
                      <Button
                        size="small"
                        onClick={() => setEditing(secret.name)}
                      >
                        Replace
                      </Button>
                      <Button
                        size="small"
                        color="error"
                        onClick={() => removeSecret(secret.name)}
                      >
                        Remove
                      </Button>
                    </>
                  )}
                </Stack>
                {canEditSecrets && editing === secret.name && (
                  <Stack
                    component="form"
                    direction="row"
                    spacing={1}
                    onSubmit={saveSecret}
                  >
                    <input
                      type="hidden"
                      name="releaseSecretName"
                      value={secret.name}
                    />
                    <TextField
                      name="releaseSecretValue"
                      label={`New value for ${secret.name}`}
                      type="password"
                      required
                      size="small"
                      autoComplete="new-password"
                    />
                    <Button type="submit" variant="contained" size="small">
                      Save
                    </Button>
                    <Button size="small" onClick={() => setEditing("")}>
                      Cancel
                    </Button>
                  </Stack>
                )}
              </Stack>
            ))
          )}
          {settings.available && canEditSecrets && (
            <>
              <Button
                size="small"
                sx={{ alignSelf: "flex-start" }}
                onClick={() => setAdding((value) => !value)}
              >
                {adding ? "Cancel new secret" : "Add custom secret"}
              </Button>
              {adding && (
                <Stack component="form" spacing={1} onSubmit={saveSecret}>
                  <TextField
                    name="releaseSecretName"
                    label="Secret name"
                    placeholder="NPM_TOKEN"
                    required
                    size="small"
                    autoComplete="off"
                    inputProps={identifierField}
                    helperText="Uppercase letters, digits, and underscores; start with a letter or underscore."
                  />
                  <TextField
                    name="releaseSecretValue"
                    label="Secret value"
                    type="password"
                    required
                    size="small"
                    autoComplete="new-password"
                  />
                  <Button
                    type="submit"
                    variant="contained"
                    size="small"
                    sx={{ alignSelf: "flex-end" }}
                  >
                    Save secret
                  </Button>
                </Stack>
              )}
            </>
          )}
        </Stack>
      )}
    </Box>
  );
}
