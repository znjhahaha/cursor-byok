package client

import (
	"errors"
	"fmt"
	"testing"

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

func TestIsQueueRejectionErrorMatchesOtherPanelWordings(t *testing.T) {
	cases := []string{
		`openai adapter status=503 body={"error":{"code":"get_channel_failed"}}`,
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
		name            string
		input           serverconfig.ProviderConfig
		wantMinutes     int
		wantIntervalSec int
	}{
		{
			name:            "未设预算时回填默认",
			input:           serverconfig.ProviderConfig{WarmupEnabled: true},
			wantMinutes:     serverconfig.DefaultWarmupMaxMinutes,
			wantIntervalSec: serverconfig.DefaultWarmupIntervalSeconds,
		},
		{
			// 1 秒间隔会把预热变成对上游的压测，必须抬到下限。
			name:            "间隔过短被抬到下限",
			input:           serverconfig.ProviderConfig{WarmupEnabled: true, WarmupMaxMinutes: 5, WarmupIntervalSeconds: 1},
			wantMinutes:     5,
			wantIntervalSec: 5,
		},
		{
			name:            "时长过长被压到上限",
			input:           serverconfig.ProviderConfig{WarmupEnabled: true, WarmupMaxMinutes: 9999, WarmupIntervalSeconds: 9999},
			wantMinutes:     60,
			wantIntervalSec: 300,
		},
		{
			name:            "负值按默认处理",
			input:           serverconfig.ProviderConfig{WarmupEnabled: true, WarmupMaxMinutes: -3, WarmupIntervalSeconds: -3},
			wantMinutes:     serverconfig.DefaultWarmupMaxMinutes,
			wantIntervalSec: serverconfig.DefaultWarmupIntervalSeconds,
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
			if normalized.WarmupIntervalSeconds != tc.wantIntervalSec {
				t.Fatalf("间隔期望 %d，实际 %d", tc.wantIntervalSec, normalized.WarmupIntervalSeconds)
			}
		})
	}
}

// 关闭预热时不应残留预算值，否则配置文件里会出现一组不会生效的数字，误导排查。
func TestNormalizeProviderConfigDropsWarmupBudgetWhenDisabled(t *testing.T) {
	normalized, err := serverconfig.NormalizeProviderConfig(serverconfig.ProviderConfig{
		Name:                  "测试站",
		Type:                  "openai",
		BaseURL:               "https://example.com",
		WarmupEnabled:         false,
		WarmupMaxMinutes:      30,
		WarmupIntervalSeconds: 20,
	})
	if err != nil {
		t.Fatalf("归一化失败：%v", err)
	}
	if normalized.WarmupEnabled {
		t.Fatal("预热应保持关闭")
	}
	if normalized.WarmupMaxMinutes != 0 || normalized.WarmupIntervalSeconds != 0 {
		t.Fatalf("关闭预热时预算应清零，实际 %d/%d",
			normalized.WarmupMaxMinutes, normalized.WarmupIntervalSeconds)
	}
}

func TestAcquireWarmupSlotLimitsConcurrency(t *testing.T) {
	service := &ProxyService{}
	releases := make([]func(), 0, maxConcurrentWarmups)
	for i := 0; i < maxConcurrentWarmups; i++ {
		release, ok := service.acquireWarmupSlot()
		if !ok {
			t.Fatalf("第 %d 个槽位应可用", i+1)
		}
		releases = append(releases, release)
	}
	if _, ok := service.acquireWarmupSlot(); ok {
		t.Fatal("超出上限的预热请求应被拒绝")
	}
	releases[0]()
	release, ok := service.acquireWarmupSlot()
	if !ok {
		t.Fatal("释放后应重新拿到槽位")
	}
	release()
	for _, fn := range releases[1:] {
		fn()
	}
	if service.warmupActive != 0 {
		t.Fatalf("全部释放后计数应归零，实际 %d", service.warmupActive)
	}
}