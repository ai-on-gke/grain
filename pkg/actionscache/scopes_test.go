package actionscache

import (
	"sync"
	"testing"
)

func TestScopesNilSafety(t *testing.T) {
	var nilScopes *Scopes
	nilScopes.Grant("tok", branch("repo"))
	nilScopes.Revoke("tok")
	if _, ok := nilScopes.Repo("tok"); ok {
		t.Error("Repo on nil Scopes returned ok = true")
	}
	if n := nilScopes.Granted(); n != 0 {
		t.Errorf("Granted on nil Scopes = %d, want 0", n)
	}
}

func TestScopesWhitespaceAndEmptyHandling(t *testing.T) {
	s := NewScopes()
	s.Grant("", branch("repo"))
	s.Grant("tok", branch(""))
	s.Grant("   ", branch("   "))
	if s.Granted() != 0 {
		t.Errorf("Granted after empty grants = %d, want 0", s.Granted())
	}

	s.Grant("  token1  ", branch("  acme/repo  "))
	if repo, ok := s.Repo("token1"); !ok || repo != "acme/repo" {
		t.Errorf("Repo(token1) = %q, %v, want acme/repo, true", repo, ok)
	}
	if repo, ok := s.Repo("  token1  "); !ok || repo != "acme/repo" {
		t.Errorf("Repo(  token1  ) = %q, %v, want acme/repo, true", repo, ok)
	}

	s.Revoke("  token1  ")
	if s.Granted() != 0 {
		t.Errorf("Granted after Revoke = %d, want 0", s.Granted())
	}
}

func TestScopesConcurrentAccess(t *testing.T) {
	s := NewScopes()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			token, err := MintToken()
			if err != nil {
				t.Errorf("MintToken failed: %v", err)
				return
			}
			s.Grant(token, branch("repo"))
			if repo, ok := s.Repo(token); !ok || repo != "repo" {
				t.Errorf("Repo(%s) = %q, %v", token, repo, ok)
			}
			_ = s.Granted()
			s.Revoke(token)
		}(i)
	}
	wg.Wait()
}
