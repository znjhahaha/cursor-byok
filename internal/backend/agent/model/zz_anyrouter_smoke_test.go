package modeladapter

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"cursor/internal/appdata"
	serverconfig "cursor/internal/backend/server/config"
	"gopkg.in/yaml.v3"
)

func TestAnyRouterConfiguredStreamingSmoke(t *testing.T) {
	if os.Getenv("CURSOR_ANYROUTER_SMOKE") != "1" {
		t.Skip("live smoke test disabled")
	}
	body, err := os.ReadFile(appdata.ConfigFilePath())
	if err != nil {
		t.Skipf("config unavailable: %v", err)
	}
	var raw serverconfig.Config
	if err := yaml.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	cfg, err := serverconfig.NormalizeConfig(raw)
	if err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	candidates := make([]serverconfig.ModelAdapterConfig, 0)
	for _, adapter := range cfg.ModelAdapters {
		if adapter.Type == "anthropic" && isAnyRouterText(adapter.DisplayName+" "+adapter.BaseURL) {
			candidates = append(candidates, adapter)
		}
	}
	if len(candidates) == 0 {
		for _, adapter := range cfg.ModelAdapters {
			if adapter.Type != "anthropic" {
				continue
			}
			for _, provider := range cfg.Providers {
				if adapter.ProviderID == provider.ID && isAnyRouterText(provider.Name+" "+provider.BaseURL) {
					candidates = append(candidates, adapter)
					break
				}
			}
		}
	}
	if len(candidates) == 0 {
		t.Skip("configured AnyRouter Anthropic model not found")
	}
	selected := candidates[0]
	for _, candidate := range candidates {
		if strings.Contains(strings.ToLower(candidate.ModelID), "opus") {
			selected = candidate
			break
		}
	}
	for _, candidate := range candidates {
		t.Logf(
			"AnyRouter candidate display=%q model=%q profile=%q context1m=%t selected=%t",
			candidate.DisplayName,
			candidate.ModelID,
			candidate.ClientProfile,
			candidate.Anthropic1MContextEnabled,
			candidate.ID == selected.ID,
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	errFirstText := errors.New("first text received")
	gotText := false
	err = NewAnthropicAdapter().Stream(ctx, StreamRequest{
		RequestID:                   "anyrouter-smoke",
		RunID:                       "anyrouter-smoke",
		ModelCallID:                 "anyrouter-smoke",
		ModelID:                     selected.ID,
		Provider:                    "anthropic",
		BaseURL:                     selected.BaseURL,
		APIKey:                      selected.APIKey,
		ProviderModelID:             selected.ModelID,
		ClientProfile:               selected.ClientProfile,
		Anthropic1MContextEnabled:   selected.Anthropic1MContextEnabled,
		ResolvedChannelID:           selected.ID,
		ResolvedChannelName:         selected.DisplayName,
		ThinkingEffort:              "disabled",
		AnthropicThinkingEffort:     "disabled",
		AnthropicMaxTokens:          8,
		CustomHeadersEnabled:        selected.CustomHeadersEnabled,
		CustomHeadersJSON:           selected.CustomHeadersJSON,
		AnthropicExtraParamsEnabled: false,
		Messages:                    []Message{{Role: "user", Content: "Reply with OK only."}},
		MaxTokens:                   8,
		Stream:                      true,
		RequestKnobs:                map[string]any{"stream": true, "max_tokens": 8},
		ProviderStreamIdleTimeout:   30 * time.Second,
		ProviderRequestMaxAttempts:  1,
	}, func(event ModelEvent) error {
		if event.Kind == ModelEventKindTextDelta && strings.TrimSpace(event.Text) != "" {
			gotText = true
			return errFirstText
		}
		if event.Kind == ModelEventKindProviderError && event.Err != nil {
			return event.Err
		}
		return nil
	})
	if gotText && errors.Is(err, errFirstText) {
		t.Log("AnyRouter streaming smoke received first text")
		return
	}
	if err == nil && gotText {
		return
	}
	message := strings.ToLower(strings.TrimSpace(errorString(err)))
	switch {
	case strings.Contains(message, "429"):
		t.Log("AnyRouter streaming smoke reached provider and returned expected 429")
	case strings.Contains(message, "503"):
		t.Fatalf("AnyRouter streaming smoke returned abnormal 503")
	default:
		t.Fatalf("AnyRouter streaming smoke failed without text: %s", redactSmokeError(message))
	}
}

func isAnyRouterText(value string) bool {
	normalized := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(value))
	return strings.Contains(normalized, "anyrouter")
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func redactSmokeError(value string) string {
	if len(value) > 240 {
		return value[:240]
	}
	return value
}
