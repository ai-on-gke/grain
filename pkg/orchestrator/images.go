package orchestrator

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// A pull request's description is built from its branch's commit
// messages (description.go), and a commit message cannot carry an image.
// The images a run attaches to what it says -- the before and after
// screenshots a UI change is asked for (docs/ui-screenshots.md) -- were
// relayed into the task's conversation in grain and nowhere else, so a
// reviewer reading the pull request on GitHub never saw them.
//
// This puts them on the pull request. A description can only show an
// image something serves to whoever is reading it, and grain's own
// attachment endpoint sits behind grain's access gate, where GitHub's
// renderer cannot reach. The repository the pull request is in already
// serves its own objects to exactly the people who can read the pull
// request, so the images are committed there: one parentless commit per
// note, on a ref outside refs/heads and refs/tags
// (github.FilePublisher), which nothing fetches, builds or merges, and
// which keeps the commit alive for as long as the description links to
// it. The description gets a marked section linking each image by that
// commit's sha.

const (
	imagesStartPrefix = "<!-- grain:images"
	imagesEnd         = "<!-- /grain:images -->"
)

// imagesStart opens the section for the note commentID: the id is what
// tells a later finish that this note's images are already up.
func imagesStart(commentID int64) string {
	return fmt.Sprintf("%s comment=%d -->", imagesStartPrefix, commentID)
}

// imagesRef is the ref one note's images are published on, under the
// task's branch name so two deployments working in one repository never
// write each other's.
func imagesRef(task model.Task, commentID int64) string {
	return fmt.Sprintf("refs/grain-images/%s/%d", task.BranchName(), commentID)
}

// publishableImage reports whether an attachment is an image a GitHub
// description renders. SVG is left out: GitHub serves a repository's SVG
// as text, not as an image.
var publishableImage = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

var imageExtension = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

// imageNote is the newest thing a run said on its task that carried
// images, with the images themselves.
type imageNote struct {
	CommentID int64
	At        time.Time
	Images    []github.PublishedFile
}

// latestImageNote is the newest comment relayed on behalf of task's own
// agent (relayCommentWithAttachments) that carries at least one image,
// or nil when there is none. The newest, not every one: a run that
// re-shot its screenshots after review has replaced the earlier set, and
// a description showing both would make the reviewer work out which is
// current. A human's attachments, and the task's own, are not the run's
// account of its change and are never published.
func latestImageNote(ctx context.Context, store *model.Store, task model.Task) (*imageNote, error) {
	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	metas, err := store.AttachmentMetas(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		if c.Handover || c.Author.OnBehalfOf == nil || c.Author.OnBehalfOf.Kind != model.PrincipalAgent ||
			c.Author.OnBehalfOf.ID != task.ID {
			continue
		}
		note := imageNote{CommentID: c.ID, At: c.CreatedAt}
		taken := map[string]bool{}
		for _, m := range metas {
			if m.CommentID == nil || *m.CommentID != c.ID || !looksLikeImage(m.ContentType, m.Filename) {
				continue
			}
			a, err := store.GetAttachment(ctx, task.ID, m.ID)
			if err != nil {
				return nil, err
			}
			if a == nil || !publishableImage[http.DetectContentType(a.Content)] {
				continue
			}
			note.Images = append(note.Images, github.PublishedFile{
				Path: uniqueImagePath(a.Filename, taken), Content: a.Content,
			})
		}
		if len(note.Images) > 0 {
			return &note, nil
		}
	}
	return nil, nil
}

// looksLikeImage is the cheap first look, on metadata, before any
// content is read: what the run said the file was, or -- since the
// content type defaults to application/octet-stream when a run leaves it
// out -- what the file is called. The bytes have the last word.
func looksLikeImage(contentType, filename string) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "image/") {
		return true
	}
	return imageExtension[strings.ToLower(path.Ext(filename))]
}

var unsafePathChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// uniqueImagePath is filename made safe to sit in a tree and a URL
// unescaped, and distinct from every path already taken: a before and an
// after shot are both called contact-sheet.png.
func uniqueImagePath(filename string, taken map[string]bool) string {
	name := strings.Trim(unsafePathChars.ReplaceAllString(filename, "-"), "-.")
	if name == "" {
		name = "image"
	}
	candidate := name
	for n := 2; taken[candidate]; n++ {
		candidate = fmt.Sprintf("%d-%s", n, name)
	}
	taken[candidate] = true
	return candidate
}

