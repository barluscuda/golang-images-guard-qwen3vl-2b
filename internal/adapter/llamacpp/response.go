package llamacpp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/domain"
)

// ParseAssessment rejects missing/null fields, duplicate keys, and trailing content.
func ParseAssessment(content string) (domain.Assessment, error) {
	data := []byte(content)
	if err := uniqueKeys(data); err != nil {
		return domain.Assessment{}, domain.ErrInvalidAssessment
	}
	if !exactKeys(data, "violation", "severity", "rule", "reason") {
		return domain.Assessment{}, domain.ErrInvalidAssessment
	}
	var raw struct {
		Violation *bool           `json:"violation"`
		Severity  *int            `json:"severity"`
		Rule      json.RawMessage `json:"rule"`
		Reason    *string         `json:"reason"`
	}
	if err := strictDecode(data, &raw); err != nil || raw.Violation == nil || raw.Severity == nil || raw.Reason == nil || len(raw.Rule) == 0 {
		return domain.Assessment{}, domain.ErrInvalidAssessment
	}
	a := domain.Assessment{Violation: *raw.Violation, Severity: *raw.Severity, Reason: *raw.Reason}
	if !bytes.Equal(bytes.TrimSpace(raw.Rule), []byte("null")) {
		if !exactKeys(raw.Rule, "id", "name") {
			return domain.Assessment{}, domain.ErrInvalidAssessment
		}
		var rule struct {
			ID   *string `json:"id"`
			Name *string `json:"name"`
		}
		if err := strictDecode(raw.Rule, &rule); err != nil || rule.ID == nil || rule.Name == nil {
			return domain.Assessment{}, domain.ErrInvalidAssessment
		}
		a.Rule = &domain.Rule{ID: *rule.ID, Name: *rule.Name}
	}
	return a, nil
}

func exactKeys(data []byte, keys ...string) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func strictDecode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing content")
	}
	return nil
}

func uniqueKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSON(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing content")
	}
	return nil
}

func walkJSON(d *json.Decoder, depth int) error {
	if depth > 8 {
		return fmt.Errorf("JSON too deep")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate or invalid key")
			}
			seen[name] = true
			if err := walkJSON(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid object")
		}
	case json.Delim('['):
		for d.More() {
			if err := walkJSON(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid array")
		}
	case json.Delim('}'), json.Delim(']'):
		return fmt.Errorf("unexpected delimiter")
	}
	return nil
}
