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

package github

import (
	"fmt"
	"strings"
)

// AuthorSignatureSource is an optional interface a TokenSource may implement
// to report the human user whose delegated credential authenticates requests
// for (owner, repo). When AuthorSignature returns a non-empty name, RESTClient
// signs new pull request descriptions, comments, and reviews with an explicit
// attribution line noting that the action was performed in that user's name
// via Grain.
type AuthorSignatureSource interface {
	AuthorSignature(owner, repo string) string
}

func (c *RESTClient) authorSignature(owner, repo string) string {
	if c == nil || c.Tokens == nil {
		return ""
	}
	src, ok := c.Tokens.(AuthorSignatureSource)
	if !ok {
		return ""
	}
	return strings.TrimSpace(src.AuthorSignature(owner, repo))
}

func (c *RESTClient) signPullRequestBody(owner, repo, body string) string {
	return SignPullRequestBody(body, c.authorSignature(owner, repo))
}

func (c *RESTClient) signCommentBody(owner, repo, body string) string {
	return SignCommentBody(body, c.authorSignature(owner, repo))
}

// PullRequestSignatureLine returns the plain-text attribution line placed in
// pull request descriptions when acting on behalf of author.
func PullRequestSignatureLine(author string) string {
	author = strings.TrimSpace(author)
	if author == "" {
		return ""
	}
	return fmt.Sprintf("Signed by %s (via Grain)", author)
}

// CommentSignatureLine returns the markdown attribution line appended to
// comments and reviews when acting on behalf of author.
func CommentSignatureLine(author string) string {
	author = strings.TrimSpace(author)
	if author == "" {
		return ""
	}
	return fmt.Sprintf("*Signed by %s (via Grain)*", author)
}

const grainTaskFooterPrefix = "Automated change for grain task "

// SignPullRequestBody adds `Signed by <author> (via Grain)` to a pull request
// body idempotently.
//
// When body ends with Grain's own task footer (`Automated change for grain task <id>.`),
// the signature line is placed immediately above that final footer line (inside
// the trailing `---` footer block when present). Keeping the task footer as
// the last line preserves orchestrator.grainAuthored, withLineage, and
// withImages across pull request description refreshes.
func SignPullRequestBody(body, author string) string {
	line := PullRequestSignatureLine(author)
	if line == "" || strings.Contains(body, line) {
		return body
	}
	return insertPullRequestSignatureLine(body, line)
}

// PreservePullRequestSignature carries over any `Signed by ... (via Grain)`
// line present in existingBody onto body, so pull request description
// refreshes recognise an unchanged signed description without issuing a
// redundant PATCH.
func PreservePullRequestSignature(body, existingBody string) string {
	line := extractPullRequestSignatureLine(existingBody)
	if line == "" || strings.Contains(body, line) {
		return body
	}
	return insertPullRequestSignatureLine(body, line)
}

func extractPullRequestSignatureLine(body string) string {
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "Signed by ") && strings.HasSuffix(trimmed, " (via Grain)") {
			return trimmed
		}
	}
	return ""
}

func insertPullRequestSignatureLine(body, line string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return line
	}

	// Check whether the last non-empty line is Grain's task footer.
	lines := strings.Split(trimmed, "\n")
	lastLine := strings.TrimSpace(lines[len(lines)-1])
	if strings.HasPrefix(lastLine, grainTaskFooterPrefix) {
		if i := strings.LastIndex(trimmed, "\n---\n"); i >= 0 {
			return trimmed[:i+len("\n---\n")] + line + "\n" + trimmed[i+len("\n---\n"):]
		}
		if len(lines) == 1 {
			return line + "\n" + lastLine
		}
		return strings.Join(lines[:len(lines)-1], "\n") + "\n" + line + "\n" + lastLine
	}

	return strings.TrimRight(body, "\n") + "\n\n---\n" + line
}

// SignCommentBody appends `*Signed by <author> (via Grain)*` to a comment or
// review body idempotently.
func SignCommentBody(body, author string) string {
	sig := CommentSignatureLine(author)
	plain := PullRequestSignatureLine(author)
	if sig == "" || strings.Contains(body, sig) || (plain != "" && strings.Contains(body, plain)) {
		return body
	}
	trimmed := strings.TrimRight(body, " \t\n")
	if trimmed == "" {
		return sig
	}
	return trimmed + "\n\n---\n" + sig
}
