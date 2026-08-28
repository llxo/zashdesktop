package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const attachParentProcess uintptr = ^uintptr(0)

var (
	coreKernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	coreFreeConsole               = coreKernel32.NewProc("FreeConsole")
	coreAttachConsole             = coreKernel32.NewProc("AttachConsole")
	coreSetConsoleCtrlHandler     = coreKernel32.NewProc("SetConsoleCtrlHandler")
	coreGenerateConsoleCtrlEvent  = coreKernel32.NewProc("GenerateConsoleCtrlEvent")
	coreQueryFullProcessImageName = coreKernel32.NewProc("QueryFullProcessImageNameW")
)

func configureCoreCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}

func getProcessImagePath(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)

	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	ret, _, callErr := coreQueryFullProcessImageName.Call(
		uintptr(handle),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret == 0 {
		return "", callErr
	}
	return windows.UTF16ToString(buf[:size]), nil
}

func findExternalCoreProcess(coreType, expectedPath string) (*os.Process, error) {
	name := strings.ToLower(coreExecutableNameFor(coreType))
	var cleanExpected string
	if expectedPath != "" {
		cleanExpected = filepath.Clean(expectedPath)
		if eval, err := filepath.EvalSymlinks(cleanExpected); err == nil {
			cleanExpected = eval
		}
	}

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
			if cleanExpected != "" {
				imagePath, err := getProcessImagePath(entry.ProcessID)
				if err == nil && imagePath != "" {
					cleanImage := filepath.Clean(imagePath)
					if eval, err := filepath.EvalSymlinks(cleanImage); err == nil {
						cleanImage = eval
					}
					if strings.EqualFold(cleanImage, cleanExpected) {
						if process, err := os.FindProcess(int(entry.ProcessID)); err == nil {
							return process, nil
						}
					}
				}
			} else {
				if process, err := os.FindProcess(int(entry.ProcessID)); err == nil {
					return process, nil
				}
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

// -----------------------------------------------------------------------------
// File Lock Status
// -----------------------------------------------------------------------------

const (
	errorSharingViolation syscall.Errno = 32
	errorLockViolation    syscall.Errno = 33
)

func isFileLockedError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == errorSharingViolation || errno == errorLockViolation
}

