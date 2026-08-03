package modeladapter

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPStatusErrorCarriesSafeRetryAfter(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"3"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"busy"}`)),
	}
	err := buildHTTPStatusError("adapter", response)
	if err == nil || !strings.Contains(err.Error(), "status=429") || !strings.Contains(err.Error(), "retry_after=3") {
		t.Fatalf("error = %v", err)
	}
}
