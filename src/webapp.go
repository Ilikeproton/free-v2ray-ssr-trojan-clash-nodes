package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"daxionglink/protocol"

	"github.com/oschwald/geoip2-golang"
	"golang.org/x/net/proxy"
)

const (
	mainRoutePort        = 10600
	defaultPort          = 10601
	portMin              = 10603
	portMax              = 10700
	maxLogEntries        = 800
	defaultTestURL       = "https://youtube.com"
	defaultTestNum       = 10
	defaultSpeedWorkers  = 5
	minSpeedWorkers      = 1
	maxSpeedWorkers      = 500
	defaultSpeedLimit    = 500
	minSpeedLimit        = 1
	maxSpeedLimit        = 5000
	defaultTopLinksLimit = 20
	minTopLinksLimit     = 1
	maxTopLinksLimit     = 200
	defaultIPCountryTop  = 3
	minIPCountryTop      = 1
	maxIPCountryTop      = 100
	minValidSpeedKB      = 0.1
	maxWebsiteCount      = 3
	remoteImportTopLimit = 1000
	myIPAPIURL           = "https://api.myip.com"
	remoteBase64LinksURL = "https://github.com/kattyGG/daxionglink/raw/refs/heads/main/server_base64.txt"
	advancedGithubURL    = "https://github.com/Ilikeproton/free-v2ray-ssr-trojan-clash-nodes"
)

type guiApp struct {
	rootDir   string
	webDir    string
	configDir string
	doneDir   string
	xrayPath  string
	dbPath    string
	dbOpen    string

	db *sql.DB

	dbCloseFn func(*sql.DB) error

	windowMu         sync.RWMutex
	windowController func(action string) error

	mu        sync.Mutex
	siteProcs map[int64]*managedProcess
	httpProcs map[int64]*httpBridgeProcess
	mainProc  *managedProcess

	speedMu      sync.Mutex
	speedRunning map[int64]bool
	ipCountryMu  sync.Mutex
	ipCountryRun bool

	logMu sync.Mutex
	logs  []logEntry
	logID atomic.Int64

	geoDB *geoip2.Reader
}

type managedProcess struct {
	websiteID int64
	name      string
	port      int
	pid       int
	linkID    int64
	link      string
	config    string
	startedAt time.Time
	cmd       *exec.Cmd
	done      chan struct{}
}

type httpBridgeProcess struct {
	websiteID int64
	name      string
	socksPort int
	httpPort  int
	startedAt time.Time
	bridge    *HTTPBridge
}

type logEntry struct {
	ID      int64  `json:"id"`
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type website struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Address        string `json:"address"`
	DownloadURL    string `json:"download_url"`
	MatchRule      string `json:"match_rule"`
	Port           int    `json:"port"`
	IsEnabled      bool   `json:"is_enabled"`
	SelectedLinkID int64  `json:"selected_link_id"`
	HTTPEnabled    bool   `json:"http_enabled"`
	HTTPPort       int    `json:"http_port"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	Running        bool   `json:"running"`
	HTTPRunning    bool   `json:"http_running"`
	PID            int    `json:"pid"`
}

type linkRecord struct {
	ID      int64
	Link    string
	Country string
}

type speedRow struct {
	LinkID    int64   `json:"link_id"`
	Link      string  `json:"link"`
	Country   string  `json:"country"`
	SpeedK    float64 `json:"speed_k"`
	TestedAt  string  `json:"tested_at"`
	LastUse   string  `json:"last_use"`
	Selected  bool    `json:"selected"`
	Starred   bool    `json:"starred"`
	CanLaunch bool    `json:"can_launch"`
}

type processView struct {
	Kind       string `json:"kind"`
	WebsiteID  int64  `json:"website_id"`
	Website    string `json:"website"`
	Port       int    `json:"port"`
	PID        int    `json:"pid"`
	StartedAt  string `json:"started_at"`
	LinkID     int64  `json:"link_id"`
	Link       string `json:"link"`
	ConfigPath string `json:"config_path"`
}

func startWebManager(dbPath string) error {
	app, err := newGUIApp(dbPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := app.closeDB(); closeErr != nil {
			app.logf("warn", "database close failed: %v", closeErr)
		}
	}()
	if runtime.GOOS == "windows" {
		return app.runWebviewDesktop()
	}
	return app.run()
}

func newGUIApp(dbPath string) (*guiApp, error) {
	rootDir := detectProjectRoot()
	webDir := filepath.Join(rootDir, "web")
	configDir := filepath.Join(rootDir, "config")
	doneDir := filepath.Join(rootDir, "done")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	if err := os.MkdirAll(doneDir, 0o755); err != nil {
		return nil, fmt.Errorf("create done dir: %w", err)
	}

	dbPath = resolveDBPath(rootDir, dbPath)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}
	dbOpenPath, dbOpenFn, dbCloseFn, err := prepareRuntimeDBPath(dbPath)
	if err != nil {
		return nil, fmt.Errorf("prepare runtime db: %w", err)
	}
	var db *sql.DB
	for attempt := 0; attempt < 2; attempt++ {
		db, err = sql.Open(sqliteDriverName, dbOpenPath)
		if err != nil {
			_ = dbCloseFn(nil)
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		db.SetMaxOpenConns(1)
		if dbOpenFn == nil {
			break
		}
		if err := dbOpenFn(db); err == nil {
			break
		} else {
			_ = db.Close()
			if attempt == 0 {
				recovered, recErr := recoverEncryptedDBOnOpenError(dbPath, err)
				if recErr != nil {
					_ = dbCloseFn(nil)
					return nil, fmt.Errorf("init sqlite session: %v; recovery failed: %w", err, recErr)
				}
				if recovered {
					dbOpenPath, dbOpenFn, dbCloseFn, err = prepareRuntimeDBPath(dbPath)
					if err != nil {
						return nil, fmt.Errorf("prepare runtime db after recovery: %w", err)
					}
					continue
				}
			}
			_ = dbCloseFn(nil)
			return nil, fmt.Errorf("init sqlite session: %w", err)
		}
	}

	app := &guiApp{
		rootDir:      rootDir,
		webDir:       webDir,
		configDir:    configDir,
		doneDir:      doneDir,
		xrayPath:     detectXrayPath(rootDir),
		dbPath:       dbPath,
		dbOpen:       dbOpenPath,
		db:           db,
		dbCloseFn:    dbCloseFn,
		siteProcs:    make(map[int64]*managedProcess),
		httpProcs:    make(map[int64]*httpBridgeProcess),
		speedRunning: make(map[int64]bool),
		logs:         make([]logEntry, 0, 128),
	}

	if err := app.initSchema(); err != nil {
		return nil, err
	}
	if err := app.ensureDefaultWebsite(); err != nil {
		return nil, err
	}
	if err := app.ensureDefaultSettings(); err != nil {
		return nil, err
	}
	if err := app.normalizeSpeedResultData(); err != nil {
		app.logf("warn", "normalize speed data failed: %v", err)
	}
	if err := app.backfillTestHistoryFromSpeedResults(); err != nil {
		app.logf("warn", "backfill test history failed: %v", err)
	}
	if err := app.backfillIPCountryHistoryFromLinks(); err != nil {
		app.logf("warn", "backfill ip country history failed: %v", err)
	}
	if err := app.ensureLinksLoaded(); err != nil {
		app.logf("warn", "initial link import failed: %v", err)
	}
	app.loadGeoDB()
	app.logf("info", "web manager ready. xray=%s db=%s", app.xrayPath, app.dbPath)
	return app, nil
}

func (a *guiApp) closeDB() error {
	if a.dbCloseFn != nil {
		return a.dbCloseFn(a.db)
	}
	if a.db != nil {
		return a.db.Close()
	}
	return nil
}

func (a *guiApp) setWindowController(fn func(action string) error) {
	a.windowMu.Lock()
	a.windowController = fn
	a.windowMu.Unlock()
}

func (a *guiApp) controlWindow(action string) error {
	a.windowMu.RLock()
	fn := a.windowController
	a.windowMu.RUnlock()
	if fn == nil {
		return errors.New("desktop window control unavailable")
	}
	return fn(action)
}

func resolveDBPath(rootDir, userPath string) string {
	p := strings.TrimSpace(userPath)
	if p == "" {
		return filepath.Join(rootDir, "daxionglink_gui.db")
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(rootDir, p)
}

func detectProjectRoot() string {
	exeDir := "."
	if ex, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(ex)
	}
	cwd, err := os.Getwd()
	if err != nil {
		if hasRuntimeAssets(exeDir) {
			return exeDir
		}
		return "."
	}
	candidates := []string{
		exeDir,
		cwd,
		filepath.Dir(cwd),
		filepath.Dir(exeDir),
	}
	for _, c := range candidates {
		if hasRuntimeAssets(c) {
			return c
		}
	}
	return exeDir
}

func hasRuntimeAssets(root string) bool {
	if root == "" {
		return false
	}
	xrayName := "xray"
	if runtime.GOOS == "windows" {
		xrayName = "xray.exe"
	}
	if fileExists(filepath.Join(root, xrayName)) {
		return true
	}
	if fileExists(filepath.Join(root, "GeoLite2-City.mmdb")) ||
		fileExists(filepath.Join(root, "GeoLite2-City-l.mmdb")) ||
		fileExists(filepath.Join(root, "geoip.dat")) ||
		fileExists(filepath.Join(root, "geosite.dat")) {
		return true
	}
	if dirExists(filepath.Join(root, "config")) || dirExists(filepath.Join(root, "done")) {
		return true
	}
	return false
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func detectXrayPath(rootDir string) string {
	name := "xray"
	if runtime.GOOS == "windows" {
		name = "xray.exe"
	}
	candidates := []string{
		filepath.Join(rootDir, name),
		filepath.Join(".", name),
		name,
	}
	for _, c := range candidates {
		if fileExists(c) {
			if abs, err := filepath.Abs(c); err == nil {
				return abs
			}
			return c
		}
	}
	return name
}
func (a *guiApp) initSchema() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS links (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	link TEXT NOT NULL UNIQUE,
	country TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS websites (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	address TEXT NOT NULL DEFAULT '',
	download_url TEXT NOT NULL DEFAULT '',
	match_rule TEXT NOT NULL DEFAULT '*',
	port INTEGER NOT NULL UNIQUE,
	is_enabled INTEGER NOT NULL DEFAULT 1,
	selected_link_id INTEGER,
	http_enabled INTEGER NOT NULL DEFAULT 0,
	http_port INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS speed_results (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	link_id INTEGER NOT NULL,
	website_id INTEGER NOT NULL,
	speed_k REAL NOT NULL DEFAULT 0,
	last_use TEXT,
	tested_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(link_id, website_id)
);

CREATE TABLE IF NOT EXISTS link_site_tests (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	link_id INTEGER NOT NULL,
	website_id INTEGER NOT NULL,
	status TEXT NOT NULL DEFAULT 'fail',
	speed_k REAL NOT NULL DEFAULT 0,
	error_msg TEXT NOT NULL DEFAULT '',
	tested_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(link_id, website_id)
);

CREATE TABLE IF NOT EXISTS link_ip_country_tests (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	link_id INTEGER NOT NULL UNIQUE,
	status TEXT NOT NULL DEFAULT 'fail',
	country TEXT NOT NULL DEFAULT '',
	error_msg TEXT NOT NULL DEFAULT '',
	tested_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS website_link_stars (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	website_id INTEGER NOT NULL,
	link_id INTEGER NOT NULL,
	starred_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE(website_id, link_id)
);

CREATE TABLE IF NOT EXISTS app_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_speed_website_speed ON speed_results(website_id, speed_k DESC);
CREATE INDEX IF NOT EXISTS idx_speed_last_use ON speed_results(last_use DESC);
CREATE INDEX IF NOT EXISTS idx_link_site_tests_link ON link_site_tests(link_id, website_id);
CREATE INDEX IF NOT EXISTS idx_link_ip_country_tests_link ON link_ip_country_tests(link_id, tested_at DESC);
CREATE INDEX IF NOT EXISTS idx_website_link_stars ON website_link_stars(website_id, link_id, updated_at DESC);
`
	_, err := a.db.Exec(ddl)
	if err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	// Backward compatibility for old databases.
	if err := a.ensureColumn("websites", "http_enabled", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := a.ensureColumn("websites", "http_port", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return nil
}

func (a *guiApp) ensureColumn(tableName, columnName, columnDef string) error {
	rows, err := a.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, columnName) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, columnName, columnDef)
	if _, err := a.db.Exec(stmt); err != nil {
		return fmt.Errorf("alter table %s add column %s failed: %w", tableName, columnName, err)
	}
	return nil
}

