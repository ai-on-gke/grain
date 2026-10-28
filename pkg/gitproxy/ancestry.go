package gitproxy

// Where a fast-forward's proof leaves the pack (grain/task-3546).
//
// pack.go walks the commits a push carries from the new tip back to the
// old one. That is the whole proof when the new commits were made in the
// sandbox, but not when the remote already has some or all of them: a
// release cut that pushes its back-merge to its own task branch first,
// runs CI on it there, and only then fast-forwards main to the same
// commit sends an empty pack, and the walk has nothing to walk.
//
// So where the walk runs out -- at the new tip itself, when the pack
// does not carry it, or at each parent the pack does not carry -- it
// carries on in the repository being pushed to: the proxy asks that
// repository, through the same transport and credential the push itself
// goes out with, for the commits reachable from those and not from the
// old tip (a plain upload-pack fetch, `have` the old tip), and walks
// those. When the old tip is an ancestor, that pack is exactly the
// commits between the two and the walk reaches it; when it is not, the
// walk runs out again and the update is refused, as before. Commits are
// named by hashing what was fetched, so the remote can withhold the proof
// but not forge one.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxFrontierCommits bounds how many commits outside the pack the walk
// may ask the remote about. A fast-forward leaves a pack through a
// handful of parents at most; past this, it is not shown.
const maxFrontierCommits = 64

// maxUploadPackPktLines bounds the acknowledgement lines read ahead of
// the fetched pack. Asking with one `have` and no multi_ack gets one.
const maxUploadPackPktLines = 64

// frontier is where a walk from newTip through c leaves it: newTip when c
// does not carry it, or else every parent reachable from newTip through
// commits c carries that c does not carry itself. ok is false when there
// are more than limit.
func (c packCommits) frontier(newTip string, limit int) (wants []string, ok bool) {
	seen := map[string]bool{}
	queue := []string{newTip}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		parents, carried := c[id]
		if !carried {
			if wants = append(wants, id); len(wants) > limit {
				return nil, false
			}
			continue
		}
		queue = append(queue, parents...)
	}
	return wants, true
}

// upstreamCommits fetches from the repository req pushes to the commits
// reachable from wants and not from have, as packCommits: the local
// repository when req is served by it, or else the host req names, with
// the credential its push would be forwarded with.
func (p *GitProxy) upstreamCommits(ctx context.Context, sandbox string, req GitRequest, wants []string, have string, hashLen int) (packCommits, error) {
	headers := map[string]string{
		"Content-Type": "application/x-git-upload-pack-request",
		"Accept":       "application/x-git-upload-pack-result",
	}
	body := uploadPackRequest(wants, have, hashLen)
	record := func(credential, outcome string) {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: "git-upload-pack", Credential: credential, Outcome: "fast-forward check: " + outcome,
		})
	}

	var upstream UpstreamResponse
	var err error
	if p.Local != nil && req.Host == "" && p.Local.Matches(req.Owner, req.Repo) {
		upstream, err = p.Local.ForwardRepo(req.Owner, req.Repo, "POST", "git-upload-pack", "", headers, body)
		if err != nil {
			record("grain controller", fmt.Sprintf("error: local repository failed: %v", err))
			return nil, fmt.Errorf("asking the local repository: %w", err)
		}
		record("grain controller", fmt.Sprintf("served locally: %d", upstream.Status))
	} else {
		credential, notConfigured, err := p.selectCredential(ctx, sandbox, req.Host, req.Owner, req.Repo)
		switch {
		case err != nil:
			return nil, err
		case notConfigured != "":
			return nil, errors.New(notConfigured)
		case credential.SSH != nil:
			return nil, errors.New("the remote is reached over SSH, which this check does not ask")
		}
		forwarder, err := p.forwarderFor(credential)
		if err != nil {
			return nil, err
		}
		upstream, err = forwarder.Forward("POST", fmt.Sprintf("/%s/%s.git/git-upload-pack", req.Owner, req.Repo),
			"", headers, body, credential.Token)
		if err != nil {
			record(credential.Name, fmt.Sprintf("error: forwarding failed: %v", err))
			return nil, fmt.Errorf("asking the remote: %w", err)
		}
		record(credential.Name, forwardedOutcome(credential, upstream.Status))
	}

	var stream io.Reader
	if upstream.Stream != nil {
		defer upstream.Stream.Close()
		stream = upstream.Stream
	} else {
		stream = strings.NewReader(string(upstream.Body))
	}
	if upstream.Status != 200 {
		return nil, fmt.Errorf("the remote answered %d", upstream.Status)
	}
	// What the remote may send is held to what a push may: the walk
	// bounds objects and inflation, but not the bytes it reads.
	return readUploadPackResult(io.LimitReader(stream, p.maxBodyBytes("git-receive-pack")), hashLen)
}

// uploadPackRequest is a protocol-v0 upload-pack request for wants, with
// have as the one commit the client already has. It asks for no
// side-band and no thin pack, so the pack comes back raw and whole.
func uploadPackRequest(wants []string, have string, hashLen int) []byte {
	var b strings.Builder
	pkt := func(line string) { fmt.Fprintf(&b, "%04x%s", len(line)+4, line) }
	capabilities := " ofs-delta no-progress agent=grain-proxy"
	if hashLen == 32 {
		capabilities += " object-format=sha256"
	}
	for i, w := range wants {
		if i == 0 {
			pkt("want " + w + capabilities + "\n")
		} else {
			pkt("want " + w + "\n")
		}
	}
	b.WriteString("0000")
	pkt("have " + have + "\n")
	pkt("done\n")
	return []byte(b.String())
}

// readUploadPackResult reads an upload-pack response to uploadPackRequest:
// its ACK or NAK, then the pack. An ERR line -- a want the remote does not
// have, or will not serve -- is the error it says.
func readUploadPackResult(body io.Reader, hashLen int) (packCommits, error) {
	r := bufio.NewReaderSize(body, 64<<10)
	for i := 0; ; i++ {
		head, err := r.Peek(4)
		if err != nil {
			return nil, errors.New("the remote's answer ended before its pack")
		}
		if string(head) == "PACK" {
			return readPackStream(r, hashLen)
		}
		if i >= maxUploadPackPktLines {
			return nil, errors.New("the remote's answer has no pack")
		}
		n, err := strconv.ParseUint(string(head), 16, 16)
		if err != nil {
			return nil, fmt.Errorf("malformed pkt-line length %q in the remote's answer", head)
		}
		if _, err := r.Discard(4); err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		if n < 4 {
			return nil, fmt.Errorf("malformed pkt-line length %d in the remote's answer", n)
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(r, line); err != nil {
			return nil, errors.New("the remote's answer ended before its pack")
		}
		if msg, ok := strings.CutPrefix(string(line), "ERR "); ok {
			return nil, fmt.Errorf("the remote refused: %s", strings.TrimSpace(msg))
		}
	}
}
