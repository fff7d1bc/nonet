package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	exitMain(run(os.Args[1:]))
}

func exitMain(err error) {
	if err == nil {
		return
	}
	var targetErr *targetExitError
	if errors.As(err, &targetErr) {
		if targetErr.signal == 0 {
			os.Exit(targetErr.exitCode)
		}
		// Terminate the wrapper the same way as the target so launchers can
		// distinguish signal death from an ordinary nonzero exit.
		signal.Reset(targetErr.signal)
		if err := syscall.Kill(os.Getpid(), targetErr.signal); err == nil {
			for {
				_ = syscall.Pause()
			}
		}
		os.Exit(128 + int(targetErr.signal))
	}
	fmt.Fprintf(os.Stderr, "nonet: %v\n", err)
	os.Exit(1)
}
