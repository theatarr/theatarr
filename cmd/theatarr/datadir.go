package main

import (
	"fmt"
	"os"
)

// checkDataDir makes sure serve can write the data directory (the database's directory, which also holds the logs,
// the backups and the first admin password by default) before anything opens a file there. It writes for real, so
// a read-only mount is caught too. The usual cause is a bind-mounted directory Docker created owned by root while
// the image runs as another user: the error is one line naming the directory, the user and the fix, so a container
// in a restart loop prints it on every start.
func checkDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return dataDirError(dir, "cannot be created", err)
	}
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return dataDirError(dir, "is not writable", err)
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return dataDirError(dir, "is not writable", err)
	}
	return nil
}

func dataDirError(dir, what string, cause error) error {
	uid, gid := os.Getuid(), os.Getgid()
	owner := ""
	if fi, err := os.Stat(dir); err == nil {
		owner = dirOwner(fi)
	}
	return fmt.Errorf("data directory %s %s by uid %d:%d%s: %w. Fix on the host: chown -R %d:%d the directory mounted "+
		"at %s, or run the container as a user that owns it. See docs/troubleshooting.md#data-directory",
		dir, what, uid, gid, owner, cause, uid, gid, dir)
}
