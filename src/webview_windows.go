//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview "github.com/webview/webview_go"
)

func (a *guiApp) runWebviewDesktop() error {
	srv, ln, uiURL, err := a.prepareWebServer(50000)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	a.logf("info", "desktop ui started on %s", uiURL)
	width, height := getPrimaryScreenSize()
	splash, splashErr := createStartupSplash(width, height)
	if splashErr != nil {
		a.logf("warn", "create startup splash failed: %v", splashErr)
	}
	if splash != nil {
		defer splash.Close()
	}

	host, hostErr := createMainHostWindow(width, height)
	if hostErr != nil {
		a.logf("warn", "create host window failed, fallback to default webview window: %v", hostErr)
	}
	if host != nil {
		defer host.Close()
	}

	var w webview.WebView
	if host != nil {
		w = webview.NewWindow(false, unsafe.Pointer(host.hwnd))
	} else {
		w = webview.New(false)
	}
	if w == nil {
		_ = ln.Close()
		return errors.New("create webview failed")
	}
	defer w.Destroy()
	defer a.setWindowController(nil)

	hwnd := uintptr(w.Window())
	if hwnd != 0 {
		hideWindow(hwnd)
	}

	w.SetTitle("")
	w.SetSize(width, height, webview.HintFixed)
	if err := setBorderlessMaximized(w); err != nil {
		a.logf("warn", "set borderless maximized failed: %v", err)
	}
	var revealOnce sync.Once
	revealWindow := func() {
		revealOnce.Do(func() {
			w.Dispatch(func() {
				_ = setBorderlessMaximized(w)
				showWindow(uintptr(w.Window()))
				resizeHostedWidget(uintptr(w.Window()))
				if splash != nil {
					splash.Close()
				}
			})
		})
	}
	revealTimer := time.AfterFunc(10*time.Second, revealWindow)
	defer revealTimer.Stop()
	if bindErr := w.Bind("dxlSplashReady", func() {
		revealWindow()
	}); bindErr != nil {
		a.logf("warn", "bind splash ready callback failed: %v", bindErr)
	}
	w.SetHtml(startupLoadingHTML(uiURL))
	a.setWindowController(func(action string) error {
		done := make(chan error, 1)
		w.Dispatch(func() {
			done <- executeWindowAction(w, action)
		})
		select {
		case ctrlErr := <-done:
			return ctrlErr
		case <-time.After(3 * time.Second):
			return errors.New("window control timeout")
		}
	})
	w.Dispatch(func() { _ = setBorderlessMaximized(w) })
	w.Run()

	// Closing desktop window exits the app and requests managed processes to stop.
	a.stopAll()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)

	select {
	case serveErr := <-errCh:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
	default:
	}
	return nil
}

