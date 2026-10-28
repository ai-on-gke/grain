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
  Alert,
  Box,
  Button,
  Chip,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { useCallback, useEffect, useState } from "react";
import api from "../../api.js";

// The CLIs a run location can be set for, in the order the daemon
// resolves them (model.CLIRunFrameworks).
export const CLI_RUN_CLIS = [
  { framework: "claude", label: "claude" },
  { framework: "codex", label: "codex" },
  { framework: "antigravity", label: "agy (Antigravity)" },
  { framework: "custom", label: "custom commands" },
];

const LOCATIONS = [
  { value: "", label: "Default" },
  { value: "host", label: "Host" },
  { value: "container", label: "Container" },
  // The image's CLI with the deployment's login, as a container run, but
  // started detached through grain hostd so a daemon upgrade does not
  // interrupt it (docs/non-disruptive-upgrade.md). Only offered where the
  // daemon says so: claude, behind hostd.
  { value: "detached", label: "Detached in grain hostd" },
];

// resolvedLine is where a CLI will run on its next dispatch, as the
// daemon resolves it: "container (the daemon's own)", "host (/path,
// through grain hostd)" -- the path being the one hostd's allowlist entry
// runs -- or, for a development daemon on the bare machine, "host (path)".
export function resolvedLine(r) {
  if (!r) return "";
  if (r.location === "container") return "container (the daemon's own)";
  if (r.location === "detached") {
    return `detached (${r.hostPath || "the image's CLI"}, through grain hostd as ${r.entry || "its detached entry"}; outlives a daemon upgrade)`;
  }
  if (r.via === "hostd") {
    return r.hostPath
      ? `host (${r.hostPath}, through grain hostd)`
      : "host (through grain hostd)";
  }
  if (r.framework === "custom") return "host (each connection's own command)";
  return `host (${r.hostPath || "not found on PATH"})`;
}

// locationsFor is the options a CLI's select offers: the locations the
// daemon accepts for it (the resolution's `locations`), or -- with no
// daemon to say -- both. A stored value the daemon no longer accepts (a
// host from an earlier install, say) stays listed, disabled, so the
// select can show what is stored.
export function locationsFor(r, current) {
  const accepted = r?.locations || ["host", "container"];
  return LOCATIONS.filter(
    (l) => l.value === "" || accepted.includes(l.value) || l.value === current,
  ).map((l) => ({
    ...l,
    disabled: l.value !== "" && !accepted.includes(l.value),
  }));
}

// HostdWhere is where grain hostd is, after "grain hostd is up": at its
// socket, or -- for one on another machine that dials in to this daemon
// (grain hostd -server) -- the host it connected from.
function HostdWhere({ executor }) {
  if (!executor.remote) {
    return (
      <>
        {" "}
        at <code>{executor.socket}</code>
      </>
    );
  }
  const host = executor.hostd?.remote?.host;
  return host ? (
    <>
      {" "}
      on <code>{host}</code>, connected to this daemon
    </>
  ) : (
    " and connected to this daemon"
  );
}

