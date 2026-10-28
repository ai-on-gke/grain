import { useState } from "react";
import {
  Box,
  Button,
  FormControlLabel,
  Radio,
  RadioGroup,
  TextField,
  Typography,
} from "@mui/material";
import CustomizeSidebarOverlay from "../CustomizeSidebarOverlay.jsx";
import SettingsCard from "./SettingsCard.jsx";
import { useThemeMode } from "../../ThemeModeContext.jsx";
import { useTimelineOrder } from "../../TimelineOrderContext.jsx";
import { usePreferences } from "../../PreferencesContext.jsx";
import { browserTimeZone, timeZoneHelp, timeZoneOptions } from "../../time.js";

// PreferencesSection is Settings → Preferences: this browser's own
// choices, which were the old Settings overlay's (SettingsOverlay.jsx).
// Each saves the moment it is picked, into this browser alone.
export default function PreferencesSection({
  sessions = [],
  boards = [],
  deploymentTimeZone = "",
}) {
  const { timeZone: userTimeZone, setTimeZone: setUserTimeZone } =
    usePreferences();
  const browserZone = browserTimeZone();
  const [showCustomizePinned, setShowCustomizePinned] = useState(false);
  const { mode: themeMode, setMode: setThemeMode } = useThemeMode();
  const { order: timelineOrder, setOrder: setTimelineOrder } =
    useTimelineOrder();
  return (
    <>
      <SettingsCard title="Appearance" anchor="appearance">
        <RadioGroup
          row
          aria-label="Appearance"
          value={themeMode}
          onChange={(evt) => setThemeMode(evt.target.value)}
        >
          <FormControlLabel value="auto" control={<Radio />} label="Auto" />
          <FormControlLabel value="light" control={<Radio />} label="Light" />
          <FormControlLabel value="dark" control={<Radio />} label="Dark" />
        </RadioGroup>
      </SettingsCard>
      <SettingsCard title="Time zone" anchor="time-zone">
        <TextField
          select
          SelectProps={{ native: true }}
          InputLabelProps={{ shrink: true }}
          name="userTimeZone"
          label="Time zone"
          helperText={
            userTimeZone
              ? timeZoneHelp(userTimeZone)
              : deploymentTimeZone
                ? `Times here follow this deployment's clock, ${deploymentTimeZone}, until this browser picks its own.`
                : "Times here follow this browser's own clock until it picks one."
          }
          value={userTimeZone || ""}
          onChange={(evt) => setUserTimeZone(evt.target.value)}
          fullWidth
          margin="normal"
        >
          <option value="">
            {deploymentTimeZone
              ? `Deployment default (${deploymentTimeZone})`
              : `Browser default (${browserZone})`}
          </option>
          {timeZoneOptions(userTimeZone).map((zone) => (
            <option key={zone} value={zone}>
              {zone}
              {zone === browserZone ? " (this browser)" : ""}
            </option>
          ))}
        </TextField>
      </SettingsCard>
      <SettingsCard title="Task timeline" anchor="timeline">
        <RadioGroup
          row
          aria-label="Task timeline"
          value={timelineOrder}
          onChange={(evt) => setTimelineOrder(evt.target.value)}
        >
          <FormControlLabel
            value="newest"
            control={<Radio />}
            label="Newest first"
          />
          <FormControlLabel
            value="oldest"
            control={<Radio />}
            label="Oldest first"
          />
        </RadioGroup>
      </SettingsCard>
      <SettingsCard title="Sidebar" anchor="pinned">
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
          Choose which sections the left panel shows and which shortcuts are
          pinned at its top.
        </Typography>
        <Box>
          <Button
            variant="outlined"
            size="small"
            onClick={() => setShowCustomizePinned(true)}
          >
            Customize sidebar
          </Button>
        </Box>
      </SettingsCard>
      {showCustomizePinned && (
        <CustomizeSidebarOverlay
          open={showCustomizePinned}
          onClose={() => setShowCustomizePinned(false)}
          sessions={sessions}
          boards={boards}
        />
      )}
    </>
  );
}
