package checks

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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

// TestChecks checks that every check has a verdict and a description, and a
// rule if and only if it is for insecure requests.
func TestChecks(t *testing.T) {
	for _, c := range checks {
		if !slices.Contains(verdictOrder, c.verdict) || c.description == "" || c.fn == nil ||
			(c.rule != "") != (c.verdict == Insecure) {
			t.Errorf("check %q: needs a verdict, a description, a function, and a rule only if insecure", c.classification())
		}
	}
}

func TestClassify(t *testing.T) {
	match := func(verdict Verdict, description string) check {
		c := check{verdict: verdict, description: description}
		if verdict == Insecure {
			c.rule = "R-4.1"
		}
		c.fn = func(context.Context, *safenet.Request) (bool, error) { return true, nil }
		return c
	}
	abstain := check{verdict: Insecure, rule: "R-4.2", description: "abstains"}
	abstain.fn = func(context.Context, *safenet.Request) (bool, error) { return false, nil }
	errCheck := errors.New("check failed")
	fail := check{verdict: Insecure, rule: "R-4.2", description: "fails"}
	fail.fn = func(context.Context, *safenet.Request) (bool, error) { return false, errCheck }

	secure := match(Secure, "signs a message")
	insecure := match(Insecure, "adds an owner")
	outOfScope := match(OutOfScope, "not a Safe")
	tests := []struct {
		name   string
		checks []check
		want   check
		err    error
	}{
		{"no checks", nil, check{}, nil},
		{"all abstain", []check{abstain, abstain}, check{}, nil},
		{"first match wins", []check{abstain, insecure, match(Insecure, "removes an owner")}, insecure, nil},
		{"out of scope before insecure", []check{secure, insecure, outOfScope}, outOfScope, nil},
		{"insecure before secure", []check{secure, abstain, insecure}, insecure, nil},
		{"error", []check{abstain, fail, insecure}, check{}, errCheck},
		{"match before error", []check{fail, outOfScope}, outOfScope, nil},
		{"error before match", []check{fail, secure}, check{}, errCheck},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := classify(t.Context(), test.checks, &safenet.Request{})
			if want := test.want.classification(); got != want || !errors.Is(err, test.err) {
				t.Errorf("classify() = %+v, %v; want %+v, %v", got, err, want, test.err)
			}
		})
	}
}

func TestOrdered(t *testing.T) {
	checks := []check{
		{verdict: Secure, description: "secure 1"},
		{verdict: Insecure, rule: "R-4.1", description: "insecure 1"},
		{verdict: OutOfScope, description: "out of scope 1"},
		{verdict: Secure, description: "secure 2"},
		{verdict: OutOfScope, description: "out of scope 2"},
		{verdict: Insecure, rule: "R-4.2", description: "insecure 2"},
	}
	var got []string
	for _, c := range ordered(checks) {
		got = append(got, c.description)
	}
	want := []string{"out of scope 1", "out of scope 2", "insecure 1", "insecure 2", "secure 1", "secure 2"}
	if !slices.Equal(got, want) {
		t.Errorf("ordered() = %q, want %q", got, want)
	}
}
