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

import { useEffect, useMemo } from "react";
import { CssBaseline, ThemeProvider, useMediaQuery } from "@mui/material";
import {
  darkTheme,
  darkThemeReducedMotion,
  lightTheme,
  lightThemeReducedMotion,
} from "./theme.js";
import { ThemeModeProvider, useThemeMode } from "./ThemeModeContext.jsx";

// Split out from AppThemeProvider so useThemeMode's context is available:
// the resolved light/dark choice depends on both the user's stored mode
// and (when that mode is "auto") the OS-level prefers-color-scheme signal.
function ResolvedThemeProvider({ children }) {
  const { mode } = useThemeMode();
  const prefersDark = useMediaQuery("(prefers-color-scheme: dark)");
  const resolvedMode =
    mode === "auto" ? (prefersDark ? "dark" : "light") : mode;
  // noSsr so the first render already has the answer: without it the
  // hook reports false until an effect runs, and a drawer or dialog open
  // at load would slide in before the preference was read. It stays live,
  // so changing the OS setting swaps the theme without a reload.
  const reduceMotion = useMediaQuery("(prefers-reduced-motion: reduce)", {
    noSsr: true,
  });
  const theme = useMemo(() => {
    if (resolvedMode === "dark") {
      return reduceMotion ? darkThemeReducedMotion : darkTheme;
    }
    return reduceMotion ? lightThemeReducedMotion : lightTheme;
  }, [resolvedMode, reduceMotion]);

  // style.css keys its own custom properties (state dots, badges, the
  // task list layout -- pieces MUI has no primitive for) off this
  // attribute rather than duplicating MUI's palette choice in JS.
  useEffect(() => {
    document.documentElement.dataset.theme = resolvedMode;
  }, [resolvedMode]);

  // The mark is drawn in two colours -- bronze for a light ground, wheat
  // for a dark one -- and the tab icon is the one place the ground is
  // the browser's rather than the app's. Following the app's own
  // resolved theme is the best guess available: a reader in dark mode
  // has a dark browser chrome far more often than not, and the
  // alternative (one file that has to work on both) is the washed-out
  // middle the brand explicitly avoided.
  useEffect(() => {
    const svg = document.getElementById("favicon");
    if (svg) svg.href = `/grain-mark-${resolvedMode}.svg`;
    const png = document.getElementById("favicon-png");
    if (png) png.href = `/grain-mark-${resolvedMode}.png`;
  }, [resolvedMode]);

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      {children}
    </ThemeProvider>
  );
}

export default function AppThemeProvider({ children }) {
  return (
    <ThemeModeProvider>
      <ResolvedThemeProvider>{children}</ResolvedThemeProvider>
    </ThemeModeProvider>
  );
}
