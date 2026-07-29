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
	ProviderCalls     int64 `json:"provider_calls"`
	TurnsTotal        int64 `json:"turns_total"`
	ValidTurnsTotal   int64 `json:"valid_turns_total"`
	InvalidTurnsTotal int64 `json:"invalid_turns_total"`
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CacheReadTokens   int64 `json:"cache_read_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_tokens"`
	TotalTokens       int64 `json:"total_tokens"`
}

type usageFileProviderStat struct {
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	usageFileCounters
}

type usageFileModelStat struct {
	Model        string `json:"model"`
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	usageFileCounters
}

type usageFileDaily struct {
	Date string `json:"date"`
	usageFileCounters
	ByProvider map[string]usageFileProviderStat `json:"by_provider"`
	ByModel    map[string]usageFileModelStat    `json:"by_model"`
	ByHour     map[string]usageFileCounters     `json:"by_hour"`
}

type usageFileDocument struct {
	Totals struct {
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
		Days:      points,
		Providers: providerPointsFrom(doc.Totals.ByProvider),
		Models:    modelPointsFrom(doc.Totals.ByModel),
		Hours:     hours,
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
		points = append(points, HourPoint{Hour: hour})
	}
	return points
}

func accumulateHourPoints(points []HourPoint, buckets map[string]usageFileCounters) {
	for key, counters := range buckets {
		hour, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil || hour < 0 || hour > 23 {
			continue
		}
		points[hour].ProviderCalls += counters.ProviderCalls
		points[hour].TotalTokens += counters.TotalTokens
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
		InputTokens:        counters.InputTokens,
		OutputTokens:       counters.OutputTokens,
		CacheReadTokens:    counters.CacheReadTokens,
		CacheWriteTokens:   counters.CacheWriteTokens,
		PromptTokensTotal:  counters.InputTokens + counters.CacheReadTokens + counters.CacheWriteTokens,
		RequestTokensTotal: counters.TotalTokens,
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
	}
}

func buildEmptyDayPoints(days int) []DayPoint {
	today := time.Now().UTC().Truncate(24 * time.Hour)
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
		Date:             date,
		ProviderCalls:    item.ProviderCalls,
		TurnsTotal:       item.TurnsTotal,
		InputTokens:      item.InputTokens,
		OutputTokens:     item.OutputTokens,
		CacheReadTokens:  item.CacheReadTokens,
		CacheWriteTokens: item.CacheWriteTokens,
		TotalTokens:      item.TotalTokens,
		Providers:        providerPointsFrom(item.ByProvider),
		Models:           modelPointsFrom(item.ByModel),
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
			Model:            model,
			ProviderID:       strings.TrimSpace(stat.ProviderID),
			ProviderName:     providerName,
			ProviderCalls:    stat.ProviderCalls,
			TurnsTotal:       stat.TurnsTotal,
			InputTokens:      stat.InputTokens,
			OutputTokens:     stat.OutputTokens,
			CacheReadTokens:  stat.CacheReadTokens,
			CacheWriteTokens: stat.CacheWriteTokens,
			TotalTokens:      stat.TotalTokens,
		})
	}
	sort.Slice(points, func(i int, j int) bool {
		if points[i].TotalTokens == points[j].TotalTokens {
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
			ProviderID:       providerID,
			ProviderName:     name,
			ProviderCalls:    stat.ProviderCalls,
			TurnsTotal:       stat.TurnsTotal,
			InputTokens:      stat.InputTokens,
			OutputTokens:     stat.OutputTokens,
			CacheReadTokens:  stat.CacheReadTokens,
			CacheWriteTokens: stat.CacheWriteTokens,
			TotalTokens:      stat.TotalTokens,
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
