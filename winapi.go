package main

// Windows calls: visible windows, a process's path and start time, an exe's
// product name, single instance, wake-up.
//
// Everything is read-only, and processes are opened only with the minimal
// PROCESS_QUERY_LIMITED_INFORMATION right (the one Task Manager uses):
// nothing is injected into or read from game memory, so there is no risk
// with anti-cheat software.

import (
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                   = windows.NewLazySystemDLL("user32.dll")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	procGetWindow            = user32.NewProc("GetWindow")
	procGetWindowLongPtrW    = user32.NewProc("GetWindowLongPtrW")
	procAllowSetForeground   = user32.NewProc("AllowSetForegroundWindow")
	procFindWindowW          = user32.NewProc("FindWindowW")
	procPostMessageW         = user32.NewProc("PostMessageW")
	procShowWindow           = user32.NewProc("ShowWindow")
	procIsIconic             = user32.NewProc("IsIconic")
	procGetCursorPos         = user32.NewProc("GetCursorPos")
	procMonitorFromPoint     = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW      = user32.NewProc("GetMonitorInfoW")
	procGetWindowRect        = user32.NewProc("GetWindowRect")
	procSetWindowPos         = user32.NewProc("SetWindowPos")
)

const asfwAny = ^uint32(0) // ASFW_ANY: any process

const (
	gwOwner         = 4
	wsExToolWindow  = 0x00000080
	dwmwaCloaked    = 14
	processQueryLtd = windows.PROCESS_QUERY_LIMITED_INFORMATION
)

// Negative index, passed through a variable because a negative constant
// cannot be converted to uintptr at compile time.
var gwlExStyle int32 = -20

// winProc is a process with at least one visible top-level window.
type winProc struct {
	PID     uint32
	Path    string
	Titles  []string
	Started time.Time
}

func windowText(h windows.HWND) string {
	n, _, _ := procGetWindowTextLengthW.Call(uintptr(h))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf)
}

// The EnumWindows callback is created ONCE: only a limited number of
// callbacks can be created per process (creating one per scan would crash
// the app after a few days).
var (
	enumMu    sync.Mutex
	enumProcs map[uint32]*winProc
	enumCB    = syscall.NewCallback(enumWindowProc)
)

func enumWindowProc(h windows.HWND, _ uintptr) uintptr {
	if !windows.IsWindowVisible(h) {
		return 1
	}
	if owner, _, _ := procGetWindow.Call(uintptr(h), gwOwner); owner != 0 {
		return 1
	}
	if ex, _, _ := procGetWindowLongPtrW.Call(uintptr(h), uintptr(gwlExStyle)); ex&wsExToolWindow != 0 {
		return 1
	}
	var cloaked uint32
	if windows.DwmGetWindowAttribute(h, dwmwaCloaked, unsafe.Pointer(&cloaked), 4) == nil && cloaked != 0 {
		return 1
	}
	var pid uint32
	windows.GetWindowThreadProcessId(h, &pid)
	if pid == 0 {
		return 1
	}
	p := enumProcs[pid]
	if p == nil {
		p = &winProc{PID: pid}
		enumProcs[pid] = p
	}
	if title := strings.TrimSpace(windowText(h)); title != "" {
		p.Titles = append(p.Titles, title)
	}
	return 1
}

// listWindowedProcesses returns the processes that have a visible, uncloaked,
// unowned window that is not a tool window. Services and system processes
// have none, so they are never even looked at.
func listWindowedProcesses() map[uint32]*winProc {
	enumMu.Lock()
	enumProcs = map[uint32]*winProc{}
	windows.EnumWindows(enumCB, nil)
	procs := enumProcs
	enumProcs = nil
	enumMu.Unlock()

	for pid, p := range procs {
		h, err := windows.OpenProcess(processQueryLtd, false, pid)
		if err != nil {
			delete(procs, pid)
			continue
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		size := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) == nil {
			p.Path = windows.UTF16ToString(buf[:size])
		}
		var c, e, k, u windows.Filetime
		if windows.GetProcessTimes(h, &c, &e, &k, &u) == nil {
			p.Started = time.Unix(0, c.Nanoseconds())
		}
		windows.CloseHandle(h)
		if p.Path == "" {
			delete(procs, pid)
		}
	}
	return procs
}

