package grainlog

import "github.com/ai-on-gke/grain/pkg/redact"

// Redactor is pkg/redact's, which this package's journal was the first
// caller of: the same shapes are now cut out of a host sandbox's
// run_command output and a run's stored transcript, so they live where
// every one of those can reach them.
type Redactor = redact.Redactor

// KnownValue is pkg/redact's, for Redactor's reason.
type KnownValue = redact.KnownValue

// NewRedactor is redact.New, kept under the name this package's callers
// already use.
func NewRedactor(known []KnownValue) *Redactor { return redact.New(known) }