func (a *guiApp) ensureDefaultWebsite() error {
	const q = `
INSERT INTO websites(name, address, download_url, match_rule, port, is_enabled)
SELECT '默认设置', 'youtube.com', '', '*', ?, 1
WHERE NOT EXISTS (SELECT 1 FROM websites WHERE port = ?)`
	_, err := a.db.Exec(q, defaultPort, defaultPort)
	if err != nil {
		return fmt.Errorf("ensure default website: %w", err)
	}
	_, err = a.db.Exec(`
UPDATE websites
SET address='youtube.com', updated_at=datetime('now')
WHERE port=? AND (address='' OR address='*')`, defaultPort)
	if err != nil {
		return fmt.Errorf("normalize default website: %w", err)
	}
	return nil
}

func (a *guiApp) ensureDefaultSettings() error {
	_, err := a.db.Exec(`
INSERT INTO app_settings(key, value, updated_at)
VALUES
	('speedtest_workers', ?, datetime('now')),
	('speedtest_limit', ?, datetime('now')),
	('top_links_limit', ?, datetime('now')),
	('ip_country_top_count', ?, datetime('now'))
ON CONFLICT(key) DO NOTHING`, strconv.Itoa(defaultSpeedWorkers), strconv.Itoa(defaultSpeedLimit), strconv.Itoa(defaultTopLinksLimit), strconv.Itoa(defaultIPCountryTop))
	return err
}

func normalizeSpeedWorkers(v int) int {
	if v < minSpeedWorkers {
		return minSpeedWorkers
	}
	if v > maxSpeedWorkers {
		return maxSpeedWorkers
	}
	return v
}

func normalizeSpeedLimit(v int) int {
	if v < minSpeedLimit {
		return minSpeedLimit
	}
	if v > maxSpeedLimit {
		return maxSpeedLimit
	}
	return v
}

func normalizeTopLinksLimit(v int) int {
	if v < minTopLinksLimit {
		return minTopLinksLimit
	}
	if v > maxTopLinksLimit {
		return maxTopLinksLimit
	}
	return v
}

func normalizeIPCountryTop(v int) int {
	if v < minIPCountryTop {
		return minIPCountryTop
	}
	if v > maxIPCountryTop {
		return maxIPCountryTop
	}
	return v
}

func (a *guiApp) getSpeedWorkers() int {
	return defaultSpeedWorkers
}

func (a *guiApp) setSpeedWorkers(v int) error {
	_ = v
	_, err := a.db.Exec(`
INSERT INTO app_settings(key, value, updated_at)
VALUES('speedtest_workers', ?, datetime('now'))
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, strconv.Itoa(defaultSpeedWorkers))
	return err
}

func (a *guiApp) getSpeedLimit() int {
	var raw string
	err := a.db.QueryRow(`SELECT value FROM app_settings WHERE key='speedtest_limit'`).Scan(&raw)
	if err != nil {
		return defaultSpeedLimit
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return defaultSpeedLimit
	}
	return normalizeSpeedLimit(n)
}

func (a *guiApp) setSpeedLimit(v int) error {
	v = normalizeSpeedLimit(v)
	_, err := a.db.Exec(`
INSERT INTO app_settings(key, value, updated_at)
VALUES('speedtest_limit', ?, datetime('now'))
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, strconv.Itoa(v))
	return err
}

func (a *guiApp) getTopLinksLimit() int {
	var raw string
	err := a.db.QueryRow(`SELECT value FROM app_settings WHERE key='top_links_limit'`).Scan(&raw)
	if err != nil {
		return defaultTopLinksLimit
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return defaultTopLinksLimit
	}
	return normalizeTopLinksLimit(n)
}

func (a *guiApp) setTopLinksLimit(v int) error {
	v = normalizeTopLinksLimit(v)
	_, err := a.db.Exec(`
INSERT INTO app_settings(key, value, updated_at)
VALUES('top_links_limit', ?, datetime('now'))
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, strconv.Itoa(v))
	return err
}

func (a *guiApp) getIPCountryTop() int {
	return defaultIPCountryTop
}

func (a *guiApp) setIPCountryTop(v int) error {
	_ = v
	_, err := a.db.Exec(`
INSERT INTO app_settings(key, value, updated_at)
VALUES('ip_country_top_count', ?, datetime('now'))
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, strconv.Itoa(defaultIPCountryTop))
	return err
}

func (a *guiApp) normalizeSpeedResultData() error {
	if _, err := a.db.Exec(`UPDATE speed_results SET speed_k = ROUND(speed_k, 1)`); err != nil {
		return err
	}
	if _, err := a.db.Exec(`DELETE FROM speed_results WHERE speed_k < ?`, minValidSpeedKB); err != nil {
		return err
	}
	if _, err := a.db.Exec(`UPDATE link_site_tests SET speed_k = ROUND(speed_k, 1)`); err != nil {
		return err
	}
	if _, err := a.db.Exec(`
UPDATE link_site_tests
SET status='fail', error_msg=CASE WHEN error_msg='' THEN 'speed < 0.1KB/s' ELSE error_msg END
WHERE status='success' AND speed_k < ?`, minValidSpeedKB); err != nil {
		return err
	}
	return nil
}

func (a *guiApp) backfillTestHistoryFromSpeedResults() error {
	_, err := a.db.Exec(`
INSERT INTO link_site_tests(link_id, website_id, status, speed_k, error_msg, tested_at)
SELECT link_id, website_id, 'success', ROUND(speed_k, 1), '', tested_at
FROM speed_results
WHERE speed_k >= ?
ON CONFLICT(link_id, website_id) DO NOTHING`, minValidSpeedKB)
	return err
}

func (a *guiApp) backfillIPCountryHistoryFromLinks() error {
	_, err := a.db.Exec(`
INSERT INTO link_ip_country_tests(link_id, status, country, error_msg, tested_at)
SELECT id, 'success', TRIM(country), '', datetime('now')
FROM links
WHERE INSTR(TRIM(COALESCE(country, '')), ',') > 0
ON CONFLICT(link_id) DO NOTHING`)
	return err
}

func (a *guiApp) ensureLinksLoaded() error {
	pattern := filepath.Join(a.rootDir, "server*.txt")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		a.logf("info", "startup import: no server*.txt in %s", a.rootDir)
		return nil
	}

	totalInserted := 0
	for _, f := range files {
		if !fileExists(f) {
			continue
		}
		lines, err := readLines(f)
		if err != nil {
			a.logf("warn", "startup import read failed: %s err=%v", f, err)
			continue
		}
		inserted, err := a.insertLinksWithCount(lines)
		if err != nil {
			a.logf("warn", "startup import save failed: %s err=%v", f, err)
			continue
		}
		totalInserted += inserted
		dst, err := a.moveToDoneDir(f)
		if err != nil {
			a.logf("warn", "startup import move failed: %s err=%v", f, err)
			continue
		}
		a.logf("info", "startup import file=%s inserted=%d moved=%s", filepath.Base(f), inserted, filepath.Base(dst))
	}
	a.logf("info", "startup import completed: new links=%d", totalInserted)
	return nil
}

func (a *guiApp) insertLinks(lines []string) error {
	_, err := a.insertLinksWithCount(lines)
	return err
}

