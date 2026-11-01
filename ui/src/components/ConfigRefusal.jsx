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
  AlertTitle,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Link,
  Typography,
} from "@mui/material";
import api from "../api.js";
import CopyButton from "./CopyButton.jsx";
import ConfigRepoLink, { openInApp } from "./ConfigRepoLink.jsx";
import { settleEdits } from "../unsavedEdits.js";

// The refusal a configuration write gets when the deployment's
// configuration lives in an external config repository (pkg/ui's
// config_refusal.go): code "config_managed_externally", the repository,
// the file(s) holding the setting and the patch that makes the change.
export const CONFIG_MANAGED_EXTERNALLY = "config_managed_externally";

// The code POST /api/state-repo/pull-requests' 422 carries when the
// refusal's patch no longer applies: grain/config moved since
// (pkg/ui's ConfigChangeStale).
export const CONFIG_CHANGE_STALE = "config_change_stale";

// ConfigPullRequestError says why a config pull request was not opened:
// a stale patch as something to make again, the pre-review suite's
// liveness findings as a list, anything else in the server's words.
export function ConfigPullRequestError({ error, branch }) {
  const message = error?.message || String(error);
  if (error?.code === CONFIG_CHANGE_STALE) {
    return (
      <>
        <AlertTitle>grain/config has changed since this was refused</AlertTitle>
        {`This patch no longer applies to ${branch || "main"}, so no pull request was opened. Close this and make the change again for a patch that does.`}
        <Typography
          variant="caption"
          component="div"
          sx={{ mt: 0.5, opacity: 0.8 }}
        >
          {message}
        </Typography>
      </>
    );
  }
  const liveness = error?.validation?.liveness || [];
  if (liveness.length) {
    return (
      <>
        <AlertTitle>This change does not pass validation</AlertTitle>
        No pull request was opened: merged, it would leave grain unable to run
        anything.
        <Box component="ul" sx={{ m: 0, mt: 0.5, pl: 2.5 }}>
          {liveness.map((l) => (
            <li key={l}>{l}</li>
          ))}
        </Box>
      </>
    );
  }
  return message;
}

// configFileURL is the page of path in the repository, when the server
// could tell where the repository is browsable.
export function configFileURL(refusal, path) {
  if (!refusal?.url) return "";
  return `${refusal.url}/blob/${refusal.branch || "main"}/${path}`;
}

