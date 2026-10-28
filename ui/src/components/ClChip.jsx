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

import { Chip } from "@mui/material";

// ClChip is a changelist CL that is a task's change (model.LinkChangelist,
// docs/design/changelists.md §10): cl/<number>, linking to its
// http://cl/<number> go link, which resolves because the UI is reached
// over UberProxy. The task's first CL is its primary.
export function clUrl(cl) {
  const n = String(cl || "").replace(/^cl\//, "");
  return /^[0-9]+$/.test(n) ? `http://cl/${n}` : null;
}

export default function ClChip({ cl, primary = false }) {
  const href = clUrl(cl);
  if (!href) return null;
  return (
    <Chip
      className="cl-chip"
      data-testid="cl-chip"
      data-primary={primary ? "true" : undefined}
      component="a"
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      clickable
      size="small"
      variant={primary ? "filled" : "outlined"}
      color="primary"
      label={cl}
      title={primary ? `${cl}: this task's primary CL` : cl}
      onClick={(e) => e.stopPropagation()}
    />
  );
}
