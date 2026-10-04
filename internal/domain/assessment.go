package domain

import (
	"errors"
	"strings"
)

var ErrInvalidAssessment = errors.New("invalid assessment")

type Rule struct{ ID, Name string }

type Assessment struct {
	Violation bool
	Severity  int
	Rule      *Rule
	Reason    string
}

type Failure struct{ Code, Message string }

func ValidateAssessment(a Assessment, p Policy) error {
	if a.Severity < 0 || a.Severity > 10 || strings.TrimSpace(a.Reason) == "" || len(a.Reason) > 2048 {
		return ErrInvalidAssessment
	}
	if !a.Violation {
		if a.Severity != 0 || a.Rule != nil {
			return ErrInvalidAssessment
		}
		return nil
	}
	if a.Severity == 0 || a.Rule == nil {
		return ErrInvalidAssessment
	}
	name, ok := p.rules[a.Rule.ID]
	if !ok || name != a.Rule.Name {
		return ErrInvalidAssessment
	}
	return nil
}
