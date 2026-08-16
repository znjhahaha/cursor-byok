package modeladapter

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"
)

// Read 工具读到的图片必须以各协议自己的标准图片块送出，而不是一坨 base64 文本。
func TestToolImageProviderEncodings(t *testing.T) {
	message := toolImageMessageForTest()

	t.Run("openai_chat", func(t *testing.T) {
		items, err := normalizeOpenAIProviderMessages([]Message{message}, false)
		if err != nil {
			t.Fatalf("normalizeOpenAIProviderMessages() error = %v", err)
		}
		if len(items) != 1 || items[0]["role"] != "tool" || items[0]["tool_call_id"] != "call-1" {
			t.Fatalf("openai chat tool message = %#v", items)
		}
		content, ok := items[0]["content"].([]map[string]any)
		if !ok || len(content) != 2 {
			t.Fatalf("openai chat content = %#v", items[0]["content"])
		}
		imageURL, ok := content[1]["image_url"].(map[string]any)
		if content[1]["type"] != "image_url" || !ok {
			t.Fatalf("openai chat image part = %#v", content[1])
		}
		url, ok := imageURL["url"].(string)
		if !ok || !strings.HasPrefix(url, "data:image/png;base64,") {
			t.Fatalf("openai chat image url = %#v", imageURL["url"])
		}
	})

	t.Run("openai_responses", func(t *testing.T) {
		_, items, err := normalizeOpenAIResponsesInput([]Message{message})
		if err != nil {
			t.Fatalf("normalizeOpenAIResponsesInput() error = %v", err)
		}
		// 孤儿 tool 结果（无前置 assistant 调用）会补一个占位 function_call，
		// 保证每个 function_call_output 都有配对调用。
		if len(items) != 2 || items[0]["type"] != "function_call" || items[0]["call_id"] != "call-1" || items[1]["type"] != "function_call_output" {
			t.Fatalf("openai responses items = %#v", items)
		}
		content, ok := items[1]["output"].([]map[string]any)
		if !ok || len(content) != 2 {
			t.Fatalf("openai responses output = %#v", items[1]["output"])
		}
		if content[0]["type"] != "input_text" || content[1]["type"] != "input_image" {
			t.Fatalf("openai responses content = %#v", content)
		}
		url, ok := content[1]["image_url"].(string)
		if !ok || !strings.HasPrefix(url, "data:image/png;base64,") {
			t.Fatalf("openai responses image url = %#v", content[1]["image_url"])
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		_, messages, err := normalizeAnthropicProviderMessages([]Message{message}, false, false)
		if err != nil {
			t.Fatalf("normalizeAnthropicProviderMessages() error = %v", err)
		}
		if len(messages) != 1 || messages[0].Role != "user" || len(messages[0].Content) != 1 {
			t.Fatalf("anthropic messages = %#v", messages)
		}
		toolResult := messages[0].Content[0]
		if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call-1" {
			t.Fatalf("anthropic tool result = %#v", toolResult)
		}
		content, ok := toolResult["content"].([]map[string]any)
		if !ok || len(content) != 2 {
			t.Fatalf("anthropic tool content = %#v", toolResult["content"])
		}
		if content[0]["type"] != "text" || content[1]["type"] != "image" {
			t.Fatalf("anthropic content blocks = %#v", content)
		}
		source, ok := content[1]["source"].(map[string]any)
		if !ok || source["type"] != "base64" || source["media_type"] != "image/png" {
			t.Fatalf("anthropic image source = %#v", content[1]["source"])
		}
		// 带图片的 tool_result 依然要被认成可缓存块，否则 cache_control 会漏挂。
		if !isAnthropicCacheableBlock(toolResult) {
			t.Fatal("tool_result with image blocks is not cacheable")
		}
	})
}

