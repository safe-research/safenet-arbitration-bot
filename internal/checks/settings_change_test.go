package checks

import (
	"math/big"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

func TestSettingsChange(t *testing.T) {
	safeVersions.Clear()
	safe := &safeID{address: ethrpc.Address{0: 0x5a, 19: 1}, chainID: big.NewInt(ethrpc.Mainnet)}
	node := &fakeNode{accounts: map[ethrpc.Address]account{safe.address: proxy(safeProxy150, singletonOf("1.5.0"))}}
	at := &env{dial: node.dialer(t, testSafeBlock), block: testSafeBlock}

	owner, module := ethrpc.Address{19: 0x0e}, ethrpc.Address{19: 0x0d}
	// self returns a call by the Safe to its function with signature.
	self := func(signature string, args ...any) call {
		return call{
			to:        safe.address,
			value:     new(big.Int),
			data:      solabi.Call(solabi.Selector(signature), args...),
			operation: safenet.OperationCall,
		}
	}
	with := func(c call, change func(c *call)) call {
		change(&c)
		return c
	}
	withValue := func(c call) call { return with(c, func(c *call) { c.value = big.NewInt(1) }) }
	batched := func(c call) call { return with(c, func(c *call) { c.kind = Batched }) }
	withData := func(c call, data []byte) call { return with(c, func(c *call) { c.data = data }) }
	// dirty sets the upper bytes of the argument with index i, which the Safe's ABI
	// decoder ignores for an address.
	dirty := func(c call, i int) call {
		data := append(ethrpc.Bytes{}, c.data...)
		data[4+32*i] = 0xff
		return withData(c, data)
	}

	addOwner := self("addOwnerWithThreshold(address,uint256)", owner, uint64(1))
	swapOwner := self("swapOwner(address,address,address)", sentinel, owner, module)
	disableModule := self("disableModule(address,address)", sentinel, module)
	setFallbackHandler := self("setFallbackHandler(address)", ethrpc.Address{})
	setModuleGuard := self("setModuleGuard(address)", module)

	tests := []struct {
		name  string
		check check
		c     call
		want  bool
	}{
		{"addOwnerWithThreshold", addOwnerCheck, addOwner, true},
		{"addOwnerWithThreshold with value", addOwnerCheck, withValue(addOwner), true},
		{"batched addOwnerWithThreshold", addOwnerCheck, batched(addOwner), true},
		{"addOwnerWithThreshold with a zero threshold", addOwnerCheck, self("addOwnerWithThreshold(address,uint256)", owner, uint64(0)), true},
		{"addOwnerWithThreshold of the Safe", addOwnerCheck, self("addOwnerWithThreshold(address,uint256)", safe.address, uint64(1)), true},
		{"addOwnerWithThreshold with no arguments", addOwnerCheck, withData(addOwner, addOwner.data[:4]), false},
		{"addOwnerWithThreshold with a truncated threshold", addOwnerCheck, withData(addOwner, addOwner.data[:67]), false},
		{"addOwnerWithThreshold with trailing data", addOwnerCheck, withData(addOwner, append(append(ethrpc.Bytes{}, addOwner.data...), 0)), true},
		{"addOwnerWithThreshold of the zero address", addOwnerCheck, self("addOwnerWithThreshold(address,uint256)", ethrpc.Address{}, uint64(1)), false},
		{"addOwnerWithThreshold of the sentinel", addOwnerCheck, self("addOwnerWithThreshold(address,uint256)", sentinel, uint64(1)), false},
		{
			"addOwnerWithThreshold of the zero address with dirty bits",
			addOwnerCheck,
			dirty(self("addOwnerWithThreshold(address,uint256)", ethrpc.Address{}, uint64(1)), 0),
			false,
		},
		{"addOwnerWithThreshold on another account", addOwnerCheck, with(addOwner, func(c *call) { c.to = owner }), false},
		{"delegate call to addOwnerWithThreshold", addOwnerCheck, with(addOwner, func(c *call) { c.operation = safenet.OperationDelegateCall }), false},
		{"another function", addOwnerCheck, swapOwner, false},
		{"no data", addOwnerCheck, withData(addOwner, nil), false},
		{"truncated selector", addOwnerCheck, withData(addOwner, addOwner.data[:3]), false},

		{"removeOwner", removeOwnerCheck, self("removeOwner(address,address,uint256)", sentinel, owner, uint64(1)), true},
		{"removeOwner of the zero address", removeOwnerCheck, self("removeOwner(address,address,uint256)", sentinel, ethrpc.Address{}, uint64(1)), true},

		{"swapOwner", swapOwnerCheck, swapOwner, true},
		{"swapOwner of the zero address", swapOwnerCheck, self("swapOwner(address,address,address)", sentinel, ethrpc.Address{}, module), true},
		{"swapOwner to the zero address", swapOwnerCheck, self("swapOwner(address,address,address)", sentinel, owner, ethrpc.Address{}), false},
		{"swapOwner to the sentinel", swapOwnerCheck, self("swapOwner(address,address,address)", sentinel, owner, sentinel), false},
		{"swapOwner with a truncated new owner", swapOwnerCheck, withData(swapOwner, swapOwner.data[:99]), false},

		{"changeThreshold", changeThresholdCheck, self("changeThreshold(uint256)", uint64(2)), true},
		{"changeThreshold to zero", changeThresholdCheck, self("changeThreshold(uint256)", uint64(0)), true},
		{"changeThreshold with no threshold", changeThresholdCheck, withData(addOwner, solabi.Call(solabi.Selector("changeThreshold(uint256)"))), false},

		{"enableModule", enableModuleCheck, self("enableModule(address)", module), true},
		{"enableModule of the sentinel", enableModuleCheck, self("enableModule(address)", sentinel), true},

		{"disableModule", disableModuleCheck, disableModule, false},
		{"batched disableModule", disableModuleCheck, batched(disableModule), true},
		{"disableModule with value", disableModuleCheck, withValue(disableModule), true},
		{"disableModule with trailing data", disableModuleCheck, withData(disableModule, append(append(ethrpc.Bytes{}, disableModule.data...), 0)), true},
		{"disableModule with a dirty previous module", disableModuleCheck, dirty(disableModule, 0), true},
		{"disableModule with a dirty module", disableModuleCheck, dirty(disableModule, 1), true},
		{"disableModule with a truncated module", disableModuleCheck, withData(disableModule, disableModule.data[:67]), false},

		{"setGuard", setGuardCheck, self("setGuard(address)", module), true},
		{"setGuard to the zero address", setGuardCheck, self("setGuard(address)", ethrpc.Address{}), true},

		{"setFallbackHandler to the zero address", setFallbackHandlerCheck, setFallbackHandler, false},
		{"setFallbackHandler", setFallbackHandlerCheck, self("setFallbackHandler(address)", module), true},
		{"batched setFallbackHandler to the zero address", setFallbackHandlerCheck, batched(setFallbackHandler), true},
		{"setFallbackHandler to the zero address with value", setFallbackHandlerCheck, withValue(setFallbackHandler), true},
		{"setFallbackHandler to a dirty zero address", setFallbackHandlerCheck, dirty(setFallbackHandler, 0), true},
		{"setFallbackHandler with no handler", setFallbackHandlerCheck, withData(setFallbackHandler, setFallbackHandler.data[:4]), false},
		{
			"setFallbackHandler to the zero address with trailing data",
			setFallbackHandlerCheck,
			withData(setFallbackHandler, append(append(ethrpc.Bytes{}, setFallbackHandler.data...), 0)),
			true,
		},

		{"allowed disableModule", allowedDisableModuleCheck, disableModule, true},
		{"allowed disableModule of the sentinel", allowedDisableModuleCheck, self("disableModule(address,address)", sentinel, sentinel), true},
		{"batched allowed disableModule", allowedDisableModuleCheck, batched(disableModule), false},
		{"allowed disableModule with value", allowedDisableModuleCheck, withValue(disableModule), false},
		{"allowed disableModule with a dirty module", allowedDisableModuleCheck, dirty(disableModule, 1), false},
		{"allowed disableModule with a truncated module", allowedDisableModuleCheck, withData(disableModule, disableModule.data[:67]), false},
		{"allowed disableModule on another account", allowedDisableModuleCheck, with(disableModule, func(c *call) { c.to = module }), false},
		{
			"allowed disableModule as a delegate call",
			allowedDisableModuleCheck,
			with(disableModule, func(c *call) { c.operation = safenet.OperationDelegateCall }),
			false,
		},
		{"allowed disableModule as a refund", allowedDisableModuleCheck, with(disableModule, func(c *call) { c.kind = Refund }), false},
		{"another function allowed as disableModule", allowedDisableModuleCheck, withData(disableModule, append(self("enableModule(address)", module).data, make([]byte, 32)...)), false},

		{"allowed setFallbackHandler", allowedSetFallbackHandlerCheck, setFallbackHandler, true},
		{"allowed setFallbackHandler to another handler", allowedSetFallbackHandlerCheck, self("setFallbackHandler(address)", module), false},
		{"batched allowed setFallbackHandler", allowedSetFallbackHandlerCheck, batched(setFallbackHandler), false},
		{"allowed setFallbackHandler with value", allowedSetFallbackHandlerCheck, withValue(setFallbackHandler), false},
		{"allowed setFallbackHandler to a dirty zero address", allowedSetFallbackHandlerCheck, dirty(setFallbackHandler, 0), false},
		{"allowed setFallbackHandler with no handler", allowedSetFallbackHandlerCheck, withData(setFallbackHandler, setFallbackHandler.data[:4]), false},

		{"setModuleGuard", setModuleGuardCheck, setModuleGuard, true},
		{"setModuleGuard to the zero address", setModuleGuardCheck, self("setModuleGuard(address)", ethrpc.Address{}), true},
		{"setModuleGuard with a truncated guard", setModuleGuardCheck, withData(setModuleGuard, setModuleGuard.data[:35]), false},
	}
	for _, test := range tests {
		got, err := test.check.fn(t.Context(), at, safe, test.c)
		if got != test.want || err != nil {
			t.Errorf("%s: check %q = %t, %v; want %t", test.name, test.check.classification(), got, err, test.want)
		}
	}
}

// TestSetModuleGuard checks that setModuleGuardCheck matches only on Safe
// 1.5.0, since earlier versions pass the call to their fallback handler.
func TestSetModuleGuard(t *testing.T) {
	tests := []struct {
		account account
		want    bool
	}{
		{proxy(safeProxy150, singletonOf("1.5.0")), true},
		{proxy(safeProxy150, singletonOf("1.5.0+L2")), true},
		{proxy(safeProxy141, singletonOf("1.4.1")), false},
		{proxy(safeProxy141, singletonOf("1.4.1+L2")), false},
		{proxy(safeProxy130, singletonOf("1.3.0")), false},
		{proxy(safeProxy130, singletonOf("1.3.0+L2")), false},
	}
	for _, test := range tests {
		safeVersions.Clear()
		safe := &safeID{address: ethrpc.Address{0: 0x5a, 19: 2}, chainID: big.NewInt(ethrpc.Gnosis)}
		node := &fakeNode{accounts: map[ethrpc.Address]account{safe.address: test.account}}
		at := &env{dial: node.dialer(t, testSafeBlock), block: testSafeBlock}
		c := call{
			to:        safe.address,
			value:     new(big.Int),
			data:      solabi.Call(solabi.Selector("setModuleGuard(address)"), ethrpc.Address{19: 0x0d}),
			operation: safenet.OperationCall,
		}
		version := singletons[ethrpc.Address(test.account.slot0[12:])]
		if got, err := setModuleGuardCheck.fn(t.Context(), at, safe, c); got != test.want || err != nil {
			t.Errorf("setModuleGuard on Safe %s = %t, %v; want %t", version, got, err, test.want)
		}
	}
}

// TestClassifySettingsChange checks settings changes through Classify, where
// the calls of a MultiSend are batched.
func TestClassifySettingsChange(t *testing.T) {
	safeVersions.Clear()
	safe := ethrpc.Address{0: 0x5a, 19: 3}
	node := &fakeNode{accounts: map[ethrpc.Address]account{safe: proxy(safeProxy141, singletonOf("1.4.1"))}}
	dial := node.dialer(t, testSafeBlock)

	disableModule := solabi.Call(solabi.Selector("disableModule(address,address)"), sentinel, ethrpc.Address{19: 0x0d})
	multiSend := ethrpc.MustParseAddress("0x38869bf66a61cF6bDB996A6aE40D5853Fd43B526")
	batch := solabi.Call(multiSendSelector, pack(call{to: safe, value: new(big.Int), data: disableModule, operation: safenet.OperationCall}))
	tests := []struct {
		name string
		tx   safenet.SafeTransaction
		want Classification
	}{
		{
			"addOwnerWithThreshold",
			safenet.SafeTransaction{Safe: safe, To: safe, Data: solabi.Call(solabi.Selector("addOwnerWithThreshold(address,uint256)"), ethrpc.Address{19: 0x0e}, uint64(1))},
			addOwnerCheck.classification(),
		},
		{"disableModule", safenet.SafeTransaction{Safe: safe, To: safe, Data: disableModule}, allowedDisableModuleCheck.classification()},
		{
			"setFallbackHandler to the zero address",
			safenet.SafeTransaction{Safe: safe, To: safe, Data: solabi.Call(solabi.Selector("setFallbackHandler(address)"), ethrpc.Address{})},
			allowedSetFallbackHandlerCheck.classification(),
		},
		{
			"disableModule with a refund",
			safenet.SafeTransaction{Safe: safe, To: safe, Data: disableModule, GasPrice: big.NewInt(1), RefundReceiver: ethrpc.Address{19: 0xee}},
			Classification{},
		},
		{
			"batched disableModule",
			safenet.SafeTransaction{Safe: safe, To: multiSend, Data: batch, Operation: safenet.OperationDelegateCall},
			disableModuleCheck.classification(),
		},
	}
	for _, test := range tests {
		if got, err := Classify(t.Context(), dial, request(test.tx)); got != test.want || err != nil {
			t.Errorf("%s: Classify() = %+v, %v; want %+v", test.name, got, err, test.want)
		}
	}
}
