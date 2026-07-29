package forwarder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUsageFileStoreWritesHourlyBuckets(t *testing.T) {
	root := t.TempDir()
	store := NewUsageFileStore(root)
	at := time.Date(2026, time.July, 29, 13, 45, 0, 0, time.UTC)

	if err := store.UpsertEvent(usageFileEvent{
		EventID:         "request-1",
		Kind:            usageEventKindProvider,
		ProviderID:      "provider-1",
		ProviderName:    "Provider 1",
		Model:           "model-1",
		At:              at,
		InputTokens:     100,
		OutputTokens:    20,
		CacheReadTokens: 30,
	}); err != nil {
		t.Fatalf("UpsertEvent() error = %v", err)
	}

	body, err := os.ReadFile(filepath.Join(root, usageFileName))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var doc usageFileDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if doc.SchemaVersion != usageFileSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", doc.SchemaVersion, usageFileSchemaVersion)
	}
	if len(doc.Daily) != 1 {
		t.Fatalf("len(Daily) = %d, want 1", len(doc.Daily))
	}
	hour := doc.Daily[0].ByHour["13"]
	if hour.ProviderCalls != 1 {
		t.Errorf("hour.ProviderCalls = %d, want 1", hour.ProviderCalls)
	}
	if hour.TotalTokens != 150 {
		t.Errorf("hour.TotalTokens = %d, want 150", hour.TotalTokens)
	}
}
