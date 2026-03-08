//go:build windows

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	encryptedDBMagic      = "DXLDB01" // legacy custom-encrypted db magic
	dbKeySize             = 32
	dbEntropySalt         = "daxionglink-db-key-v2"
	allowDBAutoRecoverEnv = "DXL_DB_ALLOW_DESTRUCTIVE_RECOVERY"
	sqlCipherPageSize     = 4096
)

var errDBKeyUnprotect = errors.New("db key unprotect failed")

func prepareRuntimeDBPath(persistentPath string) (string, func(*sql.DB) error, func(*sql.DB) error, error) {
	dir := filepath.Dir(persistentPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, nil, err
	}

	entropies, err := dbEntropyCandidates()
	if err != nil {
		return "", nil, nil, err
	}
	keyPath := persistentPath + ".key"

	key, err := loadOrCreateProtectedDBKey(keyPath, entropies)
	if err != nil {
		// Keep existing db/key untouched on key-unprotect failures.
		// Auto-archiving here can cause data loss when executable path changes.
		if errors.Is(err, errDBKeyUnprotect) {
			return "", nil, nil, fmt.Errorf("db key cannot be unlocked; existing files preserved: %w", err)
		}
		return "", nil, nil, err
	}
	keyHex := hex.EncodeToString(key)
	passphrase := base64.StdEncoding.EncodeToString(key)
	openDSN := buildSQLCipherDSN(persistentPath, keyHex)

	if err := migrateLegacyDBIfNeeded(persistentPath, keyPath, key, keyHex); err != nil {
		return "", nil, nil, err
	}
	if fileExists(persistentPath) {
		if st, stErr := os.Stat(persistentPath); stErr == nil && st.Size() > 0 {
			if probeErr := probeSQLCipherDSN(openDSN); probeErr != nil {
				legacyDSN := buildLegacySQLCipherDSN(persistentPath, passphrase)
				if legacyErr := probeSQLCipherDSN(legacyDSN); legacyErr == nil {
					openDSN = legacyDSN
				}
			}
		}
	}

	openFn := func(db *sql.DB) error {
		return verifySQLCipherSession(db)
	}
	closeFn := func(db *sql.DB) error {
		if db != nil {
			return db.Close()
		}
		return nil
	}
	return openDSN, openFn, closeFn, nil
}

func buildSQLCipherDSN(dbPath, keyHex string) string {
	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	return fmt.Sprintf(
		"%s%s_pragma_key=x'%s'&_pragma_cipher_page_size=%d&_foreign_keys=on",
		dbPath,
		sep,
		keyHex,
		sqlCipherPageSize,
	)
}

func buildLegacySQLCipherDSN(dbPath, passphrase string) string {
	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	return fmt.Sprintf(
		"%s%s_pragma_key=%s&_pragma_cipher_page_size=%d&_foreign_keys=on",
		dbPath,
		sep,
		url.QueryEscape(passphrase),
		sqlCipherPageSize,
	)
}

func probeSQLCipherDSN(dsn string) error {
	db, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return verifySQLCipherSession(db)
}

func verifySQLCipherSession(db *sql.DB) error {
	if db == nil {
		return errors.New("nil db")
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		return err
	}
	// Validate key correctness early.
	if _, err := db.Exec("SELECT count(*) FROM sqlite_master;"); err != nil {
		return fmt.Errorf("sqlcipher key verify failed: %w", err)
	}
	return nil
}

func recoverEncryptedDBOnOpenError(persistentPath string, openErr error) (bool, error) {
	if openErr == nil {
		return false, nil
	}
	msg := strings.ToLower(openErr.Error())
	if !strings.Contains(msg, "file is encrypted or is not a database") &&
		!strings.Contains(msg, "file is not a database") &&
		!strings.Contains(msg, "sqlcipher key verify failed") {
		return false, nil
	}
	if strings.TrimSpace(os.Getenv(allowDBAutoRecoverEnv)) != "1" {
		return false, fmt.Errorf(
			"sqlcipher open failed; destructive auto-recovery is disabled (set %s=1 to force archive+recreate)",
			allowDBAutoRecoverEnv,
		)
	}
	keyPath := persistentPath + ".key"
	if err := archiveBrokenDBFiles(persistentPath, keyPath, "sqlcipher-open-failed"); err != nil {
		return false, err
	}
	return true, nil
}

