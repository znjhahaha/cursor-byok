package logger

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cursor/internal/appdata"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
)

const (
	appLogMaxBytes    int64 = 4 * 1024 * 1024
	appLogBackupCount       = 2
)

var (
	initOnce sync.Once
	fileSink *switchableFileHandler
)

// Init 配置默认 slog logger，并把标准库 log 接到同一输出。
func Init() {
	initOnce.Do(func() {
		consoleHandler := tint.NewHandler(colorable.NewColorableStdout(), &tint.Options{
			Level:      slog.LevelInfo,
			TimeFormat: "15:04:05.000",
			NoColor:    disableColor(),
		})
		fileSink = newSwitchableFileHandler(
			filepath.Join(appdata.LogsRootPath(), "app.log"),
			appLogMaxBytes,
			appLogBackupCount,
		)
		slog.SetDefault(slog.New(&multiHandler{handlers: []slog.Handler{consoleHandler, fileSink}}))
		stdlog.SetFlags(0)
	})
}

// SetFileLoggingEnabled 热切换文件日志；控制台日志始终保留。
func SetFileLoggingEnabled(value bool) {
	Init()
	if fileSink == nil || !fileSink.SetEnabled(value) {
		return
	}
	if value {
		slog.Info("详细日志已启用", "path", filepath.Join(appdata.LogsRootPath(), "app.log"), "pid", os.Getpid())
		return
	}
	slog.Info("详细日志已关闭")
}

// FileLoggingEnabled 返回当前是否写入应用日志文件。
func FileLoggingEnabled() bool {
	Init()
	return fileSink != nil && fileSink.EnabledForWriting()
}

// Info 输出 info 级日志。
func Info(msg string, args ...any) {
	Init()
	slog.Info(msg, args...)
}

// Error 输出 error 级日志。
func Error(msg string, args ...any) {
	Init()
	slog.Error(msg, args...)
}

// Infof 输出格式化的 info 级日志。
func Infof(format string, args ...any) {
	Init()
	slog.Info(formatMessage(format, args...))
}

// Errorf 输出格式化的 error 级日志。
func Errorf(format string, args ...any) {
	Init()
	slog.Error(formatMessage(format, args...))
}

func formatMessage(format string, args ...any) string {
	if len(args) == 0 {
		return strings.TrimSpace(format)
	}
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}

func disableColor() bool {
	if strings.TrimSpace(os.Getenv("NO_COLOR")) != "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return true
	}
	fd := os.Stdout.Fd()
	return !isatty.IsTerminal(fd) && !isatty.IsCygwinTerminal(fd)
}

type fileHandlerState struct {
	mu      sync.RWMutex
	enabled bool
	writer  *rollingFileWriter
}

type switchableFileHandler struct {
	state   *fileHandlerState
	handler slog.Handler
}

func newSwitchableFileHandler(path string, maxBytes int64, backupCount int) *switchableFileHandler {
	writer := &rollingFileWriter{
		path:        strings.TrimSpace(path),
		maxBytes:    maxBytes,
		backupCount: backupCount,
	}
	return &switchableFileHandler{
		state: &fileHandlerState{writer: writer},
		handler: tint.NewHandler(writer, &tint.Options{
			Level:      slog.LevelInfo,
			TimeFormat: time.RFC3339,
			NoColor:    true,
		}),
	}
}

func (handler *switchableFileHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if handler == nil || handler.state == nil || handler.handler == nil {
		return false
	}
	handler.state.mu.RLock()
	defer handler.state.mu.RUnlock()
	return handler.state.enabled && handler.handler.Enabled(ctx, level)
}

func (handler *switchableFileHandler) Handle(ctx context.Context, record slog.Record) error {
	if handler == nil || handler.state == nil || handler.handler == nil {
		return nil
	}
	handler.state.mu.RLock()
	defer handler.state.mu.RUnlock()
	if !handler.state.enabled || !handler.handler.Enabled(ctx, record.Level) {
		return nil
	}
	return handler.handler.Handle(ctx, record)
}

