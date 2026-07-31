package client

import (
	"encoding/json"
	"strings"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
)

func TestRedactDiagnosticTextRemovesCommonSecrets(t *testing.T) {
	source := `authorization=Bearer secret-token.123 x-api-key: sk-ant-abcdef123456 Cookie="session=very-secret" access_token=eyJabcdefgh.abcdefgh.abcdefgh`
	redacted := redactDiagnosticText(source)
	for _, forbidden := range []string{"secret-token.123", "sk-ant-abcdef123456", "very-secret", "eyJabcdefgh.abcdefgh.abcdefgh"} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("redacted output leaked %q: %s", forbidden, redacted)
		}
	}
	if !strings.Contains(redacted, "[REDACTED") {
		t.Fatalf("redacted output did not include a redaction marker: %s", redacted)
	}
}

func TestDiagnosticConfigSummaryExcludesCredentialsAndHeaders(t *testing.T) {
	cfg := UserConfig{
		Log: true,
		Providers: []serverconfig.ProviderConfig{{
			ID:            "provider-1",
			Name:          "Relay",
			Type:          "anthropic",
			BaseURL:       "https://user:password@example.com/v1",
			APIKey:        "sk-ant-secret",
			HeadersJSON:   `{"Cookie":"secret-cookie"}`,
			ClientProfile: "claude-code",
		}},
		ModelAdapters: []serverconfig.ModelAdapterConfig{{
			DisplayName:       "Relay Opus",
			Type:              "anthropic",
			ModelID:           "claude-opus-5",
			APIKey:            "sk-ant-model-secret",
			CustomHeadersJSON: `{"Authorization":"Bearer hidden"}`,
		}},
	}
	summary := buildDiagnosticConfigSummary(cfg)
	body, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	text := strings.ToLower(string(body))
	for _, forbidden := range []string{"password", "sk-ant-secret", "sk-ant-model-secret", "secret-cookie", "bearer hidden", "customheaders"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("config summary leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "example.com") {
		t.Fatalf("config summary should preserve the non-sensitive host: %s", text)
	}
}
