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

import React, { useEffect, useId, useRef, useState } from "react";
import { useThemeMode } from "./ThemeModeContext.jsx";

export function formatRelativeAge(secondsOrIso) {
  if (
    secondsOrIso === null ||
    secondsOrIso === undefined ||
    secondsOrIso === ""
  ) {
    return "never";
  }
  let secs = null;
  if (typeof secondsOrIso === "number") {
    secs = Math.max(0, Math.round(secondsOrIso));
  } else if (typeof secondsOrIso === "string") {
    const parsed = Date.parse(secondsOrIso);
    if (!Number.isNaN(parsed)) {
      secs = Math.max(0, Math.round((Date.now() - parsed) / 1000));
    }
  }
  if (secs === null) return String(secondsOrIso);
  if (secs < 60) return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

export function StatusPill({ status, ageSeconds, detail }) {
  const norm = String(status || "offline").toLowerCase();
  let cls = "gs-pill gs-pill-offline";
  if (
    norm === "online" ||
    norm === "ok" ||
    norm === "running" ||
    norm === "healthy"
  ) {
    cls = "gs-pill gs-pill-online";
  } else if (
    norm === "degraded" ||
    norm === "creating" ||
    norm === "replacing"
  ) {
    cls = "gs-pill gs-pill-degraded";
  } else if (norm === "failed" || norm === "deleted") {
    cls = "gs-pill gs-pill-danger";
  }
  const ageText =
    ageSeconds !== undefined && ageSeconds !== null
      ? ` · ${formatRelativeAge(ageSeconds)}`
      : "";
  return (
    <span className={cls} title={detail || status || "offline"}>
      <span className="gs-pill-dot" aria-hidden="true" />
      <span>
        {norm}
        {ageText}
      </span>
    </span>
  );
}

const HOST_KIND_LABELS = {
  "registry-vm": { label: "registry-vm", cls: "gs-badge gs-badge-vm" },
  workstation: { label: "workstation", cls: "gs-badge gs-badge-workstation" },
  kubernetes: { label: "kubernetes", cls: "gs-badge gs-badge-k8s" },
  other: { label: "other", cls: "gs-badge" },
};

export function HostKindBadge({ hostKind, kind }) {
  const raw = hostKind || (kind === "instance" ? "registry-vm" : "other");
  const info = HOST_KIND_LABELS[raw] || { label: raw, cls: "gs-badge" };
  return <span className={info.cls}>{info.label}</span>;
}

export function PageHeader({ title, subtitle, actions }) {
  return (
    <header className="gs-page-header">
      <div>
        <h1 className="gs-page-title">{title}</h1>
        {subtitle ? <p className="gs-page-subtitle">{subtitle}</p> : null}
      </div>
      {actions ? <div className="gs-page-actions">{actions}</div> : null}
    </header>
  );
}

export function FilterBar({ children, ariaLabel = "Filters" }) {
  return (
    <section className="gs-filter-bar" aria-label={ariaLabel}>
      {children}
    </section>
  );
}

export function SortableTable({
  columns,
  rows,
  rowKey,
  emptyMessage = "No records found.",
}) {
  const [sortCol, setSortCol] = useState(null);
  const [sortDir, setSortDir] = useState("asc");

  const handleSort = (colKey) => {
    if (sortCol === colKey) {
      setSortDir((d) => (d === "asc" ? "desc" : "asc"));
    } else {
      setSortCol(colKey);
      setSortDir("asc");
    }
  };

  const sortedRows = React.useMemo(() => {
    if (!sortCol) return rows;
    const col = columns.find((c) => c.key === sortCol);
    if (!col || !col.sortValue) return rows;
    const copy = [...rows];
    copy.sort((a, b) => {
      const va = col.sortValue(a);
      const vb = col.sortValue(b);
      if (va < vb) return sortDir === "asc" ? -1 : 1;
      if (va > vb) return sortDir === "asc" ? 1 : -1;
      return 0;
    });
    return copy;
  }, [columns, rows, sortCol, sortDir]);

  if (!rows || rows.length === 0) {
    return <div className="gs-empty-state">{emptyMessage}</div>;
  }

  return (
    <div className="gs-table-wrap">
      <table className="gs-table">
        <thead>
          <tr>
            {columns.map((col) => (
              <th key={col.key} scope="col">
                {col.sortValue ? (
                  <button
                    type="button"
                    className="gs-table-sort-btn"
                    onClick={() => handleSort(col.key)}
                  >
                    <span>{col.header}</span>
                    {sortCol === col.key ? (
                      <span>{sortDir === "asc" ? "↑" : "↓"}</span>
                    ) : null}
                  </button>
                ) : (
                  col.header
                )}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {sortedRows.map((row, idx) => {
            const key =
              typeof rowKey === "function"
                ? rowKey(row, idx)
                : typeof rowKey === "string" && row[rowKey] != null
                  ? row[rowKey]
                  : idx;
            return (
              <tr key={key}>
                {columns.map((col) => (
                  <td key={col.key}>
                    {col.render ? col.render(row) : row[col.key]}
                  </td>
                ))}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export function CopyBlock({ label, code }) {
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(code);
      }
      setCopied(true);
      setTimeout(() => setCopied(false), 1800);
    } catch {
      setCopied(false);
    }
  };

  return (
    <div className="gs-copy-block">
      <div className="gs-copy-header">
        <span>{label}</span>
        <button type="button" className="gs-btn gs-btn-sm" onClick={handleCopy}>
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <pre className="gs-copy-pre">
        <code>{code}</code>
      </pre>
    </div>
  );
}

export function ConfirmDialog({
  open,
  title,
  children,
  confirmLabel = "Confirm",
  cancelLabel = "Cancel",
  danger = false,
  disabled = false,
  onConfirm,
  onCancel,
}) {
  const titleId = useId();
  const dialogRef = useRef(null);
  const cancelRef = useRef(null);
  const onCancelRef = useRef(onCancel);
  onCancelRef.current = onCancel;
  // A modal confirmation owns the keyboard while it is open: focus moves to
  // Cancel (the safe choice for a destructive action), Tab stays inside the
  // dialog, Escape cancels, and closing hands focus back to whatever opened it.
  useEffect(() => {
    if (!open) return undefined;
    const opener = document.activeElement;
    cancelRef.current?.focus();
    const onKeyDown = (e) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        onCancelRef.current?.();
        return;
      }
      if (e.key !== "Tab" || !dialogRef.current) return;
      const focusable = Array.from(
        dialogRef.current.querySelectorAll(
          'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
        ),
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      const inside = dialogRef.current.contains(document.activeElement);
      if (e.shiftKey && (document.activeElement === first || !inside)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && (document.activeElement === last || !inside)) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("keydown", onKeyDown, true);
      if (opener && typeof opener.focus === "function" && opener.isConnected) {
        opener.focus();
      }
    };
  }, [open]);
  if (!open) return null;
  return (
    <div className="gs-dialog-backdrop" role="presentation" onClick={onCancel}>
      <div
        ref={dialogRef}
        className="gs-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 id={titleId} className="gs-dialog-title">
          {title}
        </h2>
        <div className="gs-dialog-body">{children}</div>
        <div className="gs-dialog-actions">
          <button
            ref={cancelRef}
            type="button"
            className="gs-btn"
            onClick={onCancel}
          >
            {cancelLabel}
          </button>
          <button
            type="button"
            className={
              danger ? "gs-btn gs-btn-danger" : "gs-btn gs-btn-primary"
            }
            disabled={disabled}
            onClick={onConfirm}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

export function EmptyState({ title, description, action }) {
  return (
    <div className="gs-empty-state">
      <h3 className="gs-empty-title">{title}</h3>
      {description ? <p className="gs-empty-desc">{description}</p> : null}
      {action ? <div className="gs-dialog-actions">{action}</div> : null}
    </div>
  );
}

export function ThemeModeToggle() {
  const { mode, resolvedMode, setMode } = useThemeMode();
  const nextMode = resolvedMode === "dark" ? "light" : "dark";
  return (
    <button
      type="button"
      className="gs-btn gs-btn-sm"
      onClick={() => setMode(nextMode)}
      aria-label={`Switch to ${nextMode} theme (current: ${mode})`}
      title={`Theme: ${resolvedMode} (click for ${nextMode})`}
    >
      {resolvedMode === "dark" ? "Light mode" : "Dark mode"}
    </button>
  );
}
