// Package functions decodes the call data of well-known functions: those of
// ERC-20 tokens, the execTransaction and setup of Safe 1.3.0, 1.4.1, and 1.5.0,
// the multiSend of their MultiSend contracts, the functions of their proxy
// factories that create a proxy, and the performCreate and performCreate2 of
// their CreateCall contracts.
//
// Call data is decoded by its selector and ABI layout only, so data that
// decodes as one of them says nothing about the contract that it calls, whose
// code decides what the call does, or whether it does anything at all. Callers
// must look at the account called.
package functions

import (
	"strings"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/multisend"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// Function is decoded call data.
type Function struct {
	Name string `json:"name"`
	// Signature is the canonical signature of the function, such as
	// "transfer(address,uint256)", whose selector the data starts with.
	Signature string `json:"signature"`
	Args      []Arg  `json:"args"`
}

// Arg is an argument of a function.
type Arg struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Value is an address, a list of addresses, a *big.Int, a uint64, a 32-byte
	// word, or bytes, all of which encode in JSON as text, numbers, or lists of
	// them.
	Value any `json:"value"`
	// Call is the decoded call data of a bytes argument that is the data of a call
	// that the function makes, such as the data of execTransaction, if it is that
	// of one of the well-known functions. It is left out otherwise.
	Call *Function `json:"call,omitempty"`
	// Batch is the transactions that a bytes argument packs for MultiSend, such as
	// the argument of multiSend. It is left out of other arguments.
	Batch []Transaction `json:"batch,omitzero"`
}

// Transaction is a transaction of a MultiSend batch, with its data decoded as a
// well-known function, if it is one.
type Transaction struct {
	multisend.Transaction
	Function *Function `json:"function"`
}

// param is a parameter of a function. A bytes parameter that is call is the
// data of a call that the function makes, unless the earlier address parameter
// named unless is the zero address, and one that is batch packs the
// transactions of a MultiSend batch.
type param struct {
	name   string
	typ    string
	call   bool
	unless string
	batch  bool
}

// definition is a well-known function: its name and parameters, in order.
type definition struct {
	name      string
	signature string
	params    []param
}

// definitions are the functions that Decode knows, by their selector.
var definitions = map[[4]byte]definition{}

// define adds the function with the given name and parameters.
func define(name string, params ...param) {
	types := make([]string, len(params))
	for i, p := range params {
		types[i] = p.typ
	}
	signature := name + "(" + strings.Join(types, ",") + ")"
	selector := solabi.Selector(signature)
	if _, ok := definitions[selector]; ok {
		panic("functions: two definitions with the selector of " + signature)
	}
	definitions[selector] = definition{name, signature, params}
}

// p is a parameter, data a bytes parameter that is call data, and batch one
// that packs MultiSend transactions.
func p(typ, name string) param { return param{name: name, typ: typ} }
func data(name string) param   { return param{name: name, typ: "bytes", call: true} }
func batch(name string) param  { return param{name: name, typ: "bytes", batch: true} }

func init() {
	// ERC-20.
	define("transfer", p("address", "to"), p("uint256", "value"))
	define("transferFrom", p("address", "from"), p("address", "to"), p("uint256", "value"))
	define("approve", p("address", "spender"), p("uint256", "value"))

	// Safe. It runs the call that data encodes on to, with value and operation, if
	// the signatures are valid for its owners.
	define("execTransaction", p("address", "to"), p("uint256", "value"), data("data"), p("uint8", "operation"),
		p("uint256", "safeTxGas"), p("uint256", "baseGas"), p("uint256", "gasPrice"), p("address", "gasToken"),
		p("address", "refundReceiver"), p("bytes", "signatures"))
	// A new Safe's setup delegate calls to with data, unless to is the zero
	// address.
	define("setup", p("address[]", "owners"), p("uint256", "threshold"), p("address", "to"),
		param{name: "data", typ: "bytes", call: true, unless: "to"},
		p("address", "fallbackHandler"), p("address", "paymentToken"), p("uint256", "payment"), p("address", "paymentReceiver"))

	// MultiSend and MultiSendCallOnly. They run the transactions that transactions
	// packs, as multisend.Decode reads them.
	define("multiSend", batch("transactions"))

	// Safe proxy factories. They deploy a proxy of singleton, and call it with the
	// initializer, such as setup, or with data for 1.3.0's createProxy. The L2 and
	// chain-specific functions differ only in the events they log and in the
	// proxy's address.
	define("createProxy", p("address", "singleton"), data("data"))
	for _, name := range []string{"createProxyWithNonce", "createProxyWithNonceL2", "createChainSpecificProxyWithNonce", "createChainSpecificProxyWithNonceL2"} {
		define(name, p("address", "singleton"), data("initializer"), p("uint256", "saltNonce"))
	}
	define("createProxyWithCallback", p("address", "singleton"), data("initializer"), p("uint256", "saltNonce"), p("address", "callback"))

	// Safe CreateCall. It deploys a contract with the init code deploymentData and
	// value, with CREATE, or CREATE2 and salt, from its own address, or from the
	// Safe's if the Safe delegate calls it.
	define("performCreate", p("uint256", "value"), p("bytes", "deploymentData"))
	define("performCreate2", p("uint256", "value"), p("bytes", "deploymentData"), p("bytes32", "salt"))
}

