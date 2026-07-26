package forwarder

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	usageFileName          = "usage.json"
	usageFileSchemaVersion = 3
	usageRecentEventLimit  = 500

	usageEventKindProvider = "provider_call"
	usageEventKindTurn     = "turn_finalized"
	usageTurnStatusDone    = "completed"

	// usageProviderUnknownID 收纳未归属中转站的调用，包括本次改造前产生的历史数据。
	usageProviderUnknownID   = "unknown"
	usageProviderUnknownName = "未归属"

	// usageModelUnknown 收纳没有解析出模型名的调用，口径与 provider 的 unknown 桶一致。
	usageModelUnknown = "未知模型"
)

type UsageFileStore struct {
	path string
}

type usageFileDocument struct {
	SchemaVersion int                       `json:"schema_version"`
	UpdatedAt     time.Time                 `json:"updated_at"`
	Totals        usageFileTotals           `json:"totals"`
	Daily         []usageFileDaily          `json:"daily"`
	RecentEvents  []usageFileEvent          `json:"recent_events"`
	EventIndex    map[string]usageFileEvent `json:"event_index,omitempty"`
}

// usageFileCounters 是所有聚合口径共用的计数字段。
// 以匿名嵌入方式复用：encoding/json 会把这些字段提升到外层，磁盘格式与 v2 保持一致。
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

type usageFileTotals struct {
	usageFileCounters
	ByProvider map[string]usageFileProviderStat `json:"by_provider,omitempty"`
	ByModel    map[string]usageFileModelStat    `json:"by_model,omitempty"`
}

type usageFileDaily struct {
	Date string `json:"date"`
	usageFileCounters
	ByProvider map[string]usageFileProviderStat `json:"by_provider,omitempty"`
	ByModel    map[string]usageFileModelStat    `json:"by_model,omitempty"`
}

type usageFileProviderStat struct {
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name,omitempty"`
	usageFileCounters
}

type usageFileModelStat struct {
	Model string `json:"model"`
	// ProviderID 记录该模型最近一次调用所属的中转站，供前端做「模型 → 来源」的下钻展示。
	ProviderID   string `json:"provider_id,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
	usageFileCounters
}

// usageBucketKey 是分桶身份（中转站 + 模型），不参与加减，因此与 usageFileDelta 分开传递。
// 新增统计维度时只需扩展本结构，applyUsageFileDelta 的签名保持稳定。
type usageBucketKey struct {
	ProviderID   string
	ProviderName string
	Model        string
}

type usageFileEvent struct {
	EventID string `json:"event_id"`
	Kind    string `json:"kind,omitempty"`
	Status  string `json:"status,omitempty"`
	// ProviderID 标识调用所属中转站，空值会归入 unknown 桶。
	ProviderID       string    `json:"provider_id,omitempty"`
	ProviderName     string    `json:"provider_name,omitempty"`
	Model            string    `json:"model,omitempty"`
	At               time.Time `json:"at"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	UsagePresent     bool      `json:"usage_present"`
}

type usageFileDelta struct {
	providerCalls     int64
	turnsTotal        int64
	validTurnsTotal   int64
	invalidTurnsTotal int64
	inputTokens       int64
	outputTokens      int64
	cacheReadTokens   int64
	cacheWriteTokens  int64
	totalTokens       int64
}

func NewUsageFileStore(historyRoot string) *UsageFileStore {
	return &UsageFileStore{path: filepath.Join(strings.TrimSpace(historyRoot), usageFileName)}
}