// exeProductName returns the product name embedded in the .exe ("Dice
// Hoarder"), or failing that its file description. Empty if neither is set.
func exeProductName(path string) string {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return ""
	}
	buf := make([]byte, size)
	if windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])) != nil {
		return ""
	}
	// First declared language.
	var trans *[2]uint16
	var tlen uint32
	if windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&trans), &tlen) != nil || tlen < 4 {
		return ""
	}
	lang := strings.ToUpper(hex4(trans[0]) + hex4(trans[1]))
	for _, field := range []string{"ProductName", "FileDescription"} {
		var p *uint16
		var n uint32
		if windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\StringFileInfo\`+lang+`\`+field, unsafe.Pointer(&p), &n) == nil && n > 0 {
			if s := strings.TrimSpace(windows.UTF16PtrToString(p)); s != "" {
				return s
			}
		}
	}
	return ""
}

func hex4(v uint16) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>12&0xf], digits[v>>8&0xf], digits[v>>4&0xf], digits[v&0xf]})
}

// --- Single instance and wake-up --------------------------------------------

// Names shared between the app's processes. In --dev mode they get a suffix
// so a test build can run alongside the installed one.
var (
	mutexName = `Local\CoopRatingsAgent`
	wakeName  = `Local\CoopRatingsAgentWake`
)

func useDevNames() {
	mutexName += "-dev"
	wakeName += "-dev"
}

// acquireMutex returns false if another instance already holds this name.
// focusWindow brings the top-level window with this exact title to the
// front, restoring it if minimized; false if there is none.
func focusWindow(title string) bool {
	p, _ := windows.UTF16PtrFromString(title)
	h, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		return false
	}
	if minimized, _, _ := procIsIconic.Call(h); minimized != 0 {
		procShowWindow.Call(h, 9) // SW_RESTORE
	}
	procSetForegroundWindow.Call(h)
	return true
}

// closeWindow asks the top-level window with this exact title to close
// (WM_CLOSE); false if there is none.
func closeWindow(title string) bool {
	p, _ := windows.UTF16PtrFromString(title)
	h, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		return false
	}
	procPostMessageW.Call(h, 0x0010, 0, 0) // WM_CLOSE
	return true
}

// moveToCorner puts a window in the bottom-right corner of the work area
// (screen minus taskbar) of the monitor under the mouse, next to the
// notification area, like the Windows flyouts.
func moveToCorner(hwnd windows.HWND, margin int32) {
	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	mon, _, _ := procMonitorFromPoint.Call(uintptr(uint32(pt.X))|uintptr(uint32(pt.Y))<<32, 2) // MONITOR_DEFAULTTONEAREST
	mi := struct {
		Size          uint32
		Monitor, Work windows.Rect
		Flags         uint32
	}{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if ok, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return
	}
	// The window rect includes invisible resize borders: align the visible
	// frame instead.
	var win, vis windows.Rect
	procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&win)))
	if windows.DwmGetWindowAttribute(hwnd, 9, unsafe.Pointer(&vis), uint32(unsafe.Sizeof(vis))) != nil { // DWMWA_EXTENDED_FRAME_BOUNDS
		vis = win
	}
	x := win.Left + mi.Work.Right - margin - vis.Right
	y := win.Top + mi.Work.Bottom - margin - vis.Bottom
	procSetWindowPos.Call(uintptr(hwnd), 0, uintptr(x), uintptr(y), 0, 0, 0x0001|0x0004|0x0010) // SWP_NOSIZE|SWP_NOZORDER|SWP_NOACTIVATE
}

func acquireMutex(name string) (windows.Handle, bool) {
	p, _ := windows.UTF16PtrFromString(name)
	h, err := windows.CreateMutex(nil, false, p)
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(h)
		return 0, false
	}
	return h, err == nil
}

func mutexExists(name string) bool {
	h, ok := acquireMutex(name)
	if ok {
		windows.CloseHandle(h)
	}
	return !ok
}

func createWakeEvent() windows.Handle {
	p, _ := windows.UTF16PtrFromString(wakeName)
	h, _ := windows.CreateEvent(nil, 0, 0, p)
	return h
}

// wakeMain wakes the main process, which then processes inbox\.
func wakeMain() {
	p, _ := windows.UTF16PtrFromString(wakeName)
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, p)
	if err != nil {
		return
	}
	windows.SetEvent(h)
	windows.CloseHandle(h)
}

// hiddenProcAttr makes a child process start without a console window.
func hiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
