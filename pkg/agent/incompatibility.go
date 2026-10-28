package agent

import (
	"errors"
	"fmt"
)

// ModelIncompatibilityError says that the configured model cannot be used by
// the account behind a framework. It is a deployment condition, not a task
// failure: changing the model/account (or selecting an explicitly configured
// alternate backend) can recover it without changing the task.
type ModelIncompatibilityError struct {
	Framework string
	Model     string
	Message   string
	Err       error
}

func (e *ModelIncompatibilityError) Error() string {
	what := "configured model is incompatible with this account"
	if e.Model != "" {
		what = fmt.Sprintf("configured model %q is incompatible with this account", e.Model)
	}
	if e.Framework != "" {
		what = e.Framework + ": " + what
	}
	if e.Message != "" {
		what += "; " + e.Message
	}
	return what
}

func (e *ModelIncompatibilityError) Unwrap() error { return e.Err }

// ModelIncompatibility finds a typed compatibility refusal through wrappers.
func ModelIncompatibility(err error) (*ModelIncompatibilityError, bool) {
	var incompat *ModelIncompatibilityError
	if errors.As(err, &incompat) {
		return incompat, true
	}
	return nil, false
}
