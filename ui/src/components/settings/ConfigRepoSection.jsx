import StateRepoPanel from "../StateRepoPanel.jsx";
import SettingsCard from "./SettingsCard.jsx";

// ConfigRepoSection is Settings → Config repo
// (docs/design/settings-panel.md §3.2): the repository this deployment's
// state is exported to and read back from (StateRepoPanel). It was the
// System overlay's state tab.
export default function ConfigRepoSection({
  config,
  showError,
  onStatusChange,
}) {
  return (
    <SettingsCard label="Config repo" anchor="config-repo">
      <StateRepoPanel
        config={config}
        showError={showError}
        onStatusChange={onStatusChange}
      />
    </SettingsCard>
  );
}
