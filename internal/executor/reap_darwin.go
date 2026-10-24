package executor

// macOS reparents orphan processes to launchd. Linux production execution uses
// a child subreaper; process-group termination is shared by both platforms.
func prepareProcessReaping() error { return nil }

func reapProcessGroup(_ int) {}