func (a *guiApp) insertLinksWithCount(lines []string) (int, error) {
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO links(link, country) VALUES(?, '')`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		res, err := stmt.Exec(line)
		if err != nil {
			return 0, err
		}
		if n, err := res.RowsAffected(); err == nil {
			inserted += int(n)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (a *guiApp) moveToDoneDir(srcPath string) (string, error) {
	base := filepath.Base(srcPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	dstPath := filepath.Join(a.doneDir, base)
	if fileExists(dstPath) {
		dstPath = filepath.Join(a.doneDir, fmt.Sprintf("%s_%s%s", name, time.Now().Format("20060102_150405"), ext))
	}
	if err := os.Rename(srcPath, dstPath); err == nil {
		return dstPath, nil
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return "", err
	}
	if err := dst.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(srcPath); err != nil {
		return "", err
	}
	return dstPath, nil
}

func (a *guiApp) loadGeoDB() {
	paths := []string{
		filepath.Join(a.rootDir, "GeoLite2-City.mmdb"),
		filepath.Join(a.rootDir, "src", "GeoLite2-City.mmdb"),
		filepath.Join(a.rootDir, "src", "GeoLite2-City-l.mmdb"),
	}
	for _, p := range paths {
		if !fileExists(p) {
			continue
		}
		db, err := geoip2.Open(p)
		if err == nil {
			a.geoDB = db
			a.logf("info", "loaded geo db: %s", p)
			return
		}
	}
	a.logf("warn", "geo db not found, country will be unknown")
}

func (a *guiApp) run() error {
	srv, ln, uiURL, err := a.prepareWebServer(50000)
	if err != nil {
		return err
	}
	a.logf("info", "web server started on %s", uiURL)
	return srv.Serve(ln)
}

func (a *guiApp) prepareWebServer(startPort int) (*http.Server, net.Listener, string, error) {
	ln, err := listenWebUI(startPort)
	if err != nil {
		return nil, nil, "", err
	}

	srv := &http.Server{
		Handler: a.newWebHandler(),
	}
	return srv, ln, "http://" + ln.Addr().String(), nil
}

func (a *guiApp) newWebHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", a.handleAPI)
	staticHandler := http.FileServer(http.FS(embeddedWebSubFS))
	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(embeddedIndexHTML)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			a.handleAPI(w, r)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		rel := strings.TrimPrefix(clean, "/")
		if rel == "" || rel == "." {
			serveIndex(w)
			return
		}
		if st, err := fs.Stat(embeddedWebSubFS, rel); err == nil && !st.IsDir() {
			req := r.Clone(r.Context())
			req.URL.Path = "/" + rel
			staticHandler.ServeHTTP(w, req)
			return
		}
		serveIndex(w)
	})
	return withJSONHeaders(mux)
}

func listenWebUI(startPort int) (net.Listener, error) {
	if startPort < 1 {
		startPort = 1
	}
	for port := startPort; port <= 65535; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
	}
	return nil, fmt.Errorf("no available web ui port in range %d-65535", startPort)
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func withJSONHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *guiApp) handleAPI(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/health" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case r.URL.Path == "/api/status" && r.Method == http.MethodGet:
		a.handleStatus(w)
	case r.URL.Path == "/api/websites":
		a.handleWebsites(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/websites/"):
		a.handleWebsiteAction(w, r)
	case r.URL.Path == "/api/start-all" && r.Method == http.MethodPost:
		a.handleStartAll(w)
	case r.URL.Path == "/api/start-rules" && r.Method == http.MethodPost:
		a.handleStartRules(w)
	case r.URL.Path == "/api/stop-all" && r.Method == http.MethodPost:
		a.handleStopAll(w)
	case r.URL.Path == "/api/kill-all-xray" && r.Method == http.MethodPost:
		a.handleKillAllXray(w)
	case r.URL.Path == "/api/kill-pid" && r.Method == http.MethodPost:
		a.handleKillPID(w, r)
	case r.URL.Path == "/api/processes" && r.Method == http.MethodGet:
		a.handleProcesses(w)
	case r.URL.Path == "/api/speedtest/all" && r.Method == http.MethodPost:
		a.handleSpeedTestAll(w)
	case r.URL.Path == "/api/settings":
		a.handleSettings(w, r)
	case r.URL.Path == "/api/window/minimize" && r.Method == http.MethodPost:
		a.handleWindowMinimize(w)
	case r.URL.Path == "/api/window/close" && r.Method == http.MethodPost:
		a.handleWindowClose(w)
	case r.URL.Path == "/api/logs" && r.Method == http.MethodGet:
		a.handleLogs(w, r)
	case r.URL.Path == "/api/links/import" && r.Method == http.MethodPost:
		a.handleImportLinks(w, r)
	case r.URL.Path == "/api/links/import-remote-base64" && r.Method == http.MethodPost:
		a.handleImportRemoteBase64Links(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/links/"):
		a.handleLinkAction(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (a *guiApp) handleStatus(w http.ResponseWriter) {
	websites, err := a.listWebsites()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	processes := a.listProcesses()
	a.logMu.Lock()
	logs := append([]logEntry(nil), a.logs...)
	a.logMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"websites":  websites,
		"processes": processes,
		"logs":      logs,
	})
}

func (a *guiApp) handleWebsites(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := a.listWebsites()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var req website
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if err := a.createWebsite(req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.logf("info", "website created: %s:%d", req.Name, req.Port)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *guiApp) handleWebsiteAction(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/websites/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "missing website id")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid website id")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	if action == "" {
		switch r.Method {
		case http.MethodPut:
			var req website
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid body")
				return
			}
			if err := a.updateWebsite(id, req); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			a.logf("info", "website updated: id=%d", id)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		case http.MethodDelete:
			if err := a.deleteWebsite(id); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			a.logf("info", "website deleted: id=%d", id)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	switch action {
	case "top-links":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		limit := a.getTopLinksLimit()
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxTopLinksLimit {
				limit = n
			}
		}
		rows, err := a.listTopLinks(id, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		totalLinks, err := a.countAllLinks()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"items":       rows,
			"total_links": totalLinks,
		})
	case "start":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := a.startWebsite(id, 0); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "stop":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.stopWebsite(id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "start-http":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := a.startWebsiteHTTP(id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "stop-http":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.stopWebsiteHTTP(id)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "speedtest":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := a.startSpeedTestCurrent(id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "ip-country":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		tested, updated, skipped, err := a.runWebsiteIPCountryTest(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"tested":  tested,
			"updated": updated,
			"skipped": skipped,
		})
	case "start-link":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			LinkID int64 `json:"link_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LinkID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid link_id")
			return
		}
		if err := a.startWebsite(id, req.LinkID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "star":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			LinkID  int64 `json:"link_id"`
			Starred bool  `json:"starred"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LinkID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid link_id")
			return
		}
		if err := a.setWebsiteLinkStar(id, req.LinkID, req.Starred); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusNotFound, "unknown action")
	}
}

func (a *guiApp) handleStartAll(w http.ResponseWriter) {
	if err := a.startAllRules(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *guiApp) handleStartRules(w http.ResponseWriter) {
	if err := a.startAllRules(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *guiApp) handleSpeedTestAll(w http.ResponseWriter) {
	if err := a.startSpeedTestAll(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *guiApp) handleStopAll(w http.ResponseWriter) {
	a.stopAll()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *guiApp) handleKillAllXray(w http.ResponseWriter) {
	if err := a.killAllXrayProcesses(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *guiApp) handleKillPID(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PID int `json:"pid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid pid")
		return
	}
	if err := a.killPID(req.PID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *guiApp) handleProcesses(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"items": a.listProcesses()})
}

func (a *guiApp) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"items": map[string]any{
				"speedtest_workers":    a.getSpeedWorkers(),
				"speedtest_limit":      a.getSpeedLimit(),
				"top_links_limit":      a.getTopLinksLimit(),
				"ip_country_top_count": a.getIPCountryTop(),
				"workers_min":          minSpeedWorkers,
				"workers_max":          maxSpeedWorkers,
				"limit_min":            minSpeedLimit,
				"limit_max":            maxSpeedLimit,
				"top_links_limit_min":  minTopLinksLimit,
				"top_links_limit_max":  maxTopLinksLimit,
				"ip_country_top_min":   minIPCountryTop,
				"ip_country_top_max":   maxIPCountryTop,
			},
		})
	case http.MethodPut:
		var req struct {
			SpeedtestWorkers *int `json:"speedtest_workers"`
			SpeedtestLimit   *int `json:"speedtest_limit"`
			TopLinksLimit    *int `json:"top_links_limit"`
			IPCountryTop     *int `json:"ip_country_top_count"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if req.SpeedtestWorkers == nil && req.SpeedtestLimit == nil && req.TopLinksLimit == nil && req.IPCountryTop == nil {
			writeError(w, http.StatusBadRequest, "no settings to update")
			return
		}
		if req.SpeedtestWorkers != nil {
			// Fixed setting: always keep speed workers at default value.
			if err := a.setSpeedWorkers(defaultSpeedWorkers); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if req.SpeedtestLimit != nil {
			if *req.SpeedtestLimit < minSpeedLimit || *req.SpeedtestLimit > maxSpeedLimit {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("speedtest_limit must be in %d-%d", minSpeedLimit, maxSpeedLimit))
				return
			}
			if err := a.setSpeedLimit(*req.SpeedtestLimit); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if req.TopLinksLimit != nil {
			if *req.TopLinksLimit < minTopLinksLimit || *req.TopLinksLimit > maxTopLinksLimit {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("top_links_limit must be in %d-%d", minTopLinksLimit, maxTopLinksLimit))
				return
			}
			if err := a.setTopLinksLimit(*req.TopLinksLimit); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if req.IPCountryTop != nil {
			// Fixed setting: always keep IP country top count at default value.
			if err := a.setIPCountryTop(defaultIPCountryTop); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		a.logf("info", "settings updated: speedtest_workers=%d speedtest_limit=%d top_links_limit=%d ip_country_top_count=%d", a.getSpeedWorkers(), a.getSpeedLimit(), a.getTopLinksLimit(), a.getIPCountryTop())
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *guiApp) handleWindowMinimize(w http.ResponseWriter) {
	if err := a.controlWindow("minimize"); err != nil {
		writeError(w, http.StatusNotImplemented, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *guiApp) handleWindowClose(w http.ResponseWriter) {
	if err := a.controlWindow("close"); err != nil {
		writeError(w, http.StatusNotImplemented, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *guiApp) handleLogs(w http.ResponseWriter, r *http.Request) {
	since := int64(0)
	if v := r.URL.Query().Get("since"); v != "" {
		n, _ := strconv.ParseInt(v, 10, 64)
		since = n
	}
	a.logMu.Lock()
	defer a.logMu.Unlock()
	items := make([]logEntry, 0, len(a.logs))
	next := since
	for _, item := range a.logs {
		if item.ID > since {
			items = append(items, item)
		}
		if item.ID > next {
			next = item.ID
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":      items,
		"next_since": next,
	})
}

func (a *guiApp) handleImportLinks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(req.File)
	if name == "" {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	if strings.Contains(name, "..") {
		writeError(w, http.StatusBadRequest, "invalid file")
		return
	}
	p := filepath.Join(a.rootDir, name)
	if !fileExists(p) {
		writeError(w, http.StatusBadRequest, "file not found")
		return
	}
	lines, err := readLines(p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.insertLinks(lines); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.logf("info", "imported %d links from %s", len(lines), p)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(lines)})
}

func (a *guiApp) handleImportRemoteBase64Links(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	sourceURL, err := normalizeImportSourceURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	count, total, via, err := a.importRemoteBase64Links(sourceURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"count":  count,
		"total":  total,
		"source": sourceURL,
		"via":    via,
	})
}

func normalizeImportSourceURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return remoteBase64LinksURL, nil
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("导入URL无效")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("导入URL只支持 http 或 https")
	}
	return u.String(), nil
}

func (a *guiApp) importRemoteBase64Links(sourceURL string) (int, int, string, error) {
	raw, via, err := a.downloadRemoteBase64Source(sourceURL)
	if err != nil {
		return 0, 0, "", err
	}
	encoded := strings.Join(strings.Fields(string(raw)), "")

	var decoded []byte
	decodeFns := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	}
	for _, fn := range decodeFns {
		decoded, err = fn(encoded)
		if err == nil {
			break
		}
	}
	if err != nil {
		return 0, 0, "", fmt.Errorf("decode remote base64 links failed: %w", err)
	}

	lines := splitUniqueNonEmptyLines(string(decoded))
	sourceTotal := len(lines)
	if sourceTotal == 0 {
		return 0, 0, "", errors.New("remote base64 decoded to empty content")
	}
	if len(lines) > remoteImportTopLimit {
		lines = lines[:remoteImportTopLimit]
	}
	inserted, err := a.insertLinksWithCount(lines)
	if err != nil {
		return 0, 0, "", err
	}
	a.logf("info", "remote base64 import completed: source=%s via=%s source_total=%d used=%d(limit=%d) inserted=%d",
		sourceURL, via, sourceTotal, len(lines), remoteImportTopLimit, inserted)
	return inserted, len(lines), via, nil
}

func (a *guiApp) downloadRemoteBase64Source(sourceURL string) ([]byte, string, error) {
	a.logf("info", "remote base64 download try default proxy first: socks=%d source=%s", defaultPort, sourceURL)
	raw, err := a.fetchRemoteBase64ViaSocks(sourceURL, defaultPort)
	if err == nil {
		return raw, "default-socks", nil
	}

	a.logf("warn", "remote base64 download fallback to direct network: socks=%d source=%s err=%v", defaultPort, sourceURL, err)
	raw, directErr := a.fetchRemoteBase64Direct(sourceURL)
	if directErr != nil {
		return nil, "", fmt.Errorf("download remote base64 links failed: proxy err=%v; direct err=%w", err, directErr)
	}
	return raw, "direct", nil
}

func (a *guiApp) fetchRemoteBase64Direct(sourceURL string) ([]byte, error) {
	client := &http.Client{Timeout: 45 * time.Second}
	req, err := http.NewRequest(http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "daxionglink-gui/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download remote base64 links failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("download remote base64 links failed: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read remote base64 links failed: %w", err)
	}
	return raw, nil
}

func (a *guiApp) fetchRemoteBase64ViaSocks(sourceURL string, port int) ([]byte, error) {
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("create socks5 dialer failed: %w", err)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 20 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   45 * time.Second,
	}
	req, err := http.NewRequest(http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "daxionglink-gui/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read remote base64 links failed: %w", err)
	}
	return raw, nil
}

func splitUniqueNonEmptyLines(content string) []string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	out := make([]string, 0, 256)
	seen := make(map[string]struct{}, 256)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	return out
}

func (a *guiApp) handleLinkAction(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/links/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "missing link id")
		return
	}
	linkID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || linkID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid link id")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "country":
		if r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Country string `json:"country"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if err := a.updateLinkCountry(linkID, req.Country); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"link_id": linkID,
			"country": strings.TrimSpace(req.Country),
		})
	default:
		writeError(w, http.StatusNotFound, "unknown action")
	}
}

