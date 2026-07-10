package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

type runConfig struct {
	command        []string
	forwardOpenTCP bool
	debug          bool
}

type childSetupStatus struct {
	targetStarted bool
	exitCode      int
}

type targetExitError struct {
	exitCode int
	signal   syscall.Signal
}

func (e *targetExitError) Error() string {
	if e.signal != 0 {
		return fmt.Sprintf("target terminated by signal %s", e.signal)
	}
	return fmt.Sprintf("target exited with status %d", e.exitCode)
}

func runParent(cfg runConfig) error {
	return runWithEnv(cfg, os.Environ())
}

func runWithEnv(cfg runConfig, env []string) error {
	cfg.debugf("command args: %s", formatCommandArgs(cfg.command))
	resolvedArgs, err := resolveCommand(cfg.command)
	if err != nil {
		cfg.debugf("resolve command failed: %v", err)
		return err
	}
	cfg.debugf("resolved command: %s", formatCommandArgs(resolvedArgs))

	uid := os.Getuid()
	gid := os.Getgid()
	cfg.debugf("caller identity: uid=%d gid=%d", uid, gid)

	forwardSpec := ""
	var forwardSpecs []tcpForwardSpec
	var forwardControl *forwardControl
	if cfg.forwardOpenTCP {
		cfg.debugf("TCP loopback forwarding: enabled")
		specs, err := snapshotOpenTCPForwards()
		if err != nil {
			cfg.debugf("TCP loopback forwarding snapshot failed: %v", err)
			return err
		}
		cfg.debugf("TCP loopback forwarding snapshot: %s", formatTCPForwardSpecs(specs))
		forwardSpecs = specs
		forwardSpec = encodeTCPForwardSpecs(specs)
		if forwardSpec != "" {
			forwardControl, err = newForwardControl()
			if err != nil {
				cfg.debugf("create forwarding control socket failed: %v", err)
				return err
			}
			defer forwardControl.closeParent()
			cfg.debugf("forwarding control socket created")
		} else {
			cfg.debugf("TCP loopback forwarding snapshot is empty; continuing without forwarding")
		}
	} else {
		cfg.debugf("TCP loopback forwarding: disabled")
	}

	setupReader, setupWriter, err := os.Pipe()
	if err != nil {
		cfg.debugf("create setup status pipe failed: %v", err)
		return fmt.Errorf("create setup status pipe: %w", err)
	}
	defer setupReader.Close()
	defer setupWriter.Close()
	// The child reports pre-exec failures through this descriptor. Its close-on-
	// exec flag makes EOF an unambiguous signal that the target was started.
	syscall.CloseOnExec(int(setupWriter.Fd()))
	cfg.debugf("setup status pipe created")

	syncReader, syncWriter, err := os.Pipe()
	if err != nil {
		cfg.debugf("create sync pipe failed: %v", err)
		return fmt.Errorf("create sync pipe: %w", err)
	}
	defer syncReader.Close()
	defer syncWriter.Close()
	cfg.debugf("sync pipe created")

	// The child starts in a fresh user namespace and blocks on this pipe. That
	// gives the parent a window to install uid_map/gid_map before the child
	// continues into network namespace setup.
	forwardFD := -1
	if forwardControl != nil {
		forwardFD = forwardControl.childFD()
	}
	child, err := spawnInUserNamespace(resolvedArgs, env, int(syncReader.Fd()), forwardFD, int(setupWriter.Fd()), forwardSpec)
	if err != nil {
		if forwardControl != nil {
			forwardControl.closeChild()
		}
		cfg.debugf("spawn helper failed: %v", err)
		return err
	}
	defer child.close()
	cfg.debugf("spawned helper: pid=%d", child.pid)
	syncReader.Close()
	setupWriter.Close()
	if forwardControl != nil {
		forwardControl.closeChild()
	}

	if err := installIdentityMappings(child.pid, uid, gid); err != nil {
		// If mapping setup fails, the child is still blocked on the sync pipe.
		// Kill and reap it so a paused helper is not left behind.
		child.kill()
		_, _ = child.wait()
		cfg.debugf("install identity mappings failed: %v", err)
		return err
	}
	cfg.debugf("installed identity mappings for pid=%d", child.pid)
	if _, err := syncWriter.Write([]byte{1}); err != nil {
		// A failed release write leaves the child unable to make progress, so
		// treat it the same as mapping setup failure.
		child.kill()
		_, _ = child.wait()
		cfg.debugf("release helper failed: %v", err)
		return fmt.Errorf("release helper: %w", err)
	}
	syncWriter.Close()
	cfg.debugf("released helper")

	var bridge *forwardBridge
	var bridgeSetupErr error
	if forwardControl != nil {
		cfg.debugf("waiting for forwarding listener handoff")
		bridge, err = forwardControl.startHostBridge(forwardSpecs)
		forwardControl.closeParent()
		if err != nil {
			cfg.debugf("TCP loopback forwarding setup failed: %v", err)
			bridgeSetupErr = fmt.Errorf("set up TCP loopback forwarding: %w", err)
		} else {
			cfg.debugf("started host forwarding bridge")
		}
	}

	setupStatus, setupErr := readChildSetupStatus(setupReader)
	if setupErr != nil {
		child.kill()
		_, _ = child.wait()
		if bridge != nil {
			bridge.close()
			_ = bridge.wait()
		}
		return setupErr
	}
	if bridgeSetupErr != nil && setupStatus.targetStarted {
		child.kill()
	}

	var waitStatus syscall.WaitStatus
	var waitErr error
	if setupStatus.targetStarted && bridgeSetupErr == nil {
		waitStatus, waitErr = waitForTarget(child)
	} else {
		waitStatus, waitErr = child.wait()
	}
	if waitErr != nil {
		err = fmt.Errorf("wait for command: %w", waitErr)
	} else {
		err = classifyChildStatus(waitStatus, setupStatus)
	}
	if bridgeSetupErr != nil && setupStatus.targetStarted {
		err = bridgeSetupErr
	}
	if err != nil {
		cfg.debugf("wrapped command finished with error: %v", err)
	} else {
		cfg.debugf("wrapped command finished successfully")
	}
	if bridge != nil {
		cfg.debugf("closing host forwarding bridge")
		bridge.close()
		bridgeErr := bridge.wait()
		if bridgeErr != nil {
			cfg.debugf("host forwarding bridge finished with error: %v", bridgeErr)
		} else {
			cfg.debugf("host forwarding bridge stopped")
		}
		if err == nil && bridgeErr != nil {
			err = bridgeErr
		}
	}
	return err
}

