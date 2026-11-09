// Copyright 2026 The grain Authors. All rights reserved.
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

package server

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/hub/store"
)

const statePageCSP = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

const statePageCSS = `:root {
  color-scheme: light dark;
  --bg: #f8fafc;
  --card: #ffffff;
  --border: #e2e8f0;
  --text: #0f172a;
  --muted: #475569;
  --code-bg: #f1f5f9;
  --accent: #2563eb;
  --accent-fg: #ffffff;
  --badge-bg: #e2e8f0;
  --badge-fg: #1e293b;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0b0f19;
    --card: #111827;
    --border: #1f2937;
    --text: #f8fafc;
    --muted: #94a3b8;
    --code-bg: #1e293b;
    --accent: #3b82f6;
    --accent-fg: #ffffff;
    --badge-bg: #1f2937;
    --badge-fg: #e2e8f0;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0;
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: var(--bg);
  color: var(--text);
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  line-height: 1.5;
}
main {
  width: 100%;
  max-width: 560px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 28px 32px;
}
.badge {
  display: inline-block;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  padding: 3px 10px;
  border-radius: 999px;
  background: var(--badge-bg);
  color: var(--badge-fg);
  margin-bottom: 12px;
}
h1 {
  margin: 0 0 8px 0;
  font-size: 22px;
  font-weight: 600;
}
p {
  margin: 0 0 16px 0;
  color: var(--muted);
  font-size: 15px;
}
dl {
  margin: 16px 0;
  padding: 14px 16px;
  background: var(--code-bg);
  border-radius: 8px;
  display: grid;
  grid-template-columns: 140px 1fr;
  row-gap: 8px;
  column-gap: 12px;
  font-size: 14px;
}
dt {
  color: var(--muted);
  font-weight: 500;
}
dd {
  margin: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  word-break: break-all;
}
.actions {
  margin-top: 20px;
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}
a.btn {
  display: inline-block;
  text-decoration: none;
  padding: 8px 16px;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 500;
  background: var(--accent);
  color: var(--accent-fg);
}
a.btn-secondary {
  background: transparent;
  color: var(--text);
  border: 1px solid var(--border);
}
code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  background: var(--code-bg);
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 13px;
}`

