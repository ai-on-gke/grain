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

// GET /api/config's `version` is the build stamp object (pkg/version's
// Info: release, edition, commit, ...); older servers sent a string.
export interface VersionInfo {
  release?: string;
  edition?: string;
  commit?: string;
  committedAt?: string;
  modified?: boolean;
  unversioned?: boolean;
}

// versionLabel is the short line the footer shows, built the way the
// classic sidebar's buildStampLabel does: the release, then the edition,
// or a short commit when there is no release.
export function versionLabel(v: unknown): string {
  if (typeof v === "string") return v;
  if (!v || typeof v !== "object") return "";
  const info = v as VersionInfo;
  const parts: string[] = [];
  if (info.release) parts.push(info.release);
  if (info.edition) parts.push(info.edition);
  if (parts.length === 0 && info.commit) parts.push(info.commit.slice(0, 7));
  return parts.join(" · ");
}
