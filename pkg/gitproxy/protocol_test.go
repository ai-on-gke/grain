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

package gitproxy

import "testing"

func TestParsePathParsesInfoRefs(t *testing.T) {
	req, ok := ParsePath("/owner/repo.git/info/refs")
	if !ok || req != (GitRequest{Owner: "owner", Repo: "repo", Action: "info/refs"}) {
		t.Fatalf("got %+v, %v", req, ok)
	}
}

func TestParsePathWorksWithoutDotGitSuffix(t *testing.T) {
	req, ok := ParsePath("/owner/repo/git-upload-pack")
	if !ok || req != (GitRequest{Owner: "owner", Repo: "repo", Action: "git-upload-pack"}) {
		t.Fatalf("got %+v, %v", req, ok)
	}
}

func TestParsePathParsesGitReceivePack(t *testing.T) {
	req, ok := ParsePath("/owner/repo.git/git-receive-pack")
	if !ok || req != (GitRequest{Owner: "owner", Repo: "repo", Action: "git-receive-pack"}) {
		t.Fatalf("got %+v, %v", req, ok)
	}
}

func TestParsePathReadsAHost(t *testing.T) {
	for path, want := range map[string]GitRequest{
		"/ghe.example.com/owner/repo.git/info/refs":        {Host: "ghe.example.com", Owner: "owner", Repo: "repo", Action: "info/refs"},
		"/ghe.example.com:8443/owner/repo/git-upload-pack": {Host: "ghe.example.com:8443", Owner: "owner", Repo: "repo", Action: "git-upload-pack"},
		"/127.0.0.1:9000/owner/repo.git/git-receive-pack":  {Host: "127.0.0.1:9000", Owner: "owner", Repo: "repo", Action: "git-receive-pack"},
		"/owner/repo.git/info/refs":                        {Owner: "owner", Repo: "repo", Action: "info/refs"},
	} {
		got, ok := ParsePath(path)
		if !ok || got != want {
			t.Errorf("ParsePath(%q) = %+v, %v; want %+v", path, got, ok, want)
		}
	}
	// A first segment with no dot or colon is an owner, so a host-less
	// owner/repo/extra path is not a host-qualified one.
	if _, ok := ParsePath("/a/owner/repo.git/info/refs"); ok {
		t.Errorf("ParsePath read a dotless first segment as a host")
	}
}

func TestParsePathRejectsAnythingElse(t *testing.T) {
	for _, path := range []string{
		"/owner/repo.git/objects/info/packs",
		"/owner/repo.git/HEAD",
		"/",
		"/just-one-segment",
	} {
		if _, ok := ParsePath(path); ok {
			t.Errorf("ParsePath(%q) should not match", path)
		}
	}
}

func TestIsValidGitRequestNeedsAGitUserAgent(t *testing.T) {
	if IsValidGitRequest("Mozilla/5.0", "*/*", "info/refs") {
		t.Error("a browser user agent should be rejected")
	}
	if !IsValidGitRequest("git/2.39.2", "*/*", "info/refs") {
		t.Error("a git user agent should be accepted for info/refs")
	}
}

func TestIsValidGitRequestChecksAcceptForPackEndpoints(t *testing.T) {
	const ua = "git/2.39.2"
	if !IsValidGitRequest(ua, "application/x-git-upload-pack-result", "git-upload-pack") {
		t.Error("matching Accept header should be accepted")
	}
	if IsValidGitRequest(ua, "application/x-git-receive-pack-result", "git-upload-pack") {
		t.Error("mismatched Accept header should be rejected")
	}
	if IsValidGitRequest(ua, "*/*", "git-upload-pack") {
		t.Error("*/* should be rejected for a pack endpoint")
	}
}

// What a refusal looks like on the wire. git prints the body of a
// non-200 response verbatim, as "remote: " lines, and only when it is
// told the body is text -- so a length prefix in front of the message
// is noise an operator reads, and a missing Content-Type is a message
// they never see at all.
func TestDenialIsPlainTextGitWillPrint(t *testing.T) {
	resp := Denial(500, "no credential configured for owner/repo")
	if resp.Status != 500 {
		t.Errorf("status = %d, want 500", resp.Status)
	}
	if got := string(resp.Body); got != "no credential configured for owner/repo\n" {
		t.Errorf("body = %q, want the bare message and a newline", got)
	}
	if got := resp.Headers["Content-Type"]; got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want plain text", got)
	}
}
