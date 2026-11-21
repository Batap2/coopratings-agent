package main

// Per-process usage of the GPU's 3D engine, from the Windows performance
// counters ("GPU Engine", the ones Task Manager shows). A game keeps the 3D
// engine busy; a video, a browser or Discord barely use it (video playback
// goes through the decoder engine).
//
// Sampled on every scan: the value is the average since the previous scan.
// Nothing is read from the game itself.

import (
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pdh                              = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQueryW                = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW        = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData          = pdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterArrayW = pdh.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery                = pdh.NewProc("PdhCloseQuery")
)

const (
	pdhFmtDouble   = 0x00000200
	pdhFmtNoCap100 = 0x00008000
	pdhMoreData    = 0x800007D2
)

// pdhItem mirrors PDH_FMT_COUNTERVALUE_ITEM_W (64-bit Windows).
type pdhItem struct {
	Name   *uint16
	Status uint32
	_      uint32
	Value  float64
}

type gpuSampler struct {
	query   uintptr
	counter uintptr
	ok      bool
}

func newGPUSampler() *gpuSampler {
	g := &gpuSampler{}
	if pdh.Load() != nil {
		return g
	}
	if r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&g.query))); r != 0 {
		return g
	}
	path, _ := windows.UTF16PtrFromString(`\GPU Engine(*engtype_3D)\Utilization Percentage`)
	if r, _, _ := procPdhAddEnglishCounterW.Call(g.query, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&g.counter))); r != 0 {
		procPdhCloseQuery.Call(g.query)
		return g
	}
	procPdhCollectQueryData.Call(g.query) // first sample: baseline
	g.ok = true
	return g
}

// sample returns the % of the 3D engine used by each PID since the previous call.
func (g *gpuSampler) sample() map[uint32]float64 {
	out := map[uint32]float64{}
	if !g.ok {
		return out
	}
	if r, _, _ := procPdhCollectQueryData.Call(g.query); r != 0 {
		return out
	}
	var size, count uint32
	r, _, _ := procPdhGetFormattedCounterArrayW.Call(g.counter, pdhFmtDouble|pdhFmtNoCap100,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(r) != pdhMoreData || size == 0 {
		return out
	}
	buf := make([]byte, size)
	r, _, _ = procPdhGetFormattedCounterArrayW.Call(g.counter, pdhFmtDouble|pdhFmtNoCap100,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0])))
	if r != 0 || count == 0 {
		return out
	}
	items := unsafe.Slice((*pdhItem)(unsafe.Pointer(&buf[0])), count)
	for _, it := range items {
		if it.Status > 1 { // 0 = valid data, 1 = new data
			continue
		}
		if pid := pidFromInstance(windows.UTF16PtrToString(it.Name)); pid > 0 {
			out[pid] += it.Value
		}
	}
	return out
}

// pidFromInstance: "pid_1234_luid_0x..._phys_0_eng_0_engtype_3D" -> 1234
func pidFromInstance(name string) uint32 {
	if !strings.HasPrefix(name, "pid_") {
		return 0
	}
	rest := name[4:]
	if i := strings.IndexByte(rest, '_'); i > 0 {
		rest = rest[:i]
	}
	n, err := strconv.ParseUint(rest, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

func (g *gpuSampler) close() {
	if g.ok {
		procPdhCloseQuery.Call(g.query)
		g.ok = false
	}
}
