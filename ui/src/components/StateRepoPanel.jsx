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

import { useEffect, useState } from "react";
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
import StateRepoBrowser from "./StateRepoBrowser.jsx";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";
import { identifierField } from "../identifierField.js";

// The controller-local config repository and its operational state.
//
// grain exports its database there as JSON so configuration can be changed
// through native grain/config pull requests. The repository is no longer
// mirrored to an external Git remote; grain backup protects it and the
// database together without exposing credentials.
//
// The pane also carries one thing that is not a setting at all: what a
// merged settings change replaced on a deployment that had nowhere to
// say so (the notice below, docs/state-pane-reset-notice.md). It is here
// because it is about this repository and because there is nowhere else
// for such a deployment to be told -- and it is dismissable, unlike
// everything else on this pane, because it is an event that happened
// once rather than a condition that is still true.
//
// onStatusChange, when given, re-reads GET /api/config after an action
// changes what the deployment reports. Optional -- a panel rendered without
// it simply leaves that to the next poll.
// resetValueCount is how many values one merge replaced: the ones this
// notice carries plus the ones past the cap it kept, since the count is
// of what was lost rather than of what fits on a pane.
const resetValueCount = (notice) =>
  (notice.values?.length || 0) + (notice.moreValues || 0);

export default function StateRepoPanel({ showError, onStatusChange }) {
  const zone = useTimeZone();
  const [status, setStatus] = useState(null);
  // A restored secrets file may arrive before the key that opens it, so
  // importing that key remains its own action.
  const [importKey, setImportKey] = useState("");
  const [mirrorRemote, setMirrorRemote] = useState("");
  const [mirrorBranch, setMirrorBranch] = useState("main");
  const [mirrorToken, setMirrorToken] = useState("");
  // Where the encrypted file itself is backed up, which is a separate
  // repository from the one above and a separate question from the key
  // that opens it (pkg/secretsrepo). The pane has reported this since the
  // repository existed; these are what set it, so that the deployment
  // whose operator has no terminal on the host can act on the warning it
  // reads here rather than only read it.
  const [secretsRemote, setSecretsRemote] = useState("");
  const [secretsBranch, setSecretsBranch] = useState("main");
  const [secretsToken, setSecretsToken] = useState("");
  const [busy, setBusy] = useState(false);

  // Why the first read failed, or "" while it has not. Without it the tab
  // rendered nothing at all once the snackbar faded.
  const [loadError, setLoadError] = useState("");
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let live = true;
    setLoadError("");
    api("/api/state-repo")
      .then((s) => {
        if (!live) return;
        setStatus(s);
        // Prefilled from what is in force, so that changing the branch
        // of a repository already named does not mean retyping its URL
        // -- and so that an operator can see, in the field they are
        // about to edit, exactly what they are editing.
        if (s.remote) setMirrorRemote(s.remote);
        if (s.branch) setMirrorBranch(s.branch);
        if (s.secretsRepo) setSecretsRemote(s.secretsRepo);
        if (s.secretsRepoBranch) setSecretsBranch(s.secretsRepoBranch);
      })
      .catch((e) => {
        if (!live) return;
        setLoadError(e?.message || String(e));
        showError?.(e.message);
      });
    return () => {
      live = false;
    };
  }, [showError, attempt]);

  const act = async (path, body) => {
    setBusy(true);
    try {
      setStatus(
        await api(path, { method: "POST", body: JSON.stringify(body || {}) }),
      );
      // Credentials have reached the daemon and are written to files only it
      // reads; keeping them in form fields afterwards serves nobody.
      setImportKey("");
      setMirrorToken("");
      setSecretsToken("");
      await onStatusChange?.();
    } catch (e) {
      showError?.(e.message);
    } finally {
      setBusy(false);
    }
  };

  if (!status && loadError) {
    return (
      <Alert
        severity="error"
        action={
          <Button
            color="inherit"
            size="small"
            onClick={() => setAttempt((n) => n + 1)}
          >
            Try again
          </Button>
        }
      >
        The config repository&apos;s status could not be read ({loadError}).
      </Alert>
    );
  }
  if (!status) return null;
  if (!status.available) {
    return (
      <Typography variant="body2" color="text.secondary">
        This UI is not running inside a daemon that owns a config repository, so
        there is nothing to configure here.
      </Typography>
    );
  }

  const schemaMismatch =
    status.schemaVersion > 0 &&
    status.schemaVersion !== status.buildSchemaVersion;

  return (
    <Box>
      <Typography variant="subtitle2">Config repository</Typography>
      {status.remote ? (
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          <strong>External repository (Infrastructure as Code):</strong> grain
          does not host <code>grain/config</code> locally; it reads
          configuration directly from <code>{status.remote}</code> on branch{" "}
          <code>{status.branch || "main"}</code>, never exports or pushes
          daemon-generated commits to it, and UI settings are read-only.{" "}
          <code>grain backup</code> protects the checkout and the Grain database
          together in Google Drive or GCS; daemon backup flags schedule the same
          operation. Credentials are not included.
        </Typography>
      ) : (
        <>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            <strong>
              Internal (<code>grain/config</code> on this host &mdash; default):
            </strong>{" "}
            grain hosts <code>grain/config</code> locally and exports UI
            settings changes into it &mdash; one JSON file per table &mdash; so
            settings, tasks and metrics can be read and configuration can be
            changed through native <code>grain/config</code> pull requests.
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            No external config repository is configured.{" "}
            <code>grain backup</code> protects it and the Grain database
            together in Google Drive or GCS; daemon backup flags schedule the
            same operation. Credentials are not included.
          </Typography>
        </>
      )}
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        A merged change to the settings tables &mdash; templates, suites,
        schedules, repo and deployment configuration &mdash; is applied and live
        within half a minute, with no restart. Tasks, runs and metrics are
        grain&apos;s own record of what it did and are only replaced when grain
        starts, so nothing here rewrites rows a run in flight is holding.
      </Typography>

      <Stack
        direction="row"
        spacing={1}
        alignItems="center"
        sx={{ mb: 1 }}
        flexWrap="wrap"
      >
        {status.remote ? (
          <>
            {status.repository && status.repository !== status.remote && (
              <Chip size="small" variant="outlined" label={status.repository} />
            )}
            <Chip size="small" variant="outlined" label={status.remote} />
          </>
        ) : (
          <Chip size="small" variant="outlined" label="grain/config" />
        )}
        {status.branch && (
          <Chip size="small" variant="outlined" label={status.branch} />
        )}
        {status.head && (
          <Chip
            size="small"
            variant="outlined"
            label={status.head.slice(0, 8)}
          />
        )}
      </Stack>
      <Typography variant="caption" color="text.secondary" component="div">
        working tree: {status.dir}
      </Typography>
      {status.lastSnapshot && (
        <Typography variant="caption" color="text.secondary" component="div">
          last settings snapshot: <code>{status.lastSnapshot}</code>
          {status.lastSnapshotAt
            ? ` (${formatDateTime(status.lastSnapshotAt, zone)})`
            : ""}
        </Typography>
      )}
      {/* grain replaces whole settings rows, so omitted columns land on
          schema defaults. With a controller-local repository there is no
          external pull-request conversation for that notice; the pane and
          journal retain the values instead. */}
      {status.resetNotices?.length > 0 && (
        <Alert
          severity={
            status.resetNotices.some((n) => n.failed) ? "warning" : "info"
          }
          sx={{ mt: 2 }}
          action={
            // Dismissing deletes them for this deployment, not merely
            // this browser. The journal keeps the values either way.
            <Button
              type="button"
              size="small"
              color="inherit"
              disabled={busy}
              onClick={() => act("/api/state-repo/reset-notice")}
            >
              Dismiss
            </Button>
          }
        >
          {status.resetNotices.map((notice, i) => (
            <Box key={`${notice.commit}-${notice.at}-${i}`} sx={{ mb: 1 }}>
              Merging <code>{(notice.commit || "").slice(0, 8)}</code> on{" "}
              {formatDateTime(notice.at, zone)} replaced{" "}
              {resetValueCount(notice)} settings{" "}
              {resetValueCount(notice) === 1 ? "value" : "values"} on this
              deployment. {notice.reason}. This notice and the deployment&apos;s
              journal are the only places those values are written down.
              {/* These are arbitrary database values, with each line
                  already cut to a readable width by pkg/staterepo. */}
              <Typography
                variant="caption"
                component="pre"
                sx={{ mt: 1, mb: 0, overflowX: "auto", whiteSpace: "pre-wrap" }}
              >
                {(notice.values || [])
                  .map(
                    (v) =>
                      `${v.path} (${v.row}): ${v.column} was ${v.from} and is now ${v.to}`,
                  )
                  .join("\n")}
                {notice.moreValues
                  ? `\n... and ${notice.moreValues} more, in this deployment's journal`
                  : ""}
              </Typography>
            </Box>
          ))}
          {status.resetNoticesDropped > 0 && (
            <Box component="div">
              {status.resetNoticesDropped} earlier{" "}
              {status.resetNoticesDropped === 1 ? "notice was" : "notices were"}{" "}
              dropped to keep this bounded, and{" "}
              {status.resetNoticesDropped === 1 ? "is" : "are"} in this
              deployment&apos;s journal only.
            </Box>
          )}
          <Box component="div" sx={{ mt: 1 }}>
            If any of that was not meant, merge a settings file that carries the
            column with the value it should have; grain&apos;s next export
            writes whatever the database holds back out either way.
          </Box>
        </Alert>
      )}
      {status.error && (
        <Alert severity={status.diverged ? "info" : "warning"} sx={{ mt: 2 }}>
          {status.error}
        </Alert>
      )}
      {schemaMismatch && (
        <Alert severity="error" sx={{ mt: 2 }}>
          The repository holds schema {status.schemaVersion} and this build
          knows {status.buildSchemaVersion}. grain does not migrate a dump
          between schemas; the repository has to be re-seeded.
        </Alert>
      )}

      <Button
        type="button"
        size="small"
        variant="outlined"
        disabled={busy}
        sx={{ mt: 2 }}
        onClick={() => act("/api/state-repo/sync")}
      >
        {status.remote ? "Pull now" : "Export now"}
      </Button>

      <Typography variant="subtitle2" sx={{ mt: 3 }}>
        Config repository mode
      </Typography>
      {status.remote ? (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
          <strong>External repository (Infrastructure as Code):</strong> grain
          does not host <code>grain/config</code> locally; it reads
          configuration directly from <code>{status.remote}</code> on branch{" "}
          <code>{status.branch || "main"}</code>, and UI settings are read-only.
        </Typography>
      ) : (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
          <strong>
            Internal (<code>grain/config</code> on this host &mdash; default):
          </strong>{" "}
          grain hosts <code>grain/config</code> locally and exports UI settings
          changes into it. Name a repository below &mdash; or run{" "}
          <code>grain state repo &lt;git-url&gt;</code> on the host, which
          writes the same <code>&lt;data-dir&gt;/state-repo.json</code> &mdash;
          to switch to an external config repository (read-only Infrastructure
          as Code mode).
        </Typography>
      )}
      <TextField
        label="Repository URL"
        value={mirrorRemote}
        onChange={(e) => setMirrorRemote(e.target.value)}
        placeholder="https://github.com/owner/grain-config.git"
        helperText="An external Git repository to read configuration from (read-only Infrastructure as Code mode; UI settings become read-only and grain/config is not hosted locally)."
        autoComplete="off"
        inputProps={identifierField}
        fullWidth
        margin="normal"
        size="small"
      />
      <TextField
        label="Branch"
        value={mirrorBranch}
        onChange={(e) => setMirrorBranch(e.target.value)}
        autoComplete="off"
        inputProps={identifierField}
        fullWidth
        margin="normal"
        size="small"
      />
      <TextField
        label="Personal access token (optional)"
        value={mirrorToken}
        onChange={(e) => setMirrorToken(e.target.value)}
        helperText="Leave empty to use this deployment's own GitHub credential. Stored on the host, never shown again."
        type="password"
        autoComplete="off"
        fullWidth
        margin="normal"
        size="small"
      />
      <Stack direction="row" spacing={1}>
        <Button
          type="button"
          size="small"
          variant="outlined"
          disabled={busy || !mirrorRemote.trim()}
          onClick={() =>
            act("/api/state-repo/remote", {
              remote: mirrorRemote.trim(),
              branch: mirrorBranch.trim(),
              token: mirrorToken,
            })
          }
        >
          Use external config repository
        </Button>
        {status.remote && (
          <Button
            type="button"
            size="small"
            disabled={busy}
            onClick={() => {
              setMirrorRemote("");
              act("/api/state-repo/remote", { none: true });
            }}
          >
            Switch to internal config repository
          </Button>
        )}
      </Stack>

      {/* And reading what that export wrote. This repository is
          controller-local, so there is no page anywhere else that shows
          it: without this, the only ways to read grain's own
          configuration -- or the memory prose every run in a scope is
          given as fact -- are a sandbox that has it checked out or a
          shell on the host. */}
      <StateRepoBrowser
        showError={showError}
        status={status}
        onSync={() => act("/api/state-repo/sync")}
      />

      <Typography variant="subtitle2" sx={{ mt: 3 }}>
        Secrets key
      </Typography>
      <Typography variant="body2" color="text.secondary">
        This host&apos;s secrets file is encrypted to this public key. Its
        private half lives at <code>{status.secretsKeyFile}</code> and is never
        committed anywhere &mdash; back that file up, because without it no one,
        grain included, can read those secrets again.
      </Typography>
      <Typography
        variant="caption"
        component="pre"
        sx={{ mt: 1, overflowX: "auto" }}
      >
        {status.secretsPublicKey || "(no key yet)"}
      </Typography>

      {/* Where the encrypted file itself is, which is a separate question
          from the key that opens it: the key is handed back by hand, and
          the file only comes back if it was pushed somewhere. Said either
          way, because "no backup" is exactly the state that otherwise
          shows as nothing at all. */}
      {status.secretsRepo ? (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
          The encrypted file is backed up to <code>{status.secretsRepo}</code>,
          as <code>{status.secretsRepoFile}</code>. That repository is refused
          to every sandbox, and the key above is not in it &mdash; a clone of it
          opens nothing.
        </Typography>
      ) : (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
          The encrypted file itself is on this host alone: nothing pushes it
          anywhere. Name a repository of its own below &mdash; or run{" "}
          <code>grain secrets repo &lt;git-url&gt;</code> on the host, which
          writes the same <code>&lt;data-dir&gt;/secrets-repo.json</code>{" "}
          &mdash; to keep a copy off this disk.
        </Typography>
      )}

      {/* Naming that repository, from the pane that reports it. grain
          clones it and pushes on its next sync tick rather than here, so
          this saves a setting and returns immediately: an unreachable
          remote or a credential that does not cover it shows up in the
          error line above a tick later, not as a browser waiting out a
          clone. */}
      <TextField
        label="Secrets repository URL"
        value={secretsRemote}
        onChange={(e) => setSecretsRemote(e.target.value)}
        placeholder="https://github.com/owner/grain-secrets.git"
        helperText={
          "A private repository, which may be shared with other grain deployments: this " +
          "one's encrypted file has its own name."
        }
        autoComplete="off"
        inputProps={identifierField}
        fullWidth
        margin="normal"
        size="small"
      />
      <TextField
        label="Secrets repository branch"
        value={secretsBranch}
        onChange={(e) => setSecretsBranch(e.target.value)}
        autoComplete="off"
        inputProps={identifierField}
        fullWidth
        margin="normal"
        size="small"
      />
      <TextField
        label="Credential to push with (optional)"
        value={secretsToken}
        onChange={(e) => setSecretsToken(e.target.value)}
        helperText="Leave empty to push with this deployment's own GitHub credential. Stored on the host, never shown again."
        type="password"
        autoComplete="off"
        fullWidth
        margin="normal"
        size="small"
      />
      <Stack direction="row" spacing={1}>
        <Button
          type="button"
          size="small"
          variant="outlined"
          disabled={busy || !secretsRemote.trim()}
          onClick={() =>
            act("/api/state-repo/secrets-repo", {
              remote: secretsRemote.trim(),
              branch: secretsBranch.trim(),
              token: secretsToken,
            })
          }
        >
          Back up secrets here
        </Button>
        {status.secretsRepo && (
          <Button
            type="button"
            size="small"
            disabled={busy}
            onClick={() => {
              setSecretsRemote("");
              act("/api/state-repo/secrets-repo", { none: true });
            }}
          >
            Stop backing them up
          </Button>
        )}
      </Stack>
      {status.secretsRepo && (
        <Typography variant="caption" color="text.secondary" component="div">
          Stopping leaves what has already been pushed where it is; it only
          stops grain adding to it.
        </Typography>
      )}

      {status.secretsError && (
        <Alert severity="error" sx={{ mt: 2 }}>
          grain cannot read this host&apos;s secrets file: {status.secretsError}
          {status.secretsFileRecipient && (
            <>
              {" "}
              They are encrypted to <code>{status.secretsFileRecipient}</code>;
              paste that key&apos;s private half below to install it.
            </>
          )}
        </Alert>
      )}
      <TextField
        label="Import a private key"
        value={importKey}
        onChange={(e) => setImportKey(e.target.value)}
        helperText={
          "Installs a key you already hold, so a secrets file sealed by another installation becomes " +
          "readable here. A key that cannot open the file is refused, and the key it replaces is kept on disk."
        }
        type="password"
        autoComplete="off"
        fullWidth
        margin="normal"
        size="small"
      />
      <Button
        type="button"
        size="small"
        variant="outlined"
        disabled={busy || !importKey.trim()}
        onClick={() =>
          act("/api/state-repo/secrets-key", { key: importKey.trim() })
        }
      >
        Import key
      </Button>
    </Box>
  );
}
