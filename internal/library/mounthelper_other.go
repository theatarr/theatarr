//go:build !linux

package library

import (
	"os"
	"syscall"
)

// helperSysProcAttr: no parent-death signal here; watchParent ends the helper.
func helperSysProcAttr() *syscall.SysProcAttr { return nil }

func (s *helperServer) stat(op, p, rel string, nofollow bool) (os.FileInfo, error) {
	return s.portableStat(op, p, rel, nofollow)
}

func (s *helperServer) readlink(p, rel string) (string, error) { return s.portableReadlink(p, rel) }

func (s *helperServer) open(op, p, rel string, dir bool) (*os.File, error) {
	return s.portableOpen(op, p, rel, dir)
}

func (s *helperServer) eval(p, rel string) (string, error) { return s.portableEval(p, rel) }
