import { Box, Button, Typography } from "@mui/material";
import SettingsCard from "./SettingsCard.jsx";

// actorName is the server-reported actor (GET /api/config's actor) as a
// person reads it, without its "user:" kind prefix.
export function actorName(config) {
  const actor = config?.actor?.id || config?.actor || "";
  return typeof actor === "string" ? actor.replace(/^user:/, "") : "";
}

// AccessSection is Settings → Access: how this browser is signed in
// (docs/design/settings-panel.md §3.2). grain has no users (§6): the access
// code is the one credential, so the only name shown is the actor the server
// reports for an IAP sign-in.
export default function AccessSection({ config, onLogout }) {
  const viaIAP = (config?.authMethod || "") === "iap";
  return (
    <>
      <SettingsCard title="This browser" anchor="this-browser">
        {viaIAP ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
            Signed in through IAP as {actorName(config)}. IAP manages this
            sign-in; sign out of your Google account to end it.
          </Typography>
        ) : (
          <Box sx={{ mt: 1 }}>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
              Signed in with the access code.
            </Typography>
            <Button
              variant="outlined"
              color="error"
              size="small"
              onClick={onLogout}
            >
              Log out
            </Button>
          </Box>
        )}
      </SettingsCard>
    </>
  );
}
