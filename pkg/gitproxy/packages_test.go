package gitproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestPackageCredentialAnswersFromTheLadder(t *testing.T) {
	dir := writeCredentialSet(t,
		map[string]string{"acme/widgets": "narrow", "*": "global"},
		map[string]string{"narrow": "narrow-token", "global": "global-token"})
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}

	cred, err := useTestPatterns(set).PackageCredential("", "acme", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Token != "narrow-token" || cred.Name != "narrow" {
		t.Errorf("got %q from %q, want the ladder's own credential for acme/widgets", cred.Token, cred.Name)
	}
	if cred.Registry != DefaultPackageRegistry || cred.Username != PackageUsername {
		t.Errorf("got %s as %s, want %s as %s", cred.Registry, cred.Username, DefaultPackageRegistry, PackageUsername)
	}

	// Same ladder, a repository only the global pattern covers.
	other, err := useTestPatterns(set).PackageCredential("ghes.example.com", "other", "thing")
	if err != nil {
		t.Fatal(err)
	}
	if other.Token != "global-token" || other.Registry != "ghes.example.com" {
		t.Errorf("got %q at %s, want the global credential at the named registry", other.Token, other.Registry)
	}
}

func TestPackageCredentialRefusesWhatCannotPublish(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"acme/*": "acme"}, map[string]string{"acme": "acme-token"})
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}

	// No pattern covers it: a publish has no credential to be, and says
	// so now rather than as an anonymous push the registry refuses.
	if _, err := useTestPatterns(set).PackageCredential("", "other", "thing"); err == nil {
		t.Error("a repository no credential covers published anyway")
	} else if !strings.Contains(err.Error(), "other/thing") {
		t.Errorf("error %q does not name the repository it is about", err)
	}

	// A registry that is a URL rather than a host would arrive in a
	// `docker login` as something else entirely.
	if _, err := useTestPatterns(set).PackageCredential("https://ghcr.io/acme", "acme", "widgets"); err == nil {
		t.Error("a registry with a scheme and a path was accepted as a host")
	}

	// A fine-grained PAT cannot grant GHCR package permissions at all:
	// refuse before the job starts rather than after an hour of building.
	fgDir := writeCredentialSet(t, map[string]string{"acme/widgets": "fg-pat"}, map[string]string{"fg-pat": "github_pat_11ABCDEFG0123456789_abcdefghijklmnop"})
	fgSet, err := LoadCredentialSet(fgDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := useTestPatterns(fgSet).PackageCredential("", "acme", "widgets"); err == nil {
		t.Error("a fine-grained PAT was accepted for ghcr.io package publishing")
	} else {
		for _, want := range []string{"fg-pat", "acme/widgets", "fine-grained", "write:packages"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	}
}

func TestPackageCredentialReportsAFailedAppMint(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"acme/widgets": "ci-app"}, nil)
	writeAppCredential(t, dir, "ci-app")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	set.AppTransport = github.NewFakeTransport(github.ApiResponse{
		Status: 403, Body: []byte(`{"message":"This installation has been suspended"}`)})

	_, err = useTestPatterns(set).PackageCredential("", "acme", "widgets")
	if err == nil {
		t.Fatal("a credential with no mintable token published anyway")
	}
	// The App's own refusal, not "anonymous": the whole point of failing
	// here is that the operator is told what to fix.
	for _, want := range []string{"ci-app", "suspended"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestPackageCredentialUsesRegisteredPackageCredentialSource(t *testing.T) {
	t.Setenv("GRAIN_IN_CONTAINER", "0")
	bin := t.TempDir()
	tokenFile := filepath.Join(bin, "token")
	if err := os.WriteFile(tokenFile, []byte("pkg-access-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat '" + tokenFile + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "fake-pkg-auth"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	unreg := RegisterPackageCredentialSource([]string{"*.forge.example.test"}, PackageCredentialSource{
		Registry:    "pkg.example.test",
		RegistryEnv: "TEST_FAKE_PKG_REGISTRY",
		Username:    "oauth2accesstoken",
		Command:     "fake-pkg-auth",
		Args:        []string{"print-token"},
		LoginFix:    "`fake-pkg-auth login`",
	})
	defer unreg()

	dir := t.TempDir()
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := set.SetAmbient("corp", "git.forge.example.test", AmbientSourceAuto); err != nil {
		t.Fatal(err)
	}
	if err := set.SetHost("corp", CredentialHost{Host: "git.forge.example.test", Kind: model.GitHostGitHub}); err != nil {
		t.Fatal(err)
	}
	set.SetConnections(func(owner, repo string) (string, bool) {
		if owner == "git.forge.example.test/team" && repo == "app" {
			return "corp", true
		}
		return "", false
	})

	cred, err := set.PackageCredential("", "git.forge.example.test/team", "app")
	if err != nil {
		t.Fatalf("PackageCredential: %v", err)
	}
	if cred.Registry != "pkg.example.test" || cred.Username != "oauth2accesstoken" || cred.Token != "pkg-access-token" || cred.Name != "corp" {
		t.Fatalf("got %+v", cred)
	}

	// RegistryEnv overrides Registry when caller did not specify one.
	t.Setenv("TEST_FAKE_PKG_REGISTRY", "env-pkg.example.test")
	envCred, err := set.PackageCredential("", "git.forge.example.test/team", "app")
	if err != nil {
		t.Fatalf("PackageCredential with RegistryEnv: %v", err)
	}
	if envCred.Registry != "env-pkg.example.test" {
		t.Fatalf("Registry = %q, want env-pkg.example.test", envCred.Registry)
	}

	// Explicit registry argument takes precedence over RegistryEnv and Registry.
	override, err := set.PackageCredential("custom-pkg.example.test", "git.forge.example.test/team", "app")
	if err != nil {
		t.Fatalf("PackageCredential with override: %v", err)
	}
	if override.Registry != "custom-pkg.example.test" {
		t.Fatalf("Registry = %q, want custom-pkg.example.test", override.Registry)
	}
	t.Setenv("TEST_FAKE_PKG_REGISTRY", "")

	// An AmbientCommandSource with PackageCredential set is also used when
	// selected explicitly by source name (even with OnlyHosts=false).
	unregSrc := RegisterAmbientSource(AmbientCommandSource{
		Name:    "fake-ambient-pkg",
		Command: "fake-pkg-auth",
		Args:    []string{"git-token"},
		PackageCredential: &PackageCredentialSource{
			Registry: "ambient-pkg.example.test",
			Username: "oauth2accesstoken",
			Command:  "fake-pkg-auth",
			Args:     []string{"print-token"},
			LoginFix: "`fake-pkg-auth login`",
		},
	})
	defer unregSrc()
	if err := set.SetAmbient("corp-src", "git.other.example.test", "fake-ambient-pkg"); err != nil {
		t.Fatal(err)
	}
	if err := set.SetHost("corp-src", CredentialHost{Host: "git.other.example.test", Kind: model.GitHostGitHub}); err != nil {
		t.Fatal(err)
	}
	set.SetConnections(func(owner, repo string) (string, bool) {
		switch owner {
		case "git.forge.example.test/team":
			return "corp", repo == "app"
		case "git.other.example.test/team":
			return "corp-src", repo == "app"
		default:
			return "", false
		}
	})
	srcCred, err := set.PackageCredential("", "git.other.example.test/team", "app")
	if err != nil {
		t.Fatalf("PackageCredential via explicit ambient source: %v", err)
	}
	if srcCred.Registry != "ambient-pkg.example.test" || srcCred.Username != "oauth2accesstoken" || srcCred.Token != "pkg-access-token" || srcCred.Name != "corp-src" {
		t.Fatalf("got %+v", srcCred)
	}

	// Empty token fails with the source's LoginFix.
	if err := os.WriteFile(tokenFile, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := set.PackageCredential("", "git.forge.example.test/team", "app"); err == nil || !strings.Contains(err.Error(), "fake-pkg-auth login") {
		t.Fatalf("err = %v, want mention of fake-pkg-auth login", err)
	}
}
