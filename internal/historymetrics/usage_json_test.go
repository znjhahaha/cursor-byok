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
	today := time.Now().UTC().Format("2006-01-02")
	doc := usageFileDocument{
		Daily: []usageFileDaily{
			{
				Date: today,
				ByHour: map[string]usageFileCounters{
					"9":  {ProviderCalls: 2, TotalTokens: 120},
					"21": {ProviderCalls: 1, TotalTokens: 45},
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
		if got != want {
			t.Errorf("Hours[%s] = %+v, want %+v", strconv.Itoa(hour), got, want)
		}
	}
}
