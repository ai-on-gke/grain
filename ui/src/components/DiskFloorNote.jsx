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

import { formatSpace } from "./SandboxHealthPage.jsx";

// DiskFloorNote is what the two disk-floor fields above it come to on
// this deployment's own volumes -- ui.Settings.sandboxDiskFloors, one
// entry per filesystem, with the figures resolved by the server
// (grain/task-372).
//
// It is here for the same reason SandboxCommitmentNote beside it is: the
// setting and the thing it decides are not the same number. "A tenth of
// each volume" is not a quantity of disk until it meets a disk, and on a
// deployment sized the way terraform/gcp is the same setting means 44 GB
// held back on the 443 GB sandbox volume and the 5 GiB minimum on the 20
// GB store disk next to it -- while a share large enough to wedge the
// deployment is quietly capped at a quarter of the volume instead. An
// operator lowering this figure is usually doing it because their
// deployment has already gone quiet, and they should be able to see what
// they have just chosen without going to the host pane to find out.
//
// The figures are the server's own arithmetic, never recomputed here:
// the floor has a minimum and a cap in it (model.DiskFloor.FloorMB), and
// a second implementation of that in JavaScript would be a second answer
// to "why is this deployment holding work".
export default function DiskFloorNote({ settings, sx }) {
  const floors = settings?.sandboxDiskFloors;
  // Nothing to say where the server resolved nothing: a UI not colocated
  // with this deployment's host, or one whose volumes were never
  // resolved. The fields above still show what is stored.
  if (!floors || floors.length === 0) return null;

  const off =
    !settings.sandboxDiskFloorShare && !settings.sandboxDiskFloorMinMb;
  if (off) {
    return (
      <Typography variant="body2" color="text.secondary" sx={sx}>
        No disk floor: nothing holds work back as{" "}
        {floors.map((d) => d.path).join(", ")} fill, and a volume with no room
        left takes this deployment's own database down with it.
      </Typography>
    );
  }

  return (
    <Typography variant="body2" color="text.secondary" sx={sx}>
      Kept free on this deployment's own volumes:{" "}
      {floors
        .filter((d) => d.totalMB > 0)
        .map((d) => (
          <span key={d.path}>
            {d.path} ({formatSpace(d.totalMB)}) holds {formatSpace(d.floorMB)}{" "}
            free, sweeping back to {formatSpace(d.reclaimMB)}.{" "}
          </span>
        ))}
      Never more than{" "}
      {Math.round((settings.sandboxDiskFloorMaxShare || 0) * 100)}% of a volume
      is kept free, whatever is set here.
    </Typography>
  );
}
