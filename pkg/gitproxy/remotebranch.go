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

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// maxRefAdvertisementBytes bounds what RemoteBranchExists reads of a
// remote's ref advertisement: the fetch body limit, which no list of
// branches a person keeps comes near.
const maxRefAdvertisementBytes = DefaultMaxFetchBodyBytes

// RemoteBranchExists is `git ls-remote --heads` for one branch of
// owner/repo on h, asked with credential through the same transport the
// git proxy forwards that credential's requests with. It is how grain
// reads a branch on a host with no forge API to ask (a plain git host):
// a GET of info/refs, the first half of any clone.
//
// A repository the remote will not show -- not there, or not readable
// with credential -- is an error rather than false: "no such branch" is
// only what an advertisement that lacks it says.
func RemoteBranchExists(h CredentialHost, credential Credential, owner, repo, branch string) (bool, error) {
	refs, err := remoteRefs(h, credential, owner, repo)
	if err != nil {
		return false, err
	}
	_, ok := refs["refs/heads/"+branch]
	return ok, nil
}

// RemoteBranches is `git ls-remote --heads` for owner/repo on h: every
// branch the remote advertises, sorted. It is the branch pickers' listing
// for a repository on a plain git host, where GitHub's
// /repos/{owner}/{repo}/branches would ask about some other repository
// altogether. Errors as RemoteBranchExists's do.
func RemoteBranches(h CredentialHost, credential Credential, owner, repo string) ([]string, error) {
	refs, err := remoteRefs(h, credential, owner, repo)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(refs))
	for ref := range refs {
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok && name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// remoteRefs is owner/repo's ref advertisement on h, read with credential:
// the first half of any clone.
func remoteRefs(h CredentialHost, credential Credential, owner, repo string) (map[string]string, error) {
	if credential.SSH != nil {
		return nil, fmt.Errorf("%s/%s is reached over SSH, and its branches are read over HTTP(S) only", owner, repo)
	}
	upstream, err := hostForwarder(h, credential).Forward("GET", fmt.Sprintf("/%s/%s.git/info/refs", owner, repo),
		"service=git-upload-pack", nil, nil, credential.Token)
	if err != nil {
		return nil, fmt.Errorf("listing %s/%s's branches on %s: %w", owner, repo, h.Host, err)
	}
	var body io.Reader = strings.NewReader(string(upstream.Body))
	if upstream.Stream != nil {
		defer upstream.Stream.Close()
		body = upstream.Stream
	}
	if upstream.Status != 200 {
		return nil, fmt.Errorf("listing %s/%s's branches on %s: the remote answered %d", owner, repo, h.Host, upstream.Status)
	}
	refs, err := readRefAdvertisement(io.LimitReader(body, maxRefAdvertisementBytes))
	if err != nil {
		return nil, fmt.Errorf("listing %s/%s's branches on %s: %w", owner, repo, h.Host, err)
	}
	return refs, nil
}

// readRefAdvertisement reads a smart-HTTP upload-pack advertisement
// (protocol v0: the "# service=" line and its flush, then one
// "<id> <ref>" pkt-line per ref, the first carrying capabilities after a
// NUL) into ref name -> object id.
func readRefAdvertisement(body io.Reader) (map[string]string, error) {
	r := bufio.NewReader(body)
	refs := map[string]string{}
	flushes := 0
	for flushes < 2 {
		var head [4]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			if flushes == 1 && errors.Is(err, io.EOF) {
				// A server that ends the advertisement at EOF rather
				// than with its flush.
				break
			}
			return nil, errors.New("the remote's ref advertisement ended early")
		}
		n, err := strconv.ParseUint(string(head[:]), 16, 16)
		if err != nil {
			return nil, fmt.Errorf("malformed pkt-line length %q in the remote's ref advertisement", head)
		}
		if n == 0 {
			flushes++
			continue
		}
		if n < 4 {
			return nil, fmt.Errorf("malformed pkt-line length %d in the remote's ref advertisement", n)
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(r, line); err != nil {
			return nil, errors.New("the remote's ref advertisement ended early")
		}
		text := strings.TrimSuffix(string(line), "\n")
		if msg, ok := strings.CutPrefix(text, "ERR "); ok {
			return nil, fmt.Errorf("the remote refused: %s", strings.TrimSpace(msg))
		}
		if flushes == 0 {
			if !strings.HasPrefix(text, "# service=") {
				return nil, errors.New("the remote did not answer as a smart-HTTP git server")
			}
			continue
		}
		text, _, _ = strings.Cut(text, "\x00")
		if id, name, ok := strings.Cut(text, " "); ok {
			refs[name] = id
		}
	}
	return refs, nil
}
