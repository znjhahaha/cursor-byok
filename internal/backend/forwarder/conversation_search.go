// conversation_search.go 提供跨会话全文搜索、只读 transcript 投影与 Markdown 导出渲染。
// 搜索采用线性扫描：本地会话量级下一次全量扫描即可秒级完成，避免维护索引的一致性成本。
package forwarder

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
)

const (
	// conversationSearchMaxHits 限制返回的会话数量，避免一次搜索把全部历史抛给前端。
	conversationSearchMaxHits = 50
	// conversationSearchMaxMatchesPerConversation 限制每个会话展示的命中片段数。
	conversationSearchMaxMatchesPerConversation = 3
	// conversationSearchSnippetRadius 是命中词前后保留的字节数（UTF-8 安全边界内取整）。
	conversationSearchSnippetRadius = 120
	// conversationSearchWorkerCount 是并发扫描会话文件的 worker 数；IO 密集，固定小并发即可。
	conversationSearchWorkerCount = 8
	// conversationMarkdownToolResultLimit 是导出 Markdown 时单个工具结果保留的最大字符数。
	conversationMarkdownToolResultLimit = 2000
)

// ConversationSearchOptions 是一次跨会话搜索的全部条件。
type ConversationSearchOptions struct {
	Query        string `json:"query"`
	IncludeTools bool   `json:"includeTools"`
	// Mode 非空时只保留该模式（agent/plan/...）的会话。
	Mode string `json:"mode"`
	// UpdatedWithinDays 大于 0 时只保留最近 N*24h 内更新过的会话。
	UpdatedWithinDays int `json:"updatedWithinDays"`
}

// ConversationSearchResult 是搜索结果与截断标记。
type ConversationSearchResult struct {
	Hits []ConversationSearchHit `json:"hits"`
	// Truncated 表示命中会话数超过返回上限，结果被截断。
	Truncated bool `json:"truncated"`
}

// ConversationSearchMatch 是单条命中片段。
type ConversationSearchMatch struct {
	Seq       int64     `json:"seq"`
	TurnSeq   int64     `json:"turnSeq"`
	Kind      string    `json:"kind"`
	ToolName  string    `json:"toolName,omitempty"`
	Snippet   string    `json:"snippet"`
	CreatedAt time.Time `json:"createdAt"`
}

// ConversationSearchHit 是单个会话的搜索结果。
type ConversationSearchHit struct {
	ConversationID string                    `json:"conversationId"`
	Name           string                    `json:"name"`
	Mode           string                    `json:"mode"`
	UpdatedAt      time.Time                 `json:"updatedAt"`
	EntryCount     int                       `json:"entryCount"`
	TitleMatched   bool                      `json:"titleMatched"`
	TotalMatches   int                       `json:"totalMatches"`
	Matches        []ConversationSearchMatch `json:"matches"`
}

