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
  Box,
  Checkbox,
  FormControlLabel,
  Link,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import api from "../../api.js";
import { buildPath } from "../../paths.js";
import { repoApiRoot } from "../../repoApi.js";
import { knownRepos } from "../../state.js";
import {
  CI_MODE_LABELS,
  confirmBaseRepairBypass,
  repoCIMode,
} from "../RepoPage.jsx";
import { repoHostOf } from "../RepoList.jsx";
import SettingsCard from "./SettingsCard.jsx";

// RepositoriesSection is Settings → Repositories: an index only
// (docs/design/settings-panel.md §2.3). Each repository's settings stay
// on its own page, beside the status they govern; this answers "where
// do I configure a repo" with one row per configured repository: its
// base branch, whether its declarations are read, its CI mode and the
// Git connection that reaches it, each a link to the tab of its page
// (or the Settings section) that changes it.
//
// GET /api/config names the repositories but not their settings, so
// each row reads what the repo page itself reads for the same facts --
// GET /api/repos/{owner}/{name}/declarations (declarations and CI mode)
// and .../release (the base branches grain watches) -- and the Git
// column reads GET /api/deployment/reachable-repos once, as the Add
// repo picker does. No route of its own.
export default function RepositoriesSection({
  config,
  tasks,
  onOpenRepo,
  onSection,
}) {
  const repos = knownRepos(config, tasks);
  const [facts, setFacts] = useState({});
  const [reach, setReach] = useState(null);
  const repoList = repos.join("\n");

  useEffect(() => {
    let live = true;
    for (const repo of repoList ? repoList.split("\n") : []) {
      const root = repoApiRoot(repo);
      const note = (patch) => {
        if (!live) return;
        setFacts((all) => ({ ...all, [repo]: { ...all[repo], ...patch } }));
      };
      api(`${root}/declarations`)
        .then((defaults) => note({ defaults }))
        .catch(() => note({ defaults: false }));
      api(`${root}/release`)
        .then((release) => note({ release }))
        .catch(() => note({ release: false }));
    }
    return () => {
      live = false;
    };
  }, [repoList]);

  useEffect(() => {
    let live = true;
    api("/api/deployment/reachable-repos")
      .then((r) => live && setReach(r))
      .catch(() => live && setReach(false));
    return () => {
      live = false;
    };
  }, []);

  // A link to one tab of a repo's page, opened in place.
  const tabLink = (repo, tab, text) => (
    <Link
      href={buildPath({ view: "repos", repo, repoTab: tab })}
      onClick={(evt) => {
        if (!onOpenRepo || !plainLeftClick(evt)) return;
        evt.preventDefault();
        onOpenRepo(repo, tab === "overview" ? null : tab);
      }}
    >
      {text}
    </Link>
  );

  const toggleBaseRepairBypass = async (repo, on) => {
    if (on && !confirmBaseRepairBypass(repo)) return;
    const root = repoApiRoot(repo);
    try {
      const defaults = await api(`${root}/base-repair-bypasses-human-gates`, {
        method: "PUT",
        body: JSON.stringify({ baseRepairBypassesHumanGates: on }),
      });
      setFacts((all) => ({ ...all, [repo]: { ...all[repo], defaults } }));
    } catch {
      // Leave the previous state untouched if the write fails.
    }
  };

  return (
    <SettingsCard title="Repositories" anchor="repositories">
      {repos.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          No repository is configured yet. Add one from Repos in the sidebar.
        </Typography>
      ) : (
        <>
          <Table size="small" aria-label="Repositories">
            <TableHead>
              <TableRow>
                <TableCell>Repository</TableCell>
                <TableCell>Base branch</TableCell>
                <TableCell>Declarations</TableCell>
                <TableCell>CI mode</TableCell>
                <TableCell>Git connection</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {repos.map((repo) => {
                const { defaults, release } = facts[repo] || {};
                return (
                  <TableRow
                    key={repo}
                    data-testid="settings-repo-row"
                    data-repo={repo}
                  >
                    <TableCell>{tabLink(repo, "overview", repo)}</TableCell>
                    <TableCell data-testid="settings-repo-base">
                      {tabLink(repo, "releases", baseBranchText(release))}
                    </TableCell>
                    <TableCell data-testid="settings-repo-declarations">
                      {tabLink(repo, "advanced", declarationsText(defaults))}
                    </TableCell>
                    <TableCell data-testid="settings-repo-ci">
                      {tabLink(repo, "ci", ciModeText(defaults))}
                    </TableCell>
                    <TableCell data-testid="settings-repo-git">
                      <Link
                        href={buildPath({
                          view: "settings",
                          settingsSection: "git",
                        })}
                        onClick={(evt) => {
                          if (!onSection || !plainLeftClick(evt)) return;
                          evt.preventDefault();
                          onSection("git");
                        }}
                      >
                        {connectionText(reach, repo)}
                      </Link>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
          <Box sx={{ mt: 2, pt: 1.5, borderTop: 1, borderColor: "divider" }}>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              Base-branch repair human-gate bypass
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
              Base-branch repair PRs may merge without a human when their CI is
              green. Risk: while a base branch is red, an agent repairing it can
              merge changes to workflow declarations (
              <code>.grain/workflows/**</code>) or trust-boundary code without
              human pre-merge sign-off. Security and trigger review tasks are
              still filed, the bypass is recorded on the task and pull request,
              and any review findings file a follow-up task. Once the base
              branch is green again, the exemption stops applying.
            </Typography>
            <Stack spacing={0.5}>
              {repos.map((repo) => {
                const { defaults } = facts[repo] || {};
                const checked = Boolean(defaults?.baseRepairBypassesHumanGates);
                return (
                  <FormControlLabel
                    key={repo}
                    sx={{ alignItems: "center", m: 0 }}
                    control={
                      <Checkbox
                        size="small"
                        checked={checked}
                        disabled={!defaults}
                        onChange={(e) =>
                          toggleBaseRepairBypass(repo, e.target.checked)
                        }
                        inputProps={{
                          "aria-label": `${repo}: Base-branch repair PRs may merge without a human when their CI is green`,
                        }}
                      />
                    }
                    label={
                      <Typography variant="body2">
                        <strong>{repo}</strong> — Base-branch repair PRs may
                        merge without a human when their CI is green (
                        {tabLink(repo, "merge-queue", "Merge queue")})
                      </Typography>
                    }
                  />
                );
              })}
            </Stack>
          </Box>
        </>
      )}
    </SettingsCard>
  );
}

function plainLeftClick(evt) {
  return (
    evt.button === 0 &&
    !evt.metaKey &&
    !evt.ctrlKey &&
    !evt.shiftKey &&
    !evt.altKey
  );
}

// Each column's text: "…" while its read is out, "Unknown" if it
// failed, and otherwise the fact.

function baseBranchText(release) {
  if (release === undefined) return "…";
  if (!release) return "Unknown";
  const branches = release.baseBranches || [];
  // No row yet: grain watches the default branch from its first merge.
  return branches.length > 0 ? branches.join(", ") : "Default branch";
}

function declarationsText(defaults) {
  if (defaults === undefined) return "…";
  if (!defaults) return "Unknown";
  return defaults.declarations ? "Read" : "Not read";
}

function ciModeText(defaults) {
  if (defaults === undefined) return "…";
  if (!defaults) return "Unknown";
  return CI_MODE_LABELS[repoCIMode(defaults)] || repoCIMode(defaults);
}

// connectionText is which of the deployment's Git connections reaches
// the repo, by what the connections list: the connections on its host,
// and whether the listing gave push. The listing names repositories,
// not the connection each came from, so two connections on one host are
// both named.
export function connectionText(reach, repo) {
  if (reach === null) return "…";
  if (!reach) return "Unknown";
  if (!reach.available) return "Not listed";
  const host = repoHostOf(repo) || "github.com";
  const listed = (reach.repos || []).find(
    (r) => (r.repo || "").toLowerCase() === repo.toLowerCase(),
  );
  if (!listed) return "Not reached";
  const names = (reach.connections || [])
    .filter((c) => c.ok && (c.host || "github.com").toLowerCase() === host)
    .map((c) => c.credential)
    .filter(Boolean);
  const via = names.length > 0 ? names.join(", ") : host;
  return listed.push ? via : `${via} (read only)`;
}
