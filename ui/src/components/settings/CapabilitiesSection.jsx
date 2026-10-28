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
