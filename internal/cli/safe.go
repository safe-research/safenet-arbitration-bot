package cli

import (
	"context"
	"fmt"

	"github.com/safe-research/safenet-arbitration-bot/internal/checks"
	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/safesettings"
)

// safeReport is the JSON output of `arbot safe`.
type safeReport struct {
	Safe      ethrpc.Address `json:"safe"`
	ChainID   uint64         `json:"chainId"`
	SafeBlock uint64         `json:"safeBlock"`
	// Block is the block that the settings are read as of with -block, and is left
	// out without it, when they are read as of the Safe block.
	Block *uint64 `json:"block,omitempty"`
	*safesettings.Settings
}

// safeCommand shows the settings of the Safe of a request before its proposal,
// or of another Safe on its chain.
func safeCommand(ctx context.Context, e *env, args []string) error {
	flags := e.flagSet("safe")
	asJSON := flags.Bool("json", false, "write the settings as JSON")
	requestFile := flags.String("request-file", "", "read the Safe, its chain, and the block from the request in the JSON file at `path`, as written by arbot info -json")
	addressValue := flags.String("address", "", "show the settings of the Safe at `address` on the Safe's chain instead of the Safe's")
	blockFlag := flags.Uint64("block", 0, "read the settings as of block `N`, which can't be after the request's Safe block (default: the Safe block)")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s safe [flags] -request-file <path>\n\n", e.progname)
		fmt.Fprintf(out, "Shows the settings of the Safe of a request, as Charter § 2.10 lists them, read from the storage of\n")
		fmt.Fprintf(out, "the Safe on its chain as of the request's Safe block, without the transaction's own effects or\n")
		fmt.Fprintf(out, "anything later: its singleton, owners, threshold, nonce, enabled modules, fallback handler, guard,\n")
		fmt.Fprintf(out, "and module guard, which only Safe 1.5.0 has. The storage is read with the layout of the Safe versions\n")
		fmt.Fprintf(out, "of § 2.1, so the settings are meaningless for an account that isn't a Safe of one of them, which\n")
		fmt.Fprintf(out, "arbot calls shows. The zero address means that a setting isn't set.\n\n")
		fmt.Fprintf(out, "With -address, it shows the settings of another Safe on the Safe's chain, such as a recipient, and\n")
		fmt.Fprintf(out, "with -block, it reads them as of an earlier block. Either way, it fails for an account that arbot\n")
		fmt.Fprintf(out, "calls wouldn't identify as a Safe of a § 2.1 version, and says why: it has no code, its code isn't a\n")
		fmt.Fprintf(out, "SafeProxy of a § 2.1 proxy version, or its singleton isn't a § 2.1 singleton. arbot account shows\n")
		fmt.Fprintf(out, "what such an account is.\n\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, 0); err != nil {
		return err
	}
	address, err := addressFlag("address", *addressValue)
	if err != nil {
		return err
	}
	safe, err := openSafe(ctx, e, *requestFile)
	if err != nil {
		return err
	}
	if *blockFlag > safe.SafeBlock {
		return usageError(fmt.Sprintf("-block %d is after the request's Safe block %d", *blockFlag, safe.SafeBlock))
	}

	report := safeReport{Safe: safe.Safe, ChainID: safe.ChainID, SafeBlock: safe.SafeBlock}
	block := safe.SafeBlock
	if isSet(flags, "block") {
		block = *blockFlag
		report.Block = &block
	}
	if address != nil {
		report.Safe = *address
	}
	// Without flags, the request's Safe is read without being identified first.
	// Another account, or another block, is only read if it is a Safe whose storage
	// has settings.
	if address != nil || report.Block != nil {
		account, err := checks.NewCallReader(safe.eth, report.Safe, ethrpc.BlockNumber(block)).Safe(ctx)
		if err != nil {
			return fmt.Errorf("reading the code of %s at block %d: %w", report.Safe, block, err)
		}
		if err := notASafe(account, block); err != nil {
			return err
		}
	}
	settings, err := safesettings.Read(ctx, safe.eth, report.Safe, ethrpc.BlockNumber(block))
	if err != nil {
		return err
	}
	report.Settings = settings
	if *asJSON {
		return writeJSON(e.stdout, report)
	}
	fmt.Fprintf(e.stdout, "Safe %s on chain %d, as of block %d\n", report.Safe, report.ChainID, block)
	fmt.Fprintf(e.stdout, "  Singleton         %s\n", settings.Singleton)
	fmt.Fprintf(e.stdout, "  Threshold         %s of %s owners\n", settings.Threshold, settings.OwnerCount)
	fmt.Fprintf(e.stdout, "  Nonce             %s\n", settings.Nonce)
	fmt.Fprintf(e.stdout, "  Fallback handler  %s\n", settings.FallbackHandler)
	fmt.Fprintf(e.stdout, "  Guard             %s\n", settings.Guard)
	fmt.Fprintf(e.stdout, "  Module guard      %s\n", settings.ModuleGuard)
	for _, list := range []struct {
		name    string
		entries []ethrpc.Address
	}{{"Owners", settings.Owners}, {"Modules", settings.Modules}} {
		fmt.Fprintf(e.stdout, "\n%s\n", list.name)
		if len(list.entries) == 0 {
			fmt.Fprintf(e.stdout, "  none\n")
		}
		for _, address := range list.entries {
			fmt.Fprintf(e.stdout, "  %s\n", address)
		}
	}
	return nil
}

// notASafe returns an error with the reason if the account isn't a Safe of a §
// 2.1 version as of block, as arbot calls identifies one, since only the
// storage of such a Safe has the layout of its settings. It returns nil for a
// Safe.
func notASafe(account checks.SafeAccount, block uint64) error {
	var why string
	switch {
	case account.CodeSize == 0:
		why = "it has no code"
	case account.ProxyVersion == nil:
		why = "its code isn't that of a SafeProxy of a § 2.1 proxy version on its network"
	case account.SingletonVersion == nil:
		why = "its singleton isn't a Safe or SafeL2 singleton of a § 2.1 version, so its storage layout is unknown"
	default:
		return nil
	}
	return fmt.Errorf("%s isn't a Safe of a § 2.1 version at block %d: %s; arbot account shows what it is", account.Address, block, why)
}