func (a *guiApp) listWebsites() ([]website, error) {
	rows, err := a.db.Query(`
SELECT id, name, address, download_url, match_rule, port, is_enabled,
       COALESCE(selected_link_id, 0), COALESCE(http_enabled, 0), COALESCE(http_port, 0), created_at, updated_at
FROM websites
ORDER BY CASE WHEN port = ? THEN 0 ELSE 1 END, id`, defaultPort)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	a.mu.Lock()
	procs := make(map[int64]*managedProcess, len(a.siteProcs))
	for id, p := range a.siteProcs {
		procs[id] = p
	}
	httpProcs := make(map[int64]*httpBridgeProcess, len(a.httpProcs))
	for id, p := range a.httpProcs {
		httpProcs[id] = p
	}
	a.mu.Unlock()

	items := make([]website, 0)
	for rows.Next() {
		var it website
		var enabled int
		var httpEnabled int
		if err := rows.Scan(
			&it.ID, &it.Name, &it.Address, &it.DownloadURL, &it.MatchRule, &it.Port,
			&enabled, &it.SelectedLinkID, &httpEnabled, &it.HTTPPort, &it.CreatedAt, &it.UpdatedAt,
		); err != nil {
			return nil, err
		}
		it.IsEnabled = enabled == 1
		it.HTTPEnabled = httpEnabled == 1
		if p := procs[it.ID]; p != nil {
			it.Running = true
			it.PID = p.pid
			if p.linkID > 0 {
				it.SelectedLinkID = p.linkID
			}
		}
		if hp := httpProcs[it.ID]; hp != nil {
			it.HTTPRunning = true
			if it.HTTPPort == 0 {
				it.HTTPPort = hp.httpPort
			}
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (a *guiApp) createWebsite(req website) error {
	req.Name = strings.TrimSpace(req.Name)
	req.Address = strings.TrimSpace(req.Address)
	req.DownloadURL = strings.TrimSpace(req.DownloadURL)
	req.MatchRule = strings.TrimSpace(req.MatchRule)

	if req.Name == "" {
		return errors.New("name is required")
	}
	if req.MatchRule == "" {
		req.MatchRule = "*"
	}
	if req.Port < portMin || req.Port > portMax {
		return fmt.Errorf("port must be in %d-%d", portMin, portMax)
	}
	httpPort, err := normalizeWebsiteHTTPPort(req.Port, req.HTTPPort)
	if err != nil {
		return err
	}
	if err := a.validateWebsiteHTTPPortUnique(0, httpPort); err != nil {
		return err
	}
	if err := a.ensureWebsiteCreateAllowed(); err != nil {
		return err
	}

	enabled := 0
	if req.IsEnabled {
		enabled = 1
	}
	httpEnabled := 0
	if req.HTTPEnabled {
		httpEnabled = 1
	}
	_, err = a.db.Exec(`
INSERT INTO websites(name, address, download_url, match_rule, port, is_enabled, http_enabled, http_port, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		req.Name, req.Address, req.DownloadURL, req.MatchRule, req.Port, enabled, httpEnabled, httpPort)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return errors.New("port already exists, choose another one")
		}
		return err
	}
	return nil
}

func (a *guiApp) ensureWebsiteCreateAllowed() error {
	var total int
	if err := a.db.QueryRow(`SELECT COUNT(1) FROM websites`).Scan(&total); err != nil {
		return err
	}
	if total >= maxWebsiteCount {
		return fmt.Errorf("website limit reached: max %d. advanced version: %s", maxWebsiteCount, advancedGithubURL)
	}
	return nil
}

func (a *guiApp) updateWebsite(id int64, req website) error {
	current, err := a.getWebsite(id)
	if err != nil {
		return err
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Address = strings.TrimSpace(req.Address)
	req.DownloadURL = strings.TrimSpace(req.DownloadURL)
	req.MatchRule = strings.TrimSpace(req.MatchRule)

	if req.Name == "" {
		return errors.New("name is required")
	}
	if req.MatchRule == "" {
		req.MatchRule = "*"
	}
	if current.Port == defaultPort {
		req.Port = defaultPort
	}
	if req.Port == 0 {
		req.Port = current.Port
	}
	if req.Port != defaultPort && (req.Port < portMin || req.Port > portMax) {
		return fmt.Errorf("port must be in %d-%d", portMin, portMax)
	}
	httpPort, err := normalizeWebsiteHTTPPort(req.Port, req.HTTPPort)
	if err != nil {
		return err
	}
	if err := a.validateWebsiteHTTPPortUnique(id, httpPort); err != nil {
		return err
	}

	enabled := 0
	if req.IsEnabled {
		enabled = 1
	}
	httpEnabled := 0
	if req.HTTPEnabled {
		httpEnabled = 1
	}

	if current.Port != req.Port {
		a.stopWebsite(id)
	}
	if current.HTTPPort != httpPort || (current.HTTPEnabled && !req.HTTPEnabled) {
		a.stopWebsiteHTTP(id)
	}

	_, err = a.db.Exec(`
UPDATE websites
SET name=?, address=?, download_url=?, match_rule=?, port=?, is_enabled=?, http_enabled=?, http_port=?, updated_at=datetime('now')
WHERE id=?`,
		req.Name, req.Address, req.DownloadURL, req.MatchRule, req.Port, enabled, httpEnabled, httpPort, id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return errors.New("port already exists, choose another one")
		}
		return err
	}
	if req.HTTPEnabled {
		a.mu.Lock()
		running := a.siteProcs[id] != nil
		a.mu.Unlock()
		if running {
			if err := a.startWebsiteHTTP(id); err != nil {
				a.logf("warn", "update website auto start http failed: id=%d err=%v", id, err)
			}
		}
	}
	return nil
}

func (a *guiApp) deleteWebsite(id int64) error {
	ws, err := a.getWebsite(id)
	if err != nil {
		return err
	}
	if ws.Port == defaultPort {
		return errors.New("default website cannot be deleted")
	}
	a.stopWebsite(id)
	_, err = a.db.Exec(`DELETE FROM speed_results WHERE website_id=?`, id)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`DELETE FROM websites WHERE id=?`, id)
	if err != nil {
		return err
	}
	return nil
}

func (a *guiApp) getWebsite(id int64) (*website, error) {
	var ws website
	var enabled int
	var httpEnabled int
	err := a.db.QueryRow(`
SELECT id, name, address, download_url, match_rule, port, is_enabled,
       COALESCE(selected_link_id, 0), COALESCE(http_enabled, 0), COALESCE(http_port, 0), created_at, updated_at
FROM websites WHERE id=?`, id).Scan(
		&ws.ID, &ws.Name, &ws.Address, &ws.DownloadURL, &ws.MatchRule,
		&ws.Port, &enabled, &ws.SelectedLinkID, &httpEnabled, &ws.HTTPPort, &ws.CreatedAt, &ws.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("website not found")
	}
	if err != nil {
		return nil, err
	}
	ws.IsEnabled = enabled == 1
	ws.HTTPEnabled = httpEnabled == 1
	return &ws, nil
}

func normalizeWebsiteHTTPPort(sitePort int, rawHTTPPort int) (int, error) {
	if rawHTTPPort <= 0 {
		return 0, nil
	}
	if rawHTTPPort < 1 || rawHTTPPort > 65535 {
		return 0, errors.New("http port must be in 1-65535")
	}
	if rawHTTPPort == sitePort {
		return 0, errors.New("http port cannot equal website socks port")
	}
	return rawHTTPPort, nil
}

func (a *guiApp) validateWebsiteHTTPPortUnique(websiteID int64, httpPort int) error {
	if httpPort <= 0 {
		return nil
	}
	var existsID int64
	err := a.db.QueryRow(`SELECT id FROM websites WHERE id != ? AND http_port = ? LIMIT 1`, websiteID, httpPort).Scan(&existsID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("http port %d already exists, choose another one", httpPort)
}

func resolveWebsiteHTTPPort(sitePort int, httpPort int) (int, error) {
	if httpPort > 0 {
		return httpPort, nil
	}
	if sitePort >= 65535 {
		return 0, errors.New("invalid socks port for default http port")
	}
	return sitePort + 1, nil
}
func (a *guiApp) listTopLinks(websiteID int64, limit int) ([]speedRow, error) {
	const q = `
SELECT l.id, l.link, COALESCE(l.country, ''),
       COALESCE(s.speed_k, 0), COALESCE(s.tested_at, ''), COALESCE(s.last_use, ''),
       CASE WHEN w.selected_link_id = l.id THEN 1 ELSE 0 END,
       CASE WHEN ws.link_id IS NOT NULL THEN 1 ELSE 0 END
FROM links l
JOIN websites w ON w.id = ?
LEFT JOIN speed_results s ON s.link_id = l.id AND s.website_id = w.id
LEFT JOIN website_link_stars ws ON ws.website_id = w.id AND ws.link_id = l.id
ORDER BY
       CASE WHEN ws.link_id IS NOT NULL THEN 0 ELSE 1 END ASC,
       s.last_use DESC,
       s.speed_k DESC,
       s.tested_at DESC,
       l.id ASC
LIMIT ?`
	rows, err := a.db.Query(q, websiteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]speedRow, 0, limit)
	for rows.Next() {
		var it speedRow
		var selected int
		var starred int
		if err := rows.Scan(&it.LinkID, &it.Link, &it.Country, &it.SpeedK, &it.TestedAt, &it.LastUse, &selected, &starred); err != nil {
			return nil, err
		}
		it.Selected = selected == 1
		it.Starred = starred == 1
		it.CanLaunch = it.SpeedK > 0
		items = append(items, it)
	}
	return items, rows.Err()
}

func (a *guiApp) countAllLinks() (int64, error) {
	var total int64
	if err := a.db.QueryRow(`SELECT COUNT(1) FROM links`).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (a *guiApp) startSpeedTestCurrent(websiteID int64) error {
	if _, err := a.getWebsite(websiteID); err != nil {
		return err
	}
	return a.startSpeedTestWithMode(websiteID, false)
}

func (a *guiApp) startSpeedTestAll() error {
	return a.startSpeedTestWithMode(0, true)
}

func (a *guiApp) runWebsiteIPCountryTest(websiteID int64) (int, int, int, error) {
	ws, err := a.getWebsite(websiteID)
	if err != nil {
		return 0, 0, 0, err
	}

	a.ipCountryMu.Lock()
	if a.ipCountryRun {
		a.ipCountryMu.Unlock()
		return 0, 0, 0, errors.New("当前正在测试IP属地，请稍后再试")
	}
	a.ipCountryRun = true
	a.ipCountryMu.Unlock()
	defer func() {
		a.ipCountryMu.Lock()
		a.ipCountryRun = false
		a.ipCountryMu.Unlock()
	}()

	limit := a.getIPCountryTop()
	links, err := a.loadSpeedRankedLinksForWebsite(websiteID)
	if err != nil {
		return 0, 0, 0, err
	}
	if len(links) == 0 {
		return 0, 0, 0, errors.New("当前网站没有测速结果，请先测试速度")
	}

	port := 15000 + int(websiteID%1000)
	if port < 15000 || port > 62000 {
		port = 15000
	}
	cfg := filepath.Join(a.configDir, fmt.Sprintf("tmp_ip_country_%d.json", websiteID))
	defer os.Remove(cfg)

	a.logf("info", "ip country test started: website=%s links=%d(limit=%d) api=%s", ws.Name, len(links), limit, myIPAPIURL)

	tested := 0
	updated := 0
	skipped := 0
	for _, item := range links {
		if tested >= limit {
			break
		}

		exists, checkErr := a.hasLinkIPCountryTest(item.ID)
		if checkErr != nil {
			a.logf("warn", "ip country history check failed: site=%d link=%d err=%v", websiteID, item.ID, checkErr)
		}
		if exists {
			skipped++
			a.logf("info", "ip country skip: site=%d(%s) link=%d reason=already-tested", websiteID, ws.Name, item.ID)
			continue
		}

		tested++
		tag := fmt.Sprintf("ip-country:site=%d link=%d", websiteID, item.ID)
		cmd, err := a.startProbeXray(item.Link, port, cfg, tag)
		if err != nil {
			a.logf("warn", "ip country xray start failed: site=%d link=%d err=%v", websiteID, item.ID, err)
			if recErr := a.recordLinkIPCountryTest(item.ID, "fail", "", err.Error()); recErr != nil {
				a.logf("warn", "record ip country history failed: link=%d err=%v", item.ID, recErr)
			}
			continue
		}

		country, ip, cc, err := a.lookupCountryWithRunningXray(port)
		a.stopProbeXray(cmd)
		if err != nil {
			a.logf("warn", "ip country fetch failed: site=%d link=%d err=%v", websiteID, item.ID, err)
			if recErr := a.recordLinkIPCountryTest(item.ID, "fail", "", err.Error()); recErr != nil {
				a.logf("warn", "record ip country history failed: link=%d err=%v", item.ID, recErr)
			}
			continue
		}

		countryValue := formatIPCountryValue(cc, ip)
		if _, err := a.db.Exec(`UPDATE links SET country=? WHERE id=?`, countryValue, item.ID); err != nil {
			a.logf("warn", "ip country save failed: site=%d link=%d err=%v", websiteID, item.ID, err)
			if recErr := a.recordLinkIPCountryTest(item.ID, "fail", "", err.Error()); recErr != nil {
				a.logf("warn", "record ip country history failed: link=%d err=%v", item.ID, recErr)
			}
			continue
		}
		if recErr := a.recordLinkIPCountryTest(item.ID, "success", countryValue, ""); recErr != nil {
			a.logf("warn", "record ip country history failed: link=%d err=%v", item.ID, recErr)
		}
		updated++
		a.logf("info", "ip country updated: site=%d(%s) link=%d ip=%s country=%s cc=%s", websiteID, ws.Name, item.ID, ip, country, cc)
	}

	a.logf("info", "ip country test finished: website=%s tested=%d updated=%d skipped=%d", ws.Name, tested, updated, skipped)
	if tested == 0 && skipped > 0 {
		return tested, updated, skipped, nil
	}
	if updated == 0 {
		return tested, updated, skipped, errors.New("未获取到任何IP属地，请查看日志")
	}
	return tested, updated, skipped, nil
}

func (a *guiApp) startSpeedTestWithMode(websiteID int64, allWebsites bool) error {
	if err := a.ensureLinksLoaded(); err != nil {
		a.logf("warn", "speed test pre-import server files failed: %v", err)
	}
	const globalSpeedTestKey int64 = 0
	a.speedMu.Lock()
	if a.speedRunning[globalSpeedTestKey] {
		a.speedMu.Unlock()
		return errors.New("speed test is already running")
	}
	a.speedRunning[globalSpeedTestKey] = true
	a.speedMu.Unlock()

	go a.runSpeedTest(websiteID, allWebsites)
	return nil
}

func (a *guiApp) runSpeedTest(websiteID int64, allWebsites bool) {
	const globalSpeedTestKey int64 = 0
	defer func() {
		a.speedMu.Lock()
		delete(a.speedRunning, globalSpeedTestKey)
		a.speedMu.Unlock()
	}()

	var (
		triggerName    string
		websitesToTest []website
		err            error
	)
	if allWebsites {
		triggerName = "ALL_WEBSITES"
		websitesToTest, err = a.listWebsitesForSpeedTest()
		if err != nil {
			a.logf("error", "load websites failed: %v", err)
			return
		}
	} else {
		ws, err := a.getWebsite(websiteID)
		if err != nil {
			a.logf("error", "speed test aborted: %v", err)
			return
		}
		triggerName = ws.Name
		websitesToTest = []website{*ws}
	}
	if len(websitesToTest) == 0 {
		a.logf("warn", "speed test no websites available")
		return
	}

	links, err := a.loadSpeedTestLinks(websiteID, allWebsites)
	if err != nil {
		a.logf("error", "load links failed: %v", err)
		return
	}
	if len(links) == 0 {
		a.logf("warn", "speed test no links; import links first")
		return
	}

	limit := a.getSpeedLimit()
	if len(links) > limit {
		links = links[:limit]
	}

	workers := a.getSpeedWorkers()
	if workers > len(links) {
		workers = len(links)
	}
	if workers < 1 {
		workers = 1
	}
	basePort := 13000 + int(websiteID%1000)*20
	if basePort < 13000 || basePort > 62000 {
		basePort = 13000
	}

	modeName := "current-website"
	if allWebsites {
		modeName = "all-websites"
	}
	a.logf("info", "speed test started: trigger=%s links=%d(limit=%d) websites=%d workers=%d mode=%s one-link-one-xray skip-tested",
		triggerName, len(links), limit, len(websitesToTest), workers, modeName)

	jobs := make(chan linkRecord, len(links))
	for _, link := range links {
		jobs <- link
	}
	close(jobs)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			port := basePort + workerID
			cfg := filepath.Join(a.configDir, fmt.Sprintf("tmp_speed_%d_%d.json", websiteID, workerID))
			for item := range jobs {
				tag := fmt.Sprintf("speedtest:w%d link=%d", workerID, item.ID)
				testedSet, err := a.loadTestedWebsiteSet(item.ID)
				if err != nil {
					a.logf("warn", "speed test failed load tested set: link=%d err=%v", item.ID, err)
					continue
				}
				if allTargetWebsitesTested(testedSet, websitesToTest) {
					a.logf("info", "speed test skip link=%d reason=already-tested-target-websites", item.ID)
					continue
				}

				cmd, err := a.startProbeXray(item.Link, port, cfg, tag)
				if err != nil {
					a.logf("warn", "speed test xray start failed: link=%d err=%v", item.ID, err)
					continue
				}

				country := ""
				successCount := 0
				skipCount := 0
				failCount := 0
				for _, site := range websitesToTest {
					if testedSet[site.ID] {
						skipCount++
						continue
					}
					targetURL, downloadMode := chooseTargetURL(&site)
					speedKB, extractedIP, err := a.measureTargetWithRunningXray(port, targetURL, downloadMode)
					if err != nil {
						failCount++
						a.logf("warn", "speed test failed: link=%d site=%d(%s) err=%v", item.ID, site.ID, site.Name, err)
						if recErr := a.recordLinkWebsiteTest(site.ID, item.ID, "fail", 0, err.Error()); recErr != nil {
							a.logf("warn", "record test history failed: site=%d link=%d err=%v", site.ID, item.ID, recErr)
						}
						testedSet[site.ID] = true
						continue
					}
					speedKB = roundTo1Decimal(speedKB)
					if speedKB < minValidSpeedKB {
						failCount++
						msg := fmt.Sprintf("speed %.1fKB/s < %.1fKB/s", speedKB, minValidSpeedKB)
						a.logf("warn", "speed test invalid: link=%d site=%d(%s) %s", item.ID, site.ID, site.Name, msg)
						if recErr := a.recordLinkWebsiteTest(site.ID, item.ID, "fail", speedKB, msg); recErr != nil {
							a.logf("warn", "record test history failed: site=%d link=%d err=%v", site.ID, item.ID, recErr)
						}
						testedSet[site.ID] = true
						continue
					}
					if err := a.upsertSpeedResult(site.ID, item.ID, speedKB); err != nil {
						failCount++
						a.logf("warn", "speed test save failed: link=%d site=%d err=%v", item.ID, site.ID, err)
						if recErr := a.recordLinkWebsiteTest(site.ID, item.ID, "fail", speedKB, err.Error()); recErr != nil {
							a.logf("warn", "record test history failed: site=%d link=%d err=%v", site.ID, item.ID, recErr)
						}
						testedSet[site.ID] = true
						continue
					}
					successCount++
					testedSet[site.ID] = true
					if recErr := a.recordLinkWebsiteTest(site.ID, item.ID, "success", speedKB, ""); recErr != nil {
						a.logf("warn", "record test history failed: site=%d link=%d err=%v", site.ID, item.ID, recErr)
					}
					if country == "" || country == "unknown" {
						country = a.resolveCountry(extractedIP, item.Link)
					}
				}
				if country != "" && country != "unknown" {
					_, _ = a.db.Exec(`UPDATE links SET country=? WHERE id=?`, country, item.ID)
				}
				a.stopProbeXray(cmd)
				a.logf("info", "speed test link=%d done success=%d skip=%d fail=%d", item.ID, successCount, skipCount, failCount)
			}
			_ = os.Remove(cfg)
		}(i)
	}
	wg.Wait()

	a.logf("info", "speed test finished: trigger=%s mode=%s", triggerName, modeName)
}

func (a *guiApp) loadSpeedTestLinks(websiteID int64, allWebsites bool) ([]linkRecord, error) {
	if !allWebsites && websiteID > 0 {
		return a.loadAllLinksNewestFirst()
	}
	return a.loadAllLinks()
}

func (a *guiApp) loadAllLinksNewestFirst() ([]linkRecord, error) {
	rows, err := a.db.Query(`
SELECT l.id, l.link, l.country
FROM links l
ORDER BY l.created_at DESC, l.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]linkRecord, 0, 256)
	for rows.Next() {
		var it linkRecord
		if err := rows.Scan(&it.ID, &it.Link, &it.Country); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (a *guiApp) listWebsitesForSpeedTest() ([]website, error) {
	rows, err := a.db.Query(`
SELECT id, name, address, download_url, match_rule, port, is_enabled,
       COALESCE(selected_link_id, 0), created_at, updated_at
FROM websites
ORDER BY CASE WHEN port = ? THEN 0 ELSE 1 END, id`, defaultPort)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]website, 0, 16)
	for rows.Next() {
		var it website
		var enabled int
		if err := rows.Scan(&it.ID, &it.Name, &it.Address, &it.DownloadURL, &it.MatchRule, &it.Port, &enabled, &it.SelectedLinkID, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.IsEnabled = enabled == 1
		items = append(items, it)
	}
	return items, rows.Err()
}

func (a *guiApp) loadTestedWebsiteSet(linkID int64) (map[int64]bool, error) {
	rows, err := a.db.Query(`SELECT website_id FROM link_site_tests WHERE link_id=?`, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]bool)
	for rows.Next() {
		var websiteID int64
		if err := rows.Scan(&websiteID); err != nil {
			return nil, err
		}
		out[websiteID] = true
	}
	return out, rows.Err()
}

func allTargetWebsitesTested(testedSet map[int64]bool, websites []website) bool {
	if len(websites) == 0 {
		return false
	}
	for _, site := range websites {
		if !testedSet[site.ID] {
			return false
		}
	}
	return true
}

func chooseTargetURL(ws *website) (string, bool) {
	if ws == nil {
		return defaultTestURL, false
	}
	if dl := strings.TrimSpace(ws.DownloadURL); dl != "" {
		return normalizeRawURL(dl), true
	}
	addr := strings.TrimSpace(ws.Address)
	if addr == "" || addr == "*" {
		addr = defaultTestURL
	}
	return normalizeRawURL(addr), false
}

func normalizeRawURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultTestURL
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	raw = strings.TrimPrefix(raw, "*.")
	return "https://" + raw
}

func (a *guiApp) loadSpeedRankedLinksForWebsite(websiteID int64) ([]linkRecord, error) {
	rows, err := a.db.Query(`
SELECT l.id, l.link, COALESCE(l.country, '')
FROM speed_results s
JOIN links l ON l.id = s.link_id
WHERE s.website_id = ? AND s.speed_k >= ?
ORDER BY s.speed_k DESC, COALESCE(s.last_use, '') DESC, s.tested_at DESC, l.id ASC
`, websiteID, minValidSpeedKB)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]linkRecord, 0, 64)
	for rows.Next() {
		var it linkRecord
		if err := rows.Scan(&it.ID, &it.Link, &it.Country); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (a *guiApp) loadAllLinks() ([]linkRecord, error) {
	rows, err := a.db.Query(`
SELECT l.id, l.link, l.country
FROM links l
LEFT JOIN (
	SELECT link_id, COUNT(1) AS tested_count
	FROM link_site_tests
	GROUP BY link_id
) t ON t.link_id = l.id
ORDER BY
	CASE WHEN t.tested_count IS NULL THEN 0 ELSE 1 END ASC,
	COALESCE(t.tested_count, 0) ASC,
	l.created_at DESC,
	l.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]linkRecord, 0, 256)
	for rows.Next() {
		var it linkRecord
		if err := rows.Scan(&it.ID, &it.Link, &it.Country); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (a *guiApp) startProbeXray(link string, port int, cfg string, logTag string) (*exec.Cmd, error) {
	if err := protocol.GenerateXrayConfig(link, port, cfg); err != nil {
		return nil, err
	}
	if err := setXrayConfigLogLevel(cfg, "info"); err != nil {
		a.logf("warn", "speedtest verbose log config failed: %v", err)
	}

	cmd := exec.Command(a.xrayPath, "run", "-c", cfg)
	hideCommandWindow(cmd)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go a.captureProcessLogs(logTag, stdout)
	go a.captureProcessLogs(logTag, stderr)

	time.Sleep(900 * time.Millisecond)
	return cmd, nil
}

func (a *guiApp) stopProbeXray(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

func (a *guiApp) measureTargetWithRunningXray(port int, targetURL string, downloadMode bool) (float64, string, error) {
	var (
		speed       float64
		extractedIP string
		err         error
	)
	if downloadMode {
		speed, extractedIP, err = protocol.MeasureDownloadSpeed(port, targetURL, 8*time.Second)
	} else {
		speed, extractedIP, err = protocol.MeasureAdvanced(port, targetURL)
	}
	if err != nil || speed <= 0 {
		if err == nil {
			err = errors.New("speed is zero")
		}
		return 0, "", err
	}
	return speed / 1024.0, extractedIP, nil
}

func (a *guiApp) lookupCountryWithRunningXray(port int) (string, string, string, error) {
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, proxy.Direct)
	if err != nil {
		return "", "", "", fmt.Errorf("create socks5 dialer failed: %w", err)
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 10 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   12 * time.Second,
	}

	resp, err := client.Get(myIPAPIURL)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var payload struct {
		IP      string `json:"ip"`
		Country string `json:"country"`
		CC      string `json:"cc"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8*1024)).Decode(&payload); err != nil {
		return "", "", "", fmt.Errorf("decode myip response failed: %w", err)
	}

	country := strings.TrimSpace(payload.Country)
	if country == "" {
		country = strings.TrimSpace(payload.CC)
	}
	if country == "" {
		country = "unknown"
	}
	return country, strings.TrimSpace(payload.IP), strings.TrimSpace(payload.CC), nil
}

