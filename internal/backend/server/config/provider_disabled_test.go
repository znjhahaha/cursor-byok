package config

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	legacyruntime "cursor/internal/runtime"
)

func buildProviderWithModel(t *testing.T, disabled bool) (ProviderConfig, ModelAdapterConfig) {
	t.Helper()
	provider := ProviderConfig{
		ID:       "p-test",
		Name:     "测试站",
		Type:     "anthropic",
		BaseURL:  "https://relay.example.com",
		APIKey:   "sk-provider",
		Disabled: disabled,
	}
	adapter := ModelAdapterConfig{
		ProviderID:              provider.ID,
		DisplayName:             "测试站-claude-sonnet-4-5",
		Type:                    "anthropic",
		BaseURL:                 provider.BaseURL,
		APIKey:                  provider.APIKey,
		TooltipData:             provider.Name,
		ModelID:                 "claude-sonnet-4-5",
		AnthropicThinkingEffort: "xhigh",
	}
	return provider, adapter
}

func TestIsAdapterActiveFollowsProviderDisabledFlag(t *testing.T) {
	enabledProvider, adapter := buildProviderWithModel(t, false)
	disabledProvider, _ := buildProviderWithModel(t, true)

	if !IsAdapterActive(adapter, []ProviderConfig{enabledProvider}) {
		t.Fatal("启用站点下的模型应当参与下发")
	}
	if IsAdapterActive(adapter, []ProviderConfig{disabledProvider}) {
		t.Fatal("停用站点下的模型不应参与下发")
	}
	// 未归属站点与悬空引用都不受停用影响：前者本来就独立，后者由引用校验负责报错。
	if !IsAdapterActive(ModelAdapterConfig{}, []ProviderConfig{disabledProvider}) {
		t.Fatal("未归属站点的模型应当始终启用")
	}
	if !IsAdapterActive(adapter, nil) {
		t.Fatal("悬空引用不应在这里被判成停用")
	}
}

func TestDisabledProviderHidesModelsFromSnapshotAndResolution(t *testing.T) {
	provider, adapter := buildProviderWithModel(t, true)
	cfg := DefaultConfig()
	cfg.Providers = append(cfg.Providers, provider)
	cfg.ModelAdapters = []ModelAdapterConfig{adapter}

	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if got := normalized.Providers[len(normalized.Providers)-1].Disabled; !got {
		t.Fatal("停用标记必须在归一化后保留")
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	store := NewStore(path, t.TempDir())
	if _, err := store.Save(context.Background(), normalized); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	manager, err := NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	snapshot, err := manager.LegacyRuntimeSnapshot(context.Background())
	if err != nil {
		t.Fatalf("LegacyRuntimeSnapshot() error = %v", err)
	}
	if len(snapshot.ModelAdapters) != 0 {
		t.Fatalf("len(snapshot.ModelAdapters) = %d, want 0", len(snapshot.ModelAdapters))
	}

	if _, err := manager.SelectChannelForModel(context.Background(), adapter.ModelID); !errors.Is(err, legacyruntime.ErrChannelNotAvailable) {
		t.Fatalf("SelectChannelForModel() error = %v, want ErrChannelNotAvailable", err)
	}
}

func TestEnabledProviderKeepsModelsResolvable(t *testing.T) {
	provider, adapter := buildProviderWithModel(t, false)
	cfg := DefaultConfig()
	cfg.Providers = append(cfg.Providers, provider)
	cfg.ModelAdapters = []ModelAdapterConfig{adapter}

	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := NewStore(path, t.TempDir())
	if _, err := store.Save(context.Background(), normalized); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	manager, err := NewManager(context.Background(), store)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	snapshot, err := manager.LegacyRuntimeSnapshot(context.Background())
	if err != nil {
		t.Fatalf("LegacyRuntimeSnapshot() error = %v", err)
	}
	if len(snapshot.ModelAdapters) != 1 {
		t.Fatalf("len(snapshot.ModelAdapters) = %d, want 1", len(snapshot.ModelAdapters))
	}
	channel, err := manager.SelectChannelForModel(context.Background(), adapter.ModelID)
	if err != nil {
		t.Fatalf("SelectChannelForModel() error = %v", err)
	}
	if channel.GroupName != provider.Name {
		t.Fatalf("channel.GroupName = %q, want %q", channel.GroupName, provider.Name)
	}
}
