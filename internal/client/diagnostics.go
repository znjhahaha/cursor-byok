package client

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cursor/internal/appdata"
)

const diagnosticLogPageLimit = 200

type DiagnosticLogQuery struct {
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
	Level     string `json:"level"`
	RequestID string `json:"requestID"`
	Model     string `json:"model"`
	Search    string `json:"search"`
}

type DiagnosticLogEntry struct {
	Index   int    `json:"index"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type DiagnosticLogPage struct {
	Entries    []DiagnosticLogEntry `json:"entries"`
	Total      int                  `json:"total"`
	NextOffset int                  `json:"nextOffset"`
	HasMore    bool                 `json:"hasMore"`
}

type DiagnosticExportResult struct {
	Path string `json:"path"`
}

var (
	diagnosticBearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/\-=]+`)
	diagnosticJWTShape      = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	diagnosticSecretShape   = regexp.MustCompile(`(?i)\b(?:sk|rk|pk|api)[-_][A-Za-z0-9_-]{8,}\b`)
	diagnosticNamedSecret   = regexp.MustCompile(`(?i)("?(?:authorization|proxy-authorization|cookie|set-cookie|x-api-key|api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret)"?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;}\]]+)`)
	diagnosticLevelPattern  = regexp.MustCompile(`(?i)\b(DEBUG|INFO|WARN|WARNING|ERROR)\b`)
)

func (s *ProxyService) GetDiagnosticLogs(query DiagnosticLogQuery) (DiagnosticLogPage, error) {
	lines, err := readDiagnosticLogLines(filepath.Join(appdata.LogsRootPath(), "app.log"))
	if err != nil {
		return DiagnosticLogPage{}, err
	}
	filtered := make([]DiagnosticLogEntry, 0, len(lines))
	levelFilter := strings.ToUpper(strings.TrimSpace(query.Level))
	needles := []string{
		strings.ToLower(strings.TrimSpace(query.RequestID)),
		strings.ToLower(strings.TrimSpace(query.Model)),
		strings.ToLower(strings.TrimSpace(query.Search)),
	}
	for index := len(lines) - 1; index >= 0; index-- {
		message := redactDiagnosticText(lines[index])
		level := diagnosticLogLevel(message)
		if levelFilter != "" && levelFilter != "ALL" && level != levelFilter {
			continue
		}
		lower := strings.ToLower(message)
		matched := true
		for _, needle := range needles {
			if needle != "" && !strings.Contains(lower, needle) {
				matched = false
				break
			}
		}
		if matched {
			filtered = append(filtered, DiagnosticLogEntry{Index: index, Level: level, Message: message})
		}
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	limit := query.Limit
	if limit <= 0 || limit > diagnosticLogPageLimit {
		limit = diagnosticLogPageLimit
	}
	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return DiagnosticLogPage{
		Entries:    filtered[offset:end],
		Total:      len(filtered),
		NextOffset: end,
		HasMore:    end < len(filtered),
	}, nil
}

func (s *ProxyService) ExportDiagnosticBundle() (DiagnosticExportResult, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return DiagnosticExportResult{}, err
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return DiagnosticExportResult{}, err
	}
	lines, err := readDiagnosticLogLines(filepath.Join(appdata.LogsRootPath(), "app.log"))
	if err != nil {
		return DiagnosticExportResult{}, err
	}
	if len(lines) > 1000 {
		lines = lines[len(lines)-1000:]
	}
	for index := range lines {
		lines[index] = redactDiagnosticText(lines[index])
	}
	summary := buildDiagnosticConfigSummary(cfg)
	usageMetadata := readDiagnosticUsageMetadata(appdata.UsageFilePath())

	path := filepath.Join(appdata.LogsRootPath(), "diagnostics-"+time.Now().Format("20060102-150405")+".zip")
	file, err := os.Create(path)
	if err != nil {
		return DiagnosticExportResult{}, fmt.Errorf("create diagnostic bundle: %w", err)
	}
	writer := zip.NewWriter(file)
	writeErr := writeDiagnosticZipFile(writer, "config-summary.json", summary)
	if writeErr == nil {
		writeErr = writeDiagnosticZipFile(writer, "usage-metadata.json", usageMetadata)
	}
	if writeErr == nil {
		writeErr = writeDiagnosticZipFile(writer, "app-log-tail.txt", strings.Join(lines, "\n"))
	}
	closeZipErr := writer.Close()
	closeFileErr := file.Close()
	if writeErr != nil {
		return DiagnosticExportResult{}, writeErr
	}
	if closeZipErr != nil {
		return DiagnosticExportResult{}, closeZipErr
	}
	if closeFileErr != nil {
		return DiagnosticExportResult{}, closeFileErr
	}
	return DiagnosticExportResult{Path: path}, nil
}

func readDiagnosticLogLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read diagnostic log: %w", err)
	}
	defer file.Close()
	lines := make([]string, 0, 512)
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan diagnostic log: %w", err)
	}
	return lines, nil
}

func redactDiagnosticText(value string) string {
	value = diagnosticBearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = diagnosticJWTShape.ReplaceAllString(value, "[REDACTED_JWT]")
	value = diagnosticSecretShape.ReplaceAllString(value, "[REDACTED_KEY]")
	return diagnosticNamedSecret.ReplaceAllString(value, `${1}[REDACTED]`)
}

func diagnosticLogLevel(value string) string {
	match := diagnosticLevelPattern.FindStringSubmatch(value)
	if len(match) < 2 {
		return "INFO"
	}
	level := strings.ToUpper(match[1])
	if level == "WARNING" {
		return "WARN"
	}
	return level
}

func buildDiagnosticConfigSummary(cfg UserConfig) map[string]any {
	providers := make([]map[string]any, 0, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		host := ""
		if parsed, err := url.Parse(strings.TrimSpace(provider.BaseURL)); err == nil {
			host = parsed.Hostname()
		}
		providers = append(providers, map[string]any{
			"id":            provider.ID,
			"name":          provider.Name,
			"type":          provider.Type,
			"host":          host,
			"clientProfile": provider.ClientProfile,
		})
	}
	models := make([]map[string]any, 0, len(cfg.ModelAdapters))
	for _, model := range cfg.ModelAdapters {
		models = append(models, map[string]any{
			"displayName":      model.DisplayName,
			"type":             model.Type,
			"modelID":          model.ModelID,
			"providerID":       model.ProviderID,
			"clientProfile":    model.ClientProfile,
			"context1MEnabled": model.Anthropic1MContextEnabled,
		})
	}
	return map[string]any{
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
		"logEnabled":  cfg.Log,
		"routingMode": cfg.Routing.Mode,
		"providers":   providers,
		"models":      models,
	}
}

func readDiagnosticUsageMetadata(path string) map[string]any {
	body, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{"available": false}
	}
	var source struct {
		SchemaVersion  int       `json:"schema_version"`
		UpdatedAt      time.Time `json:"updated_at"`
		Timezone       string    `json:"timezone"`
		LegacyUTCDates bool      `json:"legacy_utc_dates"`
	}
	if json.Unmarshal(body, &source) != nil {
		return map[string]any{"available": false}
	}
	return map[string]any{
		"available":      true,
		"schemaVersion":  source.SchemaVersion,
		"updatedAt":      source.UpdatedAt,
		"timezone":       source.Timezone,
		"legacyUTCDates": source.LegacyUTCDates,
	}
}

func writeDiagnosticZipFile(writer *zip.Writer, name string, value any) error {
	entry, err := writer.Create(name)
	if err != nil {
		return err
	}
	var body []byte
	switch typed := value.(type) {
	case string:
		body = []byte(typed)
	default:
		body, err = json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
	}
	_, err = bytes.NewReader(body).WriteTo(entry)
	return err
}
