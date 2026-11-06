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
	"regexp"
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

// TaskTokenSource is an optional interface a TokenSource may implement
// to return a copy of itself scoped to a specific task's Git credential
// (such as the task author's personal PAT or a task capability grant).
type TaskTokenSource interface {
	ForTask(taskID string) TokenSource
}

// TaskClientScoper is an optional interface a Client wrapper may implement
// to pass a task ID through to the RESTClient underneath it.
type TaskClientScoper interface {
	ForTask(taskID string) Client
}

// ForTask returns a copy of c whose requests authenticate and sign with
// taskID's resolved Git credential when c's TokenSource implements
// TaskTokenSource. A client that is not a RESTClient (or whose TokenSource
// is not task-aware) is returned unchanged.
func ForTask(c Client, taskID string) Client {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || c == nil {
		return c
	}
	switch c := c.(type) {
	case *RESTClient:
		if c != nil {
			return c.ForTask(taskID)
		}
	case TaskClientScoper:
		return c.ForTask(taskID)
	}
	return c
}

// ForTask returns a copy of c scoped to taskID when c.Tokens implements
// TaskTokenSource, sharing c's Transport and Caller.
func (c *RESTClient) ForTask(taskID string) *RESTClient {
	taskID = strings.TrimSpace(taskID)
	if c == nil || c.Tokens == nil || taskID == "" {
		return c
	}
	ts, ok := c.Tokens.(TaskTokenSource)
	if !ok {
		return c
	}
	out := *c
	out.Tokens = ts.ForTask(taskID)
	return &out
}

var headBranchTaskIDPattern = regexp.MustCompile(`^grain/(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?/)?task-([0-9]+)(?:-[a-z0-9][a-z0-9-]*)?$`)

func taskIDFromHeadBranch(head string) string {
	head = strings.TrimSpace(head)
	if _, b, ok := strings.Cut(head, ":"); ok {
		head = b
	}
	head = strings.TrimPrefix(head, "refs/heads/")
	if m := headBranchTaskIDPattern.FindStringSubmatch(head); m != nil {
		return m[1]
	}
	return ""
}

// AuthorSignatureFor returns the human author signature configured on client
// for (owner, repo), or "" when client is not backed by an
// AuthorSignatureSource or the credential is a bot/anonymous credential.
func AuthorSignatureFor(client Client, owner, repo string) string {
	rc, ok := client.(*RESTClient)
	if !ok || rc == nil {
		return ""
	}
	return rc.authorSignature(owner, repo)
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