func (handler *switchableFileHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if handler == nil {
		return handler
	}
	return &switchableFileHandler{state: handler.state, handler: handler.handler.WithAttrs(attrs)}
}

func (handler *switchableFileHandler) WithGroup(name string) slog.Handler {
	if handler == nil {
		return handler
	}
	return &switchableFileHandler{state: handler.state, handler: handler.handler.WithGroup(name)}
}

func (handler *switchableFileHandler) SetEnabled(value bool) bool {
	if handler == nil || handler.state == nil {
		return false
	}
	handler.state.mu.Lock()
	defer handler.state.mu.Unlock()
	previous := handler.state.enabled
	handler.state.enabled = value
	if !value && handler.state.writer != nil {
		_ = handler.state.writer.Close()
	}
	return previous != value
}

func (handler *switchableFileHandler) EnabledForWriting() bool {
	if handler == nil || handler.state == nil {
		return false
	}
	handler.state.mu.RLock()
	defer handler.state.mu.RUnlock()
	return handler.state.enabled
}

type rollingFileWriter struct {
	mu          sync.Mutex
	path        string
	file        *os.File
	size        int64
	maxBytes    int64
	backupCount int
}

func (writer *rollingFileWriter) Write(payload []byte) (int, error) {
	if writer == nil || strings.TrimSpace(writer.path) == "" {
		return 0, fmt.Errorf("log file writer is not initialized")
	}
	if len(payload) == 0 {
		return 0, nil
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := writer.openLocked(); err != nil {
		return 0, err
	}
	if writer.maxBytes > 0 && writer.size > 0 && writer.size+int64(len(payload)) > writer.maxBytes {
		if err := writer.rotateLocked(); err != nil {
			return 0, err
		}
	}
	written, err := writer.file.Write(payload)
	writer.size += int64(written)
	return written, err
}

func (writer *rollingFileWriter) Close() error {
	if writer == nil {
		return nil
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.closeLocked()
}

func (writer *rollingFileWriter) openLocked() error {
	if writer.file != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(writer.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(writer.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return statErr
	}
	writer.file = file
	writer.size = info.Size()
	return nil
}

func (writer *rollingFileWriter) closeLocked() error {
	if writer.file == nil {
		return nil
	}
	err := writer.file.Close()
	writer.file = nil
	return err
}

func (writer *rollingFileWriter) rotateLocked() error {
	if err := writer.closeLocked(); err != nil {
		return err
	}
	if writer.backupCount <= 0 {
		if err := removeIfExists(writer.path); err != nil {
			return errors.Join(err, writer.openLocked())
		}
		writer.size = 0
		return writer.openLocked()
	}
	for index := writer.backupCount; index >= 1; index-- {
		target := fmt.Sprintf("%s.%d", writer.path, index)
		if err := removeIfExists(target); err != nil {
			return errors.Join(err, writer.openLocked())
		}
		if index == 1 {
			if err := renameIfExists(writer.path, target); err != nil {
				return errors.Join(err, writer.openLocked())
			}
			continue
		}
		source := fmt.Sprintf("%s.%d", writer.path, index-1)
		if err := renameIfExists(source, target); err != nil {
			return errors.Join(err, writer.openLocked())
		}
	}
	writer.size = 0
	return writer.openLocked()
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func renameIfExists(source string, target string) error {
	err := os.Rename(source, target)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

type multiHandler struct {
	handlers []slog.Handler
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, record slog.Record) error {
	var handleErr error
	for _, handler := range h.handlers {
		if !handler.Enabled(ctx, record.Level) {
			continue
		}
		if err := handler.Handle(ctx, record.Clone()); err != nil {
			handleErr = errors.Join(handleErr, err)
		}
	}
	return handleErr
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		next = append(next, handler.WithAttrs(attrs))
	}
	return &multiHandler{handlers: next}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		next = append(next, handler.WithGroup(name))
	}
	return &multiHandler{handlers: next}
}
