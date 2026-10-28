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

import { useState } from "react";
import AddIcon from "@mui/icons-material/Add";
import MenuIcon from "@mui/icons-material/Menu";
import {
  AppBar,
  Badge,
  Box,
  Chip,
  Drawer,
  IconButton,
  Toolbar,
  Typography,
} from "@mui/material";
import GrainMark from "./GrainMark.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import { stateDot } from "./UnbackedStateDot.jsx";

// PhoneNav is what the sidebar becomes on a phone (phone.js): the rail
// itself is unchanged and moves, whole, into a drawer that slides in
// over the page, and what stays on screen is one bar across the top --
// the mark, the wordmark, the deployment's name, and the two controls
// worth a permanent tap target.
//
// The rail is not rebuilt for the small screen, it is *hidden* behind a
// button: every entry in it (the state filters, the four lists, the
// board, Settings/System/Metrics) is still one tap away, and there is
// only one nav in the tree to keep correct. A bottom tab bar would have
// meant choosing four of a dozen destinations and leaving the rest
// somewhere else.
//
// The two controls that do not go behind the button are the menu itself
// and "+" -- filing a task is the one thing somebody does on a phone
// without navigating anywhere first, and it is the sidebar's own primary
// button, so making it cost two taps would be the wrong trade.
//
// Which build is running (the sidebar's footer stamp) is deliberately
// left in the drawer rather than promoted here: it is worth a glance
// during a deploy and nothing on any other day, and this bar has one row
// to spend. The environment chip is the opposite case and rides along --
// the sidebar's own argument for it is that it is on screen in every
// view, which on a phone means here or nowhere.
export default function PhoneNav({
  config,
  running,
  onOpenNewTask,
  alerts = null,
  // The gear, between the alerts button and "+" (docs/design/settings-panel.md
  // §4.1): the way into Settings on a phone. System is the row at the
  // foot of the drawer.
  onOpenSettings,
  settingsOpen = false,
  children,
}) {
  const [open, setOpen] = useState(false);

  return (
    <>
      {/* position="static", not "fixed": the shell is a flex column on a
          phone (style.css), so this bar takes its own row at the top and
          the page scrolls beneath it without needing an offset that
          would have to be kept in step with the bar's height.
          The status bar's own strip above it -- index.html asks for
          viewport-fit=cover, so without something holding it back this
          bar's contents would sit under the clock -- is .app-shell's to
          reserve (style.css), for every top row rather than for this
          one; the drawer below still needs its own, being a portal
          outside the shell entirely. */}
      <AppBar
        position="static"
        elevation={0}
        color="default"
        sx={{
          flex: "none",
          bgcolor: "background.paper",
          borderBottom: 1,
          borderColor: "divider",
        }}
      >
        <Toolbar variant="dense" sx={{ gap: 1, minHeight: 52 }}>
          <IconButton
            edge="start"
            aria-label="Open navigation"
            onClick={() => setOpen(true)}
          >
            {/* The rail's own marker for the State pane
                (UnbackedStateDot.jsx: a state repository nobody has a
                second copy of, or a merged change that reset a value
                here and reached nobody) sits on the Settings entry --
                which on a phone is behind this button, so without
                something here it would be a warning visible only to
                whoever had already gone looking. A dot on the button
                that hides it, therefore: it says the rail has something
                on it, and opening the drawer says what and where. */}
            <Badge
              color="warning"
              variant="dot"
              overlap="circular"
              invisible={!stateDot(config)}
            >
              <MenuIcon />
            </Badge>
          </IconButton>
          {/* Animated while anything is running, exactly as in the rail:
              on a phone this is the only piece of the deployment's live
              state that is always on screen. */}
          <GrainMark size={24} animated={running} />
          <Typography
            component="h1"
            variant="subtitle1"
            fontWeight={600}
            letterSpacing="-0.01em"
            noWrap
            sx={{ m: 0, flex: 1, minWidth: 0 }}
          >
            grain
          </Typography>
          {config?.environmentName ? (
            <Chip
              label={config.environmentName}
              size="small"
              color="warning"
              title={`Environment: ${config.environmentName}`}
              sx={{ maxWidth: 110, fontWeight: 600 }}
            />
          ) : null}
          {/* The alerts bell, on the bar rather than in the drawer with
              the rail: a pause hidden behind a menu
              button is one nobody on a phone finds out about. */}
          {alerts}
          {onOpenSettings && (
            <IconButton
              size="small"
              aria-label="Settings"
              aria-pressed={settingsOpen}
              data-testid="phone-settings"
              onClick={onOpenSettings}
              color={settingsOpen ? "primary" : "inherit"}
            >
              <ItemGlyph kind="settings" size={20} />
            </IconButton>
          )}
          {onOpenNewTask && (
            <IconButton
              edge="end"
              aria-label="New task"
              onClick={onOpenNewTask}
            >
              <AddIcon />
            </IconButton>
          )}
        </Toolbar>
      </AppBar>

      <Drawer
        open={open}
        onClose={() => setOpen(false)}
        // The rail is 232px of a ~390px screen, so the page it is
        // covering stays visible down the right-hand edge -- which is
        // what makes it read as a drawer over the page rather than as a
        // new screen.
        //
        // Named on its paper, not on the Drawer: a Drawer's own props
        // land on MUI's Modal root, a role="presentation" element whose
        // aria-label no assistive tech reads, and its paper has no role
        // at all -- so a screen reader landed in the open drawer with
        // nothing saying it was a modal or what it held, and
        // getByRole("dialog", { name: "Navigation" }) found nothing.
        // The same fix SidePanel.jsx carries for its form drawers.
        PaperProps={{
          role: "dialog",
          "aria-modal": true,
          "aria-label": "Navigation",
          sx: { pt: "env(safe-area-inset-top, 0px)" },
        }}
      >
        {/* Every navigation in the rail closes the drawer so the page
            navigated to is immediately visible. Interactive controls inside
            the rail (like the team picker dropdown and its search input)
            stay open during interaction until a destination is chosen.

            A click is not the only way an entry is chosen. The rail's
            entries are MUI ListItemButtons -- a <div role="button"> --
            and ButtonBase answers Enter (on keydown) and Space (on
            keyup) on one of those by calling its onClick itself, with
            no DOM click for the capture below to see. Picked from the
            keyboard, an entry navigated and left the drawer open over
            the page it had just opened, still trapping focus. */}
        <Box
          onClickCapture={(e) => {
            if (keepsDrawerOpen(e.target)) return;
            setOpen(false);
          }}
          onKeyDownCapture={(e) => {
            if (e.key === "Enter" && activatesWithoutClick(e.target)) {
              setOpen(false);
            }
          }}
          onKeyUpCapture={(e) => {
            if (e.key === " " && activatesWithoutClick(e.target)) {
              setOpen(false);
            }
          }}
          // The rail alone, the whole height of the drawer: the page's
          // title is on its own header rail (PageHeader.jsx), which on a
          // phone stays on the page under this bar rather than in here
          // (grain/task-4915). The rail keeps its own
          // footer on screen inside that height -- App.jsx hands it
          // pinFooter on a phone, so its list scrolls and the footer
          // under it does not (Sidebar.jsx).
          sx={{
            display: "flex",
            flexDirection: "column",
            height: "100%",
            "& > aside": { flex: "1 1 auto", minHeight: 0 },
          }}
        >
          {children}
        </Box>
      </Drawer>
    </>
  );
}

