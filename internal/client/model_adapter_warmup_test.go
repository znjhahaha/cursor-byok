package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	serverconfig "cursor/internal/backend/server/config"
)

// realAnyRouterQueueBody 是用户实测捕获的排队报文，原样保留。
// 预热功能的整个触发条件都建立在这份报文上，任何改动都必须让这条用例继续通过。
const realAnyRouterQueueBody = `{"error":{"message":"当前模型 gpt-5.6-sol 负载已经达到上限，请稍后重试 (request id: 20260801103512345678)","type":"new_api_error","code":"get_channel_failed"}}`

func TestIsQueueRejectionErrorMatchesRealPayload(t *testing.T) {
	err := fmt.Errorf("openai adapter status=%d body=%s", 500, realAnyRouterQueueBody)
	if !isQueueRejectionError(err) {
		t.Fatalf("真实排队报文必须被识别为排队：%v", err)
	}
}

// 重试间隔是固定的：配多少就等多少，不随尝试次数增长。
func TestWarmupRetryDelayIsConstantAndHonoursRetryAfter(t *testing.T) {
	for _, attempt := range []int{1, 2, 3, 10} {
		if got := warmupRetryDelay(errors.New("status=429"), defaultWarmupRetryDelay); got != 500*time.Millisecond {
			t.Fatalf("attempt %d delay = %s, want 500ms", attempt, got)
		}
	}
	if got := warmupRetryDelay(errors.New("status=429"), 2*time.Second); got != 2*time.Second {
		t.Fatalf("configured 2s delay = %s", got)
	}
	// 缺省（0）必须落回默认间隔，而不是退化成 0 秒空转重试。
	if got := warmupRetryDelay(errors.New("status=429"), 0); got != defaultWarmupRetryDelay {
		t.Fatalf("zero delay = %s, want %s", got, defaultWarmupRetryDelay)
	}
	// Retry-After 是上游的明确指令，比本地配置更长时以它为准。
	if got := warmupRetryDelay(errors.New("status=429 retry_after=3"), defaultWarmupRetryDelay); got != 3*time.Second {
		t.Fatalf("Retry-After delay = %s", got)
	}
	if got := warmupRetryDelay(errors.New("status=429 retry_after=750ms"), defaultWarmupRetryDelay); got != 750*time.Millisecond {
		t.Fatalf("duration Retry-After delay = %s", got)
	}
	// 反过来，Retry-After 比配置短时不该让我们提前去撞墙。
	if got := warmupRetryDelay(errors.New("status=429 retry_after=0"), 2*time.Second); got != 2*time.Second {
		t.Fatalf("short Retry-After delay = %s, want 2s", got)
	}
}

func TestWarmupQueueThenFirstTextSuccessUsesRealWireBuilder(t *testing.T) {
	var attempts atomic.Int32
	var capturedModel atomic.Value
	var capturedUA atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempt := attempts.Add(1)
		capturedUA.Store(request.Header.Get("User-Agent"))
		body, _ := io.ReadAll(request.Body)
		text := string(body)
		if index := strings.Index(text, `"model":"`); index >= 0 {
			rest := text[index+len(`"model":"`):]
			if end := strings.Index(rest, `"`); end >= 0 {
				capturedModel.Store(rest[:end])
			}
		}
		if attempt <= 2 {
			writer.Header().Set("Retry-After", "0")
			http.Error(writer, `{"error":{"code":"get_channel_failed","message":"no available channel"}}`, http.StatusTooManyRequests)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\n")
	}))
	defer server.Close()

	service := &ProxyService{modelTestResults: make(map[string]ModelAdapterTestResult), warmupCancels: make(map[string]context.CancelFunc)}
	result, err := service.warmupModelAdapter(context.Background(), serverconfig.ModelAdapterConfig{
		ID: "anyrouter-model", Type: "anthropic", BaseURL: server.URL, APIKey: "secret", ModelID: "claude-opus-5[1m]",
		ClientProfile: "claude-code", Anthropic1MContextEnabled: true,
	}, "hash", warmupPlan{retryDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("warmup error = %v", err)
	}
	if result.Status != string(ModelAdapterTestStatusSuccess) || result.WarmupAttempt != 3 || result.FirstTextTokenMS < 0 {
		t.Fatalf("result = %+v", result)
	}
	if got, _ := capturedModel.Load().(string); got != "claude-opus-5" {
		t.Fatalf("wire model = %q", got)
	}
	if got, _ := capturedUA.Load().(string); !strings.Contains(got, "claude-cli/") {
		t.Fatalf("User-Agent = %q", got)
	}
}

