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

import {
  Alert,
  Box,
  Checkbox,
  FormControlLabel,
  TextField,
} from "@mui/material";
import SandboxCommitmentNote from "../SandboxCommitmentNote.jsx";
import CustomSandboxImageCard from "./CustomSandboxImageCard.jsx";
import DiskFloorNote from "../DiskFloorNote.jsx";
import { readNumberBox } from "../../numberBox.js";
import { settingLabel, settingMin } from "../../settingsPlan.js";
import SettingsCard, {
  DefaultableField,
  settingHints,
} from "./SettingsCard.jsx";

const SANDBOX_BACKEND_LABELS = {
  none: "Disabled (remote execution only)",
  host: "Host directories (no isolation)",
  container: "Local Docker containers",
  granule: "Granule virtual machines",
  kubernetes: "Kubernetes — container pod per run (no VM)",
  "kubernetes-granule": "Kubernetes — Granule VM per run",
};

const SANDBOX_BACKEND_ISOLATION = {
  none: "No local or cluster sandboxes are created; tasks run only on remote executors.",
  host: "Each run executes directly in a directory on the daemon machine with no container or VM isolation.",
  container:
    "Each run executes in an unprivileged local Docker container sharing the host kernel (no VM).",
  granule:
    "Each run boots an isolated Granule microVM with its own guest kernel on this host.",
  kubernetes:
    "Each run executes in a Kubernetes container pod scheduled on the cluster, sharing the node kernel (no VM).",
  "kubernetes-granule":
    "Each run boots a Granule microVM with its own guest kernel over /dev/kvm inside a privileged Kubernetes pod.",
};

