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

import { Component } from "react";
import {
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
} from "@mui/material";

// RENDER_FAILED starts the console line componentDidCatch writes, and is
// what ui/e2e/pageerrors.js looks for. A render throw a boundary catches
// is no longer an uncaught exception -- React reports it to the console
// rather than to window.onerror -- so without a line of our own the e2e
// suite's "the page threw" guard would go quiet on exactly the failures
// it was written for (grain/task-182).
export const RENDER_FAILED = "grain: render failed";

// ErrorBoundary keeps one render throw from taking the whole app down
// with it (grain/task-1435). React unmounts everything above the nearest
// boundary when a render throws; with none, that was the entire tree --
// rail, banners, every pane -- and a blank white page with no message
// and no way back but a manual reload.
//
// App.jsx puts one around the main pane and one around each overlay, so
// the rail survives a pane that cannot draw and a broken overlay can be
// closed; main.jsx puts one around the whole app for anything those miss.
//
// resetKey is how a boundary recovers without a reload: whatever the
// caller derives it from (the page on screen, the task an overlay is
// showing) changing means the thing that threw is no longer what is
// being asked for, so the error is dropped and the children are tried
// again. Navigating somewhere else, or Back, is enough.
//
// variant picks the fallback: "page" (the default) fills the space the
// children would have, "dialog" stands in for an overlay with a Close
// that calls onClose, and "app" is the full-screen one main.jsx uses.
export default class ErrorBoundary extends Component {
  constructor(props) {
    super(props);
    this.state = { error: null, resetKey: props.resetKey };
  }

  static getDerivedStateFromError(error) {
    return { error };
  }

  static getDerivedStateFromProps(props, state) {
    if (props.resetKey !== state.resetKey) {
      return { error: null, resetKey: props.resetKey };
    }
    return null;
  }

  componentDidCatch(error, info) {
    console.error(
      `${RENDER_FAILED} in ${this.props.label || "the app"}: ${
        error?.stack || String(error)
      }`,
      info?.componentStack || "",
    );
  }

  render() {
    const { error } = this.state;
    if (error === null) return this.props.children;
    const { variant = "page", label, onClose } = this.props;
    const message = String(error?.message || error);
    const reload = () => window.location.reload();
    const explanation = (
      <p>
        {label ? `The ${label}` : "This page"} hit an error and could not be
        drawn.
        {variant === "app"
          ? " Reloading may clear it."
          : " The rest of grain still works: pick another page, go Back, or reload."}
      </p>
    );
    if (variant === "dialog") {
      return (
        <Dialog open onClose={onClose} maxWidth="sm" fullWidth>
          <DialogTitle>Something went wrong</DialogTitle>
          <DialogContent className="render-error" role="alert">
            {explanation}
            <pre className="render-error-message">{message}</pre>
          </DialogContent>
          <DialogActions>
            <Button onClick={reload}>Reload</Button>
            {onClose && (
              <Button variant="contained" onClick={onClose}>
                Close
              </Button>
            )}
          </DialogActions>
        </Dialog>
      );
    }
    return (
      <div
        className={`render-error render-error-${variant}`}
        role="alert"
        data-testid="render-error"
      >
        <h2>Something went wrong</h2>
        {explanation}
        <pre className="render-error-message">{message}</pre>
        <div className="render-error-actions">
          <Button variant="contained" onClick={reload}>
            Reload
          </Button>
          {/* A full navigation rather than a pushState: if the app is
              what broke, a page load is the one way home that does not
              go through it. */}
          <Button href="/">Go to tasks</Button>
        </div>
      </div>
    );
  }
}
