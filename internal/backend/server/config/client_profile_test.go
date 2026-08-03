package config

import "testing"

func TestNormalizeModelAdapterRejectsInvalidClientProfile(t *testing.T) {
	adapter := validAnthropicAdapterForProfileTest()
	adapter.ClientProfile = "not-a-client"
	if _, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter}); err == nil {
		t.Fatal("expected invalid clientProfile to be rejected")
	}
}

func TestNormalizeModelAdapterPreservesSupportedClientProfiles(t *testing.T) {
	for _, profile := range []string{"generic", "claude-code", "codex"} {
		t.Run(profile, func(t *testing.T) {
			adapter := validAnthropicAdapterForProfileTest()
			adapter.DisplayName += "-" + profile
			adapter.ClientProfile = profile
			normalized, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter})
			if err != nil {
				t.Fatalf("normalize %s adapter: %v", profile, err)
			}
			if normalized[0].ClientProfile != profile {
				t.Fatalf("client profile = %q, want %q", normalized[0].ClientProfile, profile)
			}
		})
	}
}

func TestModelClientProfileOverridesProviderAndDoesNotChangeStableChannelID(t *testing.T) {
	adapter := validAnthropicAdapterForProfileTest()
	adapter.ProviderID = "provider-1"
	genericProvider := ProviderConfig{
		ID:            "provider-1",
		Name:          "Provider",
		Type:          "anthropic",
		BaseURL:       adapter.BaseURL,
		APIKey:        adapter.APIKey,
		ClientProfile: "generic",
	}
	claudeProvider := genericProvider
	claudeProvider.ClientProfile = "claude-code"

	explicitGeneric, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{
		ApplyProviderInheritance(adapter, []ProviderConfig{claudeProvider}),
	})
	if err != nil {
		t.Fatalf("normalize explicit generic adapter: %v", err)
	}
	if explicitGeneric[0].ClientProfile != "generic" {
		t.Fatalf("explicit model profile was overridden: %q", explicitGeneric[0].ClientProfile)
	}

	missingProfile := adapter
	missingProfile.ClientProfile = ""
	inheritedClaude, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{
		ApplyProviderInheritance(missingProfile, []ProviderConfig{claudeProvider}),
	})
	if err != nil {
		t.Fatalf("normalize inherited Claude adapter: %v", err)
	}
	if inheritedClaude[0].ClientProfile != "claude-code" {
		t.Fatalf("inherited profile = %q", inheritedClaude[0].ClientProfile)
	}
	if explicitGeneric[0].ID != inheritedClaude[0].ID {
		t.Fatalf("client profile changed stable channel ID: %q != %q", explicitGeneric[0].ID, inheritedClaude[0].ID)
	}

	inheritedGeneric, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{
		ApplyProviderInheritance(missingProfile, []ProviderConfig{genericProvider}),
	})
	if err != nil {
		t.Fatalf("normalize inherited generic adapter: %v", err)
	}
	if inheritedGeneric[0].ClientProfile != "generic" {
		t.Fatalf("generic provider profile was not inherited: %q", inheritedGeneric[0].ClientProfile)
	}
}

func TestNormalizeProviderBaseURLAndInheritanceUseRootOnly(t *testing.T) {
	provider, err := NormalizeProviderConfig(ProviderConfig{
		ID:            "provider-1",
		Name:          "Relay",
		Type:          "anthropic",
		BaseURL:       "https://relay.example.com/v1/messages/",
		APIKey:        "provider-key",
		InferencePath: "/v1/messages",
	})
	if err != nil {
		t.Fatalf("normalize provider: %v", err)
	}
	if provider.BaseURL != "https://relay.example.com" {
		t.Fatalf("normalized provider baseURL = %q", provider.BaseURL)
	}

	adapter := validAnthropicAdapterForProfileTest()
	adapter.ProviderID = provider.ID
	resolved := ApplyProviderInheritance(adapter, []ProviderConfig{provider})
	if resolved.BaseURL != "https://relay.example.com" {
		t.Fatalf("inherited baseURL = %q", resolved.BaseURL)
	}
}

