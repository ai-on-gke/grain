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

package ui

import "net/http"

// contentSecurityPolicy is the policy every response from the daemon
// carries. The built UI is one same-origin bundle -- index.html, its
// module scripts and stylesheets, all served from static/ -- so scripts,
// connections, fonts and form posts are held to 'self'.
//
// Two allowances beyond that, each for something the UI actually does:
//
//   - style-src 'unsafe-inline': MUI styles through emotion, which writes
//     <style> elements into the document at runtime. Scripts get no such
//     allowance; an inline style cannot run code.
//   - img-src data: blob: http: https:: SVG data URIs in the bundled
//     CSS, and images in rendered Markdown (task bodies, comments) that
//     point at wherever their author linked them -- over plain http too,
//     which an internal dashboard or a LAN host often is. An image cannot
//     run script, and https: already admits every origin, so http: opens
//     no channel https: had not.
//
// frame-ancestors 'none' (and X-Frame-Options for browsers too old to
// read it) is the anti-clickjacking half: no other site may put the UI,
// with its approve/merge/delete buttons, in a frame under its own.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: http: https:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// setSecurityHeaders sets the browser-hardening headers on a response
// before any handler writes to it. They are set on API responses too,
// not only the UI's pages: a JSON body or a served attachment opened
// directly in a tab is a document like any other, and nosniff is what
// keeps a browser from deciding an attachment is HTML after all.
func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
}
