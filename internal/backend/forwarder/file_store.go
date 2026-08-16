// file_store.go 负责 conversation 的两份持久化事实：state.json 与 context.json。
package forwarder

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cursor/gen/agentv1"
)

const (
	conversationStateFileName          = "state.json"
	conversationContextFileName        = "context.json"
	conversationSchemaVersion          = 1
	conversationLockStaleAfter         = 30 * time.Minute
	legacyConversationLockStaleAfter   = 30 * time.Second
	conversationLockAcquireTimeout     = 30 * time.Second
	staleConversationLockRemoveTimeout = 30 * time.Second
	conversationLockRetryInterval      = 10 * time.Millisecond
)

var (
	conversationLockProcessStartedAt = time.Now()
	conversationProcessLocksMu       sync.Mutex
	conversationProcessLocks         = make(map[string]*conversationProcessLock)
)

type conversationProcessLock struct {
	mu   sync.Mutex
	refs int
}

type ConversationFileStore struct {
	root string

	// contextMu 保护 contextCache：磁盘状态与缓存的一致性由 lookupValidContextCache
	// 在会话文件锁内用 size+modtime 校验，任何外部写入都会让缓存自然失效。
	contextMu    sync.Mutex
	contextCache map[string]*contextDiskCache
}

// contextDiskCache 记录上次由本进程写入后的 context.json 磁盘状态。
// 命中时读路径跳过全量 JSON 解码，写路径只把新 entry 以 JSONL 行追加到尾部。
type contextDiskCache struct {
	conversation *ConversationFile
	size         int64
	modTime      time.Time
}

type conversationContextFile struct {
	SchemaVersion  int            `json:"schema_version"`
	ConversationID string         `json:"conversation_id"`
	Version        int64          `json:"version"`
	UpdatedAt      time.Time      `json:"updated_at"`
	Items          []HistoryEntry `json:"items"`
}

// conversationContextFormatJSONL 是新版 context.json 格式：首行 header，
// 其后每行一条 HistoryEntry。旧格式是单个 JSON 对象，读取时两者都支持。
const conversationContextFormatJSONL = "jsonl"

