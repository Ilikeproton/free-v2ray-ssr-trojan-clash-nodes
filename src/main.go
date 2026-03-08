package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		runWebAndExit("")
		return
	}

	mode := strings.TrimSpace(os.Args[1])
	switch mode {
	case "c":
		configFile := "config.txt"
		if len(os.Args) >= 3 {
			configFile = os.Args[2] + ".txt"
		}
		runCompare(configFile)
	case "q":
		configFile := "config.txt"
		if len(os.Args) >= 3 {
			configFile = os.Args[2] + ".txt"
		}
		runQuick(configFile)
	case "web":
		dbPath, err := parseWebDBPath(os.Args[2:])
		if err != nil {
			fmt.Println("invalid web args:", err)
			printUsageAndExit()
		}
		runWebAndExit(dbPath)
	default:
		// Allow direct web flags without explicit "web" mode:
		// daxionglink.exe --db D:\data\daxionglink_gui.db
		if strings.HasPrefix(mode, "-") {
			dbPath, err := parseWebDBPath(os.Args[1:])
			if err != nil {
				fmt.Println("invalid web args:", err)
				printUsageAndExit()
			}
			runWebAndExit(dbPath)
			return
		}
		fmt.Println("unknown mode:", mode)
		printUsageAndExit()
	}
}

func runWebAndExit(dbPath string) {
	releaseLock, alreadyRunning, err := acquireSingleInstanceLock()
	if err != nil {
		msg := fmt.Sprintf("startup lock failed: %v", err)
		showStartupError(msg)
		fmt.Println(msg)
		os.Exit(1)
	}
	if alreadyRunning {
		msg := "程序已在运行，不能重复启动。"
		showStartupError(msg)
		fmt.Println(msg)
		os.Exit(1)
	}
	if releaseLock != nil {
		defer releaseLock()
	}

	if err := startWebManager(dbPath); err != nil {
		msg := fmt.Sprintf("web manager start failed: %v", err)
		showStartupError(msg)
		fmt.Println(msg)
		os.Exit(1)
	}
}

func parseWebDBPath(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if len(args) == 1 && !strings.HasPrefix(args[0], "-") {
		return strings.TrimSpace(args[0]), nil
	}
	for i := 0; i < len(args); i++ {
		v := strings.TrimSpace(args[i])
		switch {
		case v == "--db" || v == "-db":
			if i+1 >= len(args) {
				return "", errors.New("--db requires a path")
			}
			return strings.TrimSpace(args[i+1]), nil
		case strings.HasPrefix(v, "--db="):
			return strings.TrimSpace(strings.TrimPrefix(v, "--db=")), nil
		}
	}
	return "", errors.New("unsupported arguments")
}

func printUsageAndExit() {
	fmt.Println("usage:")
	fmt.Println("  daxionglink.exe c [configPrefix]")
	fmt.Println("  daxionglink.exe q [configPrefix]")
	fmt.Println("  daxionglink.exe web [--db <dbPath>]")
	fmt.Println("  daxionglink.exe --db <dbPath>    # same as web mode")
	fmt.Println("  daxionglink.exe                  # web mode, default db in run dir")
	os.Exit(1)
}
