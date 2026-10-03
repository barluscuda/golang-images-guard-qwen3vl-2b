package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/core"
)

func TestClientThinkingAndFailures(t *testing.T) {
	policy, err := core.NewPolicy("RULE SEXUAL_CONTENT | Sexual Content")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, backend, format, finish, content string
		oversized                              bool
		wantErr                                error
	}{
		{"vllm-thinking", "vllm", "json_schema", "stop", `{"violation":false,"severity":0,"rule":null,"reason":"Safe."}`, false, nil},
		{"ollama-thinking", "ollama", "json_object", "stop", `{"violation":false,"severity":0,"rule":null,"reason":"Safe."}`, false, nil},
		{"truncated", "vllm", "json_schema", "length", `{"violation":false,"severity":0,"rule":null,"reason":"Safe."}`, false, core.ErrInvalidAssessment},
		{"mixed-thinking", "vllm", "json_schema", "stop", `<think>private</think>{"violation":false,"severity":0,"rule":null,"reason":"Safe."}`, false, core.ErrInvalidAssessment},
		{"bounded-response", "vllm", "json_schema", "stop", "", true, core.ErrModelUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if request["response_format"].(map[string]any)["type"] != tc.format {
					t.Error("wrong response format")
				}
				if tc.backend == "vllm" {
					if request["chat_template_kwargs"].(map[string]any)["enable_thinking"] != true {
						t.Error("thinking disabled")
					}
				} else if request["reasoning_effort"] != "high" {
					t.Error("thinking effort missing")
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.oversized {
					// Flush headers so the body has no Content-Length to reject up front.
					w.(http.Flusher).Flush()
					_, _ = io.WriteString(w, strings.Repeat(" ", 2048))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
					"index": 0, "finish_reason": tc.finish, "message": map[string]any{
						"role": "assistant", "content": tc.content, "reasoning": "private thinking",
					},
				}}})
			}))
			defer stub.Close()
			client := New(Options{BaseURL: stub.URL + "/v1", Backend: tc.backend, ResponseFormat: tc.format,
				Timeout: time.Second, MaxTokens: 8192, MaxResponseBytes: 1024, Temperature: 0.6, TopP: 0.95, Concurrency: 1})
			defer client.Close()
			_, err := client.Assess(context.Background(), core.Image{Data: []byte("image")}, policy, "thinking-model")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v want %v", err, tc.wantErr)
			}
		})
	}
}
