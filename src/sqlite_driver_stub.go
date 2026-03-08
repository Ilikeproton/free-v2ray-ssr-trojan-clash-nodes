//go:build !windows

package main

import _ "modernc.org/sqlite"

const sqliteDriverName = "sqlite"
