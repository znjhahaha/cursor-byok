package historymetrics

type Summary struct {
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

type Totals struct {
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	PromptTokensTotal  int64
	RequestTokensTotal int64
}

// ProviderPoint 是单个中转站的用量聚合。
type ProviderPoint struct {
	ProviderID       string `json:"providerID"`
	ProviderName     string `json:"providerName"`
	ProviderCalls    int64  `json:"providerCalls"`
	TurnsTotal       int64  `json:"turnsTotal"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheReadTokens  int64  `json:"cacheReadTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens"`
	TotalTokens      int64  `json:"totalTokens"`
}

// ModelPoint 是单个模型的用量聚合，ProviderName 表示最近一次调用的来源站点。
type ModelPoint struct {
	Model            string `json:"model"`
	ProviderID       string `json:"providerID"`
	ProviderName     string `json:"providerName"`
	ProviderCalls    int64  `json:"providerCalls"`
	TurnsTotal       int64  `json:"turnsTotal"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheReadTokens  int64  `json:"cacheReadTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens"`
	TotalTokens      int64  `json:"totalTokens"`
}

// DayPoint 是单个自然日的用量聚合，Providers / Models 为该日的细分。
type DayPoint struct {
	Date             string          `json:"date"`
	ProviderCalls    int64           `json:"providerCalls"`
	TurnsTotal       int64           `json:"turnsTotal"`
	InputTokens      int64           `json:"inputTokens"`
	OutputTokens     int64           `json:"outputTokens"`
	CacheReadTokens  int64           `json:"cacheReadTokens"`
	CacheWriteTokens int64           `json:"cacheWriteTokens"`
	TotalTokens      int64           `json:"totalTokens"`
	Providers        []ProviderPoint `json:"providers"`
	Models           []ModelPoint    `json:"models"`
}

// HourPoint 是单个整点（UTC）的用量聚合，供「按小时分布」视图使用。
type HourPoint struct {
	Hour          int   `json:"hour"`
	ProviderCalls int64 `json:"providerCalls"`
	TotalTokens   int64 `json:"totalTokens"`
}

// Series 同时承载按天、按中转站、按模型三个统计维度。
type Series struct {
	Days      []DayPoint      `json:"days"`
	Providers []ProviderPoint `json:"providers"`
	Models    []ModelPoint    `json:"models"`
	// Hours 覆盖所选天数区间的 0-23 整点聚合；历史日期没有小时分桶，
	// 因此它只反映引入该统计之后产生的调用。
	Hours []HourPoint `json:"hours"`
}

func cacheHitRateFromTotals(totals Totals) *float64 {
	inputCacheTokensTotal := totals.CacheReadTokens + totals.InputTokens
	if inputCacheTokensTotal <= 0 {
		return nil
	}
	value := float64(totals.CacheReadTokens) / float64(inputCacheTokensTotal)
	return &value
}
