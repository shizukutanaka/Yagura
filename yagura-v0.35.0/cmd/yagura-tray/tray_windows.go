//go:build windows
// +build windows

// Windows system tray implementation using user32.dll + shell32.dll directly.
//
// Why no external deps:
//   getlantern/systray works but pulls cgo + multi-deps. yagura's ADR-0001
//   forbids external Go modules. Windows tray is achievable with raw
//   syscalls; the message loop is ~150 lines.
//
// Architecture:
//   1. RegisterClassExW + CreateWindowExW → invisible message-only window
//   2. Shell_NotifyIconW (NIM_ADD) → tray icon
//   3. WndProc receives WM_USER+1 messages on tray events
//   4. Right-click → CreatePopupMenu + TrackPopupMenu → command dispatch
//   5. Left-click (single) → open dashboard
//   6. WM_COMMAND from menu → handlers (open / restart / quit)
//
// Limitations (v0.32 — kept intentionally simple):
//   - Icon is generic system icon (IDI_APPLICATION) — no custom .ico
//   - English-only menu labels (yagura is i18n-aware in JSON, not UI)
//   - Single instance not enforced via mutex (port conflict instead)
package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	// Window message constants
	wmDestroy     = 0x0002
	wmCommand     = 0x0111
	wmTrayMessage = 0x0400 + 1 // WM_USER + 1
	wmRButtonUp   = 0x0205
	wmLButtonUp   = 0x0202
	wmLButtonDBL  = 0x0203

	// Shell_NotifyIcon flags
	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04

	nimAdd    = 0
	nimDelete = 2

	// Menu flags
	mfString    = 0x0000
	mfSeparator = 0x0800

	// TrackPopupMenu flags
	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020

	// Predefined icon
	idiApplication = 32512
)

// Command IDs for menu items
const (
	cmdOpenDashboard = 1001
	cmdOpenMetrics   = 1002
	cmdRestart       = 1003
	cmdQuit          = 1099
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             syscall.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            syscall.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UTimeoutVersion  uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       syscall.Handle
}

type point struct {
	X, Y int32
}

type msg struct {
	HWnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW   = user32.NewProc("RegisterClassExW")
	procCreateWindowExW    = user32.NewProc("CreateWindowExW")
	procDefWindowProcW     = user32.NewProc("DefWindowProcW")
	procDestroyWindow      = user32.NewProc("DestroyWindow")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessageW   = user32.NewProc("DispatchMessageW")
	procPostQuitMessage    = user32.NewProc("PostQuitMessage")
	procLoadIconW          = user32.NewProc("LoadIconW")
	procGetCursorPos       = user32.NewProc("GetCursorPos")
	procCreatePopupMenu    = user32.NewProc("CreatePopupMenu")
	procAppendMenuW        = user32.NewProc("AppendMenuW")
	procDestroyMenu        = user32.NewProc("DestroyMenu")
	procTrackPopupMenu     = user32.NewProc("TrackPopupMenu")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")

	// State accessible from WndProc callback
	currentDaemon *daemon
	currentAddr   string
	hwnd          syscall.Handle
	nid           notifyIconData
)

// platformSupportsTray returns true on Windows.
func platformSupportsTray() bool { return true }

