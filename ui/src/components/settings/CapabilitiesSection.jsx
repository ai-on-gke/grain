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

import { useContext } from "react";
import CapabilityServersPanel from "../CapabilityServersPanel.jsx";
import CapabilityPolicyEditor from "../CapabilityPolicyEditor.jsx";
import ApiScopeSetsPanel from "../ApiScopeSetsPanel.jsx";
import SettingsCard, { SettingsReadOnlyContext } from "./SettingsCard.jsx";

// CapabilitiesSection is Settings → Capabilities: the capability-server
// entries (mcpServers), the capability policy and the agent API's scope
// sets.
export default function CapabilitiesSection({
  s,
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
  return (
    <>
      <SettingsCard title="Capability servers" anchor="capability-servers">
        <CapabilityServersPanel
          showError={effectiveShowError}
          readOnly={effectiveReadOnly}
          selected={item}
          onSelect={onItem}
        />
      </SettingsCard>
      <SettingsCard title="Capability policy" anchor="capability-policy">
        <CapabilityPolicyEditor
          showError={effectiveShowError}
          readOnly={effectiveReadOnly}
        />
      </SettingsCard>
      <SettingsCard title="API scope sets" anchor="api-scope-sets">
        <ApiScopeSetsPanel
          showError={effectiveShowError}
          readOnly={effectiveReadOnly}
        />
      </SettingsCard>
    </>
  );
}