// HostExecutorNotice says what "host" means for this deployment when it
// is not the bare machine: why there is no host to run on (Kubernetes, or
// a container with no grain hostd), or whether grain hostd answers.
export function HostExecutorNotice({ executor }) {
  if (!executor || executor.kind === "local") return null;
  if (!executor.hostAvailable) {
    return (
      <Alert severity="info" data-testid="cli-run-no-host">
        Host is not offered.{" "}
        {executor.reason.charAt(0).toUpperCase() + executor.reason.slice(1)}.
      </Alert>
    );
  }
  const hostd = executor.hostd;
  // What hostd said in hello: its own build, which an Upgrade brings to
  // the daemon's only once hostd has drained and restarted.
  const build = hostd?.build
    ? ` (build ${hostd.build}, hostd protocol ${hostd.protocol})`
    : "";
  if (hostd?.reachable && (hostd.updating || hostd.mismatch)) {
    // Up, but starting no new host runs: draining for its own upgrade,
    // or on a protocol this daemon cannot talk to. Runs already going
    // carry on either way.
    return (
      <Alert severity="warning" data-testid="cli-run-hostd-status">
        grain hostd is up
        <HostdWhere executor={executor} />
        {build}, but{" "}
        {hostd.updating
          ? "it is updating: new host runs wait until it is back, and runs already going carry on."
          : `it cannot run this daemon's host CLIs (${hostd.mismatch}).`}
      </Alert>
    );
  }
  if (hostd?.reachable) {
    const offers = hostd.offers?.length
      ? `, offering ${hostd.offers.join(", ")}`
      : ", offering nothing";
    return (
      <Alert severity="success" data-testid="cli-run-hostd-status">
        grain hostd is up
        <HostdWhere executor={executor} />
        {build}
        {offers}. Host runs go through it.
      </Alert>
    );
  }
  if (executor.remote && !hostd?.remote?.connected) {
    // A hostd on another machine dials in (hostd -server); none has, or
    // the one that did has dropped and not yet reconnected.
    return (
      <Alert severity="warning" data-testid="cli-run-hostd-status">
        This daemon takes a grain hostd that dials in to it, but none is
        connected. On the host, run{" "}
        <code>
          grain hostd -server &lt;this daemon&apos;s URL&gt; -token-file &lt;the
          hostd token&gt;
        </code>
        . A CLI set to host fails its runs until it connects.
      </Alert>
    );
  }
  if (executor.remote) {
    const host = hostd.remote.host;
    return (
      <Alert severity="warning" data-testid="cli-run-hostd-status">
        grain hostd is connected
        {host ? (
          <>
            {" "}
            from <code>{host}</code>
          </>
        ) : null}{" "}
        but is not answering{hostd.error ? ` (${hostd.error})` : ""}. A CLI set
        to host fails its runs until it does.
      </Alert>
    );
  }
  return (
    <Alert severity="warning" data-testid="cli-run-hostd-status">
      grain hostd is configured at <code>{executor.socket}</code> but is not
      answering{hostd?.error ? ` (${hostd.error})` : ""}. A CLI set to host
      fails its runs until hostd is back.
    </Alert>
  );
}

// processLine is one process hostd holds, as HostdProcesses lists it.
export function processLine(p) {
  const what = p.kind === "cli" ? `${p.name}` : `${p.kind} ${p.name}`;
  const run = p.run ? ` for run ${p.run} of task ${p.task}` : "";
  let state = "running";
  if (p.exited) state = "ended, waiting for a daemon to read how";
  else if (p.detached && !p.attached) state = "running, no daemon attached";
  else if (p.detached) state = "running, detached";
  if (p.adopted) state += ", adopted by this daemon";
  return `${what}${run}: pid ${p.pid}, ${state}`;
}

// shortVersion is an installer version's image ID cut to docker's own 12.
const shortVersion = (id) => (id && id.length > 12 ? id.slice(0, 12) : id);

// HostdUpgrade says what the installer has put behind grain hostd: the
// version (and build) its current link names when hostd is still
// running another -- an upgrade waiting on hostd's drain -- and the
// allowlist entries this edition runs on the host that the operator's
// allowlist lacks. Those are only suggested: nothing writes the
// allowlist for them.
export function HostdUpgrade({ hostd }) {
  if (!hostd?.reachable) return null;
  const pending =
    hostd.current && hostd.running && hostd.current !== hostd.running;
  const suggest = hostd.suggest || [];
  if (!pending && suggest.length === 0) return null;
  return (
    <Stack spacing={1} sx={{ minWidth: 0 }}>
      {pending ? (
        <Typography
          variant="body2"
          data-testid="cli-run-hostd-current"
          sx={{ overflowWrap: "anywhere" }}
        >
          Installed: build <code>{hostd.currentBuild || "unknown"}</code>{" "}
          (version <code>{shortVersion(hostd.current)}</code>); grain hostd runs
          version <code>{shortVersion(hostd.running)}</code>
          {hostd.updating
            ? " and restarts on the installed one once the runs it holds have ended."
            : " until it next drains or restarts."}
        </Typography>
      ) : null}
      {suggest.length ? (
        <Alert severity="info" data-testid="cli-run-hostd-suggest">
          Suggested allowlist {suggest.length === 1 ? "entry" : "entries"}:{" "}
          {suggest.join(", ")}. This edition runs{" "}
          {suggest.length === 1 ? "it" : "them"} on the host by default, and{" "}
          <code>/etc/grain/hostd.yaml</code> has no such entry. Copy it from{" "}
          <code>/etc/grain/hostd.yaml.default</code>; grain never edits your
          allowlist.
        </Alert>
      ) : null}
    </Stack>
  );
}

