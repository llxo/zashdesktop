package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

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
		CreationFlags: windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}

func findExternalCoreProcess(coreType string) (*os.Process, error) {
	name := strings.ToLower(coreExecutableNameFor(coreType))
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("create process snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, nil
	}

	currentPID := uint32(os.Getpid())
	for {
		processName := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		if processName == name && entry.ProcessID != currentPID && entry.ProcessID > 0 {
			process, err := os.FindProcess(int(entry.ProcessID))
			if err == nil {
				return process, nil
			}
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return nil, nil
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
