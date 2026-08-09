package execbridge

import (
	"bytes"
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

// 图片要原样保留给投影阶段，非图片的大二进制仍按原有上限截断。
func TestConvertReadResultPreservesLargeImagesOnly(t *testing.T) {
	largePNG := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, readReplayBinaryLimit)...)
	imageResult := convertReadResultToReadToolResult(&agentv1.ReadResult{
		Result: &agentv1.ReadResult_Success{
			Success: &agentv1.ReadSuccess{
				Path:   "image.png",
				Output: &agentv1.ReadSuccess_Data{Data: largePNG},
			},
		},
	})
	imageSuccess := imageResult.GetSuccess()
	if imageSuccess == nil {
		t.Fatal("large image result is not successful")
	}
	if !bytes.Equal(imageSuccess.GetData(), largePNG) {
		t.Fatalf("large image data bytes = %d, want %d", len(imageSuccess.GetData()), len(largePNG))
	}
	if imageSuccess.GetExceededLimit() {
		t.Fatal("large image unexpectedly marked as exceeded limit")
	}

	largeBinary := bytes.Repeat([]byte{0xff}, readReplayBinaryLimit+1)
	binaryResult := convertReadResultToReadToolResult(&agentv1.ReadResult{
		Result: &agentv1.ReadResult_Success{
			Success: &agentv1.ReadSuccess{
				Path:   "archive.bin",
				Output: &agentv1.ReadSuccess_Data{Data: largeBinary},
			},
		},
	})
	binarySuccess := binaryResult.GetSuccess()
	if binarySuccess == nil {
		t.Fatal("large binary result is not successful")
	}
	if !binarySuccess.GetExceededLimit() {
		t.Fatal("large non-image binary was not marked as exceeded limit")
	}
	if binarySuccess.GetData() != nil {
		t.Fatal("large non-image binary data was retained")
	}
	if !strings.Contains(binarySuccess.GetContent(), "Read binary data") {
		t.Fatalf("large binary fallback = %q", binarySuccess.GetContent())
	}
}

func TestSupportedReadImageMIMEType(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "png", data: []byte("\x89PNG\r\n\x1a\npayload"), want: "image/png"},
		{name: "jpeg", data: []byte("\xff\xd8\xff\xe0payload"), want: "image/jpeg"},
		{name: "gif", data: []byte("GIF89apayload"), want: "image/gif"},
		{name: "empty", data: nil, want: ""},
		{name: "plain binary", data: bytes.Repeat([]byte{0xfe}, 32), want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SupportedReadImageMIMEType(test.data); got != test.want {
				t.Fatalf("SupportedReadImageMIMEType() = %q, want %q", got, test.want)
			}
		})
	}
}