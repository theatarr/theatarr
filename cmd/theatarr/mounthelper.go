package main

import (
	"errors"
	"flag"
	"os"

	"github.com/theatarr/theatarr/internal/library"
)

func init() {
	register(command{
		name:    "mount-helper",
		summary: "answer theatarr serve's read-only calls on the debrid mount over stdin/stdout (started by theatarr serve)",
		run:     runMountHelper,
		hidden:  true,
	})
}

// runMountHelper is the helper process theatarr serve runs its mount calls in (docs/design/library.md "Mount guard"): it
// stats, lists, reads links and probes file headers under -root, nothing else, until stdin closes.
func runMountHelper(args []string) error {
	fs := flag.NewFlagSet("mount-helper", flag.ContinueOnError)
	root := fs.String("root", "", "the mount; paths outside it are refused")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" || fs.NArg() != 0 {
		return errors.New("usage: theatarr mount-helper -root <mount path>")
	}
	return library.ServeMountHelper(*root, os.Stdin, os.Stdout)
}

// mountHelperConfig is theatarr serve's helper: this binary's mount-helper command. nil when --mount-helper is off: the
// mount calls then run in Theatarr itself under the in-process guard.
func mountHelperConfig(on bool) (*library.MountHelper, error) {
	if !on {
		return nil, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &library.MountHelper{Exe: exe, Args: []string{"mount-helper"}}, nil
}
