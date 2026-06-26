package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type runConfig struct {
	command        []string
	forwardOpenTCP bool
	debug          bool
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
	var forwardControl *forwardControl
	if cfg.forwardOpenTCP {
		cfg.debugf("TCP loopback forwarding: enabled")
		specs, err := snapshotOpenTCPForwards()
		if err != nil {
			cfg.debugf("TCP loopback forwarding snapshot failed: %v", err)
			return err
		}
		cfg.debugf("TCP loopback forwarding snapshot: %s", formatTCPForwardSpecs(specs))
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
	child, err := spawnInUserNamespace(resolvedArgs, env, int(syncReader.Fd()), forwardFD, forwardSpec)
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
	if forwardControl != nil {
		forwardControl.closeChild()
	}

	if err := installIdentityMappings(child.pid, uid, gid); err != nil {
		// If mapping setup fails, the child is still blocked on the sync pipe.
		// Kill and reap it so a paused helper is not left behind.
		child.kill()
		_ = child.wait()
		cfg.debugf("install identity mappings failed: %v", err)
		return err
	}
	cfg.debugf("installed identity mappings for pid=%d", child.pid)
	if _, err := syncWriter.Write([]byte{1}); err != nil {
		// A failed release write leaves the child unable to make progress, so
		// treat it the same as mapping setup failure.
		child.kill()
		_ = child.wait()
		cfg.debugf("release helper failed: %v", err)
		return fmt.Errorf("release helper: %w", err)
	}
	syncWriter.Close()
	cfg.debugf("released helper")

	var bridgeDone <-chan error
	if forwardControl != nil {
		bridgeDone = forwardControl.startHostBridge()
		cfg.debugf("started host forwarding bridge")
	}

	err = child.wait()
	if err != nil {
		cfg.debugf("wrapped command finished with error: %v", err)
	} else {
		cfg.debugf("wrapped command finished successfully")
	}
	if forwardControl != nil {
		forwardControl.closeParent()
		if bridgeErr := <-bridgeDone; err == nil && bridgeErr != nil {
			cfg.debugf("host forwarding bridge finished with error: %v", bridgeErr)
			err = bridgeErr
		}
	}
	return err
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
