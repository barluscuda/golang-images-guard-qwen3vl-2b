package llamacpp

import (
	"testing"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/core"
)

func TestStrictAssessmentJSON(t *testing.T) {
	policy, err := core.NewPolicy("RULE SEXUAL_CONTENT | Sexual Content")
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		`{"violation":false,"severity":0,"rule":null,"reason":"Safe."}`,
		`{"violation":true,"severity":8,"rule":{"id":"SEXUAL_CONTENT","name":"Sexual Content"},"reason":"Explicit content."}`,
	} {
		a, err := ParseAssessment(content)
		if err != nil {
			t.Fatal(err)
		}
		if err := core.ValidateAssessment(a, policy); err != nil {
			t.Fatal(err)
		}
	}
	for _, content := range []string{
		`{}`, `null`, `[]`,
		`{"violation":false,"severity":0,"reason":"Safe."}`,
		`{"violation":null,"severity":0,"rule":null,"reason":"Safe."}`,
		`{"violation":false,"severity":0,"rule":null,"reason":null}`,
		`{"violation":false,"severity":0.5,"rule":null,"reason":"Safe."}`,
		`{"violation":false,"severity":0,"rule":null,"reason":"Safe.","extra":1}`,
		`{"violation":false,"Violation":true,"severity":0,"rule":null,"reason":"Safe."}`,
		`{"Violation":false,"severity":0,"rule":null,"reason":"Safe."}`,
		`{"violation":false,"violation":true,"severity":0,"rule":null,"reason":"Safe."}`,
		`{"violation":true,"severity":8,"rule":{"id":"A","id":"B","name":"X"},"reason":"X"}`,
		`{"violation":true,"severity":8,"rule":{"id":"A"},"reason":"X"}`,
		`{"violation":false,"severity":0,"rule":null,"reason":"Safe."} {}`,
		"<think>analysis</think>" + `{"violation":false,"severity":0,"rule":null,"reason":"Safe."}`,
		"```json\n{}\n```",
	} {
		if _, err := ParseAssessment(content); err == nil {
			t.Fatalf("accepted invalid JSON: %s", content)
		}
	}
}
