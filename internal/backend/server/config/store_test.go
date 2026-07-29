package config

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStorePersistsProviderKeyAndModelBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := NewStore(path, t.TempDir())
	cfg := DefaultConfig()
	provider := cfg.Providers[0]
	provider.APIKey = "test-provider-key"
	cfg.Providers[0] = provider
	cfg.ModelAdapters = []ModelAdapterConfig{
		{
			ProviderID:              provider.ID,
			DisplayName:             "test-model",
			Type:                    provider.Type,
			BaseURL:                 provider.BaseURL,
			APIKey:                  provider.APIKey,
			TooltipData:             provider.Name,
			ModelID:                 "test-model-id",
			AnthropicThinkingEffort: "xhigh",
		},
	}

	if _, err := store.Save(context.Background(), cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := loaded.Providers[0].APIKey; got != provider.APIKey {
		t.Fatalf("provider apiKey = %q, want %q", got, provider.APIKey)
	}
	if len(loaded.ModelAdapters) != 1 {
		t.Fatalf("len(ModelAdapters) = %d, want 1", len(loaded.ModelAdapters))
	}
	if got := loaded.ModelAdapters[0].ProviderID; got != provider.ID {
		t.Fatalf("model providerID = %q, want %q", got, provider.ID)
	}
	if got := loaded.ModelAdapters[0].ModelID; got != "test-model-id" {
		t.Fatalf("modelID = %q, want %q", got, "test-model-id")
	}
}
