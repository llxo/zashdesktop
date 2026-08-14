//go:build windows

package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

const attachParentProcess uintptr = ^uintptr(0)

var (
	coreKernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	coreFreeConsole              = coreKernel32.NewProc("FreeConsole")
	coreAttachConsole            = coreKernel32.NewProc("AttachConsole")
	coreSetConsoleCtrlHandler    = coreKernel32.NewProc("SetConsoleCtrlHandler")
	coreGenerateConsoleCtrlEvent = coreKernel32.NewProc("GenerateConsoleCtrlEvent")
)

func configureCoreCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

func findExternalCoreProcess(coreType string) (*os.Process, error) {
	name := coreExecutableNameFor(coreType)
	output, err := exec.Command("tasklist.exe", "/FI", "IMAGENAME eq "+name, "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil, fmt.Errorf("list %s processes: %w", name, err)
	}

	reader := csv.NewReader(strings.NewReader(string(output)))
	reader.FieldsPerRecord = -1
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			return nil, nil
		}
		if readErr != nil || len(record) < 2 {
			continue
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(record[1]))
		if parseErr != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}
		process, processErr := os.FindProcess(pid)
		if processErr != nil {
			continue
		}
		return process, nil
	}
}

func requestCoreStop(process *os.Process) error {
	if ret, _, err := coreFreeConsole.Call(); ret == 0 && err != windows.ERROR_INVALID_HANDLE {
		return err
	}
	defer coreAttachConsole.Call(attachParentProcess)

	if ret, _, err := coreAttachConsole.Call(uintptr(process.Pid)); ret == 0 && err != windows.ERROR_ACCESS_DENIED {
		return err
	}
	if ret, _, err := coreSetConsoleCtrlHandler.Call(0, 1); ret == 0 {
		return err
	}
	if ret, _, err := coreGenerateConsoleCtrlEvent.Call(windows.CTRL_BREAK_EVENT, uintptr(process.Pid)); ret == 0 {
		return err
	}
	return nil
}

func coreProcessAlive(process *os.Process) (bool, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(process.Pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return false, nil
		}
		return false, err
	}
	defer windows.CloseHandle(handle)

	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, err
	}
	switch state {
	case windows.WAIT_OBJECT_0:
		return false, nil
	case uint32(windows.WAIT_TIMEOUT):
		return true, nil
	default:
		return false, fmt.Errorf("unexpected process wait status: %d", state)
	}
}