func formatIPCountryValue(cc, ip string) string {
	cc = strings.TrimSpace(strings.ToUpper(cc))
	if cc == "" {
		cc = "unknown"
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	return cc + "," + ip
}

func isCheckedIPCountryValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	left, right, ok := strings.Cut(value, ",")
	if !ok {
		return false
	}
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && right != "" && net.ParseIP(right) != nil
}

func (a *guiApp) resolveCountry(extractedIP, link string) string {
	if a.geoDB == nil {
		return "unknown"
	}
	ipStr := strings.TrimSpace(extractedIP)
	if ipStr == "" {
		node, err := protocol.ParseNode(link)
		if err == nil {
			ipStr = strings.TrimSpace(node.Address)
		}
	}
	ip := net.ParseIP(strings.Trim(ipStr, "[]"))
	if ip == nil {
		return "unknown"
	}
	rec, err := a.geoDB.City(ip)
	if err != nil || rec == nil {
		return "unknown"
	}
	if val, ok := rec.Country.Names["en"]; ok && val != "" {
		return val
	}
	if rec.Country.IsoCode != "" {
		return rec.Country.IsoCode
	}
	return "unknown"
}

func (a *guiApp) upsertSpeedResult(websiteID, linkID int64, speedKB float64) error {
	speedKB = roundTo1Decimal(speedKB)
	if speedKB < minValidSpeedKB {
		return fmt.Errorf("invalid speed %.1fKB/s (< %.1fKB/s)", speedKB, minValidSpeedKB)
	}
	_, err := a.db.Exec(`
INSERT INTO speed_results(link_id, website_id, speed_k, tested_at)
VALUES(?, ?, ?, datetime('now'))
ON CONFLICT(link_id, website_id)
DO UPDATE SET speed_k=excluded.speed_k, tested_at=excluded.tested_at`, linkID, websiteID, speedKB)
	return err
}

