//go:build !unix

package main

import "os/exec"

func detach(command *exec.Cmd) {}

func checkPrivateDirectory(directory string) error { return nil }