// 没有图片时三种协议都应保持纯字符串形态，避免给只认字符串的端点添麻烦。
func TestToolResultWithoutImageStaysPlainText(t *testing.T) {
	message := Message{
		Role:       "tool",
		Content:    "plain tool output",
		ToolCallID: "call-1",
		Name:       "Read",
	}

	items, err := normalizeOpenAIProviderMessages([]Message{message}, false)
	if err != nil {
		t.Fatalf("normalizeOpenAIProviderMessages() error = %v", err)
	}
	if items[0]["content"] != "plain tool output" {
		t.Fatalf("openai chat content = %#v", items[0]["content"])
	}

	_, responsesItems, err := normalizeOpenAIResponsesInput([]Message{message})
	if err != nil {
		t.Fatalf("normalizeOpenAIResponsesInput() error = %v", err)
	}
	// 孤儿结果会补一个占位 function_call，输出本体在第二项且保持纯字符串。
	if len(responsesItems) != 2 || responsesItems[0]["type"] != "function_call" || responsesItems[1]["output"] != "plain tool output" {
		t.Fatalf("openai responses items = %#v", responsesItems)
	}

	_, messages, err := normalizeAnthropicProviderMessages([]Message{message}, false, false)
	if err != nil {
		t.Fatalf("normalizeAnthropicProviderMessages() error = %v", err)
	}
	if messages[0].Content[0]["content"] != "plain tool output" {
		t.Fatalf("anthropic tool content = %#v", messages[0].Content[0]["content"])
	}
}

// 图片字节必须原样抵达 wire format：三种协议各自编码后解码回来要与原图逐字节相同。
// 这条锁住的是数据完整性，任何一处截断或重编码都会让它失败。
func TestToolImageRoundTripPreservesBytes(t *testing.T) {
	original := realPNGForTest(t)
	message := Message{
		Role:       "tool",
		Content:    "read image",
		ToolCallID: "call-1",
		Name:       "Read",
		ContentParts: []ContentPart{
			{Type: "text", Text: "read image"},
			{Type: "image", Image: &ImageContent{MIMEType: "image/png", Path: "icon.png", Data: original}},
		},
	}

	t.Run("openai_chat", func(t *testing.T) {
		items, err := normalizeOpenAIProviderMessages([]Message{message}, false)
		if err != nil {
			t.Fatalf("normalizeOpenAIProviderMessages() error = %v", err)
		}
		content := items[0]["content"].([]map[string]any)
		url := content[1]["image_url"].(map[string]any)["url"].(string)
		assertDataURLRoundTrip(t, url, original)
	})

	t.Run("openai_responses", func(t *testing.T) {
		_, items, err := normalizeOpenAIResponsesInput([]Message{message})
		if err != nil {
			t.Fatalf("normalizeOpenAIResponsesInput() error = %v", err)
		}
		// 孤儿结果补位后输出在第二项。
		content := items[1]["output"].([]map[string]any)
		assertDataURLRoundTrip(t, content[1]["image_url"].(string), original)
	})

	t.Run("anthropic", func(t *testing.T) {
		_, messages, err := normalizeAnthropicProviderMessages([]Message{message}, false, false)
		if err != nil {
			t.Fatalf("normalizeAnthropicProviderMessages() error = %v", err)
		}
		content := messages[0].Content[0]["content"].([]map[string]any)
		source := content[1]["source"].(map[string]any)
		decoded, err := base64.StdEncoding.DecodeString(source["data"].(string))
		if err != nil {
			t.Fatalf("decode anthropic image data: %v", err)
		}
		if !bytes.Equal(decoded, original) {
			t.Fatalf("anthropic image bytes = %d, want %d", len(decoded), len(original))
		}
	})
}

func assertDataURLRoundTrip(t *testing.T, dataURL string, original []byte) {
	t.Helper()
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		t.Fatalf("data url prefix = %q", dataURL[:min(len(dataURL), len(prefix))])
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		t.Fatalf("decode data url: %v", err)
	}
	if !bytes.Equal(decoded, original) {
		t.Fatalf("decoded image bytes = %d, want %d", len(decoded), len(original))
	}
}

// realPNGForTest 生成一张真正可解码的 PNG，避免用伪造字节掩盖编码问题。
func realPNGForTest(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x * 32), G: uint8(y * 32), B: 128, A: 255})
		}
	}
	buffer := &bytes.Buffer{}
	if err := png.Encode(buffer, canvas); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	payload := buffer.Bytes()
	if http.DetectContentType(payload) != "image/png" {
		t.Fatalf("generated payload is not detected as png")
	}
	return payload
}

func toolImageMessageForTest() Message {
	return Message{
		Role:       "tool",
		Content:    "read image path=\"diagram.png\" mime=image/png bytes=16",
		ToolCallID: "call-1",
		Name:       "Read",
		ContentParts: []ContentPart{
			{Type: "text", Text: "read image path=\"diagram.png\" mime=image/png bytes=16"},
			{
				Type: "image",
				Image: &ImageContent{
					MIMEType: "image/png",
					Path:     "diagram.png",
					Data:     []byte("\x89PNG\r\n\x1a\nimage"),
				},
			},
		},
	}
}