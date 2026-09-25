package checks

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
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
	target := ethrpc.Address{19: 0x70}
	receiver := ethrpc.Address{19: 0xee}
	newCheck := func(verdict Verdict, description string, fn func(c call) (bool, error)) check {
		c := check{verdict: verdict, description: description}
		if verdict == Insecure {
			c.rule = "R-4.1"
		}
		c.fn = func(_ context.Context, _ *safeID, call call) (bool, error) { return fn(call) }
		return c
	}
	to := func(address ethrpc.Address) func(c call) (bool, error) {
		return func(c call) (bool, error) { return c.to == address, nil }
	}
	always := func(call) (bool, error) { return true, nil }
	match := func(verdict Verdict, description string) check { return newCheck(verdict, description, always) }
	abstain := newCheck(Insecure, "abstains", func(call) (bool, error) { return false, nil })
	errCheck := errors.New("check failed")
	fail := newCheck(Insecure, "fails", func(call) (bool, error) { return false, errCheck })
	failSecure := newCheck(Secure, "fails", func(call) (bool, error) { return false, errCheck })

	secure := match(Secure, "signs a message")
	insecure := match(Insecure, "adds an owner")
	outOfScope := match(OutOfScope, "not a Safe")
	// A transaction whose call is to target, and that pays an ether refund to
	// receiver.
	refunded := safenet.SafeTransaction{To: target, GasPrice: big.NewInt(1), RefundReceiver: receiver}
	tests := []struct {
		name   string
		tx     safenet.SafeTransaction
		checks []check
		want   Classification
		err    error
	}{
		{"no checks", safenet.SafeTransaction{}, nil, Classification{}, nil},
		{"all abstain", safenet.SafeTransaction{}, []check{abstain, abstain}, Classification{}, nil},
		{
			"first match wins",
			safenet.SafeTransaction{},
			[]check{abstain, insecure, match(Insecure, "removes an owner")},
			insecure.classification(),
			nil,
		},
		{
			"out of scope before insecure",
			safenet.SafeTransaction{},
			[]check{secure, insecure, outOfScope},
			outOfScope.classification(),
			nil,
		},
		{"insecure before secure", safenet.SafeTransaction{}, []check{secure, abstain, insecure}, insecure.classification(), nil},
		{"error", safenet.SafeTransaction{}, []check{abstain, fail, insecure}, Classification{}, errCheck},
		{"match before error", safenet.SafeTransaction{}, []check{fail, outOfScope}, outOfScope.classification(), nil},
		{"error before match", safenet.SafeTransaction{}, []check{fail, secure}, Classification{}, errCheck},
		{"secure error", safenet.SafeTransaction{}, []check{failSecure, secure}, Classification{}, errCheck},
		{"secure match before error", safenet.SafeTransaction{}, []check{secure, failSecure}, secure.classification(), nil},
		{
			"insecure on any call",
			refunded,
			[]check{newCheck(Insecure, "pays a refund", to(receiver)), secure},
			Classification{Verdict: Insecure, Rule: "R-4.1", Description: "pays a refund"},
			nil,
		},
		{
			"secure needs every call",
			refunded,
			[]check{newCheck(Secure, "calls the target", to(target))},
			Classification{},
			nil,
		},
		{
			"secure calls of different classes",
			refunded,
			[]check{
				newCheck(Secure, "calls the target", to(target)),
				newCheck(Secure, "pays a refund", to(receiver)),
				secure,
			},
			Classification{Verdict: Secure, Description: "calls the target; pays a refund"},
			nil,
		},
		{
			"secure calls of one class",
			refunded,
			[]check{secure, newCheck(Secure, "calls the target", to(target))},
			secure.classification(),
			nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := classify(t.Context(), test.checks, request(test.tx))
			if got != test.want || !errors.Is(err, test.err) {
				t.Errorf("classify() = %+v, %v; want %+v, %v", got, err, test.want, test.err)
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

// request returns a request for tx, with a missing chain ID set to that of
// Ethereum Mainnet, and any other missing amounts set to zero.
func request(tx safenet.SafeTransaction) *safenet.Request {
	if tx.ChainID == nil {
		tx.ChainID = big.NewInt(ethrpc.Mainnet)
	}
	for _, amount := range []**big.Int{&tx.Value, &tx.SafeTxGas, &tx.BaseGas, &tx.GasPrice, &tx.Nonce} {
		if *amount == nil {
			*amount = new(big.Int)
		}
	}
	return &safenet.Request{Proposal: safenet.Proposal{Transaction: tx}}
}

func TestClassifyMissingAmount(t *testing.T) {
	r := request(safenet.SafeTransaction{})
	r.Proposal.Transaction.GasPrice = nil
	if _, err := classify(t.Context(), nil, r); err == nil || !strings.Contains(err.Error(), "gasPrice") {
		t.Errorf("classify() = _, %v; want an error for the missing gasPrice", err)
	}
}
