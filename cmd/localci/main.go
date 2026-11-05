// Command localci runs GitHub Actions jobs on the developer's machine via an
// ephemeral self-hosted runner inside a local Linux VM.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: localci [flags] <command>

commands:
  version   print the localci version

flags:
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "localci:", err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("localci", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	logLevel := fs.String("log-level", envOr("LOCALCI_LOG_LEVEL", "info"), "log level: debug, info, warn, error (env LOCALCI_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("invalid -log-level %q", *logLevel)
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	switch fs.Arg(0) {
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "":
		fs.Usage()
		return errors.New("missing command")
	default:
		logger.Debug("unknown command", "command", fs.Arg(0))
		fs.Usage()
		return fmt.Errorf("unknown command %q", fs.Arg(0))
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