func TestBuiltinClientProfileMigrationPreservesCustomUserAgent(t *testing.T) {
	oldAgentRouter := ProviderConfig{
		ID:            "builtin-agentrouter",
		Name:          "AgentRouter",
		Type:          "anthropic",
		BaseURL:       "https://agentrouter.org",
		UserAgent:     "claude-cli/1.0.60 (external, cli)",
		ClientProfile: "",
	}
	customAnyRouter := ProviderConfig{
		ID:            "builtin-anyrouter",
		Name:          "AnyRouter",
		Type:          "anthropic",
		BaseURL:       "https://anyrouter.top",
		UserAgent:     "my-custom-client/7",
		ClientProfile: "",
	}
	providers, err := NormalizeProviderConfigs([]ProviderConfig{oldAgentRouter, customAnyRouter})
	if err != nil {
		t.Fatalf("normalize providers: %v", err)
	}
	agentRouter, ok := FindProvider(providers, "builtin-agentrouter")
	if !ok {
		t.Fatal("AgentRouter preset missing")
	}
	if agentRouter.ClientProfile != "claude-code" {
		t.Fatalf("AgentRouter profile = %q", agentRouter.ClientProfile)
	}
	if agentRouter.UserAgent != "" {
		t.Fatalf("known legacy User-Agent was not removed: %q", agentRouter.UserAgent)
	}
	anyRouter, ok := FindProvider(providers, "builtin-anyrouter")
	if !ok {
		t.Fatal("AnyRouter preset missing")
	}
	if anyRouter.ClientProfile != "claude-code" {
		t.Fatalf("AnyRouter profile = %q", anyRouter.ClientProfile)
	}
	if anyRouter.UserAgent != "my-custom-client/7" {
		t.Fatalf("custom User-Agent was overwritten: %q", anyRouter.UserAgent)
	}
}

func TestAnyRouterWarmupDefaultOnlyAppliesToNewPreset(t *testing.T) {
	fresh, err := NormalizeProviderConfigs(nil)
	if err != nil {
		t.Fatalf("normalize fresh providers: %v", err)
	}
	anyRouter, ok := FindProvider(fresh, "builtin-anyrouter")
	if !ok || !anyRouter.WarmupEnabled {
		t.Fatalf("fresh AnyRouter preset = %+v", anyRouter)
	}

	existing, err := NormalizeProviderConfigs([]ProviderConfig{{
		ID: "builtin-anyrouter", Name: "AnyRouter", Type: "anthropic", BaseURL: "https://anyrouter.top",
		ClientProfile: "claude-code", WarmupEnabled: false,
	}})
	if err != nil {
		t.Fatalf("normalize existing providers: %v", err)
	}
	anyRouter, ok = FindProvider(existing, "builtin-anyrouter")
	if !ok || anyRouter.WarmupEnabled {
		t.Fatalf("existing explicit setting was overwritten: %+v", anyRouter)
	}
}

func TestRepairProviderReferencesRebindsOrDetachesCompleteAdapters(t *testing.T) {
	provider := ProviderConfig{
		ID:      "provider-current",
		Name:    "Provider",
		Type:    "anthropic",
		BaseURL: "https://relay.example.com",
		APIKey:  "provider-key",
	}
	rebind := validAnthropicAdapterForProfileTest()
	rebind.ProviderID = "provider-stale"
	// 旧版本会把探测出的推理端点物化进 adapter.BaseURL；迁移时应按根地址重绑。
	rebind.BaseURL = "https://relay.example.com/v1/messages"
	detach := validAnthropicAdapterForProfileTest()
	detach.ProviderID = "provider-stale"
	detach.BaseURL = "https://standalone.example.com"

	repaired := RepairProviderReferences([]ModelAdapterConfig{rebind, detach}, []ProviderConfig{provider})
	if repaired[0].ProviderID != provider.ID {
		t.Fatalf("matching stale reference was not rebound: %q", repaired[0].ProviderID)
	}
	if repaired[1].ProviderID != "" {
		t.Fatalf("complete standalone adapter was not detached: %q", repaired[1].ProviderID)
	}
}

