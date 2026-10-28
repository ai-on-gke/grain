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

// hasGitCredential is whether the deployment (config.deployment) holds
// a git credential for the default host -- what "has no GitHub
// credential" warnings read (grain/task-3079). Adding or
// deleting a git connection moves the legacy gitCredential into
// gitConnections and blanks it, so that field alone says nothing.
//
// The server answers it as hasGitCredential
// (model.Team.HasGitConnectionOn: any connection on the default host, or
// the legacy credential, never "anonymous", which pushes nothing). A row
// from a server that predates that field falls back to the same rule
// with github.com as the default host.
export const DEFAULT_GIT_HOST = "github.com";

export function hasGitCredential(deployment) {
  if (!deployment) return false;
  if (typeof deployment.hasGitCredential === "boolean")
    return deployment.hasGitCredential;
  const legacy = (deployment.gitCredential || "").trim();
  if (legacy && legacy !== "anonymous") return true;
  return (deployment.gitConnections || []).some((c) => {
    const cred = (c?.credential || "").trim();
    if (!cred || cred === "anonymous") return false;
    const host = (c.host || "").trim().toLowerCase();
    return host === "" || host === DEFAULT_GIT_HOST;
  });
}

// hasAnyGitConnection is whether deployment has a git connection on any
// host at all -- what the "add a git connection first" notices in the New
// task dialog and on the Repositories page read. hasGitCredential asks
// about the default host alone, and a deployment whose one connection is
// on another host (a Cloudtop's depot, say) clones and pushes there.
// "anonymous" pushes nothing, so it is not counted.
export function hasAnyGitConnection(deployment) {
  if (!deployment) return false;
  if (hasGitCredential(deployment)) return true;
  return (deployment.gitConnections || []).some((c) => {
    const cred = (c?.credential || "").trim();
    return cred !== "" && cred !== "anonymous";
  });
}

// splitRepo is a repository as deploymentRepos lists it -- "owner/name",
// or "host/owner/name" off the default host -- in parts, lowercased the
// way connection scopes are stored.
export function splitRepo(repo) {
  const parts = (repo || "").trim().toLowerCase().split("/");
  if (parts.length >= 3) {
    return { host: parts[0], owner: parts[1], name: parts.slice(2).join("/") };
  }
  return {
    host: DEFAULT_GIT_HOST,
    owner: parts[0] || "",
    name: parts[1] || "",
  };
}

function connHost(conn) {
  return ((conn?.host || "").trim().toLowerCase() || DEFAULT_GIT_HOST).replace(
    /\/+$/,
    "",
  );
}

function connScope(conn) {
  return (conn?.scope || "")
    .trim()
    .toLowerCase()
    .replace(/^\/+|\/+$/g, "")
    .replace(/\/\*$/, "");
}

// resolveRepoConnection is which of connections (GET
// /api/deployment/git-connections, in their stored order) repo resolves to
// -- the same rule as model.MatchTeamGitConnection: on the repo's host
// only, a connection scoped to the repository, then to its owner, then
// the host's default, the earlier entry winning a tie. The answer is
// { connection, match: "repo" | "owner" | "default" }, or null when no
// connection reaches the repository and its tasks are held.
export function resolveRepoConnection(connections, repo) {
  const { host, owner, name } = splitRepo(repo);
  const rank = { default: 1, owner: 2, repo: 3 };
  let best = null;
  for (const conn of connections || []) {
    if (!(conn?.credential || "").trim() || connHost(conn) !== host) continue;
    const scope = connScope(conn);
    let match = null;
    if (scope === "") match = "default";
    else if (owner && name && scope === `${owner}/${name}`) match = "repo";
    else if (owner && scope === owner) match = "owner";
    if (match && (!best || rank[match] > rank[best.match])) {
      best = { connection: conn, match };
    }
  }
  return best;
}

// repoPinnedConnection is the connection scoped to exactly repo -- what
// the Repositories picker set, or null when the repo is on "Default".
export function repoPinnedConnection(connections, repo) {
  const { host, owner, name } = splitRepo(repo);
  return (
    (connections || []).find(
      (c) => connHost(c) === host && connScope(c) === `${owner}/${name}`,
    ) || null
  );
}

// distinctConnections is connections with each credential once, as its
// broadest entry: picking a credential for a repository records it again,
// scoped to that repository, and that entry is the repository's choice
// rather than another connection to list (or to remove, which would take
// the credential away from every scope it holds).
export function distinctConnections(connections) {
  const order = (c) => {
    const scope = connScope(c);
    if (!scope) return 0;
    return scope.includes("/") ? 2 : 1;
  };
  const byCred = new Map();
  for (const conn of connections || []) {
    const have = byCred.get(conn.credential);
    if (!have || order(conn) < order(have)) byCred.set(conn.credential, conn);
  }
  return [...byCred.values()];
}
