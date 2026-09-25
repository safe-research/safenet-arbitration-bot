package checks

import (
	"context"
	"slices"
	"sync"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
)

// safeProxyCodeHashes are the Keccak-256 hashes of the code of the SafeProxy
// contracts of the Safe versions that § 2.1 lists, 1.3.0, 1.4.1, and 1.5.0, as
// their proxy factories deploy them. A proxy has the same code however it was
// deployed, since its code doesn't depend on its singleton, which it keeps in
// storage.
var safeProxyCodeHashes = []ethrpc.Hash{
	ethrpc.MustParseHash("0xb89c1b3bdf2cf8827818646bce9a8f6e372885f8c55e5c07acbd307cb133b000"),
	ethrpc.MustParseHash("0xd7d408ebcd99b2b70be43e20253d6d92a8ea8fab29bd3be7f55b10032331fb4c"),
	ethrpc.MustParseHash("0x4e381985ca68b3e5d27b4425fa581c19cf33146d3f887a3cfca96f55528ea46f"),
}

// singletons are the Safe and SafeL2 singletons of the Safe versions that the
// Charter lists in § 2.1, by version, with "+L2" for SafeL2. They are listed in
// the safe-deployments repository at
// https://github.com/safe-global/safe-deployments, which lists each of them on
// every network that the Charter covers. They are deterministic deployments, so
// an address has either the singleton's code or none at all.
var singletons = map[ethrpc.Address]string{
	ethrpc.MustParseAddress("0xd9Db270c1B5E3Bd161E8c8503c55cEABeE709552"): "1.3.0",
	ethrpc.MustParseAddress("0x69f4D1788e39c87893C980c06EdF4b7f686e2938"): "1.3.0",
	ethrpc.MustParseAddress("0x3E5c63644E683549055b9Be8653de26E0B4CD36E"): "1.3.0+L2",
	ethrpc.MustParseAddress("0xfb1bffC9d739B8D520DaF37dF666da4C687191EA"): "1.3.0+L2",
	ethrpc.MustParseAddress("0x41675C099F32341bf84BFc5382aF534df5C7461a"): "1.4.1",
	ethrpc.MustParseAddress("0x29fcB43b46531BcA003ddC8FCB67FFE91900C762"): "1.4.1+L2",
	ethrpc.MustParseAddress("0xFf51A5898e281Db6DfC7855790607438dF2ca44b"): "1.5.0",
	ethrpc.MustParseAddress("0xEdd160fEBBD92E350D4D398fb636302fccd67C7e"): "1.5.0+L2",
}

// unsupportedSafe finds a call by an account that isn't a Safe of a version
// that § 2.1 lists out of scope, since the Council considers only Safes that
// use the official Safe smart account versions 1.3.0, 1.4.1, and 1.5.0 (§ 2.1
// and § 3.9).
//
// An account is such a Safe if, at the block before the proposal, it has the
// code of a SafeProxy in safeProxyCodeHashes, and its singleton, in storage
// slot 0, is one of singletons. The proxy doesn't need to have been deployed by
// an official proxy factory. The proxy delegates calls to the address in the
// low 20 bytes of the slot, so the others are ignored. It runs after
// offNetwork, so the Safe is on a network that the Charter covers.
var unsupportedSafe = check{
	verdict:     OutOfScope,
	description: "account that isn't a Safe of a version that the Charter covers",
	fn: func(ctx context.Context, env *env, safe *safeID, _ call) (bool, error) {
		version, err := safeVersion(ctx, env, safe)
		return version == "", err
	},
}

// safeAt identifies an account on a chain at a block.
type safeAt struct {
	address ethrpc.Address
	chainID uint64
	block   uint64
}

// safeVersions caches safeVersion by safeAt, since checks run on every call of
// a transaction, and a chain's state at a block doesn't change.
var safeVersions sync.Map

// safeVersion returns the version of the Safe, as singletons has it, at env's
// block, or "" if the account isn't a Safe of a version that § 2.1 lists. The
// Safe must be on a network that the Charter covers, as it is for every check
// after offNetwork.
func safeVersion(ctx context.Context, env *env, safe *safeID) (string, error) {
	key := safeAt{address: safe.address, chainID: safe.chainID.Uint64(), block: env.block}
	if version, ok := safeVersions.Load(key); ok {
		return version.(string), nil
	}
	client, err := env.dial(ctx, key.chainID)
	if err != nil {
		return "", err
	}
	// The account is read with eth_getCode and eth_getStorageAt rather than with a
	// single eth_getProof, since fewer nodes serve eth_getProof at older blocks.
	block := ethrpc.BlockNumber(env.block)
	code, err := client.GetCode(ctx, safe.address, block)
	if err != nil {
		return "", err
	}
	var version string
	if slices.Contains(safeProxyCodeHashes, ethrpc.Hash(keccak256.Hash(code))) {
		slot0, err := client.GetStorageAt(ctx, safe.address, ethrpc.Hash{}, block)
		if err != nil {
			return "", err
		}
		version = singletons[ethrpc.Address(slot0[12:])]
	}
	safeVersions.Store(key, version)
	return version, nil
}
