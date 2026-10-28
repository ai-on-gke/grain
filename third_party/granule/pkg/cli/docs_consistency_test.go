package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/granulefile"
)

// All registered host subcommands in the granule CLI.
var allSubcommands = []struct {
	category string
	subcmd   string
	keyFlag  string
}{
	{"vm", "create", "-backend"},
	{"vm", "run", "-keep-on-failure"},
	{"vm", "exec", "-connect-timeout"},
	{"vm", "shell", "-connect-timeout"},
	{"vm", "wait", "-timeout"},
	{"vm", "status", "-timeout"},
	{"vm", "update", "-memory-mb"},
	{"vm", "delete", "-static-pod-path"},
	{"vm", "list", "-state-dir"},
	{"guest", "build", "-setup"},
	{"guest", "pack", "-packer-image"},
	{"guest", "rebase", "-boot"},
	{"guest", "verify", "-checks"},
	{"guest", "flatten", "-base"},
	{"guest", "split", "-extent-size-mb"},
	{"guest", "assemble", "-verify"},
	{"granulefile", "check", "-level"},
	{"granulefile", "build", "-guest-image"},
}

// TestCLISubcommandsAndHelpAudit verifies that all subcommands are listed in
// the top-level usage string and that every subcommand handles -h with exit code 0.
func TestCLISubcommandsAndHelpAudit(t *testing.T) {
	var topHelp bytes.Buffer
	code := Run(context.Background(), []string{"-h"}, strings.NewReader(""), &topHelp, &topHelp)
	if code != 0 {
		t.Fatalf("Run -h exit code = %d, want 0", code)
	}
	topHelpStr := topHelp.String()

	for _, sc := range allSubcommands {
		cmdName := "granule " + sc.category + " " + sc.subcmd
		if !strings.Contains(topHelpStr, sc.category+" "+sc.subcmd) {
			t.Errorf("top-level usage does not mention %q", cmdName)
		}

		t.Run(sc.category+"_"+sc.subcmd+"_help", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := []string{sc.category, sc.subcmd, "-h"}
			exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
			if exitCode != 0 {
				t.Errorf("%s -h returned exit code %d, want 0", cmdName, exitCode)
			}
			out := stdout.String() + stderr.String()
			if !strings.Contains(out, sc.keyFlag) {
				t.Errorf("%s -h output missing expected flag %s:\n%s", cmdName, sc.keyFlag, out)
			}
			if strings.Contains(out, "granule: flag: help requested") {
				t.Errorf("%s -h printed unwanted error message:\n%s", cmdName, out)
			}
		})
	}
}

// TestDocGranulefileSnippetsValid extracts all Granulefile / Dockerfile code blocks
// from README.md and docs/granulefile.md and verifies they parse and validate.
func TestDocGranulefileSnippetsValid(t *testing.T) {
	root := filepath.Join("..", "..")
	docFiles := []string{
		filepath.Join(root, "README.md"),
		filepath.Join(root, "docs", "granulefile.md"),
	}

	reSnippet := regexp.MustCompile("(?s)```(?:dockerfile|granulefile)\n(.*?)```")

	for _, docPath := range docFiles {
		content, err := os.ReadFile(docPath)
		if err != nil {
			t.Fatalf("failed to read doc file %s: %v", docPath, err)
		}

		matches := reSnippet.FindAllStringSubmatch(string(content), -1)
		if len(matches) == 0 {
			t.Fatalf("expected code snippets in %s, found none", docPath)
		}

		for idx, m := range matches {
			snippet := strings.TrimSpace(m[1])
			if snippet == "" {
				continue
			}

			// Validate snippet with pkg/granulefile
			f, err := granulefile.Parse(docPath, snippet)
			if err != nil {
				t.Errorf("[%s snippet %d] Parse error: %v\nSnippet:\n%s", docPath, idx+1, err, snippet)
				continue
			}

			if err := granulefile.Validate(f, granulefile.Options{}); err != nil {
				t.Errorf("[%s snippet %d] Validate error: %v\nSnippet:\n%s", docPath, idx+1, err, snippet)
			}
		}
	}
}