func (a *guiApp) recordLinkWebsiteTest(websiteID, linkID int64, status string, speedKB float64, errMsg string) error {
	status = strings.TrimSpace(strings.ToLower(status))
	if status != "success" {
		status = "fail"
	}
	speedKB = roundTo1Decimal(speedKB)
	if speedKB < 0 {
		speedKB = 0
	}
	errMsg = strings.TrimSpace(errMsg)
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}

	_, err := a.db.Exec(`
INSERT INTO link_site_tests(link_id, website_id, status, speed_k, error_msg, tested_at)
VALUES(?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(link_id, website_id)
DO UPDATE SET status=excluded.status, speed_k=excluded.speed_k, error_msg=excluded.error_msg, tested_at=excluded.tested_at`,
		linkID, websiteID, status, speedKB, errMsg)
	return err
}

func (a *guiApp) hasLinkIPCountryTest(linkID int64) (bool, error) {
	var country string
	err := a.db.QueryRow(`SELECT COALESCE(country, '') FROM links WHERE id=? LIMIT 1`, linkID).Scan(&country)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return isCheckedIPCountryValue(country), nil
}

func (a *guiApp) recordLinkIPCountryTest(linkID int64, status, country, errMsg string) error {
	status = strings.TrimSpace(strings.ToLower(status))
	if status != "success" {
		status = "fail"
	}
	country = strings.TrimSpace(country)
	errMsg = strings.TrimSpace(errMsg)
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}

	_, err := a.db.Exec(`
INSERT INTO link_ip_country_tests(link_id, status, country, error_msg, tested_at)
VALUES(?, ?, ?, ?, datetime('now'))
ON CONFLICT(link_id)
DO UPDATE SET status=excluded.status, country=excluded.country, error_msg=excluded.error_msg, tested_at=excluded.tested_at`,
		linkID, status, country, errMsg)
	return err
}

