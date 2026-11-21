package main

// Memory: keep the app's RAM footprint at what it really uses (~2 MB).
//
// Between two scans (one per minute) the app does nothing, but Windows keeps
// every page it ever touched in its working set. Worse, Go reserves a 32 MiB
// buffer for its FIPS 140 crypto mode (never enabled here, see
// crypto/internal/fips140/drbg): it costs nothing until something reads it,
// and an antivirus inspecting the process at its first launch does, which
// pinned 32 MiB of zero pages in RAM for good (47 MB shown in Task Manager
// instead of ~2 MB).
//
// So after each scan the free Go heap goes back to Windows and the working
// set is emptied: the next scan gets the few pages it needs back from the
// standby list (soft faults, no disk access), the rest stays out of RAM.

import (
	"runtime/debug"

	"golang.org/x/sys/windows"
)

func trimMemory() {
	debug.FreeOSMemory()
	// (SIZE_T)-1 for both sizes = remove as many pages as possible.
	_ = windows.SetProcessWorkingSetSizeEx(windows.CurrentProcess(), ^uintptr(0), ^uintptr(0), 0)
}
