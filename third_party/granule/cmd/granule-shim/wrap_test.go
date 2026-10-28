package main

import (
	"testing"
)

func TestRepeatedFlag(t *testing.T) {
	var rf repeatedFlag
	if rf.String() != "" {
		t.Errorf("rf.String() = %q, want empty", rf.String())
	}
	if err := rf.Set("FOO=BAR"); err != nil {
		t.Fatalf("rf.Set() error = %v", err)
	}
	if err := rf.Set("BAZ=QUX"); err != nil {
		t.Fatalf("rf.Set() error = %v", err)
	}
	if rf.String() != "FOO=BAR,BAZ=QUX" {
		t.Errorf("rf.String() = %q, want 'FOO=BAR,BAZ=QUX'", rf.String())
	}
}

func TestRunWrapHelp(t *testing.T) {
	if err := runWrap([]string{"-h"}); err != nil {
		t.Errorf("runWrap(-h) error = %v, want nil", err)
	}
}

func TestRunWrapRejectsMissingArgs(t *testing.T) {
	if err := runWrap([]string{}); err == nil {
		t.Error("runWrap([]) expected error, got nil")
	}
	if err := runWrap([]string{"-t", "my-tag"}); err == nil {
		t.Error("runWrap(-t my-tag) without source image expected error, got nil")
	}
	if err := runWrap([]string{"src-image"}); err == nil {
		t.Error("runWrap(src-image) without -t expected error, got nil")
	}
}

func TestRunExecHelp(t *testing.T) {
	if err := runExec([]string{"-h"}); err != nil {
		t.Errorf("runExec(-h) error = %v, want nil", err)
	}
}

func TestRunCpHelp(t *testing.T) {
	if err := runCp([]string{"-h"}); err != nil {
		t.Errorf("runCp(-h) error = %v, want nil", err)
	}
}

func TestRunReadyHelp(t *testing.T) {
	if err := runReady([]string{"-h"}); err != nil {
		t.Errorf("runReady(-h) error = %v, want nil", err)
	}
}
