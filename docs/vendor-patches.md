# Vendor patches

Vendored downstream fixes are exceptional. Each entry records why the change
cannot yet be represented by a module version and how to remove it safely.

## go-cli-server v0.5.4: Windows process resource-release helper

File: `vendor/github.com/standardbeagle/go-cli-server/process/lifecycle_release_windows.go`

The platform-neutral process manager calls `hasReleasedResources` while
waiting for killed processes to release sockets. Version 0.5.4 defines the
helper only in `lifecycle_unix.go`, so Windows builds fail with an undefined
symbol. No newer module release currently exists.

The downstream Windows implementation checks the process exit code and is
conservative when process state cannot be inspected: access-denied and unknown
errors do not claim resources were released.

Remove the shim when upgrading to an upstream `go-cli-server` release that
provides the Windows helper. After any `go mod vendor`, verify the patch is
still required and run:

```bash
make cross-compile-check
```
