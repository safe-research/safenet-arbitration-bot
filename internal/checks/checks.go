// Package checks classifies Safenet requests with deterministic checks of the
// Safenet Arbitration Charter's rules.
package checks

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
// an insecure request breaks, and a short description of the class of requests
// that gets the verdict.
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

// check is a deterministic check of a class of requests, which gets its
// classification: a verdict, the ID of the Charter rule that an insecure
// request breaks, and a short description of the class.
type check struct {
	verdict     Verdict
	rule        string
	description string
	// fn reports whether a request is in the check's class. The check abstains if
	// it isn't.
	fn func(ctx context.Context, request *safenet.Request) (bool, error)
}

func (c check) classification() Classification {
	return Classification{Verdict: c.verdict, Rule: c.rule, Description: c.description}
}

// checks are the checks that Classify runs.
var checks []check

// verdictOrder is the order in which Classify runs checks by verdict: a request
// that is out of scope gets no security ruling, and a request that fails a rule
// is insecure, whatever else holds for it (Charter § 3.7 and § 3.9).
var verdictOrder = []Verdict{OutOfScope, Insecure, Secure}

// ordered returns checks in the order that Classify runs them: grouped by
// verdict in verdictOrder, and otherwise in their order in checks.
func ordered(checks []check) []check {
	return slices.SortedStableFunc(slices.Values(checks), func(a, b check) int {
		return cmp.Compare(slices.Index(verdictOrder, a.verdict), slices.Index(verdictOrder, b.verdict))
	})
}

// List returns the classifications of the checks, in the order that Classify
// runs them.
func List() []Classification {
	list := []Classification{}
	for _, c := range ordered(checks) {
		list = append(list, c.classification())
	}
	return list
}

// Classify runs the checks on request, and returns the classification of the
// first one that doesn't abstain. It returns an Unclassified classification if
// they all abstain.
func Classify(ctx context.Context, request *safenet.Request) (Classification, error) {
	return classify(ctx, checks, request)
}

func classify(ctx context.Context, checks []check, request *safenet.Request) (Classification, error) {
	for _, c := range ordered(checks) {
		match, err := c.fn(ctx, request)
		if err != nil {
			return Classification{}, fmt.Errorf("check %q: %w", c.classification(), err)
		}
		if match {
			return c.classification(), nil
		}
	}
	return Classification{}, nil
}
