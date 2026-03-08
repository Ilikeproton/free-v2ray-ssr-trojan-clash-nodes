//go:build windows

package main

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func acquireSingleInstanceLock() (release func(), alreadyRunning bool, err error) {
	lockName, err := appInstanceMutexName()
	if err != nil {
		return nil, false, err
	}
	namePtr, err := windows.UTF16PtrFromString(lockName)
	if err != nil {
		return nil, false, fmt.Errorf("encode mutex name: %w", err)
	}
	handle, createErr := windows.CreateMutex(nil, false, namePtr)
	if createErr != nil {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		if errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("create mutex: %w", createErr)
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = windows.CloseHandle(handle)
	}, false, nil
}

func appInstanceMutexName() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	cleanPath := strings.ToLower(filepath.Clean(exePath))
	sum := fnv.New64a()
	_, _ = sum.Write([]byte(cleanPath))
	return fmt.Sprintf(`Local\DaxionglinkSingleton_%x`, sum.Sum64()), nil
}
