package cli

import (
	"bytes"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

func TestPrintCreations(t *testing.T) {
	createCall := ethrpc.MustParseAddress("0x9b35Af71d77eaf8d7e40252370304687390A1A52")
	safe := ethrpc.MustParseAddress("0x5afE000000000000000000000000000000000001")
	salt, codeHash, nonce := ethrpc.Hash{31: 0x5a}, ethrpc.Hash{0: 0xc0, 31: 1}, uint64(7)
	calls := []checks.Call{
		{Creates: &checks.Creation{Address: ethrpc.Address{0: 0xc2}, Method: "create2", Deployer: createCall, Salt: &salt, InitCodeHash: codeHash}},
		{},
		{Creates: &checks.Creation{Address: ethrpc.Address{0: 0xc1}, Method: "create", Deployer: safe, Nonce: &nonce, InitCodeHash: codeHash}},
	}
	var stdout bytes.Buffer
	printCreations(&env{stdout: &stdout}, calls)
	want := "\nContracts that the calls would deploy, derived from their data\n\n" +
		"  call 1  " + ethrpc.Address{0: 0xc2}.String() + " (CREATE2 by " + createCall.String() + ", salt " + salt.String() + ", init code hash " + codeHash.String() + ")\n" +
		"  call 3  " + ethrpc.Address{0: 0xc1}.String() + " (CREATE by " + safe.String() + ", nonce 7, init code hash " + codeHash.String() + ")\n"
	if got := stdout.String(); got != want {
		t.Errorf("printCreations:\ngot  %q\nwant %q", got, want)
	}

	// Calls that deploy nothing have no list.
	stdout.Reset()
	if printCreations(&env{stdout: &stdout}, calls[1:2]); stdout.Len() != 0 {
		t.Errorf("printCreations without creations: got %q, want no output", stdout.String())
	}
}
