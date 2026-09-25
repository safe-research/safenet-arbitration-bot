package checks

import (
	"context"
	"strings"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safenet"
	"github.com/safe-research/safenet-arbitration-bot/internal/solabi"
)

// The settings-change checks find a call by the Safe to one of its own
// functions that change a setting insecure under R-4.1: "A transaction is
// insecure if it modifies any Safe setting (§ 2.10), unless it falls within an
// allowed exception". The functions require the Safe to call itself, so only a
// call to the Safe matches, and a delegate call, which runs the function in the
// Safe without changing the caller, is left to R-4.2. No function of a § 2.1
// version changes the singleton other than by a delegate call.
//
// A check matches a call to its function whatever the call's value or
// arguments, even if it would revert in the Safe's state at safeBlock, since a
// module transaction can change the state before the transaction executes, so
// that the call succeeds. It abstains only on a call that reverts whatever the
// state: one whose data is too short for the function's arguments, which the
// Safe's ABI decoder rejects, or one that adds the zero address or the sentinel
// as an owner. Data after the arguments is ignored, so a call with more data
// still matches.
var (
	addOwnerCheck = settingsChange(
		"addOwnerWithThreshold(address,uint256)",
		"calls addOwnerWithThreshold on the Safe",
		addsInvalidOwner(0),
	)
	removeOwnerCheck = settingsChange(
		"removeOwner(address,address,uint256)",
		"calls removeOwner on the Safe",
		nil,
	)
	swapOwnerCheck = settingsChange(
		"swapOwner(address,address,address)",
		"calls swapOwner on the Safe",
		addsInvalidOwner(2),
	)
	changeThresholdCheck = settingsChange(
		"changeThreshold(uint256)",
		"calls changeThreshold on the Safe",
		nil,
	)
	enableModuleCheck = settingsChange(
		"enableModule(address)",
		"calls enableModule on the Safe",
		nil,
	)
	// R-4.1 allows disableModule with any valid parameter.
	disableModuleCheck = settingsChange(
		"disableModule(address,address)",
		"calls disableModule on the Safe outside the allowed exception",
		allowed(allowedDisableModule),
	)
	setGuardCheck = settingsChange(
		"setGuard(address)",
		"calls setGuard on the Safe",
		nil,
	)
	// R-4.1 allows setFallbackHandler with the zero address as the handler.
	setFallbackHandlerCheck = settingsChange(
		"setFallbackHandler(address)",
		"calls setFallbackHandler on the Safe outside the allowed exception",
		allowed(allowedSetFallbackHandler),
	)
	// Safes before 1.5.0 have no setModuleGuard, and pass a call to it to their
	// fallback handler, which can do anything, so the check abstains on them.
	setModuleGuardCheck = settingsChange(
		"setModuleGuard(address)",
		"calls setModuleGuard on a Safe 1.5.0",
		func(ctx context.Context, env *env, safe *safeID, _ call) (bool, error) {
			version, err := safeVersion(ctx, env, safe)
			return version != "1.5.0" && version != "1.5.0+L2", err
		},
	)
)

// The allowed settings-change checks find a call by the Safe to one of its own
// functions that R-4.1 allows secure. An allowed call is the Safe transaction's
// own call, so no other call is batched with it, it sends no value, and it only
// removes a module or the fallback handler, which grants nothing, calls no
// other contract, and doesn't delegate call. It therefore has none of the
// effects that the rules of Article IV consider, whatever the gas refund does.
var (
	allowedDisableModuleCheck = check{
		verdict:     Secure,
		description: "calls disableModule on the Safe as R-4.1 allows",
		fn:          allowedSettingsChange("disableModule(address,address)", allowedDisableModule),
	}
	allowedSetFallbackHandlerCheck = check{
		verdict:     Secure,
		description: "calls setFallbackHandler on the Safe to remove the handler as R-4.1 allows",
		fn:          allowedSettingsChange("setFallbackHandler(address)", allowedSetFallbackHandler),
	}
)

// allowedDisableModule reports whether a call to disableModule on the Safe is
// in the form that R-4.1 allows, with any valid parameter.
func allowedDisableModule(c call) bool {
	return canonicalRootCall(c, 2)
}

