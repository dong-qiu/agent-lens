//go:build unix

package main

import "syscall"

// lockSink takes a blocking exclusive advisory lock (flock LOCK_EX) on fd and
// returns a release func. It serializes concurrent appendToSink writers for
// the same session file so NDJSON records can't interleave.
//
// Why this is needed (issue #3): the sink is opened O_APPEND, but POSIX only
// guarantees atomic appends for writes <= PIPE_BUF (4 KiB). A Stop hook event
// bundles a full transcript delta and routinely exceeds that, so two hook
// processes (e.g. parallel sub-agents) writing the same <sid>.ndjson can
// interleave mid-line and corrupt a record. flock is advisory, but every
// writer is this same binary, so all of them honor it.
//
// The lock is held only across the single write and released before the fd is
// closed; closing would release it anyway (flock is tied to the open file
// description), so a crashed holder never wedges the file.
func lockSink(fd uintptr) (func(), error) {
	// Retry on EINTR: LOCK_EX blocks, and Go's runtime can interrupt a
	// blocking syscall with a signal (raw syscall.Flock, unlike
	// x/sys/unix.Flock, doesn't loop for us).
	for {
		err := syscall.Flock(int(fd), syscall.LOCK_EX)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		return func() { _ = syscall.Flock(int(fd), syscall.LOCK_UN) }, nil
	}
}