func TestWarmupCanBeCanceledAndDuplicateTaskIsRejected(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		http.Error(writer, `{"error":{"message":"busy"}}`, http.StatusTooManyRequests)
	}))
	defer server.Close()

	service := &ProxyService{modelTestResults: make(map[string]ModelAdapterTestResult), warmupCancels: make(map[string]context.CancelFunc)}
	adapter := serverconfig.ModelAdapterConfig{ID: "queued-model", Type: "anthropic", BaseURL: server.URL, APIKey: "secret", ModelID: "claude-test"}
	done := make(chan ModelAdapterTestResult, 1)
	go func() {
		result, _ := service.warmupModelAdapter(context.Background(), adapter, "hash", warmupPlan{retryDelay: time.Millisecond})
		done <- result
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("warmup did not start")
	}
	if _, err := service.warmupModelAdapter(context.Background(), adapter, "hash", warmupPlan{retryDelay: time.Millisecond}); !errors.Is(err, errWarmupBusy) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := service.CancelModelAdapterTest(adapter.ID); err != nil {
		t.Fatalf("cancel error = %v", err)
	}
	select {
	case result := <-done:
		if result.Status != string(ModelAdapterTestStatusCanceled) || result.WarmupCancelable {
			t.Fatalf("canceled result = %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("warmup did not stop")
	}
}

func TestIsQueueRejectionErrorRejectsGenuineFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"没有状态码", errors.New("dial tcp: connection refused")},
		// 普通 500 不带排队特征：这是真实故障，重试只会掩盖问题。
		{"普通 500", fmt.Errorf(`openai adapter status=500 body={"error":{"message":"internal server error"}}`)},
		// 401 带排队特征串也不算：状态码说明是鉴权问题，等多久都不会好。
		{"401 带特征串", fmt.Errorf(`openai adapter status=401 body={"code":"get_channel_failed"}`)},
		{"404", fmt.Errorf(`openai adapter status=404 body={"error":{"message":"model not found"}}`)},
		{"502 无特征串", fmt.Errorf("openai adapter status=502 body=bad gateway")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isQueueRejectionError(tc.err) {
				t.Fatalf("不应识别为排队：%v", tc.err)
			}
		})
	}
}

func TestWarmupFailureClassificationStopsAuthAndRefusalButRetries503(t *testing.T) {
	for _, err := range []error{
		errors.New("anthropic adapter status=401 body=unauthorized"),
		errors.New("anthropic adapter status=403 body=forbidden"),
		&modeladapter.ProviderRefusalError{Provider: "anthropic", StopDetails: `{"type":"safety"}`},
	} {
		if isQueueRejectionError(err) || isTransientWarmupError(err) {
			t.Fatalf("terminal error was classified as retryable: %v", err)
		}
	}
	if !isTransientWarmupError(errors.New("anthropic adapter status=503 body=unavailable")) {
		t.Fatal("503 should receive limited transient retries")
	}
	if !isTransientWarmupError(&modeladapter.IncompleteStreamError{Provider: "anthropic"}) {
		t.Fatal("incomplete stream should receive limited transient retries")
	}
}

func TestIsQueueRejectionErrorMatchesOtherPanelWordings(t *testing.T) {
	cases := []string{
		`openai adapter status=500 body={"error":{"code":"get_channel_failed"}}`,
		`openai adapter status=429 body={"message":"当前分组上游负载已饱和"}`,
		`openai adapter status=500 body={"error":{"message":"no available channel"}}`,
		// 带 retry summary 的变体：buildHTTPStatusError 会在 status 与 body 之间插入摘要。
		`openai adapter status=500 attempts=3 body={"error":{"type":"new_api_error"}}`,
	}
	for _, message := range cases {
		if !isQueueRejectionError(errors.New(message)) {
			t.Fatalf("应识别为排队：%s", message)
		}
	}
}

