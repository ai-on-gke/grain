package orchestrator_test

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

func TestParseDirectivesReadsRepoBaseAndAutoMerge(t *testing.T) {
	t.Parallel()
	body := "Please fix the thing.\n\n/repo bwsalmon/grain\n/base develop\n/auto-merge true\n"
	d, err := orchestrator.ParseDirectives(body)
	if err != nil {
		t.Fatalf("ParseDirectives: %v", err)
	}
	if d.Repo == nil || *d.Repo != (model.RepoRef{Owner: "bwsalmon", Name: "grain"}) {
		t.Fatalf("Repo = %+v, want bwsalmon/grain", d.Repo)
	}
	if d.Base != "develop" {
		t.Fatalf("Base = %q, want develop", d.Base)
	}
	if !d.AutoMerge {
		t.Fatal("AutoMerge = false, want true")
	}
}

func TestParseDirectivesLaterLineWins(t *testing.T) {
	t.Parallel()
	body := "/repo a/b\n/repo c/d\n"
	d, err := orchestrator.ParseDirectives(body)
	if err != nil {
		t.Fatalf("ParseDirectives: %v", err)
	}
	if *d.Repo != (model.RepoRef{Owner: "c", Name: "d"}) {
		t.Fatalf("Repo = %+v, want c/d", d.Repo)
	}
}

func TestParseDirectivesIgnoresProseSlashLines(t *testing.T) {
	t.Parallel()
	body := "run /usr/bin/foo and see what happens\n"
	d, err := orchestrator.ParseDirectives(body)
	if err != nil {
		t.Fatalf("ParseDirectives: %v", err)
	}
	if d.Repo != nil {
		t.Fatalf("Repo = %+v, want nil", d.Repo)
	}
}

func TestParseDirectivesRejectsBadRepo(t *testing.T) {
	t.Parallel()
	if _, err := orchestrator.ParseDirectives("/repo not-a-repo\n"); err == nil {
		t.Fatal("expected an error for a malformed /repo directive")
	}
}

func TestParseDirectivesBareAutoMergeMeansTrue(t *testing.T) {
	t.Parallel()
	d, err := orchestrator.ParseDirectives("Please fix the thing.\n\n/auto-merge\n")
	if err != nil {
		t.Fatalf("ParseDirectives: %v", err)
	}
	if !d.AutoMerge {
		t.Fatal("AutoMerge = false, want true for a bare /auto-merge line")
	}
}

func TestParseDirectivesBareAutoMergeThenFalseWins(t *testing.T) {
	t.Parallel()
	d, err := orchestrator.ParseDirectives("/auto-merge\n/auto-merge false\n")
	if err != nil {
		t.Fatalf("ParseDirectives: %v", err)
	}
	if d.AutoMerge {
		t.Fatal("AutoMerge = true, want false: the later line should win")
	}
}

func TestParseDirectivesRejectsBadAutoMerge(t *testing.T) {
	t.Parallel()
	if _, err := orchestrator.ParseDirectives("/auto-merge sure\n"); err == nil {
		t.Fatal("expected an error for a non-boolean /auto-merge directive")
	}
}

// Unlike /repo, /base and /auto-merge, /reads is repeatable -- each line
// adds a repo to the set rather than replacing the previous one.
func TestParseDirectivesReadsIsRepeatable(t *testing.T) {
	t.Parallel()
	body := "/reads owner/shared-lib\n/reads owner/schema\n"
	d, err := orchestrator.ParseDirectives(body)
	if err != nil {
		t.Fatalf("ParseDirectives: %v", err)
	}
	want := []model.RepoRef{{Owner: "owner", Name: "shared-lib"}, {Owner: "owner", Name: "schema"}}
	if len(d.Reads) != len(want) || d.Reads[0] != want[0] || d.Reads[1] != want[1] {
		t.Fatalf("Reads = %+v, want %+v", d.Reads, want)
	}
}

func TestParseDirectivesRejectsBadReads(t *testing.T) {
	t.Parallel()
	if _, err := orchestrator.ParseDirectives("/reads not-a-repo\n"); err == nil {
		t.Fatal("expected an error for a malformed /reads directive")
	}
}
