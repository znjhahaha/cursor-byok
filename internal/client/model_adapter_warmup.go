package client

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/logger"
)

const (
	maxConcurrentWarmups       = 2
	warmupMaxTransientFailures = 5
	warmupBaseDelay            = 500 * time.Millisecond
	warmupMaxDelay             = 5 * time.Second
	warmupProgressInterval     = 250 * time.Millisecond
)

var (
	errWarmupBusy       = errors.New("正在排队检测的模型过多，请等待其中一个结束")
	warmupStatusPattern = regexp.MustCompile(`status=(\d{3})`)
	warmupRetryPattern  = regexp.MustCompile(`retry_after=([^\s]+)`)
	warmupJitter        = func() float64 { return 0.9 + rand.Float64()*0.2 }
)

var queueRejectionMarkers = []string{
	"get_channel_failed",
	"new_api_error",
	"no available channel",
	"负载已经达到上限",
	"负载已达到上限",
	"当前分组上游负载",
	"当前分组负载已饱和",
}

func warmupHTTPStatus(err error) int {
	if err == nil {
		return 0
	}
	match := warmupStatusPattern.FindStringSubmatch(err.Error())
	if len(match) != 2 {
		return 0
	}
	status, _ := strconv.Atoi(match[1])
	return status
}

func isQueueRejectionError(err error) bool {
	status := warmupHTTPStatus(err)
	if status == http.StatusTooManyRequests {
		return true
	}
	if status != http.StatusInternalServerError {
		return false
	}
	lowered := strings.ToLower(err.Error())
	for _, marker := range queueRejectionMarkers {
		if strings.Contains(lowered, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func isTransientWarmupError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var incomplete *modeladapter.IncompleteStreamError
	if errors.As(err, &incomplete) {
		return true
	}
	switch warmupHTTPStatus(err) {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unexpected eof", "connection reset", "connection refused",
		"server closed idle connection", "use of closed network connection",
		"tls handshake timeout", "i/o timeout", "provider stream idle timeout",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (s *ProxyService) shouldWarmupModelAdapter(adapter serverconfig.ModelAdapterConfig) bool {
	if s == nil || strings.TrimSpace(adapter.ProviderID) == "" {
		return false
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return false
	}
	provider, ok := serverconfig.FindProvider(cfg.Providers, adapter.ProviderID)
	return ok && provider.WarmupEnabled
}

func (s *ProxyService) beginWarmup(adapterID string) (context.Context, context.CancelFunc, bool) {
	s.warmupMu.Lock()
	defer s.warmupMu.Unlock()
	if s.warmupCancels == nil {
		s.warmupCancels = make(map[string]context.CancelFunc)
	}
	if _, exists := s.warmupCancels[adapterID]; exists || s.warmupActive >= maxConcurrentWarmups {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.warmupCancels[adapterID] = cancel
	s.warmupActive++
	return ctx, cancel, true
}

func (s *ProxyService) finishWarmup(adapterID string, cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
	s.warmupMu.Lock()
	defer s.warmupMu.Unlock()
	delete(s.warmupCancels, adapterID)
	if s.warmupActive > 0 {
		s.warmupActive--
	}
}

func (s *ProxyService) cancelAllWarmups() {
	if s == nil {
		return
	}
	s.warmupMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.warmupCancels))
	for _, cancel := range s.warmupCancels {
		cancels = append(cancels, cancel)
	}
	s.warmupMu.Unlock()
	for _, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
	}
}

// CancelModelAdapterTest reliably stops a manual queue/connectivity task.
func (s *ProxyService) CancelModelAdapterTest(adapterID string) (ModelAdapterTestResult, error) {
	id := strings.TrimSpace(adapterID)
	s.warmupMu.Lock()
	cancel := s.warmupCancels[id]
	s.warmupMu.Unlock()
	if cancel == nil {
		return ModelAdapterTestResult{}, errors.New("该模型当前没有可取消的排队检测")
	}
	cancel()
	if result, ok := s.getModelAdapterTestResult(id); ok {
		result.Status = string(ModelAdapterTestStatusCanceled)
		result.SummaryText = "排队检测已取消"
		result.Error = ""
		result.WarmupCancelable = false
		result.WarmupWaiting = false
		result.WarmupNextRetryMS = 0
		s.storeAndEmitModelAdapterTestResult(result)
		return result, nil
	}
	return ModelAdapterTestResult{AdapterID: id, Status: string(ModelAdapterTestStatusCanceled)}, nil
}

func (s *ProxyService) getModelAdapterTestResult(adapterID string) (ModelAdapterTestResult, bool) {
	s.modelTestMu.RLock()
	defer s.modelTestMu.RUnlock()
	result, ok := s.modelTestResults[strings.TrimSpace(adapterID)]
	return result, ok
}

func (s *ProxyService) warmupModelAdapter(
	parent context.Context,
	adapter serverconfig.ModelAdapterConfig,
	requestHash string,
) (ModelAdapterTestResult, error) {
	warmupCtx, cancel, ok := s.beginWarmup(adapter.ID)
	if !ok {
		result := buildErroredModelAdapterTestResult(adapter.ID, requestHash, errWarmupBusy)
		result.TestKind = "connectivity"
		return result, errWarmupBusy
	}
	defer s.finishWarmup(adapter.ID, cancel)

	ctx, stop := mergeWarmupContexts(parent, warmupCtx)
	defer stop()
	startedAt := time.Now().UTC()
	transientFailures := 0
	var lastResult ModelAdapterTestResult
	var lastErr error
	s.storeAndEmitModelAdapterTestResult(ModelAdapterTestResult{
		AdapterID:        strings.TrimSpace(adapter.ID),
		RequestHash:      strings.TrimSpace(requestHash),
		Status:           string(ModelAdapterTestStatusRunning),
		SummaryText:      "连通性检测中...",
		TestedAt:         startedAt.Format(time.RFC3339Nano),
		WarmupAttempt:    1,
		WarmupCancelable: true,
		TestKind:         "connectivity",
	})

	for attempt := 1; ; attempt++ {
		result, err := s.runModelAdapterConnectivityTest(ctx, adapter, requestHash, startedAt)
		result.WarmupAttempt = attempt
		result.WarmupElapsedMS = maxDurationMS(time.Since(startedAt))
		result.TestKind = "connectivity"
		if err == nil {
			result.WarmupCancelable = false
			result.WarmupWaiting = false
			result.SummaryText = fmt.Sprintf("可用 | 排队 %d 次 | 首字 %s", attempt-1, formatModelAdapterTestDuration(result.FirstTextTokenMS))
			s.logWarmupDiagnostic(adapter, attempt, result, 0, "first_text")
			return result, nil
		}
		lastResult, lastErr = result, err
		if errors.Is(ctx.Err(), context.Canceled) {
			canceled := buildCanceledWarmupResult(adapter.ID, requestHash, attempt, startedAt)
			s.storeAndEmitModelAdapterTestResult(canceled)
			return canceled, context.Canceled
		}

		queueWait := isQueueRejectionError(err)
		statusCode := warmupHTTPStatus(err)
		if !queueWait {
			if !isTransientWarmupError(err) {
				s.logWarmupDiagnostic(adapter, attempt, lastResult, statusCode, "terminal_error")
				return lastResult, lastErr
			}
			transientFailures++
			if transientFailures >= warmupMaxTransientFailures {
				lastResult.SummaryText = fmt.Sprintf("连续 %d 次临时故障，已停止排队检测", transientFailures)
				lastResult.Error = lastResult.SummaryText
				s.logWarmupDiagnostic(adapter, attempt, lastResult, statusCode, "transient_limit")
				return lastResult, lastErr
			}
		}

		delay := warmupRetryDelay(attempt, err)
		waiting := lastResult
		waiting.Status = string(ModelAdapterTestStatusRunning)
		waiting.Availability = "unavailable"
		waiting.WarmupAttempt = attempt
		waiting.WarmupWaiting = true
		waiting.WarmupCancelable = true
		waiting.WarmupElapsedMS = maxDurationMS(time.Since(startedAt))
		waiting.WarmupNextRetryMS = maxDurationMS(delay)
		waiting.TestKind = "connectivity"
		waiting.Error = ""
		waiting.SummaryText = fmt.Sprintf("排队中（第 %d 次）…", attempt)
		s.storeAndEmitModelAdapterTestResult(waiting)
		if err := s.waitWarmupRetry(ctx, waiting, startedAt, delay); err != nil {
			canceled := buildCanceledWarmupResult(adapter.ID, requestHash, attempt, startedAt)
			s.storeAndEmitModelAdapterTestResult(canceled)
			return canceled, err
		}
	}
}

func (s *ProxyService) logWarmupDiagnostic(adapter serverconfig.ModelAdapterConfig, attempt int, result ModelAdapterTestResult, statusCode int, outcome string) {
	if s == nil || !s.isDetailedFileLoggingEnabled() {
		return
	}
	logger.Infof(
		"model connectivity adapter_id=%s provider_id=%s client_profile=%s wire_model=%s attempt=%d elapsed_ms=%d first_text_ms=%d http_status=%d outcome=%s",
		strings.TrimSpace(adapter.ID), strings.TrimSpace(adapter.ProviderID), strings.TrimSpace(adapter.ClientProfile),
		strings.TrimSuffix(strings.TrimSpace(adapter.ModelID), "[1m]"), attempt, result.WarmupElapsedMS,
		result.FirstTextTokenMS, statusCode, strings.TrimSpace(outcome),
	)
}

func mergeWarmupContexts(parent context.Context, task context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(task)
	if parent == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(parent, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (s *ProxyService) runModelAdapterConnectivityTest(
	parent context.Context,
	adapter serverconfig.ModelAdapterConfig,
	requestHash string,
	_ time.Time,
) (ModelAdapterTestResult, error) {
	ctx, cancel := context.WithTimeout(parent, modelAdapterTestTimeout)
	defer cancel()
	attemptStartedAt := time.Now().UTC()
	metrics, err := s.executeModelAdapterNonStreamingTest(ctx, adapter, modelAdapterTestOptions{
		prompt:             modelAdapterConnectivityPrompt,
		maxTokens:          modelAdapterConnectivityMaxTokens,
		stopAtFirstText:    true,
		requestMaxAttempts: 1,
	})
	if err != nil {
		return buildErroredModelAdapterTestResult(adapter.ID, requestHash, err), err
	}
	if metrics == nil || metrics.firstTextTokenAt.IsZero() || strings.TrimSpace(metrics.text.String()) == "" {
		emptyErr := errors.New(modelAdapterTestEmptyTextError)
		return buildErroredModelAdapterTestResult(adapter.ID, requestHash, emptyErr), emptyErr
	}
	result := buildSuccessfulModelAdapterTestResult(adapter.ID, requestHash, attemptStartedAt, metrics, false, "连通性检测收到有效文本后已停止")
	result.TestKind = "connectivity"
	return result, nil
}

func (s *ProxyService) waitWarmupRetry(ctx context.Context, waiting ModelAdapterTestResult, startedAt time.Time, delay time.Duration) error {
	deadline := time.Now().Add(delay)
	timer := time.NewTimer(delay)
	ticker := time.NewTicker(warmupProgressInterval)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-ticker.C:
			waiting.WarmupElapsedMS = maxDurationMS(time.Since(startedAt))
			waiting.WarmupNextRetryMS = maxDurationMS(time.Until(deadline))
			s.storeAndEmitModelAdapterTestResult(waiting)
		}
	}
}

func warmupRetryDelay(attempt int, err error) time.Duration {
	delay := warmupBaseDelay
	for index := 1; index < attempt && delay < warmupMaxDelay; index++ {
		delay *= 2
		if delay > warmupMaxDelay {
			delay = warmupMaxDelay
		}
	}
	delay = time.Duration(float64(delay) * warmupJitter())
	if retryAfter := warmupRetryAfter(err); retryAfter > delay {
		delay = retryAfter
	}
	return delay
}

func warmupRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}
	match := warmupRetryPattern.FindStringSubmatch(err.Error())
	if len(match) != 2 {
		return 0
	}
	value := strings.Trim(strings.TrimSpace(match[1]), `"`)
	if seconds, parseErr := strconv.Atoi(value); parseErr == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if duration, parseErr := time.ParseDuration(value); parseErr == nil && duration >= 0 {
		return duration
	}
	if retryAt, parseErr := http.ParseTime(value); parseErr == nil {
		return maxDuration(time.Until(retryAt), 0)
	}
	return 0
}

func buildCanceledWarmupResult(adapterID string, requestHash string, attempts int, startedAt time.Time) ModelAdapterTestResult {
	return ModelAdapterTestResult{
		AdapterID:         strings.TrimSpace(adapterID),
		RequestHash:       strings.TrimSpace(requestHash),
		Status:            string(ModelAdapterTestStatusCanceled),
		Availability:      "unavailable",
		SummaryText:       "排队检测已取消",
		TestedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		WarmupAttempt:     attempts,
		WarmupElapsedMS:   maxDurationMS(time.Since(startedAt)),
		WarmupCancelable:  false,
		WarmupWaiting:     false,
		WarmupNextRetryMS: 0,
		TestKind:          "connectivity",
	}
}

func maxDurationMS(value time.Duration) int64 {
	if value < 0 {
		return 0
	}
	return value.Milliseconds()
}

func maxDuration(value time.Duration, floor time.Duration) time.Duration {
	if value < floor {
		return floor
	}
	return value
}
