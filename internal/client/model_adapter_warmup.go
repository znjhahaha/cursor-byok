// model_adapter_warmup.go 实现中转站的「排队预热」：探测撞上上游的排队/满载响应时，
// 按站点配置的预算低频重试，直到排上队或预算耗尽。
//
// 只作用于探测路径。真实转发不走这里——转发端的重试策略在
// internal/backend/agent/model/retry.go，那份策略与 Cursor 的实时对话共用，
// 把它改成分钟级长重试会让真实会话直接卡死。
package client

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	serverconfig "cursor/internal/backend/server/config"
)

// maxConcurrentWarmups 限制同时运行的预热循环数。
//
// 预热是分钟级长驻循环，而前端「测试全部」的并发是 4；没有这道闸，
// 用户点几轮就会攒出一堆互相看不见的循环持续打同一个上游。
const maxConcurrentWarmups = 2

// errWarmupBusy 表示预热槽位已满。
var errWarmupBusy = errors.New("正在预热的模型过多，请等待其中一个结束")

// warmupStatusPattern 从错误串里取状态码。
// 格式由 modeladapter.buildHTTPStatusError 固定为 "%s status=%d ... body=%s"。
var warmupStatusPattern = regexp.MustCompile(`status=(\d{3})`)

// queueRejectionStatuses 是可能承载排队语义的状态码。
//
// 500 是这里的关键：New API 面板在没有可用渠道时回的就是 500，而通用重试策略
// 只认 429/502/503/504，所以排队错误在预热之前是直接失败的。
var queueRejectionStatuses = map[string]struct{}{
	"429": {},
	"500": {},
	"503": {},
}

// queueRejectionMarkers 是排队/满载响应体里的特征串。
//
// 这些是 New API / one-api 面板的通用格式，不是某一家中转站独有的，
// 所以识别只看报文、不看域名；是否启用由每站开关决定。
var queueRejectionMarkers = []string{
	"get_channel_failed",
	"new_api_error",
	"no available channel",
	"负载已经达到上限",
	"负载已达到上限",
	"当前分组上游负载",
	"当前分组负载已饱和",
}

// warmupBudget 描述一次预热的时间预算。
type warmupBudget struct {
	total    time.Duration
	interval time.Duration
}

