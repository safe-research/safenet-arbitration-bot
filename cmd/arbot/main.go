// Command arbot is the Safenet arbitration bot's command-line tool.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	progname := filepath.Base(os.Args[0])

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s <command> [arguments]\n", progname)
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr, "%s: unknown command %q\n", progname, flag.Arg(0))
	os.Exit(2)
}
