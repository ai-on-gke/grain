package codex

import (
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent"
)

var incompatibilityMarkers = []string{
	"unsupported model",
	"model is not supported",
	"model_not_supported",
	"does not support this model",
	"not available with your chatgpt account",
	"not available for your account",
}

func modelIncompatibilityFailure(model, resultText string, runErr error) *agent.ModelIncompatibilityError {
	text := resultText
	if runErr != nil {
		text = strings.TrimSpace(text + "\n" + runErr.Error())
	}
	lower := strings.ToLower(text)
	for _, marker := range incompatibilityMarkers {
		if strings.Contains(lower, marker) {
			return &agent.ModelIncompatibilityError{
				Framework: usageLimitFramework,
				Model:     model,
				Message:   agent.TrimLimitMessage(text),
				Err:       runErr,
			}
		}
	}
	return nil
}