func TestAnthropicOneMillionContextNormalization(t *testing.T) {
	adapter := validAnthropicAdapterForProfileTest()
	adapter.Anthropic1MContextEnabled = true
	adapter.ContextWindowTokens = 200_000
	normalized, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{adapter})
	if err != nil {
		t.Fatalf("normalize explicit 1M adapter: %v", err)
	}
	if !normalized[0].Anthropic1MContextEnabled || normalized[0].ContextWindowTokens != 200_000 {
		t.Fatalf("explicit 1M normalization changed persistent intent: %#v", normalized[0])
	}
	if normalized[0].ModelID != adapter.ModelID {
		t.Fatalf("normalization mutated persistent model ID: %q", normalized[0].ModelID)
	}
	channel, err := resolveModelAdapterChannel(normalized, nil, normalized[0].ID)
	if err != nil {
		t.Fatalf("resolve 1M adapter channel: %v", err)
	}
	if channel.Model != adapter.ModelID {
		t.Fatalf("channel model identity was mutated: %q", channel.Model)
	}
	if channel.ContextWindowTokens != 1_000_000 {
		t.Fatalf("resolved context window = %d", channel.ContextWindowTokens)
	}

	legacy := validAnthropicAdapterForProfileTest()
	legacy.ModelID = "claude-opus-4-1[1m]"
	normalized, err = NormalizeModelAdapterConfigs([]ModelAdapterConfig{legacy})
	if err != nil {
		t.Fatalf("normalize legacy 1M adapter: %v", err)
	}
	if !normalized[0].Anthropic1MContextEnabled || normalized[0].ContextWindowTokens != 200_000 {
		t.Fatalf("legacy 1M normalization changed persistent intent: %#v", normalized[0])
	}

	openAI := ModelAdapterConfig{
		DisplayName:                 "OpenAI",
		Type:                        "openai",
		BaseURL:                     "https://api.example.com",
		APIKey:                      "secret",
		TooltipData:                 "OpenAI",
		ModelID:                     "gpt-5",
		ClientProfile:               "codex",
		Anthropic1MContextEnabled:   true,
		ReasoningEffort:             "medium",
		OpenAIEndpoint:              "/v1/responses",
		ContextWindowTokens:         200_000,
		MaxCompletionTokens:         16,
		AnthropicThinkingEffort:     "",
		ThinkingBudgetTokens:        0,
		OpenAIExtraParamsEnabled:    false,
		AnthropicExtraParamsEnabled: false,
	}
	normalized, err = NormalizeModelAdapterConfigs([]ModelAdapterConfig{openAI})
	if err != nil {
		t.Fatalf("normalize OpenAI adapter: %v", err)
	}
	if normalized[0].Anthropic1MContextEnabled || normalized[0].ContextWindowTokens != 200_000 {
		t.Fatalf("1M flag leaked into OpenAI adapter: %#v", normalized[0])
	}
}

func validAnthropicAdapterForProfileTest() ModelAdapterConfig {
	return ModelAdapterConfig{
		DisplayName:             "Claude",
		Type:                    "anthropic",
		BaseURL:                 "https://relay.example.com",
		APIKey:                  "secret",
		TooltipData:             "Claude",
		ModelID:                 "claude-opus-4-1",
		ClientProfile:           "generic",
		ContextWindowTokens:     200_000,
		AnthropicMaxTokens:      16,
		AnthropicThinkingEffort: "xhigh",
	}
}
