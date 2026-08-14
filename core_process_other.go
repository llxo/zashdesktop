//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func configureCoreCommand(*exec.Cmd) {}

func findExternalCoreProcess(coreType string) (*os.Process, error) {
	output, err := exec.Command("pgrep", "-x", coreExecutableNameFor(coreType)).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("list %s processes: %w", coreType, err)
	}
	for _, line := range strings.Fields(string(output)) {
		pid, parseErr := strconv.Atoi(line)
		if parseErr != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}
		process, processErr := os.FindProcess(pid)
		if processErr == nil {
			return process, nil
		}
	}
	return nil, nil
}

func requestCoreStop(process *os.Process) error {
	return process.Signal(syscall.SIGINT)
}

func coreProcessAlive(process *os.Process) (bool, error) {
	err := process.Signal(syscall.Signal(0))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrProcessDone) {
		return false, nil
	}
	if errno, ok := err.(syscall.Errno); ok {
		switch errno {
		case syscall.ESRCH:
			return false, nil
		case syscall.EPERM:
			return true, nil
		}
	}
	return false, fmt.Errorf("check core process %d: %w", process.Pid, err)
}