func migrateLegacyDBIfNeeded(persistentPath, keyPath string, key []byte, keyHex string) error {
	data, readErr := os.ReadFile(persistentPath)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil
		}
		return readErr
	}
	if len(data) == 0 {
		return nil
	}

	switch {
	case isSQLiteDBPayload(data):
		return migratePlainFileToSQLCipher(persistentPath, keyHex, "plain")
	case isEncryptedDBPayload(data):
		plain, err := decryptDBPayload(data, key)
		if err != nil {
			if arErr := archiveBrokenDBFiles(persistentPath, keyPath, "legacy-decrypt-failed"); arErr != nil {
				return fmt.Errorf("legacy decrypt failed: %v; archive failed: %w", err, arErr)
			}
			return nil
		}
		tmpPlain := persistentPath + ".legacy_plain.tmp"
		if err := os.WriteFile(tmpPlain, plain, 0o600); err != nil {
			return err
		}
		defer os.Remove(tmpPlain)
		return migrateSourceFileToSQLCipher(tmpPlain, persistentPath, keyHex, "legacy-custom")
	default:
		// Assume SQLCipher format already.
		return nil
	}
}

func migratePlainFileToSQLCipher(persistentPath, keyHex string, reason string) error {
	return migrateSourceFileToSQLCipher(persistentPath, persistentPath, keyHex, reason)
}

func migrateSourceFileToSQLCipher(sourcePath, targetPath, keyHex, reason string) error {
	workPath := targetPath + ".sqlcipher_migrate.tmp"
	_ = os.Remove(workPath)

	if err := sqlcipherExport(sourcePath, workPath, keyHex); err != nil {
		_ = os.Remove(workPath)
		return err
	}

	backupPath := targetPath + fmt.Sprintf(".legacy-%s.%s", reason, time.Now().Format("20060102150405"))
	if sourcePath == targetPath {
		if err := os.Rename(targetPath, backupPath); err != nil {
			_ = os.Remove(workPath)
			return err
		}
	}
	if err := os.Rename(workPath, targetPath); err != nil {
		if sourcePath == targetPath {
			_ = os.Rename(backupPath, targetPath)
		}
		return err
	}
	return nil
}

func sqlcipherExport(sourcePath, targetPath, keyHex string) error {
	db, err := sql.Open(sqliteDriverName, sourcePath)
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.Exec("PRAGMA key = '';"); err != nil {
		return err
	}
	targetEsc := sqlEscapeLiteral(targetPath)
	attachStmt := fmt.Sprintf(`ATTACH DATABASE '%s' AS encrypted KEY "x'%s'";`, targetEsc, keyHex)
	if _, err := db.Exec(attachStmt); err != nil {
		return err
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA encrypted.cipher_page_size = %d;", sqlCipherPageSize)); err != nil {
		_, _ = db.Exec("DETACH DATABASE encrypted;")
		return err
	}
	if _, err := db.Exec("SELECT sqlcipher_export('encrypted');"); err != nil {
		_, _ = db.Exec("DETACH DATABASE encrypted;")
		return err
	}
	if _, err := db.Exec("DETACH DATABASE encrypted;"); err != nil {
		return err
	}
	return nil
}

func sqlEscapeLiteral(v string) string {
	return strings.ReplaceAll(v, "'", "''")
}

func dbEntropyCandidates() ([][]byte, error) {
	entropies := make([][]byte, 0, 4)
	appendEntropy := func(sum [32]byte) {
		candidate := make([]byte, len(sum))
		copy(candidate, sum[:])
		for _, e := range entropies {
			if equalBytes(e, candidate) {
				return
			}
		}
		entropies = append(entropies, candidate)
	}

	// Primary entropy is stable across app updates and exe relocations.
	appendEntropy(sha256.Sum256([]byte(dbEntropySalt + "|stable")))

	// Legacy candidates for backward compatibility.
	exePath, err := os.Executable()
	if err != nil {
		return entropies, nil
	}
	exePath = filepath.Clean(exePath)
	appendEntropy(sha256.Sum256([]byte(dbEntropySalt + "|" + strings.ToLower(exePath))))

	exeData, err := os.ReadFile(exePath)
	if err != nil {
		return entropies, nil
	}
	appendEntropy(sha256.Sum256(exeData))
	return entropies, nil
}

