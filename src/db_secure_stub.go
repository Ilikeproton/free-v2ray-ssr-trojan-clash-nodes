//go:build !windows

package main

import "database/sql"

func prepareRuntimeDBPath(persistentPath string) (string, func(*sql.DB) error, func(*sql.DB) error, error) {
	openFn := func(db *sql.DB) error {
		return nil
	}
	closeFn := func(db *sql.DB) error {
		if db != nil {
			return db.Close()
		}
		return nil
	}
	return persistentPath, openFn, closeFn, nil
}

func recoverEncryptedDBOnOpenError(persistentPath string, openErr error) (bool, error) {
	return false, nil
}
