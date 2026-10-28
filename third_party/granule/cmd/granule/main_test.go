package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/cli"
)

func TestGranuleCLI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"--help"}, &bytes.Buffer{}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("cli.Run(--help) returned %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "granule") && !strings.Contains(stdout.String(), "Usage") {
		t.Errorf("cli.Run(--help) stdout = %q, want usage info", stdout.String())
	}
}

func TestGranuleCLIUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"no-such-command"}, &bytes.Buffer{}, &stdout, &stderr)
	if code == 0 {
		t.Errorf("cli.Run(no-such-command) returned 0, want non-zero")
	}
}
