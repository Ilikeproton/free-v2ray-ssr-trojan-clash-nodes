//go:build !windows

package main

func (a *guiApp) runWebviewDesktop() error {
	return a.run()
}