func (store *UsageFileStore) UpsertEvent(event usageFileEvent) error {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return nil
	}
	event.EventID = strings.TrimSpace(event.EventID)
	if event.EventID == "" {
		return nil
	}
	event.Kind = normalizeUsageEventKind(event.Kind)
	event.Status = strings.TrimSpace(event.Status)
	event.ProviderID = strings.TrimSpace(event.ProviderID)
	event.ProviderName = strings.TrimSpace(event.ProviderName)
	event.Model = strings.TrimSpace(event.Model)
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	} else {
		event.At = event.At.UTC()
	}
	event.InputTokens = nonNegativeInt64(event.InputTokens)
	event.OutputTokens = nonNegativeInt64(event.OutputTokens)
	event.CacheReadTokens = nonNegativeInt64(event.CacheReadTokens)
	event.CacheWriteTokens = nonNegativeInt64(event.CacheWriteTokens)
	event.TotalTokens = event.InputTokens + event.OutputTokens + event.CacheReadTokens + event.CacheWriteTokens

	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return fmt.Errorf("create usage directory: %w", err)
	}
	release, err := acquireConversationLock(store.path + ".lock")
	if err != nil {
		return err
	}
	defer release()

	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		return err
	}
	if doc.EventIndex == nil {
		doc.EventIndex = make(map[string]usageFileEvent)
	}
	oldEvent, found := doc.EventIndex[event.EventID]
	if found {
		// 按旧事件自身的归属回滚，避免事件改归属后源桶残留计数。
		applyUsageFileDelta(&doc, oldEvent.At, usageFileEventBucketKey(oldEvent), negateUsageFileDelta(usageFileEventDelta(oldEvent)))
	}
	applyUsageFileDelta(&doc, event.At, usageFileEventBucketKey(event), usageFileEventDelta(event))
	doc.RecentEvents = upsertRecentUsageEvent(doc.RecentEvents, event)
	doc.RecentEvents = trimRecentUsageEvents(doc.RecentEvents, usageRecentEventLimit)
	doc.EventIndex = buildUsageEventIndex(doc.RecentEvents)
	doc.SchemaVersion = usageFileSchemaVersion
	doc.UpdatedAt = time.Now().UTC()
	return writeJSONFileAtomic(store.path, doc)
}

func (store *UsageFileStore) LookupEvent(needle string) (usageFileEvent, bool, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return usageFileEvent{}, false, nil
	}
	doc, err := readUsageFileDocument(store.path)
	if err != nil {
		return usageFileEvent{}, false, err
	}
	trimmed := strings.TrimSpace(needle)
	if trimmed == "" {
		return usageFileEvent{}, false, nil
	}
	var aggregate usageFileEvent
	found := false
	events := doc.EventIndex
	if len(events) == 0 {
		events = make(map[string]usageFileEvent, len(doc.RecentEvents))
		for _, event := range doc.RecentEvents {
			if eventID := strings.TrimSpace(event.EventID); eventID != "" {
				events[eventID] = event
			}
		}
	}
	for _, event := range events {
		eventID := strings.TrimSpace(event.EventID)
		if eventID != trimmed && !strings.HasPrefix(eventID, trimmed+"::") {
			continue
		}
		if !found {
			aggregate = usageFileEvent{EventID: trimmed, At: event.At}
			found = true
		}
		if event.At.After(aggregate.At) {
			aggregate.At = event.At
		}
		// 同一 requestID 下的多次 provider 调用理论上同源，取最后出现的归属即可。
		if strings.TrimSpace(event.ProviderID) != "" {
			aggregate.ProviderID = strings.TrimSpace(event.ProviderID)
			aggregate.ProviderName = strings.TrimSpace(event.ProviderName)
		}
		if strings.TrimSpace(event.Model) != "" {
			aggregate.Model = strings.TrimSpace(event.Model)
		}
		aggregate.InputTokens += nonNegativeInt64(event.InputTokens)
		aggregate.OutputTokens += nonNegativeInt64(event.OutputTokens)
		aggregate.CacheReadTokens += nonNegativeInt64(event.CacheReadTokens)
		aggregate.CacheWriteTokens += nonNegativeInt64(event.CacheWriteTokens)
		aggregate.TotalTokens += nonNegativeInt64(event.TotalTokens)
		aggregate.UsagePresent = aggregate.UsagePresent || event.UsagePresent
	}
	if found {
		return aggregate, true, nil
	}
	return usageFileEvent{}, false, nil
}