type conversationContextHeader struct {
	SchemaVersion  int       `json:"schema_version"`
	ConversationID string    `json:"conversation_id"`
	Format         string    `json:"format"`
	Version        int64     `json:"version"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// NewConversationFileStore 创建 JSON history 文件存储。
func NewConversationFileStore(historyRoot string) *ConversationFileStore {
	return &ConversationFileStore{
		root:         strings.TrimSpace(historyRoot),
		contextCache: make(map[string]*contextDiskCache),
	}
}

// HistoryDir 返回 history 根路径。
func (store *ConversationFileStore) HistoryDir() string {
	if store == nil {
		return ""
	}
	return store.root
}

// CreateConversation 确保指定会话对应的 state/context 文件存在并完成元数据初始化。
func (store *ConversationFileStore) CreateConversation(conversationID string, mode agentv1.AgentMode, parentConversationID string, parentToolCallID string, rootConversationID string) (*ConversationFile, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation file store is nil")
	}
	return store.mutateConversation(conversationID, true, func(conversation *ConversationFile) error {
		if strings.TrimSpace(conversation.ConversationID) != "" {
			if strings.TrimSpace(conversation.Mode) == "" {
				alias, err := modeAlias(mode)
				if err != nil {
					return err
				}
				conversation.Mode = alias
			}
			return nil
		}
		now := time.Now().UTC()
		normalizedConversationID := strings.TrimSpace(conversationID)
		if normalizedConversationID == "" {
			return fmt.Errorf("conversation_id is required")
		}
		conversation.SchemaVersion = conversationSchemaVersion
		conversation.ConversationID = normalizedConversationID
		conversation.RootConversationID = strings.TrimSpace(rootConversationID)
		if conversation.RootConversationID == "" {
			conversation.RootConversationID = normalizedConversationID
		}
		conversation.ParentConversationID = strings.TrimSpace(parentConversationID)
		conversation.ParentToolCallID = strings.TrimSpace(parentToolCallID)
		alias, err := modeAlias(mode)
		if err != nil {
			return err
		}
		conversation.Mode = alias
		conversation.CreatedAt = now
		conversation.UpdatedAt = now
		conversation.NextTurnSeq = 1
		conversation.NextEntrySeq = 1
		conversation.ContextVersion = 0
		conversation.CurrentLoopStatus = "idle"
		conversation.Entries = make([]HistoryEntry, 0, 16)
		return nil
	})
}

// LoadConversation 读取 state.json + context.json。
func (store *ConversationFileStore) LoadConversation(conversationID string) (*ConversationFile, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation file store is nil")
	}
	return store.mutateConversation(conversationID, false, nil)
}

// AppendEntries 把已经发生的语义事件追加到 context.json，并同步 state.json。
// 缓存命中（磁盘仍是本进程上次写入的状态）时走增量路径：只把新 entry 以
// JSONL 行追加到文件尾部并 fsync，跳过全量重读与全量重写，这是长对话
// 工具往返延迟随历史线性增长（整体 O(n^2)）的根治点。
func (store *ConversationFileStore) AppendEntries(conversationID string, entries []HistoryEntry) (*ConversationFile, []HistoryEntry, error) {
	if store == nil {
		return nil, nil, fmt.Errorf("conversation file store is nil")
	}
	if len(entries) == 0 {
		conversation, err := store.LoadConversation(conversationID)
		return conversation, nil, err
	}
	normalizedConversationID, err := validateConversationID(conversationID)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(store.conversationDir(normalizedConversationID), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create conversation directory: %w", err)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedConversationID))
	if err != nil {
		return nil, nil, err
	}
	defer release()

	cached := store.lookupValidContextCache(normalizedConversationID)
	var conversation *ConversationFile
	if cached != nil {
		conversation = store.conversationFromCache(normalizedConversationID, cached)
	} else {
		conversation, err = store.readConversationLocked(normalizedConversationID)
		if err != nil {
			return nil, nil, err
		}
	}
	if conversation == nil {
		conversation = &ConversationFile{
			SchemaVersion:      conversationSchemaVersion,
			ConversationID:     normalizedConversationID,
			RootConversationID: normalizedConversationID,
			Mode:               defaultConversationModeAlias,
			NextTurnSeq:        1,
			NextEntrySeq:       1,
			Entries:            make([]HistoryEntry, 0, 16),
			CreatedAt:          time.Now().UTC(),
		}
	}
	assigned := appendEntriesInPlace(conversation, entries)
	deriveConversationLoopState(conversation)
	structuredDirty := entriesTouchStructuredState(assigned)
	appended := false
	if cached != nil {
		if appendErr := store.appendContextEntries(normalizedConversationID, assigned); appendErr == nil {
			appended = true
			if err := store.writeConversationMetaAfterAppendLocked(normalizedConversationID, conversation, structuredDirty); err != nil {
				return nil, nil, err
			}
			store.rememberContextWrite(normalizedConversationID, conversation)
		}
		// 增量追加失败（磁盘状态漂移、编码错误）时回退全量重写，磁盘语义保持完整。
	}
	if !appended {
		if err := store.writeConversationLocked(normalizedConversationID, conversation, structuredDirty); err != nil {
			return nil, nil, err
		}
	}
	// conversation 是本函数从磁盘重建出来的局部对象，store 不再持有引用，
	// 直接把所有权移交调用方即可；再深拷贝一遍整条历史是纯浪费。
	return conversation, assigned, nil
}

// entriesTouchStructuredState 判断这批 entry 是否可能改变 plan/todo：
// 只有 tool_result（CreatePlan/UpdateTodos）与 runtime_state 参与 structured state
// 投影，普通追加（assistant_text/tool_call/metadata 等）无需触发全量 proto 解码。
func entriesTouchStructuredState(entries []HistoryEntry) bool {
	for _, entry := range entries {
		switch strings.TrimSpace(entry.Kind) {
		case "tool_result", "runtime_state":
			return true
		}
	}
	return false
}

func (store *ConversationFileStore) SaveConversationWithEntries(conversationID string, source *ConversationFile, entries []HistoryEntry) (*ConversationFile, error) {
	conversation, _, _, err := store.saveConversationWithEntries(conversationID, source, entries, nil)
	return conversation, err
}

// SaveConversationWithEntriesIfAnyIdempotencyKeyIsNew 原子追加一批 history；
// 只有指定幂等键中至少一个尚未存在时才写入。这个判断必须在同一文件锁内完成，
// 否则重复 completion action 仍可能各自通过检查并启动两次 continuation。
func (store *ConversationFileStore) SaveConversationWithEntriesIfAnyIdempotencyKeyIsNew(conversationID string, source *ConversationFile, entries []HistoryEntry, idempotencyKeys []string) (*ConversationFile, []HistoryEntry, bool, error) {
	return store.saveConversationWithEntries(conversationID, source, entries, idempotencyKeys)
}

func (store *ConversationFileStore) saveConversationWithEntries(conversationID string, source *ConversationFile, entries []HistoryEntry, requiredNewKeys []string) (*ConversationFile, []HistoryEntry, bool, error) {
	if store == nil {
		return nil, nil, false, fmt.Errorf("conversation file store is nil")
	}
	normalizedConversationID, err := validateConversationID(conversationID)
	if err != nil {
		return nil, nil, false, err
	}
	if err := os.MkdirAll(store.conversationDir(normalizedConversationID), 0o755); err != nil {
		return nil, nil, false, fmt.Errorf("create conversation directory: %w", err)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedConversationID))
	if err != nil {
		return nil, nil, false, err
	}
	defer release()

	conversation, err := store.readConversationLocked(normalizedConversationID)
	if err != nil {
		return nil, nil, false, err
	}
	if conversation == nil {
		conversation = &ConversationFile{
			SchemaVersion:      conversationSchemaVersion,
			ConversationID:     normalizedConversationID,
			RootConversationID: normalizedConversationID,
			Mode:               defaultConversationModeAlias,
			NextTurnSeq:        1,
			NextEntrySeq:       1,
			Entries:            make([]HistoryEntry, 0, len(entries)),
			CreatedAt:          time.Now().UTC(),
		}
	}
	if len(requiredNewKeys) > 0 && !conversationHasAnyMissingIdempotencyKey(conversation, requiredNewKeys) {
		return cloneConversationFile(conversation), nil, false, nil
	}
	mergeConversationMetadata(conversation, source)
	assigned := appendEntriesInPlace(conversation, resetEntrySequences(entries))
	deriveConversationLoopState(conversation)
	if err := store.writeConversationLocked(normalizedConversationID, conversation, true); err != nil {
		return nil, nil, false, err
	}
	return cloneConversationFile(conversation), assigned, true, nil
}

func conversationHasAnyMissingIdempotencyKey(conversation *ConversationFile, keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	existingCapacity := 0
	if conversation != nil {
		existingCapacity = len(conversation.Entries)
	}
	existing := make(map[string]struct{}, existingCapacity)
	if conversation != nil {
		for _, entry := range conversation.Entries {
			if key := strings.TrimSpace(entry.IdempotencyKey); key != "" {
				existing[key] = struct{}{}
			}
		}
	}
	for _, key := range keys {
		if normalized := strings.TrimSpace(key); normalized != "" {
			if _, ok := existing[normalized]; !ok {
				return true
			}
		}
	}
	return false
}

// UpdateConversationMeta 更新 state.json；context.json 保持不变。
func (store *ConversationFileStore) UpdateConversationMeta(conversationID string, update func(*ConversationFile) error) (*ConversationFile, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation file store is nil")
	}
	normalizedConversationID, err := validateConversationID(conversationID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.conversationDir(normalizedConversationID), 0o755); err != nil {
		return nil, fmt.Errorf("create conversation directory: %w", err)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedConversationID))
	if err != nil {
		return nil, err
	}
	defer release()

	conversation, err := store.readConversationLocked(normalizedConversationID)
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		conversation = &ConversationFile{
			SchemaVersion:      conversationSchemaVersion,
			ConversationID:     normalizedConversationID,
			RootConversationID: normalizedConversationID,
			NextTurnSeq:        1,
			NextEntrySeq:       1,
			Entries:            make([]HistoryEntry, 0, 16),
			CreatedAt:          time.Now().UTC(),
		}
	}
	if update != nil {
		if err := update(conversation); err != nil {
			return nil, err
		}
	}
	if err := store.writeConversationMetaLocked(normalizedConversationID, conversation, true); err != nil {
		return nil, err
	}
	return cloneConversationFile(conversation), nil
}

// WriteConversationMetaFrom 用调用方已持有的会话快照直接写 state.json。
//
// UpdateConversationMeta 必须先从磁盘读回整个 context.json 才能重算派生字段，
// 这在长历史下是每次调用都要付的 O(历史长度) 成本。当调用方本身就握着
// 权威快照时，这次读取纯属重复劳动，直接复用即可。
// 语义与 UpdateConversationMeta 一致：只写 state.json，不触碰 context.json。
// source 被视为只读，派生过程不会修改调用方持有的快照。会话标题由
// UpdateConversationMetadata 独立更新，因此始终以锁内读到的磁盘值为准，
// 避免运行中的旧 checkpoint 把新标题覆盖掉。
func (store *ConversationFileStore) WriteConversationMetaFrom(conversationID string, source *ConversationFile) error {
	if store == nil {
		return fmt.Errorf("conversation file store is nil")
	}
	if source == nil {
		return fmt.Errorf("conversation snapshot is required")
	}
	normalizedConversationID, err := validateConversationID(conversationID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(store.conversationDir(normalizedConversationID), 0o755); err != nil {
		return fmt.Errorf("create conversation directory: %w", err)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedConversationID))
	if err != nil {
		return err
	}
	defer release()

	conversation := borrowConversationForMeta(source)
	var persisted struct {
		Name string `json:"name"`
	}
	stateBody, err := os.ReadFile(store.statePath(normalizedConversationID))
	switch {
	case err == nil:
		// state.json 损坏时沿用 source；writeConversationMetaLocked 会用完整快照修复它。
		if json.Unmarshal(stateBody, &persisted) == nil {
			conversation.Name = persisted.Name
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("read conversation state: %w", err)
	}

	// 纯 meta 写入：context.json 不变，磁盘 entries 与快照一致，
	// 结构化状态在上次追加（structuredDirty）时已刷新，无需全量 proto 重算。
	return store.writeConversationMetaLocked(normalizedConversationID, conversation, false)
}

// ReplaceEntries 原子替换 context.json，并同步 state.json 中的 sequence/version 状态。
func (store *ConversationFileStore) ReplaceEntries(conversationID string, entries []HistoryEntry, update func(*ConversationFile) error) (*ConversationFile, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation file store is nil")
	}
	normalizedConversationID, err := validateConversationID(conversationID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.conversationDir(normalizedConversationID), 0o755); err != nil {
		return nil, fmt.Errorf("create conversation directory: %w", err)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedConversationID))
	if err != nil {
		return nil, err
	}
	defer release()

	conversation, err := store.readConversationLocked(normalizedConversationID)
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		conversation = &ConversationFile{
			SchemaVersion:      conversationSchemaVersion,
			ConversationID:     normalizedConversationID,
			RootConversationID: normalizedConversationID,
			Mode:               defaultConversationModeAlias,
			NextTurnSeq:        1,
			NextEntrySeq:       1,
			Entries:            make([]HistoryEntry, 0, len(entries)),
			CreatedAt:          time.Now().UTC(),
		}
	}
	conversation.Entries = nil
	conversation.NextEntrySeq = 1
	conversation.NextTurnSeq = 1
	appendEntriesInPlace(conversation, resetEntrySequences(entries))
	if update != nil {
		if err := update(conversation); err != nil {
			return nil, err
		}
	}
	deriveConversationLoopState(conversation)
	if err := store.writeConversationLocked(normalizedConversationID, conversation, true); err != nil {
		return nil, err
	}
	return cloneConversationFile(conversation), nil
}

// GetConversationSummary 返回轻量会话摘要。
func (store *ConversationFileStore) GetConversationSummary(conversationID string) (ConversationSummary, error) {
	conversation, err := store.LoadConversation(conversationID)
	if err != nil || conversation == nil {
		return ConversationSummary{}, err
	}
	return ConversationSummary{
		ConversationID: conversation.ConversationID,
		Mode:           conversation.Mode,
		EntriesCount:   len(conversation.Entries),
		NextTurnSeq:    conversation.NextTurnSeq,
		NextEntrySeq:   conversation.NextEntrySeq,
		UpdatedAt:      conversation.UpdatedAt,
	}, nil
}

// ListConversationIDs 返回 history 根目录下包含 state/context 的 conversation id。
func (store *ConversationFileStore) ListConversationIDs() ([]string, error) {
	if store == nil {
		return nil, fmt.Errorf("conversation file store is nil")
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan history directory: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		conversationID := strings.TrimSpace(entry.Name())
		if conversationID == "" {
			continue
		}
		if ok, err := fileExists(store.statePath(conversationID)); err != nil {
			return nil, err
		} else if ok {
			ids = append(ids, conversationID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (store *ConversationFileStore) mutateConversation(conversationID string, createIfMissing bool, update func(*ConversationFile) error) (*ConversationFile, error) {
	normalizedConversationID, err := validateConversationID(conversationID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.conversationDir(normalizedConversationID), 0o755); err != nil {
		return nil, fmt.Errorf("create conversation directory: %w", err)
	}
	release, err := acquireConversationLock(store.lockPath(normalizedConversationID))
	if err != nil {
		return nil, err
	}
	defer release()

	conversation, err := store.readConversationLocked(normalizedConversationID)
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		if !createIfMissing {
			return nil, nil
		}
		conversation = &ConversationFile{
			SchemaVersion:      conversationSchemaVersion,
			ConversationID:     normalizedConversationID,
			RootConversationID: normalizedConversationID,
			NextTurnSeq:        1,
			NextEntrySeq:       1,
			Entries:            make([]HistoryEntry, 0, 16),
			CreatedAt:          time.Now().UTC(),
		}
	}
	if update == nil {
		return cloneConversationFile(conversation), nil
	}
	if err := update(conversation); err != nil {
		return nil, err
	}
	deriveConversationLoopState(conversation)
	if err := store.writeConversationLocked(normalizedConversationID, conversation, true); err != nil {
		return nil, err
	}
	return cloneConversationFile(conversation), nil
}

// readConversationLocked 以 context.json 为准恢复会话。
// state.json 中的字段都能从 entries 反推，因此它缺失或损坏时只丢失元数据，
// 不能让 context.json 里完好的历史一起变得不可见。
func (store *ConversationFileStore) readConversationLocked(conversationID string) (*ConversationFile, error) {
	if cached := store.lookupValidContextCache(conversationID); cached != nil {
		return store.conversationFromCache(conversationID, cached), nil
	}
	var conversation ConversationFile
	stateUsable := false
	stateBody, err := os.ReadFile(store.statePath(conversationID))
	switch {
	case err == nil:
		if err := json.Unmarshal(stateBody, &conversation); err != nil {
			conversation = ConversationFile{}
		} else {
			stateUsable = true
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("read conversation state: %w", err)
	}

	contextExists, err := fileExists(store.contextPath(conversationID))
	if err != nil {
		return nil, err
	}
	if !stateUsable && !contextExists {
		return nil, nil
	}

	// context.json 损坏时必须硬失败：继续下去会把空历史当成真实状态，
	// 随后的写入就会用它覆盖掉本还可以抢救的文件。
	context, err := store.readContextLocked(conversationID)
	if err != nil {
		return nil, err
	}
	conversation.Entries = context
	normalizeLoadedConversation(conversationID, &conversation)
	store.rememberContextWrite(conversationID, &conversation)
	return &conversation, nil
}

// conversationFromCache 用缓存条目拼装会话：entries 直接复用缓存副本，
// meta 仍以 state.json 为准（小文件，且可能刚被 UpdateConversationMeta 更新）。
func (store *ConversationFileStore) conversationFromCache(conversationID string, cached *contextDiskCache) *ConversationFile {
	var conversation ConversationFile
	if stateBody, err := os.ReadFile(store.statePath(conversationID)); err == nil {
		_ = json.Unmarshal(stateBody, &conversation)
	}
	conversation.Entries = append([]HistoryEntry(nil), cached.conversation.Entries...)
	normalizeLoadedConversation(conversationID, &conversation)
	return &conversation
}

func (store *ConversationFileStore) readContextLocked(conversationID string) ([]HistoryEntry, error) {
	body, err := os.ReadFile(store.contextPath(conversationID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make([]HistoryEntry, 0, 16), nil
		}
		return nil, fmt.Errorf("read conversation context: %w", err)
	}
	entries, err := parseConversationContextBody(body)
	if err != nil {
		return nil, fmt.Errorf("decode conversation context %q: %w", conversationID, err)
	}
	return entries, nil
}

// writeConversationLocked 落盘 context.json 与 state.json。
// 前置条件：调用方（AppendEntries / ReplaceEntries / saveConversationWithEntries /
// mutateConversation）都必须先跑过 deriveConversationLoopState——normalize 的全历史
// 扫描在这里再跑一遍是纯重复，长对话下每次追加都付三遍。
// structuredDirty 表示这次写入是否可能改变 plan/todo 等结构化状态；
// false 时跳过 refreshConversationRuntimeState 的全量 proto 解码。
func (store *ConversationFileStore) writeConversationLocked(conversationID string, conversation *ConversationFile, structuredDirty bool) error {
	if conversation == nil {
		return fmt.Errorf("conversation is nil")
	}
	if err := store.writeContextLocked(conversationID, conversation); err != nil {
		return err
	}
	return store.writeConversationMetaAfterAppendLocked(conversationID, conversation, structuredDirty)
}

// writeConversationMetaAfterAppendLocked 是已 normalize/derive 会话的 state.json 快路径。
func (store *ConversationFileStore) writeConversationMetaAfterAppendLocked(conversationID string, conversation *ConversationFile, structuredDirty bool) error {
	if conversation == nil {
		return fmt.Errorf("conversation is nil")
	}
	if structuredDirty {
		if err := refreshConversationRuntimeState(conversation); err != nil {
			return err
		}
	}
	metadata := cloneConversationMeta(conversation)
	metadata.SchemaVersion = conversationSchemaVersion
	metadata.ContextVersion = contextVersionForEntries(conversation.Entries)
	// state.json 是 context.json 的派生投影：readConversationLocked 在它缺失或损坏时
	// 会退回零值并从 entries 重建，因此这里不需要为它支付 fsync。
	return writeJSONFileAtomicWithoutSync(store.statePath(conversationID), metadata)
}

// writeConversationMetaLocked 写 state.json，供 UpdateConversationMeta 这类
// 「update 回调可能改动任意字段」的路径使用：normalize 全量重算，并在
// structuredDirty 时重算 plan/todo 等结构化状态。
func (store *ConversationFileStore) writeConversationMetaLocked(conversationID string, conversation *ConversationFile, structuredDirty bool) error {
	if conversation == nil {
		return fmt.Errorf("conversation is nil")
	}
	currentLoopID := conversation.CurrentLoopID
	currentLoopStatus := conversation.CurrentLoopStatus
	currentRequestID := conversation.CurrentRequestID
	currentTurnSeq := conversation.CurrentTurnSeq
	normalizeLoadedConversation(conversationID, conversation)
	if strings.TrimSpace(currentLoopStatus) != "" && (strings.TrimSpace(currentRequestID) == "" || conversationHasRequestEntry(conversation.Entries, currentRequestID, currentTurnSeq)) {
		conversation.CurrentLoopID = currentLoopID
		conversation.CurrentLoopStatus = currentLoopStatus
		conversation.CurrentRequestID = currentRequestID
		conversation.CurrentTurnSeq = currentTurnSeq
	}
	if structuredDirty {
		if err := refreshConversationRuntimeState(conversation); err != nil {
			return err
		}
	}
	metadata := cloneConversationMeta(conversation)
	metadata.SchemaVersion = conversationSchemaVersion
	metadata.ContextVersion = contextVersionForEntries(conversation.Entries)
	// state.json 是 context.json 的派生投影：readConversationLocked 在它缺失或损坏时
	// 会退回零值并从 entries 重建，因此这里不需要为它支付 fsync。
	return writeJSONFileAtomicWithoutSync(store.statePath(conversationID), metadata)
}

// writeContextLocked 全量重写 context.json（JSONL 格式）。撤回、压缩、快照保存
// 与旧格式迁移走这里；常规追加由 AppendEntries 的增量路径处理。
func (store *ConversationFileStore) writeContextLocked(conversationID string, conversation *ConversationFile) error {
	data, err := encodeConversationContextJSONL(conversationID, conversation)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(store.contextPath(conversationID), data, true); err != nil {
		return err
	}
	store.rememberContextWrite(conversationID, conversation)
	return nil
}

func contextVersionForEntries(entries []HistoryEntry) int64 {
	var version int64
	for _, entry := range entries {
		if entry.Seq > version {
			version = entry.Seq
		}
	}
	return version
}

func deriveConversationLoopState(conversation *ConversationFile) {
	if conversation == nil {
		return
	}
	conversation.SchemaVersion = conversationSchemaVersion
	conversation.ContextVersion = contextVersionForEntries(conversation.Entries)
	fallbackStatus := firstNonEmpty(strings.TrimSpace(conversation.CurrentLoopStatus), "idle")
	requestID := strings.TrimSpace(conversation.CurrentRequestID)
	turnSeq := conversation.CurrentTurnSeq
	if requestID != "" && !conversationHasRequestEntry(conversation.Entries, requestID, turnSeq) {
		requestID = ""
		turnSeq = 0
		fallbackStatus = "idle"
	}
	for index := len(conversation.Entries) - 1; index >= 0; index-- {
		entry := conversation.Entries[index]
		if strings.TrimSpace(entry.RequestID) == "" {
			continue
		}
		if requestID == "" {
			requestID = strings.TrimSpace(entry.RequestID)
			turnSeq = entry.TurnSeq
		}
		break
	}
	status := deriveRequestLoopStatus(conversation.Entries, requestID, turnSeq, fallbackStatus)
	conversation.CurrentRequestID = requestID
	conversation.CurrentTurnSeq = turnSeq
	if requestID != "" {
		conversation.CurrentLoopID = fmt.Sprintf("%d:%s", turnSeq, requestID)
	}
	conversation.CurrentLoopStatus = status
}

func conversationHasRequestEntry(entries []HistoryEntry, requestID string, turnSeq int64) bool {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.RequestID) != requestID {
			continue
		}
		if turnSeq > 0 && entry.TurnSeq > 0 && entry.TurnSeq != turnSeq {
			continue
		}
		return true
	}
	return false
}

func deriveRequestLoopStatus(entries []HistoryEntry, requestID string, turnSeq int64, fallbackStatus string) string {
	if strings.TrimSpace(requestID) == "" {
		return firstNonEmpty(strings.TrimSpace(fallbackStatus), "idle")
	}
	openToolCalls := make(map[string]struct{})
	terminalStatus := ""
	seenActivity := false
	for _, entry := range entries {
		if strings.TrimSpace(entry.RequestID) != strings.TrimSpace(requestID) {
			continue
		}
		if turnSeq > 0 && entry.TurnSeq > 0 && entry.TurnSeq != turnSeq {
			continue
		}
		switch strings.TrimSpace(entry.Kind) {
		case "tool_call":
			seenActivity = true
			toolCallID := historyEntryToolCallID(entry)
			if toolCallID == "" {
				toolCallID = fmt.Sprintf("entry:%d", entry.Seq)
			}
			openToolCalls[toolCallID] = struct{}{}
		case "tool_result", "assistant_text", "prompt_context", "request_context", "user_message":
			seenActivity = true
			if strings.TrimSpace(entry.Kind) == "tool_result" {
				if toolCallID := historyEntryToolCallID(entry); toolCallID != "" {
					delete(openToolCalls, toolCallID)
				}
			}
		case "metadata":
			var payload metadataPayload
			if err := json.Unmarshal(entry.Payload, &payload); err == nil {
				switch strings.TrimSpace(payload.Type) {
				case "turn_completed":
					terminalStatus = "completed"
				case "provider_error":
					terminalStatus = "provider_error"
				case "failed":
					terminalStatus = "failed"
				case "control":
					if strings.TrimSpace(readStringValue(payload.Value["status"])) == "canceled" {
						terminalStatus = "canceled"
					}
				case "run_request":
					seenActivity = true
				}
			}
		}
	}
	if terminalStatus != "" {
		return terminalStatus
	}
	if len(openToolCalls) > 0 {
		return "waiting_tool"
	}
	if seenActivity {
		return "running"
	}
	return firstNonEmpty(strings.TrimSpace(fallbackStatus), "idle")
}

func (store *ConversationFileStore) conversationDir(conversationID string) string {
	return filepath.Join(store.root, conversationID)
}

func (store *ConversationFileStore) statePath(conversationID string) string {
	return filepath.Join(store.conversationDir(conversationID), conversationStateFileName)
}

func (store *ConversationFileStore) contextPath(conversationID string) string {
	return filepath.Join(store.conversationDir(conversationID), conversationContextFileName)
}

func (store *ConversationFileStore) lockPath(conversationID string) string {
	return filepath.Join(store.conversationDir(conversationID), "conversation.lock")
}

func appendEntriesInPlace(conversation *ConversationFile, entries []HistoryEntry) []HistoryEntry {
	if conversation == nil || len(entries) == 0 {
		return nil
	}
	now := time.Now().UTC()
	assigned := make([]HistoryEntry, 0, len(entries))
	existingIdempotencyKeys := make(map[string]struct{})
	for _, existing := range conversation.Entries {
		if key := strings.TrimSpace(existing.IdempotencyKey); key != "" {
			existingIdempotencyKeys[key] = struct{}{}
		}
	}
	maxTurnSeq := conversation.NextTurnSeq - 1
	for _, entry := range entries {
		next := entry
		if key := strings.TrimSpace(next.IdempotencyKey); key != "" {
			if _, exists := existingIdempotencyKeys[key]; exists {
				continue
			}
			existingIdempotencyKeys[key] = struct{}{}
		}
		if next.CreatedAt.IsZero() {
			next.CreatedAt = now
		}
		if next.Seq <= 0 {
			next.Seq = conversation.NextEntrySeq
			conversation.NextEntrySeq++
		} else if next.Seq >= conversation.NextEntrySeq {
			conversation.NextEntrySeq = next.Seq + 1
		}
		if next.TurnSeq > maxTurnSeq {
			maxTurnSeq = next.TurnSeq
		}
		conversation.Entries = append(conversation.Entries, next)
		assigned = append(assigned, next)
	}
	if maxTurnSeq+1 > conversation.NextTurnSeq {
		conversation.NextTurnSeq = maxTurnSeq + 1
	}
	if conversation.CreatedAt.IsZero() {
		conversation.CreatedAt = now
	}
	conversation.UpdatedAt = now
	conversation.ContextVersion = contextVersionForEntries(conversation.Entries)
	return assigned
}

func mergeConversationMetadata(target *ConversationFile, source *ConversationFile) {
	if target == nil || source == nil {
		return
	}
	if strings.TrimSpace(source.ConversationID) != "" {
		target.ConversationID = strings.TrimSpace(source.ConversationID)
	}
	if strings.TrimSpace(source.RootConversationID) != "" {
		target.RootConversationID = strings.TrimSpace(source.RootConversationID)
	}
	target.ParentConversationID = strings.TrimSpace(source.ParentConversationID)
	target.ParentToolCallID = strings.TrimSpace(source.ParentToolCallID)
	target.SubagentTypeName = strings.TrimSpace(source.SubagentTypeName)
	// 标题由 UpdateConversationMetadata 单独写入；内存快照没带标题时保留磁盘值。
	if strings.TrimSpace(source.Name) != "" {
		target.Name = strings.TrimSpace(source.Name)
	}
	if strings.TrimSpace(source.Mode) != "" {
		target.Mode = strings.TrimSpace(source.Mode)
	}
	target.TokenDetailsUsedTokens = source.TokenDetailsUsedTokens
	if source.TokenDetailsMaxTokens > 0 {
		target.TokenDetailsMaxTokens = source.TokenDetailsMaxTokens
	}
	target.AutoCompactionPending = source.AutoCompactionPending
	target.AutoCompactionPromptTokens = source.AutoCompactionPromptTokens
	target.AutoCompactionReserveTokens = source.AutoCompactionReserveTokens
	target.AutoCompactionTriggeredAt = source.AutoCompactionTriggeredAt
	target.AutoCompactionSourceModelCallID = source.AutoCompactionSourceModelCallID
	target.CurrentPlanText = source.CurrentPlanText
	target.CurrentPlans = clonePlanRegistryEntries(source.CurrentPlans)
	target.CurrentTodos = cloneTodoItems(source.CurrentTodos)
	target.LatestRequestPrefix = cloneConversationRequestPrefix(source.LatestRequestPrefix)
	target.LastProviderCall = cloneConversationProviderCall(source.LastProviderCall)
	if !source.CreatedAt.IsZero() && (target.CreatedAt.IsZero() || source.CreatedAt.Before(target.CreatedAt)) {
		target.CreatedAt = source.CreatedAt
	}
	if !source.UpdatedAt.IsZero() && source.UpdatedAt.After(target.UpdatedAt) {
		target.UpdatedAt = source.UpdatedAt
	}
	if source.NextTurnSeq > target.NextTurnSeq {
		target.NextTurnSeq = source.NextTurnSeq
	}
	if source.NextEntrySeq > target.NextEntrySeq {
		target.NextEntrySeq = source.NextEntrySeq
	}
	target.CurrentLoopID = strings.TrimSpace(source.CurrentLoopID)
	target.CurrentLoopStatus = strings.TrimSpace(source.CurrentLoopStatus)
	target.CurrentRequestID = strings.TrimSpace(source.CurrentRequestID)
	target.CurrentTurnSeq = source.CurrentTurnSeq
}

func normalizeLoadedConversation(conversationID string, conversation *ConversationFile) {
	if conversation == nil {
		return
	}
	conversation.SchemaVersion = conversationSchemaVersion
	if strings.TrimSpace(conversation.ConversationID) == "" {
		conversation.ConversationID = conversationID
	}
	if strings.TrimSpace(conversation.RootConversationID) == "" {
		conversation.RootConversationID = conversation.ConversationID
	}
	// mode 缺失或不可识别时必须回落到 agent：它是投影的必经字段，
	// 一旦解析失败整个会话历史都无法投影给前端。
	conversation.Mode = normalizeConversationModeAlias(conversation.Mode)
	if conversation.NextTurnSeq <= 0 {
		conversation.NextTurnSeq = 1
	}
	if conversation.NextEntrySeq <= 0 {
		conversation.NextEntrySeq = 1
	}
	if conversation.Entries == nil {
		conversation.Entries = make([]HistoryEntry, 0, 16)
	}
	for _, entry := range conversation.Entries {
		if entry.Seq >= conversation.NextEntrySeq {
			conversation.NextEntrySeq = entry.Seq + 1
		}
		if entry.TurnSeq >= conversation.NextTurnSeq {
			conversation.NextTurnSeq = entry.TurnSeq + 1
		}
		if conversation.CreatedAt.IsZero() || (!entry.CreatedAt.IsZero() && entry.CreatedAt.Before(conversation.CreatedAt)) {
			conversation.CreatedAt = entry.CreatedAt
		}
		if !entry.CreatedAt.IsZero() && entry.CreatedAt.After(conversation.UpdatedAt) {
			conversation.UpdatedAt = entry.CreatedAt
		}
	}
	if conversation.CreatedAt.IsZero() {
		conversation.CreatedAt = time.Now().UTC()
	}
	if conversation.UpdatedAt.IsZero() {
		conversation.UpdatedAt = conversation.CreatedAt
	}
	deriveConversationLoopState(conversation)
}

func validateConversationID(conversationID string) (string, error) {
	normalized := strings.TrimSpace(conversationID)
	if normalized == "" {
		return "", fmt.Errorf("conversation_id is required")
	}
	if strings.Contains(normalized, "/") || strings.Contains(normalized, string(os.PathSeparator)) {
		return "", fmt.Errorf("conversation_id must not contain path separators")
	}
	return normalized, nil
}

// writeJSONFileAtomic 原子写入并保证内容已落盘，用于崩溃后必须可恢复的权威数据。
func writeJSONFileAtomic(path string, payload any) error {
	return writeJSONFile(path, payload, true)
}

// writeJSONFileAtomicWithoutSync 原子替换文件，但不为内容做 fsync。
// 只允许用于可从权威数据重建的派生文件：rename 本身仍保证读到的不是半截内容，
// 崩溃后最坏情况是这份文件停留在上一个版本，由重建逻辑纠正。
// fsync 在热路径上是毫秒级成本，对派生数据支付这个成本没有收益。
func writeJSONFileAtomicWithoutSync(path string, payload any) error {
	return writeJSONFile(path, payload, false)
}

func writeJSONFile(path string, payload any, durable bool) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	// 机器读写的文件不做美化：json.Indent 会把整份 context.json 再分配再拷贝一遍，
	// 这是每次追加都要付的纯展示成本。
	return writeFileAtomic(path, append(data, '\n'), durable)
}

// writeFileAtomic 原子替换文件内容。durable 时为内容做 fsync，用于崩溃后
// 必须可恢复的权威数据；否则只依赖 rename 的原子性，供可重建的派生文件使用。
func writeFileAtomic(path string, data []byte, durable bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	file, tempPath, err := openUniqueArtifactTempFile(path)
	if err != nil {
		return fmt.Errorf("open temp file: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	// rename 之前必须把内容刷到磁盘：否则崩溃或强杀后 rename 可能已生效，
	// 而目标文件仍是空的或半截的，读取时整条会话都会变得不可用。
	if durable {
		if err := file.Sync(); err != nil {
			file.Close()
			return fmt.Errorf("sync temp file: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := renameArtifactTempFile(tempPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	renamed = true
	if !durable {
		return nil
	}
	return syncDirectory(filepath.Dir(path))
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func cloneConversationFile(conversation *ConversationFile) *ConversationFile {
	cloned := cloneConversationMeta(conversation)
	if cloned == nil {
		return nil
	}
	cloned.Entries = append([]HistoryEntry(nil), conversation.Entries...)
	return cloned
}

// cloneConversationMeta 复制除 entries 之外的全部字段。
// 写 state.json 时 entries 会被丢弃，为它深拷贝整条历史是 O(历史长度) 的纯浪费。
func cloneConversationMeta(conversation *ConversationFile) *ConversationFile {
	if conversation == nil {
		return nil
	}
	cloned := *conversation
	cloned.CurrentPlans = clonePlanRegistryEntries(conversation.CurrentPlans)
	cloned.CurrentTodos = cloneTodoItems(conversation.CurrentTodos)
	cloned.LatestRequestPrefix = cloneConversationRequestPrefix(conversation.LatestRequestPrefix)
	cloned.LastProviderCall = cloneConversationProviderCall(conversation.LastProviderCall)
	cloned.Entries = nil
	return &cloned
}

// borrowConversationForMeta 返回一份只用于派生 state.json 的副本：
// meta 字段复制，entries 以只读方式借用。派生逻辑只读 entries，
// 容量封顶到 len 保证即使意外 append 也写不到调用方的底层数组上。
func borrowConversationForMeta(conversation *ConversationFile) *ConversationFile {
	borrowed := cloneConversationMeta(conversation)
	if borrowed == nil {
		return nil
	}
	borrowed.Entries = conversation.Entries[:len(conversation.Entries):len(conversation.Entries)]
	return borrowed
}

func cloneConversationRequestPrefix(prefix *ConversationRequestPrefix) *ConversationRequestPrefix {
	if prefix == nil {
		return nil
	}
	cloned := *prefix
	return &cloned
}

func cloneConversationProviderCall(call *ConversationProviderCall) *ConversationProviderCall {
	if call == nil {
		return nil
	}
	cloned := *call
	return &cloned
}

func cloneByteSlices(items [][]byte) [][]byte {
	if len(items) == 0 {
		return nil
	}
	cloned := make([][]byte, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, append([]byte(nil), item...))
	}
	return cloned
}

func cloneStringSlice(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	return append([]string(nil), items...)
}

func acquireConversationLock(lockPath string) (func(), error) {
	releaseProcessLock := acquireConversationProcessLock(lockPath)
	releaseFileLock, err := acquireConversationFileLock(lockPath)
	if err != nil {
		releaseProcessLock()
		return nil, err
	}
	return func() {
		releaseFileLock()
		releaseProcessLock()
	}, nil
}

func acquireConversationProcessLock(lockPath string) func() {
	key := filepath.Clean(lockPath)
	conversationProcessLocksMu.Lock()
	lock := conversationProcessLocks[key]
	if lock == nil {
		lock = &conversationProcessLock{}
		conversationProcessLocks[key] = lock
	}
	lock.refs++
	conversationProcessLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		conversationProcessLocksMu.Lock()
		lock.refs--
		if lock.refs <= 0 {
			delete(conversationProcessLocks, key)
		}
		conversationProcessLocksMu.Unlock()
	}
}

func acquireConversationFileLock(lockPath string) (func(), error) {
	deadline := time.Now().Add(conversationLockAcquireTimeout)
	var staleRemoveDeadline time.Time
	var lastStaleRemoveErr error
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			owner := conversationLockOwnerToken()
			_, _ = file.WriteString(fmt.Sprintf("pid=%d\nowner=%s\ncreated_at=%s\n", os.Getpid(), owner, time.Now().UTC().Format(time.RFC3339Nano)))
			_ = file.Close()
			return func() {
				removeConversationLockIfOwner(lockPath, owner)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create history lock: %w", err)
		}
		if stale, staleErr := conversationLockIsStale(lockPath); staleErr != nil {
			return nil, staleErr
		} else if stale {
			removeErr := os.Remove(lockPath)
			if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				lastStaleRemoveErr = nil
				staleRemoveDeadline = time.Time{}
				continue
			}
			if staleRemoveDeadline.IsZero() {
				staleRemoveDeadline = time.Now().Add(staleConversationLockRemoveTimeout)
			}
			lastStaleRemoveErr = removeErr
		} else {
			lastStaleRemoveErr = nil
			staleRemoveDeadline = time.Time{}
		}
		waitDeadline := deadline
		if lastStaleRemoveErr != nil && staleRemoveDeadline.After(waitDeadline) {
			waitDeadline = staleRemoveDeadline
		}
		remaining := time.Until(waitDeadline)
		if remaining <= 0 {
			break
		}
		if remaining > conversationLockRetryInterval {
			remaining = conversationLockRetryInterval
		}
		time.Sleep(remaining)
	}
	if lastStaleRemoveErr != nil {
		return nil, fmt.Errorf("timeout acquiring history lock %q (stale lock remove failed: %w)", lockPath, lastStaleRemoveErr)
	}
	return nil, fmt.Errorf("timeout acquiring history lock %q", lockPath)
}

func conversationLockIsStale(lockPath string) (bool, error) {
	info, err := os.Stat(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	if time.Since(info.ModTime()) > conversationLockStaleAfter {
		return true, nil
	}
	pid := readConversationLockPID(lockPath)
	if pid <= 0 {
		return time.Since(info.ModTime()) > legacyConversationLockStaleAfter, nil
	}
	if pid == os.Getpid() {
		if lockCreatedBeforeCurrentProcess(lockPath, info.ModTime()) {
			return true, nil
		}
		return false, nil
	}
	return !processLooksAlive(pid), nil
}

func lockCreatedBeforeCurrentProcess(lockPath string, modTime time.Time) bool {
	startedAt := conversationLockProcessStartedAt.Add(-time.Second)
	if createdAt := readConversationLockCreatedAt(lockPath); !createdAt.IsZero() {
		return createdAt.Before(startedAt)
	}
	return !modTime.IsZero() && modTime.Before(startedAt)
}

func readConversationLockPID(lockPath string) int {
	pid, _, _ := readConversationLockMetadata(lockPath)
	return pid
}

func readConversationLockCreatedAt(lockPath string) time.Time {
	_, createdAt, _ := readConversationLockMetadata(lockPath)
	return createdAt
}

func readConversationLockOwner(lockPath string) string {
	_, _, owner := readConversationLockMetadata(lockPath)
	return owner
}

func readConversationLockMetadata(lockPath string) (int, time.Time, string) {
	body, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, time.Time{}, ""
	}
	var pid int
	var createdAt time.Time
	var owner string
	for _, line := range strings.Split(string(body), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "pid":
			parsedPID, err := strconv.Atoi(strings.TrimSpace(value))
			if err == nil && parsedPID > 0 {
				pid = parsedPID
			}
		case "created_at":
			parsedCreatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
			if err == nil {
				createdAt = parsedCreatedAt
			}
		case "owner":
			owner = strings.TrimSpace(value)
		}
	}
	return pid, createdAt, owner
}

func conversationLockOwnerToken() string {
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}

func removeConversationLockIfOwner(lockPath string, owner string) {
	if strings.TrimSpace(owner) != "" {
		currentOwner := readConversationLockOwner(lockPath)
		if currentOwner != "" && currentOwner != owner {
			return
		}
	}
	_ = os.Remove(lockPath)
}

func processLooksAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	return processExists(pid)
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