// WantsHTML reports whether the HTTP request prefers an HTML state page (browser navigation)
// over a JSON API error payload.
func WantsHTML(r *http.Request) bool {
	if r == nil {
		return false
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, "text/html") || strings.Contains(accept, "application/xhtml+xml") {
		return true
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Dest"), "document") ||
		strings.EqualFold(r.Header.Get("Sec-Fetch-Mode"), "navigate") {
		return true
	}
	return false
}

func writeHTMLStatePage(w http.ResponseWriter, status int, badge, title, bodyHTML string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", statePageCSP)
	w.WriteHeader(status)

	page := fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>%s</style>
</head>
<body>
<main>
<span class="badge">%s</span>
<h1>%s</h1>
%s
</main>
</body>
</html>
`, html.EscapeString(title), statePageCSS, html.EscapeString(badge), html.EscapeString(title), bodyHTML)
	_, _ = w.Write([]byte(page))
}

func writeJSONStateResponse(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	writeJSONStateResponse(w, status, apierror.Response{
		Error: msg,
		Code:  code,
	})
}

func writeInternalError(w http.ResponseWriter, err error) {
	writeJSONStateResponse(w, http.StatusInternalServerError, apierror.Body(http.StatusInternalServerError, err))
}

// WriteMissingEntryState writes the 404 state page (or JSON) for an unknown or deleted `<name>.<instance-domain>`.
// When `deletedEntry` is non-nil and `isManager` is true, deletion and reservation retention timestamps are included.
func WriteMissingEntryState(w http.ResponseWriter, r *http.Request, registryHost, name string, deletedEntry *store.EntryRow, isManager bool) {
	createURL := ""
	if registryHost != "" && name != "" {
		createURL = fmt.Sprintf("https://%s/new?name=%s", NormalizeHost(registryHost), url.QueryEscape(name))
	}

	if deletedEntry != nil && isManager {
		delStr := ""
		if deletedEntry.DeletedAt != nil {
			delStr = deletedEntry.DeletedAt.UTC().Format(time.RFC3339)
		}
		resStr := ""
		if deletedEntry.PurgeAfter != nil {
			resStr = deletedEntry.PurgeAfter.UTC().Format(time.RFC3339)
		}
		if WantsHTML(r) {
			var body strings.Builder
			fmt.Fprintf(&body, `<p>Entry <code>%s</code> was deleted and is currently within its name reservation and retention window.</p>`, html.EscapeString(name))
			body.WriteString(`<dl>`)
			fmt.Fprintf(&body, `<dt>Entry</dt><dd>%s</dd>`, html.EscapeString(name))
			fmt.Fprintf(&body, `<dt>Owner</dt><dd>%s</dd>`, html.EscapeString(deletedEntry.OwnerEmail))
			if deletedEntry.OwnerGroup != "" {
				fmt.Fprintf(&body, `<dt>Owner group</dt><dd>%s</dd>`, html.EscapeString(deletedEntry.OwnerGroup))
			}
			if delStr != "" {
				fmt.Fprintf(&body, `<dt>Deleted at</dt><dd>%s</dd>`, html.EscapeString(delStr))
			}
			if resStr != "" {
				fmt.Fprintf(&body, `<dt>Reserved until</dt><dd>%s</dd>`, html.EscapeString(resStr))
			}
			body.WriteString(`</dl>`)
			if createURL != "" {
				fmt.Fprintf(&body, `<div class="actions"><a class="btn" href="%s">Re-create %s</a></div>`, html.EscapeString(createURL), html.EscapeString(name))
			}
			writeHTMLStatePage(w, http.StatusNotFound, "404 Deleted", fmt.Sprintf("%s has been deleted", name), body.String())
			return
		}
		writeJSONStateResponse(w, http.StatusNotFound, map[string]any{
			"code":           "not_found",
			"error":          "entry_deleted",
			"message":        fmt.Sprintf("entry %q has been deleted", name),
			"entry":          name,
			"owner":          deletedEntry.OwnerEmail,
			"owner_group":    deletedEntry.OwnerGroup,
			"deleted_at":     delStr,
			"reserved_until": resStr,
			"create_url":     createURL,
		})
		return
	}

	if WantsHTML(r) {
		var body strings.Builder
		if name != "" {
			fmt.Fprintf(&body, `<p>No grain instance or connected entry named <code>%s</code> is registered.</p>`, html.EscapeString(name))
		} else {
			body.WriteString(`<p>The requested host does not match any registered grain entry.</p>`)
		}
		if createURL != "" {
			fmt.Fprintf(&body, `<div class="actions"><a class="btn" href="%s">Create %s</a></div>`, html.EscapeString(createURL), html.EscapeString(name))
		}
		title := "Entry not found"
		if name != "" {
			title = fmt.Sprintf("%s not found", name)
		}
		writeHTMLStatePage(w, http.StatusNotFound, "404 Not Found", title, body.String())
		return
	}
	writeJSONStateResponse(w, http.StatusNotFound, map[string]any{
		"code":       "not_found",
		"error":      "entry_not_found",
		"message":    fmt.Sprintf("entry %q not found", name),
		"entry":      name,
		"create_url": createURL,
	})
}

// WriteProvisioningState writes the 503 state page (or JSON) with `Retry-After: 5`
// when an entry is in `creating` or `replacing`.
func WriteProvisioningState(w http.ResponseWriter, r *http.Request, registryHost string, entry *store.EntryRow) {
	w.Header().Set("Retry-After", "5")
	state := string(entry.LifecycleState)
	detailURL := ""
	if registryHost != "" {
		detailURL = fmt.Sprintf("https://%s/entries/%s", NormalizeHost(registryHost), url.PathEscape(entry.Name))
	}

	if WantsHTML(r) {
		var body strings.Builder
		fmt.Fprintf(&body, `<p>Instance <code>%s</code> is currently <strong>%s</strong> and will begin serving traffic automatically once ready.</p>`,
			html.EscapeString(entry.Name), html.EscapeString(state))
		body.WriteString(`<dl>`)
		fmt.Fprintf(&body, `<dt>Entry</dt><dd>%s</dd>`, html.EscapeString(entry.Name))
		fmt.Fprintf(&body, `<dt>State</dt><dd>%s</dd>`, html.EscapeString(state))
		fmt.Fprintf(&body, `<dt>Owner</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerEmail))
		if entry.OwnerGroup != "" {
			fmt.Fprintf(&body, `<dt>Owner group</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerGroup))
		}
		body.WriteString(`</dl>`)
		if detailURL != "" {
			fmt.Fprintf(&body, `<div class="actions"><a class="btn btn-secondary" href="%s">View status in registry</a></div>`, html.EscapeString(detailURL))
		}
		writeHTMLStatePage(w, http.StatusServiceUnavailable, "503 "+strings.ToUpper(state), fmt.Sprintf("%s is %s", entry.Name, state), body.String())
		return
	}
	writeJSONStateResponse(w, http.StatusServiceUnavailable, map[string]any{
		"code":    "service_unavailable",
		"error":   "entry_provisioning",
		"message": fmt.Sprintf("entry %q is currently %s", entry.Name, state),
		"entry":   entry.Name,
		"state":   state,
	})
}

// WriteStoppingState writes the 503 state page (or JSON) with `Retry-After: 5`
// when a hosted instance is in `stopping`.
func WriteStoppingState(w http.ResponseWriter, r *http.Request, registryHost string, entry *store.EntryRow) {
	w.Header().Set("Retry-After", "5")
	detailURL := ""
	if registryHost != "" {
		detailURL = fmt.Sprintf("https://%s/entries/%s", NormalizeHost(registryHost), url.PathEscape(entry.Name))
	}

	if WantsHTML(r) {
		var body strings.Builder
		fmt.Fprintf(&body, `<p>Instance <code>%s</code> is currently <strong>stopping</strong> while draining active tasks.</p>`,
			html.EscapeString(entry.Name))
		body.WriteString(`<dl>`)
		fmt.Fprintf(&body, `<dt>Entry</dt><dd>%s</dd>`, html.EscapeString(entry.Name))
		fmt.Fprintf(&body, `<dt>State</dt><dd>stopping</dd>`)
		fmt.Fprintf(&body, `<dt>Owner</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerEmail))
		if entry.OwnerGroup != "" {
			fmt.Fprintf(&body, `<dt>Owner group</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerGroup))
		}
		body.WriteString(`</dl>`)
		if detailURL != "" {
			fmt.Fprintf(&body, `<div class="actions"><a class="btn btn-secondary" href="%s">View status in registry</a></div>`, html.EscapeString(detailURL))
		}
		writeHTMLStatePage(w, http.StatusServiceUnavailable, "503 STOPPING", fmt.Sprintf("%s is stopping", entry.Name), body.String())
		return
	}
	writeJSONStateResponse(w, http.StatusServiceUnavailable, map[string]any{
		"code":    "service_unavailable",
		"error":   "entry_stopping",
		"message": fmt.Sprintf("entry %q is currently stopping", entry.Name),
		"entry":   entry.Name,
		"state":   "stopping",
	})
}

// WriteStoppedOrWakingState writes the 503 state page (or JSON) with `Retry-After: 5`
// when an authorized user visits a `stopped` hosted instance (waking it automatically when `waking == true`).
func WriteStoppedOrWakingState(w http.ResponseWriter, r *http.Request, registryHost string, entry *store.EntryRow, waking bool) {
	w.Header().Set("Retry-After", "5")
	detailURL := ""
	if registryHost != "" {
		detailURL = fmt.Sprintf("https://%s/entries/%s", NormalizeHost(registryHost), url.PathEscape(entry.Name))
	}

	if waking {
		if WantsHTML(r) {
			var body strings.Builder
			fmt.Fprintf(&body, `<p>Instance <code>%s</code> was stopped while idle and is now <strong>waking up automatically</strong>. This page will begin serving traffic as soon as the VM is ready.</p>`,
				html.EscapeString(entry.Name))
			body.WriteString(`<dl>`)
			fmt.Fprintf(&body, `<dt>Entry</dt><dd>%s</dd>`, html.EscapeString(entry.Name))
			fmt.Fprintf(&body, `<dt>State</dt><dd>creating (auto-waking)</dd>`)
			fmt.Fprintf(&body, `<dt>Owner</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerEmail))
			if entry.OwnerGroup != "" {
				fmt.Fprintf(&body, `<dt>Owner group</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerGroup))
			}
			body.WriteString(`</dl>`)
			if detailURL != "" {
				fmt.Fprintf(&body, `<div class="actions"><a class="btn btn-secondary" href="%s">View status in registry</a></div>`, html.EscapeString(detailURL))
			}
			writeHTMLStatePage(w, http.StatusServiceUnavailable, "503 WAKING", fmt.Sprintf("%s is waking up", entry.Name), body.String())
			return
		}
		writeJSONStateResponse(w, http.StatusServiceUnavailable, map[string]any{
			"code":      "service_unavailable",
			"error":     "entry_waking",
			"message":   fmt.Sprintf("entry %q was stopped and is waking up automatically", entry.Name),
			"entry":     entry.Name,
			"state":     "creating",
			"auto_wake": true,
		})
		return
	}

	if WantsHTML(r) {
		var body strings.Builder
		fmt.Fprintf(&body, `<p>Instance <code>%s</code> is currently <strong>stopped</strong> and auto-wake on visit is disabled.</p>`,
			html.EscapeString(entry.Name))
		body.WriteString(`<dl>`)
		fmt.Fprintf(&body, `<dt>Entry</dt><dd>%s</dd>`, html.EscapeString(entry.Name))
		fmt.Fprintf(&body, `<dt>State</dt><dd>stopped</dd>`)
		fmt.Fprintf(&body, `<dt>Owner</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerEmail))
		if entry.OwnerGroup != "" {
			fmt.Fprintf(&body, `<dt>Owner group</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerGroup))
		}
		body.WriteString(`</dl>`)
		if detailURL != "" {
			fmt.Fprintf(&body, `<div class="actions"><a class="btn" href="%s">Start in registry</a></div>`, html.EscapeString(detailURL))
		}
		writeHTMLStatePage(w, http.StatusServiceUnavailable, "503 STOPPED", fmt.Sprintf("%s is stopped", entry.Name), body.String())
		return
	}
	writeJSONStateResponse(w, http.StatusServiceUnavailable, map[string]any{
		"code":      "service_unavailable",
		"error":     "entry_stopped",
		"message":   fmt.Sprintf("entry %q is currently stopped", entry.Name),
		"entry":     entry.Name,
		"state":     "stopped",
		"auto_wake": false,
	})
}

// WriteDeletingState writes the 410 Gone state page (or JSON) when an entry is in `deleting`.
func WriteDeletingState(w http.ResponseWriter, r *http.Request, entry *store.EntryRow) {
	if WantsHTML(r) {
		var body strings.Builder
		fmt.Fprintf(&body, `<p>Entry <code>%s</code> is currently being deprovisioned and is no longer accepting requests.</p>`, html.EscapeString(entry.Name))
		writeHTMLStatePage(w, http.StatusGone, "410 Gone", fmt.Sprintf("%s is being deleted", entry.Name), body.String())
		return
	}
	writeJSONStateResponse(w, http.StatusGone, map[string]any{
		"code":    "gone",
		"error":   "entry_deleting",
		"message": fmt.Sprintf("entry %q is being deleted", entry.Name),
		"entry":   entry.Name,
		"state":   "deleting",
	})
}

// WriteFailedState writes the 502 Bad Gateway state page (or JSON) when an entry is in `failed`.
// Entry managers (`isManager == true`) see the failure reason and a Replace VM recovery action;
// other callers receive a generic 502 page without internal details.
func WriteFailedState(w http.ResponseWriter, r *http.Request, registryHost string, entry *store.EntryRow, isManager bool) {
	replaceURL := ""
	if registryHost != "" {
		replaceURL = fmt.Sprintf("https://%s/entries/%s?action=replace", NormalizeHost(registryHost), url.PathEscape(entry.Name))
	}

	if WantsHTML(r) {
		var body strings.Builder
		fmt.Fprintf(&body, `<p>Entry <code>%s</code> is in a failed state and cannot serve requests.</p>`, html.EscapeString(entry.Name))
		body.WriteString(`<dl>`)
		fmt.Fprintf(&body, `<dt>Entry</dt><dd>%s</dd>`, html.EscapeString(entry.Name))
		fmt.Fprintf(&body, `<dt>Owner</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerEmail))
		if entry.OwnerGroup != "" {
			fmt.Fprintf(&body, `<dt>Owner group</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerGroup))
		}
		if isManager && entry.StatusReason != "" {
			fmt.Fprintf(&body, `<dt>Failure detail</dt><dd>%s</dd>`, html.EscapeString(entry.StatusReason))
		}
		body.WriteString(`</dl>`)
		if isManager && replaceURL != "" {
			fmt.Fprintf(&body, `<div class="actions"><a class="btn" href="%s">Replace VM</a></div>`, html.EscapeString(replaceURL))
		}
		writeHTMLStatePage(w, http.StatusBadGateway, "502 Failed", fmt.Sprintf("%s is unavailable", entry.Name), body.String())
		return
	}

	payload := map[string]any{
		"code":    "bad_gateway",
		"error":   "entry_failed",
		"message": fmt.Sprintf("entry %q is in failed state", entry.Name),
		"entry":   entry.Name,
		"state":   "failed",
	}
	if isManager {
		payload["status_reason"] = entry.StatusReason
		payload["replace_url"] = replaceURL
	}
	writeJSONStateResponse(w, http.StatusBadGateway, payload)
}

// WriteOfflineConnectedState writes the 503 Service Unavailable state page (or JSON)
// for a `connected` entry with no active tunnel session.
// It names `owner` (and `owner_group` when set), `display_name`/`host_kind`, and `last_seen_at`
// (`GREATEST(tunnel.LastSeenAt, entry.LastHeartbeatAt)` or "never connected"), plus the
// last disconnect reason for entry managers and admins.
func WriteOfflineConnectedState(w http.ResponseWriter, r *http.Request, entry *store.EntryRow, tc TunnelCacheEntry, now time.Time, isManager bool) {
	w.Header().Set("Retry-After", "5")

	var lastSeen time.Time
	if entry.LastHeartbeatAt != nil {
		lastSeen = entry.LastHeartbeatAt.UTC()
	}
	if tc.LastSeenAt.After(lastSeen) {
		lastSeen = tc.LastSeenAt.UTC()
	}
	if entry.Tunnel != nil && entry.Tunnel.LastSeenAt.After(lastSeen) {
		lastSeen = entry.Tunnel.LastSeenAt.UTC()
	}

	reason := tc.DisconnectReason
	if reason == "" {
		reason = entry.LastDisconnectReason
	}

	var lastSeenISO string
	var secondsAgo int64
	lastSeenDisplay := "never connected"
	if !lastSeen.IsZero() {
		lastSeenISO = lastSeen.UTC().Format(time.RFC3339)
		age := now.UTC().Sub(lastSeen)
		if age < 0 {
			age = 0
		}
		secondsAgo = int64(age.Seconds())
		lastSeenDisplay = fmt.Sprintf("%s (%ds ago)", lastSeenISO, secondsAgo)
	}

	if WantsHTML(r) {
		var body strings.Builder
		fmt.Fprintf(&body, `<p>Connected grain <code>%s</code> does not have an active outbound tunnel session.</p>`, html.EscapeString(entry.Name))
		body.WriteString(`<dl>`)
		fmt.Fprintf(&body, `<dt>Entry</dt><dd>%s</dd>`, html.EscapeString(entry.Name))
		if entry.DisplayName != "" {
			fmt.Fprintf(&body, `<dt>Display name</dt><dd>%s</dd>`, html.EscapeString(entry.DisplayName))
		}
		if entry.HostKind != "" {
			fmt.Fprintf(&body, `<dt>Host kind</dt><dd>%s</dd>`, html.EscapeString(entry.HostKind))
		}
		fmt.Fprintf(&body, `<dt>Owner</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerEmail))
		if entry.OwnerGroup != "" {
			fmt.Fprintf(&body, `<dt>Owner group</dt><dd>%s</dd>`, html.EscapeString(entry.OwnerGroup))
		}
		fmt.Fprintf(&body, `<dt>Last seen</dt><dd>%s</dd>`, html.EscapeString(lastSeenDisplay))
		if isManager && reason != "" {
			fmt.Fprintf(&body, `<dt>Disconnect reason</dt><dd>%s</dd>`, html.EscapeString(reason))
		}
		body.WriteString(`</dl>`)
		writeHTMLStatePage(w, http.StatusServiceUnavailable, "503 Offline", fmt.Sprintf("%s is offline", entry.Name), body.String())
		return
	}

	payload := map[string]any{
		"code":         "entry_offline",
		"error":        "entry_offline",
		"message":      fmt.Sprintf("connected entry %q is offline", entry.Name),
		"entry":        entry.Name,
		"owner":        entry.OwnerEmail,
		"owner_group":  entry.OwnerGroup,
		"display_name": entry.DisplayName,
		"host_kind":    entry.HostKind,
		"last_seen_at": lastSeenISO,
	}
	if !lastSeen.IsZero() {
		payload["last_seen_seconds_ago"] = secondsAgo
		payload["offline_seconds"] = secondsAgo
	}
	if isManager && reason != "" {
		payload["disconnect_reason"] = reason
	}
	writeJSONStateResponse(w, http.StatusServiceUnavailable, payload)
}
