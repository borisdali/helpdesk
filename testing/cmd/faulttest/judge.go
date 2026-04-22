package main

import (
	"context"
	"os"

	"helpdesk/agentutil"
	"helpdesk/testing/faultlib"
)

// newJudgeCompleter creates a TextCompleter for the LLM judge.
// Falls back to HELPDESK_* env vars when cfg fields are empty.
func newJudgeCompleter(ctx context.Context, cfg *HarnessConfig) (faultlib.TextCompleter, error) {
	vendor := cfg.JudgeVendor
	if vendor == "" {
		vendor = os.Getenv("HELPDESK_MODEL_VENDOR")
	}
	modelName := cfg.JudgeModel
	if modelName == "" {
		modelName = os.Getenv("HELPDESK_MODEL_NAME")
	}
	apiKey := cfg.JudgeAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("HELPDESK_API_KEY")
	}

	completer, err := agentutil.NewTextCompleter(ctx, agentutil.Config{
		ModelVendor: vendor,
		ModelName:   modelName,
		APIKey:      apiKey,
	})
	if err != nil {
		return nil, err
	}
	// agentutil.TextCompleter and faultlib.TextCompleter have the same underlying type.
	return faultlib.TextCompleter(completer), nil
}