func loadOrCreateProtectedDBKey(keyPath string, entropies [][]byte) ([]byte, error) {
	primary := []byte(nil)
	if len(entropies) > 0 {
		primary = entropies[0]
	}
	if fileExists(keyPath) {
		protected, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for idx, entropy := range entropies {
			key, upErr := dpapiUnprotect(protected, entropy)
			if upErr != nil {
				lastErr = upErr
				continue
			}
			if len(key) != dbKeySize {
				lastErr = fmt.Errorf("invalid db key size: %d", len(key))
				continue
			}
			if idx > 0 && len(primary) > 0 {
				reprotected, reErr := dpapiProtect(key, primary)
				if reErr != nil {
					return nil, fmt.Errorf("re-protect db key failed: %w", reErr)
				}
				if wrErr := writeFileAtomic(keyPath, reprotected, 0o600); wrErr != nil {
					return nil, fmt.Errorf("update db key failed: %w", wrErr)
				}
			}
			return key, nil
		}
		if lastErr == nil {
			lastErr = errors.New("no usable entropy candidate")
		}
		return nil, fmt.Errorf("%w: %v", errDBKeyUnprotect, lastErr)
	}

	key := make([]byte, dbKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	protected, err := dpapiProtect(key, primary)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(keyPath, protected, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func archiveBrokenDBFiles(dbPath, keyPath, reason string) error {
	suffix := time.Now().Format("20060102150405")
	archiveDir := filepath.Join(filepath.Dir(dbPath), ".daxionglink_recovery")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return err
	}
	hideDirIfPossible(archiveDir)

	dbName := filepath.Base(dbPath)
	keyName := filepath.Base(keyPath)

	var errs []error
	if fileExists(dbPath) {
		dst := filepath.Join(archiveDir, fmt.Sprintf("%s.broken-%s.%s", dbName, reason, suffix))
		if err := os.Rename(dbPath, dst); err != nil {
			errs = append(errs, fmt.Errorf("archive db failed: %w", err))
		}
	}
	if fileExists(keyPath) {
		dst := filepath.Join(archiveDir, fmt.Sprintf("%s.broken-%s.%s", keyName, reason, suffix))
		if err := os.Rename(keyPath, dst); err != nil {
			errs = append(errs, fmt.Errorf("archive key failed: %w", err))
		}
	}
	pruneArchiveFiles(archiveDir, 20)
	return errors.Join(errs...)
}

func hideDirIfPossible(dir string) {
	pathPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return
	}
	attr, err := windows.GetFileAttributes(pathPtr)
	if err != nil {
		return
	}
	_ = windows.SetFileAttributes(pathPtr, attr|windows.FILE_ATTRIBUTE_HIDDEN)
}

func pruneArchiveFiles(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		mod  time.Time
	}
	files := make([]item, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, item{
			path: filepath.Join(dir, e.Name()),
			mod:  info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].mod.After(files[j].mod)
	})
	for i := keep; i < len(files); i++ {
		_ = os.Remove(files[i].path)
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func dpapiProtect(data []byte, entropy []byte) ([]byte, error) {
	in := bytesToBlob(data)
	var entropyBlob windows.DataBlob
	var entropyPtr *windows.DataBlob
	if len(entropy) > 0 {
		entropyBlob = bytesToBlob(entropy)
		entropyPtr = &entropyBlob
	}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, entropyPtr, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	return blobToBytesAndFree(&out)
}

func dpapiUnprotect(data []byte, entropy []byte) ([]byte, error) {
	in := bytesToBlob(data)
	var entropyBlob windows.DataBlob
	var entropyPtr *windows.DataBlob
	if len(entropy) > 0 {
		entropyBlob = bytesToBlob(entropy)
		entropyPtr = &entropyBlob
	}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, entropyPtr, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	return blobToBytesAndFree(&out)
}

func bytesToBlob(data []byte) windows.DataBlob {
	if len(data) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{
		Size: uint32(len(data)),
		Data: &data[0],
	}
}

func blobToBytesAndFree(blob *windows.DataBlob) ([]byte, error) {
	if blob == nil || blob.Data == nil || blob.Size == 0 {
		return []byte{}, nil
	}
	out := make([]byte, int(blob.Size))
	copy(out, unsafe.Slice(blob.Data, int(blob.Size)))
	_, err := windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(blob.Data))))
	blob.Data = nil
	blob.Size = 0
	return out, err
}

func isEncryptedDBPayload(data []byte) bool {
	return len(data) > len(encryptedDBMagic)+1 && string(data[:len(encryptedDBMagic)]) == encryptedDBMagic
}

func isSQLiteDBPayload(data []byte) bool {
	const header = "SQLite format 3\x00"
	return len(data) >= len(header) && string(data[:len(header)]) == header
}

func decryptDBPayload(data []byte, key []byte) ([]byte, error) {
	if !isEncryptedDBPayload(data) {
		return nil, errors.New("invalid encrypted db payload")
	}
	offset := len(encryptedDBMagic)
	nonceSize := int(data[offset])
	offset++
	if nonceSize <= 0 {
		return nil, errors.New("invalid encrypted db nonce size")
	}
	if len(data) < offset+nonceSize {
		return nil, errors.New("invalid encrypted db payload length")
	}
	nonce := data[offset : offset+nonceSize]
	ciphertext := data[offset+nonceSize:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if nonceSize != gcm.NonceSize() {
		return nil, errors.New("encrypted db nonce mismatch")
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