func readUsageFileDocument(path string) (usageFileDocument, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return usageFileDocument{
				SchemaVersion: usageFileSchemaVersion,
				Daily:         make([]usageFileDaily, 0),
				RecentEvents:  make([]usageFileEvent, 0),
				EventIndex:    make(map[string]usageFileEvent),
			}, nil
		}
		return usageFileDocument{}, fmt.Errorf("read usage file: %w", err)
	}
	var doc usageFileDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return usageFileDocument{}, fmt.Errorf("decode usage file: %w", err)
	}
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = 1
	}
	doc.RecentEvents = trimRecentUsageEvents(doc.RecentEvents, usageRecentEventLimit)
	if len(doc.EventIndex) == 0 {
		doc.EventIndex = buildUsageEventIndex(doc.RecentEvents)
	}
	return doc, nil
}

func upsertRecentUsageEvent(items []usageFileEvent, event usageFileEvent) []usageFileEvent {
	event.EventID = strings.TrimSpace(event.EventID)
	if event.EventID == "" {
		return items
	}
	next := make([]usageFileEvent, 0, len(items)+1)
	next = append(next, event)
	for _, item := range items {
		if strings.TrimSpace(item.EventID) == event.EventID {
			continue
		}
		next = append(next, item)
	}
	return next
}

func trimRecentUsageEvents(items []usageFileEvent, limit int) []usageFileEvent {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[:limit]
}

func buildUsageEventIndex(items []usageFileEvent) map[string]usageFileEvent {
	index := make(map[string]usageFileEvent, len(items))
	for _, event := range items {
		event.EventID = strings.TrimSpace(event.EventID)
		if event.EventID == "" {
			continue
		}
		event.Kind = normalizeUsageEventKind(event.Kind)
		index[event.EventID] = event
	}
	return index
}

func normalizeUsageEventKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case usageEventKindTurn:
		return usageEventKindTurn
	default:
		return usageEventKindProvider
	}
}

func usageFileEventBucketKey(event usageFileEvent) usageBucketKey {
	return normalizeUsageBucketKey(event.ProviderID, event.ProviderName, event.Model)
}

func usageFileEventDelta(event usageFileEvent) usageFileDelta {	switch normalizeUsageEventKind(event.Kind) {
	case usageEventKindTurn:
		delta := usageFileDelta{turnsTotal: 1}
		if strings.TrimSpace(event.Status) == usageTurnStatusDone {
			delta.validTurnsTotal = 1
		} else {
			delta.invalidTurnsTotal = 1
		}
		return delta
	default:
		return usageFileDelta{
			providerCalls:    1,
			inputTokens:      nonNegativeInt64(event.InputTokens),
			outputTokens:     nonNegativeInt64(event.OutputTokens),
			cacheReadTokens:  nonNegativeInt64(event.CacheReadTokens),
			cacheWriteTokens: nonNegativeInt64(event.CacheWriteTokens),
			totalTokens:      nonNegativeInt64(event.TotalTokens),
		}
	}
}

func negateUsageFileDelta(value usageFileDelta) usageFileDelta {
	return usageFileDelta{
		providerCalls:     -value.providerCalls,
		turnsTotal:        -value.turnsTotal,
		validTurnsTotal:   -value.validTurnsTotal,
		invalidTurnsTotal: -value.invalidTurnsTotal,
		inputTokens:       -value.inputTokens,
		outputTokens:      -value.outputTokens,
		cacheReadTokens:   -value.cacheReadTokens,
		cacheWriteTokens:  -value.cacheWriteTokens,
		totalTokens:       -value.totalTokens,
	}
}

func applyUsageFileDelta(doc *usageFileDocument, at time.Time, bucket usageBucketKey, delta usageFileDelta) {
	if doc == nil {
		return
	}
	applyUsageCounters(&doc.Totals.usageFileCounters, delta)
	doc.Totals.ByProvider = applyUsageProviderDelta(doc.Totals.ByProvider, bucket, delta)
	doc.Totals.ByModel = applyUsageModelDelta(doc.Totals.ByModel, bucket, delta)

	date := at.UTC().Format("2006-01-02")
	for index := range doc.Daily {
		if doc.Daily[index].Date != date {
			continue
		}
		applyUsageCounters(&doc.Daily[index].usageFileCounters, delta)
		doc.Daily[index].ByProvider = applyUsageProviderDelta(doc.Daily[index].ByProvider, bucket, delta)
		doc.Daily[index].ByModel = applyUsageModelDelta(doc.Daily[index].ByModel, bucket, delta)
		return
	}
	item := usageFileDaily{Date: date}
	applyUsageCounters(&item.usageFileCounters, delta)
	item.ByProvider = applyUsageProviderDelta(item.ByProvider, bucket, delta)
	item.ByModel = applyUsageModelDelta(item.ByModel, bucket, delta)
	doc.Daily = append(doc.Daily, item)
}

