package model_test

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestPullRequestRefRoundTrips(t *testing.T) {
	ref := model.PullRequestRef{Repo: model.RepoRef{Owner: "bwsalmon", Name: "grain"}, Number: 42}
	s := ref.String()
	if s != "bwsalmon/grain#42" {
		t.Fatalf("String() = %q, want bwsalmon/grain#42", s)
	}
	got, err := model.ParsePullRequestRef(s)
	if err != nil {
		t.Fatalf("ParsePullRequestRef(%q): %v", s, err)
	}
	if got != ref {
		t.Fatalf("ParsePullRequestRef(%q) = %+v, want %+v", s, got, ref)
	}
}

func TestParsePullRequestRefRejectsMissingNumber(t *testing.T) {
	if _, err := model.ParsePullRequestRef("bwsalmon/grain"); err == nil {
		t.Fatal("expected an error for a ref with no #number")
	}
}

func TestParsePullRequestRefRejectsBadNumber(t *testing.T) {
	if _, err := model.ParsePullRequestRef("bwsalmon/grain#abc"); err == nil {
		t.Fatal("expected an error for a non-numeric #suffix")
	}
}