// TestDocsGranulefileSettingEnvConsistency validates that all VM SET keys in
// docs/granulefile.md §5.5 are registered in granulefile.SettingEnv.
func TestDocsGranulefileSettingEnvConsistency(t *testing.T) {
	root := filepath.Join("..", "..")
	docPath := filepath.Join(root, "docs", "granulefile.md")
	content, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", docPath, err)
	}

	// Find the §5.5 table rows: | `key` | meaning | `CHV_...` |
	reRow := regexp.MustCompile(`\|\s*` + "`" + `([a-z0-9_-]+)` + "`" + `\s*\|\s*[^|]+\|\s*` + "`" + `(CHV_[A-Z0-9_]+)` + "`" + `\s*\|`)
	matches := reRow.FindAllStringSubmatch(string(content), -1)
	if len(matches) == 0 {
		t.Fatalf("no VM SET table rows matched in %s", docPath)
	}

	for _, m := range matches {
		key := m[1]
		expectedEnv := m[2]

		env, ok := granulefile.SettingEnv(key)
		if !ok {
			t.Errorf("documented VM SET key %q in §5.5 is not recognized by SettingEnv", key)
			continue
		}
		if env != expectedEnv {
			t.Errorf("documented VM SET key %q maps to %q, but SettingEnv returned %q", key, expectedEnv, env)
		}
	}
}

// TestObsoleteLegacyProjectNames verifies that obsolete project names (konturctl, Konturfile)
// do not appear in active code, scripts, benchmarks, or deployments.
func TestObsoleteLegacyProjectNames(t *testing.T) {
	root := filepath.Join("..", "..")
	disallowedNames := []string{"konturctl", "Konturfile"}

	dirsToCheck := []string{"cmd", "pkg", "deploy", "benchmarks", "test"}

	for _, dir := range dirsToCheck {
		dirPath := filepath.Join(root, dir)
		err := filepath.Walk(dirPath, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				name := info.Name()
				if name == ".git" || name == "vendor" || name == ".gemini" {
					return filepath.SkipDir
				}
				return nil
			}

			// Exclude this test file itself
			if filepath.Base(path) == "docs_consistency_test.go" {
				return nil
			}

			ext := filepath.Ext(path)
			if ext != ".go" && ext != ".md" && ext != ".sh" && ext != ".yaml" && ext != ".yml" && ext != ".json" {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			content := string(data)
			for _, disallowed := range disallowedNames {
				if strings.Contains(content, disallowed) {
					t.Errorf("file %s contains disallowed obsolete project name %q", path, disallowed)
				}
			}
			return nil
		})

		if err != nil {
			t.Fatalf("filepath.Walk failed for %s: %v", dir, err)
		}
	}
}

// TestREADMECLISubcommandsConsistency verifies that README.md documents valid CLI subcommands.
func TestREADMECLISubcommandsConsistency(t *testing.T) {
	root := filepath.Join("..", "..")
	readmePath := filepath.Join(root, "README.md")
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("failed to read README.md: %v", err)
	}

	// Match "granule <category> <subcmd>" in code blocks
	reCLI := regexp.MustCompile(`granule\s+(vm|guest|granulefile)\s+([a-z]+)`)
	matches := reCLI.FindAllStringSubmatch(string(content), -1)
	if len(matches) == 0 {
		t.Fatalf("no granule CLI commands found in README.md")
	}

	validCommands := make(map[string]bool)
	for _, sc := range allSubcommands {
		validCommands[sc.category+" "+sc.subcmd] = true
	}

	for _, m := range matches {
		invoked := m[1] + " " + m[2]
		if !validCommands[invoked] {
			t.Errorf("README.md mentions non-existent CLI subcommand: granule %s", invoked)
		}
	}
}
