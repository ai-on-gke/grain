import { useEffect, useState } from "react";
import { Link, Typography } from "@mui/material";
import api from "../../api.js";
import ExecutorsPanel from "../ExecutorsPanel.jsx";
import SettingsCard from "./SettingsCard.jsx";

// ExecutorsSection is Settings → Executors: the join tokens, which are a
// setting. Which executors are connected is status, and is on System →
// Sandboxes (docs/design/settings-panel.md §2.1).
export default function ExecutorsSection({ showError, onOpenSystem }) {
  const [connected, setConnected] = useState(null);
  useEffect(() => {
    let live = true;
    api("/api/executors")
      .then((list) => live && setConnected((list || []).length))
      .catch(() => live && setConnected(null));
    return () => {
      live = false;
    };
  }, []);
  return (
    <SettingsCard title="Executor join tokens" anchor="executors">
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        {connected === null ? "" : `${connected} connected · `}
        <Link
          component="button"
          type="button"
          onClick={() => onOpenSystem?.("sandboxes")}
          data-testid="executors-system-link"
        >
          see System → Sandboxes
        </Link>
      </Typography>
      <ExecutorsPanel showError={showError} />
    </SettingsCard>
  );
}
