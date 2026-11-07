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

package model

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// GrainClientHeader is the HTTP header a grain client names the tool and
// session it runs in with ("jetski/1.4 session=abc123"), recorded as a
// task's or comment's FiledVia
// (design/spec/external-cli/external-cli.spec.md#XC-15).
const GrainClientHeader = "X-Grain-Client"

// MaxGrainClientLen bounds a sanitized X-Grain-Client label, in bytes.
const MaxGrainClientLen = 256

// SanitizeGrainClient is raw as a single-line label: control characters
// and newlines dropped, runs of whitespace folded to one space, trimmed,
// and cut to at most MaxGrainClientLen bytes on a rune boundary.
func SanitizeGrainClient(raw string) string {
	var b strings.Builder
	space := false
	for _, r := range raw {
		switch {
		case r == utf8.RuneError:
			continue
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) <= MaxGrainClientLen {
		return out
	}
	cut := MaxGrainClientLen
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return strings.TrimSpace(out[:cut])
}

// GrainClientLabel is the X-Grain-Client value a client sends for the
// tool label client ($GRAIN_CLIENT, "<tool>/<ver>") and session id
// ($GRAIN_CLIENT_SESSION): "<client> session=<session>", either half
// alone when the other is empty, and client as it stands when it already
// names a session. Sanitized; "" when both are empty.
func GrainClientLabel(client, session string) string {
	client, session = SanitizeGrainClient(client), SanitizeGrainClient(session)
	switch {
	case session == "" || strings.Contains(client, "session="):
		return client
	case client == "":
		return SanitizeGrainClient("session=" + session)
	}
	return SanitizeGrainClient(client + " session=" + session)
}
