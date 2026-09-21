package store

import "os"

// hardExit simulates a process crash after the WAL write+fsync has completed
// but before in-memory state is advanced. Tests replace this hook.
var hardExit = func() { os.Exit(2) }
