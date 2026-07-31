package historymetrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// usageFileCounters 对应 usage.json 中各聚合口径共用的计数字段。
type usageFileCounters struct {
	ProviderCalls         int64 `json:"provider_calls"`
	TurnsTotal            int64 `json:"turns_total"`
	ValidTurnsTotal       int64 `json:"valid_turns_total"`
	InvalidTurnsTotal     int64 `json:"invalid_turns_total"`
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	CacheReadTokens       int64 `json:"cache_read_tokens"`
	CacheWriteTokens      int64 `json:"cache_write_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
	EstimatedInputTokens  int64 `json:"estimated_input_tokens"`
	EstimatedOutputTokens int64 `json:"estimated_output_tokens"`
	UnreportedCalls       int64 `json:"unreported_calls"`
}

type usageFileProviderStat struct {
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	usageFileCounters
}

type usageFileModelStat struct {
	Model        string `json:"model"`
	ModelKey     string `json:"model_key"`
	ChannelID    string `json:"channel_id"`
	ChannelName  string `json:"channel_name"`
	WireModel    string `json:"wire_model"`
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	usageFileCounters
}

type usageFileHourStat struct {
	usageFileCounters
	ByProvider map[string]usageFileProviderStat `json:"by_provider"`
	ByModel    map[string]usageFileModelStat    `json:"by_model"`
}

type usageFileDaily struct {
	Date string `json:"date"`
	usageFileCounters
	ByProvider map[string]usageFileProviderStat `json:"by_provider"`
	ByModel    map[string]usageFileModelStat    `json:"by_model"`
	ByHour     map[string]usageFileHourStat     `json:"by_hour"`
}

type usageFileDocument struct {
	SchemaVersion  int    `json:"schema_version"`
	Timezone       string `json:"timezone"`
	LegacyUTCDates bool   `json:"legacy_utc_dates"`
	Totals         struct {
		usageFileCounters
		ByProvider map[string]usageFileProviderStat `json:"by_provider"`
		ByModel    map[string]usageFileModelStat    `json:"by_model"`
	} `json:"totals"`
	Daily []usageFileDaily `json:"daily"`
}

func LoadUsageSummary(path string) (Summary, error) {
	doc, ok, err := readUsageFileDocument(path)
	if err != nil || !ok {
		return Summary{}, err
	}
	return summaryFromCounters(doc.Totals.usageFileCounters), nil
}

// LoadUsageSeries 返回按天与按中转站两个维度的用量序列。
//
// days 指定回溯的自然日数量（含今天）；序列会补齐没有调用的日期，
// 让图表拿到的是连续的时间轴，无需在前端补洞。
func LoadUsageSeries(path string, days int) (Series, error) {
	days = normalizeSeriesDays(days)
	empty := Series{
		Days:      buildEmptyDayPoints(days),
		Providers: []ProviderPoint{},
		Models:    []ModelPoint{},
		Hours:     buildEmptyHourPoints(),
		Timezone:  "Asia/Shanghai",
	}

	doc, ok, err := readUsageFileDocument(path)
	if err != nil || !ok {
		return empty, err
	}

	byDate := make(map[string]usageFileDaily, len(doc.Daily))
	for _, item := range doc.Daily {
		date := strings.TrimSpace(item.Date)
		if date != "" {
			byDate[date] = item
		}
	}

	points := buildEmptyDayPoints(days)
	hours := buildEmptyHourPoints()
	for index := range points {
		item, exists := byDate[points[index].Date]
		if !exists {
			continue
		}
		points[index] = dayPointFrom(points[index].Date, item)
		accumulateHourPoints(hours, item.ByHour)
	}

	return Series{
		Days:           points,
		Providers:      providerPointsFrom(doc.Totals.ByProvider),
		Models:         modelPointsFrom(doc.Totals.ByModel),
		Hours:          hours,
		Timezone:       firstNonEmpty(doc.Timezone, "Asia/Shanghai"),
		LegacyUTCDates: doc.LegacyUTCDates || doc.SchemaVersion < 5,
	}, nil
}

func normalizeSeriesDays(days int) int {
	if days <= 0 {
		return 30
	}
	return days
}

// buildEmptyHourPoints 铺出完整的 0-23 整点轴，前端无需补洞。
func buildEmptyHourPoints() []HourPoint {
	points := make([]HourPoint, 0, 24)
	for hour := 0; hour < 24; hour++ {
		points = append(points, HourPoint{
			Hour:      hour,
			Providers: []ProviderPoint{},
			Models:    []ModelPoint{},
		})
	}
	return points
}

func accumulateHourPoints(points []HourPoint, buckets map[string]usageFileHourStat) {
	for key, counters := range buckets {
		hour, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil || hour < 0 || hour > 23 {
			continue
		}
		points[hour].ProviderCalls += counters.ProviderCalls
		points[hour].TotalTokens += counters.TotalTokens
		points[hour].EstimatedTokens += counters.EstimatedInputTokens + counters.EstimatedOutputTokens
		points[hour].EstimatedInputTokens += counters.EstimatedInputTokens
		points[hour].UnreportedCalls += counters.UnreportedCalls
		points[hour].Providers = mergeProviderPoints(points[hour].Providers, providerPointsFrom(counters.ByProvider))
		points[hour].Models = mergeModelPoints(points[hour].Models, modelPointsFrom(counters.ByModel))
	}
}

func readUsageFileDocument(path string) (usageFileDocument, bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return usageFileDocument{}, false, nil
		}
		return usageFileDocument{}, false, fmt.Errorf("read usage file: %w", err)
	}
	var doc usageFileDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return usageFileDocument{}, false, fmt.Errorf("decode usage file: %w", err)
	}
	return doc, true, nil
}

func summaryFromCounters(counters usageFileCounters) Summary {
	totals := Totals{
		InputTokens:          counters.InputTokens,
		OutputTokens:         counters.OutputTokens,
		CacheReadTokens:      counters.CacheReadTokens,
		CacheWriteTokens:     counters.CacheWriteTokens,
		PromptTokensTotal:    counters.InputTokens + counters.CacheReadTokens + counters.CacheWriteTokens,
		RequestTokensTotal:   counters.TotalTokens,
		EstimatedInputTokens: counters.EstimatedInputTokens,
	}
	return Summary{
		ProviderCallsTotal: int(counters.ProviderCalls),
		TurnsTotal:         int(counters.TurnsTotal),
		ValidTurnsTotal:    int(counters.ValidTurnsTotal),
		InvalidTurnsTotal:  int(counters.InvalidTurnsTotal),
		RequestTokensTotal: totals.RequestTokensTotal,
		PromptTokensTotal:  totals.PromptTokensTotal,
		CacheReadTokens:    totals.CacheReadTokens,
		CacheWriteTokens:   totals.CacheWriteTokens,
		CacheHitRate:       cacheHitRateFromTotals(totals),
		EstimatedTokens:    counters.EstimatedInputTokens + counters.EstimatedOutputTokens,
		UnreportedCalls:    counters.UnreportedCalls,
	}
}

func buildEmptyDayPoints(days int) []DayPoint {
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	now := time.Now().In(shanghai)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, shanghai)
	points := make([]DayPoint, 0, days)
	for offset := days - 1; offset >= 0; offset-- {
		points = append(points, DayPoint{
			Date:      today.AddDate(0, 0, -offset).Format("2006-01-02"),
			Providers: []ProviderPoint{},
			Models:    []ModelPoint{},
		})
	}
	return points
}

func dayPointFrom(date string, item usageFileDaily) DayPoint {
	return DayPoint{
		Date:                 date,
		ProviderCalls:        item.ProviderCalls,
		TurnsTotal:           item.TurnsTotal,
		InputTokens:          item.InputTokens,
		OutputTokens:         item.OutputTokens,
		CacheReadTokens:      item.CacheReadTokens,
		CacheWriteTokens:     item.CacheWriteTokens,
		TotalTokens:          item.TotalTokens,
		EstimatedTokens:      item.EstimatedInputTokens + item.EstimatedOutputTokens,
		EstimatedInputTokens: item.EstimatedInputTokens,
		UnreportedCalls:      item.UnreportedCalls,
		Providers:            providerPointsFrom(item.ByProvider),
		Models:               modelPointsFrom(item.ByModel),
	}
}

// modelPointsFrom 与 providerPointsFrom 同构，把模型分桶展开成按 token 量降序的稳定序列。
func modelPointsFrom(buckets map[string]usageFileModelStat) []ModelPoint {
	points := make([]ModelPoint, 0, len(buckets))
	for key, stat := range buckets {
		model := strings.TrimSpace(stat.Model)
		if model == "" {
			model = strings.TrimSpace(key)
		}
		if model == "" {
			continue
		}
		providerName := strings.TrimSpace(stat.ProviderName)
		if providerName == "" {
			providerName = strings.TrimSpace(stat.ProviderID)
		}
		points = append(points, ModelPoint{
			ModelKey:             firstNonEmpty(strings.TrimSpace(stat.ModelKey), strings.TrimSpace(key)),
			Model:                model,
			ChannelID:            strings.TrimSpace(stat.ChannelID),
			ChannelName:          strings.TrimSpace(stat.ChannelName),
			WireModel:            strings.TrimSpace(stat.WireModel),
			ProviderID:           strings.TrimSpace(stat.ProviderID),
			ProviderName:         providerName,
			ProviderCalls:        stat.ProviderCalls,
			TurnsTotal:           stat.TurnsTotal,
			InputTokens:          stat.InputTokens,
			OutputTokens:         stat.OutputTokens,
			CacheReadTokens:      stat.CacheReadTokens,
			CacheWriteTokens:     stat.CacheWriteTokens,
			TotalTokens:          stat.TotalTokens,
			EstimatedTokens:      stat.EstimatedInputTokens + stat.EstimatedOutputTokens,
			EstimatedInputTokens: stat.EstimatedInputTokens,
			UnreportedCalls:      stat.UnreportedCalls,
		})
	}
	sort.Slice(points, func(i int, j int) bool {
		if points[i].TotalTokens == points[j].TotalTokens {
			if points[i].Model == points[j].Model {
				return points[i].ModelKey < points[j].ModelKey
			}
			return points[i].Model < points[j].Model
		}
		return points[i].TotalTokens > points[j].TotalTokens
	})
	return points
}

// providerPointsFrom 把 map 分桶展开成按 token 量降序的稳定序列，供前端直接渲染。
func providerPointsFrom(buckets map[string]usageFileProviderStat) []ProviderPoint {
	points := make([]ProviderPoint, 0, len(buckets))
	for id, stat := range buckets {
		providerID := strings.TrimSpace(stat.ProviderID)
		if providerID == "" {
			providerID = strings.TrimSpace(id)
		}
		if providerID == "" {
			continue
		}
		name := strings.TrimSpace(stat.ProviderName)
		if name == "" {
			name = providerID
		}
		points = append(points, ProviderPoint{
			ProviderID:           providerID,
			ProviderName:         name,
			ProviderCalls:        stat.ProviderCalls,
			TurnsTotal:           stat.TurnsTotal,
			InputTokens:          stat.InputTokens,
			OutputTokens:         stat.OutputTokens,
			CacheReadTokens:      stat.CacheReadTokens,
			CacheWriteTokens:     stat.CacheWriteTokens,
			TotalTokens:          stat.TotalTokens,
			EstimatedTokens:      stat.EstimatedInputTokens + stat.EstimatedOutputTokens,
			EstimatedInputTokens: stat.EstimatedInputTokens,
			UnreportedCalls:      stat.UnreportedCalls,
		})
	}
	sort.Slice(points, func(i int, j int) bool {
		if points[i].TotalTokens == points[j].TotalTokens {
			return points[i].ProviderID < points[j].ProviderID
		}
		return points[i].TotalTokens > points[j].TotalTokens
	})
	return points
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func mergeProviderPoints(current []ProviderPoint, next []ProviderPoint) []ProviderPoint {
	byID := make(map[string]ProviderPoint, len(current)+len(next))
	for _, point := range append(current, next...) {
		item := byID[point.ProviderID]
		if item.ProviderID == "" {
			item.ProviderID = point.ProviderID
			item.ProviderName = point.ProviderName
		}
		item.ProviderCalls += point.ProviderCalls
		item.TurnsTotal += point.TurnsTotal
		item.InputTokens += point.InputTokens
		item.OutputTokens += point.OutputTokens
		item.CacheReadTokens += point.CacheReadTokens
		item.CacheWriteTokens += point.CacheWriteTokens
		item.TotalTokens += point.TotalTokens
		item.EstimatedTokens += point.EstimatedTokens
		item.EstimatedInputTokens += point.EstimatedInputTokens
		item.UnreportedCalls += point.UnreportedCalls
		byID[point.ProviderID] = item
	}
	result := make([]ProviderPoint, 0, len(byID))
	for _, item := range byID {
		result = append(result, item)
	}
	sort.Slice(result, func(i int, j int) bool {
		return result[i].ProviderID < result[j].ProviderID
	})
	return result
}

func mergeModelPoints(current []ModelPoint, next []ModelPoint) []ModelPoint {
	byKey := make(map[string]ModelPoint, len(current)+len(next))
	for _, point := range append(current, next...) {
		key := firstNonEmpty(point.ModelKey, "legacy:"+point.Model)
		item := byKey[key]
		if item.ModelKey == "" {
			item = point
		} else {
			item.ProviderCalls += point.ProviderCalls
			item.TurnsTotal += point.TurnsTotal
			item.InputTokens += point.InputTokens
			item.OutputTokens += point.OutputTokens
			item.CacheReadTokens += point.CacheReadTokens
			item.CacheWriteTokens += point.CacheWriteTokens
			item.TotalTokens += point.TotalTokens
			item.EstimatedTokens += point.EstimatedTokens
			item.EstimatedInputTokens += point.EstimatedInputTokens
			item.UnreportedCalls += point.UnreportedCalls
		}
		item.ModelKey = key
		byKey[key] = item
	}
	result := make([]ModelPoint, 0, len(byKey))
	for _, item := range byKey {
		result = append(result, item)
	}
	sort.Slice(result, func(i int, j int) bool {
		return result[i].ModelKey < result[j].ModelKey
	})
	return result
}