// HostdProcesses lists what grain hostd holds now: each host run's CLI,
// started detached so that it outlives a daemon upgrade and is adopted
// by the next daemon, and each run: host capability server.
export function HostdProcesses({ hostd }) {
  const procs = hostd?.processes || [];
  if (!hostd?.reachable || procs.length === 0) return null;
  return (
    <Box data-testid="cli-run-hostd-processes">
      <Typography variant="body2" color="text.secondary">
        grain hostd is holding {procs.length}{" "}
        {procs.length === 1 ? "process" : "processes"}. A host run keeps running
        across a daemon upgrade, and the next daemon adopts it.
      </Typography>
      <Box component="ul" sx={{ m: 0, pl: 3 }}>
        {procs.map((p) => (
          <Typography
            component="li"
            variant="body2"
            key={p.id}
            sx={{ overflowWrap: "anywhere" }}
          >
            {processLine(p)}
          </Typography>
        ))}
      </Box>
    </Box>
  );
}

// CLIRunLocations is the "Where each agent CLI runs" card of Settings →
// Models: per CLI, host or container, saved with PUT
// /api/settings/cli-run and applied to that CLI's next run. Each row
// shows where the daemon will run it and why (GET /api/settings/cli-run's
// resolved list).
export default function CLIRunLocations({ showError, onSaved }) {
  const [loaded, setLoaded] = useState(null);
  const [draft, setDraft] = useState({});
  const [saving, setSaving] = useState(false);

  const reload = useCallback(async () => {
    try {
      const got = await api("/api/settings/cli-run");
      setLoaded(got);
      setDraft(got.settings || {});
    } catch (err) {
      if (showError) showError(err);
    }
  }, [showError]);
  useEffect(() => {
    reload();
  }, [reload]);

  if (!loaded) return null;
  const resolved = Object.fromEntries(
    (loaded.resolved || []).map((r) => [r.framework, r]),
  );
  const set = (framework, location) =>
    setDraft((d) => ({ ...d, [framework]: { location } }));
  const dirty =
    JSON.stringify(normalize(draft)) !==
    JSON.stringify(normalize(loaded.settings || {}));

  const save = async () => {
    setSaving(true);
    try {
      const got = await api("/api/settings/cli-run", {
        method: "PUT",
        body: JSON.stringify({ settings: normalize(draft) }),
      });
      setLoaded(got);
      setDraft(got.settings || {});
      onSaved?.();
    } catch (err) {
      showError?.(err);
    } finally {
      setSaving(false);
    }
  };

  const executor = loaded.hostExecutor;
  const bareMachine = executor?.kind === "local";

  return (
    <Stack spacing={1.5} data-testid="cli-run-locations">
      <Typography variant="body2" color="text.secondary">
        {bareMachine ? (
          <>
            Where each agent CLI runs. This daemon runs on the bare machine, as
            a development one does, so every CLI runs on this host as its own
            child. Container is offered only by a daemon in the release
            container.
          </>
        ) : (
          <>
            Where each agent CLI runs: in the daemon&apos;s own container, or on
            the host through grain hostd where this deployment has one. Detached
            runs the image&apos;s CLI with the deployment&apos;s login, as a
            container run does, but through grain hostd, so a daemon upgrade
            does not interrupt it.
          </>
        )}
      </Typography>
      <HostExecutorNotice executor={executor} />
      <HostdProcesses hostd={executor?.hostd} />
      <HostdUpgrade hostd={executor?.hostd} />
      {CLI_RUN_CLIS.map(({ framework, label }) => {
        const value = draft[framework] || {};
        const r = resolved[framework];
        return (
          <Box
            key={framework}
            data-testid={`cli-run-${framework}`}
            sx={{
              display: "grid",
              gap: 1,
              alignItems: "start",
              gridTemplateColumns: { xs: "1fr", md: "10rem 9rem 1fr" },
            }}
          >
            <Typography variant="subtitle2" sx={{ pt: { md: 1 } }}>
              {label}
            </Typography>
            <TextField
              select
              size="small"
              label="Runs on"
              value={value.location || ""}
              onChange={(e) => set(framework, e.target.value)}
              inputProps={{ "aria-label": `${label} run location` }}
            >
              {locationsFor(r, value.location || "").map((l) => (
                <MenuItem key={l.value} value={l.value} disabled={l.disabled}>
                  {l.label}
                  {l.disabled ? " (not available here)" : ""}
                </MenuItem>
              ))}
            </TextField>
            <Stack spacing={0.5} sx={{ minWidth: 0 }}>
              {r && (
                // The line wraps beside its chip rather than below it: a
                // host path through hostd is long enough to push the
                // whole line onto a row of its own.
                <Stack direction="row" spacing={1} alignItems="flex-start">
                  <Chip
                    size="small"
                    label={r.location}
                    color={r.location === "container" ? "info" : "default"}
                    data-testid={`cli-run-${framework}-location`}
                    sx={{ flexShrink: 0 }}
                  />
                  <Typography
                    variant="body2"
                    data-testid={`cli-run-${framework}-resolved`}
                    sx={{ overflowWrap: "anywhere", minWidth: 0, flex: 1 }}
                  >
                    {resolvedLine(r)}
                  </Typography>
                </Stack>
              )}
              {r && (
                <Typography variant="caption" color="text.secondary">
                  {r.reason}
                </Typography>
              )}
              {r?.hostUnavailable && !r?.error && (
                <Typography
                  variant="caption"
                  color="text.secondary"
                  data-testid={`cli-run-${framework}-host-unavailable`}
                  sx={{ overflowWrap: "anywhere" }}
                >
                  Host is unavailable for {label}: {r.hostUnavailable}
                </Typography>
              )}
              {r?.upgradeInterrupts && (
                // Where this CLI runs now, a daemon upgrade finishes its
                // runs as interrupted rather than carrying them across
                // the restart (docs/non-disruptive-upgrade.md). Said
                // here rather than discovered on the upgrade pane.
                // A standing fact rather than news, so a note and not an
                // alert: nothing for a screen reader to interrupt with.
                <Alert
                  severity="info"
                  role="note"
                  sx={{ py: 0 }}
                  data-testid={`cli-run-${framework}-upgrade-interrupts`}
                >
                  {r.upgradeInterrupts}
                </Alert>
              )}
              {r?.warning && (
                <Alert
                  severity="warning"
                  sx={{ py: 0 }}
                  data-testid={`cli-run-${framework}-warning`}
                >
                  {r.warning}
                </Alert>
              )}
              {r?.error && (
                <Alert severity="error" sx={{ py: 0 }}>
                  {r.error}
                </Alert>
              )}
            </Stack>
          </Box>
        );
      })}
      <Box>
        <Button
          variant="contained"
          size="small"
          disabled={!dirty || saving}
          onClick={save}
        >
          Save run locations
        </Button>
      </Box>
    </Stack>
  );
}

// normalize drops empty entries, so an untouched row and a cleared one
// compare (and save) the same.
function normalize(settings) {
  const out = {};
  for (const [k, v] of Object.entries(settings || {})) {
    const location = (v?.location || "").trim();
    if (location) out[k] = { location };
  }
  return out;
}