// maxDepth is how many levels of nested call data Decode decodes.
const maxDepth = 8

// Decode decodes call data as one of the well-known functions, and the data of
// the calls that it makes as well-known functions, if they are. It returns nil
// if the data isn't one, or has a well-known selector but arguments that don't
// fit its ABI layout, or a MultiSend batch that reverts whatever the state.
// Data after the arguments is ignored, as the Solidity ABI decoder ignores it.
func Decode(calldata []byte) *Function {
	return decode(calldata, 0)
}

func decode(calldata []byte, depth int) *Function {
	if len(calldata) < 4 || depth >= maxDepth {
		return nil
	}
	def, ok := definitions[[4]byte(calldata[:4])]
	if !ok {
		return nil
	}
	d := solabi.NewDecoder(calldata[4:])
	args := make([]Arg, len(def.params))
	for i, p := range def.params {
		arg := Arg{Name: p.name, Type: p.typ}
		switch p.typ {
		case "address":
			// The Safe and contracts built with Solidity's ABI coder v1, such as WETH9,
			// take an address from the low 20 bytes of its word, ignoring the others.
			w := d.Bytes32(i)
			arg.Value = ethrpc.Address(w[12:])
		case "address[]":
			n, elems := d.Array(i)
			addresses := make([]ethrpc.Address, n)
			for j := range n {
				w := elems.Bytes32(j)
				addresses[j] = ethrpc.Address(w[12:])
			}
			arg.Value = addresses
		case "uint256":
			arg.Value = d.Uint(i)
		case "uint8":
			v := d.Uint64(i)
			if v > 255 {
				return nil
			}
			arg.Value = v
		case "bytes32":
			arg.Value = ethrpc.Hash(d.Bytes32(i))
		case "bytes":
			b := d.Bytes(i)
			arg.Value = ethrpc.Bytes(b)
			if p.call && d.Err() == nil && !zeroAddress(args[:i], p.unless) {
				arg.Call = decode(b, depth+1)
			}
			if p.batch && d.Err() == nil {
				transactions, ok := multisend.Decode(b)
				if !ok {
					return nil
				}
				arg.Batch = []Transaction{}
				for _, tx := range transactions {
					arg.Batch = append(arg.Batch, Transaction{tx, decode(tx.Data, depth+1)})
				}
			}
		}
		if d.Err() != nil {
			return nil
		}
		args[i] = arg
	}
	return &Function{Name: def.name, Signature: def.signature, Args: args}
}

// zeroAddress reports whether args has an argument named name that is the zero
// address.
func zeroAddress(args []Arg, name string) bool {
	for _, a := range args {
		if a.Name == name {
			return a.Value == any(ethrpc.Address{})
		}
	}
	return false
}

// maxNames is how many names String writes, so that the description of a large
// batch stays short. The JSON output has all of them.
const maxNames = 16

// String returns the name of the function, followed by those of the well-known
// functions of the calls that it makes, such as "execTransaction > transfer",
// with those of a batch in brackets and "-" for unknown data, such as
// "multiSend > [transfer, -]". It writes "..." in place of the names after the
// first maxNames.
func (f *Function) String() string {
	n := maxNames
	return f.string(&n)
}

// string returns the description that String does, with at most n names, and
// counts down n by the names that it writes.
func (f *Function) string(n *int) string {
	if *n <= 0 {
		return "..."
	}
	*n--
	names := []string{f.Name}
	for _, a := range f.Args {
		if a.Call != nil {
			names = append(names, a.Call.string(n))
		}
		if a.Batch != nil {
			var batch []string
			for _, tx := range a.Batch {
				if *n <= 0 {
					batch = append(batch, "...")
					break
				}
				if tx.Function == nil {
					*n--
					batch = append(batch, "-")
					continue
				}
				batch = append(batch, tx.Function.string(n))
			}
			names = append(names, "["+strings.Join(batch, ", ")+"]")
		}
	}
	return strings.Join(names, " > ")
}