// applyUsageCounters 是全部聚合口径唯一的累加实现，新增统计维度时只需复用它。
func applyUsageCounters(counters *usageFileCounters, delta usageFileDelta) {
	if counters == nil {
		return
	}
	counters.ProviderCalls = clampNonNegativeInt64(counters.ProviderCalls + delta.providerCalls)
	counters.TurnsTotal = clampNonNegativeInt64(counters.TurnsTotal + delta.turnsTotal)
	counters.ValidTurnsTotal = clampNonNegativeInt64(counters.ValidTurnsTotal + delta.validTurnsTotal)
	counters.InvalidTurnsTotal = clampNonNegativeInt64(counters.InvalidTurnsTotal + delta.invalidTurnsTotal)
	counters.InputTokens = clampNonNegativeInt64(counters.InputTokens + delta.inputTokens)
	counters.OutputTokens = clampNonNegativeInt64(counters.OutputTokens + delta.outputTokens)
	counters.CacheReadTokens = clampNonNegativeInt64(counters.CacheReadTokens + delta.cacheReadTokens)
	counters.CacheWriteTokens = clampNonNegativeInt64(counters.CacheWriteTokens + delta.cacheWriteTokens)
	counters.TotalTokens = clampNonNegativeInt64(counters.TotalTokens + delta.totalTokens)
}

func applyUsageProviderDelta(buckets map[string]usageFileProviderStat, bucket usageBucketKey, delta usageFileDelta) map[string]usageFileProviderStat {
	if buckets == nil {
		buckets = make(map[string]usageFileProviderStat, 1)
	}
	stat, exists := buckets[bucket.ProviderID]
	if !exists {
		stat = usageFileProviderStat{ProviderID: bucket.ProviderID}
	}
	// 站点可能被改名，以最近一次调用携带的名称为准。
	if strings.TrimSpace(bucket.ProviderName) != "" {
		stat.ProviderName = bucket.ProviderName
	}
	applyUsageCounters(&stat.usageFileCounters, delta)
	buckets[bucket.ProviderID] = stat
	return buckets
}

// applyUsageModelDelta 与 provider 分桶同构，差异只在身份字段，
// 因此复用同一套 usageFileCounters 累加语义。
func applyUsageModelDelta(buckets map[string]usageFileModelStat, bucket usageBucketKey, delta usageFileDelta) map[string]usageFileModelStat {
	if buckets == nil {
		buckets = make(map[string]usageFileModelStat, 1)
	}
	stat, exists := buckets[bucket.Model]
	if !exists {
		stat = usageFileModelStat{Model: bucket.Model}
	}
	// 同名模型可能先后挂在不同中转站下，记录最近一次的归属即可满足下钻展示。
	stat.ProviderID = bucket.ProviderID
	if strings.TrimSpace(bucket.ProviderName) != "" {
		stat.ProviderName = bucket.ProviderName
	}
	applyUsageCounters(&stat.usageFileCounters, delta)
	buckets[bucket.Model] = stat
	return buckets
}

// normalizeUsageBucketKey 把空归属折叠成固定的 unknown 桶，
// 让改造前的历史数据与未绑定中转站的模型有统一去处。
func normalizeUsageBucketKey(providerID string, providerName string, model string) usageBucketKey {
	id := strings.TrimSpace(providerID)
	name := strings.TrimSpace(providerName)
	if id == "" {
		id = usageProviderUnknownID
		name = usageProviderUnknownName
	} else if name == "" {
		name = id
	}
	modelName := strings.TrimSpace(model)
	if modelName == "" {
		modelName = usageModelUnknown
	}
	return usageBucketKey{ProviderID: id, ProviderName: name, Model: modelName}
}

func clampNonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
