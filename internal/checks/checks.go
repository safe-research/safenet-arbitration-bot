// Package checks classifies Safenet requests with deterministic checks of the
// Safenet Arbitration Charter's rules.
package checks

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

// Verdict is how a request is classified.
type Verdict string

const (
	// Unclassified means that no check decides the request.
	Unclassified Verdict = ""
	Secure       Verdict = "secure"
	Insecure     Verdict = "insecure"
	OutOfScope   Verdict = "out-of-scope"
)

// String returns the verdict's name, which is "unclassified" for Unclassified.
func (v Verdict) String() string {
	if v == Unclassified {
		return "unclassified"
	}
	return string(v)
}

// MarshalJSON encodes Unclassified as null, and other verdicts as their names.
func (v Verdict) MarshalJSON() ([]byte, error) {
	if v == Unclassified {
		return []byte("null"), nil
	}
	return json.Marshal(string(v))
}

// Classification is a request's verdict, with the ID of the Charter rule that
// an insecure request breaks, and a short description of why the check reached
// its verdict for insecure and out-of-scope requests.
type Classification struct {
	Verdict     Verdict `json:"verdict"`
	Rule        string  `json:"rule,omitempty"`
	Description string  `json:"description,omitempty"`
}

// String returns the classification as a line of text: the verdict, followed by
// the rule and the description if present, separated by two spaces.
func (c Classification) String() string {
	fields := []string{c.Verdict.String()}
	for _, field := range []string{c.Rule, c.Description} {
		if field != "" {
			fields = append(fields, field)
		}
	}
	return strings.Join(fields, "  ")
}

// Check is a deterministic check of a request. It abstains by returning an
// Unclassified classification.
type Check func(ctx context.Context, request *safenet.Request) (Classification, error)

// checks are the checks that Classify runs, in order.
var checks []Check

// Classify runs the checks on request, and returns the classification of the
// first one that doesn't abstain. It returns an Unclassified classification if
// they all abstain.
func Classify(ctx context.Context, request *safenet.Request) (Classification, error) {
	return classify(ctx, checks, request)
}

func classify(ctx context.Context, checks []Check, request *safenet.Request) (Classification, error) {
	for _, check := range checks {
		c, err := check(ctx, request)
		if err != nil {
			return Classification{}, err
		}
		if c.Verdict != Unclassified {
			return c, nil
		}
	}
	return Classification{}, nil
}