// ConversationTranscriptMessage 是只读会话视图中的一条消息。
type ConversationTranscriptMessage struct {
	Seq        int64     `json:"seq"`
	TurnSeq    int64     `json:"turnSeq"`
	Kind       string    `json:"kind"`
	ToolName   string    `json:"toolName,omitempty"`
	Text       string    `json:"text,omitempty"`
	Arguments  string    `json:"arguments,omitempty"`
	ResultText string    `json:"resultText,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// ConversationTranscript 是供前端只读渲染与 Markdown 导出使用的会话投影。
type ConversationTranscript struct {
	ConversationID string                          `json:"conversationId"`
	Name           string                          `json:"name"`
	Mode           string                          `json:"mode"`
	CreatedAt      time.Time                       `json:"createdAt"`
	UpdatedAt      time.Time                       `json:"updatedAt"`
	Messages       []ConversationTranscriptMessage `json:"messages"`
}

// searchableText 是从 HistoryEntry 提取出的一段可检索文本。
type searchableText struct {
	kind     string
	toolName string
	text     string
}

// SearchConversations 在 historyRoot 下的全部主会话中检索。
// query 为空时返回按更新时间倒序的最近会话列表，充当会话浏览入口。
// 多个关键词（空格分隔）在会话级取 AND：标题与全部消息合计覆盖所有关键词才算命中。
// 子代理会话（存在父会话）不参与检索：其有效产出已回流到主会话的工具结果里。
func SearchConversations(historyRoot string, options ConversationSearchOptions) (ConversationSearchResult, error) {
	store := NewConversationFileStore(historyRoot)
	conversationIDs, err := store.ListConversationIDs()
	if err != nil {
		return ConversationSearchResult{}, err
	}
	keywords := parseSearchKeywords(options.Query)
	modeFilter := strings.ToLower(strings.TrimSpace(options.Mode))
	var updatedAfter time.Time
	if options.UpdatedWithinDays > 0 {
		updatedAfter = time.Now().Add(-time.Duration(options.UpdatedWithinDays) * 24 * time.Hour)
	}

	var (
		mu   sync.Mutex
		hits []ConversationSearchHit
	)
	jobs := make(chan string)
	var waitGroup sync.WaitGroup
	for workerIndex := 0; workerIndex < conversationSearchWorkerCount; workerIndex++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for conversationID := range jobs {
				hit, ok := searchSingleConversation(store, conversationID, keywords, options.IncludeTools, modeFilter, updatedAfter)
				if !ok {
					continue
				}
				mu.Lock()
				hits = append(hits, hit)
				mu.Unlock()
			}
		}()
	}
	for _, conversationID := range conversationIDs {
		jobs <- conversationID
	}
	close(jobs)
	waitGroup.Wait()

	sort.Slice(hits, func(left, right int) bool {
		if !hits[left].UpdatedAt.Equal(hits[right].UpdatedAt) {
			return hits[left].UpdatedAt.After(hits[right].UpdatedAt)
		}
		return hits[left].ConversationID < hits[right].ConversationID
	})
	result := ConversationSearchResult{Hits: hits}
	if len(hits) > conversationSearchMaxHits {
		result.Hits = hits[:conversationSearchMaxHits]
		result.Truncated = true
	}
	return result, nil
}

// DeleteConversation 在会话锁内永久删除整个会话目录（state/context/debug 全部内容）。
// 目录不存在时视为已删除成功。若会话仍在写入，锁保证删除不会与写交错；
// 之后的写入会按新会话语义重建目录。
func DeleteConversation(historyRoot string, conversationID string) error {
	store := NewConversationFileStore(historyRoot)
	normalizedID, err := validateConversationID(conversationID)
	if err != nil {
		return err
	}
	conversationDir := store.conversationDir(normalizedID)
	if exists, err := fileExists(store.statePath(normalizedID)); err != nil {
		return err
	} else if !exists {
		// 没有 state.json 说明不是（或已不是）一个有效会话目录，直接尝试清理残留。
		return os.RemoveAll(conversationDir)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedID))
	if err != nil {
		return err
	}
	// RemoveAll 会连锁文件一起删除；release 里对已消失的锁文件删除是无害的空操作。
	defer release()
	return os.RemoveAll(conversationDir)
}

func parseSearchKeywords(query string) []string {
	fields := strings.Fields(query)
	keywords := make([]string, 0, len(fields))
	for _, field := range fields {
		if normalized := strings.ToLower(strings.TrimSpace(field)); normalized != "" {
			keywords = append(keywords, normalized)
		}
	}
	return keywords
}

// readConversationMetaForSearch 无锁轻读 state.json。
// 写入方总是用原子 rename 替换文件，直接读不会看到半截内容；
// 搜索是只读旁路，绕开会话锁避免与活跃会话的写入互相阻塞，
// 也省掉 LoadConversation 每次必读 context.json 的全量成本。
func readConversationMetaForSearch(store *ConversationFileStore, conversationID string) (*ConversationFile, bool) {
	body, err := os.ReadFile(store.statePath(conversationID))
	if err != nil {
		return nil, false
	}
	var conversation ConversationFile
	if err := json.Unmarshal(body, &conversation); err != nil {
		return nil, false
	}
	if strings.TrimSpace(conversation.ConversationID) == "" {
		conversation.ConversationID = conversationID
	}
	return &conversation, true
}

// readConversationEntriesForSearch 无锁读取 context.json 的全部条目。
// 兼容旧版单 JSON 对象与新版 JSONL 两种格式。
func readConversationEntriesForSearch(store *ConversationFileStore, conversationID string) ([]HistoryEntry, bool) {
	body, err := os.ReadFile(store.contextPath(conversationID))
	if err != nil {
		return nil, false
	}
	entries, err := parseConversationContextBody(body)
	if err != nil {
		return nil, false
	}
	return entries, true
}

func searchSingleConversation(store *ConversationFileStore, conversationID string, keywords []string, includeTools bool, modeFilter string, updatedAfter time.Time) (ConversationSearchHit, bool) {
	// 先轻读 state.json 做预过滤：子代理/模式/时间不满足时完全不碰大的 context.json。
	// 单个会话读取失败（损坏、正被替换）不应让整次搜索失败，直接跳过。
	meta, ok := readConversationMetaForSearch(store, conversationID)
	if !ok {
		return ConversationSearchHit{}, false
	}
	if strings.TrimSpace(meta.ParentConversationID) != "" || strings.TrimSpace(meta.SubagentTypeName) != "" {
		return ConversationSearchHit{}, false
	}
	if modeFilter != "" && strings.ToLower(strings.TrimSpace(meta.Mode)) != modeFilter {
		return ConversationSearchHit{}, false
	}
	if !updatedAfter.IsZero() && meta.UpdatedAt.Before(updatedAfter) {
		return ConversationSearchHit{}, false
	}

	hit := ConversationSearchHit{
		ConversationID: meta.ConversationID,
		Name:           strings.TrimSpace(meta.Name),
		Mode:           meta.Mode,
		UpdatedAt:      meta.UpdatedAt,
	}
	// state.json 里的 NextEntrySeq 是"下一个序号"，减一即当前最大序号，
	// 浏览模式用它近似条目数，避免为一个展示数字读整个 context.json。
	if meta.NextEntrySeq > 1 {
		hit.EntryCount = int(meta.NextEntrySeq - 1)
	}
	if len(keywords) == 0 {
		return hit, true
	}

	matchedKeywords := make(map[string]struct{}, len(keywords))
	lowerTitle := strings.ToLower(hit.Name)
	for _, keyword := range keywords {
		if lowerTitle != "" && strings.Contains(lowerTitle, keyword) {
			matchedKeywords[keyword] = struct{}{}
			hit.TitleMatched = true
		}
	}

	entries, ok := readConversationEntriesForSearch(store, conversationID)
	if !ok {
		return ConversationSearchHit{}, false
	}
	hit.EntryCount = len(entries)

	for _, entry := range entries {
		for _, candidate := range extractSearchableTexts(entry, includeTools) {
			lowerText := strings.ToLower(candidate.text)
			entryMatched := false
			for _, keyword := range keywords {
				if strings.Contains(lowerText, keyword) {
					matchedKeywords[keyword] = struct{}{}
					entryMatched = true
				}
			}
			if !entryMatched {
				continue
			}
			hit.TotalMatches++
			if len(hit.Matches) < conversationSearchMaxMatchesPerConversation {
				hit.Matches = append(hit.Matches, ConversationSearchMatch{
					Seq:       entry.Seq,
					TurnSeq:   entry.TurnSeq,
					Kind:      candidate.kind,
					ToolName:  candidate.toolName,
					Snippet:   buildSearchSnippet(candidate.text, lowerText, keywords),
					CreatedAt: entry.CreatedAt,
				})
			}
		}
	}

	if len(matchedKeywords) < len(keywords) {
		return ConversationSearchHit{}, false
	}
	return hit, true
}

// extractSearchableTexts 把一条 HistoryEntry 展开成可检索文本。
// 只取用户与助手的可见文本；includeTools 时追加工具结果（参数 + 输出）与压缩摘要一并纳入。
func extractSearchableTexts(entry HistoryEntry, includeTools bool) []searchableText {
	switch strings.TrimSpace(entry.Kind) {
	case "user_message":
		userMessage := &agentv1.UserMessage{}
		if err := protojson.Unmarshal(entry.Payload, userMessage); err != nil {
			return nil
		}
		text := strings.TrimSpace(userMessage.GetText())
		if text == "" {
			return nil
		}
		return []searchableText{{kind: "user", text: text}}
	case "assistant_text":
		var payload assistantTextPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil
		}
		text := strings.TrimSpace(payload.Text)
		if text == "" {
			return nil
		}
		return []searchableText{{kind: "assistant", text: text}}
	case "compaction_summary", "compacted_summary":
		summary, ok := decodeCompactionSummaryEntry(entry)
		if !ok {
			return nil
		}
		return []searchableText{{kind: "summary", text: summary}}
	case "tool_result":
		if !includeTools {
			return nil
		}
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil
		}
		combined := strings.TrimSpace(strings.TrimSpace(payload.Arguments) + "\n" + strings.TrimSpace(payload.ResultText))
		if combined == "" {
			return nil
		}
		return []searchableText{{kind: "tool", toolName: strings.TrimSpace(payload.ToolName), text: combined}}
	default:
		return nil
	}
}

// buildSearchSnippet 以第一个命中的关键词为中心截取片段。
// 偏移在小写副本上定位后套用到原文；对绝大多数文本（ASCII/CJK）ToLower 不改变字节布局，
// 极少数特殊字符导致的轻微错位由 rune 边界修正兜底，只影响片段起止位置，不会截断出非法 UTF-8。
func buildSearchSnippet(text string, lowerText string, keywords []string) string {
	anchor := -1
	for _, keyword := range keywords {
		if index := strings.Index(lowerText, keyword); index >= 0 && (anchor < 0 || index < anchor) {
			anchor = index
		}
	}
	if anchor < 0 {
		anchor = 0
	}
	if anchor > len(text) {
		anchor = len(text)
	}
	start := anchor - conversationSearchSnippetRadius
	if start < 0 {
		start = 0
	}
	end := anchor + conversationSearchSnippetRadius
	if end > len(text) {
		end = len(text)
	}
	for start > 0 && start < len(text) && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	snippet := strings.TrimSpace(collapseSnippetWhitespace(text[start:end]))
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(text) {
		snippet += "…"
	}
	return snippet
}

// collapseSnippetWhitespace 把片段内的连续空白压成单个空格，保证结果列表单行展示。
func collapseSnippetWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// GetConversationTranscript 把会话投影成只读消息流。
func GetConversationTranscript(historyRoot string, conversationID string) (ConversationTranscript, error) {
	store := NewConversationFileStore(historyRoot)
	conversation, err := store.LoadConversation(conversationID)
	if err != nil {
		return ConversationTranscript{}, err
	}
	if conversation == nil {
		return ConversationTranscript{}, fmt.Errorf("conversation %q not found", strings.TrimSpace(conversationID))
	}
	transcript := ConversationTranscript{
		ConversationID: conversation.ConversationID,
		Name:           strings.TrimSpace(conversation.Name),
		Mode:           conversation.Mode,
		CreatedAt:      conversation.CreatedAt,
		UpdatedAt:      conversation.UpdatedAt,
		Messages:       make([]ConversationTranscriptMessage, 0, len(conversation.Entries)),
	}
	for _, entry := range conversation.Entries {
		message, ok := buildTranscriptMessage(entry)
		if ok {
			transcript.Messages = append(transcript.Messages, message)
		}
	}
	return transcript, nil
}

// buildTranscriptMessage 只保留用户可读的消息形态：用户输入、助手文本、
// 工具结果（tool_call 与 tool_result 一一对应，参数已包含在结果条目中）与压缩摘要。
func buildTranscriptMessage(entry HistoryEntry) (ConversationTranscriptMessage, bool) {
	base := ConversationTranscriptMessage{
		Seq:       entry.Seq,
		TurnSeq:   entry.TurnSeq,
		CreatedAt: entry.CreatedAt,
	}
	switch strings.TrimSpace(entry.Kind) {
	case "user_message":
		userMessage := &agentv1.UserMessage{}
		if err := protojson.Unmarshal(entry.Payload, userMessage); err != nil {
			return ConversationTranscriptMessage{}, false
		}
		text := strings.TrimSpace(userMessage.GetText())
		if text == "" {
			return ConversationTranscriptMessage{}, false
		}
		base.Kind = "user"
		base.Text = text
		return base, true
	case "assistant_text":
		var payload assistantTextPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return ConversationTranscriptMessage{}, false
		}
		text := strings.TrimSpace(payload.Text)
		if text == "" {
			return ConversationTranscriptMessage{}, false
		}
		base.Kind = "assistant"
		base.Text = text
		return base, true
	case "compaction_summary", "compacted_summary":
		summary, ok := decodeCompactionSummaryEntry(entry)
		if !ok {
			return ConversationTranscriptMessage{}, false
		}
		base.Kind = "summary"
		base.Text = summary
		return base, true
	case "tool_result":
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return ConversationTranscriptMessage{}, false
		}
		toolName := strings.TrimSpace(payload.ToolName)
		resultText := strings.TrimSpace(payload.ResultText)
		if toolName == "" && resultText == "" {
			return ConversationTranscriptMessage{}, false
		}
		base.Kind = "tool"
		base.ToolName = toolName
		base.Arguments = strings.TrimSpace(payload.Arguments)
		base.ResultText = resultText
		return base, true
	default:
		return ConversationTranscriptMessage{}, false
	}
}

// RenderConversationMarkdown 把 transcript 渲染成 Markdown 文档。
func RenderConversationMarkdown(transcript ConversationTranscript) string {
	var builder strings.Builder
	title := transcript.Name
	if title == "" {
		title = transcript.ConversationID
	}
	builder.WriteString("# " + title + "\n\n")
	builder.WriteString("- 会话 ID: `" + transcript.ConversationID + "`\n")
	builder.WriteString("- 模式: " + transcript.Mode + "\n")
	builder.WriteString("- 创建时间: " + formatMarkdownTime(transcript.CreatedAt) + "\n")
	builder.WriteString("- 最后更新: " + formatMarkdownTime(transcript.UpdatedAt) + "\n")
	builder.WriteString(fmt.Sprintf("- 消息数: %d\n", len(transcript.Messages)))

	lastTurnSeq := int64(-1)
	for _, message := range transcript.Messages {
		if message.TurnSeq != lastTurnSeq {
			builder.WriteString(fmt.Sprintf("\n---\n\n## 第 %d 轮\n", message.TurnSeq))
			lastTurnSeq = message.TurnSeq
		}
		switch message.Kind {
		case "user":
			builder.WriteString("\n### 用户 · " + formatMarkdownTime(message.CreatedAt) + "\n\n")
			builder.WriteString(message.Text + "\n")
		case "assistant":
			builder.WriteString("\n### 助手\n\n")
			builder.WriteString(message.Text + "\n")
		case "summary":
			builder.WriteString("\n### 历史压缩摘要\n\n")
			builder.WriteString(message.Text + "\n")
		case "tool":
			builder.WriteString("\n### 工具 " + message.ToolName + "\n")
			if message.Arguments != "" {
				builder.WriteString("\n参数:\n\n")
				writeMarkdownFence(&builder, message.Arguments)
			}
			if message.ResultText != "" {
				result := message.ResultText
				if utf8.RuneCountInString(result) > conversationMarkdownToolResultLimit {
					runes := []rune(result)
					result = string(runes[:conversationMarkdownToolResultLimit]) + "\n…(结果已截断)"
				}
				builder.WriteString("\n结果:\n\n")
				writeMarkdownFence(&builder, result)
			}
		}
	}
	return builder.String()
}

// writeMarkdownFence 用围栏包裹内容；内容里可能出现反引号围栏，因此动态加长围栏长度。
func writeMarkdownFence(builder *strings.Builder, content string) {
	fence := "```"
	for strings.Contains(content, fence) {
		fence += "`"
	}
	builder.WriteString(fence + "\n" + content + "\n" + fence + "\n")
}

func formatMarkdownTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.In(time.Local).Format("2006-01-02 15:04")
}