func roundTo1Decimal(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10) / 10
}

func (a *guiApp) startAllRules() error {
	websites, err := a.listWebsites()
	if err != nil {
		return err
	}
	var errs []string
	for _, ws := range websites {
		if !ws.IsEnabled {
			continue
		}
		if err := a.startWebsite(ws.ID, 0); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", ws.Name, err))
		}
	}
	if err := a.startMainRouter(); err != nil {
		errs = append(errs, fmt.Sprintf("main router: %v", err))
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (a *guiApp) startWebsite(websiteID int64, forcedLinkID int64) error {
	ws, err := a.getWebsite(websiteID)
	if err != nil {
		return err
	}
	linkID, linkText, err := a.pickLinkForWebsite(websiteID, ws.SelectedLinkID, forcedLinkID)
	if err != nil {
		return err
	}

	a.stopWebsite(websiteID)

	configPath := filepath.Join(a.configDir, fmt.Sprintf("config_gui_%d.json", ws.Port))
	if err := protocol.GenerateXrayConfig(linkText, ws.Port, configPath); err != nil {
		return fmt.Errorf("generate xray config: %w", err)
	}

	cmd := exec.Command(a.xrayPath, "run", "-c", configPath)
	hideCommandWindow(cmd)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start xray: %w", err)
	}

	proc := &managedProcess{
		websiteID: websiteID,
		name:      ws.Name,
		port:      ws.Port,
		pid:       cmd.Process.Pid,
		linkID:    linkID,
		link:      linkText,
		config:    configPath,
		startedAt: time.Now(),
		cmd:       cmd,
		done:      make(chan struct{}),
	}

	a.mu.Lock()
	a.siteProcs[websiteID] = proc
	a.mu.Unlock()

	go a.captureProcessLogs(fmt.Sprintf("site:%s", ws.Name), stdout)
	go a.captureProcessLogs(fmt.Sprintf("site:%s", ws.Name), stderr)
	go a.monitorProcess(proc, false)

	_, _ = a.db.Exec(`UPDATE websites SET selected_link_id=?, updated_at=datetime('now') WHERE id=?`, linkID, websiteID)
	_, _ = a.db.Exec(`UPDATE speed_results SET last_use=datetime('now') WHERE website_id=? AND link_id=?`, websiteID, linkID)

	a.logf("info", "site started: %s port=%d pid=%d", ws.Name, ws.Port, proc.pid)
	if ws.HTTPEnabled {
		if err := a.startWebsiteHTTP(websiteID); err != nil {
			a.logf("warn", "auto start http bridge failed: site=%s err=%v", ws.Name, err)
		}
	}
	return nil
}

func (a *guiApp) startWebsiteHTTP(websiteID int64) error {
	ws, err := a.getWebsite(websiteID)
	if err != nil {
		return err
	}
	httpPort, err := resolveWebsiteHTTPPort(ws.Port, ws.HTTPPort)
	if err != nil {
		return err
	}

	a.mu.Lock()
	siteRunning := a.siteProcs[websiteID] != nil
	current := a.httpProcs[websiteID]
	a.mu.Unlock()
	if !siteRunning {
		return errors.New("website xray is not running, start website first")
	}
	if current != nil && current.httpPort == httpPort && current.socksPort == ws.Port {
		return nil
	}

	a.stopWebsiteHTTP(websiteID)

	bridge, err := NewHTTPBridge(ws.Port, httpPort, func(format string, args ...any) {
		allArgs := append([]any{ws.Name}, args...)
		a.logf("info", "site-http:%s | "+format, allArgs...)
	})
	if err != nil {
		return err
	}
	if err := bridge.Start(); err != nil {
		return err
	}

	proc := &httpBridgeProcess{
		websiteID: websiteID,
		name:      ws.Name,
		socksPort: ws.Port,
		httpPort:  httpPort,
		startedAt: time.Now(),
		bridge:    bridge,
	}

	a.mu.Lock()
	a.httpProcs[websiteID] = proc
	a.mu.Unlock()

	a.logf("info", "site http started: %s socks=%d http=%d", ws.Name, ws.Port, httpPort)
	return nil
}

func (a *guiApp) pickLinkForWebsite(websiteID, selectedID, forcedID int64) (int64, string, error) {
	if forcedID > 0 {
		if id, link, ok := a.findLinkByID(forcedID); ok {
			return id, link, nil
		}
		return 0, "", errors.New("selected link not found")
	}
	if selectedID > 0 {
		if id, link, ok := a.findLinkByID(selectedID); ok {
			return id, link, nil
		}
	}
	var linkID int64
	var linkText string
	err := a.db.QueryRow(`
SELECT l.id, l.link
FROM speed_results s
JOIN links l ON l.id = s.link_id
WHERE s.website_id = ?
ORDER BY s.speed_k DESC, s.tested_at DESC
LIMIT 1`, websiteID).Scan(&linkID, &linkText)
	if err == nil {
		return linkID, linkText, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", err
	}
	err = a.db.QueryRow(`
SELECT l.id, l.link
FROM speed_results s
JOIN links l ON l.id = s.link_id
ORDER BY s.speed_k DESC, s.tested_at DESC
LIMIT 1`).Scan(&linkID, &linkText)
	if err == nil {
		return linkID, linkText, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", err
	}
	return 0, "", errors.New("no available link, run speed test first")
}

func (a *guiApp) findLinkByID(linkID int64) (int64, string, bool) {
	var id int64
	var link string
	err := a.db.QueryRow(`SELECT id, link FROM links WHERE id=?`, linkID).Scan(&id, &link)
	if err != nil {
		return 0, "", false
	}
	return id, link, true
}

func (a *guiApp) setWebsiteLinkStar(websiteID, linkID int64, starred bool) error {
	if _, err := a.getWebsite(websiteID); err != nil {
		return err
	}
	if _, _, ok := a.findLinkByID(linkID); !ok {
		return errors.New("link not found")
	}
	if starred {
		_, err := a.db.Exec(`
INSERT INTO website_link_stars(website_id, link_id, starred_at, updated_at)
VALUES(?, ?, datetime('now'), datetime('now'))
ON CONFLICT(website_id, link_id)
DO UPDATE SET updated_at=excluded.updated_at`, websiteID, linkID)
		if err != nil {
			return err
		}
		a.logf("info", "star set: website=%d link=%d", websiteID, linkID)
		return nil
	}
	_, err := a.db.Exec(`DELETE FROM website_link_stars WHERE website_id=? AND link_id=?`, websiteID, linkID)
	if err != nil {
		return err
	}
	a.logf("info", "star removed: website=%d link=%d", websiteID, linkID)
	return nil
}

func (a *guiApp) updateLinkCountry(linkID int64, country string) error {
	country = strings.TrimSpace(country)
	res, err := a.db.Exec(`UPDATE links SET country=? WHERE id=?`, country, linkID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("link not found")
	}
	a.logf("info", "link country updated: link=%d country=%q", linkID, country)
	return nil
}

func (a *guiApp) stopWebsite(websiteID int64) {
	a.stopWebsiteHTTP(websiteID)

	a.mu.Lock()
	proc := a.siteProcs[websiteID]
	a.mu.Unlock()
	if proc == nil {
		return
	}
	if proc.cmd != nil && proc.cmd.Process != nil {
		_ = proc.cmd.Process.Kill()
	}
	select {
	case <-proc.done:
	case <-time.After(2 * time.Second):
	}
	a.logf("warn", "site stopped: %s pid=%d", proc.name, proc.pid)
}

func (a *guiApp) stopWebsiteHTTP(websiteID int64) {
	a.mu.Lock()
	proc := a.httpProcs[websiteID]
	a.mu.Unlock()
	if proc == nil {
		return
	}
	if proc.bridge != nil {
		if err := proc.bridge.Stop(); err != nil {
			a.logf("warn", "site http stop failed: %s http=%d err=%v", proc.name, proc.httpPort, err)
		}
	}
	a.mu.Lock()
	if cur := a.httpProcs[websiteID]; cur != nil && cur == proc {
		delete(a.httpProcs, websiteID)
	}
	a.mu.Unlock()
	a.logf("warn", "site http stopped: %s http=%d", proc.name, proc.httpPort)
}

func (a *guiApp) startMainRouter() error {
	cfg, err := a.buildMainRouterConfig()
	if err != nil {
		return err
	}
	configPath := filepath.Join(a.configDir, "config_gui_10600.json")
	if err := writeJSONFile(configPath, cfg); err != nil {
		return err
	}

	a.stopMainRouter()

	cmd := exec.Command(a.xrayPath, "run", "-c", configPath)
	hideCommandWindow(cmd)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}

	proc := &managedProcess{
		websiteID: 0,
		name:      "main-router",
		port:      mainRoutePort,
		pid:       cmd.Process.Pid,
		config:    configPath,
		startedAt: time.Now(),
		cmd:       cmd,
		done:      make(chan struct{}),
	}

	a.mu.Lock()
	a.mainProc = proc
	a.mu.Unlock()

	go a.captureProcessLogs("main", stdout)
	go a.captureProcessLogs("main", stderr)
	go a.monitorProcess(proc, true)

	a.logf("info", "main router started: port=%d pid=%d", mainRoutePort, proc.pid)
	return nil
}
func (a *guiApp) buildMainRouterConfig() (map[string]any, error) {
	websites, err := a.listWebsites()
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	procs := make(map[int64]*managedProcess, len(a.siteProcs))
	for id, p := range a.siteProcs {
		procs[id] = p
	}
	a.mu.Unlock()

	defaultProxyPort := 0
	defaultWebsiteID := int64(0)
	for _, ws := range websites {
		if ws.ID == 1 && procs[ws.ID] != nil {
			defaultWebsiteID = ws.ID
			defaultProxyPort = ws.Port
			break
		}
	}
	for _, ws := range websites {
		if defaultProxyPort != 0 {
			break
		}
		if ws.Port == defaultPort {
			if procs[ws.ID] != nil {
				defaultWebsiteID = ws.ID
				defaultProxyPort = ws.Port
			}
			break
		}
	}
	if defaultProxyPort == 0 {
		// Fallback: choose the smallest ID running site as default.
		ids := make([]int64, 0, len(procs))
		for id := range procs {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			defaultWebsiteID = id
			defaultProxyPort = procs[id].port
			break
		}
	}
	if defaultProxyPort == 0 {
		return nil, errors.New("no running child xray, cannot start main router")
	}

	outbounds := make([]map[string]any, 0, len(procs)+1)
	rules := make([]map[string]any, 0, len(procs)+1)

	// Match specific websites first, from larger website ID to smaller.
	ordered := append([]website(nil), websites...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].ID > ordered[j].ID
	})
	for _, ws := range ordered {
		proc := procs[ws.ID]
		if proc == nil {
			continue
		}
		if ws.ID == defaultWebsiteID || ws.Port == defaultProxyPort {
			continue
		}
		tag := fmt.Sprintf("site-%d", ws.ID)
		outbounds = append(outbounds, localSocksOutbound(tag, ws.Port))
		domains := parseMatchRules(ws.MatchRule)
		if len(domains) == 0 {
			continue
		}
		rules = append(rules, map[string]any{
			"type":        "field",
			"domain":      domains,
			"outboundTag": tag,
		})
	}

	// Default outbound is always the last fallback.
	outbounds = append(outbounds, localSocksOutbound("default-proxy", defaultProxyPort))
	rules = append(rules, map[string]any{
		"type":        "field",
		"network":     "tcp,udp",
		"outboundTag": "default-proxy",
	})

	cfg := map[string]any{
		"log": map[string]any{
			"loglevel": "warning",
		},
		"inbounds": []map[string]any{
			{
				"tag":      "main-in",
				"listen":   "0.0.0.0",
				"port":     mainRoutePort,
				"protocol": "socks",
				"settings": map[string]any{
					"auth": "noauth",
					"udp":  true,
				},
				"sniffing": map[string]any{
					"enabled":      true,
					"destOverride": []string{"http", "tls", "quic"},
				},
			},
		},
		"outbounds": outbounds,
	}
	cfg["routing"] = map[string]any{
		"domainStrategy": "AsIs",
		"rules":          rules,
	}
	return cfg, nil
}