// runTray runs the Windows tray message loop.
//
// Blocks until the user selects "Quit" from the menu or destroys the
// invisible window. Returns when the message loop ends; caller's deferred
// daemon.Stop() then runs.
func runTray(d *daemon, addr string) {
	currentDaemon = d
	currentAddr = addr

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("yagura_tray_class")
	windowName, _ := syscall.UTF16PtrFromString("yagura")

	// Register window class
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     syscall.Handle(hInstance),
		LpszClassName: className,
	}
	atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		fmt.Printf("WARN: RegisterClassExW failed: %v — falling back to no-tray\n", err)
		runNoTrayBlock()
		return
	}

	// Create message-only window (HWND_MESSAGE = -3)
	const hwndMessage = ^uintptr(2) // -3 in two's complement
	h, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		0, 0, 0, 0, 0,
		hwndMessage,
		0,
		hInstance,
		0,
	)
	if h == 0 {
		fmt.Printf("WARN: CreateWindowExW failed: %v — falling back to no-tray\n", err)
		runNoTrayBlock()
		return
	}
	hwnd = syscall.Handle(h)

	// Load default app icon
	hIcon, _, _ := procLoadIconW.Call(0, idiApplication)

	// Add tray icon
	tip := fmt.Sprintf("yagura — http://%s", addr)
	nid = notifyIconData{
		CbSize:           uint32(unsafe.Sizeof(nid)),
		HWnd:             hwnd,
		UID:              1,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmTrayMessage,
		HIcon:            syscall.Handle(hIcon),
	}
	tipUTF16, _ := syscall.UTF16FromString(tip)
	copy(nid.SzTip[:], tipUTF16)

	r, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	if r == 0 {
		fmt.Println("WARN: Shell_NotifyIcon failed — falling back to no-tray")
		runNoTrayBlock()
		return
	}
	defer procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))

	fmt.Println("yagura-tray running. Right-click tray icon for menu.")

	// Message pump
	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break // WM_QUIT or error
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// wndProc is the window procedure callback invoked by Windows for each message.
//
// Returns 0 for handled messages, DefWindowProc result otherwise.
func wndProc(h syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmTrayMessage:
		// LParam low word = mouse event
		event := uint32(lParam) & 0xFFFF
		switch event {
		case wmLButtonUp, wmLButtonDBL:
			openBrowser("http://" + currentAddr + "/dashboard")
		case wmRButtonUp:
			showContextMenu()
		}
		return 0
	case wmCommand:
		cmd := uint16(wParam)
		handleMenuCommand(cmd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(h), uintptr(msg), wParam, lParam)
	return r
}

// showContextMenu displays the right-click popup.
func showContextMenu() {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	addItem := func(id uintptr, label string) {
		text, _ := syscall.UTF16PtrFromString(label)
		procAppendMenuW.Call(hMenu, mfString, id, uintptr(unsafe.Pointer(text)))
	}
	addSep := func() {
		procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
	}

	addItem(cmdOpenDashboard, "Open Dashboard")
	addItem(cmdOpenMetrics, "Open /metrics")
	addSep()
	addItem(cmdRestart, "Restart daemon")
	addSep()
	addItem(cmdQuit, "Quit yagura")

	// Required: foreground before TrackPopupMenu for proper dismissal
	procSetForegroundWindow.Call(uintptr(hwnd))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	procTrackPopupMenu.Call(
		hMenu,
		tpmRightButton|tpmBottomAlign,
		uintptr(pt.X),
		uintptr(pt.Y),
		0,
		uintptr(hwnd),
		0,
	)
}

// handleMenuCommand dispatches WM_COMMAND messages to feature handlers.
func handleMenuCommand(cmd uint16) {
	switch cmd {
	case cmdOpenDashboard:
		openBrowser("http://" + currentAddr + "/dashboard")
	case cmdOpenMetrics:
		openBrowser("http://" + currentAddr + "/metrics")
	case cmdRestart:
		fmt.Println("Restarting daemon...")
		if currentDaemon != nil {
			currentDaemon.Stop()
			if err := currentDaemon.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: restart failed: %v\n", err)
			}
		}
	case cmdQuit:
		procDestroyWindow.Call(uintptr(hwnd))
	}
}

// runNoTrayBlock is the fallback when tray init fails: block on stdin.
func runNoTrayBlock() {
	fmt.Println("(tray unavailable — press Enter to quit)")
	var buf [1]byte
	for {
		_, err := os.Stdin.Read(buf[:])
		if err != nil {
			return
		}
		if buf[0] == '\n' {
			return
		}
	}
}
