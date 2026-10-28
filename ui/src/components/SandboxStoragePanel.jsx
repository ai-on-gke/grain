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

import { Typography } from "@mui/material";
import { formatBytes } from "./PackageCachePanel.jsx";

// What the sandboxes themselves are occupying, in the System pane's
// Storage section (pkg/ui/sandbox_health.go, grain/task-276).
//
// The largest consumer of that volume by far, and the one that had no
// figure anywhere: this deployment's own data directory went from 70 GB
// to 181 GB in an hour on 2026-09-17 with about a dozen sandboxes live,
// which is what the disk floor was built for (model.DiskFloor). Every
// other entry under this heading is a cache of megabytes or single
// gigabytes; an operator who clears all three and finds the bar has
// barely moved has been looking in the wrong place, and this is the line
// that says so.
//
// It is a sum of the same readings the sandbox table below draws, so the
// total and its rows cannot disagree, and it is what those guests have
// *written* rather than what the pool was promised: a granule sandbox's
// disk is an overlay that grows as it is written, which is exactly why
// the bound on it is a floor under the free space rather than a ceiling
// over the promises.
//
// No button, for a better reason than the artifacts' one: a sandbox is
// not a thing to clear, it is a run in flight. Every one of these goes
// when its run ends, and the deployment deletes orphaned ones itself
// while it is under its floor (grain/task-213). The action an operator
// has here is on the Tasks pane, and it is to stop something.

// sandboxStorageSummary is what the live sandboxes have written, and how
// many of them are writing it.
function sandboxStorageSummary(storage) {
  if (!storage) return "";
  const live = storage.sandboxes || 0;
  if (live === 0) {
    return "Nothing is in flight, so the sandboxes are occupying nothing -- an idle deployment's volume is whatever has not been cleared up.";
  }
  const written = formatBytes((storage.usedMB || 0) * 1024 * 1024);
  const parts = [
    `${written} written by ${live} live ${live === 1 ? "sandbox" : "sandboxes"}`,
  ];
  if (storage.declaredGB) {
    parts.push(
      `of ${formatBytes(storage.declaredGB * 1024 * 1024 * 1024)} promised to them`,
    );
  }
  return `${parts.join(" ")}.`;
}

// sandboxStorageNote is what that figure does and does not cover: the
// guests that gave no reading, and the fact that the promise is not a
// reservation.
function sandboxStorageNote(storage) {
  if (!storage) return "";
  const live = storage.sandboxes || 0;
  if (live === 0) return "";
  const parts = [
    "A sandbox's disk is an overlay that grows as it is written, so what it was promised is not taken out of the volume until it is used -- which is why what holds work back here is the free-space floor on the bars above, not a ceiling on this figure",
  ];
  const measured = storage.measured || 0;
  if (measured < live) {
    parts.push(
      `${live - measured} of them gave no disk reading and count for nothing here -- a guest still starting, or one that has stopped answering`,
    );
  }
  return `${parts.join(". ")}.`;
}

// SandboxStoragePanel is those two lines, and nothing at all on a
// deployment with no sandbox backend wired -- which is the same thing an
// empty sandbox table means and is left to say it.
export default function SandboxStoragePanel({ storage }) {
  if (!storage) return null;
  const note = sandboxStorageNote(storage);
  return (
    <>
      <Typography variant="body2" color="text.secondary">
        {sandboxStorageSummary(storage)}
      </Typography>
      {note && (
        <Typography variant="caption" color="text.secondary" component="div">
          {note}
        </Typography>
      )}
    </>
  );
}
