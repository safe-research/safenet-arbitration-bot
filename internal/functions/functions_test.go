package functions

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/multisend"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

var (
	token     = ethrpc.MustParseAddress("0x7000000000000000000000000000000000000001")
	alice     = ethrpc.MustParseAddress("0xa11ce00000000000000000000000000000000001")
	bob       = ethrpc.MustParseAddress("0xb0b0000000000000000000000000000000000002")
	singleton = ethrpc.MustParseAddress("0x41675C099F32341bf84BFc5382aF534df5C7461a")
)

// TestSelectors checks the selectors of the functions against those of deployed
// contracts.
func TestSelectors(t *testing.T) {
	for signature, want := range map[string]string{
		"transfer(address,uint256)":             "0xa9059cbb",
		"transferFrom(address,address,uint256)": "0x23b872dd",
		"approve(address,uint256)":              "0x095ea7b3",
		"execTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes)": "0x6a761202",
		"createProxyWithNonce(address,bytes,uint256)":                                                "0x1688f0b9",
		"performCreate(uint256,bytes)":                                                               "0x4c8c9ea1",
		"performCreate2(uint256,bytes,bytes32)":                                                      "0x4847be6f",
		"setup(address[],uint256,address,bytes,address,address,uint256,address)":                     "0xb63e800d",
		"createProxy(address,bytes)":                                                                 "0x61b69abd",
		"createProxyWithNonceL2(address,bytes,uint256)":                                              "0x4662f9d6",
		"createChainSpecificProxyWithNonce(address,bytes,uint256)":                                   "0xec9e80bb",
		"createChainSpecificProxyWithNonceL2(address,bytes,uint256)":                                 "0x1a334b0b",
		"createProxyWithCallback(address,bytes,uint256,address)":                                     "0xd18af54d",
		"multiSend(bytes)": "0x8d80ff0a",
	} {
		selector := solabi.Selector(signature)
		if got := ethrpc.Bytes(selector[:]).String(); got != want {
			t.Errorf("selector of %s: got %s, want %s", signature, got, want)
		}
		if _, ok := definitions[selector]; !ok {
			t.Errorf("%s isn't defined", signature)
		}
	}
}

// execTransaction returns the calldata of an execTransaction call of a Safe.
// Its two signatures start with transfer call data, which must not be decoded.
func execTransaction(to ethrpc.Address, value int64, data []byte, operation uint64) ethrpc.Bytes {
	signatures := make([]byte, 130)
	copy(signatures, solabi.Call(solabi.Selector("transfer(address,uint256)"), alice, big.NewInt(1)))
	return solabi.Call(solabi.Selector("execTransaction(address,uint256,bytes,uint8,uint256,uint256,uint256,address,address,bytes)"),
		to, big.NewInt(value), data, operation, new(big.Int), new(big.Int), new(big.Int), ethrpc.Address{}, ethrpc.Address{}, signatures)
}

// multiSend returns the calldata of a multiSend call that packs transactions.
func multiSend(transactions ...multisend.Transaction) ethrpc.Bytes {
	var packed []byte
	for _, tx := range transactions {
		value := solabi.Uint(tx.Value)
		length := solabi.Uint64(uint64(len(tx.Data)))
		packed = append(packed, byte(tx.Operation))
		packed = append(packed, tx.To[:]...)
		packed = append(packed, value[:]...)
		packed = append(packed, length[:]...)
		packed = append(packed, tx.Data...)
	}
	return solabi.Call(solabi.Selector("multiSend(bytes)"), packed)
}

