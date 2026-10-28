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