func TestNormalizeProviderConfigClampsWarmupBudget(t *testing.T) {
	cases := []struct {
		name           string
		input          serverconfig.ProviderConfig
		wantMinutes    int
		wantIntervalMS int
	}{
		{
			name:           "未设预算时回填默认",
			input:          serverconfig.ProviderConfig{WarmupEnabled: true},
			wantMinutes:    serverconfig.DefaultWarmupMaxMinutes,
			wantIntervalMS: serverconfig.DefaultWarmupIntervalMS,
		},
		{
			// 低于 500ms 就不再是排队等待，而是对一个正在拒绝我们的上游加密探测。
			name:           "间隔过短被抬到下限",
			input:          serverconfig.ProviderConfig{WarmupEnabled: true, WarmupMaxMinutes: 5, WarmupIntervalMS: 50},
			wantMinutes:    5,
			wantIntervalMS: serverconfig.MinWarmupIntervalMS,
		},
		{
			name:           "时长过长被压到上限",
			input:          serverconfig.ProviderConfig{WarmupEnabled: true, WarmupMaxMinutes: 9999, WarmupIntervalMS: 999999},
			wantMinutes:    60,
			wantIntervalMS: serverconfig.MaxWarmupIntervalMS,
		},
		{
			name:           "负值按默认处理",
			input:          serverconfig.ProviderConfig{WarmupEnabled: true, WarmupMaxMinutes: -3, WarmupIntervalMS: -3},
			wantMinutes:    serverconfig.DefaultWarmupMaxMinutes,
			wantIntervalMS: serverconfig.DefaultWarmupIntervalMS,
		},
		{
			name:           "半秒是合法取值，不被抬高",
			input:          serverconfig.ProviderConfig{WarmupEnabled: true, WarmupIntervalMS: 500},
			wantMinutes:    serverconfig.DefaultWarmupMaxMinutes,
			wantIntervalMS: 500,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := tc.input
			provider.Name = "测试站"
			provider.Type = "openai"
			provider.BaseURL = "https://example.com"
			normalized, err := serverconfig.NormalizeProviderConfig(provider)
			if err != nil {
				t.Fatalf("归一化失败：%v", err)
			}
			if normalized.WarmupMaxMinutes != tc.wantMinutes {
				t.Fatalf("时长期望 %d，实际 %d", tc.wantMinutes, normalized.WarmupMaxMinutes)
			}
			if normalized.WarmupIntervalMS != tc.wantIntervalMS {
				t.Fatalf("间隔期望 %d，实际 %d", tc.wantIntervalMS, normalized.WarmupIntervalMS)
			}
		})
	}
}

// 关闭预热时不应残留预算值，否则配置文件里会出现一组不会生效的数字，误导排查。
func TestNormalizeProviderConfigDropsWarmupBudgetWhenDisabled(t *testing.T) {
	normalized, err := serverconfig.NormalizeProviderConfig(serverconfig.ProviderConfig{
		Name:             "测试站",
		Type:             "openai",
		BaseURL:          "https://example.com",
		WarmupEnabled:    false,
		WarmupMaxMinutes: 30,
		WarmupIntervalMS: 2000,
	})
	if err != nil {
		t.Fatalf("归一化失败：%v", err)
	}
	if normalized.WarmupEnabled {
		t.Fatal("预热应保持关闭")
	}
	if normalized.WarmupMaxMinutes != 0 || normalized.WarmupIntervalMS != 0 {
		t.Fatalf("关闭预热时预算应清零，实际 %d/%d",
			normalized.WarmupMaxMinutes, normalized.WarmupIntervalMS)
	}
}

func TestAcquireWarmupSlotLimitsConcurrency(t *testing.T) {
	service := &ProxyService{warmupCancels: make(map[string]context.CancelFunc)}
	type slot struct {
		id     string
		cancel context.CancelFunc
	}
	releases := make([]slot, 0, maxConcurrentWarmups)
	for i := 0; i < maxConcurrentWarmups; i++ {
		id := fmt.Sprintf("adapter-%d", i)
		_, cancel, ok := service.beginWarmup(id)
		if !ok {
			t.Fatalf("第 %d 个槽位应可用", i+1)
		}
		releases = append(releases, slot{id: id, cancel: cancel})
	}
	if _, _, ok := service.beginWarmup("overflow"); ok {
		t.Fatal("超出上限的预热请求应被拒绝")
	}
	service.finishWarmup(releases[0].id, releases[0].cancel)
	_, cancel, ok := service.beginWarmup("replacement")
	if !ok {
		t.Fatal("释放后应重新拿到槽位")
	}
	service.finishWarmup("replacement", cancel)
	for _, fn := range releases[1:] {
		service.finishWarmup(fn.id, fn.cancel)
	}
	if service.warmupActive != 0 {
		t.Fatalf("全部释放后计数应归零，实际 %d", service.warmupActive)
	}
}
