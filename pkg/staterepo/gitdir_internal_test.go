package staterepo

import (
	"context"
	"os"
	"path/filepath"
	stdsync "sync"
	"testing"
)

func TestGitDirFileCachesTheGitDirectory(t *testing.T) {
	t.Parallel()
	repo := aRepoForNotices(t)
	ctx := context.Background()
	want, err := repo.gitDirFile(ctx, "first")
	if err != nil {
		t.Fatalf("finding git directory: %v", err)
	}

	gitDir := filepath.Join(repo.Dir(), ".git")
	moved := gitDir + ".moved"
	if err := os.Rename(gitDir, moved); err != nil {
		t.Fatalf("moving git directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(moved, gitDir) })

	got, err := repo.gitDirFile(ctx, "second")
	if err != nil {
		t.Fatalf("using cached git directory: %v", err)
	}
	if got != filepath.Join(filepath.Dir(want), "second") {
		t.Fatalf("git directory = %q, want %q", got, filepath.Join(filepath.Dir(want), "second"))
	}
}

func TestGitDirFileCacheIsConcurrent(t *testing.T) {
	t.Parallel()
	repo := aRepoForNotices(t)
	ctx := context.Background()

	var wg stdsync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := repo.gitDirFile(ctx, "marker"); err != nil {
				t.Errorf("finding git directory: %v", err)
			}
		}()
	}
	wg.Wait()
}
