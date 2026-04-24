package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// The self-test re-execs the same binary with this hidden marker instead of
	// exposing an internal public flag just for probe mode.
	internalProbeCommand  = "__nonet_internal_probe__"
	internalExpectUIDEnv  = "NONET_EXPECT_UID"
	internalExpectGIDEnv  = "NONET_EXPECT_GID"
	internalExpectHomeEnv = "NONET_EXPECT_HOME"
)

func run(args []string) error {
	// Internal probe mode is intentionally checked before normal flag parsing so
	// the public CLI surface stays minimal.
	if len(args) > 0 && args[0] == internalProbeCommand {
		expectUID, err := internalIntEnv(internalExpectUIDEnv)
		if err != nil {
			return err
		}
		expectGID, err := internalIntEnv(internalExpectGIDEnv)
		if err != nil {
			return err
		}
		return runInternalProbe(expectUID, expectGID, os.Getenv(internalExpectHomeEnv))
	}

	cfg, err := parseCLI(args)
	if err != nil {
		return err
	}

	switch {
	case cfg.showHelp:
		return nil
	case cfg.selfTest:
		return runSelfTest()
	default:
		return runParent(cfg.command)
	}
}

type cliConfig struct {
	command  []string
	selfTest bool
	showHelp bool
}

func parseCLI(args []string) (cliConfig, error) {
	cfg := cliConfig{}

	fs := flag.NewFlagSet("nonet", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&cfg.selfTest, "self-test", false, "validate runtime prerequisites and perform an end-to-end probe")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s [options] [--] [command [args...]]\n", filepath.Base(os.Args[0]))
		fmt.Fprintln(fs.Output(), "Run a command in a fresh network namespace with loopback enabled.")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Options:")
		fmt.Fprintln(fs.Output(), "  --self-test    Validate runtime support and perform an end-to-end probe")
		fmt.Fprintln(fs.Output(), "  -h, --help     Show this help")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Use -- to stop option parsing if the command name starts with '-'.")
	}
	if wantsHelp(args) {
		fs.Usage()
		cfg.showHelp = true
		return cfg, nil
	}

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if cfg.selfTest && len(fs.Args()) > 0 {
		return cfg, errors.New("--self-test does not accept a command; use -- to run a command literally")
	}

	cfg.command = commandOrShell(fs.Args())
	return cfg, nil
}

func wantsHelp(args []string) bool {
	for _, arg := range args {
		// Respect "--" as the standard "end of options" marker so a literal
		// command named "-h" or "--help" is still runnable.
		if arg == "--" {
			return false
		}
		if arg == "-h" || arg == "--help" || arg == "-help" {
			return true
		}
	}
	return false
}

func internalIntEnv(name string) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return -1, fmt.Errorf("missing internal environment variable %s", name)
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func commandOrShell(args []string) []string {
	if len(args) > 0 {
		return args
	}
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return []string{shell}
	}
	return []string{"/bin/sh"}
}