func readChildSetupStatus(reader io.Reader) (childSetupStatus, error) {
	var code [1]byte
	n, err := reader.Read(code[:])
	if errors.Is(err, io.EOF) && n == 0 {
		return childSetupStatus{targetStarted: true}, nil
	}
	if err != nil {
		return childSetupStatus{}, fmt.Errorf("read child setup status: %w", err)
	}
	if n != len(code) {
		return childSetupStatus{}, errors.New("read child setup status: short message")
	}
	return childSetupStatus{exitCode: int(code[0])}, nil
}

func classifyChildStatus(status syscall.WaitStatus, setup childSetupStatus) error {
	if !setup.targetStarted {
		if msg, ok := childExitDescription(setup.exitCode); ok {
			return errors.New(msg)
		}
		return fmt.Errorf("helper failed before exec with status %d", setup.exitCode)
	}
	if status.Signaled() {
		return &targetExitError{signal: status.Signal()}
	}
	if status.Exited() && status.ExitStatus() != 0 {
		return &targetExitError{exitCode: status.ExitStatus()}
	}
	return nil
}

type childWaitResult struct {
	status syscall.WaitStatus
	err    error
}

func waitForTarget(child *spawnedChild) (syscall.WaitStatus, error) {
	termCh := make(chan os.Signal, 1)
	signal.Notify(termCh, syscall.SIGTERM)
	defer signal.Stop(termCh)
	return waitForTargetSignal(child, termCh)
}

func waitForTargetSignal(child *spawnedChild, termCh <-chan os.Signal) (syscall.WaitStatus, error) {
	waitCh := make(chan childWaitResult, 1)
	go func() {
		status, err := child.wait()
		waitCh <- childWaitResult{status: status, err: err}
	}()

	for {
		select {
		case <-termCh:
			if err := child.signal(syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
				child.kill()
				result := <-waitCh
				if result.err != nil {
					return result.status, result.err
				}
				return result.status, fmt.Errorf("forward SIGTERM to command: %w", err)
			}
		case result := <-waitCh:
			return result.status, result.err
		}
	}
}

func (cfg runConfig) debugf(format string, args ...any) {
	if cfg.debug {
		fmt.Fprintf(os.Stderr, "nonet debug: "+format+"\n", args...)
	}
}

func formatCommandArgs(args []string) string {
	if len(args) == 0 {
		return "<none>"
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, strconv.Quote(arg))
	}
	return strings.Join(quoted, " ")
}

func resolveCommand(commandArgs []string) ([]string, error) {
	if len(commandArgs) == 0 {
		return nil, errors.New("missing command")
	}
	if strings.ContainsRune(commandArgs[0], '/') {
		return commandArgs, nil
	}
	path, err := exec.LookPath(commandArgs[0])
	if err != nil {
		return nil, fmt.Errorf("resolve command %q: %w", commandArgs[0], err)
	}
	resolved := append([]string{path}, commandArgs[1:]...)
	return resolved, nil
}

func installIdentityMappings(pid, uid, gid int) error {
	if err := writeProcMap(fmt.Sprintf("/proc/%d/uid_map", pid), identityMapContent(uid)); err != nil {
		return fmt.Errorf("install uid map: %w", err)
	}
	// Unprivileged gid_map writes require setgroups to be permanently disabled
	// first. Older kernels may not expose this file, so ENOENT is tolerated.
	if err := os.WriteFile(fmt.Sprintf("/proc/%d/setgroups", pid), []byte("deny\n"), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("disable setgroups for gid map: %w", err)
	}
	if err := writeProcMap(fmt.Sprintf("/proc/%d/gid_map", pid), identityMapContent(gid)); err != nil {
		return fmt.Errorf("install gid map: %w", err)
	}
	return nil
}

func identityMapContent(id int) string {
	// The current design intentionally installs a single "identity" mapping for
	// the calling uid/gid only. Richer multi-entry mappings would need
	// privileged helpers such as newuidmap/newgidmap.
	return fmt.Sprintf("%d %d 1\n", id, id)
}

func writeProcMap(path, content string) error {
	return os.WriteFile(path, []byte(content), 0)
}
