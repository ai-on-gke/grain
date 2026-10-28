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

import { createTheme } from "@mui/material/styles";

// The palettes below carry over the accent/danger/surface values that
// style.css's own :root and prefers-color-scheme:dark blocks already
// defined (bwsalmon/agents#364's Plane-inspired tokens) so adopting MUI's
// primitives (Button, TextField, Dialog, ...) does not also reset the
// app's look. style.css keeps those custom properties for the pieces MUI
// has no primitive for -- state dots, badges, the task list layout.
//
// Every value here has a twin in style.css and the two have to be
// changed together. The accent is the brand mark's own grain colour --
// bronze (#8A6A2E) on the light ground, wheat (#D9A441) on the dark one,
// which is the split the mark itself is drawn with because the gold
// disappears against white; the greys are warmed to match. See
// docs/brand.md.
// SIDEBAR_WIDTH is the one layout number two pieces of chrome have to
// agree on: the sidebar itself (Sidebar.jsx) and the pane anything you
// open fills (Overlay.jsx's own `pane`), which starts exactly where the
// sidebar ends. It lives here rather than in either of them so neither
// owns the other's width.
export const SIDEBAR_WIDTH = 232;
// SIDEBAR_COLLAPSED_WIDTH is the same rail folded down to its icons
// (sidebarCollapse.js): wide enough for a 16px glyph centred in a 44px
// target with a count badge on its corner, and no wider.
export const SIDEBAR_COLLAPSED_WIDTH = 60;

const shared = {
  shape: { borderRadius: 6 },
  typography: {
    fontFamily:
      '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif',
    fontSize: 14,
    button: { textTransform: "lowercase", fontWeight: 500 },
    overline: { textTransform: "lowercase" },
  },
  components: {
    MuiButton: {
      styleOverrides: {
        root: { borderRadius: 6, textTransform: "lowercase" },
      },
    },
    MuiButtonBase: {
      styleOverrides: {
        root: { textTransform: "lowercase" },
      },
    },
    MuiTab: {
      styleOverrides: {
        root: { textTransform: "lowercase" },
      },
    },
    MuiTableCell: {
      styleOverrides: {
        head: { textTransform: "lowercase" },
      },
    },
    MuiTableHead: {
      styleOverrides: {
        root: { textTransform: "lowercase" },
      },
    },
    MuiTableSortLabel: {
      styleOverrides: {
        root: { textTransform: "lowercase" },
      },
    },
    MuiFormLabel: {
      styleOverrides: {
        root: { textTransform: "lowercase" },
      },
    },
    MuiInputLabel: {
      styleOverrides: {
        root: { textTransform: "lowercase" },
      },
    },
    MuiDialogTitle: {
      styleOverrides: {
        root: { textTransform: "lowercase" },
      },
    },
    MuiChip: {
      styleOverrides: {
        label: { textTransform: "lowercase" },
      },
    },
    // An alert's message is `overflow: auto`, so a word longer than its
    // line -- a pause reason with no spaces, a declaring file's path, a
    // URL -- made the message scroll sideways inside the banner or pane
    // rather than wrap, with the rest of it out of sight on a phone.
    // Breaking such a word only where nothing else fits leaves ordinary
    // text as it was (grain/task-1987).
    MuiAlert: {
      styleOverrides: {
        message: { overflowWrap: "anywhere" },
      },
    },
    MuiTypography: {
      styleOverrides: {
        h1: { textTransform: "lowercase" },
        h2: { textTransform: "lowercase" },
        h3: { textTransform: "lowercase" },
        h4: { textTransform: "lowercase" },
        h5: { textTransform: "lowercase" },
        h6: { textTransform: "lowercase" },
        subtitle1: { textTransform: "lowercase" },
        subtitle2: { textTransform: "lowercase" },
        overline: { textTransform: "lowercase" },
        button: { textTransform: "lowercase" },
      },
    },
  },
};

// Under prefers-reduced-motion: reduce, every transition MUI draws for
// itself -- the drawer's slide, a Dialog's fade and its backdrop's,
// Collapse, Grow, the Paper shadow -- runs in zero time. MUI ignores the
// preference on its own; the app's own animations (GrainMark.jsx,
// style.css's reduced-motion block) already honour it. Two halves, both
// needed: the durations are what Fade/Slide/Drawer take their default
// timeouts from, and create() is what every styled component writes its
// CSS `transition` with -- including the ones handed an explicit
// duration, which the durations alone would not reach.
// AppThemeProvider picks these variants while the media query matches.
const reducedMotion = {
  transitions: {
    duration: {
      shortest: 0,
      shorter: 0,
      short: 0,
      standard: 0,
      complex: 0,
      enteringScreen: 0,
      leavingScreen: 0,
    },
    create: (props = ["all"]) =>
      (Array.isArray(props) ? props : [props])
        .map((prop) => `${prop} 0ms`)
        .join(","),
  },
};

const lightPalette = {
  palette: {
    mode: "light",
    primary: { main: "#8a6a2e", dark: "#6d5324", contrastText: "#ffffff" },
    error: { main: "#d0453a" },
    // success mirrors --state-completed; warning is pulled off MUI's own
    // bright orange to a burnt one that reads as the accent's louder
    // cousin rather than fighting it; info is the one cool tone left in
    // the app, gold's complement, which is what an Alert that is only
    // telling you something should be next to all this warmth.
    success: { main: "#1a9c53" },
    warning: { main: "#a8621c" },
    info: { main: "#3f6b8a" },
    background: { default: "#f7f6f3", paper: "#ffffff" },
    text: { primary: "#24221d", secondary: "#6e6a60" },
    divider: "#e6e3dc",
  },
};

const darkPalette = {
  palette: {
    mode: "dark",
    // primary.dark is MUI's hover shade, and on this ground the hover has
    // to be *lighter* than the resting colour -- the same inversion the
    // light palette above does not need.
    primary: { main: "#d9a441", dark: "#e8b962", contrastText: "#17130a" },
    error: { main: "#ec7469" },
    success: { main: "#3ec573" },
    warning: { main: "#e29a4e" },
    info: { main: "#7fa8c7" },
    background: { default: "#0f1013", paper: "#14161a" },
    text: { primary: "#e9e7e1", secondary: "#9b988f" },
    divider: "#292b31",
  },
};

export const lightTheme = createTheme({ ...shared, ...lightPalette });
export const darkTheme = createTheme({ ...shared, ...darkPalette });
export const lightThemeReducedMotion = createTheme({
  ...shared,
  ...lightPalette,
  ...reducedMotion,
});
export const darkThemeReducedMotion = createTheme({
  ...shared,
  ...darkPalette,
  ...reducedMotion,
});
