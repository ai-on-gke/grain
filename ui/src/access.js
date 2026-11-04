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

// canAccess reports whether config's caller holds scope on at least one
// repository.
export function canAccess(config, scope) {
  const access = config?.access;
  if (!access || access.full) return true;
  return Array.isArray(access.scopes) && access.scopes.includes(scope);
}

// accessRoleLabel is the caller's role as a person reads it, or "" for an
// unlimited caller.
export function accessRoleLabel(config) {
  return config?.access?.role || "";
}
