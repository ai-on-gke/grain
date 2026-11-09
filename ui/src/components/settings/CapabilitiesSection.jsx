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

import { useCallback, useContext, useState } from "react";
import { Alert } from "@mui/material";
import CapabilityServersPanel from "../CapabilityServersPanel.jsx";
import CapabilityPolicyEditor from "../CapabilityPolicyEditor.jsx";
import ApiScopeSetsPanel from "../ApiScopeSetsPanel.jsx";
import SettingsCard, {
  SettingsReadOnlyContext,
  SettingsRoleRefusalContext,
} from "./SettingsCard.jsx";
import CredentialsSection from "./CredentialsSection.jsx";

// CapabilitiesSection is Settings → Capabilities: the capability-server
// entries (mcpServers), the capability policy and the agent API's scope
// sets, and the Deployment credentials card (CredentialsSection) that was
// Settings → Legacy capabilities: the GitHub host, the GCP settings and
// the stored secrets, claimedSecrets naming the ones grain itself reads.
export default function CapabilitiesSection({
  s,
  claimedSecrets,
  showError,
  readOnly: readOnlyProp,
  item,
  onItem,
}) {
  const contextReadOnly = useContext(SettingsReadOnlyContext);
  const effectiveShowError = showError || s?.showError;
  const effectiveReadOnly = Boolean(
    readOnlyProp ?? s?.readOnly ?? contextReadOnly,
  );
  // Bumped whenever a capability server is added, configured, renamed or
  // removed, so the Deployment credentials card's secrets list -- which
  // lists each server's own secrets apart -- reads the store again
  // (2026-11-08 connections walkthrough: a removed server's secret stayed
  // listed, linking to the gone server, until a reload).
  const [secretsSeq, setSecretsSeq] = useState(0);
  const onSaved = s?.onSaved;
  const serversChanged = useCallback(() => {
    setSecretsSeq((n) => n + 1);
    onSaved?.();
  }, [onSaved]);
  return (
    <>
      <Alert
        severity="info"
        data-testid="gcp-key-deprecation-notice"
        sx={{ mb: 2 }}
      >
        Deprecated: bare <code>gcp-key</code> and{" "}
        <code>gcp-key:&lt;name&gt;</code> in-process grants were retired onto{" "}
        <code>https://grain.dev/capabilities/gcp#gcp-key</code> permission sets.
        They remain readable as aliases for one release; update{" "}
        <code>.grain</code> declarations and external configs to{" "}
        <code>gcp#gcp-key</code>.
      </Alert>
      <Alert
        severity="info"
        data-testid="model-proxy-deprecation-notice"
        sx={{ mb: 2 }}
      >
        Deprecated: the in-process <code>claude-proxy</code>,{" "}
        <code>openai-proxy</code> and <code>gemini-proxy</code> grants were
        retired onto{" "}
        <code>
          https://grain.dev/capabilities/model-proxy#model-proxy {"{vendor}"}
        </code>
        , which forwards with the same team connection. They remain readable as
        aliases for one release; update <code>.grain</code> declarations and
        external configs to <code>model-proxy#model-proxy</code>.
      </Alert>
      <Alert
        severity="info"
        data-testid="gemini-key-deprecation-notice"
        sx={{ mb: 2 }}
      >
        Deprecated: the in-process <code>gemini-key</code> grant was retired
        onto <code>https://grain.dev/capabilities/gcp#gemini-key</code>, which
        mints with the same <code>gcp-key-minter</code> secret. It remains
        readable as an alias for one release; update <code>.grain</code>{" "}
        declarations and external configs to <code>gcp#gemini-key</code>.
      </Alert>
      <Alert
        severity="info"
        data-testid="release-deprecation-notice"
        sx={{ mb: 2, overflowWrap: "anywhere" }}
      >
        Deprecated: the in-process <code>image-signing</code>,{" "}
        <code>registry-push</code> and <code>package-publish</code> grants were
        retired onto the in-process{" "}
        <code>https://grain.dev/capabilities/release</code> server&apos;s
        methods of the same names, which place the same files on the same CI
        events. They remain readable as aliases for one release; update{" "}
        <code>.grain</code> declarations, repository CI capabilities and
        external configs to <code>release#&lt;method&gt;</code>.
      </Alert>
      <Alert
        severity="info"
        data-testid="grain-ci-deprecation-notice"
        sx={{ mb: 2, overflowWrap: "anywhere" }}
      >
        Deprecated: the in-process <code>grain-ci</code> grant was retired onto{" "}
        <code>https://grain.dev/capabilities/grain#ci</code>, which a
        repository&apos;s CI Mode still grants. It remains readable as an alias
        for one release; update <code>.grain</code> declarations and external
        configs to <code>grain#ci</code>.
      </Alert>
      <Alert
        severity="info"
        data-testid="bootstrap-playbooks-deprecation-notice"
        sx={{ mb: 2, overflowWrap: "anywhere" }}
      >
        Deprecated: the in-process <code>bootstrap-playbooks</code> grant was
        retired onto{" "}
        <code>https://grain.dev/capabilities/config-map#config-map</code>{" "}
        <code>{'{"config_maps":["bootstrap"]}'}</code>, which places the same
        embedded runbooks in <code>/home/debian/.config-map/bootstrap</code>. It
        remains readable as an alias for one release; update <code>.grain</code>{" "}
        declarations and external configs to <code>config-map#config-map</code>.
      </Alert>
      <Alert
        severity="info"
        data-testid="github-sandbox-deprecation-notice"
        sx={{ mb: 2, overflowWrap: "anywhere" }}
      >
        Deprecated: the in-process <code>github-sandbox</code> grant was retired
        onto <code>https://grain.dev/capabilities/github#sandbox-repo</code>,
        which mints ephemeral repositories with the same{" "}
        <code>github-app/app-id</code> and <code>github-app/private-key</code>{" "}
        secrets. It remains readable as an alias for one release; update{" "}
        <code>.grain</code> declarations, repository CI capabilities and
        external configs to <code>github#sandbox-repo</code>.
      </Alert>
      {/* The servers panel draws its own read-only form (readOnly), and
          keeps its list clickable so one server's page can still be
          opened; the card's disabled fieldset would stop that too. */}
      <SettingsCard
        title="Capability servers"
        anchor="capability-servers"
        allowInExternalMode
      >
        <CapabilityServersPanel
          showError={effectiveShowError}
          readOnly={effectiveReadOnly}
          selected={item}
          onSelect={onItem}
          onChanged={serversChanged}
        />
      </SettingsCard>
      <SettingsCard title="Capability policy" anchor="capability-policy">
        <CapabilityPolicyEditor
          showError={effectiveShowError}
          readOnly={effectiveReadOnly}
          onSaved={onSaved}
        />
      </SettingsCard>
      <SettingsCard title="API scope sets" anchor="api-scope-sets">
        <ApiScopeSetsPanel
          showError={effectiveShowError}
          readOnly={effectiveReadOnly}
          onSaved={onSaved}
        />
      </SettingsCard>
      {/* The Deployment credentials card writes through PUT /api/settings
          and /api/secrets, not the capabilities:admin routes above, so the
          section's refusal does not reach it: it holds its fields to
          settings:write and its secrets to secrets:write itself
          (2026-11-08 deployment credentials walkthrough, F5). */}
      {s?.settings && (
        <SettingsRoleRefusalContext.Provider value="">
          <CredentialsSection
            s={s}
            claimedSecrets={claimedSecrets}
            secretsSeq={secretsSeq}
          />
        </SettingsRoleRefusalContext.Provider>
      )}
    </>
  );
}
