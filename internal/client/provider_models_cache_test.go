package client

import (
	"path/filepath"
	"testing"

	serverconfig "cursor/internal/backend/server/config"
)

func newTestProvider() serverconfig.ProviderConfig {
	return serverconfig.ProviderConfig{
		ID:      "p1",
		Name:    "Test Station",
		Type:    "openai",
		BaseURL: "https://example.test",
		APIKey:  "sk-original",
	}
}

func TestProviderModelsRequestHashCoversConnectionFields(t *testing.T) {
	base := newTestProvider()
	baseHash := buildProviderModelsRequestHash(base)

	// provider.ID 只哈希 baseURL 与名称，所以改密钥时 ID 不变。
	// 缓存键必须自己覆盖密钥，否则会命中旧密钥的结果。
	changedKey := base
	changedKey.APIKey = "sk-rotated"
	if buildProviderModelsRequestHash(changedKey) == baseHash {
		t.Fatal("改 apiKey 之后请求哈希没有变化，缓存会命中旧密钥的结果")
	}

	for name, mutate := range map[string]func(*serverconfig.ProviderConfig){
		"type":          func(p *serverconfig.ProviderConfig) { p.Type = "anthropic" },
		"baseURL":       func(p *serverconfig.ProviderConfig) { p.BaseURL = "https://other.test" },
		"userAgent":     func(p *serverconfig.ProviderConfig) { p.UserAgent = "claude-cli/1.0.0" },
		"headersJSON":   func(p *serverconfig.ProviderConfig) { p.HeadersJSON = `{"X-A":"1"}` },
		"modelsPath":    func(p *serverconfig.ProviderConfig) { p.ModelsPath = "/v1/models" },
		"inferencePath": func(p *serverconfig.ProviderConfig) { p.InferencePath = "/v1/messages" },
	} {
		mutated := base
		mutate(&mutated)
		if buildProviderModelsRequestHash(mutated) == baseHash {
			t.Errorf("改 %s 之后请求哈希应该变化", name)
		}
	}

	// 纯展示字段不参与请求，改了不该让缓存失效。
	for name, mutate := range map[string]func(*serverconfig.ProviderConfig){
		"name":    func(p *serverconfig.ProviderConfig) { p.Name = "Renamed" },
		"note":    func(p *serverconfig.ProviderConfig) { p.Note = "some note" },
		"builtin": func(p *serverconfig.ProviderConfig) { p.Builtin = true },
		"homeURL": func(p *serverconfig.ProviderConfig) { p.HomeURL = "https://home.test" },
		"id":      func(p *serverconfig.ProviderConfig) { p.ID = "p2" },
	} {
		mutated := base
		mutate(&mutated)
		if buildProviderModelsRequestHash(mutated) != baseHash {
			t.Errorf("改 %s 不应该让请求哈希变化", name)
		}
	}
}

func TestProviderModelsCacheRoundTripAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "provider-models.json")
	cache := newProviderModelsCache(path)

	success := ProviderModelsResult{
		ProviderID:  "p1",
		OK:          true,
		RequestHash: "hash-ok",
		Models:      []ProviderModel{{ID: "gpt-5", Name: "gpt-5"}},
	}
	if err := cache.Put(success); err != nil {
		t.Fatalf("Put 成功结果失败: %v", err)
	}
	if got, ok := cache.Get("hash-ok"); !ok || len(got.Models) != 1 {
		t.Fatalf("成功结果应该能取回，got=%+v ok=%v", got, ok)
	}

	// 失败结果不入缓存：一次网络抖动不应该让用户在修好配置之后仍看到旧错误。
	if err := cache.Put(ProviderModelsResult{ProviderID: "p1", OK: false, RequestHash: "hash-fail", Error: "boom"}); err != nil {
		t.Fatalf("Put 失败结果不应报错: %v", err)
	}
	if _, ok := cache.Get("hash-fail"); ok {
		t.Fatal("失败结果不应该被缓存")
	}

	// 重开一个实例读同一个文件，验证落盘生效（重启应用后缓存仍在）。
	reopened := newProviderModelsCache(path)
	if got, ok := reopened.Get("hash-ok"); !ok || got.Models[0].ID != "gpt-5" {
		t.Fatalf("落盘的缓存应该能被新实例读回，got=%+v ok=%v", got, ok)
	}
	if len(reopened.Snapshot()) != 1 {
		t.Fatalf("快照应该只有 1 条，got=%d", len(reopened.Snapshot()))
	}
}

// 拉取成功后调用方会把探测到的路径物化回配置，而路径参与哈希；
// 若只按本次请求的哈希存一份，第二次带着物化后的配置进来就必然 miss。
func TestProviderModelsCacheReachableAfterPathMaterialization(t *testing.T) {
	requested := newTestProvider()
	requestHash := buildProviderModelsRequestHash(requested)

	result := ProviderModelsResult{
		ProviderID:    requested.ID,
		OK:            true,
		RequestHash:   requestHash,
		ResolvedPath:  "/v1/models",
		InferencePath: "/v1/chat/completions",
		Models:        []ProviderModel{{ID: "gpt-5", Name: "gpt-5"}},
	}

	service := &ProxyService{
		providerModels: newProviderModelsCache(filepath.Join(t.TempDir(), "provider-models.json")),
	}
	service.storeAndEmitProviderModels(requested, result)

	// 模拟前端物化：把探测出的路径写回 provider，再按归一化后的配置查缓存。
	materialized := requested
	materialized.ModelsPath = result.ResolvedPath
	materialized.InferencePath = result.InferencePath
	normalized, err := serverconfig.NormalizeProviderConfig(materialized)
	if err != nil {
		t.Fatalf("归一化物化后的 provider 失败: %v", err)
	}

	materializedHash := buildProviderModelsRequestHash(normalized)
	if materializedHash == requestHash {
		t.Skip("物化没有改变哈希，本用例不适用")
	}
	if _, ok := service.providerModels.Get(materializedHash); !ok {
		t.Fatal("物化路径之后缓存应该仍然命中，否则第二次拉取永远 miss")
	}
	if _, ok := service.providerModels.Get(requestHash); !ok {
		t.Fatal("本次请求用的哈希也应该保留")
	}
}
