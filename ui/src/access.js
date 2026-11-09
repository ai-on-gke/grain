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

// The caller's role, as GET /api/config reports it for a verified session
// (docs/design/access-rbac.md §6). config.access is absent for an
// unlimited caller -- the access code -- who may do everything. The
// server is the enforcement point: these only decide what to show.
//
// A scope's name is the one pkg/ui/route_scopes.go gives the route the
// control calls, so a control and the server agree on what it needs.

// reposOf is repo as a list: a single "owner/name", a list of them (a
// batch action over tasks in several repositories), or none.
function reposOf(repo) {
  if (Array.isArray(repo)) return repo.filter(Boolean);
  return repo ? [repo] : [];
}

// reaches reports whether limit -- a scope's repoLimits entry -- names
// repo. Repository names compare case-insensitively, as the server's do.
function reaches(limit, repo) {
  const want = String(repo).toLowerCase();
  return limit.some((r) => String(r).toLowerCase() === want);
}

// repoLimit is the repositories config's caller holds scope on, or null
// when the scope is not limited to some.
export function repoLimit(config, scope) {
  const limit = config?.access?.repoLimits?.[scope];
  return Array.isArray(limit) && limit.length > 0 ? limit : null;
}

// canAccess reports whether config's caller holds scope. With no repo it
// asks whether the scope is held on at least one repository; with one
// (or a list), whether it is held on that repository (on every one of
// them) -- a role limited to some repositories (access.repoLimits) only
// reaches those.
export function canAccess(config, scope, repo) {
  const access = config?.access;
  if (!access || access.full) return true;
  if (!Array.isArray(access.scopes) || !access.scopes.includes(scope)) {
    return false;
  }
  const repos = reposOf(repo);
  const limit = repoLimit(config, scope);
  if (!limit || repos.length === 0) return true;
  return repos.every((r) => reaches(limit, r));
}

// accessRoleLabel is the caller's role as a person reads it, or "" for an
// unlimited caller.
export function accessRoleLabel(config) {
  return config?.access?.role || "";
}

// roleNoun is "your role grain/viewer", or "your roles a, b" when several
// allowlist entries matched, and whether it was several.
function roleNoun(config) {
  const label = accessRoleLabel(config) || "none";
  const several = (config?.access?.roles || []).length > 1;
  return [`your ${several ? "roles" : "role"} ${label}`, several];
}

// accessDeniedReason is why config's caller may not use a control needing
// scope (on repo), naming the scope and the role, or "" when it may. It
// is the tooltip a hidden-in-plain-sight control wears.
export function accessDeniedReason(config, scope, repo) {
  if (canAccess(config, scope, repo)) return "";
  const [who, several] = roleNoun(config);
  const limit = repoLimit(config, scope);
  const held = (config?.access?.scopes || []).includes(scope);
  if (held && limit) {
    const missing = reposOf(repo).filter((r) => !reaches(limit, r));
    return `Needs ${scope} on ${missing.join(", ")}: ${who} ${several ? "hold" : "holds"} it only on ${limit.join(", ")}.`;
  }
  return `Needs ${scope}, which ${who} ${several ? "do" : "does"} not grant.`;
}
