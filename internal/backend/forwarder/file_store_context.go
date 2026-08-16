// file_store_context.go 负责 context.json 的磁盘格式：兼容旧版单 JSON 对象与
// 新版 JSONL（首行 header + 每行一条 entry），并为追加路径提供增量写入与缓存校验。
package forwarder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// parseConversationContextBody 兼容两种 context.json：旧版整体 JSON 对象，以及
// 新版「首行 header + 每行一条 entry」的 JSONL。整体 JSON 解析成功按旧格式处理；
// 多行 JSONL 一定解析失败（首对象后有尾随数据），转入逐行解析。
func parseConversationContextBody(body []byte) ([]HistoryEntry, error) {
	var legacy conversationContextFile
	if err := json.Unmarshal(body, &legacy); err == nil {
		return append([]HistoryEntry(nil), legacy.Items...), nil
	}
	return parseConversationContextJSONL(body)
}

// parseConversationContextJSONL 逐行解析 JSONL 格式的 context.json。
// 不用 bufio.Scanner：它的 64KB 单行上限装不下携带整个文件内容的 entry payload。
// 结尾的半行（追加中途崩溃的残迹）容忍并丢弃；中间损坏仍硬失败，
// 因为静默跳过中间条目会让历史出现不可解释的空洞。
func parseConversationContextJSONL(body []byte) ([]HistoryEntry, error) {
	rest := body
	first := true
	entries := make([]HistoryEntry, 0, 256)
	for len(rest) > 0 {
		var line []byte
		if index := bytes.IndexByte(rest, '\n'); index >= 0 {
			line, rest = rest[:index], rest[index+1:]
		} else {
			line, rest = rest, nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if first {
			first = false
			var header conversationContextHeader
			if err := json.Unmarshal(line, &header); err != nil {
				return nil, fmt.Errorf("decode conversation context header: %w", err)
			}
			if strings.TrimSpace(header.Format) != conversationContextFormatJSONL {
				return nil, fmt.Errorf("unsupported conversation context format %q", header.Format)
			}
			continue
		}
		var entry HistoryEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			if len(rest) == 0 {
				break
			}
			return nil, fmt.Errorf("decode conversation context entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if first {
		return nil, fmt.Errorf("conversation context body has no header line")
	}
	return entries, nil
}

// encodeConversationContextJSONL 全量编码 context.json（JSONL 格式）。
// 只在撤回重写、压缩、保存快照与旧格式迁移时调用；常规追加走 appendContextEntries。
func encodeConversationContextJSONL(conversationID string, conversation *ConversationFile) ([]byte, error) {
	var buffer bytes.Buffer
	header, err := json.Marshal(conversationContextHeader{
		SchemaVersion:  conversationSchemaVersion,
		ConversationID: strings.TrimSpace(conversationID),
		Format:         conversationContextFormatJSONL,
		Version:        contextVersionForEntries(conversation.Entries),
		UpdatedAt:      time.Now().UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("encode conversation context header: %w", err)
	}
	buffer.Write(header)
	buffer.WriteByte('\n')
	for _, entry := range conversation.Entries {
		line, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("encode history entry: %w", err)
		}
		buffer.Write(line)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}

// appendContextEntries 把新 entry 以 JSONL 行追加到 context.json 尾部。
// 调用前提：缓存校验已确认磁盘文件就是本进程上次写入的状态，文件末尾必然是完整行。
// 追加的内容本身就是权威事实，崩溃恢复依赖它完整落盘，因此保留 fsync。
func (store *ConversationFileStore) appendContextEntries(conversationID string, entries []HistoryEntry) error {
	if len(entries) == 0 {
		return nil
	}
	var buffer bytes.Buffer
	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("encode history entry: %w", err)
		}
		buffer.Write(line)
		buffer.WriteByte('\n')
	}
	path := store.contextPath(conversationID)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open conversation context for append: %w", err)
	}
	if _, err := file.Write(buffer.Bytes()); err != nil {
		file.Close()
		return fmt.Errorf("append conversation context: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync conversation context: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close conversation context: %w", err)
	}
	return nil
}

// lookupValidContextCache 返回命中缓存的会话，未命中返回 nil。
// 校验规则：context.json 的 size 与 modtime 都必须等于缓存记录值。
// 文件被删除或被任何写入者（包括其他进程）改动都会 miss，缓存随之作废，
// 调用方回退全量读取——正确性永远不依赖缓存的命中。
func (store *ConversationFileStore) lookupValidContextCache(conversationID string) *contextDiskCache {
	if store == nil {
		return nil
	}
	info, err := os.Stat(store.contextPath(conversationID))
	if err != nil {
		store.dropContextCache(conversationID)
		return nil
	}
	store.contextMu.Lock()
	cached := store.contextCache[conversationID]
	store.contextMu.Unlock()
	if cached == nil || cached.size != info.Size() || !cached.modTime.Equal(info.ModTime()) {
		store.dropContextCache(conversationID)
		return nil
	}
	return cached
}

func (store *ConversationFileStore) dropContextCache(conversationID string) {
	if store == nil {
		return
	}
	store.contextMu.Lock()
	delete(store.contextCache, conversationID)
	store.contextMu.Unlock()
}

// rememberContextWrite 在一次成功写入后记录新的磁盘状态。
// 缓存持有独立副本：AppendEntries 会把 conversation 的所有权交给调用方，
// 调用方后续的就地修改不能穿透到缓存。
func (store *ConversationFileStore) rememberContextWrite(conversationID string, conversation *ConversationFile) {
	if store == nil || conversation == nil {
		return
	}
	info, err := os.Stat(store.contextPath(conversationID))
	if err != nil {
		store.dropContextCache(conversationID)
		return
	}
	store.contextMu.Lock()
	store.contextCache[conversationID] = &contextDiskCache{
		conversation: cloneConversationFile(conversation),
		size:         info.Size(),
		modTime:      info.ModTime(),
	}
	store.contextMu.Unlock()
}