// Whether a tap on `target` leaves the drawer open: a text field, and a
// control that opens a menu of its own -- the rail's Create options
// split button. That menu is anchored to its button inside the drawer,
// so closing the drawer on the tap that opened it unmounted the anchor
// and the menu never appeared, which left New session and New board
// out of a phone's reach (2026-10-23 responsive walkthrough, bug 3).
// The menu's own items still close it: the menu is portalled to <body>,
// but React carries their clicks back up through this Box all the same.
function keepsDrawerOpen(target) {
  return Boolean(
    target.closest('input, [aria-haspopup]:not([aria-haspopup="false"])'),
  );
}

// Whether a key on `target` is one ButtonBase turns into an onClick call
// rather than a click event: a non-native button or menu item (the
// Create options menu's entries are MUI MenuItems, an <li>), the key
// pressed on the button itself (ButtonBase checks the same), and not
// disabled. A native
// <button> or <a> gets a real click from the browser, which
// onClickCapture already sees.
function activatesWithoutClick(target) {
  const role = target.getAttribute("role");
  return (
    (role === "button" || role === "menuitem") &&
    target.tagName !== "BUTTON" &&
    target.tagName !== "A" &&
    target.getAttribute("aria-disabled") !== "true" &&
    !keepsDrawerOpen(target)
  );
}
