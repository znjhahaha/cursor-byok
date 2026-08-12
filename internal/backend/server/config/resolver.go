package config

import (
	"context"
	"strings"

	"cursor/internal/modelchannel"
	legacyruntime "cursor/internal/runtime"
)

const (
	defaultChannelTimeoutMS           = int((2 * 60 * 60) * 1000)
	defaultChannelContextWindowTokens = 200_000
	defaultChannelMaxTokens           = 65_536
	defaultChannelThinkingBudget      = 4_096
	defaultChannelAnthropicEffort     = "xhigh"
)

func (manager *Manager) SelectChannelForModel(_ context.Context, modelID string) (*legacyruntime.ResolvedChannel, error) {
	if manager == nil {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	cfg := manager.Current()
	adapters, err := NormalizeModelAdapterConfigs(cfg.ModelAdapters)
	if err != nil {
		return nil, err
	}
	return resolveModelAdapterChannel(adapters, cfg.Providers, modelID)
}

func resolveModelAdapterChannel(adapters []ModelAdapterConfig, providers []ProviderConfig, requestedModel string) (*legacyruntime.ResolvedChannel, error) {
	// 与模型列表下发用同一个谓词过滤：停用站点下的模型必须明确不可用，
	// 而不是「列表里看不到、旧会话却还在悄悄计费」。
	active := ActiveModelAdapters(adapters, providers)
	matchIndex, ok := modelchannel.ResolveAdapterIndex(
		active,
		requestedModel,
		func(adapter ModelAdapterConfig) string { return adapter.ID },
		func(adapter ModelAdapterConfig) string { return adapter.ModelID },
		func(adapter ModelAdapterConfig) string {
			return modelchannel.BuildLegacyChannelID(adapter.BaseURL, adapter.ModelID, adapter.APIKey, adapter.DisplayName)
		},
	)
	if !ok {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	matched := active[matchIndex]
	// 归一化阶段已完成继承物化，这里只需把站点名带出来供运行时日志与统计使用。
	providerName := ""
	if provider, ok := FindProvider(providers, matched.ProviderID); ok {
		providerName = provider.Name
	}

	resolved := &legacyruntime.ResolvedChannel{
		ID:                          strings.TrimSpace(matched.ID),
		Name:                        strings.TrimSpace(matched.DisplayName),
		GroupName:                   resolveChannelGroupName(providerName),
		ProviderID:                  strings.TrimSpace(matched.ProviderID),
		Code:                        strings.TrimSpace(matched.ID),
		Provider:                    strings.TrimSpace(matched.Type),
		BaseURL:                     strings.TrimSpace(matched.BaseURL),
		APIKey:                      strings.TrimSpace(matched.APIKey),
		Model:                       strings.TrimSpace(matched.ModelID),
		ClientProfile:               normalizeClientProfile(matched.ClientProfile),
		Anthropic1MContextEnabled:   matched.Anthropic1MContextEnabled,
		TextOnlyEnabled:             matched.TextOnlyEnabled,
		OpenAIEndpoint:              strings.TrimSpace(matched.OpenAIEndpoint),
		OpenAIExtraParamsEnabled:    matched.OpenAIExtraParamsEnabled,
		OpenAIExtraParamsJSON:       strings.TrimSpace(matched.OpenAIExtraParamsJSON),
		CustomHeadersEnabled:        matched.CustomHeadersEnabled,
		CustomHeadersJSON:           strings.TrimSpace(matched.CustomHeadersJSON),
		AnthropicExtraParamsEnabled: matched.AnthropicExtraParamsEnabled,
		AnthropicExtraParamsJSON:    strings.TrimSpace(matched.AnthropicExtraParamsJSON),
		TimeoutMS:                   defaultChannelTimeoutMS,
		ContextWindowTokens:         defaultChannelContextWindowTokens,
		MaxTokens:                   defaultChannelMaxTokens,
		ReasoningEffort:             strings.TrimSpace(matched.ReasoningEffort),
		AnthropicMaxTokens:          defaultChannelMaxTokens,
		AnthropicThinkingEffort:     defaultChannelAnthropicEffort,
		ThinkingEnabled:             true,
		ThinkingBudgetTokens:        defaultChannelThinkingBudget,
	}
	if matched.ContextWindowTokens > 0 {
		resolved.ContextWindowTokens = matched.ContextWindowTokens
	}
	if matched.Anthropic1MContextEnabled && resolved.ContextWindowTokens < 1_000_000 {
		resolved.ContextWindowTokens = 1_000_000
	}
	if matched.MaxCompletionTokens > 0 {
		resolved.MaxTokens = matched.MaxCompletionTokens
	}
	if matched.AnthropicMaxTokens > 0 {
		resolved.AnthropicMaxTokens = matched.AnthropicMaxTokens
	}
	if matched.ThinkingBudgetTokens > 0 {
		resolved.ThinkingBudgetTokens = matched.ThinkingBudgetTokens
	}
	if strings.TrimSpace(matched.AnthropicThinkingEffort) != "" {
		resolved.AnthropicThinkingEffort = strings.TrimSpace(matched.AnthropicThinkingEffort)
	}
	return resolved, nil
}

// resolveChannelGroupName 决定渠道在运行时日志中的分组名。
// 隶属中转站时用站点名，未归属时沿用历史的 "local"。
func resolveChannelGroupName(providerName string) string {
	if name := strings.TrimSpace(providerName); name != "" {
		return name
	}
	return "local"
}
