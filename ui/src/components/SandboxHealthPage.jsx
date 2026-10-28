import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  ButtonBase,
  Chip,
  CircularProgress,
  Link,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import api from "../api.js";
import { buildPath } from "../paths.js";
import CIArtifactsPanel from "./CIArtifactsPanel.jsx";
import CIHistoryPanel from "./CIHistoryPanel.jsx";
import CIJobCachePanel from "./CIJobCachePanel.jsx";
import { formatDuration } from "./CIPage.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import PersistentSandboxesPanel from "./PersistentSandboxesPanel.jsx";
import { ListHeader } from "./ListPrimitives.jsx";
import { formatBytes } from "./PackageCachePanel.jsx";
import SandboxLifetimePanel from "./SandboxLifetimePanel.jsx";
import SandboxStoragePanel from "./SandboxStoragePanel.jsx";
import Sparkline from "./Sparkline.jsx";

// REFRESH_MS matches LogsPage's own polling cadence -- the System overlay
// (bwsalmon/agents#640) these two panels share should feel like the same
// kind of live view. It is the gap between one poll's answer and the next
// poll, rather than the period of a timer: see the effect that schedules
// it.
const REFRESH_MS = 5000;

// LIVE_COLUMNS is how many columns the live sandbox table has, which
// the CI group's summary row spans.
const LIVE_COLUMNS = 16;

// HISTORY_LENGTH caps how many polls' worth of samples each trend chart
// keeps (bwsalmon/agents#566), at 5 minutes of history at REFRESH_MS's own
// cadence -- long enough to see a trend developing, short enough that a
// pane left open overnight does not grow its history without bound.
const HISTORY_LENGTH = 60;

const emptySeries = { cpu: [], mem: [], disk: [] };

// emptyHostSeries is emptySeries' own shape for the host, whose disk
// history is one series per filesystem rather than one series -- the host
// section reports two or three of them now (grain/task-148: the store's
// disk, the sandbox volume, docker's data root), where a sandbox still
// has the one root filesystem.
const emptyHostSeries = { cpu: [], mem: [], disks: {} };

// packageCacheSummary is the one clause the Storage section still says
// about the cache itself: how much of these bars it is holding, in the
// same units the bars above are read in. The figures per ecosystem, and
// the flush buttons, are the Proxy cache page's (grain/task-321).
//
// "" when this deployment runs no cache -- the sentence it hangs off
// reads as a description of the cache in general either way, and an
// operator with no cache has nothing here to act on.
function packageCacheSummary(cache) {
  if (!cache || cache.length === 0) return "";
  const bytes = cache.reduce((sum, stats) => sum + (stats.bytes || 0), 0);
  return `, and holding ${formatBytes(bytes)} of them right now`;
}

function formatMemory(usedMB, totalMB) {
  if (!totalMB) return "—";
  return `${usedMB} / ${totalMB} MB`;
}

function formatSwap(usedMB, totalMB) {
  if (!totalMB) return "disabled";
  return `${usedMB} / ${totalMB} MB`;
}

// formatDisk is formatMemory for a figure that arrives in MB but is
// normally counted in GB: a sandbox disk is tens of gigabytes where its
// memory is a couple, and "3174 / 20480 MB" is a worse answer to "how
// full is it" than "3.1 / 20.0 GB". Anything under a gigabyte stays in
// MB, where the same argument runs the other way.
function formatShape(sandbox) {
  if (!sandbox.cpus && !sandbox.memoryMB && !sandbox.diskGB) return "—";
  return [
    sandbox.cpus ? `${sandbox.cpus} vCPU` : null,
    sandbox.memoryMB ? `${sandbox.memoryMB} MB RAM` : null,
    sandbox.diskGB ? `${sandbox.diskGB} GB disk` : null,
  ]
    .filter(Boolean)
    .join(" · ");
}

function formatDisk(usedMB, totalMB) {
  if (!totalMB) return "—";
  if (totalMB < 1024) return `${usedMB} / ${totalMB} MB`;
  return `${(usedMB / 1024).toFixed(1)} / ${(totalMB / 1024).toFixed(1)} GB`;
}

// formatSpace is one figure in the same units the bar beside it is drawn
// in, for the floor and for the room left over it.
export function formatSpace(mb) {
  if (mb === null || mb === undefined || Number.isNaN(mb)) return "—";
  if (mb < 1024) return `${Math.round(mb)} MB`;
  return `${(mb / 1024).toFixed(1)} GB`;
}

// diskFloorLabel is what this deployment keeps free on one filesystem,
// and how much room is left above it (ui.DiskUsage.FloorMB,
// grain/task-214).
//
// It is the number that makes the bar beside it actionable. "181 / 443
// GB" says what is eating the volume; it says nothing about how much of
// the remainder a job can actually have, and the answer is not all of it
// -- a new CI job is held and an agent dispatch deferred while free space
// is under this figure, which is how a deployment with 44 GB free goes
// quiet and looks healthy doing it.
function diskFloorLabel(disk) {
  if (!disk || !disk.floorMB || !disk.totalMB) return "";
  const free = Math.max(disk.totalMB - disk.usedMB, 0);
  return `${formatSpace(disk.floorMB)} kept free, ${formatSpace(Math.max(free - disk.floorMB, 0))} spare`;
}

// DISK_HELD is what a deployment under its disk floor is doing, in the
// words its own journal uses for it (pkg/cli/daemon_disk_floor.go's
// report). Written once here so the row, its tooltip and the sentence
// under the table cannot drift from each other -- or from the line an
// operator may have arrived from the journal having already read.
const DISK_HELD =
  "new CI jobs are held and agent dispatch deferred until space is freed";

// diskFloorLevel is how loudly one row speaks about its floor, in the
// same vocabulary ui.SandboxCommitment reports its own dimensions in: ""
// for a volume with room over it, "caution" for one close enough to the
// floor to be worth seeing, and "warning" for one under it and holding
// work right now.
//
// Two levels rather than the one this had, because a row that speaks only
// once work is already held is a row an operator reads after the
// deployment has gone quiet -- and the whole point of putting the floor
// on the pane is to be read before that. The caution threshold is the
// daemon's own reclaim target (ui.DiskUsage.ReclaimMB) rather than a
// number chosen here: a volume inside it is one this deployment would
// still be sweeping if it had just crossed the floor, which is as good a
// definition of "close to the line" as it has.
function diskFloorLevel(disk) {
  if (!disk || !disk.floorMB || !disk.totalMB) return "";
  const free = Math.max(disk.totalMB - disk.usedMB, 0);
  if (free < disk.floorMB) return "warning";
  if (disk.reclaimMB && free < disk.reclaimMB) return "caution";
  return "";
}

// diskFloorTitle is what hovering one of those rows says: what the figure
// means at all, and -- on the volume that is holding work -- what is
// happening because of it.
function diskFloorTitle(level, disk) {
  if (level === "warning") {
    return `Free space on ${disk.path} is under this deployment's disk floor: ${DISK_HELD}, and it is reclaiming what it can.`;
  }
  if (level === "caution") {
    return `Closer to this volume's floor than a reclaim sweep aims to leave it: work is still being admitted, and ${DISK_HELD} if it fills much further.`;
  }
  return "Work is held back below this much free space, so it is not room any job can have.";
}

// diskFloorHeld is the sentence under the table on a deployment that is
// holding work back right now, naming the volumes doing it.
//
// Said in prose as well as on the row because this is the one thing on
// this pane that explains a deployment which has stopped starting things,
// and until now it was said only in the daemon's journal -- which is not
// where anybody looks when a deployment has merely gone quiet. Empty
// where nothing is held, which is the ordinary case and needs no sentence.
function diskFloorHeld(disks) {
  const held = (disks || []).filter((d) => diskFloorLevel(d) === "warning");
  if (held.length === 0) return "";
  return `Free space on ${held.map((d) => d.path).join(" and ")} is under this deployment's disk floor: ${DISK_HELD}, and it is reclaiming what it can (orphaned sandbox VMs, the CI job cache, kept artifacts).`;
}

// NESTED_VIRT is how each state orchestrator.SandboxHealth.NestedVirt
// reports for a sandbox is shown: a short label and the chip colour that
// goes with it, plus the sentence explaining what to do about it, which
// the cell carries as its title so the table stays a table.
//
// The labels deliberately avoid the word "ready", which the Status column
// beside this one already uses for something else -- a sandbox is
// "ready" when its guest answers at all, and that says nothing about
// whether a VM can be started inside it.
const NESTED_VIRT = {
  ready: {
    label: "yes",
    color: "success",
    title: "/dev/kvm is present and this sandbox's account can open it",
  },
  denied: {
    label: "no access",
    color: "warning",
    title:
      "/dev/kvm is there but the account tools run as cannot open it -- a guest image built before the guest build put that account in the kvm group, on which grain's own grant at acquire time (ensureGuestKVMAccess) also failed: its reason is in the daemon's log",
  },
  "no-device": {
    label: "no /dev/kvm",
    color: "warning",
    title:
      "the guest's CPU offers virtualization but nothing loaded its kvm_intel/kvm_amd module",
  },
  unsupported: {
    label: "no",
    color: "default",
    title:
      "no vmx/svm flag in the guest's CPU: the host below is not running KVM with nesting enabled, or this VM was configured without it",
  },
};

// HOST_NESTED_VIRT is the same for the host's own half of the question
// (sysstat.NestedVirtualization): whether the machine this daemon runs
// on will let the VMs it boots run VMs of their own.
const HOST_NESTED_VIRT = {
  enabled: "enabled",
  disabled: "disabled",
  unavailable: "unavailable",
};

