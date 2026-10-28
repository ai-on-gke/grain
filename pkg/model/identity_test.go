package model_test

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestNormalizeIdentity(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"dharb@google.com":                             "dharb@google.com",
		" accounts.google.com:dharb@google.com ":       "dharb@google.com",
		"https://accounts.google.com:dharb@google.com": "dharb@google.com",
		"user:dharb@google.com":                        "dharb@google.com",
		"User:Accounts.Google.Com:Dharb@Google.com":    "Dharb@Google.com",
		"":                     "",
		"group:eng@google.com": "group:eng@google.com",
	} {
		if got := model.NormalizeIdentity(in); got != want {
			t.Errorf("NormalizeIdentity(%q) = %q, want %q", in, got, want)
		}
	}
	if model.SameIdentity("", "") {
		t.Error("SameIdentity of two empty identities is true; nobody is nobody's")
	}
	if !model.SameIdentity("accounts.google.com:Dharb@google.com", "user:dharb@GOOGLE.com") {
		t.Error("IAP's and IAM's spellings of one address are not the same identity")
	}
}