// repoWebURL is where repo's pages live, read off a pull request's own
// link so a GitHub Enterprise host is linked to itself rather than to
// github.com.
func repoWebURL(htmlURL string, repo model.RepoRef) string {
	if i := strings.LastIndex(htmlURL, "/pull/"); i > 0 {
		return htmlURL[:i]
	}
	return fmt.Sprintf("https://github.com/%s/%s", repo.Owner, repo.Name)
}

// renderImages is the description's section for one published note.
func renderImages(task model.Task, web, sha string, note imageNote) string {
	var b strings.Builder
	b.WriteString(imagesStart(note.CommentID) + "\n")
	b.WriteString("### Images\n\n")
	fmt.Fprintf(&b, "Attached by the agent to its note on grain task %s.\n", task.ID)
	for _, img := range note.Images {
		fmt.Fprintf(&b, "\n![%s](%s/blob/%s/%s?raw=true)\n", img.Path, web, sha, img.Path)
	}
	b.WriteString(imagesEnd)
	return b.String()
}

// imagesSection is the images section body already carries, or "".
func imagesSection(body string) string {
	start := strings.Index(body, imagesStartPrefix)
	if start < 0 {
		return ""
	}
	end := strings.Index(body[start:], imagesEnd)
	if end < 0 {
		return ""
	}
	return body[start : start+end+len(imagesEnd)]
}

// withImages puts section into body under the description and above the
// lineage section and grain's footer, replacing a section an earlier
// write put there. The footer stays the last line, which is what
// grainAuthored reads.
func withImages(body, section string) string {
	if section == "" {
		return body
	}
	if old := imagesSection(body); old != "" {
		return strings.Replace(body, old, section, 1)
	}
	if i := strings.Index(body, lineageStart); i >= 0 {
		return body[:i] + section + "\n\n" + body[i:]
	}
	if i := strings.LastIndex(body, "\n---\n"); i >= 0 {
		return body[:i] + "\n\n" + section + "\n" + body[i:]
	}
	return section + "\n\n---\n" + body
}

// publishImages puts the images from the newest note task's runs left
// on its open pull request number: published to the repository
// (github.FilePublisher) and linked from the description.
//
// Nothing here can fail a finish, for refreshPullRequest's reason: the
// images are still in grain's conversation either way, so every error
// is a log line. It writes nothing on a description a human has taken
// over (grainAuthored), and nothing when the description already shows
// this note's images, so a retried finish publishes nothing twice.
func publishImages(ctx context.Context, store *model.Store, client github.Client, task model.Task, number int) {
	publisher, ok := client.(github.FilePublisher)
	if !ok || store == nil || task.Target == nil {
		return
	}
	note, err := latestImageNote(ctx, store, task)
	if err != nil {
		log.Printf("orchestrator: reading %s's images for its pull request: %v", task.ID, err)
		return
	}
	if note == nil {
		return
	}
	repo := *task.Target
	detail, err := client.GetPullRequest(repo.Owner, repo.Name, number)
	if err != nil {
		log.Printf("orchestrator: reading %s/%s#%d to add images to it: %v", repo.Owner, repo.Name, number, err)
		return
	}
	if !grainAuthored(detail.Body, task.ID) || strings.Contains(detail.Body, imagesStart(note.CommentID)) {
		return
	}
	sha, err := publisher.PublishFiles(repo.Owner, repo.Name, imagesRef(task, note.CommentID),
		fmt.Sprintf("Images attached to grain task %s\n\nPublished for %s/%s#%d's description. "+
			"This commit is on no branch and is never merged.", task.ID, repo.Owner, repo.Name, number),
		note.At, note.Images)
	if err != nil {
		log.Printf("orchestrator: publishing %s's images to %s/%s: %v -- "+
			"they are in grain's conversation only", task.ID, repo.Owner, repo.Name, err)
		return
	}
	body := withImages(detail.Body, renderImages(task, repoWebURL(detail.HTMLURL, repo), sha, *note))
	if err := client.UpdatePullRequestBody(repo.Owner, repo.Name, number, body); err != nil {
		log.Printf("orchestrator: adding %s's images to %s/%s#%d: %v", task.ID, repo.Owner, repo.Name, number, err)
	}
}
