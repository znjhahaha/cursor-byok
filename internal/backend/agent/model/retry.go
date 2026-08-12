package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	providerRetryMaxAttempts   = 7
	providerRetrySummaryHeader = "X-Cursor-Provider-Retry-Summary"
)

var providerRetryBackoff = []time.Duration{
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	30 * time.Second,
	30 * time.Second,
}

// DoProviderRequestWithRetry keeps the historical entry point and retries
// transient upstream capacity failures with bounded backoff.
func DoProviderRequestWithRetry(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	buildRequest func(context.Context) (*http.Request, error),
) (*http.Response, error) {
	return doProviderRequestWithRetry(ctx, client, provider, requestID, modelCallID, providerRetryMaxAttempts, buildRequest)
}

func doProviderRequestWithRetry(
	ctx context.Context,
	client *http.Client,
	provider string,
	requestID string,
	modelCallID string,
	maxAttempts int,
	buildRequest func(context.Context) (*http.Request, error),
) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if maxAttempts <= 0 {
		maxAttempts = providerRetryMaxAttempts
	}
	statuses := make([]string, 0, maxAttempts)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		httpReq, err := buildRequest(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(httpReq)
		if err != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			statuses = append(statuses, "transport")
			if attempt == maxAttempts-1 || !isRetryableProviderTransportError(err) {
				if len(statuses) > 1 {
					return nil, fmt.Errorf("provider request failed after attempts=%d statuses=%s: %w", len(statuses), strings.Join(statuses, ","), err)
				}
				return nil, err
			}
			if err := waitProviderRetry(ctx, providerRetryBackoffDelay(attempt)); err != nil {
				return nil, err
			}
			continue
		}
		statuses = append(statuses, strconv.Itoa(resp.StatusCode))
		if !isRetryableProviderStatus(resp.StatusCode) || attempt == maxAttempts-1 {
			if len(statuses) > 1 {
				resp.Header.Set(providerRetrySummaryHeader, fmt.Sprintf("attempts=%d statuses=%s", len(statuses), strings.Join(statuses, ",")))
			}
			return resp, nil
		}

		delay := providerRetryDelay(resp, attempt)
		if resp.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
			_ = resp.Body.Close()
		}
		if err := waitProviderRetry(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("provider retry loop exhausted")
}

func isRetryableProviderTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// 中转站掐掉连接时 client.Do 会返回裸 io.EOF(例如 `Post ...: EOF`）。
	// 此时请求没有收到任何响应字节，重试是安全的。
	if errors.Is(err, io.EOF) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, token := range []string{
		"tls: handshake failure",
		"tls handshake timeout",
		"unexpected eof",
		"connection reset",
		"connection refused",
		"server closed idle connection",
		"use of closed network connection",
		"i/o timeout",
	} {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

func isRetryableProviderStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func providerRetryDelay(resp *http.Response, attempt int) time.Duration {
	if resp != nil {
		retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
		if retryAt, err := http.ParseTime(retryAfter); err == nil {
			if delay := time.Until(retryAt); delay > 0 {
				return delay
			}
			return 0
		}
	}
	if attempt >= 0 && attempt < len(providerRetryBackoff) {
		return providerRetryBackoff[attempt]
	}
	return providerRetryBackoff[len(providerRetryBackoff)-1]
}

func providerRetryBackoffDelay(attempt int) time.Duration {
	if attempt >= 0 && attempt < len(providerRetryBackoff) {
		return providerRetryBackoff[attempt]
	}
	return providerRetryBackoff[len(providerRetryBackoff)-1]
}

func waitProviderRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ProviderRetryAttemptSummary returns a safe status sequence without request
// headers, bodies, or credentials.
func ProviderRetryAttemptSummary(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.Header.Get(providerRetrySummaryHeader))
}
