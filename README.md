# nonet

`nonet` runs a command without access to the outside network, but with a functional loopback interface inside its own network namespace.

The intended result is close to `unshare -c -n`, except that `nonet` also brings `lo` up while still leaving the final command with the caller's visible UID/GID.

With plain `unshare`, the tradeoff is usually:

- `unshare -c -n` keeps your visible UID/GID, but the final command cannot bring `lo` up
- `unshare -r -n` lets you bring `lo` up, but changes the visible identity to namespace-root

`nonet` is meant to give you:

- no outside network access
- working isolated loopback
- the final command still running as your normal visible user

This is a convenience/testing tool, not a security boundary.

## Usage

Run a command:

```sh
nonet <command> [args...]
```

Run a shell:

```sh
nonet
```

Stop option parsing:

```sh
nonet -- --test
```

That executes a command literally named `--test`.

Run the built-in runtime check:

```sh
nonet --self-test
```

## Operation

Inside `nonet`:

- the process has its own network namespace
- `lo` exists and is brought up automatically
- `127.0.0.1` works inside that namespace
- the loopback there is separate from the host loopback
- binding `127.0.0.1:1234` inside `nonet` does not conflict with the host binding the same address and port
- the final command still sees your visible UID/GID

Supplementary groups may display oddly, similar to `unshare -c -n`. In practice this shows up in tools such as `id -G`, which report the supplementary group list via `getgroups(2)`. Across user namespaces that output can look strange or partially remapped even when actual filesystem permission checks through those groups still behave as expected. In testing, group-based access still worked despite the odd-looking `id -G` output.

`nonet` is a single binary. It does not invoke `unshare`, `newuidmap`, or `newgidmap`.

The implementation has two layers:

- normal Go control flow in [cli.go](./cli.go), [runner.go](./runner.go), [probe.go](./probe.go), and [netns.go](./netns.go)
- a small in-binary cgo/C shim in [spawn_linux.go](./spawn_linux.go)

The basic sequence is:

1. The parent process opens a pipe for synchronization.
2. The parent calls the in-binary C shim.
3. The shim uses `clone(CLONE_NEWUSER | SIGCHLD, ...)` to start a child directly in a fresh user namespace.
4. The child blocks immediately on the sync pipe before doing any namespace work.
5. The parent writes one-line identity mappings into:
   - `/proc/<child-pid>/uid_map`
   - `/proc/<child-pid>/gid_map`
6. Before writing `gid_map`, the parent writes `deny` to `/proc/<child-pid>/setgroups`, which is required for the unprivileged GID mapping path.
7. The parent releases the child by writing one byte to the pipe.
8. The child calls `unshare(CLONE_NEWNET)`.
9. The child brings `lo` up using `ioctl(SIOCGIFFLAGS)` and `ioctl(SIOCSIFFLAGS)` on a datagram socket.
10. The child `execve()`s the resolved command path.

The important detail is step 9 happens before the final `exec`.

Here, `<child-pid>` means the PID of the just-cloned helper as seen by the parent in the parent namespace. The parent writes those procfs files from outside the child before releasing it to continue.

That is why this works while plain `unshare -c -n <cmd>` does not: the helper still has capabilities in the fresh user namespace at that point, so it can create the new network namespace and configure loopback before handing control to the final command.

### Identity Model

The current design uses a simple identity map, not subordinate ID ranges.

The parent writes:

```text
uid_map: <uid> <uid> 1
gid_map: <gid> <gid> 1
```

That keeps the final command's visible UID/GID unchanged.

Other IDs are not preserved. In particular, host-owned `0:0` objects such as `/` will usually appear as the overflow owner/group, just as they do under `unshare` with a simple current-user mapping. So behavior for owners other than the current user is intentionally on par with `unshare`, not a special remapping done by `nonet`.

This is enough because `nonet` does not try to preserve extra namespace-root identity after `exec`; it only needs the temporary privileges that exist before `exec` in the freshly created user namespace.

### Why There Is a C Shim

The user-namespace child is created with a raw `clone(2)` call from the small C layer.

That avoids relying on external helpers and keeps the low-level namespace creation step explicit and predictable. The Go side then handles the parent-side orchestration, mapping writes, and self-test logic.

### Why This Uses cgo

The project uses cgo on purpose.

Go can call Linux syscalls, but it does not provide a clean public API for the exact process-creation sequence `nonet` needs.

The low-level part of `nonet` needs to:

- create a helper directly with `clone(CLONE_NEWUSER)`
- pause that helper immediately
- let the parent write UID/GID maps through procfs
- then continue the child into further namespace setup before the final `exec`

That can in principle be attempted without cgo, but in practice this particular clone/synchronize/map/continue path is much more predictable with a very small C layer than through ordinary Go process APIs.

So the tradeoff chosen by this project is:

- keep almost all logic in Go
- keep the namespace-critical process creation step in a tiny C shim
- avoid external helper binaries
- accept that builds require cgo

## Self-Test

`nonet --self-test` performs an end-to-end runtime probe of the actual execution path.

It checks:

- visible UID/GID
- `/proc/sys/kernel/unprivileged_userns_clone` if present
- helper spawn and user-namespace setup
- successful network-namespace creation
- that only `lo` is present in the namespace
- that `lo` is up
- TCP loopback connectivity on `127.0.0.1`
- access to the caller's home directory

If this passes, the host is a good candidate for `nonet`.

## Limits

`nonet` is not a general-purpose sandbox.

It prevents normal network access by running the command in a separate network namespace, but it does not attempt to confine the process in other ways. In particular, it does not block:

- filesystem access available to your user
- Unix sockets
- inherited file descriptors
- other local IPC mechanisms

So it is appropriate for things like:

- testing builds without outside network access
- checking whether a process unexpectedly reaches out to the network
- running one command with isolated loopback

It should not be treated as a hardened security container.

## Requirements

- user namespaces available on the target host
- network namespaces available on the target host
- a runtime policy that allows this style of unprivileged namespace creation
- cgo enabled at build time

The produced binary targets Linux. Because the project uses cgo for the namespace helper, building a Linux binary on a non-Linux host requires a Linux-capable C cross-toolchain.

It may fail inside restricted containers even if it works on the host.

## Build

Build:

```sh
make build
```

Static build:

```sh
make static
```

Run tests:

```sh
make test
```

Output binary:

```sh
build/bin/host/nonet
```

Both normal and static builds require cgo, because the project uses the in-binary C shim for the namespace helper.
