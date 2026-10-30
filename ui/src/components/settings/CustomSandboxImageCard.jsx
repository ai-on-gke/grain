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

import { Alert, Box, Button, Link, TextField, Typography } from "@mui/material";
import SettingsCard from "./SettingsCard.jsx";

export const SANDBOX_CONTRACT_DOC =
  "https://github.com/ai-on-gke/grain/blob/main/docs/sandbox-image-contract.md";

const VM_BACKENDS = new Set(["granule", "kubernetes-granule"]);

const mono = {
  fontFamily:
    "ui-monospace, SFMono-Regular, Menlo, Consolas, 'Liberation Mono', monospace",
  fontSize: "0.8125rem",
};

// CustomSandboxImageCard is Settings → Sandboxes → Custom sandbox image:
// the OCI reference new sandboxes are created from in place of the image
// this build is stamped for (model.Config.SandboxImage), with the base
// image a custom one is built FROM and the sandbox contract it is checked
// against (pkg/sandboximage). `s` is the page's useSettingsSection().
export default function CustomSandboxImageCard({ s }) {
  const { settings, onTextBlur, onEnterKey } = s;
  const backend = settings.sandboxImageBackend || settings.sandboxBackend || "";
  const vm = VM_BACKENDS.has(backend);
  const contract = settings.sandboxContract || {};
  const current = settings.sandboxContractVersion ?? contract.contract ?? 1;
  const minimum =
    settings.sandboxMinimumContractVersion ?? contract.minimumContract ?? 1;
  const granuleContract =
    settings.sandboxGranuleContractVersion ?? contract.granuleContract ?? 1;
  const contractLabel = contract.contractLabel || "dev.grain.sandbox.contract";
  const granuleLabel =
    contract.granuleContractLabel || "dev.granule.guest.contract";
  const baseImage =
    settings.sandboxBaseImage ||
    (vm
      ? contract.guestBaseImage || "ghcr.io/bwsalmon/grain/guest-base:latest"
      : contract.k8sSandboxBaseImage ||
        "ghcr.io/bwsalmon/grain/k8s-sandbox-base:latest");
  const defaultImage = settings.sandboxImageDefault || "";
  const range = minimum === current ? `v${current}` : `v${minimum}–v${current}`;
  const check = settings.sandboxImageCheck;

  return (
    <SettingsCard title="Custom sandbox image" anchor="custom-sandbox-image">
      <TextField
        key={`sandboxImage-${settings.sandboxImage || ""}`}
        name="sandboxImage"
        data-setting-id="sandboxImage"
        label="Custom sandbox image (OCI reference)"
        placeholder={defaultImage}
        helperText={
          defaultImage
            ? `Empty uses the image this build is stamped for (${defaultImage}). A change applies to the next sandbox created; running sandboxes keep theirs.`
            : "Empty uses the image this build is stamped for. A change applies to the next sandbox created; running sandboxes keep theirs."
        }
        defaultValue={settings.sandboxImage || ""}
        onBlur={onTextBlur("sandboxImage")}
        onKeyDown={onEnterKey}
        autoComplete="off"
        spellCheck={false}
        fullWidth
        margin="normal"
        inputProps={{ style: mono }}
        InputProps={
          settings.sandboxImage
            ? {
                endAdornment: (
                  <Button
                    size="small"
                    onMouseDown={(evt) => evt.preventDefault()}
                    onClick={() => s.saveChoice?.({ sandboxImage: "" })}
                    data-testid="sandboxImage-reset"
                    sx={{
                      whiteSpace: "nowrap",
                      textTransform: "none",
                      flexShrink: 0,
                    }}
                  >
                    Reset to default
                  </Button>
                ),
              }
            : undefined
        }
      />
      {settings.sandboxImage && settings.sandboxImageWarning ? (
        <Alert
          severity="warning"
          data-testid="sandbox-image-warning"
          sx={{ mt: 1 }}
        >
          {settings.sandboxImageWarning}
        </Alert>
      ) : (
        settings.sandboxImage &&
        check?.verified && (
          <Alert
            severity="success"
            data-testid="sandbox-image-contract-ok"
            sx={{ mt: 1 }}
          >
            {check.contract?.summary ||
              `${contractLabel} = ${current} (current)`}
          </Alert>
        )
      )}

      <Typography variant="subtitle2" sx={{ mt: 2 }}>
        Build it FROM grain's minimal base image
      </Typography>
      <Typography
        variant="body2"
        color="text.secondary"
        data-testid="sandbox-image-inherits"
      >
        {vm
          ? "Granule VM backends boot a packed guest. guest-base already satisfies the entire sandbox contract: user debian (uid 1000) with the /home/agent symlink, git, curl, ca-certificates, python3-minimal for port forwarding, passwordless sudo, systemd, grain-connect and the contract version stamps."
          : "Container and Kubernetes pod backends run a container image. k8s-sandbox-base already satisfies the entire sandbox contract: user debian (uid 1000) with the /home/agent symlink, git, curl, ca-certificates, python3-minimal for port forwarding and the contract version stamps."}
      </Typography>
      <Typography
        variant="body2"
        color="text.secondary"
        data-testid="sandbox-image-todo"
        sx={{ mt: 1 }}
      >
        All your image has to do is start FROM it and install the packages and
        toolchains your tasks need. Leave what the base image carries in place.
      </Typography>
      <Box
        component="pre"
        data-testid="sandbox-image-dockerfile"
        // A tab stop and a name, so the keyboard can scroll the lines
        // that run past a phone's edge.
        tabIndex={0}
        role="region"
        aria-label="Example Dockerfile"
        sx={{
          ...mono,
          mt: 1,
          mb: 0,
          p: 1.5,
          borderRadius: 1,
          bgcolor: "action.hover",
          overflowX: "auto",
          whiteSpace: "pre",
        }}
      >
        {`FROM ${baseImage}\nRUN apt-get update \\\n && apt-get install -y --no-install-recommends <your packages> \\\n && rm -rf /var/lib/apt/lists/*`}
      </Box>

      <Typography variant="subtitle2" sx={{ mt: 2 }}>
        Sandbox contract
      </Typography>
      <Box
        component="ul"
        data-testid="sandbox-contract-versions"
        sx={{ mt: 0.5, mb: 0, pl: 2.5 }}
      >
        <li>
          <Box component="code" sx={mono}>
            {contractLabel} = {current}
          </Box>{" "}
          <Typography component="span" variant="body2" color="text.secondary">
            (this grain drives images at {range})
          </Typography>
        </li>
        {vm && (
          <li>
            <Box component="code" sx={mono}>
              {granuleLabel} = {granuleContract}
            </Box>{" "}
            <Typography component="span" variant="body2" color="text.secondary">
              (Granule's own guest contract, carried by guest-base)
            </Typography>
          </li>
        )}
      </Box>
      <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
        A custom image does not need rebuilding on every grain upgrade. The base
        image stamps the contract version it was built at, and you only need to
        rebuild when that version falls below what grain supports. That happens
        when grain changes what it relies on inside a sandbox: the user and home
        layout, the boundary tools, grain-connect, or Granule's guest contract.
        This pane and the daemon log warn you when it does.
      </Typography>
      <Typography
        variant="body2"
        data-testid="sandbox-contract-reference"
        sx={{ mt: 1.5 }}
      >
        <Link href={SANDBOX_CONTRACT_DOC} target="_blank" rel="noreferrer">
          docs/sandbox-image-contract.md
        </Link>{" "}
        lists every property the base image provides, which is what{" "}
        <Box component="code" sx={mono}>
          grain sandbox-image check --probe
        </Box>{" "}
        verifies. To check an image before you save it here, run{" "}
        <Box component="code" sx={mono}>
          grain sandbox-image check &lt;image&gt;
        </Box>
        .
      </Typography>
    </SettingsCard>
  );
}
