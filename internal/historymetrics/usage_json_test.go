package historymetrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLoadUsageSeriesAggregatesHourlyBucketsInRange(t *testing.T) {
	today := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02")
	doc := usageFileDocument{
		Daily: []usageFileDaily{
			{
				Date: today,
				ByHour: map[string]usageFileHourStat{
					"9":  {usageFileCounters: usageFileCounters{ProviderCalls: 2, TotalTokens: 120}},
					"21": {usageFileCounters: usageFileCounters{ProviderCalls: 1, TotalTokens: 45}},
				},
			},
		},
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	series, err := LoadUsageSeries(path, 1)
	if err != nil {
		t.Fatalf("LoadUsageSeries() error = %v", err)
	}
	if len(series.Hours) != 24 {
		t.Fatalf("len(Hours) = %d, want 24", len(series.Hours))
	}
	for hour, want := range map[int]HourPoint{
		9:  {Hour: 9, ProviderCalls: 2, TotalTokens: 120},
		21: {Hour: 21, ProviderCalls: 1, TotalTokens: 45},
	} {
		got := series.Hours[hour]
		if got.Hour != want.Hour || got.ProviderCalls != want.ProviderCalls || got.TotalTokens != want.TotalTokens {
			t.Errorf("Hours[%s] = %+v, want counters %+v", strconv.Itoa(hour), got, want)
		}
	}
}

func TestLoadUsageSeriesKeepsStableChannelModelDetails(t *testing.T) {
	today := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02")
	doc := usageFileDocument{
		SchemaVersion: 5,
		Timezone:      "Asia/Shanghai",
		Daily: []usageFileDaily{{
			Date: today,
			ByModel: map[string]usageFileModelStat{
				"channel-baibei": {
					Model:        "baibei-claude-opus-4-8",
					ModelKey:     "channel-baibei",
					ChannelID:    "channel-baibei",
					ChannelName:  "baibei-claude-opus-4-8",
					WireModel:    "claude-opus-5",
					ProviderID:   "provider-baibei",
					ProviderName: "Baibei",
					usageFileCounters: usageFileCounters{
						ProviderCalls: 1,
						TotalTokens:   123,
					},
				},
			},
		}},
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	series, err := LoadUsageSeries(path, 1)
	if err != nil {
		t.Fatalf("LoadUsageSeries() error = %v", err)
	}
	if len(series.Days) != 1 || len(series.Days[0].Models) != 1 {
		t.Fatalf("day models = %+v, want one model", series.Days)
	}
	model := series.Days[0].Models[0]
	if model.ModelKey != "channel-baibei" || model.Model != "baibei-claude-opus-4-8" {
		t.Fatalf("model identity = %+v", model)
	}
	if model.WireModel != "claude-opus-5" || model.ProviderName != "Baibei" {
		t.Fatalf("model details = %+v", model)
	}
}

func TestLoadUsageSeriesReadsLegacyUsageJSON(t *testing.T) {
	today := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02")
	legacy := map[string]any{
		"schema_version": 4,
		"daily": []any{map[string]any{
			"date": today,
			"by_hour": map[string]any{
				"7": map[string]any{"provider_calls": 2, "total_tokens": 88},
			},
			"by_model": map[string]any{
				"baibei-claude-opus-4-8": map[string]any{
					"model":          "baibei-claude-opus-4-8",
					"provider_calls": 2,
					"total_tokens":   88,
				},
			},
		}},
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	series, err := LoadUsageSeries(path, 1)
	if err != nil {
		t.Fatalf("LoadUsageSeries() error = %v", err)
	}
	if !series.LegacyUTCDates {
		t.Fatal("LegacyUTCDates = false, want true for schema v4")
	}
	if series.Hours[7].ProviderCalls != 2 || series.Hours[7].TotalTokens != 88 {
		t.Fatalf("legacy hour = %+v", series.Hours[7])
	}
	if len(series.Days[0].Models) != 1 || series.Days[0].Models[0].ModelKey != "baibei-claude-opus-4-8" {
		t.Fatalf("legacy models = %+v", series.Days[0].Models)
	}
}
