package bridge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"cursor/internal/appdata"
	"cursor/internal/backend/forwarder"
)

// ConversationSearchHit 定义单个会话的搜索命中。
type ConversationSearchHit = forwarder.ConversationSearchHit

// ConversationSearchOptions 定义一次搜索的条件。
type ConversationSearchOptions = forwarder.ConversationSearchOptions

// ConversationSearchResult 定义搜索结果与截断标记。
type ConversationSearchResult = forwarder.ConversationSearchResult

// ConversationTranscript 定义会话的只读消息投影。
type ConversationTranscript = forwarder.ConversationTranscript

// ConversationsService 定义会话搜索与导出的 Wails service。
type ConversationsService struct {
	app *application.App
	mu  sync.RWMutex
}

// NewConversationsService 创建会话搜索 service。
func NewConversationsService() *ConversationsService {
	return &ConversationsService{}
}

// SetApp 注入应用实例，导出时用于弹出系统保存对话框。
func (service *ConversationsService) SetApp(app *application.App) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.app = app
}

// SearchConversations 在本地历史中全文检索；query 为空时返回最近会话列表。
func (service *ConversationsService) SearchConversations(options ConversationSearchOptions) (ConversationSearchResult, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return ConversationSearchResult{}, err
	}
	return forwarder.SearchConversations(appdata.HistoryRootPath(), options)
}

// DeleteConversation 永久删除指定会话（含 debug 子目录），不可恢复。
func (service *ConversationsService) DeleteConversation(conversationID string) error {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return err
	}
	return forwarder.DeleteConversation(appdata.HistoryRootPath(), conversationID)
}

// GetConversationTranscript 返回指定会话的只读消息流。
func (service *ConversationsService) GetConversationTranscript(conversationID string) (ConversationTranscript, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return ConversationTranscript{}, err
	}
	return forwarder.GetConversationTranscript(appdata.HistoryRootPath(), conversationID)
}

// ExportConversationMarkdown 把会话导出为 Markdown 文件。
// 优先弹系统保存对话框让用户选路径；对话框不可用时退化写入应用数据的 exports 目录。
// 返回最终写入路径；用户取消保存时返回空字符串且不视为错误。
func (service *ConversationsService) ExportConversationMarkdown(conversationID string) (string, error) {
	transcript, err := service.GetConversationTranscript(conversationID)
	if err != nil {
		return "", err
	}
	markdown := forwarder.RenderConversationMarkdown(transcript)
	filename := buildConversationExportFilename(transcript)

	targetPath, err := service.promptExportPath(filename)
	if err != nil {
		return "", err
	}
	if targetPath == "" {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return "", fmt.Errorf("create export directory: %w", err)
	}
	if err := os.WriteFile(targetPath, []byte(markdown), 0o644); err != nil {
		return "", fmt.Errorf("write markdown file: %w", err)
	}
	return targetPath, nil
}

func (service *ConversationsService) promptExportPath(filename string) (string, error) {
	service.mu.RLock()
	app := service.app
	service.mu.RUnlock()
	if app == nil {
		return filepath.Join(appdata.DataRootPath(), "exports", filename), nil
	}
	path, err := app.Dialog.SaveFile().
		SetFilename(filename).
		AddFilter("Markdown (*.md)", "*.md").
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(path), nil
}

// buildConversationExportFilename 用会话标题生成安全文件名，标题缺失时回退会话 ID。
func buildConversationExportFilename(transcript ConversationTranscript) string {
	base := strings.TrimSpace(transcript.Name)
	if base == "" {
		base = strings.TrimSpace(transcript.ConversationID)
	}
	if base == "" {
		base = "conversation"
	}
	base = sanitizeExportFilename(base)
	const maxBaseLength = 60
	if runes := []rune(base); len(runes) > maxBaseLength {
		base = string(runes[:maxBaseLength])
	}
	timestamp := transcript.UpdatedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	return fmt.Sprintf("%s-%s.md", base, timestamp.In(time.Local).Format("20060102-1504"))
}

func sanitizeExportFilename(name string) string {
	replacer := strings.NewReplacer(
		"<", "-", ">", "-", ":", "-", "\"", "-", "/", "-",
		"\\", "-", "|", "-", "?", "-", "*", "-", "\n", " ", "\r", " ", "\t", " ",
	)
	cleaned := strings.TrimSpace(replacer.Replace(name))
	cleaned = strings.Trim(cleaned, ". ")
	if cleaned == "" {
		return "conversation"
	}
	return cleaned
}
