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
import CapabilityServersPanel from "../CapabilityServersPanel.jsx";
import CapabilityPolicyEditor from "../CapabilityPolicyEditor.jsx";
import ApiScopeSetsPanel from "../ApiScopeSetsPanel.jsx";
import DeclarationAcceptancesPanel from "../DeclarationAcceptancesPanel.jsx";
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
      {/* Accepting a declaration takes the scopes its set needs, which
          the server asks for itself (POST /api/declarations/accept), so
          the section's capabilities:admin refusal does not disable it. */}
      <SettingsRoleRefusalContext.Provider value="">
        <SettingsCard title="Repository declarations" anchor="declarations">
          <DeclarationAcceptancesPanel showError={effectiveShowError} />
        </SettingsCard>
      </SettingsRoleRefusalContext.Provider>
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