// SandboxesSection is Settings → Sandboxes (docs/design/settings-panel.md
// §3.2). `s` is the page's useSettingsSection().
export default function SandboxesSection({ s }) {
  const {
    settings,
    saveChoice,
    onTextBlur,
    onIntBlur,
    onFloatBlur,
    onEnterKey,
    refuseBox,
    unchanged,
  } = s;
  const { restartRequired, restartHint, restartChip } = settingHints(settings);
  const buildVersion = settings.daemonBuildVersion || "dev";
  const backendOptions =
    Array.isArray(settings.sandboxBackendOptions) &&
    settings.sandboxBackendOptions.length > 0
      ? settings.sandboxBackendOptions
      : (settings.sandboxBackends || []).map((backend) => ({
          backend,
          available: true,
          reason: "",
        }));
  const isolationHelp = SANDBOX_BACKEND_ISOLATION[settings.sandboxBackend];
  const overcommit = {
    mode: settings.sandboxOvercommit?.mode || "fixed",
    cpuMin: settings.sandboxOvercommit?.cpuMin ?? 1.0,
    cpuMax: settings.sandboxOvercommit?.cpuMax ?? 6.0,
    memoryMin: settings.sandboxOvercommit?.memoryMin ?? 1.0,
    memoryMax: settings.sandboxOvercommit?.memoryMax ?? 2.0,
    cpuPressureTarget: settings.sandboxOvercommit?.cpuPressureTarget ?? 25.0,
    memoryPressureTarget:
      settings.sandboxOvercommit?.memoryPressureTarget ?? 5.0,
  };
  const liveBudget = settings.sandboxCommitment?.budget;
  const liveOvercommit = liveBudget?.overcommit;
  const isAutoOvercommit =
    overcommit.mode === "auto" || liveBudget?.mode === "auto";
  const effectiveCpuRatio =
    liveOvercommit?.cpuOvercommit ?? liveBudget?.cpuOvercommit;
  const effectiveMemoryRatio =
    liveOvercommit?.memoryOvercommit ?? liveBudget?.memoryOvercommit;

  // A refusal of one bound is reported against that bound's own box
  // (sandboxOvercommitCpuMin, ...), the key refuseBox and unchanged use
  // for it too, rather than against the mode the whole object is saved
  // under.
  const saveOvercommit = (patch) => {
    const [field] = Object.keys(patch);
    saveChoice(
      {
        sandboxOvercommit: {
          ...overcommit,
          ...patch,
        },
      },
      field && field !== "mode"
        ? {
            errorKey: `sandboxOvercommit${field[0].toUpperCase()}${field.slice(1)}`,
          }
        : undefined,
    );
  };

  // The auto controller's bounds are one JSON setting (sandboxOvercommit)
  // saved whole, so they cannot go through onFloatBlur; they are read the
  // way every other number box is, though (numberBox.js). These used to
  // parseFloat the box themselves and put a refused value back without a
  // word: -1 or 0 just reverted, 0.1 under a 0.25 minimum or 150% over a
  // 100% target went to the daemon, and a half-typed "1e" -- which the
  // browser reports as "" -- saved grain's default in its place
  // (docs/test-runs/2026-10-25-task-lifecycle-review-questions.md).
  const onOvercommitFloatBlur = (field, fallbackVal) => (evt) => {
    const key = `sandboxOvercommit${field[0].toUpperCase()}${field.slice(1)}`;
    const read = readNumberBox(evt.target, {
      min: settingMin(key),
      label: settingLabel(key),
    });
    if (read.error) {
      refuseBox(key, read.error);
      return;
    }
    const n = read.empty ? fallbackVal : read.value;
    if (n !== overcommit[field]) {
      saveOvercommit({ [field]: n });
    } else {
      evt.target.value = String(overcommit[field]);
      unchanged(key);
    }
  };
  return (
    <>
      <SettingsCard title="Backend and size" anchor="backend-and-size">
        {backendOptions.length > 0 && (
          <TextField
            select
            name="sandboxBackend"
            data-setting-id="sandboxBackend"
            label="Sandbox backend"
            value={settings.sandboxBackend || ""}
            onChange={(evt) => saveChoice({ sandboxBackend: evt.target.value })}
            helperText={restartHint(
              "sandboxBackend",
              isolationHelp || "How grain isolates every agent and CI run.",
            )}
            fullWidth
            margin="normal"
            SelectProps={{ native: true }}
            InputLabelProps={{ shrink: true }}
            InputProps={{ endAdornment: restartChip("sandboxBackend") }}
          >
            {!backendOptions.some(
              (opt) => opt.backend === settings.sandboxBackend,
            ) && <option value="" disabled />}
            {backendOptions.map((opt) => {
              const label = SANDBOX_BACKEND_LABELS[opt.backend] || opt.backend;
              return (
                <option
                  key={opt.backend}
                  value={opt.backend}
                  disabled={!opt.available}
                  title={!opt.available && opt.reason ? opt.reason : undefined}
                >
                  {!opt.available && opt.reason
                    ? `${label} — unavailable (${opt.reason})`
                    : label}
                </option>
              );
            })}
          </TextField>
        )}

        {settings.sandboxShapeIgnored && (
          <Alert severity="warning" sx={{ mb: 2 }}>
            Not in effect on this deployment: its sandboxes are directories on
            the daemon&apos;s own machine, and a host-directory sandbox has no
            CPU, memory or disk shape of its own -- a run here uses whatever the
            host has. These are still saved, and would size a granule-managed
            sandbox VM.
          </Alert>
        )}
        <TextField
          key={`cpus-${settings.sandboxCpus || ""}`}
          name="sandboxCpus"
          data-setting-id="sandboxCpus"
          label="Sandbox vCPUs"
          helperText="default vCPU count for a granule-managed sandbox VM. Overridable per task."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={
            settings.sandboxCpus ? String(settings.sandboxCpus) : ""
          }
          placeholder={
            settings.sandboxCpusDefault
              ? String(settings.sandboxCpusDefault)
              : undefined
          }
          onBlur={onIntBlur("sandboxCpus", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`memory-${settings.sandboxMemoryMb || ""}`}
          name="sandboxMemoryMb"
          data-setting-id="sandboxMemoryMb"
          label="Sandbox memory (MiB)"
          helperText="default guest memory, in MiB, for a granule-managed sandbox VM. Overridable per task."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={
            settings.sandboxMemoryMb ? String(settings.sandboxMemoryMb) : ""
          }
          placeholder={
            settings.sandboxMemoryMbDefault
              ? String(settings.sandboxMemoryMbDefault)
              : undefined
          }
          onBlur={onIntBlur("sandboxMemoryMb", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`disk-${settings.sandboxDiskGb || ""}`}
          name="sandboxDiskGb"
          data-setting-id="sandboxDiskGb"
          label="Sandbox disk (GiB)"
          helperText="default root disk size, in GiB, for a granule-managed sandbox VM. Overridable per task."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={
            settings.sandboxDiskGb ? String(settings.sandboxDiskGb) : ""
          }
          placeholder={
            settings.sandboxDiskGbDefault
              ? String(settings.sandboxDiskGbDefault)
              : undefined
          }
          onBlur={onIntBlur("sandboxDiskGb", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          select
          name="networkAccess"
          data-setting-id="networkAccess"
          label="Sandbox network"
          value={settings.networkAccess || "open"}
          onChange={(evt) => saveChoice({ networkAccess: evt.target.value })}
          helperText="how much of the internet a run's sandbox reaches unless its task says otherwise. A sandbox with no network still reaches this deployment's own git proxy. Overridable per task."
          fullWidth
          margin="normal"
          SelectProps={{ native: true }}
          InputLabelProps={{ shrink: true }}
        >
          {(settings.networkAccesses || ["open", "none"]).map((access) => (
            <option key={access} value={access}>
              {access === "none"
                ? "none (this deployment only)"
                : "egress (outbound internet only)"}
            </option>
          ))}
        </TextField>
        <TextField
          key={`sessionSandboxIdleTtl-${settings.sessionSandboxIdleTtl || ""}`}
          name="sessionSandboxIdleTtl"
          // Config-managed, unlike the rest of this section: refused by
          // the API while settings are locked (settings-mutability.spec.md D-1).
          disabled={Boolean(s.configLocked)}
          data-setting-id="sessionSandboxIdleTtl"
          label="Session sandbox idle TTL"
          helperText="how long an interactive chat or director session's sandbox (and in-process agent session) stays warm awaiting a reply after a turn ends before being released. A new message within this window continues in the same sandbox; the oldest idle session sandbox is evicted first under capacity pressure. Blank uses grain's default (30m); a negative duration (-1s) disables warm retention."
          defaultValue={settings.sessionSandboxIdleTtl || ""}
          placeholder={settings.sessionSandboxIdleTtlDefault || undefined}
          onBlur={onTextBlur("sessionSandboxIdleTtl")}
          onKeyDown={onEnterKey}
          autoComplete="off"
          fullWidth
          margin="normal"
        />
      </SettingsCard>
      <SettingsCard title="Sandbox budget" anchor="sandbox-budget">
        <TextField
          select
          name="sandboxOvercommit"
          data-setting-id="sandboxOvercommit"
          label="Overcommit mode"
          value={overcommit.mode}
          onChange={(evt) => saveOvercommit({ mode: evt.target.value })}
          helperText="fixed holds the configured CPU and memory ratios below; auto adjusts the effective ratios from host CPU and memory PSI within the bounds and targets below."
          fullWidth
          margin="normal"
          SelectProps={{ native: true }}
          InputLabelProps={{ shrink: true }}
        >
          <option value="fixed">fixed (hold configured ratios)</option>
          <option value="auto">auto (adjust from host pressure)</option>
        </TextField>
        {isAutoOvercommit &&
          (effectiveCpuRatio != null || effectiveMemoryRatio != null) && (
            <Alert
              severity="info"
              sx={{ my: 1 }}
              data-testid="auto-overcommit-effective"
            >
              Auto overcommit is active &mdash; live effective factors:{" "}
              {effectiveCpuRatio != null && (
                <>
                  <strong>
                    CPU {Number(effectiveCpuRatio).toFixed(2)}&times;
                  </strong>
                  {effectiveMemoryRatio != null ? ", " : ""}
                </>
              )}
              {effectiveMemoryRatio != null && (
                <strong>
                  memory {Number(effectiveMemoryRatio).toFixed(2)}&times;
                </strong>
              )}
              {liveOvercommit?.last && (
                <>
                  {" "}
                  (PSI: CPU{" "}
                  {Number(
                    liveOvercommit.last.cpuSome ??
                      liveOvercommit.last.cpuPressure ??
                      0,
                  ).toFixed(1)}
                  %, memory{" "}
                  {Number(
                    liveOvercommit.last.memorySome ??
                      liveOvercommit.last.memoryPressure ??
                      0,
                  ).toFixed(1)}
                  %)
                </>
              )}
              .
            </Alert>
          )}
        <TextField
          key={`cpuOvercommit-${settings.sandboxCpuOvercommit ?? ""}`}
          name="sandboxCpuOvercommit"
          data-setting-id="sandboxCpuOvercommit"
          label="CPU overcommit"
          helperText="how much more vCPU than the sandbox host has may be committed to sandboxes at once -- 2 promises a 28-core host 56 vCPU of agent and CI sandboxes. Some overcommitment is right: a run spends most of its wall time waiting on a model or a network rather than on a core. 0 leaves vCPU bounded by the run counts alone."
          type="number"
          inputProps={{ min: 0, step: 0.25 }}
          defaultValue={
            settings.sandboxCpuOvercommit === undefined
              ? ""
              : String(settings.sandboxCpuOvercommit)
          }
          onBlur={onFloatBlur("sandboxCpuOvercommit", {
            defaultVal: undefined,
            emptyVal: 0,
          })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`memOvercommit-${settings.sandboxMemoryOvercommit ?? ""}`}
          name="sandboxMemoryOvercommit"
          data-setting-id="sandboxMemoryOvercommit"
          label="Memory overcommit"
          helperText="how much more guest memory than the sandbox host has may be committed to sandboxes at once -- 1.5 on a 64 GiB host allows 96 GiB of sandbox memory between them. 0 leaves memory bounded by the run counts alone."
          type="number"
          inputProps={{ min: 0, step: 0.25 }}
          defaultValue={
            settings.sandboxMemoryOvercommit === undefined
              ? ""
              : String(settings.sandboxMemoryOvercommit)
          }
          onBlur={onFloatBlur("sandboxMemoryOvercommit", {
            defaultVal: undefined,
            emptyVal: 0,
          })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`diskOvercommit-${settings.sandboxDiskOvercommit ?? ""}`}
          name="sandboxDiskOvercommit"
          data-setting-id="sandboxDiskOvercommit"
          label="Disk overcommit"
          helperText={`how many times over the sandbox volume (or, on Kubernetes, the cluster's allocatable ephemeral storage), less the disk floor, sandbox disks may be promised -- a sandbox disk is a thin overlay a run mostly never writes. On Kubernetes this also scales each pod's ephemeral-storage request down while its limit stays the full disk. 0 leaves disk held by the floor alone. Default ${settings.sandboxDiskOvercommitDefault ?? 2}.`}
          type="number"
          inputProps={{ min: 0, step: 0.5 }}
          defaultValue={
            settings.sandboxDiskOvercommit === undefined
              ? ""
              : String(settings.sandboxDiskOvercommit)
          }
          onBlur={onFloatBlur("sandboxDiskOvercommit", {
            defaultVal: undefined,
            emptyVal: 0,
          })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <Box
          sx={{
            display: "grid",
            gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr" },
            gap: 2,
            mt: 1,
          }}
        >
          <TextField
            key={`cpuOvercommitMin-${overcommit.cpuMin}`}
            name="sandboxOvercommitCpuMin"
            data-setting-id="sandboxOvercommitCpuMin"
            label="Auto CPU minimum"
            helperText="Lower bound on effective CPU overcommit in auto mode (default 1.0x)."
            type="number"
            inputProps={{ min: 0.25, step: 0.25 }}
            defaultValue={String(overcommit.cpuMin)}
            onBlur={onOvercommitFloatBlur("cpuMin", 1.0)}
            onKeyDown={onEnterKey}
            fullWidth
            margin="none"
          />
          <TextField
            key={`cpuOvercommitMax-${overcommit.cpuMax}`}
            name="sandboxOvercommitCpuMax"
            data-setting-id="sandboxOvercommitCpuMax"
            label="Auto CPU maximum"
            helperText="Upper bound on effective CPU overcommit in auto mode (default 6.0x)."
            type="number"
            inputProps={{ min: 0.25, step: 0.25 }}
            defaultValue={String(overcommit.cpuMax)}
            onBlur={onOvercommitFloatBlur("cpuMax", 6.0)}
            onKeyDown={onEnterKey}
            fullWidth
            margin="none"
          />
          <TextField
            key={`memOvercommitMin-${overcommit.memoryMin}`}
            name="sandboxOvercommitMemoryMin"
            data-setting-id="sandboxOvercommitMemoryMin"
            label="Auto memory minimum"
            helperText="Lower bound on effective memory overcommit in auto mode (default 1.0x)."
            type="number"
            inputProps={{ min: 0.25, step: 0.05 }}
            defaultValue={String(overcommit.memoryMin)}
            onBlur={onOvercommitFloatBlur("memoryMin", 1.0)}
            onKeyDown={onEnterKey}
            fullWidth
            margin="none"
          />
          <TextField
            key={`memOvercommitMax-${overcommit.memoryMax}`}
            name="sandboxOvercommitMemoryMax"
            data-setting-id="sandboxOvercommitMemoryMax"
            label="Auto memory maximum"
            helperText="Upper bound on effective memory overcommit in auto mode (default 2.0x)."
            type="number"
            inputProps={{ min: 0.25, step: 0.05 }}
            defaultValue={String(overcommit.memoryMax)}
            onBlur={onOvercommitFloatBlur("memoryMax", 2.0)}
            onKeyDown={onEnterKey}
            fullWidth
            margin="none"
          />
          <TextField
            key={`cpuPressureTarget-${overcommit.cpuPressureTarget}`}
            name="sandboxOvercommitCpuPressureTarget"
            data-setting-id="sandboxOvercommitCpuPressureTarget"
            label="CPU pressure target (%)"
            helperText="CPU PSI some avg60 threshold before auto mode backs off (default 25%)."
            type="number"
            inputProps={{ min: 0.5, max: 100, step: 0.5 }}
            defaultValue={String(overcommit.cpuPressureTarget)}
            onBlur={onOvercommitFloatBlur("cpuPressureTarget", 25.0)}
            onKeyDown={onEnterKey}
            fullWidth
            margin="none"
          />
          <TextField
            key={`memPressureTarget-${overcommit.memoryPressureTarget}`}
            name="sandboxOvercommitMemoryPressureTarget"
            data-setting-id="sandboxOvercommitMemoryPressureTarget"
            label="Memory pressure target (%)"
            helperText="Memory PSI some avg60 threshold before auto mode backs off (default 5%)."
            type="number"
            inputProps={{ min: 0.5, max: 100, step: 0.5 }}
            defaultValue={String(overcommit.memoryPressureTarget)}
            onBlur={onOvercommitFloatBlur("memoryPressureTarget", 5.0)}
            onKeyDown={onEnterKey}
            fullWidth
            margin="none"
          />
        </Box>
        <TextField
          key={`memReserve-${settings.sandboxMemoryReserveMb ?? ""}`}
          name="sandboxMemoryReserveMb"
          data-setting-id="sandboxMemoryReserveMb"
          label="Host memory reserve (MiB)"
          helperText="memory left uncommitted for the host's own processes and page cache. Blank uses grain's default (an eighth of host RAM, never less than 2 GiB); 0 keeps nothing back."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          // A negative reserve is grain's own (model.ReserveMemoryDefault)
          // and 0 is a reserve of nothing, so blank stands for the
          // negative one rather than for 0.
          defaultValue={
            settings.sandboxMemoryReserveMb === undefined ||
            settings.sandboxMemoryReserveMb < 0
              ? ""
              : String(settings.sandboxMemoryReserveMb)
          }
          placeholder={
            settings.sandboxMemoryReserveMbDefault
              ? String(settings.sandboxMemoryReserveMbDefault)
              : undefined
          }
          onBlur={onIntBlur("sandboxMemoryReserveMb", {
            defaultVal: -1,
            emptyVal: -1,
          })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`diskFloorShare-${settings.sandboxDiskFloorShare ?? ""}`}
          name="sandboxDiskFloorShare"
          data-setting-id="sandboxDiskFloorShare"
          label="Disk floor (share of each volume)"
          helperText="how much of each volume this deployment writes to is kept free: a new CI job is held and an agent dispatch deferred while free space is under it, because a sandbox's disk grows as it is written rather than being taken out of the volume when the sandbox is made. A share, so 0.1 is a tenth. 0 with no minimum holds nothing back at all."
          type="number"
          inputProps={{ min: 0, max: 1, step: 0.01 }}
          defaultValue={
            settings.sandboxDiskFloorShare === undefined
              ? ""
              : String(settings.sandboxDiskFloorShare)
          }
          onBlur={onFloatBlur("sandboxDiskFloorShare", {
            defaultVal: undefined,
            emptyVal: 0,
          })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`diskFloorMin-${settings.sandboxDiskFloorMinMb ?? ""}`}
          name="sandboxDiskFloorMinMb"
          data-setting-id="sandboxDiskFloorMinMb"
          label="Disk floor minimum (MiB)"
          helperText="the least that floor is ever worth, whatever the share comes to on a small volume -- a tenth of a 20 GB store disk is 2 GB, which is not enough room to notice a runaway write in."
          type="number"
          inputProps={{ min: 0, step: 1024 }}
          defaultValue={
            settings.sandboxDiskFloorMinMb === undefined
              ? ""
              : String(settings.sandboxDiskFloorMinMb)
          }
          placeholder={
            settings.sandboxDiskFloorMinMbDefault
              ? String(settings.sandboxDiskFloorMinMbDefault)
              : undefined
          }
          onBlur={onIntBlur("sandboxDiskFloorMinMb", {
            defaultVal: undefined,
            emptyVal: 0,
          })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />

        <DiskFloorNote settings={settings} sx={{ mt: 1 }} />

        {!settings.sandboxCommitment?.uncapped && (
          <SandboxCommitmentNote
            commitment={settings.sandboxCommitment}
            sx={{ mt: 1 }}
          />
        )}
      </SettingsCard>
      <SettingsCard title="Kubernetes" anchor="kubernetes">
        {(settings.k8sSandboxImageWarning ||
          settings.k8sGranuleImageWarning) && (
          <Alert
            severity="warning"
            data-testid="k8s-image-warning"
            sx={{ mt: 1, mb: 1 }}
          >
            {[settings.k8sSandboxImageWarning, settings.k8sGranuleImageWarning]
              .filter(Boolean)
              .join(" ")}
          </Alert>
        )}
        <DefaultableField
          s={s}
          name="k8sNamespace"
          label="Kubernetes namespace"
          helperText={restartHint(
            "k8sNamespace",
            "Namespace sandbox pods are created in.",
          )}
          defaultValue={settings.k8sNamespaceDefault || "grain"}
          endAdornment={restartChip("k8sNamespace")}
        />
        <DefaultableField
          s={s}
          name="k8sSandboxImage"
          label="Kubernetes sandbox pod image"
          helperText={restartHint(
            "k8sSandboxImage",
            `Container image every plain Kubernetes sandbox pod runs (matches this daemon's build ${buildVersion}).`,
          )}
          defaultValue={
            settings.k8sSandboxImageDefault ||
            "ghcr.io/bwsalmon/grain/k8s-sandbox:latest"
          }
          endAdornment={restartChip("k8sSandboxImage")}
        />
        <DefaultableField
          s={s}
          name="k8sGranuleImage"
          label="Kubernetes Granule VM pod image"
          helperText={restartHint(
            "k8sGranuleImage",
            `Packed guest image every Kubernetes Granule VM pod boots (matches this daemon's build ${buildVersion}).`,
          )}
          defaultValue={
            settings.k8sGranuleImageDefault ||
            "ghcr.io/bwsalmon/grain/guest:latest"
          }
          endAdornment={restartChip("k8sGranuleImage")}
        />
        <DefaultableField
          s={s}
          name="k8sWorkspace"
          label="Kubernetes workspace directory"
          helperText={restartHint(
            "k8sWorkspace",
            "Working directory inside each Kubernetes sandbox pod or VM.",
          )}
          defaultValue={
            settings.k8sWorkspaceDefault ||
            (settings.sandboxBackend === "kubernetes-granule"
              ? "/home/debian"
              : "/home/agent")
          }
          endAdornment={restartChip("k8sWorkspace")}
        />
        <TextField
          key={`k8sGitProxyHost-${settings.k8sGitProxyHost || ""}`}
          name="k8sGitProxyHost"
          data-setting-id="k8sGitProxyHost"
          label="Kubernetes git & model proxy host"
          helperText={restartHint(
            "k8sGitProxyHost",
            "Host address (no port) sandbox pods reach this daemon's git and model proxies at. Defaults to $GRAIN_POD_IP in-cluster.",
          )}
          defaultValue={settings.k8sGitProxyHost || ""}
          onBlur={onTextBlur("k8sGitProxyHost")}
          onKeyDown={onEnterKey}
          autoComplete="off"
          fullWidth
          margin="normal"
          InputProps={{ endAdornment: restartChip("k8sGitProxyHost") }}
        />
        <TextField
          key={`k8sSandboxService-${settings.k8sSandboxService || ""}`}
          name="k8sSandboxService"
          data-setting-id="k8sSandboxService"
          label="Kubernetes sandbox Service"
          helperText={restartHint(
            "k8sSandboxService",
            "Optional ClusterIP Service fronting the git and model proxies so sandboxes survive a daemon rollout.",
          )}
          defaultValue={settings.k8sSandboxService || ""}
          onBlur={onTextBlur("k8sSandboxService")}
          onKeyDown={onEnterKey}
          autoComplete="off"
          fullWidth
          margin="normal"
          InputProps={{ endAdornment: restartChip("k8sSandboxService") }}
        />
        <TextField
          key={`k8sNodeSelector-${(settings.k8sNodeSelector || []).join(",")}`}
          name="k8sNodeSelector"
          data-setting-id="k8sNodeSelector"
          label="Kubernetes node selector"
          helperText={restartHint(
            "k8sNodeSelector",
            "Comma-separated key=value nodeSelector entries for sandbox pods.",
          )}
          defaultValue={(settings.k8sNodeSelector || []).join(", ")}
          placeholder="pool=sandboxes"
          onBlur={(evt) => {
            const next = evt.target.value
              .split(",")
              .map((entry) => entry.trim())
              .filter(Boolean);
            const current = settings.k8sNodeSelector || [];
            if (next.join(",") !== current.join(",")) {
              saveChoice({ k8sNodeSelector: next });
            }
          }}
          onKeyDown={onEnterKey}
          autoComplete="off"
          fullWidth
          margin="normal"
          InputProps={{ endAdornment: restartChip("k8sNodeSelector") }}
        />
        <TextField
          key={`k8sServiceAccount-${settings.k8sServiceAccount || ""}`}
          name="k8sServiceAccount"
          data-setting-id="k8sServiceAccount"
          label="Kubernetes sandbox ServiceAccount"
          helperText={restartHint(
            "k8sServiceAccount",
            "Optional ServiceAccount for sandbox pods.",
          )}
          defaultValue={settings.k8sServiceAccount || ""}
          onBlur={onTextBlur("k8sServiceAccount")}
          onKeyDown={onEnterKey}
          autoComplete="off"
          fullWidth
          margin="normal"
          InputProps={{ endAdornment: restartChip("k8sServiceAccount") }}
        />
        {/* Plain text boxes rather than DefaultableFields: an empty
            priority class is "set none on these pods", not "use the
            default", so blank has to stay blank. */}
        {[
          {
            key: "k8sPriorityClassInteractive",
            label: "Priority class for interactive sessions",
            hint: "Highest: an interactive session's pod may preempt CI pods on a full cluster.",
          },
          {
            key: "k8sPriorityClassAgent",
            label: "Priority class for agent runs",
            hint: "Agent runs, base-branch repairs and persistent sandboxes.",
          },
          {
            key: "k8sPriorityClassCi",
            label: "Priority class for CI jobs",
            hint: "Lowest: a preempted CI job is retried, not failed.",
          },
        ].map(({ key, label, hint }) => (
          <TextField
            key={`${key}-${settings[key] ?? ""}`}
            name={key}
            data-setting-id={key}
            label={label}
            helperText={restartHint(
              key,
              `${hint} Empty sets no priority class. deploy/kubernetes/ defines ${settings[`${key}Default`] || "the default"}; a class the cluster lacks is created if it is grain's own, or else left off the pods with an alert.`,
            )}
            defaultValue={settings[key] ?? ""}
            placeholder="none"
            onBlur={onTextBlur(key)}
            onKeyDown={onEnterKey}
            autoComplete="off"
            fullWidth
            margin="normal"
            InputProps={{ endAdornment: restartChip(key) }}
          />
        ))}
        <DefaultableField
          s={s}
          name="k8sDeployment"
          label="Kubernetes Deployment"
          helperText={restartHint(
            "k8sDeployment",
            "Kubernetes Deployment running grain itself, repointed on self-upgrade.",
          )}
          defaultValue={settings.k8sDeploymentDefault || "grain"}
          endAdornment={restartChip("k8sDeployment")}
        />
        <DefaultableField
          s={s}
          name="k8sContainer"
          label="Kubernetes container"
          helperText={restartHint(
            "k8sContainer",
            "Container name within the Kubernetes Deployment for self-upgrade.",
          )}
          defaultValue={settings.k8sContainerDefault || "grain"}
          endAdornment={restartChip("k8sContainer")}
        />
        <DefaultableField
          s={s}
          name="k8sKubectl"
          label="Kubectl binary"
          helperText={restartHint(
            "k8sKubectl",
            "Optional path to kubectl binary; empty finds kubectl on PATH.",
          )}
          defaultValue={settings.k8sKubectlDefault || "kubectl"}
          endAdornment={restartChip("k8sKubectl")}
        />
        <FormControlLabel
          control={
            <Checkbox
              name="k8sPrivileged"
              data-setting-id="k8sPrivileged"
              checked={!!settings.k8sPrivileged}
              onChange={(evt) =>
                saveChoice({ k8sPrivileged: evt.target.checked })
              }
            />
          }
          label={
            <>
              Run Kubernetes sandbox containers privileged
              {restartChip("k8sPrivileged")}
              {restartRequired.has("k8sPrivileged") && (
                <span className="hint">
                  {" "}
                  {restartHint("k8sPrivileged", "")}
                </span>
              )}
            </>
          }
          sx={{ display: "flex", mt: 1 }}
        />
        <FormControlLabel
          control={
            <Checkbox
              name="k8sNetworkPolicy"
              data-setting-id="k8sNetworkPolicy"
              checked={!!settings.k8sNetworkPolicy}
              onChange={(evt) =>
                saveChoice({ k8sNetworkPolicy: evt.target.checked })
              }
            />
          }
          label={
            <>
              Cluster CNI enforces NetworkPolicy for network: none runs
              {restartChip("k8sNetworkPolicy")}
              {restartRequired.has("k8sNetworkPolicy") && (
                <span className="hint">
                  {" "}
                  {restartHint("k8sNetworkPolicy", "")}
                </span>
              )}
            </>
          }
          sx={{ display: "flex", mt: 1 }}
        />
      </SettingsCard>
      <CustomSandboxImageCard s={s} />
    </>
  );
}
