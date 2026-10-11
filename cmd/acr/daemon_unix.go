//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// detach starts the daemon in its own session, so it outlives the terminal.
func detach(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// checkPrivateDirectory refuses a socket directory other users can reach or
// that belongs to someone else.
func checkPrivateDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("daemon directory %s must be a directory owned by you with mode 0700", directory)
	}
	return nil
}
