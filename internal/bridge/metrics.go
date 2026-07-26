package bridge

import (
	"cursor/internal/appdata"
	"cursor/internal/historymetrics"
)

// HomeMetricsSummary 定义首页展示的历史统计摘要。
type HomeMetricsSummary struct {
	ProviderCallsTotal int      `json:"providerCallsTotal"`
	TurnsTotal         int      `json:"turnsTotal"`
	ValidTurnsTotal    int      `json:"validTurnsTotal"`
	InvalidTurnsTotal  int      `json:"invalidTurnsTotal"`
	RequestTokensTotal int64    `json:"requestTokensTotal"`
	PromptTokensTotal  int64    `json:"promptTokensTotal"`
	CacheReadTokens    int64    `json:"cacheReadTokens"`
	CacheWriteTokens   int64    `json:"cacheWriteTokens"`
	CacheHitRate       *float64 `json:"cacheHitRate"`
}

// UsageSeries 定义按天与按中转站的用量序列。
type UsageSeries = historymetrics.Series

// UsageDayPoint 定义单日用量。
type UsageDayPoint = historymetrics.DayPoint

// UsageProviderPoint 定义单个中转站的用量。
type UsageProviderPoint = historymetrics.ProviderPoint

// MetricsService 定义首页统计相关的 Wails service。
type MetricsService struct{}

// NewMetricsService 创建首页统计 service。
func NewMetricsService() *MetricsService {
	return &MetricsService{}
}

// GetHomeMetricsSummary 返回首页展示的全量历史统计摘要。
func (service *MetricsService) GetHomeMetricsSummary() (HomeMetricsSummary, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return HomeMetricsSummary{}, err
	}

	summary, err := historymetrics.LoadUsageSummary(appdata.UsageFilePath())
	if err != nil {
		return HomeMetricsSummary{}, err
	}
	return HomeMetricsSummary{
		ProviderCallsTotal: summary.ProviderCallsTotal,
		TurnsTotal:         summary.TurnsTotal,
		ValidTurnsTotal:    summary.ValidTurnsTotal,
		InvalidTurnsTotal:  summary.InvalidTurnsTotal,
		RequestTokensTotal: summary.RequestTokensTotal,
		PromptTokensTotal:  summary.PromptTokensTotal,
		CacheReadTokens:    summary.CacheReadTokens,
		CacheWriteTokens:   summary.CacheWriteTokens,
		CacheHitRate:       summary.CacheHitRate,
	}, nil
}

// GetUsageSeries 返回最近 days 天的按天用量与按中转站聚合。
func (service *MetricsService) GetUsageSeries(days int) (UsageSeries, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return UsageSeries{}, err
	}
	return historymetrics.LoadUsageSeries(appdata.UsageFilePath(), days)
}