// LOAD_LEVEL is how loud a load average that has passed the host's own
// core count is drawn, and what the figure's tooltip says it means.
//
// Two levels rather than one because the two ends of this are different
// news. A load equal to the core count is a machine with exactly as much
// runnable work as it has cores: fully committed, nothing waiting yet,
// and the ordinary state of a deployment running the number of sandboxes
// it was sized for -- worth seeing, not worth alarm. Twice that is a
// machine where the average runnable process is waiting about as long as
// it runs, which is what a starved host looks like from the inside of a
// sandbox that seems stuck. The same caution/warning vocabulary
// ui.SandboxCommitment already uses for the other half of this question,
// so an operator reading both panes reads one scale.
//
// It stays a colour on the numbers rather than a banner: this is a
// point-in-time reading and a load average crosses back over on its own,
// where the commitment banner is about settings that stay wrong until
// somebody changes them.
const LOAD_LEVEL = {
  caution: {
    color: "warning.main",
    title: "as much runnable work as this host has cores",
  },
  warning: {
    color: "error.main",
    title: "more than twice as much runnable work as this host has cores",
  },
};

// loadLevel places one of the host's load averages against the core count
// it is only meaningful over -- 8.0 is half an idle 32-core machine and a
// badly starved 4-core one, and until HostPressure.CPUs existed the pane
// showed the 8.0 alone (grain/task-286).
//
// null both for a figure with nothing to say about it and for a host that
// reported no core count at all (a non-Linux daemon, or one older than
// that field): the pane shows the bare averages then, exactly as it did
// before, rather than guessing at a denominator.
export function loadLevel(load, cpus) {
  if (!cpus || typeof load !== "number" || Number.isNaN(load)) return null;
  const perCore = load / cpus;
  if (perCore >= 2) return "warning";
  if (perCore >= 1) return "caution";
  return null;
}

// LoadFigure is one of those three averages: the number itself, coloured
// and given the per-core figure as its tooltip once it has passed the
// core count. The number is never replaced by a word -- what an operator
// deciding whether to reduce max_workers needs is the reading, and the
// colour is only what makes them look at it.
function LoadFigure({ load, cpus }) {
  const level = loadLevel(load, cpus);
  if (!level) return load.toFixed(2);
  return (
    <Box
      component="span"
      sx={{ color: LOAD_LEVEL[level].color, fontWeight: 600 }}
      title={`${(load / cpus).toFixed(2)} per core: ${LOAD_LEVEL[level].title}`}
    >
      {load.toFixed(2)}
    </Box>
  );
}

// pushSample appends a value to a capped history array, dropping the
// oldest sample once HISTORY_LENGTH is exceeded. null/undefined/NaN mean
// "no reading this poll" (a sandbox that errored, or host stats that
// failed) and are skipped rather than plotted as zero, which would read
// as a real dip in usage instead of a missing sample.
function pushSample(series, value) {
  if (value === null || value === undefined || Number.isNaN(value))
    return series;
  return [...series, value].slice(-HISTORY_LENGTH);
}

// appendDiskHistory folds one poll's host disk figures into the running
// per-filesystem history, keyed by the path each reading was taken
// through -- not by array position, so a chart follows the same
// filesystem across polls even as docker's data root appears in the list
// or drops out of it. A filesystem the host has stopped reporting drops
// its series with it, which is what keeps this from growing without
// bound.
function appendDiskHistory(prev, disks) {
  const next = {};
  for (const d of disks || []) {
    // 0/0 is how both ends spell "no reading available" for one disk (an
    // unreadable path, and see the disk's own `error`), and plotting the
    // 0 would read as an empty disk rather than as a missing sample.
    next[d.path] = pushSample(prev[d.path] || [], d.totalMB ? d.usedMB : null);
  }
  return next;
}

// appendHistory folds one GET /api/sandboxes response into the running
// per-host and per-sandbox history SandboxHealthPage charts from. Load
// average, not a CPU percentage, is what SandboxHealth/HostPressure
// actually report (see orchestrator.SandboxHealth's own doc comment on
// why), so the
// "CPU" trend charts below are the 1-minute load average over time, same
// as the existing text summary already shows.
//
// A stale response contributes no sandbox sample at all (see
// SandboxReadingLine): its rows are the ones the previous poll already
// plotted, and plotting them again would draw a load average holding
// perfectly steady -- a reading, and a reassuring one -- where what is
// really happening is that the probe has stopped coming back. The host
// half is appended regardless, because the host figures are read fresh
// on every request and are never served from the sweep's cache.
export function appendHistory(prev, result) {
  const host = result?.host
    ? {
        cpu: pushSample(
          prev.host.cpu,
          Number.isFinite(result.host.cpuUtilization)
            ? result.host.cpuUtilization * 100
            : null,
        ),
        mem: pushSample(prev.host.mem, result.host.memoryUsedMB),
        disks: appendDiskHistory(prev.host.disks, result.host.disks),
      }
    : prev.host;

  if (result?.sandboxReading?.stale || !Array.isArray(result?.sandboxes))
    return { host, sandboxes: prev.sandboxes };

  // Rebuilt from this poll's list rather than added to: a sandbox that is
  // no longer reported has finished, and its series goes with it, the way
  // appendDiskHistory drops a filesystem -- every run gets a sandbox of
  // its own name, so keeping them would grow without bound on a pane left
  // open.
  const sandboxes = {};
  for (const s of result.sandboxes) {
    const existing = prev.sandboxes[s.sandbox] || emptySeries;
    const load1 =
      s.ready && s.loadAverage ? parseFloat(s.loadAverage.split(" ")[0]) : null;
    sandboxes[s.sandbox] = {
      cpu: pushSample(existing.cpu, load1),
      mem: pushSample(existing.mem, s.ready ? s.memoryUsedMB : null),
      disk: pushSample(
        existing.disk,
        s.ready && s.diskTotalMB ? s.diskUsedMB : null,
      ),
    };
  }
  return { host, sandboxes };
}

