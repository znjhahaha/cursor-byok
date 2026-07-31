package forwarder

import (
	"encoding/json"
	"fmt"
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
	if doc.Daily[0].Date != "2026-07-29" {
		t.Fatalf("Daily[0].Date = %q, want Beijing date", doc.Daily[0].Date)
	}
	hour := doc.Daily[0].ByHour["21"]
	if hour.ProviderCalls != 1 {
		t.Errorf("hour.ProviderCalls = %d, want 1", hour.ProviderCalls)
	}
	if hour.TotalTokens != 150 {
		t.Errorf("hour.TotalTokens = %d, want 150", hour.TotalTokens)
	}
}

func TestUsageFileStoreSeparatesSharedWireModelByStableChannel(t *testing.T) {
	root := t.TempDir()
	store := NewUsageFileStore(root)
	for index, channel := range []struct {
		id   string
		name string
	}{
		{id: "channel-baibei", name: "baibei-claude-opus-4-8"},
		{id: "channel-anyrouter", name: "AnyRouter Opus"},
	} {
		if err := store.UpsertEvent(usageFileEvent{
			EventID:              fmt.Sprintf("request-%d", index),
			Kind:                 usageEventKindProvider,
			ProviderID:           "provider-shared",
			ProviderName:         "Shared Relay",
			Model:                channel.name,
			WireModel:            "claude-opus-5",
			ChannelID:            channel.id,
			ChannelName:          channel.name,
			At:                   time.Date(2026, time.July, 29, 16, 30, 0, 0, time.UTC),
			InputTokens:          40,
			OutputTokens:         10,
			EstimatedInputTokens: 40,
			UsageMissing:         true,
			UsageSource:          "estimated",
		}); err != nil {
			t.Fatalf("UpsertEvent(%s) error = %v", channel.id, err)
		}
	}

	doc, err := readUsageFileDocument(filepath.Join(root, usageFileName))
	if err != nil {
		t.Fatalf("readUsageFileDocument() error = %v", err)
	}
	if len(doc.Totals.ByModel) != 2 {
		t.Fatalf("len(ByModel) = %d, want 2", len(doc.Totals.ByModel))
	}
	for _, channelID := range []string{"channel-baibei", "channel-anyrouter"} {
		stat, ok := doc.Totals.ByModel[channelID]
		if !ok {
			t.Fatalf("missing stable channel bucket %q", channelID)
		}
		if stat.WireModel != "claude-opus-5" {
			t.Errorf("%s WireModel = %q", channelID, stat.WireModel)
		}
		if stat.UnreportedCalls != 1 || stat.EstimatedInputTokens != 40 {
			t.Errorf("%s estimate counters = %+v", channelID, stat.usageFileCounters)
		}
	}
	if doc.Daily[0].Date != "2026-07-30" {
		t.Fatalf("UTC+8 cross-day date = %q, want 2026-07-30", doc.Daily[0].Date)
	}
}
