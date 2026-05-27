//go:build !unix

package main

// lockSink is a no-op on non-unix platforms. agent-lens-hook ships only for
// darwin and linux (see release.yml's build matrix), so the concurrent-writer
// NDJSON protection in flock_unix.go always applies to released binaries; this
// stub exists solely so the package still compiles on a non-unix dev machine.
func lockSink(_ uintptr) (func(), error) {
	return func() {}, nil
}