// HostDisks is the host section's disk figures: one row per filesystem
// the daemon's own state sits on, rather than the single "Disk: x / y GB"
// this showed while that state was assumed to be one volume.
//
// It is a table rather than another entry in the text line above because
// there is now more than one number and each needs saying which disk it
// is: a deployment sized the way terraform/gcp is has a small store disk
// that stays near-empty beside a large sandbox volume that a runaway
// build fills, and it was the store's figure alone the pane used to show
// (grain/task-148). The "Holds" column is what of grain's own state
// lives there -- two words in one row when the daemon found them to be
// the same filesystem, which is the ordinary case on a single-disk host.
function HostDisks({ disks, history }) {
  if (!disks || disks.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary" sx={{ mb: "1rem" }}>
        No disk figures available.
      </Typography>
    );
  }
  // A table's intrinsic width is wider than a phone. Keep that width for
  // readable cells, but make the table -- not the entire page -- own the
  // horizontal scroll. The live-sandbox table below follows the same rule.
  const floored = disks.some((d) => d.floorMB > 0);
  const held = diskFloorHeld(disks);
  return (
    <TableContainer sx={{ mb: "1.5rem", maxWidth: "44rem" }}>
      {held && (
        // Above the table rather than under it: a deployment that is
        // holding every job back is the first thing to say about its
        // disks, not a footnote to them.
        <Typography variant="caption" color="error.main" component="div">
          {held}
        </Typography>
      )}
      {!floored && (
        // Said once under the table rather than as a dash per row: a
        // deployment that holds nothing back as its volume fills is a
        // fact about the deployment, and the failure that put a floor
        // here in the first place (grain/task-196) is what it risks.
        <Typography variant="caption" color="text.secondary" component="div">
          No disk floor applies to these, so nothing holds work back as they
          fill.
        </Typography>
      )}
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>Holds</TableCell>
            <TableCell>Path</TableCell>
            <TableCell>Usage</TableCell>
            {/* What the usage beside it has to be read against: a
                deployment does not get to spend the last tenth of a
                volume, and which of these filesystems is closest to that
                point is what decides whether work runs at all. */}
            <TableCell>Floor</TableCell>
            <TableCell>Trend</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {disks.map((d) => (
            <TableRow key={d.path}>
              <TableCell>{(d.holds || []).join(", ")}</TableCell>
              <TableCell sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}>
                {d.path}
              </TableCell>
              <TableCell>
                {/* A filesystem that has stopped answering says why rather
                    than showing a bare dash. This is the reading an
                    operator is here for, and "the sandbox volume is no
                    longer mounted" is the answer they want -- not a row
                    that has simply gone quiet. */}
                {d.error ? (
                  <Chip size="small" color="warning" label={d.error} />
                ) : (
                  formatDisk(d.usedMB, d.totalMB)
                )}
              </TableCell>
              <TableCell>
                {/* A volume already under its floor says so rather than
                    reporting a negative amount of room: this is the one
                    row on the pane that explains a deployment which has
                    gone quiet, and it is the reading the daemon's journal
                    used to hold alone. The volume that is merely close to
                    its floor is coloured rather than chipped -- it is
                    still admitting every job, which is the difference
                    between the two levels. */}
                {!d.floorMB ? (
                  "—"
                ) : diskFloorLevel(d) === "warning" ? (
                  <Chip
                    size="small"
                    color="error"
                    label={`under ${formatSpace(d.floorMB)}`}
                    title={diskFloorTitle("warning", d)}
                  />
                ) : (
                  <Typography
                    component="span"
                    variant="body2"
                    color={
                      diskFloorLevel(d) === "caution"
                        ? "warning.main"
                        : "inherit"
                    }
                    title={diskFloorTitle(diskFloorLevel(d), d)}
                  >
                    {diskFloorLabel(d)}
                  </Typography>
                )}
              </TableCell>
              <TableCell>
                <Sparkline
                  data={history[d.path] || []}
                  width={80}
                  height={24}
                  color="#2e7d32"
                  valueLabel={(value) => formatDisk(value, d.totalMB)}
                />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

// sandboxImageDrifted is whether this deployment's sandboxes are built
// from something other than the image this build of grain was stamped to
// go with -- an image pinned in the host's own systemd unit, which wins
// over the stamp.
//
// It is only ever true for a pinned image: left unpinned the daemon fills
// in its own stamp, so the two are the same reference by construction.
// A unit still carrying the install-time -granule-image an older setup.sh
// wrote is not pinned in this sense -- the daemon adopts its own stamp
// over that one -- so this pane says nothing about it either.
export function sandboxImageDrifted(image) {
  return Boolean(image?.pinned && image.image !== image.expected);
}

// formatReadingAge is how old a sandbox reading is, in the words the
// line below says it in. Whole seconds: this is only ever shown for a
// reading the server held back rather than answering with (at least
// sandboxHealthWait old, and never more than sandboxHealthMaxAge), and
// tenths of a second would be false precision about a number that is
// moving while it is read.
function formatReadingAge(ageMS) {
  if (!Number.isFinite(ageMS)) return null;
  return `${Math.max(1, Math.round(ageMS / 1000))}s`;
}

// SandboxReadingLine says that the rows below are not current, and how
// far from current they are.
//
// Nothing at all on the ordinary poll, which is the point: the server
// answers from the sweep the caller itself started whenever the guests
// answer in time, so a line here means something specific happened --
// one guest stopped answering and held the sweep inside orchestrator's
// 20-second probe budget, and rather than leave this pane blank for
// those 20 seconds the server handed over the last complete reading
// (grain/task-262).
//
// One line for the whole table rather than a marker per row: a sweep
// probes every guest in one call and answers for all of them at once, so
// every row here is exactly as old as every other, and a per-row marker
// would be the same marker on every row of the table. The row that is
// actually sick is not the stale one -- it is the one that will come
// back with an error chip once the sweep it is holding open finishes.
function SandboxReadingLine({ reading }) {
  if (!reading?.stale) return null;
  return (
    <Typography
      variant="caption"
      color="warning.main"
      component="div"
      sx={{ mb: "0.5rem" }}
    >
      As of {formatReadingAge(reading.ageMS)} ago: the probe running now has not
      come back yet, which usually means one of these guests has stopped
      answering. These figures, and the sandbox storage above, are from the last
      probe that finished.
    </Typography>
  );
}

// SLOW_PROBE_MS is how long a *successful* health probe has to have
// taken before this pane says so out loud.
//
// The probe is one command run inside the guest
// (orchestrator.guestStatsCommand), so a healthy sandbox on a host with
// room answers it in tens of milliseconds. A whole second means
// something -- the guest, or the host underneath it -- took a thousand
// times that to read two files, which is the early shape of the trouble
// this pane exists to show and is worth an operator's eye. Below it the
// number is noise: a column that says "41ms" on every row of every
// healthy deployment is a column nobody reads, and the reading is still
// one hover away on the status chip.
//
// A second is also well inside the budget a probe is given (20s of
// orchestrator.healthTimeout), which is deliberate: the point is to see
// a guest going slow long before it goes quiet.
const SLOW_PROBE_MS = 1000;

// formatProbeMS is one probe duration, in the units it is worth reading
// in -- milliseconds while it is a fast probe, tenths of a second once it
// is not. null when there is no duration to show, which includes the
// backend that runs no probe at all: a host sandbox's "probe" is that its
// directory exists (orchestrator.HostSandboxes), and reporting that as
// "0ms" would be this pane's fastest guest rather than its least
// interesting row.
export function formatProbeMS(ms) {
  if (!Number.isFinite(ms) || ms <= 0) return null;
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

// probeNote is what this row's own health probe has to say for itself:
// { text, tone, title }, or null when no probe stands behind the row.
//
// text is null when the duration is worth recording but not worth
// showing -- a healthy guest that answered promptly -- and the title goes
// on the status chip regardless, so the number is always one hover away.
//
// Why the duration belongs here at all: an error chip cannot tell a guest
// that is gone from a guest that is merely buried under host load, and
// those want different things from an operator (grain/task-390). A probe
// that failed in 40 ms was refused -- the guest, or its container, is no
// longer there. A probe that failed after the full 20-second budget was
// never answered, which on a host deep in swap is what an overloaded but
// perfectly alive guest looks like. In the one boolean this row used to
// carry, those are the same row.
export function probeNote(sandbox) {
  const duration = formatProbeMS(sandbox?.probeMS);
  if (!duration) return null;
  const failed = Boolean(sandbox.error);
  const measurement = failed
    ? `failed after ${duration}`
    : `answered in ${duration}`;
  // How far back the probe behind this row ran, for the row a sick
  // guest's breaker answered from memory rather than by probing
  // (orchestrator.healthBreakerCooldown). Nothing else says so: skipping
  // the probe is what makes that sweep quick, so the reading arrives
  // marked fresh (SandboxReadingLine stays silent) carrying an error and
  // a duration from up to fifteen seconds ago -- which is also how long
  // this row can go on looking red after the guest has come back.
  const age =
    Number.isFinite(sandbox.probeAgeMS) && sandbox.probeAgeMS > 0
      ? formatReadingAge(sandbox.probeAgeMS)
      : null;
  const reading = age ? `${measurement}, ${age} ago` : measurement;
  return {
    text: failed || sandbox.probeMS >= SLOW_PROBE_MS ? reading : null,
    tone: failed ? "error.main" : "warning.main",
    title: [
      `This sandbox's health probe ${measurement}.`,
      failed
        ? "A probe that fails at once was refused, which is a guest that is gone; one that fails after the whole budget was never answered, which is usually a guest buried under host load."
        : sandbox.probeMS >= SLOW_PROBE_MS
          ? "The guest is up, but one command inside it took seconds -- look at the host's load before this becomes an error chip."
          : "",
      age
        ? `Measured ${age} before this reading: grain waits before spending another probe budget on a guest that has just failed, so this row repeats that probe rather than one taken now, and a guest that has recovered can stay red here for a few seconds.`
        : "",
    ]
      .filter(Boolean)
      .join(" "),
  };
}

// SandboxStatus is one row's status cell: whether the guest answered,
// and -- where it is the finding rather than a constant -- how long it
// took to say so.
//
// One cell rather than a column of its own, because a column has to
// print something for every row: every healthy row would carry the same
// unremarkable number, and the rows with no probe behind them at all
// (host sandboxes) would carry a dash that means "not applicable" in a
// column where the neighbouring dashes mean "unavailable". Here the
// quiet cases stay quiet.
function SandboxStatus({ sandbox }) {
  const probe = probeNote(sandbox);
  return (
    <>
      <Chip
        size="small"
        color={sandbox.error ? "error" : "success"}
        label={sandbox.error || "ready"}
        title={probe?.title}
      />
      {probe?.text && (
        <Typography
          variant="caption"
          color={probe.tone}
          component="div"
          title={probe.title}
          sx={{ mt: "0.15rem", whiteSpace: "nowrap" }}
        >
          {probe.text}
        </Typography>
      )}
    </>
  );
}

function formatGiB(mb) {
  return `${((mb || 0) / 1024).toFixed(1)} GiB`;
}

function poolLabel(pool) {
  switch (pool) {
    case "ci":
      return "CI";
    case "agent":
      return "Agent";
    case "persistent":
      return "Persistent";
    case "standing":
      return "Standing";
    default:
      return pool;
  }
}

// CI_SANDBOXES_STORAGE_KEY is where this browser remembers whether the
// live table's CI rows were left open -- the same arrangement as the CI
// page's active sandboxes (ACTIVE_SANDBOXES_STORAGE_KEY). Collapsed is
// the default and is stored as no key at all.
export const CI_SANDBOXES_STORAGE_KEY = "grain.system.ciSandboxes.expanded";

function readCISandboxesExpanded() {
  try {
    return localStorage.getItem(CI_SANDBOXES_STORAGE_KEY) === "true";
  } catch {
    return false;
  }
}

function writeCISandboxesExpanded(expanded) {
  try {
    if (expanded) localStorage.setItem(CI_SANDBOXES_STORAGE_KEY, "true");
    else localStorage.removeItem(CI_SANDBOXES_STORAGE_KEY);
  } catch {
    // A browser that will not store it just forgets the choice.
  }
}

// ciSandboxesSummary is the collapsed CI group's one line: how many,
// what they hold between them, and the longest-running of them where
// the rows say when they started. The shape is summed from the rows
// themselves so it describes exactly the machines folded away; a
// backend that reports no per-sandbox shape (host) falls back to the
// budget's own CI pool, the figure the budget panel's "Pools:" line
// prints.
export function ciSandboxesSummary(rows, pools, now = Date.now()) {
  const parts = [
    `${rows.length} CI ${rows.length === 1 ? "sandbox" : "sandboxes"}`,
  ];
  let cpus = rows.reduce((n, s) => n + (s.cpus || 0), 0);
  let memoryMB = rows.reduce((n, s) => n + (s.memoryMB || 0), 0);
  if (!cpus && !memoryMB) {
    const pool = (pools || []).find((p) => p.pool === "ci");
    cpus = pool?.cpus || 0;
    memoryMB = pool?.memoryMB || 0;
  }
  if (cpus || memoryMB) parts.push(`${cpus} vCPU / ${formatGiB(memoryMB)}`);
  let longest = null;
  for (const s of rows) {
    const at = s.startedAt ? Date.parse(s.startedAt) : NaN;
    if (Number.isNaN(at)) continue;
    if (!longest || at < longest.at) longest = { at, sandbox: s.sandbox };
  }
  if (longest) {
    parts.push(
      `longest ${formatDuration(Math.max(now - longest.at, 0))} (${longest.sandbox})`,
    );
  }
  return parts.join(" · ");
}

// runningFor is the live table's "Running for" cell: how long ago the
// backend acquired the sandbox, or a dash when it did not say.
export function runningFor(startedAt, now = Date.now()) {
  const at = startedAt ? Date.parse(startedAt) : NaN;
  if (Number.isNaN(at)) return "—";
  return formatDuration(Math.max(now - at, 0));
}

export function SandboxOvercommitStatus({ budget, overcommit, sx }) {
  const oc = overcommit || budget?.overcommit || null;
  const hasFactors =
    oc?.cpuOvercommit != null ||
    oc?.memoryOvercommit != null ||
    budget?.cpuOvercommit != null ||
    budget?.memoryOvercommit != null;
  if (!oc && !budget?.mode && !hasFactors) return null;

  const mode = oc?.mode || budget?.mode || "fixed";
  const cpuRatio = oc?.cpuOvercommit ?? budget?.cpuOvercommit;
  const memRatio = oc?.memoryOvercommit ?? budget?.memoryOvercommit;
  const oomKills = oc?.oomKills ?? 0;
  const adjustments = oc?.adjustments || [];
  const bounds = oc?.settings;

  return (
    <Box sx={{ mb: "0.25rem", ...sx }} data-testid="sandbox-overcommit-status">
      <Box
        sx={{
          display: "flex",
          alignItems: "center",
          flexWrap: "wrap",
          gap: "0.5rem",
          mb: adjustments.length > 0 ? "0.25rem" : 0,
        }}
      >
        <Chip
          size="small"
          color={mode === "auto" ? "info" : "default"}
          label={`Overcommit: ${mode}`}
        />
        {oomKills > 0 && (
          <Chip
            size="small"
            color="error"
            label={`${oomKills} OOM ${oomKills === 1 ? "kill" : "kills"}`}
          />
        )}
        <Typography variant="caption" color="text.secondary" component="span">
          Effective factors:{" "}
          {cpuRatio != null ? `CPU ${Number(cpuRatio).toFixed(2)}×` : "CPU —"}
          {" · "}
          {memRatio != null
            ? `memory ${Number(memRatio).toFixed(2)}×`
            : "memory —"}
          {mode === "auto" && bounds && (
            <>
              {" "}
              (bounds CPU {Number(bounds.cpuMin ?? 1).toFixed(2)}×..
              {Number(bounds.cpuMax ?? 6).toFixed(2)}×, memory{" "}
              {Number(bounds.memoryMin ?? 1).toFixed(2)}×..
              {Number(bounds.memoryMax ?? 2).toFixed(2)}× · targets CPU{" "}
              {Number(bounds.cpuPressureTarget ?? 25).toFixed(1)}% / memory{" "}
              {Number(bounds.memoryPressureTarget ?? 5).toFixed(1)}%)
            </>
          )}
          {oc?.last && (
            <>
              {" · "}PSI: CPU{" "}
              {Number(oc.last.cpuSome ?? oc.last.cpuPressure ?? 0).toFixed(1)}%,
              memory{" "}
              {Number(
                oc.last.memorySome ?? oc.last.memoryPressure ?? 0,
              ).toFixed(1)}
              %
              {oc.last.swapInMBps > 0 &&
                `, swap-in ${Number(oc.last.swapInMBps).toFixed(1)} MiB/s`}
            </>
          )}
          {" · "}OOM kills: {oomKills}
          {oomKills > 0 && oc?.lastOomAt
            ? ` (last ${new Date(oc.lastOomAt).toLocaleString()})`
            : ""}
        </Typography>
      </Box>
      {adjustments.length > 0 && (
        <Box
          sx={{ pl: "0.5rem", borderLeft: "2px solid", borderColor: "divider" }}
          data-testid="overcommit-adjustments"
        >
          <Typography
            variant="caption"
            color="text.secondary"
            component="div"
            sx={{ fontWeight: 600 }}
          >
            Recent overcommit adjustments:
          </Typography>
          {adjustments.map((adj, idx) => (
            <Typography
              key={`${adj.at || idx}-${adj.dimension}-${idx}`}
              variant="caption"
              color="text.secondary"
              component="div"
              sx={{ fontFamily: "monospace", fontSize: "0.75rem" }}
            >
              {adj.dimension === "cpu" ? "CPU" : adj.dimension || "ratio"}{" "}
              {Number(adj.from ?? 0).toFixed(2)}× &rarr;{" "}
              {Number(adj.to ?? 0).toFixed(2)}×
              {adj.reason ? ` — ${adj.reason}` : ""}
              {adj.at ? ` (${new Date(adj.at).toLocaleTimeString()})` : ""}
            </Typography>
          ))}
        </Box>
      )}
    </Box>
  );
}

// SandboxClusterFree is a Kubernetes deployment's own reading of its
// nodes' free capacity, beside the budget above it: the budget is
// arithmetic on allocatable times overcommit, while this is what the
// scheduler would still place, and each sandbox pod is admitted against
// it (grain/task-805). The roomiest node is named because a pod cannot
// be split across nodes -- the cluster-wide free total can be large
// while no single node has room for one more sandbox.
export function SandboxClusterFree({ cluster }) {
  if (!cluster) return null;
  if (!cluster.known) {
    return (
      <Typography
        variant="caption"
        color="text.secondary"
        component="div"
        sx={{ mb: "0.25rem" }}
        data-testid="sandbox-cluster-free"
      >
        Cluster free capacity:{" "}
        {cluster.error
          ? `could not be read (${cluster.error})`
          : "not read yet"}
      </Typography>
    );
  }
  const cpus = (n) => `${Math.round((n || 0) * 10) / 10} vCPU`;
  return (
    <Typography
      variant="caption"
      color="text.secondary"
      component="div"
      sx={{ mb: "0.25rem" }}
      data-testid="sandbox-cluster-free"
      title={cluster.summary || undefined}
    >
      Cluster free: {cpus(cluster.freeCPUs)} / {formatGiB(cluster.freeMemoryMB)}{" "}
      of {cpus(cluster.allocatableCPUs)} /{" "}
      {formatGiB(cluster.allocatableMemoryMB)} allocatable on {cluster.nodes}{" "}
      {cluster.nodes === 1 ? "node" : "nodes"}
      {cluster.largestFreeNode && (
        <>
          {" "}
          &middot; roomiest node {cluster.largestFreeNode}:{" "}
          {cpus(cluster.largestFreeCPUs)} /{" "}
          {formatGiB(cluster.largestFreeMemoryMB)}
        </>
      )}
      {cluster.pendingPods > 0 && (
        <> &middot; {cluster.pendingPods} new pod(s) not yet in this reading</>
      )}
      {cluster.partial && <> &middot; other namespaces' pods not counted</>}
    </Typography>
  );
}

export function SandboxBudgetPanel({ budget, overcommit, reconciliation }) {
  if (!budget && !overcommit && !reconciliation) return null;
  const count = budget?.committedCount || 0;
  const countWord = count === 1 ? "sandbox" : "sandboxes";
  return (
    <Box sx={{ mb: "0.75rem" }} data-testid="sandbox-budget">
      {budget && (
        <>
          <Box
            sx={{
              display: "flex",
              alignItems: "center",
              flexWrap: "wrap",
              gap: "0.5rem",
              mb: "0.25rem",
            }}
          >
            <Chip
              size="small"
              color={
                budget.overCapByException || (budget.known && !budget.withinCap)
                  ? "warning"
                  : budget.known
                    ? "success"
                    : "default"
              }
              label={
                budget.overCapByException || (budget.known && !budget.withinCap)
                  ? "Exception active (1 sandbox over cap)"
                  : budget.known
                    ? "Within budget cap"
                    : "Unbounded budget"
              }
            />
            <Typography variant="body2" component="span">
              {budget.known ? (
                <>
                  Committed: {formatGiB(budget.committedMemoryMB)} of{" "}
                  {formatGiB(budget.budgetMemoryMB)} memory &middot;{" "}
                  {budget.committedCPUs || 0} of {budget.budgetCPUs || 0} vCPU (
                  {count} {countWord})
                  {(budget.repairReserveMemoryMB > 0 ||
                    budget.repairReserveCPUs > 0) && (
                    <>
                      {" "}
                      &middot; Repair reserve: {budget.repairReserveCPUs ||
                        0}{" "}
                      vCPU / {formatGiB(budget.repairReserveMemoryMB)}
                    </>
                  )}
                </>
              ) : (
                <>
                  Committed: {formatGiB(budget.committedMemoryMB)} memory
                  &middot; {budget.committedCPUs || 0} vCPU ({count} {countWord}
                  ) &mdash; no host budget bound
                </>
              )}
            </Typography>
          </Box>
          {budget.pools?.length > 0 && (
            <Typography
              variant="caption"
              color="text.secondary"
              component="div"
              sx={{ mb: "0.25rem" }}
            >
              Pools:{" "}
              {budget.pools
                .map(
                  (p) =>
                    `${poolLabel(p.pool)}: ${p.count} (${p.cpus || 0} vCPU / ${formatGiB(p.memoryMB)})`,
                )
                .join(" · ")}
            </Typography>
          )}
        </>
      )}
      <SandboxClusterFree cluster={budget?.cluster} />
      <SandboxOvercommitStatus budget={budget} overcommit={overcommit} />
      {reconciliation && (
        <Box data-testid="sandbox-reconciliation">
          <Box
            sx={{
              display: "flex",
              alignItems: "center",
              flexWrap: "wrap",
              gap: "0.5rem",
            }}
          >
            <Typography
              variant="caption"
              color="text.secondary"
              component="span"
            >
              Reconciliation: {reconciliation.accountedCount || 0} accounted in
              ledger vs. {reconciliation.liveCount || 0} live on backend
              {(reconciliation.liveMemoryMB > 0 ||
                reconciliation.liveCPUs > 0) &&
                ` (${reconciliation.liveCPUs || 0} vCPU / ${formatGiB(reconciliation.liveMemoryMB)})`}
              {" · "}
              Orphans reaped: {reconciliation.totalOrphansReaped || 0}
              {reconciliation.orphansReaped > 0 &&
                ` (${reconciliation.orphansReaped} this cycle)`}
            </Typography>
            <Chip
              size="small"
              color={reconciliation.inSync ? "success" : "warning"}
              label={reconciliation.inSync ? "in sync" : "discrepancy repaired"}
            />
          </Box>
          {reconciliation.discrepancies?.length > 0 && (
            <Alert severity="warning" sx={{ mt: "0.5rem" }}>
              {reconciliation.discrepancies.join("; ")}
            </Alert>
          )}
        </Box>
      )}
    </Box>
  );
}

const STANDING_SANDBOXES_DOC =
  "https://github.com/bwsalmon/grain/blob/main/docs/repo-controllers-and-environments.md#3-long-running-standing-sandbox-hosting";

// StandingSandboxesPanel is the standing sandboxes grain/config declares
// and the daemon keeps running (standingSandboxes on GET /api/sandboxes):
// one row each, with whether its last launch or probe failed and why, and
// -- for one whose launches keep failing -- when the supervisor, which
// backs off, will next try. On a deployment defining none, it explains
// what they are and points to the director chat and
// tables/standing_sandbox.json.
function StandingSandboxesPanel({ standing, onOpenSession }) {
  const list = Array.isArray(standing) ? standing : [];
  const directorHref = buildPath({ view: "session", sessionId: "director" });
  return (
    <Box
      id="standing-sandboxes"
      sx={{ mb: "0.75rem" }}
      data-testid="standing-sandboxes"
    >
      <Typography variant="body2" color="text.secondary" sx={{ mb: "0.25rem" }}>
        Standing sandboxes
      </Typography>
      {list.length === 0 ? (
        <>
          <Typography variant="body2" color="text.secondary">
            Standing sandboxes keep a long-lived container or VM image running
            with secrets injected from the secret store. To add one, ask the{" "}
            <Link
              href={directorHref}
              onClick={(evt) => {
                if (
                  !onOpenSession ||
                  evt.button !== 0 ||
                  evt.metaKey ||
                  evt.ctrlKey ||
                  evt.shiftKey ||
                  evt.altKey
                )
                  return;
                evt.preventDefault();
                onOpenSession("director");
              }}
            >
              director
            </Link>{" "}
            to set one up — e.g.{" "}
            <em>
              &apos;set up a standing sandbox running
              ghcr.io/acme/worker:latest&apos;
            </em>
            .
          </Typography>
          <Typography variant="caption" color="text.secondary" component="div">
            Or define it in the config repo&apos;s{" "}
            <code>tables/standing_sandbox.json</code> (
            <Link
              href={STANDING_SANDBOXES_DOC}
              target="_blank"
              rel="noopener noreferrer"
            >
              docs/repo-controllers-and-environments.md
            </Link>
            ).
          </Typography>
        </>
      ) : (
        <TableContainer>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Image</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Started</TableCell>
                <TableCell>Restarts</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {list.map((s) => (
                <TableRow key={s.name}>
                  <TableCell>
                    {s.name}
                    {s.sandbox && (
                      <Typography
                        variant="caption"
                        color="text.secondary"
                        sx={{ display: "block", fontFamily: "monospace" }}
                      >
                        {s.sandbox}
                      </Typography>
                    )}
                  </TableCell>
                  <TableCell
                    sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}
                  >
                    {s.image || "—"}
                  </TableCell>
                  <TableCell>
                    <Chip
                      size="small"
                      color={s.failed ? "error" : "success"}
                      label={s.failed ? "failed" : "running"}
                    />
                    {/* Why, beside the chip rather than as its label: a chip
                      is one lowercased line, and a real launch error is a
                      sentence naming identifiers -- as a label it pushed
                      the started and restarts columns off the pane. */}
                    {s.failed && s.lastError && (
                      <Typography
                        variant="caption"
                        color="error"
                        component="div"
                        sx={{ mt: 0.5, overflowWrap: "anywhere" }}
                      >
                        {s.lastError}
                      </Typography>
                    )}
                    {s.failed && s.nextAttempt && (
                      <Typography
                        variant="caption"
                        color="text.secondary"
                        sx={{ display: "block" }}
                        data-testid="standing-next-attempt"
                      >
                        {s.failures > 1
                          ? `${s.failures} failed launches in a row; `
                          : ""}
                        next attempt {new Date(s.nextAttempt).toLocaleString()}
                      </Typography>
                    )}
                  </TableCell>
                  <TableCell sx={{ whiteSpace: "nowrap" }}>
                    {s.startedAt ? new Date(s.startedAt).toLocaleString() : "—"}
                  </TableCell>
                  <TableCell>{s.restarts}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}

// SandboxImageLine is which image every sandbox here is created from, and
// -- when that is not the one this build expects -- what that costs.
//
// The drift is otherwise invisible from the outside: each upgrade pulls
// the guest the new grain names, reports the pull as a success, and then
// the daemon goes on creating VMs from the pinned one, so the deployment
// runs a daemon from one commit against guests from another and the only
// symptom is whatever the older guest is missing. The daemon warns at
// every start, but nobody is reading the journal weeks later when a
// sandbox starts behaving like an older commit -- they are looking at
// this pane (grain/task-306).
//
// It carries the guest reading beside the reference (grain/task-397)
// because the two are one question. `Image: …/guest:sha-7746d62` on its
// own says which guest is running and nothing about whether anything is
// still publishing one -- and on 2026-09-20 that was the whole gap: the
// repository's release switch had been off for a day, so no merge had
// published a guest, every branch's own CI missed the content-addressed
// cache and built a ~1GB guest inline, and the line that would have
// connected the two existed nowhere.
// guestWarning is whether the guest reading is worth an alert rather
// than silence: nothing is publishing a guest, or something is supposed
// to be and it is not coming out.
//
// The second of those is why this is a function. The condition here was
// `!guest.publishing` alone, which drew the line for a switch an
// operator had turned off and drew nothing at all for the state that
// actually cost this deployment three days -- releases switched on, the
// release run failing on every merge to main, and no guest published
// since the 16th (grain/task-230). The pane showed the guest reference
// and not one word about its publisher being dead, which is precisely
// the join this line exists to make.
//
// `detail` carries the whole sentence in both cases, written server-side
// (pkg/ui/sandbox_guest.go), so this decides only whether to draw it.
//
// `cacheStalled` is the third condition and the one the first two cannot
// cover (grain/task-547): with releases off and grain dispatching the
// cache job after every merge instead, `publishing` is false -- which
// already draws this alert -- but with releases *on* and the cache job
// named among a repository's post-merge dispatch jobs, a dispatch that
// fills nothing is a cold guest cache behind a reading that says both
// switches are where they should be.
function guestWarning(guest) {
  if (!guest) return false;
  return !guest.publishing || guest.stalled || guest.cacheStalled;
}

function SandboxImageLine({ image, guest }) {
  if (!image) return null;
  return (
    <>
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{
          mb: "0.5rem",
          overflowWrap: "anywhere",
          fontFamily: "monospace",
          fontSize: "0.8rem",
        }}
      >
        Image: {image.image}
      </Typography>
      {sandboxImageDrifted(image) && (
        <Alert severity="warning" sx={{ mb: "1rem" }}>
          Sandboxes are created from {image.image}, pinned by this host&apos;s
          own configuration, but this build of grain is stamped for{" "}
          {image.expected}. The daemon and its sandboxes are from different
          commits, and the sandbox image every upgrade pulls is the stamped one,
          which nothing then runs. Dropping the <code>-sandbox-image</code> pin
          from this host&apos;s grain-daemon.service lets the sandbox move with
          grain again.
        </Alert>
      )}
      {guestWarning(guest) && (
        <Alert severity="warning" sx={{ mb: "1rem" }}>
          {guest.detail}
        </Alert>
      )}
    </>
  );
}

// formatUptime renders how long the daemon has been up, in the words
// somebody diagnosing a restart loop uses: seconds while it is seconds,
// because "up 4s" is the whole diagnosis.
export function formatUptime(ms) {
  if (ms === null || ms === undefined || Number.isNaN(ms) || ms < 0) return "—";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400)
    return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

// daemonRestarting is whether the daemon reporting this has restarted,
// over whatever it counted, without anybody asking it to -- which is to
// say, whether this deployment has been restarting rather than running.
//
// One start is the ordinary answer and says nothing; no count at all
// (a deployment with no journal to read, a pod that was not told its own
// name) is not a claim that it has been stable, so it says nothing
// either. Nor do restarts grain asked for itself -- an upgrade's, the
// restart button's -- which the server tells apart from a crash and
// reports as `restarting: false` (grain task 9: two upgrades a minute
// apart were raised here as a restart loop). A daemon too old to send
// that verdict is read the way it always was: any start past the first.
export function daemonRestarting(daemon) {
  if (!daemon || !(daemon.starts > 1)) return false;
  if (typeof daemon.restarting === "boolean") return daemon.restarting;
  return true;
}

// requestedNote is the start line's account of the restarts grain asked
// for itself, so a count above one that raises no alarm says why.
export function requestedNote(daemon) {
  const n = daemon?.requestedStarts || 0;
  if (n <= 0) return "";
  return n === 1
    ? " (1 of them a restart grain asked for: an upgrade, or the restart or reboot button)"
    : ` (${n} of them restarts grain asked for: an upgrade, or the restart or reboot button)`;
}

// startsScope is what that count is a count *of*, in the words the
// daemon sent: "in the last 30 minutes" where it counted its unit's
// starts in the journal over a window, "since this pod was created"
// where it counted a kubelet's restarts of its own container and there
// is no window at all (grain/task-367). The two numbers mean different
// things and this pane cannot tell which it has, so the server says.
//
// The fallback is for a daemon older than that field, which is a
// windowed journal reading by construction.
export function startsScope(daemon) {
  if (daemon?.startsScope) return daemon.startsScope;
  return daemon?.windowMinutes
    ? `in the last ${daemon.windowMinutes} minutes`
    : "";
}

// daemonAdvice is the rest of what only the server knows about this
// deployment's shape: what passed the flags it is ignoring, what an
// operator does about them, and which command shows the log of the start
// that failed. Each falls back to the host deployment's answer, which is
// what a daemon too old to send them is (the Kubernetes reading is newer
// than all three).
function daemonAdvice(daemon) {
  return {
    flagSource: daemon?.flagSource || "this host's grain-daemon.service",
    flagFix:
      daemon?.flagFix ||
      "Re-running scripts/setup.sh on the host rewrites the unit.",
    logCommand: daemon?.logCommand || "journalctl -u grain-daemon.service",
  };
}

// sentenceCase capitalises a server-supplied noun phrase that starts a
// sentence ("this host's grain-daemon.service passes ...").
function sentenceCase(text) {
  return text ? text[0].toUpperCase() + text.slice(1) : text;
}

// BaseBranchGateAlerts is every base branch this deployment's merge
// queue is merging onto that nothing is judging, or that nothing has
// judged for longer than it should have been (pkg/ui/basebranchgate.go,
// grain/task-453).
//
// It is on this pane, above the machine readings, because of what
// happened when it was on no pane at all. This deployment's own `main`
// went 33 hours with nothing judging it and about a hundred merges
// landing onto it, and no surface a person looks at said so -- not the
// repository pane, not the inbox, not this one. The switch-shaped way
// into that state has since been closed (grain/task-461), which removes
// the cause but not the blind spot: a branch can still quietly stop
// having runs started for it, and nothing but this would say so.
//
// A row here is a control that is off rather than a reading that is
// high, so an unjudged branch is drawn as an error and a branch that is
// merely late as a warning. A healthy deployment renders nothing.
function BaseBranchGateAlerts({ gates }) {
  if (!gates || gates.length === 0) return null;
  return (
    <>
      {gates.map((gate) => (
        <Alert
          key={`${gate.repo}/${gate.branch || ""}`}
          severity={gate.judged ? "warning" : "error"}
          sx={{ mb: "1rem" }}
        >
          {gate.detail}
        </Alert>
      ))}
    </>
  );
}

// BaseBranchPublishingAlerts is every base branch this deployment has
// stopped publishing from — merges landing, releases failing, nothing
// shipping (pkg/ui/basebranchrelease.go, grain/task-511).
//
// It sits under the gate alerts above because the two findings are
// neighbours and opposites. That one is a merge queue merging onto a
// tree nothing has tested; this one is a repository whose queue is
// working perfectly and whose images have not moved since Tuesday. A
// failed release deliberately holds nothing since grain/task-461 — the
// operator's ruling was that gating merges on the publish was the bug —
// and removing the hold removed the only thing that ever said a
// publisher was broken, which is what this line replaces.
//
// It is a warning rather than an error: nothing is stuck, nothing is
// unsafe, and the repair is an operator's whenever they get to it.
function BaseBranchPublishingAlerts({ publishing }) {
  if (!publishing || publishing.length === 0) return null;
  return (
    <>
      {publishing.map((branch) => (
        <Alert
          key={`${branch.repo}/${branch.branch || ""}`}
          severity="warning"
          sx={{ mb: "1rem" }}
        >
          {branch.detail}
        </Alert>
      ))}
    </>
  );
}

// HostCredentialsAlert is this host's credentials having expired
// (pkg/ui/host_credentials.go): every run dispatched while it lasts fails
// to start its tools and is parked, so it is said here once, for the
// host, with the command that fixes it. An error rather than
// a warning: nothing is dispatched successfully until somebody acts.
function HostCredentialsAlert({ warning }) {
  if (!warning) return null;
  return (
    <Alert severity="error" sx={{ mb: "1rem" }}>
      {warning.detail}
      {warning.runs > 1 &&
        ` — ${warning.runs} runs have failed this way since ${new Date(warning.since).toLocaleString()}.`}{" "}
      Tasks it stopped are parked; reply on each once that has been run.
    </Alert>
  );
}

// DaemonHealthLine is the deployment's own state, above the readings it
// takes of the machine under it: how long this process has been up, how
// many times its service has started lately, and which of the flags it
// was started with it does not understand.
//
// It is here because of the eight minutes on 2026-09-18 when this
// deployment was not running at all (grain/task-233). A unit passing a
// flag the image had dropped exited on every start; systemd restarted it
// every five seconds, 52 times; nothing was dispatched throughout, and
// nothing anywhere said so -- it was found two hours later by reading
// the journal by hand. A daemon in that state serves this pane only in
// the seconds between crashes, which is exactly when this line has to be
// on it.
function DaemonHealthLine({ daemon }) {
  if (!daemon) return null;
  const started = Date.parse(daemon.startedAt);
  const uptime = Number.isNaN(started) ? null : Date.now() - started;
  const scope = startsScope(daemon);
  const { flagSource, flagFix, logCommand } = daemonAdvice(daemon);
  return (
    <>
      <Typography variant="body2" color="text.secondary" sx={{ mb: "0.5rem" }}>
        Daemon
      </Typography>
      <Typography variant="body2" sx={{ mb: "1rem" }}>
        Up {formatUptime(uptime)}
        {daemon.starts > 0 &&
          `, started ${daemon.starts} ${daemon.starts === 1 ? "time" : "times"} ${scope}`.trimEnd()}
        {daemon.starts > 1 && requestedNote(daemon)}
        {daemon.error && ` (start count unavailable: ${daemon.error})`}
      </Typography>
      {daemonRestarting(daemon) && (
        <Alert severity="error" sx={{ mb: "1rem" }}>
          This daemon has started {daemon.starts} times {scope}: it is
          restarting rather than running, and nothing is dispatched between the
          restarts. Whatever it is failing on is in the daemon&apos;s own log —{" "}
          <code>{logCommand}</code>.
        </Alert>
      )}
      {daemon.unknownFlags?.length > 0 && (
        <Alert severity="warning" sx={{ mb: "1rem" }}>
          {sentenceCase(flagSource)} passes {daemon.unknownFlags.join(", ")},
          which this build of grain does not define: the daemon started and is
          ignoring {daemon.unknownFlags.length === 1 ? "it" : "them"}. The
          command line it was started with and the image it runs have drifted
          apart — an upgrade or a rollback moved one without the other — so each
          of those is a setting this deployment asks for and is not getting.{" "}
          {flagFix}
        </Alert>
      )}
    </>
  );
}

// SandboxHealthPage is the deployment-wide Sandboxes destination. It started
// as a diagnostic tab under System; keeping it in the primary navigation makes
// the live capacity grain is spending visible alongside agent work and CI.
// It shows a live view of
// every live run's own sandbox -- a granule VM or a host directory,
// whichever backend this deployment runs -- plus the daemon's own host
// machine's CPU/RAM/disk pressure. Both come back from the same
// GET /api/sandboxes call since a sandbox that looks stuck is often
// really the host it runs on being starved, so debugging one usually
// means looking at both at once. GET /api/sandboxes' own "enabled" flag
// says whether this deployment has either piece configured at all, the
// same convention LogsPage's own GET /api/logs already establishes for
// the System overlay this panel sits alongside.
export default function SandboxHealthPage({
  showError,
  standalone = false,
  onOpenSession,
}) {
  const [data, setData] = useState(null);
  // The live table's rows for standing sandboxes, by the sandbox each
  // runs as (StandingSandboxStatus.Sandbox).
  const standingBySandbox = new Map(
    (data?.standingSandboxes || [])
      .filter((s) => s.sandbox)
      .map((s) => [s.sandbox, s]),
  );
  // The live table's rows, CI's apart from the rest (grain/task-990): a
  // busy kubernetes deployment holds dozens of CI sandboxes beside a
  // handful of agent, persistent and standing ones, and those few are
  // what an operator usually came for. The CI rows fold behind one
  // summary row, collapsed by default, and the choice is remembered per
  // browser. A standing sandbox the backend reports under some other
  // kind is never folded, since it is named by its standing row here.
  const [ciExpanded, setCIExpanded] = useState(readCISandboxesExpanded);
  const toggleCI = () => {
    setCIExpanded((v) => {
      writeCISandboxesExpanded(!v);
      return !v;
    });
  };
  const liveRows = data?.sandboxes || [];
  const isCIRow = (s) => s.kind === "ci" && !standingBySandbox.has(s.sandbox);
  const ciRows = liveRows.filter(isCIRow);
  const otherRows = liveRows.filter((s) => !isCIRow(s));
  const ciRowIds = ciRows.map((s) => `live-sandbox-${s.sandbox}`).join(" ");
  const [history, setHistory] = useState({
    host: emptyHostSeries,
    sandboxes: {},
  });

  // Why the last read failed, or "" once one succeeds. Without it a pane
  // whose very first read failed spun "Loading sandbox health" for as
  // long as it was open, and one whose reads started failing later went
  // on showing the last figures as if they were current.
  const [loadError, setLoadError] = useState("");
  // Whether the reads are failing right now, so a run of failures is
  // reported once rather than in a new snackbar every REFRESH_MS.
  const failing = useRef(false);

  const refresh = useCallback(async () => {
    try {
      const result = await api("/api/sandboxes");
      setData(result);
      setHistory((prev) => appendHistory(prev, result));
      setLoadError("");
      failing.current = false;
    } catch (err) {
      setLoadError(err?.message || String(err));
      if (!failing.current) showError(err);
      failing.current = true;
    }
  }, [showError]);

  // One poll at a time, REFRESH_MS after the last one came back -- not a
  // setInterval firing every REFRESH_MS regardless.
  //
  // This endpoint is not a database read. Answering it runs a command
  // inside every live guest (orchestrator.GranuleSandboxes.Health), and
  // that is deliberately allowed to take much longer than this interval
  // on a loaded host, because a guest which is slow to answer is not a
  // guest that is down (orchestrator's own healthTimeout). Polling on a
  // fixed interval through that would stack requests -- four of them in
  // flight against every sandbox at once, each adding work to the very
  // machine whose overload made the previous one slow, and the pane would
  // be helping to cause what it is there to report.
  useEffect(() => {
    let timer = null;
    let stopped = false;
    const tick = async () => {
      await refresh();
      if (stopped) return;
      timer = setTimeout(tick, REFRESH_MS);
    };
    tick();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
    };
  }, [refresh]);

  const refreshButton = (data?.enabled || loadError) && (
    <Button
      size="small"
      variant="outlined"
      sx={{ ml: "auto" }}
      onClick={refresh}
    >
      Refresh
    </Button>
  );
  const Root = standalone ? "main" : "section";

  // renderLiveRow is one live sandbox's row, whichever group it is in.
  const renderLiveRow = (s) => {
    const series = history.sandboxes[s.sandbox] || emptySeries;
    // A standing sandbox runs as "s" and a hash of its
    // declared name, which is all the backend knows it
    // by, and the backend counts it with the runs; the
    // person reading this table knows it by the name
    // grain/config gives it.
    const standing = standingBySandbox.get(s.sandbox);
    return (
      <TableRow key={s.sandbox} id={`live-sandbox-${s.sandbox}`}>
        <TableCell>
          {standing ? standing.name : s.sandbox}
          {standing && (
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ display: "block", fontFamily: "monospace" }}
            >
              {s.sandbox}
            </Typography>
          )}
        </TableCell>
        <TableCell>{standing ? "standing" : s.kind || "—"}</TableCell>
        <TableCell>{s.backend}</TableCell>
        <TableCell sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}>
          {s.name}
        </TableCell>
        {/* The chip, plus how long the probe behind it
            took on the rows where that is the finding --
            see probeNote. */}
        <TableCell>
          <SandboxStatus sandbox={s} />
        </TableCell>
        <TableCell
          sx={{ whiteSpace: "nowrap" }}
          title={
            s.startedAt
              ? `Started ${new Date(s.startedAt).toLocaleString()}`
              : undefined
          }
        >
          {runningFor(s.startedAt)}
        </TableCell>
        <TableCell sx={{ whiteSpace: "nowrap" }}>{formatShape(s)}</TableCell>
        <TableCell>{s.networkAccess || "—"}</TableCell>
        <TableCell>{s.loadAverage || "—"}</TableCell>
        <TableCell>
          <Sparkline
            data={series.cpu}
            width={80}
            height={24}
            valueLabel={(value) => value.toFixed(2)}
          />
        </TableCell>
        <TableCell>{formatMemory(s.memoryUsedMB, s.memoryTotalMB)}</TableCell>
        <TableCell>{formatSwap(s.swapUsedMB, s.swapTotalMB)}</TableCell>
        <TableCell>
          <Sparkline
            data={series.mem}
            width={80}
            height={24}
            color="#9c27b0"
            valueLabel={(value) => `${value} MB`}
          />
        </TableCell>
        <TableCell>{formatDisk(s.diskUsedMB, s.diskTotalMB)}</TableCell>
        <TableCell>
          <Sparkline
            data={series.disk}
            width={80}
            height={24}
            color="#2e7d32"
            valueLabel={(value) => formatDisk(value, s.diskTotalMB)}
          />
        </TableCell>
        {/* Whether a task in this sandbox could start a VM
            of its own. A dash rather than "no" when the
            backend gave no reading at all (a host
            sandbox, or a guest too old to answer): the two
            are different, and only one of them is a
            finding. */}
        <TableCell>
          {NESTED_VIRT[s.nestedVirt] ? (
            <Chip
              size="small"
              color={NESTED_VIRT[s.nestedVirt].color}
              label={NESTED_VIRT[s.nestedVirt].label}
              title={NESTED_VIRT[s.nestedVirt].title}
            />
          ) : (
            "—"
          )}
        </TableCell>
      </TableRow>
    );
  };

  return (
    <Root
      className={
        standalone
          ? "sandbox-health-panel standalone-page"
          : "sandbox-health-panel"
      }
    >
      {standalone ? (
        <ListHeader
          title="Sandboxes"
          icon={<ItemGlyph kind="sandboxes" size={20} />}
          action={refreshButton}
        />
      ) : (
        <Box
          sx={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            mb: 1,
          }}
        >
          <Typography variant="h6" component="h1">
            Sandboxes
          </Typography>
          {refreshButton}
        </Box>
      )}
      {data === null && !loadError && (
        <Box sx={{ display: "flex", justifyContent: "center", py: 3 }}>
          <CircularProgress size={28} aria-label="Loading sandbox health" />
        </Box>
      )}
      {data === null && loadError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          Sandbox health could not be read ({loadError}). This pane tries again
          every few seconds.
        </Alert>
      )}
      {data !== null && loadError && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          The latest read failed ({loadError}), so the figures below are from
          the last one that succeeded. This pane tries again every few seconds.
        </Alert>
      )}
      {data !== null && <HostCredentialsAlert warning={data.hostCredentials} />}
      {data !== null && !data.enabled && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Not available: this deployment has no sandbox pool or host stats
          configured.
        </Alert>
      )}
      {data !== null && data.enabled && (
        <Box>
          {/* The daemon's own state comes first, above the machine it
              runs on: every figure below this is taken by a process that
              is running, and all of them look healthy in the four
              seconds between two crashes (grain/task-233). */}
          <DaemonHealthLine daemon={data.daemon} />
          {/* And then the controls that are off, above the figures that
              are merely high: a base branch nothing is testing costs
              nothing measurable until the day it costs a broken `main`,
              so it is never the thing a reader notices on a pane full of
              live numbers unless it is put in front of them. */}
          <BaseBranchGateAlerts gates={data.baseBranchGates} />
          {/* And beside them the repository that is merging happily and
              shipping nothing, which has had no surface at all since a
              failed release stopped holding the queue (grain/task-511). */}
          <BaseBranchPublishingAlerts publishing={data.baseBranchPublishing} />
          <Typography
            variant="body2"
            color="text.secondary"
            sx={{ mb: "0.5rem" }}
          >
            Host
          </Typography>
          {data.hostError && (
            <Alert severity="warning" sx={{ mb: "1rem" }}>
              Host stats unavailable: {data.hostError}
            </Alert>
          )}
          {data.host && (
            <>
              <Typography variant="body2" sx={{ mb: "1rem" }}>
                Load average (1/5/15 min):{" "}
                <LoadFigure
                  load={data.host.loadAverage1}
                  cpus={data.host.cpus}
                />{" "}
                /{" "}
                <LoadFigure
                  load={data.host.loadAverage5}
                  cpus={data.host.cpus}
                />{" "}
                /{" "}
                <LoadFigure
                  load={data.host.loadAverage15}
                  cpus={data.host.cpus}
                />
                {/* The denominator those three figures are read against.
                    Without it a load of 8 is unreadable -- an idle
                    32-core machine and a starved 4-core one say the same
                    number -- and a host that reported no count says
                    nothing here rather than claiming one. */}
                {data.host.cpus ? ` of ${data.host.cpus} vCPU` : ""}
                {" · "}
                {Number.isFinite(data.host.cpuUtilization) && (
                  <>
                    Utilization: {Math.round(data.host.cpuUtilization * 100)}%
                    ·{" "}
                  </>
                )}
                Memory:{" "}
                {formatMemory(data.host.memoryUsedMB, data.host.memoryTotalMB)}
                {" · "}
                Swap: {formatSwap(data.host.swapUsedMB, data.host.swapTotalMB)}
                {data.host.nestedVirt && (
                  <>
                    {" · "}
                    {/* The precondition for a sandbox running VMs of its
                        own, shown here because it is a fact about this
                        machine rather than about any one sandbox -- and
                        because it is what tells a sandbox reporting no
                        CPU virtualization flag from one whose guest is
                        simply missing the device. */}
                    Nested virtualization:{" "}
                    {HOST_NESTED_VIRT[data.host.nestedVirt] ||
                      data.host.nestedVirt}
                    {data.host.nestedVirtDetail
                      ? ` (${data.host.nestedVirtDetail})`
                      : ""}
                  </>
                )}
              </Typography>
              <div
                style={{
                  display: "flex",
                  flexWrap: "wrap",
                  gap: "1rem 2.5rem",
                  marginBottom: "1.5rem",
                }}
              >
                <div>
                  <Typography
                    variant="caption"
                    color="text.secondary"
                    component="div"
                  >
                    CPU utilization (%)
                  </Typography>
                  <Sparkline
                    data={history.host.cpu}
                    valueLabel={(value) => `${Math.round(value)}%`}
                  />
                </div>
                <div>
                  <Typography
                    variant="caption"
                    color="text.secondary"
                    component="div"
                  >
                    Memory (MB)
                  </Typography>
                  <Sparkline
                    data={history.host.mem}
                    color="#9c27b0"
                    valueLabel={(value) => `${value} MB`}
                  />
                </div>
              </div>
              <HostDisks disks={data.host.disks} history={history.host.disks} />
              {/* Storage is what is filling those bars, each figure
                  beside the bound that applies to it and beside whatever
                  can safely be done about it: the CI history, bounded
                  rather than cleared (grain/task-290); and, since
                  grain/task-276, the CI job cache -- expensive to empty
                  rather than destructive, so it asks first -- the CI
                  artifacts, which have no button at all because they are
                  the only copy of what a run produced, and the
                  sandboxes themselves, which are the largest consumer
                  of this volume by far and go when their runs end.

                  The sandboxes come first rather than last, which is not
                  the order these were built in: somebody reading this is
                  asking "what is eating my 443 GB", and the answer is
                  almost always the sandboxes -- the caches under them
                  are megabytes and single gigabytes, and an operator who
                  empties all four and finds the bar has barely moved has
                  been sent to the wrong place.

                  The proxy cache is the one of these with figures and a
                  flush button somewhere else: it had them here from
                  grain/task-266 until it got a page of its own in the
                  rail (ProxyCachePage.jsx, grain/task-321). What is
                  left of it here is the pointer, because an operator
                  reading a full bar still has to be told the cache is
                  one of the things filling it, how much of it, and
                  where the button went. */}
              <Typography
                variant="body2"
                color="text.secondary"
                sx={{ mb: "0.5rem" }}
              >
                Storage
              </Typography>
              <Typography
                variant="caption"
                color="text.secondary"
                component="div"
                sx={{ mb: "1.5rem" }}
              >
                Sandboxes -- the runs in flight, and the largest consumer of
                this volume by far. There is nothing to clear here: a
                sandbox&apos;s disk goes when its run ends, and this deployment
                deletes orphaned ones itself once it is under the floor above.
              </Typography>
              <Box sx={{ mb: "1.5rem", maxWidth: "44rem" }}>
                <SandboxStoragePanel storage={data.sandboxStorage} />
              </Box>
              <Typography
                variant="caption"
                color="text.secondary"
                component="div"
                sx={{ mb: "1.5rem" }}
              >
                Proxy cache -- what sandboxes have downloaded through this
                deployment rather than from the internet
                {packageCacheSummary(data.packageCache)}. What it holds, per
                ecosystem, and the buttons that flush it are on System &rarr;
                Caches; whether sandboxes use it at all, and how large it may
                grow, are on Settings &rarr; Caches.
              </Typography>
              <Typography
                variant="caption"
                color="text.secondary"
                component="div"
              >
                CI history -- the step output of this deployment's own Grain CI
                runs, which is what makes a red run readable afterwards. It is
                swept oldest-first back under the ceiling rather than flushed by
                hand: how much is kept, for how long, and how much any one run
                may store are on Settings &rarr; CI, under CI history.
              </Typography>
              <Box sx={{ mb: "1.5rem", maxWidth: "44rem" }}>
                <CIHistoryPanel history={data.ciHistory} />
              </Box>
              <Typography
                variant="caption"
                color="text.secondary"
                component="div"
              >
                CI job cache -- the results of jobs grain CI has already run,
                which is how a commit that changes nothing a job reads gets its
                verdict back in seconds. Emptying it loses nothing, since an
                entry is only a record that identical work was already done, but
                it costs every job of the next few runs a re-run; its bounds are
                on Settings &rarr; CI, under CI job cache.
              </Typography>
              <Box sx={{ mb: "1.5rem", maxWidth: "44rem" }}>
                <CIJobCachePanel
                  cache={data.ciJobCache}
                  showError={showError}
                  onCleared={(cache) =>
                    setData((prev) => ({ ...prev, ciJobCache: cache }))
                  }
                />
              </Box>
              <Typography
                variant="caption"
                color="text.secondary"
                component="div"
              >
                CI artifacts -- what a failing job was told to keep because its
                log could not carry it. Unlike everything above, these are not
                reproducible: the run that made them is over, and the bounds
                below are the only thing that ever removes one. They are on
                Settings &rarr; CI, under CI artifacts.
              </Typography>
              <Box sx={{ mb: "1.5rem", maxWidth: "44rem" }}>
                <CIArtifactsPanel artifacts={data.ciArtifacts} />
              </Box>
            </>
          )}
          {!data.host && !data.hostError && (
            <Typography
              variant="body2"
              color="text.secondary"
              sx={{ mb: "1rem" }}
            >
              Not configured for this deployment.
            </Typography>
          )}

          <Typography
            id="sandboxes"
            variant="body2"
            color="text.secondary"
            sx={{ mb: "0.5rem" }}
          >
            Sandboxes
          </Typography>
          {/* How old these rows are, on the polls where the answer came
              from the previous sweep rather than from one taken now --
              silent on every other poll. */}
          <SandboxReadingLine reading={data.sandboxReading} />
          {/* Which image they are built from comes before the table of
              the ones currently in flight: it is true of this deployment
              whether or not anything is running right now, and an idle
              deployment is exactly when somebody is here to check it. */}
          <SandboxImageLine
            image={data.sandboxImage}
            guest={data.sandboxGuest}
          />
          <SandboxBudgetPanel
            budget={data.budget || data.sandboxCommitment?.budget}
            overcommit={
              data.overcommit ||
              data.budget?.overcommit ||
              data.sandboxCommitment?.budget?.overcommit
            }
            reconciliation={data.reconciliation}
          />
          {/* And what they have cost altogether, which is the other
              thing true of this deployment whether or not anything is
              running right now (grain/task-315). It sits above the live
              table on purpose: the table answers "what is up", and the
              question that had no answer anywhere was "how many have
              there been" -- asked of an idle deployment as often as of a
              busy one, and answered until now by counting log lines. */}
          <Box sx={{ mb: "0.75rem" }}>
            <SandboxLifetimePanel lifetime={data.lifetime} />
          </Box>
          <StandingSandboxesPanel
            standing={data.standingSandboxes}
            onOpenSession={onOpenSession}
          />
          <PersistentSandboxesPanel
            sandboxes={data.persistentSandboxes}
            onChange={refresh}
            onOpenSession={onOpenSession}
          />
          {!data.sandboxes || data.sandboxes.length === 0 ? (
            <Typography variant="body2" color="text.secondary">
              Nothing in flight, so no sandboxes exist.
            </Typography>
          ) : (
            <TableContainer>
              <Table size="small">
                <TableHead>
                  <TableRow>
                    {/* A sandbox is named after whatever holds it, and
                      that is no longer only an agent run: a grain CI job
                      gets one of its own, on the same sandbox backend
                      (model.Limits.CIJobs), and its name starts "ci-".
                      Both are listed here because both are machines this
                      deployment is holding -- the count in this table is
                      meant to be the number that exists. */}
                    <TableCell>Run or CI job</TableCell>
                    <TableCell>Type</TableCell>
                    <TableCell>Backend</TableCell>
                    <TableCell>Name</TableCell>
                    <TableCell>Status</TableCell>
                    <TableCell>Running for</TableCell>
                    <TableCell>Configuration</TableCell>
                    <TableCell>Network</TableCell>
                    <TableCell>Load average</TableCell>
                    <TableCell>CPU trend</TableCell>
                    <TableCell>Memory</TableCell>
                    <TableCell>Swap</TableCell>
                    <TableCell>Memory trend</TableCell>
                    <TableCell>Disk</TableCell>
                    <TableCell>Disk trend</TableCell>
                    <TableCell>Nested virt</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {otherRows.map(renderLiveRow)}
                  {ciRows.length > 0 && (
                    <TableRow data-testid="ci-sandboxes-group">
                      <TableCell colSpan={LIVE_COLUMNS} sx={{ py: "0.35rem" }}>
                        <Box
                          sx={{
                            display: "flex",
                            alignItems: "center",
                            flexWrap: "wrap",
                            columnGap: 1,
                          }}
                        >
                          <ButtonBase
                            aria-expanded={ciExpanded}
                            aria-controls={ciExpanded ? ciRowIds : undefined}
                            aria-describedby="ci-sandboxes-summary"
                            onClick={toggleCI}
                            sx={{
                              gap: 1,
                              font: "inherit",
                              fontWeight: 500,
                              borderRadius: 1,
                              "&.Mui-focusVisible": {
                                outline: 2,
                                outlineColor: "primary.main",
                              },
                            }}
                          >
                            <ExpandMoreIcon
                              fontSize="small"
                              aria-hidden
                              sx={{
                                transform: ciExpanded
                                  ? "rotate(0deg)"
                                  : "rotate(-90deg)",
                                transition: "transform 150ms",
                              }}
                            />
                            {`CI sandboxes (${ciRows.length})`}
                          </ButtonBase>
                          <Typography
                            id="ci-sandboxes-summary"
                            variant="caption"
                            color="text.secondary"
                            onClick={toggleCI}
                            sx={{ cursor: "pointer" }}
                          >
                            {ciSandboxesSummary(ciRows, data.budget?.pools)}
                          </Typography>
                        </Box>
                      </TableCell>
                    </TableRow>
                  )}
                  {ciExpanded && ciRows.map(renderLiveRow)}
                </TableBody>
              </Table>
            </TableContainer>
          )}
        </Box>
      )}
    </Root>
  );
}
