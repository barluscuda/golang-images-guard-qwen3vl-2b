package llamacpp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/core"
	"golang.org/x/image/webp"
)

func TestClientThinkingAndFailures(t *testing.T) {
	// Neither hosted-provider nor legacy model credentials may affect requests.
	t.Setenv("OPENAI_API_KEY", "must-not-be-sent")
	t.Setenv("GUARD_MODEL_API_KEY", "must-not-be-sent")
	policy, err := core.NewPolicy("RULE SEXUAL_CONTENT | Sexual Content")
	if err != nil {
		t.Fatal(err)
	}
	imageData, err := os.ReadFile("../http/testdata/landscape.webp")
	if err != nil {
		t.Fatal(err)
	}
	original, err := webp.Decode(bytes.NewReader(imageData))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, finish, content string
		status, oversized     int
		wantErr               error
	}{
		{"llamacpp-thinking", "stop", `{"violation":false,"severity":0,"rule":null,"reason":"Safe."}`, 200, 0, nil},
		{"truncated", "length", `{"violation":false,"severity":0,"rule":null,"reason":"Safe."}`, 200, 0, core.ErrInvalidAssessment},
		{"mixed-thinking", "stop", `<think>private</think>{"violation":false,"severity":0,"rule":null,"reason":"Safe."}`, 200, 0, core.ErrInvalidAssessment},
		{"malformed-content", "stop", `not JSON`, 200, 0, core.ErrInvalidAssessment},
		{"bounded-chunked-response", "stop", "", 200, 1, core.ErrModelUnavailable},
		{"bounded-content-length", "stop", "", 200, 2, core.ErrModelUnavailable},
		{"server-unavailable", "", "private request data", 503, 0, core.ErrModelUnavailable},
		{"redirect", "", "", 307, 0, core.ErrModelUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("incorrect HTTP request")
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" {
					t.Error("request contains authentication")
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				format := request["response_format"].(map[string]any)
				if format["type"] != "json_schema" || format["json_schema"].(map[string]any)["schema"] == nil {
					t.Error("missing schema response format")
				}
				if request["chat_template_kwargs"].(map[string]any)["enable_thinking"] != true || request["reasoning_format"] != "deepseek" {
					t.Error("thinking output not separated")
				}
				if request["model"] != "thinking-model" || request["max_tokens"] != float64(8192) || request["top_k"] != float64(20) || request["min_p"] != float64(0) || request["stream"] != false {
					t.Error("incorrect inference parameters")
				}
				messages := request["messages"].([]any)
				if !strings.Contains(messages[0].(map[string]any)["content"].(string), policy.Text()) {
					t.Error("missing policy")
				}
				parts := messages[1].(map[string]any)["content"].([]any)
				url := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
				if !strings.HasPrefix(url, "data:image/png;base64,") {
					t.Error("image is not PNG")
					return
				}
				data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/png;base64,"))
				if err != nil {
					t.Error(err)
					return
				}
				converted, err := png.Decode(bytes.NewReader(data))
				if err != nil {
					t.Error(err)
					return
				}
				if converted.Bounds() != original.Bounds() {
					t.Error("image dimensions changed")
				}
				for y := original.Bounds().Min.Y; y < original.Bounds().Max.Y; y++ {
					for x := original.Bounds().Min.X; x < original.Bounds().Max.X; x++ {
						r1, g1, b1, a1 := original.At(x, y).RGBA()
						r2, g2, b2, a2 := converted.At(x, y).RGBA()
						if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
							t.Error("image pixels changed")
							return
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.status == 307 {
					w.Header().Set("Location", "/must-not-follow")
				}
				w.WriteHeader(tc.status)
				if tc.oversized != 0 {
					if tc.oversized == 1 {
						w.(http.Flusher).Flush()
					}
					_, _ = io.WriteString(w, strings.Repeat(" ", 2048))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
					"index": 0, "finish_reason": tc.finish, "message": map[string]any{
						"role": "assistant", "content": tc.content, "reasoning_content": "private thinking",
					},
				}}})
			}))
			defer stub.Close()
			client := New(Options{BaseURL: stub.URL + "/v1/",
				Timeout: time.Second, MaxTokens: 8192, MaxResponseBytes: 1024, Temperature: 0.6, TopP: 0.95, Concurrency: 1})
			defer client.Close()
			result, err := client.Assess(context.Background(), core.Image{Data: imageData}, policy, "thinking-model")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v want %v", err, tc.wantErr)
			}
			if err == nil && result.Reason != "Safe." {
				t.Fatal("unexpected assessment")
			}
		})
	}
}

func TestClientCanceledContext(t *testing.T) {
	client := New(Options{Timeout: time.Second, MaxResponseBytes: 1024, Concurrency: 1})
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Assess(ctx, core.Image{}, core.Policy{}, "thinking"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v want cancellation", err)
	}
}
