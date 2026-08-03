// http_error.go 负责把非 2xx HTTP 响应整理成带响应体摘要的错误。
package modeladapter

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// maxErrorBodyBytes 表示错误响应体最多读取的字节数。
	maxErrorBodyBytes = 8192
)

// buildHTTPStatusError 读取响应体摘要并生成带状态码的错误。
func buildHTTPStatusError(prefix string, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("%s response is nil", strings.TrimSpace(prefix))
	}

	retryAfterSummary := retryAfterErrorSummary(resp.Header.Get("Retry-After"))
	limitedBody, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		if retrySummary := ProviderRetryAttemptSummary(resp); retrySummary != "" {
			return fmt.Errorf("%s status=%d %s %s body_read_error=%v", strings.TrimSpace(prefix), resp.StatusCode, retrySummary, retryAfterSummary, err)
		}
		return fmt.Errorf("%s status=%d %s body_read_error=%v", strings.TrimSpace(prefix), resp.StatusCode, retryAfterSummary, err)
	}
	retrySummary := strings.TrimSpace(strings.Join([]string{ProviderRetryAttemptSummary(resp), retryAfterSummary}, " "))
	bodyText := strings.TrimSpace(string(limitedBody))
	if bodyText == "" {
		if retrySummary != "" {
			return fmt.Errorf("%s status=%d %s", strings.TrimSpace(prefix), resp.StatusCode, retrySummary)
		}
		return fmt.Errorf("%s status=%d", strings.TrimSpace(prefix), resp.StatusCode)
	}
	if retrySummary != "" {
		return fmt.Errorf("%s status=%d %s body=%s", strings.TrimSpace(prefix), resp.StatusCode, retrySummary, bodyText)
	}
	return fmt.Errorf("%s status=%d body=%s", strings.TrimSpace(prefix), resp.StatusCode, bodyText)
}

func retryAfterErrorSummary(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		delay := time.Until(retryAt)
		if delay < 0 {
			delay = 0
		}
		return "retry_after=" + delay.Round(time.Millisecond).String()
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return ""
		}
	}
	return "retry_after=" + value
}