func TestDecode(t *testing.T) {
	transfer := solabi.Call(solabi.Selector("transfer(address,uint256)"), bob, big.NewInt(7))
	dirtyAddress := append([]byte{}, transfer...)
	dirtyAddress[4] = 1
	nested := execTransaction(token, 0, transfer, 0)
	initializer := solabi.Call(solabi.Selector("setup()"))
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{
			"transfer", transfer,
			`{"name":"transfer","signature":"transfer(address,uint256)","args":[` +
				`{"name":"to","type":"address","value":"0xb0b0000000000000000000000000000000000002"},` +
				`{"name":"value","type":"uint256","value":7}]}`,
		},
		{
			"transfer with data after its arguments", append(append([]byte{}, transfer...), 0xff),
			`{"name":"transfer","signature":"transfer(address,uint256)","args":[` +
				`{"name":"to","type":"address","value":"0xb0b0000000000000000000000000000000000002"},` +
				`{"name":"value","type":"uint256","value":7}]}`,
		},
		{
			"transfer with upper bytes in its address", dirtyAddress,
			`{"name":"transfer","signature":"transfer(address,uint256)","args":[` +
				`{"name":"to","type":"address","value":"0xb0b0000000000000000000000000000000000002"},` +
				`{"name":"value","type":"uint256","value":7}]}`,
		},
		{
			"transferFrom", solabi.Call(solabi.Selector("transferFrom(address,address,uint256)"), alice, bob, big.NewInt(3)),
			`{"name":"transferFrom","signature":"transferFrom(address,address,uint256)","args":[` +
				`{"name":"from","type":"address","value":"0xa11ce00000000000000000000000000000000001"},` +
				`{"name":"to","type":"address","value":"0xb0b0000000000000000000000000000000000002"},` +
				`{"name":"value","type":"uint256","value":3}]}`,
		},
		{
			"approve", solabi.Call(solabi.Selector("approve(address,uint256)"), bob, big.NewInt(9)),
			`{"name":"approve","signature":"approve(address,uint256)","args":[` +
				`{"name":"spender","type":"address","value":"0xb0b0000000000000000000000000000000000002"},` +
				`{"name":"value","type":"uint256","value":9}]}`,
		},
		{
			"createProxyWithNonce", solabi.Call(solabi.Selector("createProxyWithNonce(address,bytes,uint256)"), singleton, initializer, big.NewInt(4)),
			`{"name":"createProxyWithNonce","signature":"createProxyWithNonce(address,bytes,uint256)","args":[` +
				`{"name":"singleton","type":"address","value":"0x41675c099f32341bf84bfc5382af534df5c7461a"},` +
				`{"name":"initializer","type":"bytes","value":"0xba0bba40"},` +
				`{"name":"saltNonce","type":"uint256","value":4}]}`,
		},
		{
			"performCreate", solabi.Call(solabi.Selector("performCreate(uint256,bytes)"), big.NewInt(0), []byte{0x60, 0x00}),
			`{"name":"performCreate","signature":"performCreate(uint256,bytes)","args":[` +
				`{"name":"value","type":"uint256","value":0},` +
				`{"name":"deploymentData","type":"bytes","value":"0x6000"}]}`,
		},
		{
			"performCreate2", solabi.Call(solabi.Selector("performCreate2(uint256,bytes,bytes32)"), big.NewInt(1), []byte{0x60, 0x00}, ethrpc.Hash{31: 5}),
			`{"name":"performCreate2","signature":"performCreate2(uint256,bytes,bytes32)","args":[` +
				`{"name":"value","type":"uint256","value":1},` +
				`{"name":"deploymentData","type":"bytes","value":"0x6000"},` +
				`{"name":"salt","type":"bytes32","value":"0x0000000000000000000000000000000000000000000000000000000000000005"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(Decode(test.data))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Errorf("Decode:\ngot  %s\nwant %s", got, test.want)
			}
		})
	}

	t.Run("execTransaction of a transfer", func(t *testing.T) {
		f := Decode(nested)
		if f == nil || f.Name != "execTransaction" || len(f.Args) != 10 {
			t.Fatalf("Decode: got %+v, want execTransaction with 10 arguments", f)
		}
		if to := f.Args[0]; to.Name != "to" || to.Value != token {
			t.Errorf("to: got %+v, want %s", to, token)
		}
		if op := f.Args[3]; op.Name != "operation" || op.Value != uint64(0) {
			t.Errorf("operation: got %+v, want 0", op)
		}
		data := f.Args[2]
		if data.Name != "data" || data.Call == nil || data.Call.Name != "transfer" {
			t.Fatalf("data: got %+v, want the decoded transfer", data)
		}
		if to := data.Call.Args[0]; to.Value != bob {
			t.Errorf("recipient of the inner transfer: got %v, want %s", to.Value, bob)
		}
		if sigs := f.Args[9]; sigs.Name != "signatures" || sigs.Call != nil || len(sigs.Value.(ethrpc.Bytes)) != 130 {
			t.Errorf("signatures: got %+v, want 130 bytes that aren't decoded as a call", sigs)
		}
		if got, want := f.String(), "execTransaction > transfer"; got != want {
			t.Errorf("String: got %q, want %q", got, want)
		}
	})

	t.Run("proxy creations with a setup", func(t *testing.T) {
		setup := solabi.Call(solabi.Selector("setup(address[],uint256,address,bytes,address,address,uint256,address)"),
			[]ethrpc.Address{alice, bob}, big.NewInt(2), token, transfer, ethrpc.Address{}, ethrpc.Address{}, new(big.Int), ethrpc.Address{})
		for want, data := range map[string]ethrpc.Bytes{
			"createProxy > setup > transfer": solabi.Call(solabi.Selector("createProxy(address,bytes)"), singleton, setup),
			"createProxyWithNonce > setup > transfer": solabi.Call(solabi.Selector("createProxyWithNonce(address,bytes,uint256)"),
				singleton, setup, big.NewInt(4)),
			"createProxyWithNonceL2 > setup > transfer": solabi.Call(solabi.Selector("createProxyWithNonceL2(address,bytes,uint256)"),
				singleton, setup, big.NewInt(4)),
			"createChainSpecificProxyWithNonce > setup > transfer": solabi.Call(solabi.Selector("createChainSpecificProxyWithNonce(address,bytes,uint256)"),
				singleton, setup, big.NewInt(4)),
			"createChainSpecificProxyWithNonceL2 > setup > transfer": solabi.Call(solabi.Selector("createChainSpecificProxyWithNonceL2(address,bytes,uint256)"),
				singleton, setup, big.NewInt(4)),
			"createProxyWithCallback > setup > transfer": solabi.Call(solabi.Selector("createProxyWithCallback(address,bytes,uint256,address)"),
				singleton, setup, big.NewInt(4), alice),
		} {
			f := Decode(data)
			if f == nil || f.String() != want {
				t.Errorf("Decode: got %+v, want %s", f, want)
				continue
			}
			s := f.Args[1].Call
			if owners := s.Args[0]; owners.Name != "owners" || !reflect.DeepEqual(owners.Value, []ethrpc.Address{alice, bob}) {
				t.Errorf("%s: owners %+v, want %s and %s", want, owners, alice, bob)
			}
			if threshold := s.Args[1]; threshold.Value.(*big.Int).Int64() != 2 {
				t.Errorf("%s: threshold %+v, want 2", want, threshold)
			}
		}
	})

	t.Run("setup without a call", func(t *testing.T) {
		// The Safe only delegate calls to with data if to isn't the zero address.
		f := Decode(solabi.Call(solabi.Selector("setup(address[],uint256,address,bytes,address,address,uint256,address)"),
			[]ethrpc.Address{alice}, big.NewInt(1), ethrpc.Address{}, transfer, ethrpc.Address{}, ethrpc.Address{}, new(big.Int), ethrpc.Address{}))
		if f == nil || f.String() != "setup" || f.Args[3].Call != nil {
			t.Errorf("Decode: got %+v, want setup whose data isn't decoded", f)
		}
	})

	t.Run("execTransaction of a batch", func(t *testing.T) {
		approve := solabi.Call(solabi.Selector("approve(address,uint256)"), bob, big.NewInt(9))
		inner := multiSend(multisend.Transaction{Operation: safenet.OperationCall, To: token, Value: new(big.Int), Data: approve})
		batch := multiSend(
			multisend.Transaction{Operation: safenet.OperationCall, To: token, Value: new(big.Int), Data: transfer},
			multisend.Transaction{Operation: safenet.OperationCall, To: alice, Value: big.NewInt(5), Data: ethrpc.Bytes{}},
			multisend.Transaction{Operation: safenet.OperationDelegateCall, To: token, Value: new(big.Int), Data: inner},
		)
		f := Decode(execTransaction(token, 0, batch, 1))
		if got, want := f.String(), "execTransaction > multiSend > [transfer, -, multiSend > [approve]]"; got != want {
			t.Fatalf("String: got %q, want %q", got, want)
		}
		txs := f.Args[2].Call.Args[0].Batch
		if len(txs) != 3 || txs[1].To != alice || txs[1].Value.Int64() != 5 || txs[2].Operation != safenet.OperationDelegateCall {
			t.Errorf("batch: got %+v, want a transfer, 5 wei to %s, and a delegate call", txs, alice)
		}
		if to := txs[0].Function.Args[0].Value; to != bob {
			t.Errorf("recipient of the batched transfer: got %v, want %s", to, bob)
		}
	})

	t.Run("batch output", func(t *testing.T) {
		send := multisend.Transaction{Operation: safenet.OperationCall, To: token, Value: big.NewInt(5), Data: ethrpc.Bytes{}}
		for _, test := range []struct {
			data          ethrpc.Bytes
			batch, string string
		}{
			{multiSend(), `[]`, "multiSend > []"},
			{
				multiSend(send),
				`[{"operation":"CALL","to":"0x7000000000000000000000000000000000000001","value":5,"data":"0x","function":null}]`,
				"multiSend > [-]",
			},
		} {
			f := Decode(test.data)
			if f == nil {
				t.Fatalf("Decode(%s): got nil, want multiSend", test.data)
			}
			if got, err := json.Marshal(f.Args[0].Batch); err != nil || string(got) != test.batch {
				t.Errorf("batch: got %s, %v; want %s", got, err, test.batch)
			}
			if got := f.String(); got != test.string {
				t.Errorf("String: got %q, want %q", got, test.string)
			}
		}
	})

	t.Run("names of a large batch", func(t *testing.T) {
		transfers := make([]multisend.Transaction, 20)
		for i := range transfers {
			transfers[i] = multisend.Transaction{Operation: safenet.OperationCall, To: token, Value: new(big.Int), Data: transfer}
		}
		want := "multiSend > [" + strings.Repeat("transfer, ", maxNames-1) + "...]"
		if got := Decode(multiSend(transfers...)).String(); got != want {
			t.Errorf("String: got %q, want %q", got, want)
		}
		// Nested batches share the names, so the description stays short.
		batches := make([]multisend.Transaction, 20)
		for i := range batches {
			batches[i] = multisend.Transaction{Operation: safenet.OperationDelegateCall, To: token, Value: new(big.Int), Data: multiSend(transfers...)}
		}
		got := Decode(multiSend(batches...)).String()
		if n := strings.Count(got, "transfer") + strings.Count(got, "multiSend"); n != maxNames {
			t.Errorf("String: got %d names in %q, want %d", n, got, maxNames)
		}
		// So do the calls that a batch's calls make.
		execs := make([]multisend.Transaction, 20)
		for i := range execs {
			execs[i] = multisend.Transaction{Operation: safenet.OperationCall, To: token, Value: new(big.Int), Data: execTransaction(token, 0, transfer, 0)}
		}
		got = Decode(multiSend(execs...)).String()
		if n := strings.Count(got, "transfer") + strings.Count(got, "execTransaction") + strings.Count(got, "multiSend"); n != maxNames {
			t.Errorf("String: got %d names in %q, want %d", n, got, maxNames)
		}
	})

	t.Run("execTransaction of unknown data", func(t *testing.T) {
		f := Decode(execTransaction(token, 0, []byte{1, 2, 3, 4, 5}, 1))
		if f == nil || f.Args[2].Call != nil || f.String() != "execTransaction" {
			t.Errorf("Decode: got %+v, want execTransaction whose data isn't decoded", f)
		}
	})
}

func TestDecodeMismatches(t *testing.T) {
	transfer := solabi.Call(solabi.Selector("transfer(address,uint256)"), bob, big.NewInt(7))
	badOperation := execTransaction(token, 0, nil, 0)
	badOperation[4+3*32+30] = 1 // operation 256
	badOwners := solabi.Call(solabi.Selector("setup(address[],uint256,address,bytes,address,address,uint256,address)"),
		[]ethrpc.Address{alice}, big.NewInt(1), ethrpc.Address{}, []byte{}, ethrpc.Address{}, ethrpc.Address{}, new(big.Int), ethrpc.Address{})
	copy(badOwners[4+8*32+24:], bytes.Repeat([]byte{0xff}, 8)) // 2^64-1 owners, far more than the data has
	revertingBatch := multiSend(multisend.Transaction{Operation: 2, To: token, Value: new(big.Int), Data: transfer})
	badOffset := execTransaction(token, 0, nil, 0)
	badOffset[4+2*32+28] = 0xff // past the end of the data
	for name, data := range map[string][]byte{
		"no data":          nil,
		"short selector":   transfer[:3],
		"unknown selector": solabi.Call(solabi.Selector("transfer(address,uint128)"), bob, big.NewInt(7)),
		"short arguments":  transfer[:4+40],
		"operation 256":    badOperation,
		"owners past data": badOwners,
		"reverting batch":  revertingBatch,
		"bad offset":       badOffset,
	} {
		if f := Decode(data); f != nil {
			t.Errorf("%s: Decode got %+v, want nil", name, f)
		}
	}
}

func TestDecodeBatchDepth(t *testing.T) {
	data := solabi.Call(solabi.Selector("transfer(address,uint256)"), bob, big.NewInt(1))
	for range maxDepth + 2 {
		data = multiSend(multisend.Transaction{Operation: safenet.OperationCall, To: token, Value: new(big.Int), Data: data})
	}
	depth := 0
	for f := Decode(data); f != nil; f = f.Args[0].Batch[0].Function {
		depth++
		if f.Name != "multiSend" {
			break
		}
	}
	if depth != maxDepth {
		t.Errorf("decoded %d levels, want %d", depth, maxDepth)
	}
}

func TestDecodeDepth(t *testing.T) {
	data := solabi.Call(solabi.Selector("transfer(address,uint256)"), bob, big.NewInt(1))
	for range maxDepth + 2 {
		data = execTransaction(token, 0, data, 0)
	}
	depth := 0
	for f := Decode(data); f != nil; f = f.Args[2].Call {
		depth++
		if f.Name != "execTransaction" {
			break
		}
	}
	if depth != maxDepth {
		t.Errorf("decoded %d levels, want %d", depth, maxDepth)
	}
}