func localSocksOutbound(tag string, port int) map[string]any {
	return map[string]any{
		"tag":      tag,
		"protocol": "socks",
		"settings": map[string]any{
			"servers": []map[string]any{
				{
					"address": "127.0.0.1",
					"port":    port,
				},
			},
		},
	}
}

func parseMatchRules(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	raw = strings.ReplaceAll(raw, "\n", ",")
	raw = strings.ReplaceAll(raw, " ", ",")
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		entry := normalizeMatchRuleEntry(p)
		if entry == "" {
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		out = append(out, entry)
	}
	return out
}

func normalizeMatchRuleEntry(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" || p == "*" {
		return ""
	}
	if strings.Contains(p, "://") {
		if u, err := url.Parse(p); err == nil && u.Host != "" {
			p = u.Host
		}
	}
	if strings.Contains(p, "/") {
		p = strings.SplitN(p, "/", 2)[0]
	}
	if strings.Contains(p, ":") {
		host, _, err := net.SplitHostPort(p)
		if err == nil {
			p = host
		}
	}
	p = strings.TrimSpace(p)
	if p == "" || p == "*" {
		return ""
	}

	// Allow advanced match entries directly.
	lower := strings.ToLower(p)
	if strings.HasPrefix(lower, "domain:") ||
		strings.HasPrefix(lower, "regexp:") ||
		strings.HasPrefix(lower, "keyword:") ||
		strings.HasPrefix(lower, "full:") ||
		strings.HasPrefix(lower, "geosite:") {
		return p
	}

	// Wildcard support: e.g. *mage-b2.civitai.* -> regexp:^.*mage\-b2\.civitai\..*$
	if strings.Contains(p, "*") {
		// *.civitai.* -> allow both civitai.com and image.civitai.com
		if strings.HasPrefix(p, "*.") {
			rest := strings.TrimPrefix(p, "*.")
			restRe := regexp.QuoteMeta(rest)
			restRe = strings.ReplaceAll(restRe, `\*`, ".*")
			return "regexp:^(?:.*\\.)?" + restRe + "$"
		}
		// Simple leading *.<domain> is equivalent to domain suffix.
		if strings.HasPrefix(p, "*.") && strings.Count(p, "*") == 1 {
			sfx := strings.TrimPrefix(p, "*.")
			sfx = strings.TrimPrefix(sfx, ".")
			if sfx == "" {
				return ""
			}
			return "domain:" + sfx
		}
		re := regexp.QuoteMeta(p)
		re = strings.ReplaceAll(re, `\*`, ".*")
		return "regexp:^" + re + "$"
	}

	p = strings.TrimPrefix(p, "*.")
	p = strings.TrimPrefix(p, ".")
	if p == "" {
		return ""
	}
	return "domain:" + p
}

func (a *guiApp) stopMainRouter() {
	a.mu.Lock()
	proc := a.mainProc
	a.mu.Unlock()
	if proc == nil {
		return
	}
	if proc.cmd != nil && proc.cmd.Process != nil {
		_ = proc.cmd.Process.Kill()
	}
	select {
	case <-proc.done:
	case <-time.After(2 * time.Second):
	}
	a.logf("warn", "main router stopped pid=%d", proc.pid)
}

func (a *guiApp) stopAll() {
	a.stopMainRouter()
	a.mu.Lock()
	ids := make([]int64, 0, len(a.siteProcs))
	for id := range a.siteProcs {
		ids = append(ids, id)
	}
	httpIDs := make([]int64, 0, len(a.httpProcs))
	for id := range a.httpProcs {
		httpIDs = append(httpIDs, id)
	}
	a.mu.Unlock()
	for _, id := range httpIDs {
		a.stopWebsiteHTTP(id)
	}
	for _, id := range ids {
		a.stopWebsite(id)
	}
	a.logf("warn", "all managed processes requested to stop")
}

func (a *guiApp) killAllXrayProcesses() error {
	a.stopAll()

	target := strings.TrimSpace(filepath.Base(a.xrayPath))
	if target == "" || target == "." || target == string(os.PathSeparator) {
		if runtime.GOOS == "windows" {
			target = "xray.exe"
		} else {
			target = "xray"
		}
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		name := strings.TrimSuffix(target, filepath.Ext(target))
		script := fmt.Sprintf("Get-Process -Name '%s' -ErrorAction SilentlyContinue | Stop-Process -Force", strings.ReplaceAll(name, "'", "''"))
		cmd = exec.Command("powershell", "-NoProfile", "-Command", script)
	} else {
		cmd = exec.Command("pkill", "-f", target)
	}
	hideCommandWindow(cmd)

	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if runtime.GOOS != "windows" {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				a.logf("info", "kill all xray requested: no external xray process found target=%s", target)
				return nil
			}
		}
		if text != "" {
			return fmt.Errorf("kill all xray failed: %s", text)
		}
		return fmt.Errorf("kill all xray failed: %w", err)
	}

	if text == "" {
		a.logf("warn", "kill all xray requested: target=%s", target)
	} else {
		a.logf("warn", "kill all xray requested: target=%s output=%s", target, text)
	}
	return nil
}

func (a *guiApp) killPID(pid int) error {
	a.mu.Lock()
	if a.mainProc != nil && a.mainProc.pid == pid {
		a.mu.Unlock()
		a.stopMainRouter()
		return nil
	}
	for id, p := range a.siteProcs {
		if p.pid == pid {
			a.mu.Unlock()
			a.stopWebsite(id)
			return nil
		}
	}
	a.mu.Unlock()

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Kill(); err != nil {
		return err
	}
	a.logf("warn", "killed external pid=%d", pid)
	return nil
}

func (a *guiApp) monitorProcess(proc *managedProcess, isMain bool) {
	err := proc.cmd.Wait()
	close(proc.done)

	a.mu.Lock()
	if isMain {
		if a.mainProc != nil && a.mainProc.pid == proc.pid {
			a.mainProc = nil
		}
	} else {
		if cur := a.siteProcs[proc.websiteID]; cur != nil && cur.pid == proc.pid {
			delete(a.siteProcs, proc.websiteID)
		}
	}
	a.mu.Unlock()

	if err != nil {
		a.logf("warn", "process exited: name=%s pid=%d err=%v", proc.name, proc.pid, err)
	} else {
		a.logf("info", "process exited: name=%s pid=%d", proc.name, proc.pid)
	}
}

func (a *guiApp) captureProcessLogs(prefix string, rc io.ReadCloser) {
	if rc == nil {
		return
	}
	defer rc.Close()
	scanner := bufio.NewScanner(rc)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		a.logf("info", "%s | %s", prefix, line)
	}
}

func (a *guiApp) listProcesses() []processView {
	a.mu.Lock()
	defer a.mu.Unlock()
	items := make([]processView, 0, len(a.siteProcs)+len(a.httpProcs)+1)
	if a.mainProc != nil {
		items = append(items, processView{
			Kind:       "main",
			WebsiteID:  0,
			Website:    "main-router",
			Port:       a.mainProc.port,
			PID:        a.mainProc.pid,
			StartedAt:  a.mainProc.startedAt.Format(time.RFC3339),
			LinkID:     a.mainProc.linkID,
			Link:       a.mainProc.link,
			ConfigPath: a.mainProc.config,
		})
	}
	for _, p := range a.siteProcs {
		items = append(items, processView{
			Kind:       "child",
			WebsiteID:  p.websiteID,
			Website:    p.name,
			Port:       p.port,
			PID:        p.pid,
			StartedAt:  p.startedAt.Format(time.RFC3339),
			LinkID:     p.linkID,
			Link:       p.link,
			ConfigPath: p.config,
		})
	}
	for _, hp := range a.httpProcs {
		items = append(items, processView{
			Kind:      "http",
			WebsiteID: hp.websiteID,
			Website:   hp.name + " HTTP",
			Port:      hp.httpPort,
			PID:       0,
			StartedAt: hp.startedAt.Format(time.RFC3339),
			LinkID:    0,
			Link:      fmt.Sprintf("http://127.0.0.1:%d -> socks://127.0.0.1:%d", hp.httpPort, hp.socksPort),
		})
	}
	return items
}

func (a *guiApp) logf(level string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	entry := logEntry{
		ID:      a.logID.Add(1),
		Time:    time.Now().Format("2006-01-02 15:04:05"),
		Level:   level,
		Message: msg,
	}
	a.logMu.Lock()
	a.logs = append(a.logs, entry)
	if len(a.logs) > maxLogEntries {
		a.logs = a.logs[len(a.logs)-maxLogEntries:]
	}
	a.logMu.Unlock()
	fmt.Printf("[%s] %s\n", strings.ToUpper(level), msg)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg})
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func setXrayConfigLogLevel(path string, level string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	logObj, _ := cfg["log"].(map[string]any)
	if logObj == nil {
		logObj = map[string]any{}
	}
	logObj["loglevel"] = level
	cfg["log"] = logObj
	return writeJSONFile(path, cfg)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
