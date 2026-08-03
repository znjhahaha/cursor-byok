package modeladapter

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	serverconfig "cursor/internal/backend/server/config"
	"gopkg.in/yaml.v3"
)

// TestAnyRouterClaudeCodeSmoke is opt-in because it consumes a real credential
// and reaches an external provider. It never prints credentials or request
// bodies. Set CURSOR_BYOK_SMOKE_CONFIG to an existing config.yaml to run it.
func TestAnyRouterClaudeCodeSmoke(t *testing.T) {
	resolved := loadClaudeCodeSmokeConfig(t)

	var target *serverconfig.ModelAdapterConfig
	for index := range resolved.ModelAdapters {
		item := &resolved.ModelAdapters[index]
		if item.Type != "anthropic" || item.ClientProfile != ClientProfileClaudeCode {
			continue
		}
		if !strings.Contains(strings.ToLower(item.BaseURL), "anyrouter") {
			continue
		}
		if target == nil || item.Anthropic1MContextEnabled {
			target = item
		}
		if item.Anthropic1MContextEnabled && strings.Contains(strings.ToLower(item.ModelID), "opus") {
			break
		}
	}
	if target == nil {
		t.Skip("no AnyRouter Claude Code model is configured")
	}

	timeoutContext, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	startedAt := time.Now().UTC()
	adapter := NewAnthropicAdapter()
	err := adapter.Stream(timeoutContext, claudeCodeSmokeRequest(*target, "anyrouter"), func(ModelEvent) error { return nil })
	if err != nil {
		if strings.Contains(err.Error(), "status=429") {
			t.Logf("AnyRouter accepted the Claude Code stream shape and rate-limited it at %s after %s", startedAt.Format(time.RFC3339), time.Since(startedAt).Round(time.Millisecond))
			return
		}
		t.Fatalf("AnyRouter smoke failed at %s after %s: %v", startedAt.Format(time.RFC3339), time.Since(startedAt).Round(time.Millisecond), err)
	}
	t.Logf("AnyRouter smoke succeeded at %s in %s", startedAt.Format(time.RFC3339), time.Since(startedAt).Round(time.Millisecond))
}

func TestConfiguredClaudeCodeRelaysSmoke(t *testing.T) {
	if os.Getenv("CURSOR_BYOK_SMOKE_ALL") != "1" {
		t.Skip("CURSOR_BYOK_SMOKE_ALL is not set")
	}
	resolved := loadClaudeCodeSmokeConfig(t)
	targets := map[string]serverconfig.ModelAdapterConfig{}
	for _, item := range resolved.ModelAdapters {
		if item.Type != "anthropic" || item.ClientProfile != ClientProfileClaudeCode ||
			strings.TrimSpace(item.BaseURL) == "" || strings.TrimSpace(item.APIKey) == "" {
			continue
		}
		key := strings.TrimRight(strings.ToLower(strings.TrimSpace(item.BaseURL)), "/")
		current, exists := targets[key]
		if !exists || claudeCodeSmokeModelPriority(item) > claudeCodeSmokeModelPriority(current) {
			targets[key] = item
		}
	}
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		t.Skip("no configured Claude Code relays")
	}
	for _, key := range keys {
		target := targets[key]
		host := claudeCodeSmokeHost(target.BaseURL)
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			timeoutContext, cancel := context.WithTimeout(context.Background(), 240*time.Second)
			defer cancel()
			startedAt := time.Now().UTC()
			err := NewAnthropicAdapter().Stream(timeoutContext, claudeCodeSmokeRequest(target, host), func(ModelEvent) error { return nil })
			elapsed := time.Since(startedAt).Round(time.Millisecond)
			if err == nil {
				t.Logf("%s stream succeeded in %s using model %s", host, elapsed, target.ModelID)
				return
			}
			if strings.Contains(err.Error(), "status=429") {
				t.Logf("%s accepted the Claude Code stream and returned 429 in %s using model %s", host, elapsed, target.ModelID)
				return
			}
			t.Errorf("%s stream failed in %s using model %s: %v", host, elapsed, target.ModelID, err)
		})
	}
}

func loadClaudeCodeSmokeConfig(t *testing.T) serverconfig.Config {
	t.Helper()
	configPath := strings.TrimSpace(os.Getenv("CURSOR_BYOK_SMOKE_CONFIG"))
	if configPath == "" {
		t.Skip("CURSOR_BYOK_SMOKE_CONFIG is not set")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read smoke config: %v", err)
	}
	var persisted serverconfig.Config
	if err := yaml.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode smoke config: %v", err)
	}
	resolved, err := serverconfig.NormalizeConfig(persisted)
	if err != nil {
		t.Fatalf("normalize smoke config: %v", err)
	}
	return resolved
}

func claudeCodeSmokeRequest(target serverconfig.ModelAdapterConfig, label string) StreamRequest {
	effort := strings.TrimSpace(target.AnthropicThinkingEffort)
	if effort == "" {
		effort = "high"
	}
	requestLabel := "smoke-" + strings.NewReplacer(".", "-", ":", "-", "/", "-").Replace(label) + "-0.0.47"
	return StreamRequest{
		RequestID:                 requestLabel,
		RunID:                     requestLabel,
		ModelCallID:               requestLabel,
		ConversationID:            requestLabel,
		BaseURL:                   target.BaseURL,
		APIKey:                    target.APIKey,
		ProviderModelID:           target.ModelID,
		ClientProfile:             target.ClientProfile,
		Anthropic1MContextEnabled: target.Anthropic1MContextEnabled,
		AnthropicThinkingEffort:   effort,
		MaxTokens:                 1024,
		Messages:                  []Message{{Role: "user", Content: "Reply with OK."}},
		Tools: []json.RawMessage{json.RawMessage(`{
			"type":"function",
			"function":{
				"name":"Echo",
				"description":"Echo a short diagnostic string.",
				"parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}
			}
		}`)},
		CustomHeadersEnabled:        target.CustomHeadersEnabled,
		CustomHeadersJSON:           target.CustomHeadersJSON,
		ProviderStreamIdleTimeout:   180 * time.Second,
		AnthropicExtraParamsEnabled: false,
	}
}

func claudeCodeSmokeModelPriority(item serverconfig.ModelAdapterConfig) int {
	model := strings.ToLower(strings.TrimSpace(item.ModelID))
	score := 0
	switch {
	case strings.Contains(model, "opus-5"):
		score = 100
	case strings.Contains(model, "opus-4-7"):
		score = 90
	case strings.Contains(model, "opus-4-6"):
		score = 80
	case strings.Contains(model, "opus"):
		score = 70
	case strings.Contains(model, "sonnet"):
		score = 50
	default:
		score = 10
	}
	if item.Anthropic1MContextEnabled {
		score += 5
	}
	return score
}

func claudeCodeSmokeHost(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err == nil && strings.TrimSpace(parsed.Hostname()) != "" {
		return strings.ToLower(parsed.Hostname())
	}
	return "configured-relay"
}
