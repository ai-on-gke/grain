package github

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PublishedFile is one file FilePublisher writes: its path in the
// commit's tree and its bytes.
type PublishedFile struct {
	Path    string
	Content []byte
}

// FilePublisher is the optional half of Client that puts files in a
// repository without putting them on any branch: one parentless commit
// carrying files, pointed at by ref, which must lie outside refs/heads
// and refs/tags so that nothing fetches, builds or merges it. It is how
// the images a run attaches to its closing note reach the pull request
// it opened (orchestrator.publishImages): a description can only show an
// image something serves, and the repository the pull request is in
// serves its own objects to exactly the people who can read the pull
// request.
//
// Optional for MergeQueuer's reason: only that one caller wants it, and
// a client without it leaves the images in grain's own conversation,
// which is where they were before.
//
// The commit is stamped at, with a fixed author, so the same files
// published again produce the same commit: a finish that is retried
// after publishing writes nothing new.
type FilePublisher interface {
	PublishFiles(owner, repo, ref, message string, at time.Time, files []PublishedFile) (sha string, err error)
}

var _ FilePublisher = (*RESTClient)(nil)

// publishAuthor is who a published commit says wrote it. The address is
// deliberately one that names no GitHub account, so the commit is never
// attributed to somebody's profile.
var publishAuthor = map[string]string{"name": "grain", "email": "grain@noreply.invalid"}

// PublishFiles writes files through GitHub's git-database API -- a blob
// each, one tree, one parentless commit -- and points ref at the commit,
// creating it or moving it there. It returns the commit's sha, which is
// what a link to one of the files names.
func (c *RESTClient) PublishFiles(owner, repo, ref, message string, at time.Time, files []PublishedFile) (string, error) {
	if !strings.HasPrefix(ref, "refs/") || strings.HasPrefix(ref, "refs/heads/") || strings.HasPrefix(ref, "refs/tags/") {
		return "", fmt.Errorf("github: publishing to %q: the ref must be outside refs/heads and refs/tags", ref)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("github: publishing to %q: no files", ref)
	}
	if err := c.gitOnly(owner, repo); err != nil {
		return "", err
	}
	type entry struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	tree := make([]entry, 0, len(files))
	for _, f := range files {
		sha, err := c.createGitObject(owner, repo, "blobs", map[string]string{
			"content": base64.StdEncoding.EncodeToString(f.Content), "encoding": "base64",
		})
		if err != nil {
			return "", fmt.Errorf("github: publishing %s: %w", f.Path, err)
		}
		tree = append(tree, entry{Path: f.Path, Mode: "100644", Type: "blob", SHA: sha})
	}
	treeSHA, err := c.createGitObject(owner, repo, "trees", map[string]any{"tree": tree})
	if err != nil {
		return "", fmt.Errorf("github: publishing a tree for %s: %w", ref, err)
	}
	stamp := map[string]string{"name": publishAuthor["name"], "email": publishAuthor["email"],
		"date": at.UTC().Format(time.RFC3339)}
	commitSHA, err := c.createGitObject(owner, repo, "commits", map[string]any{
		"message": message, "tree": treeSHA, "parents": []string{},
		"author": stamp, "committer": stamp,
	})
	if err != nil {
		return "", fmt.Errorf("github: publishing a commit for %s: %w", ref, err)
	}
	if err := c.setRef(owner, repo, ref, commitSHA); err != nil {
		return "", fmt.Errorf("github: pointing %s at %s: %w", ref, commitSHA, err)
	}
	return commitSHA, nil
}

// createGitObject POSTs payload to one of the git-database collections
// (blobs, trees, commits) and returns the sha GitHub gave the object.
func (c *RESTClient) createGitObject(owner, repo, kind string, payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	resp, err := c.request("POST", fmt.Sprintf("/repos/%s/%s/git/%s", owner, repo, kind),
		c.headers(owner, repo, true), body)
	if err != nil {
		return "", err
	}
	if resp.Status != 201 {
		return "", &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var created struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(resp.Body, &created); err != nil || created.SHA == "" {
		return "", fmt.Errorf("creating a git %s: no sha in %s", strings.TrimSuffix(kind, "s"), resp.Body)
	}
	return created.SHA, nil
}

// setRef creates ref at sha, or -- GitHub answering 422 because it
// already exists -- moves it there.
func (c *RESTClient) setRef(owner, repo, ref, sha string) error {
	payload, _ := json.Marshal(map[string]string{"ref": ref, "sha": sha})
	resp, err := c.request("POST", fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo),
		c.headers(owner, repo, true), payload)
	if err != nil {
		return err
	}
	if resp.Status == 201 {
		return nil
	}
	if resp.Status != 422 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	payload, _ = json.Marshal(map[string]any{"sha": sha, "force": true})
	resp, err = c.request("PATCH", fmt.Sprintf("/repos/%s/%s/git/%s", owner, repo, ref),
		c.headers(owner, repo, true), payload)
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return &Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	return nil
}
