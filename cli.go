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
	// Internal modes are intentionally hidden from help and README usage docs;
	// they are argv contracts between nonet processes, not public CLI.
	internalFlag           = "--internal"
	internalProbeMode      = "probe"
	internalForwarderMode  = "forwarder"
	internalExpectUIDEnv   = "NONET_EXPECT_UID"
	internalExpectGIDEnv   = "NONET_EXPECT_GID"
	internalExpectHomeEnv  = "NONET_EXPECT_HOME"
	internalForwardTestEnv = "NONET_FORWARD_TEST_ADDRS"
)

func run(args []string) error {
	// Internal dispatch is checked before normal flag parsing so the hidden
	// re-exec modes do not become part of the public flag surface.
	if len(args) > 0 && args[0] == internalFlag {
		mode, rest, err := parseInternalArgs(args)
		if err != nil {
			return err
		}
		switch mode {
		case internalProbeMode:
			if len(rest) != 0 {
				return fmt.Errorf("%s %s does not accept arguments", internalFlag, mode)
			}
			return runInternalProbeFromEnv()
		case internalForwarderMode:
			return runInternalForwarder(rest)
		default:
			return fmt.Errorf("unknown internal mode %q", mode)
		}
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
		return runParent(runConfig{
			command:        cfg.command,
			forwardOpenTCP: cfg.forwardOpenTCP,
			debug:          cfg.debug,
		})
	}
}

func parseInternalArgs(args []string) (string, []string, error) {
	if len(args) < 2 {
		return "", nil, fmt.Errorf("%s requires a mode", internalFlag)
	}
	return args[1], args[2:], nil
}

func runInternalProbeFromEnv() error {
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

type cliConfig struct {
	command        []string
	selfTest       bool
	showHelp       bool
	forwardOpenTCP bool
	debug          bool
}

func parseCLI(args []string) (cliConfig, error) {
	cfg := cliConfig{}

	fs := flag.NewFlagSet("nonet", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&cfg.selfTest, "self-test", false, "validate runtime prerequisites and perform an end-to-end probe")
	fs.BoolVar(&cfg.forwardOpenTCP, "forward-open-tcp", false, "forward host TCP listeners bound to 127.0.0.1 and ::1")
	fs.BoolVar(&cfg.forwardOpenTCP, "F", false, "alias for --forward-open-tcp")
	fs.BoolVar(&cfg.debug, "debug", false, "print setup diagnostics to stderr")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s [options] [--] [command [args...]]\n", filepath.Base(os.Args[0]))
		fmt.Fprintln(fs.Output(), "Run a command in a fresh network namespace with loopback enabled.")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Options:")
		fmt.Fprintln(fs.Output(), "  -F, --forward-open-tcp")
		fmt.Fprintln(fs.Output(), "                 Forward current host TCP listeners on 127.0.0.1 and ::1")
		fmt.Fprintln(fs.Output(), "  --debug        Print setup diagnostics to stderr")
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
	// Self-test is a standalone diagnostic mode. Extra arguments would make it
	// ambiguous whether the user expected a diagnostic or a wrapped command.
	if cfg.selfTest && len(fs.Args()) > 0 {
		return cfg, errors.New("--self-test does not accept a command; use -- to run a command literally")
	}
	if cfg.selfTest && cfg.forwardOpenTCP {
		return cfg, errors.New("--self-test cannot be combined with --forward-open-tcp")
	}
	if cfg.selfTest && cfg.debug {
		return cfg, errors.New("--self-test cannot be combined with --debug")
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