// configRepoRefusal is the part of a refusal the settings banner can
// know before anything is refused: the repository, from its remote, and
// where it is browsable (pkg/ui's repoSlugFromRemote, configRepoWebURL).
export function configRepoRefusal(remote, branch) {
  const s = String(remote || "")
    .trim()
    .replace(/\.git$/, "")
    .replace(/\/$/, "");
  let url = "";
  if (s.startsWith("https://github.com/")) url = s;
  else if (s.startsWith("git@github.com:"))
    url = `https://github.com/${s.slice("git@github.com:".length)}`;
  else if (s.startsWith("ssh://git@github.com/"))
    url = `https://github.com/${s.slice("ssh://git@github.com/".length)}`;
  const parts = s
    .replace(/^[a-z+]+:\/\/[^/]+\//, "")
    .replace(/^[^/:]+:/, "")
    .split("/");
  const repo = parts.length >= 2 ? parts.slice(-2).join("/") : remote;
  return { repo, remote, branch: branch || "main", url };
}

// isInternalRefusal is whether a refusal names this deployment's own
// grain/config (pkg/ui's config_refusal.go sets internal), which the
// Config Repo Browser shows and POST /api/state-repo/pull-requests opens
// pull requests in. An external repository's files and pull requests are
// on its own host.
export function isInternalRefusal(refusal) {
  return Boolean(refusal?.internal);
}

// configPullRequestTitle names a pull request after what it changes.
export function configPullRequestTitle(refusal, paths) {
  const what = refusal?.description || `Change ${paths.join(", ")}`;
  return what.charAt(0).toUpperCase() + what.slice(1);
}

// settingsSectionPath is the file of the config repository a settings
// section's cards are exported to: the same files a refused write from
// that section names (staterepo.DumpPath).
const SECTION_PATHS = {
  repositories: "tables/repo_config.json",
  memory: "memory/",
};
export function settingsSectionPath(section) {
  return SECTION_PATHS[section] || "tables/grain_config.json";
}

// EditInConfigRepo is the one line both surfaces lead with:
// "Edit <path> in <repo>", the path linked where it can be.
export function EditInConfigRepo({ refusal, paths }) {
  const repo = refusal.repo || refusal.remote;
  if (isInternalRefusal(refusal)) {
    return (
      <Box data-testid="config-refusal-where">
        <Typography variant="body2">
          {`This setting lives in ${repo} (${refusal.branch || "main"}) and is changed with a config pull request there; grain applies it once a person merges it.`}
        </Typography>
        <Box component="ul" sx={{ m: 0, mt: 0.5, pl: 2.5 }}>
          {paths.map((p) => (
            <Typography component="li" variant="body2" key={p}>
              <ConfigRepoLink path={p} label="Open in Config Browser" />
            </Typography>
          ))}
        </Box>
      </Box>
    );
  }
  return (
    <Typography variant="body2" data-testid="config-refusal-where">
      {"Edit "}
      {paths.map((p, i) => {
        const url = configFileURL(refusal, p);
        return (
          <span key={p}>
            {i > 0 && ", "}
            {url ? (
              <Link href={url} target="_blank" rel="noreferrer">
                <code>{p}</code>
              </Link>
            ) : (
              <code>{p}</code>
            )}
          </span>
        );
      })}
      {` in ${repo} (${refusal.branch || "main"}), then open a pull request there; grain applies it on its next sync.`}
    </Typography>
  );
}

// ConfigRefusalDialog shows a refused write's instruction: where, and
// the patch to commit, copyable.
// For a refusal of this deployment's own grain/config it also offers to
// open that pull request (POST /api/state-repo/pull-requests, held for a
// person's Submit) carrying the refusal's change as it is.
export default function ConfigRefusalDialog({ refusal, onClose }) {
  const [opening, setOpening] = useState(false);
  const [opened, setOpened] = useState(null);
  const [openError, setOpenError] = useState(null);
  // A new refusal is a new question: what the last one opened, or why
  // it could not, is not this one's answer.
  useEffect(() => {
    setOpened(null);
    setOpenError(null);
    setOpening(false);
  }, [refusal]);
  if (!refusal) return null;
  const changes = refusal.change || [];
  const patch = changes.map((c) => c.patch).join("");
  const paths = refusal.path?.length
    ? refusal.path
    : changes.map((c) => c.path);
  const canOpenPR = isInternalRefusal(refusal) && changes.length > 0;
  const openPullRequest = async () => {
    setOpening(true);
    setOpenError(null);
    try {
      const pr = await api("/api/state-repo/pull-requests", {
        method: "POST",
        body: JSON.stringify({
          title: configPullRequestTitle(refusal, paths),
          changes,
        }),
      });
      // The editor behind this dialog holds the edit the pull request
      // now carries: leaving it loses nothing, so it should not ask.
      settleEdits();
      setOpened(pr);
    } catch (err) {
      setOpenError(err);
    } finally {
      setOpening(false);
    }
  };
  const taskHref = opened?.taskId ? `/tasks/${opened.taskId}` : "";
  return (
    <Dialog
      open
      onClose={onClose}
      maxWidth="md"
      fullWidth
      data-testid="config-refusal-dialog"
    >
      <DialogTitle>Configuration is managed in {refusal.repo}</DialogTitle>
      <DialogContent>
        <EditInConfigRepo refusal={refusal} paths={paths} />
        {opened && (
          <Alert
            severity="success"
            sx={{ mt: 2 }}
            data-testid="config-refusal-opened"
          >
            {"Opened config pull request"}
            {opened.number ? ` #${opened.number}` : ""}
            {taskHref ? (
              <>
                {" as "}
                <Link
                  href={taskHref}
                  onClick={(evt) => {
                    openInApp(evt, taskHref);
                    if (evt.defaultPrevented) onClose?.();
                  }}
                >
                  task {opened.taskId}
                </Link>
              </>
            ) : null}
            . It waits for a person to submit it.
          </Alert>
        )}
        {openError && (
          <Alert
            severity="error"
            sx={{ mt: 2 }}
            data-testid="config-refusal-error"
          >
            <ConfigPullRequestError error={openError} branch={refusal.branch} />
          </Alert>
        )}
        {patch ? (
          <Box sx={{ position: "relative", mt: 2 }}>
            <Box sx={{ position: "absolute", top: 4, right: 4 }}>
              <CopyButton text={patch} label="Copy patch" />
            </Box>
            <Box
              component="pre"
              data-testid="config-refusal-patch"
              sx={{
                m: 0,
                p: 1.5,
                pr: 5,
                fontSize: "0.8rem",
                overflow: "auto",
                maxHeight: "50vh",
                bgcolor: "action.hover",
                borderRadius: 1,
              }}
            >
              {patch}
            </Box>
          </Box>
        ) : (
          refusal.description && (
            <Typography variant="body2" sx={{ mt: 2 }}>
              {refusal.description}.
            </Typography>
          )
        )}
      </DialogContent>
      <DialogActions>
        {refusal.docs && (
          <Button href={refusal.docs} target="_blank" rel="noreferrer">
            How this works
          </Button>
        )}
        {canOpenPR && !opened && openError?.code !== CONFIG_CHANGE_STALE && (
          <Button
            onClick={openPullRequest}
            disabled={opening}
            data-testid="config-refusal-open-pr"
          >
            {opening ? "Opening…" : "Create Config PR"}
          </Button>
        )}
        <Button onClick={onClose} variant="contained">
          Close
        </Button>
      </DialogActions>
    </Dialog>
  );
}
