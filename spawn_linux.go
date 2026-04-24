package main

/*
#define _GNU_SOURCE
#include <errno.h>
#include <grp.h>
#include <sched.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>
#include <net/if.h>

#ifndef STACK_SIZE
#define STACK_SIZE (1024 * 1024)
#endif

struct child_state {
	int sync_fd;
	char **argv;
	char **envp;
};

static int set_loopback_up(void) {
	int fd = socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0);
	if (fd < 0) {
		return errno;
	}

	struct ifreq ifr;
	memset(&ifr, 0, sizeof(ifr));
	strncpy(ifr.ifr_name, "lo", IFNAMSIZ - 1);

	if (ioctl(fd, SIOCGIFFLAGS, &ifr) < 0) {
		int err = errno;
		close(fd);
		return err;
	}

	ifr.ifr_flags |= IFF_UP;
	if (ioctl(fd, SIOCSIFFLAGS, &ifr) < 0) {
		int err = errno;
		close(fd);
		return err;
	}

	close(fd);
	return 0;
}

static int child_main(void *arg) {
	struct child_state *state = (struct child_state *)arg;
	char byte;

	// Block immediately until the Go parent installs uid/gid mappings for this
	// just-cloned child. Doing the wait here keeps the race window tiny.
	if (read(state->sync_fd, &byte, 1) != 1) {
		_exit(200);
	}
	close(state->sync_fd);

	if (unshare(CLONE_NEWNET) != 0) {
		_exit(201);
	}
	int err = set_loopback_up();
	if (err != 0) {
		_exit(202);
	}
	execve(state->argv[0], state->argv, state->envp);
	_exit(203);
}

static int nonet_spawn_child(char **argv, char **envp, int sync_fd, pid_t *pid_out, void **stack_out, void **state_out) {
	void *stack = malloc(STACK_SIZE);
	if (stack == NULL) {
		return ENOMEM;
	}

	struct child_state *state = malloc(sizeof(struct child_state));
	if (state == NULL) {
		free(stack);
		return ENOMEM;
	}

	state->sync_fd = sync_fd;
	state->argv = argv;
	state->envp = envp;

	// clone(2) is used here instead of Go's higher-level process APIs because
	// nonet needs a child that starts in CLONE_NEWUSER and then pauses before
	// the parent writes procfs mappings.
	pid_t pid = clone(child_main, (char *)stack + STACK_SIZE, CLONE_NEWUSER | SIGCHLD, state);
	if (pid < 0) {
		int err = errno;
		free(state);
		free(stack);
		return err;
	}

	*pid_out = pid;
	*stack_out = stack;
	*state_out = state;
	return 0;
}

static void nonet_free_stack(void *stack) {
	free(stack);
}

static void nonet_free_state(void *state) {
	free(state);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	childExitWaitSync   = 200
	childExitUnshareNet = 201
	childExitLoopbackUp = 202
	childExitExecTarget = 203
)

type spawnedChild struct {
	pid   int
	stack unsafe.Pointer
	state unsafe.Pointer
	argv  []*C.char
	envp  []*C.char
}

func spawnInUserNamespace(commandArgs, env []string, syncFD int) (*spawnedChild, error) {
	// Keep argv/envp in C memory for the lifetime of the helper process because
	// child_main ultimately passes them straight to execve(2).
	cargv := make([]*C.char, 0, len(commandArgs)+1)
	for _, arg := range commandArgs {
		cargv = append(cargv, C.CString(arg))
	}
	cargv = append(cargv, nil)

	cenv := make([]*C.char, 0, len(env)+1)
	for _, value := range env {
		cenv = append(cenv, C.CString(value))
	}
	cenv = append(cenv, nil)

	var pid C.pid_t
	var stack unsafe.Pointer
	var state unsafe.Pointer
	rc := C.nonet_spawn_child((**C.char)(unsafe.Pointer(&cargv[0])), (**C.char)(unsafe.Pointer(&cenv[0])), C.int(syncFD), &pid, &stack, &state)
	if rc != 0 {
		for _, arg := range cargv[:len(cargv)-1] {
			C.free(unsafe.Pointer(arg))
		}
		for _, value := range cenv[:len(cenv)-1] {
			C.free(unsafe.Pointer(value))
		}
		return nil, fmt.Errorf("spawn helper: %w", syscall.Errno(rc))
	}

	return &spawnedChild{
		pid:   int(pid),
		stack: stack,
		state: state,
		argv:  cargv[:len(cargv)-1],
		envp:  cenv[:len(cenv)-1],
	}, nil
}

func (c *spawnedChild) close() {
	for _, arg := range c.argv {
		C.free(unsafe.Pointer(arg))
	}
	for _, value := range c.envp {
		C.free(unsafe.Pointer(value))
	}
	if c.stack != nil {
		C.nonet_free_stack(c.stack)
		c.stack = nil
	}
	if c.state != nil {
		C.nonet_free_state(c.state)
		c.state = nil
	}
}

func (c *spawnedChild) kill() {
	if c.pid > 0 {
		_ = syscall.Kill(c.pid, syscall.SIGKILL)
	}
}

func (c *spawnedChild) wait() error {
	var status syscall.WaitStatus
	_, err := syscall.Wait4(c.pid, &status, 0, nil)
	if err != nil {
		return err
	}
	if status.Signaled() {
		return fmt.Errorf("run command: terminated by signal %s", status.Signal())
	}
	if status.Exited() {
		code := status.ExitStatus()
		if code == 0 {
			return nil
		}
		// The helper exits with reserved codes for setup failures so the Go side
		// can return a readable error instead of just "exit status N".
		if msg, ok := childExitDescription(code); ok {
			return errors.New(msg)
		}
		os.Exit(code)
	}
	return nil
}

func childExitDescription(code int) (string, bool) {
	switch code {
	case childExitWaitSync:
		return "helper failed while waiting for namespace mappings from parent", true
	case childExitUnshareNet:
		return "helper could not create a new network namespace; likely blocked by container/runtime policy or missing namespace privileges", true
	case childExitLoopbackUp:
		return "helper created the network namespace but could not bring loopback up", true
	case childExitExecTarget:
		return "helper failed to exec the requested command", true
	default:
		return "", false
	}
}