// allowedSetFallbackHandler reports whether a call to setFallbackHandler on the
// Safe is in the form that R-4.1 allows, with the zero address as the handler.
func allowedSetFallbackHandler(c call) bool {
	return canonicalRootCall(c, 1) && [32]byte(c.data[4:]) == [32]byte{}
}

// allowed returns a function that reports whether a call to a settings-change
// function on the Safe is an allowed exception to R-4.1, as isAllowed reports.
func allowed(isAllowed func(c call) bool) func(context.Context, *env, *safeID, call) (bool, error) {
	return func(_ context.Context, _ *env, _ *safeID, c call) (bool, error) {
		return isAllowed(c), nil
	}
}

// allowedSettingsChange returns a check function that reports whether a call is
// a call by the Safe to its own function with signature that isAllowed reports
// is an allowed exception to R-4.1.
func allowedSettingsChange(
	signature string,
	isAllowed func(c call) bool,
) func(context.Context, *env, *safeID, call) (bool, error) {
	selector := solabi.Selector(signature)
	return func(_ context.Context, _ *env, safe *safeID, c call) (bool, error) {
		return selfCall(safe, c, selector) && isAllowed(c), nil
	}
}

// selfCall reports whether c is a call by the Safe to its own function with
// selector.
func selfCall(safe *safeID, c call, selector [4]byte) bool {
	return c.to == safe.address && c.operation == safenet.OperationCall && len(c.data) >= 4 &&
		[4]byte(c.data[:4]) == selector
}

// settingsChange returns a check that finds a call by the Safe to its own
// function with signature insecure under R-4.1, with description. The function
// must only have static arguments, which are 32 bytes each. The check abstains
// on a call whose data is too short for them, which reverts, or if abstain
// reports that it should, such as for an allowed exception.
func settingsChange(
	signature, description string,
	abstain func(ctx context.Context, env *env, safe *safeID, c call) (bool, error),
) check {
	selector := solabi.Selector(signature)
	args := strings.Count(signature, ",") + 1
	if strings.HasSuffix(signature, "()") {
		args = 0
	}
	return check{
		verdict:     Insecure,
		rule:        "R-4.1",
		description: description,
		fn: func(ctx context.Context, env *env, safe *safeID, c call) (bool, error) {
			if !selfCall(safe, c, selector) || len(c.data) < 4+32*args {
				return false, nil
			}
			if abstain == nil {
				return true, nil
			}
			skip, err := abstain(ctx, env, safe, c)
			return !skip && err == nil, err
		},
	}
}

// sentinel is the address that the Safe uses as the head of its owner and
// module lists, which can't be an owner or a module.
var sentinel = ethrpc.Address{19: 1}

// addsInvalidOwner returns a function that reports whether a call adds the
// owner in its argument with index i, which is the zero address or the
// sentinel, and so reverts whatever the state. The Safe's ABI decoder takes an
// address from the low 20 bytes of its argument, ignoring the others. The call
// has data for all of its arguments.
func addsInvalidOwner(i int) func(context.Context, *env, *safeID, call) (bool, error) {
	return func(_ context.Context, _ *env, _ *safeID, c call) (bool, error) {
		start := 4 + 32*i
		owner := ethrpc.Address(c.data[start+12 : start+32])
		return owner == (ethrpc.Address{}) || owner == sentinel, nil
	}
}

// canonicalRootCall reports whether c meets the conditions of R-4.1's allowed
// exception that don't depend on the function: it is the Safe transaction's own
// call rather than a batched call or the refund, it sends no value, and its
// data is the canonical ABI encoding of a call with args address arguments,
// with no trailing bytes and zero upper bytes in each argument. The caller
// checks that it is a call to the Safe's own function, with selfCall, and any
// conditions on the arguments' values.
func canonicalRootCall(c call, args int) bool {
	if c.kind != Root || c.value.Sign() != 0 || len(c.data) != 4+32*args {
		return false
	}
	for i := range args {
		if [12]byte(c.data[4+32*i:]) != [12]byte{} {
			return false
		}
	}
	return true
}
