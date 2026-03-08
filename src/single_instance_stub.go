//go:build !windows

package main

func acquireSingleInstanceLock() (release func(), alreadyRunning bool, err error) {
	return nil, false, nil
}
