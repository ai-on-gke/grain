import { useCallback, useEffect, useRef } from "react";
import { knownRepos } from "../../state.js";
import { guardLeaving } from "../../unsavedEdits.js";
import MemoryPanel from "../MemoryPanel.jsx";
import SettingsCard from "./SettingsCard.jsx";

// MemorySection is Settings → Memory: what the memory agent has written
// down for this deployment's runs, its own layer and each repo's
// (model.MemoryFor). It was a page of its own in the rail (MemoryPage,
// grain/task-3100); it sits under Agents now, beside the standing
// instructions, because it is what runs are told.
//
// Every box saves on its own Save, so leaving it asks first when one
// holds an edit (unsavedEdits.js's guardLeaving, which App reads, and
// s.markDirty, which marks the section in SettingsPage's header and nav).
export default function MemorySection({ s, config, tasks, showError }) {
  const unsavedRef = useRef(null);
  const markDirty = s?.markDirty;
  const handleDirtyChange = useCallback(
    (dirty) => markDirty?.("Memory", dirty),
    [markDirty],
  );
  useEffect(() => guardLeaving(() => Boolean(unsavedRef.current?.())), []);
  useEffect(() => () => markDirty?.("Memory", false), [markDirty]);
  return (
    <SettingsCard label="Memory" anchor="memory">
      <div data-testid="memory-page">
        <MemoryPanel
          repos={knownRepos(config, tasks)}
          unsavedRef={unsavedRef}
          onDirtyChange={handleDirtyChange}
          showError={showError}
        />
      </div>
    </SettingsCard>
  );
}
