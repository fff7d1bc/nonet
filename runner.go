package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func runParent(commandArgs []string) error {
	return runWithEnv(commandArgs, os.Environ())
}

func runWithEnv(commandArgs, env []string) error {
	resolvedArgs, err := resolveCommand(commandArgs)
	if err != nil {
		return err
	}

	uid := os.Getuid()
	gid := os.Getgid()

	syncReader, syncWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create sync pipe: %w", err)
	}
	defer syncReader.Close()
	defer syncWriter.Close()

	child, err := spawnInUserNamespace(resolvedArgs, env, int(syncReader.Fd()))
	if err != nil {
		return err
	}
	defer child.close()
	syncReader.Close()

	if err := installIdentityMappings(child.pid, uid, gid); err != nil {
		child.kill()
		_ = child.wait()
		return err
	}
	if _, err := syncWriter.Write([]byte{1}); err != nil {
		child.kill()
		_ = child.wait()
		return fmt.Errorf("release helper: %w", err)
	}
	syncWriter.Close()

	return child.wait()
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
	if err := os.WriteFile(fmt.Sprintf("/proc/%d/setgroups", pid), []byte("deny\n"), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("disable setgroups for gid map: %w", err)
	}
	if err := writeProcMap(fmt.Sprintf("/proc/%d/gid_map", pid), identityMapContent(gid)); err != nil {
		return fmt.Errorf("install gid map: %w", err)
	}
	return nil
}

func identityMapContent(id int) string {
	return fmt.Sprintf("%d %d 1\n", id, id)
}

func writeProcMap(path, content string) error {
	return os.WriteFile(path, []byte(content), 0)
}