// isQueueRejectionError 判断错误是否为「排队中/暂无可用渠道」。
//
// 必须同时满足状态码与报文特征：只看报文会把上游透传的其它 5xx 也吞掉，
// 只看状态码则会把真实的 500 故障拖进无限重试。
func isQueueRejectionError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	match := warmupStatusPattern.FindStringSubmatch(message)
	if match == nil {
		return false
	}
	if _, ok := queueRejectionStatuses[match[1]]; !ok {
		return false
	}
	lowered := strings.ToLower(message)
	for _, marker := range queueRejectionMarkers {
		if strings.Contains(lowered, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

// resolveWarmupBudget 查出该模型所属站点的预热配置。
// 站点不存在、未归属站点或未开启预热时返回 false。
func (s *ProxyService) resolveWarmupBudget(adapter serverconfig.ModelAdapterConfig) (warmupBudget, bool) {
	if s == nil {
		return warmupBudget{}, false
	}
	providerID := strings.TrimSpace(adapter.ProviderID)
	if providerID == "" {
		return warmupBudget{}, false
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return warmupBudget{}, false
	}
	provider, ok := serverconfig.FindProvider(cfg.Providers, providerID)
	if !ok || !provider.WarmupEnabled {
		return warmupBudget{}, false
	}
	// 走一遍归一化拿到钳制后的预算：配置文件可能是手写的，绕过了保存路径。
	normalized, normalizeErr := serverconfig.NormalizeProviderConfig(provider)
	if normalizeErr != nil {
		return warmupBudget{}, false
	}
	return warmupBudget{
		total:    time.Duration(normalized.WarmupMaxMinutes) * time.Minute,
		interval: time.Duration(normalized.WarmupIntervalSeconds) * time.Second,
	}, true
}

// acquireWarmupSlot 尝试占用一个预热槽位，返回释放函数。
func (s *ProxyService) acquireWarmupSlot() (func(), bool) {
	s.warmupMu.Lock()
	defer s.warmupMu.Unlock()
	if s.warmupActive >= maxConcurrentWarmups {
		return nil, false
	}
	s.warmupActive++
	return func() {
		s.warmupMu.Lock()
		defer s.warmupMu.Unlock()
		if s.warmupActive > 0 {
			s.warmupActive--
		}
	}, true
}

// warmupModelAdapter 反复探测直到排上队、预算耗尽或被取消。
//
// 循环体直接复用 runModelAdapterTest——它本身就是一次完整且无副作用的探测。
// 非排队错误立即终止：预热的意义是等队列，不是掩盖真实故障。
func (s *ProxyService) warmupModelAdapter(
	ctx context.Context,
	adapter serverconfig.ModelAdapterConfig,
	requestHash string,
	budget warmupBudget,
) (ModelAdapterTestResult, error) {
	release, ok := s.acquireWarmupSlot()
	if !ok {
		result := buildErroredModelAdapterTestResult(adapter.ID, requestHash, errWarmupBusy)
		return result, errWarmupBusy
	}
	defer release()

	deadlineCtx, cancel := context.WithTimeout(ctx, budget.total)
	defer cancel()

	var lastResult ModelAdapterTestResult
	var lastErr error
	for attempt := 1; ; attempt++ {
		// 每次尝试仍受 modelAdapterTestTimeout 约束，总预算由 deadlineCtx 兜住。
		result, err := s.runModelAdapterTest(deadlineCtx, adapter, requestHash)
		if err == nil {
			if attempt > 1 {
				result.WarmupAttempt = attempt
				result.Warning = strings.TrimSpace(fmt.Sprintf("%s 排队 %d 次后接通", result.Warning, attempt-1))
			}
			return result, nil
		}
		lastResult, lastErr = result, err

		if !isQueueRejectionError(err) {
			return lastResult, lastErr
		}
		// 预算已经耗尽时，deadlineCtx 会让下一次尝试立刻失败，
		// 那条错误没有诊断价值，所以在等待前先判定。
		if deadlineCtx.Err() != nil {
			break
		}

		waiting := lastResult
		waiting.Status = string(ModelAdapterTestStatusRunning)
		waiting.WarmupAttempt = attempt
		waiting.WarmupWaiting = true
		waiting.Error = ""
		waiting.SummaryText = fmt.Sprintf("排队中（第 %d 次）...", attempt)
		s.storeAndEmitModelAdapterTestResult(waiting)

		select {
		case <-deadlineCtx.Done():
			// 区分「总预算到点」与「用户主动取消」：后者不该报成预热超时。
			if ctx.Err() != nil {
				return lastResult, ctx.Err()
			}
			return buildWarmupExhaustedResult(lastResult, attempt, budget), lastErr
		case <-time.After(budget.interval):
		}
	}
	return buildWarmupExhaustedResult(lastResult, 0, budget), lastErr
}

// buildWarmupExhaustedResult 把最后一次排队失败标注成「预热用尽」。
// 保留原始报文，用户仍需要看到上游到底回了什么。
func buildWarmupExhaustedResult(last ModelAdapterTestResult, attempts int, budget warmupBudget) ModelAdapterTestResult {
	result := last
	result.Status = string(ModelAdapterTestStatusError)
	result.Availability = "unavailable"
	result.WarmupWaiting = false
	if attempts > 0 {
		result.WarmupAttempt = attempts
	}
	result.SummaryText = fmt.Sprintf("预热 %s 仍未排到队，已放弃", formatWarmupDuration(budget.total))
	if strings.TrimSpace(result.Error) == "" {
		result.Error = result.SummaryText
	}
	return result
}

func formatWarmupDuration(value time.Duration) string {
	minutes := int(value.Minutes())
	if minutes <= 0 {
		return fmt.Sprintf("%d 秒", int(value.Seconds()))
	}
	return fmt.Sprintf("%d 分钟", minutes)
}