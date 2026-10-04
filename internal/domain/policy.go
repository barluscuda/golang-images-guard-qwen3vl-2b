package domain

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const MaxPolicyBytes = 64 << 10

var ruleIDPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// Policy is an immutable snapshot. Rule declarations use: RULE ID | Display Name.
type Policy struct {
	text, hash string
	rules      map[string]string
}

func NewPolicy(text string) (Policy, error) {
	if strings.TrimSpace(text) == "" || len(text) > MaxPolicyBytes {
		return Policy{}, fmt.Errorf("policy must contain 1–%d bytes", MaxPolicyBytes)
	}
	rules := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "RULE ") {
			continue
		}
		id, name, ok := strings.Cut(strings.TrimPrefix(line, "RULE "), "|")
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		if !ok || !ruleIDPattern.MatchString(id) || name == "" || len(name) > 128 {
			return Policy{}, fmt.Errorf("invalid rule declaration: %q", line)
		}
		if _, exists := rules[id]; exists {
			return Policy{}, fmt.Errorf("duplicate rule %s", id)
		}
		rules[id] = name
	}
	if len(rules) == 0 || len(rules) > 64 {
		return Policy{}, fmt.Errorf("policy must declare 1–64 rules")
	}
	return Policy{text: text, hash: fmt.Sprintf("%x", sha256.Sum256([]byte(text))), rules: rules}, nil
}

func (p Policy) Text() string { return p.text }
func (p Policy) Hash() string { return p.hash }
func (p Policy) Rules() []Rule {
	keys := make([]string, 0, len(p.rules))
	for id := range p.rules {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	rules := make([]Rule, 0, len(keys))
	for _, id := range keys {
		rules = append(rules, Rule{ID: id, Name: p.rules[id]})
	}
	return rules
}
