package checks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
)

func TestClassification(t *testing.T) {
	tests := []struct {
		c    Classification
		text string
		json string
	}{
		{Classification{}, "unclassified", `{"verdict":null}`},
		{Classification{Verdict: Secure}, "secure", `{"verdict":"secure"}`},
		{
			Classification{Verdict: Insecure, Rule: "R-4.1", Description: "adds an owner"},
			"insecure  R-4.1  adds an owner",
			`{"verdict":"insecure","rule":"R-4.1","description":"adds an owner"}`,
		},
		{
			Classification{Verdict: OutOfScope, Description: "not a Safe"},
			"out-of-scope  not a Safe",
			`{"verdict":"out-of-scope","description":"not a Safe"}`,
		},
	}
	for _, test := range tests {
		if got := test.c.String(); got != test.text {
			t.Errorf("%+v.String() = %q, want %q", test.c, got, test.text)
		}
		got, err := json.Marshal(test.c)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != test.json {
			t.Errorf("json.Marshal(%+v) = %s, want %s", test.c, got, test.json)
		}
	}
}

func TestClassify(t *testing.T) {
	abstain := func(context.Context, *safenet.Request) (Classification, error) {
		return Classification{}, nil
	}
	verdict := func(c Classification) Check {
		return func(context.Context, *safenet.Request) (Classification, error) { return c, nil }
	}
	errCheck := errors.New("check failed")
	fail := func(context.Context, *safenet.Request) (Classification, error) {
		return Classification{}, errCheck
	}
	insecure := Classification{Verdict: Insecure, Rule: "R-4.1", Description: "adds an owner"}
	outOfScope := Classification{Verdict: OutOfScope, Description: "not a Safe"}

	tests := []struct {
		name   string
		checks []Check
		want   Classification
		err    error
	}{
		{"no checks", nil, Classification{}, nil},
		{"all abstain", []Check{abstain, abstain}, Classification{}, nil},
		{"first verdict wins", []Check{abstain, verdict(insecure), verdict(outOfScope)}, insecure, nil},
		{"error", []Check{abstain, fail, verdict(insecure)}, Classification{}, errCheck},
		{"verdict before error", []Check{verdict(outOfScope), fail}, outOfScope, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := classify(t.Context(), test.checks, &safenet.Request{})
			if got != test.want || !errors.Is(err, test.err) {
				t.Errorf("classify() = %+v, %v; want %+v, %v", got, err, test.want, test.err)
			}
		})
	}
}