func startupLoadingHTML(uiURL string) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width,initial-scale=1" />
  <title>Loading</title>
  <style>
    :root { color-scheme: dark; }
    * { box-sizing: border-box; }
    html, body {
      margin: 0;
      width: 100%%;
      height: 100%%;
      overflow: hidden;
      font-family: "Segoe UI", "PingFang SC", "Noto Sans SC", sans-serif;
      background: radial-gradient(160%% 120%% at 10%% 10%%, #1f2937 0%%, #020617 48%%, #000 100%%);
      color: #e2e8f0;
    }
    body::before {
      content: "";
      position: fixed;
      inset: 0;
      background:
        radial-gradient(circle at 22%% 26%%, rgba(59,130,246,.42), transparent 52%%),
        radial-gradient(circle at 78%% 72%%, rgba(16,185,129,.32), transparent 56%%);
      filter: blur(36px);
      transform: scale(1.06);
    }
    .stage {
      position: relative;
      z-index: 1;
      width: 100%%;
      height: 100%%;
      display: grid;
      place-items: center;
      padding: 22px;
    }
    .panel {
      min-width: 300px;
      border-radius: 26px;
      border: 1px solid rgba(148,163,184,.35);
      background: linear-gradient(135deg, rgba(15,23,42,.72), rgba(2,6,23,.88));
      box-shadow: 0 18px 55px rgba(2,6,23,.58);
      backdrop-filter: blur(14px);
      padding: 26px 26px 20px;
      text-align: center;
    }
    .spinner {
      width: 56px;
      height: 56px;
      margin: 0 auto 14px;
      border-radius: 999px;
      border: 4px solid rgba(148,163,184,.28);
      border-top-color: #60a5fa;
      border-right-color: #34d399;
      animation: spin .95s linear infinite;
      box-shadow: 0 0 24px rgba(56,189,248,.35);
    }
    .title {
      margin: 0;
      font-size: 18px;
      letter-spacing: .2px;
    }
    .desc {
      margin: 10px 0 0;
      font-size: 13px;
      color: #94a3b8;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
  </style>
</head>
<body>
  <div class="stage">
    <div class="panel">
      <div class="spinner"></div>
      <h1 class="title">Starting</h1>
      <p id="desc" class="desc">Initializing, please wait...</p>
    </div>
  </div>
  <script>
    (function () {
      const loadingStart = Date.now();
      const loadingMinMs = 1200;
      const notifySplashReady = () => {
        try {
          if (typeof window.dxlSplashReady === "function") {
            window.dxlSplashReady();
          }
        } catch (_) {
        }
      };
      if (document.readyState === "complete") {
        requestAnimationFrame(() => requestAnimationFrame(notifySplashReady));
      } else {
        window.addEventListener("load", () => {
          requestAnimationFrame(() => requestAnimationFrame(notifySplashReady));
        }, { once: true });
      }
      const target = %q;
      const desc = document.getElementById("desc");
      const healthURL = target + "/api/health";
      let retry = 0;
      const poll = async () => {
        try {
          const res = await fetch(healthURL, { cache: "no-store" });
          if (res.ok) {
            desc.textContent = "Opening main interface...";
            const elapsed = Date.now() - loadingStart;
            const waitMs = Math.max(420, loadingMinMs - elapsed);
            setTimeout(() => {
              location.replace(target);
            }, waitMs);
            return;
          }
        } catch (_) {
        }
        retry += 1;
        if (retry %% 8 === 0) {
          desc.textContent = "Startup is slower than usual, still loading...";
        }
        setTimeout(poll, 180);
      };
      poll();
    })();
  </script>
</body>
</html>`, uiURL)
}

var (
	kernel32DLL           = syscall.NewLazyDLL("kernel32.dll")
	user32DLL             = syscall.NewLazyDLL("user32.dll")
	gdi32DLL              = syscall.NewLazyDLL("gdi32.dll")
	procGetModuleHandleW  = kernel32DLL.NewProc("GetModuleHandleW")
	procGetWindowLongW    = user32DLL.NewProc("GetWindowLongW")
	procSetWindowLongW    = user32DLL.NewProc("SetWindowLongW")
	procSetWindowPos      = user32DLL.NewProc("SetWindowPos")
	procMonitorFromWindow = user32DLL.NewProc("MonitorFromWindow")
	procGetMonitorInfoW   = user32DLL.NewProc("GetMonitorInfoW")
	procRegisterClassExW  = user32DLL.NewProc("RegisterClassExW")
	procCreateWindowExW   = user32DLL.NewProc("CreateWindowExW")
	procDestroyWindow     = user32DLL.NewProc("DestroyWindow")
	procLoadImageW        = user32DLL.NewProc("LoadImageW")
	procSendMessageW      = user32DLL.NewProc("SendMessageW")
	procDefWindowProcW    = user32DLL.NewProc("DefWindowProcW")
	procFindWindowExW     = user32DLL.NewProc("FindWindowExW")
	procGetClientRect     = user32DLL.NewProc("GetClientRect")
	procMoveWindow        = user32DLL.NewProc("MoveWindow")
	procPostMessageW      = user32DLL.NewProc("PostMessageW")
	procPostQuitMessage   = user32DLL.NewProc("PostQuitMessage")
	procUnregisterClassW  = user32DLL.NewProc("UnregisterClassW")
	procShowWindow        = user32DLL.NewProc("ShowWindow")
	procUpdateWindow      = user32DLL.NewProc("UpdateWindow")
	procGetSystemMetrics  = user32DLL.NewProc("GetSystemMetrics")
	procCreateSolidBrush  = gdi32DLL.NewProc("CreateSolidBrush")
	procDeleteObject      = gdi32DLL.NewProc("DeleteObject")
	splashWndProcPtr      = syscall.NewCallback(splashWndProc)
	mainHostWndProcPtr    = syscall.NewCallback(mainHostWndProc)
)

const (
	gwlStyle   = -16
	gwlExStyle = -20

	wsCaption      = 0x00C00000
	wsThickFrame   = 0x00040000
	wsMinimize     = 0x20000000
	wsMaximizeBox  = 0x00010000
	wsSysMenu      = 0x00080000
	wsPopup        = 0x80000000
	wsClipChildren = 0x02000000
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsExDlgModal   = 0x00000001
	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080
	wsExClientEdge = 0x00000200
	wsExStaticEdge = 0x00020000

	csVRedraw = 0x0001
	csHRedraw = 0x0002

	swpNoOwnerZOrder        = 0x0200
	swpFrameChanged         = 0x0020
	monitorDefaultToNearest = 2
	wmDestroy               = 0x0002
	wmSize                  = 0x0005
	wmClose                 = 0x0010
	wmSetIcon               = 0x0080
	iconSmall               = 0
	iconBig                 = 1
	swHide                  = 0
	swShow                  = 5
	swMinimize              = 6
	smCxScreen              = 0
	smCyScreen              = 1
	smCxIcon                = 11
	smCyIcon                = 12
	smCxSmIcon              = 49
	smCySmIcon              = 50
	imageIcon               = 1
	lrDefaultSize           = 0x0040
	lrShared                = 0x8000
	idiApplication          = 32512
)

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type monitorInfo struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	DwFlags   uint32
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type startupSplash struct {
	once      sync.Once
	hwnd      uintptr
	hInstance uintptr
	className string
	bgBrush   uintptr
}

type mainHostWindow struct {
	once      sync.Once
	hwnd      uintptr
	hInstance uintptr
	className string
	bgBrush   uintptr
}

func createStartupSplash(width, height int) (*startupSplash, error) {
	hInstance, _, callErr := procGetModuleHandleW.Call(0)
	if hInstance == 0 {
		return nil, fmt.Errorf("get module handle failed: %v", callErr)
	}

	className := fmt.Sprintf("DXLStartupSplash_%d", os.Getpid())
	classNamePtr, _ := syscall.UTF16PtrFromString(className)
	brush, _, callErr := procCreateSolidBrush.Call(uintptr(rgbColor(3, 9, 20)))
	if brush == 0 {
		return nil, fmt.Errorf("create splash brush failed: %v", callErr)
	}

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		Style:         csHRedraw | csVRedraw,
		LpfnWndProc:   splashWndProcPtr,
		HInstance:     hInstance,
		HbrBackground: brush,
		LpszClassName: classNamePtr,
	}
	atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		procDeleteObject.Call(brush)
		return nil, fmt.Errorf("register splash class failed: %v", callErr)
	}

	titlePtr, _ := syscall.UTF16PtrFromString("Daxionglink Starting")
	hwnd, _, callErr := procCreateWindowExW.Call(
		wsExTopmost|wsExToolWindow,
		uintptr(unsafe.Pointer(classNamePtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		wsPopup|wsVisible,
		uintptr(int32(0)),
		uintptr(int32(0)),
		uintptr(int32(width)),
		uintptr(int32(height)),
		0,
		0,
		hInstance,
		0,
	)
	if hwnd == 0 {
		procUnregisterClassW.Call(uintptr(unsafe.Pointer(classNamePtr)), hInstance)
		procDeleteObject.Call(brush)
		return nil, fmt.Errorf("create splash window failed: %v", callErr)
	}

	showWindow(hwnd)
	return &startupSplash{
		hwnd:      hwnd,
		hInstance: hInstance,
		className: className,
		bgBrush:   brush,
	}, nil
}

func createMainHostWindow(width, height int) (*mainHostWindow, error) {
	hInstance, _, callErr := procGetModuleHandleW.Call(0)
	if hInstance == 0 {
		return nil, fmt.Errorf("get module handle failed: %v", callErr)
	}

	className := fmt.Sprintf("DXLMainHost_%d", os.Getpid())
	classNamePtr, _ := syscall.UTF16PtrFromString(className)
	brush, _, callErr := procCreateSolidBrush.Call(uintptr(rgbColor(3, 9, 20)))
	if brush == 0 {
		return nil, fmt.Errorf("create host brush failed: %v", callErr)
	}

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		Style:         csHRedraw | csVRedraw,
		LpfnWndProc:   mainHostWndProcPtr,
		HInstance:     hInstance,
		HIcon:         loadWindowIcon(hInstance, false),
		HIconSm:       loadWindowIcon(hInstance, true),
		HbrBackground: brush,
		LpszClassName: classNamePtr,
	}
	atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		procDeleteObject.Call(brush)
		return nil, fmt.Errorf("register host class failed: %v", callErr)
	}

	titlePtr, _ := syscall.UTF16PtrFromString("Daxionglink")
	hwnd, _, callErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(classNamePtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		wsPopup|wsClipChildren,
		uintptr(int32(0)),
		uintptr(int32(0)),
		uintptr(int32(width)),
		uintptr(int32(height)),
		0,
		0,
		hInstance,
		0,
	)
	if hwnd == 0 {
		procUnregisterClassW.Call(uintptr(unsafe.Pointer(classNamePtr)), hInstance)
		procDeleteObject.Call(brush)
		return nil, fmt.Errorf("create host window failed: %v", callErr)
	}
	applyWindowIcons(hwnd, hInstance)

	return &mainHostWindow{
		hwnd:      hwnd,
		hInstance: hInstance,
		className: className,
		bgBrush:   brush,
	}, nil
}

func (h *mainHostWindow) Close() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		if h.hwnd != 0 {
			procPostMessageW.Call(h.hwnd, wmClose, 0, 0)
			h.hwnd = 0
		}
		h.className = ""
		h.hInstance = 0
		h.bgBrush = 0
	})
}

func (s *startupSplash) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.hwnd != 0 {
			procPostMessageW.Call(s.hwnd, wmClose, 0, 0)
			s.hwnd = 0
		}
		s.className = ""
		s.hInstance = 0
		s.bgBrush = 0
	})
}

func splashWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	if msg == wmClose {
		procDestroyWindow.Call(hwnd)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func mainHostWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmSize:
		resizeHostedWidget(hwnd)
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func resizeHostedWidget(hostHwnd uintptr) {
	if hostHwnd == 0 {
		return
	}
	var rc winRect
	ret, _, _ := procGetClientRect.Call(hostHwnd, uintptr(unsafe.Pointer(&rc)))
	if ret == 0 {
		return
	}
	child, _, _ := procFindWindowExW.Call(hostHwnd, 0, 0, 0)
	if child == 0 {
		return
	}
	width := int32(rc.Right - rc.Left)
	height := int32(rc.Bottom - rc.Top)
	if width < 1 || height < 1 {
		return
	}
	procMoveWindow.Call(
		child,
		0,
		0,
		uintptr(width),
		uintptr(height),
		1,
	)
}

func rgbColor(r, g, b byte) uint32 {
	return uint32(r) | (uint32(g) << 8) | (uint32(b) << 16)
}

func setBorderlessMaximized(w webview.WebView) error {
	hwnd := uintptr(w.Window())
	if hwnd == 0 {
		return errors.New("invalid window handle")
	}

	style, err := getWindowLong(hwnd, gwlStyle)
	if err != nil {
		return err
	}
	style &^= wsCaption | wsThickFrame | wsMinimize | wsMaximizeBox | wsSysMenu
	style |= wsPopup | wsVisible
	if err := setWindowLong(hwnd, gwlStyle, style); err != nil {
		return err
	}

	exStyle, err := getWindowLong(hwnd, gwlExStyle)
	if err == nil {
		exStyle &^= wsExDlgModal | wsExClientEdge | wsExStaticEdge
		_ = setWindowLong(hwnd, gwlExStyle, exStyle)
	}

	monitor, _, callErr := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if monitor == 0 {
		return fmt.Errorf("monitor lookup failed: %v", callErr)
	}

	mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	ret, _, callErr := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&mi)))
	if ret == 0 {
		return fmt.Errorf("get monitor info failed: %v", callErr)
	}

	width := mi.RcWork.Right - mi.RcWork.Left
	height := mi.RcWork.Bottom - mi.RcWork.Top
	ret, _, callErr = procSetWindowPos.Call(
		hwnd,
		0,
		uintptr(int32(mi.RcWork.Left)),
		uintptr(int32(mi.RcWork.Top)),
		uintptr(int32(width)),
		uintptr(int32(height)),
		swpNoOwnerZOrder|swpFrameChanged,
	)
	if ret == 0 {
		return fmt.Errorf("set window position failed: %v", callErr)
	}
	return nil
}

func executeWindowAction(w webview.WebView, action string) error {
	hwnd := uintptr(w.Window())
	if hwnd == 0 {
		return errors.New("window not ready")
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "minimize":
		if err := minimizeWindow(hwnd); err != nil {
			return err
		}
		return nil
	case "close":
		w.Terminate()
		return nil
	default:
		return fmt.Errorf("unsupported window action: %s", action)
	}
}

func minimizeWindow(hwnd uintptr) error {
	procShowWindow.Call(hwnd, swMinimize)
	return nil
}

func hideWindow(hwnd uintptr) {
	procShowWindow.Call(hwnd, swHide)
}

func showWindow(hwnd uintptr) {
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
}

func loadWindowIcon(hInstance uintptr, small bool) uintptr {
	width := getSystemMetric(smCxIcon)
	height := getSystemMetric(smCyIcon)
	if small {
		width = getSystemMetric(smCxSmIcon)
		height = getSystemMetric(smCySmIcon)
	}
	if width <= 0 {
		width = 16
	}
	if height <= 0 {
		height = 16
	}
	if icon := loadIconByName(hInstance, "IDI_APP_ICON", width, height); icon != 0 {
		return icon
	}
	if icon := loadIconByID(hInstance, 1, width, height); icon != 0 {
		return icon
	}
	return loadIconByID(0, idiApplication, width, height)
}

func loadIconByName(hInstance uintptr, name string, width, height int) uintptr {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	ret, _, _ := procLoadImageW.Call(
		hInstance,
		uintptr(unsafe.Pointer(namePtr)),
		imageIcon,
		uintptr(width),
		uintptr(height),
		lrShared|lrDefaultSize,
	)
	return ret
}

func loadIconByID(hInstance uintptr, id uint16, width, height int) uintptr {
	ret, _, _ := procLoadImageW.Call(
		hInstance,
		uintptr(id),
		imageIcon,
		uintptr(width),
		uintptr(height),
		lrShared|lrDefaultSize,
	)
	return ret
}

func applyWindowIcons(hwnd, hInstance uintptr) {
	if hwnd == 0 {
		return
	}
	big := loadWindowIcon(hInstance, false)
	if big != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconBig, big)
	}
	small := loadWindowIcon(hInstance, true)
	if small != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, small)
	}
}

func getPrimaryScreenSize() (int, int) {
	width := getSystemMetric(smCxScreen)
	height := getSystemMetric(smCyScreen)
	if width < 640 {
		width = 1280
	}
	if height < 480 {
		height = 720
	}
	return width, height
}

func getSystemMetric(metric int) int {
	v, _, _ := procGetSystemMetrics.Call(uintptr(metric))
	return int(v)
}

func getWindowLong(hwnd uintptr, index int32) (uint32, error) {
	ret, _, callErr := procGetWindowLongW.Call(hwnd, uintptr(index))
	if ret == 0 && callErr != syscall.Errno(0) {
		return 0, callErr
	}
	return uint32(ret), nil
}

func setWindowLong(hwnd uintptr, index int32, value uint32) error {
	ret, _, callErr := procSetWindowLongW.Call(hwnd, uintptr(index), uintptr(value))
	if ret == 0 && callErr != syscall.Errno(0) {
		return callErr
	}
	return nil
}
