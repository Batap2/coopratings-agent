package main

// Installation without administrator rights:
//   - the exe is copied to %LOCALAPPDATA%\CoopRatingsAgent (the downloaded
//     file can be deleted afterwards);
//   - start with Windows (HKCU\...\Run);
//   - notifications and the coopratings-agent: protocol (see notify.go).

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "CoopRatingsAgent"

func installedExe() string { return appFile("CoopRatings-Agent.exe") }

func currentExe() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

func isInstalledCopy() bool {
	return strings.EqualFold(filepath.Clean(currentExe()), filepath.Clean(installedExe()))
}

// installSelf copies the current exe to its install location.
func installSelf() error {
	src, err := os.Open(currentExe())
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := installedExe() + ".new"
	dst, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	dst.Close()
	// The old version may take a moment to exit.
	for i := 0; i < 20; i++ {
		if err = os.Rename(tmp, installedExe()); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return err
}

// relaunch starts the installed exe with the given arguments.
// Do NOT use a hidden window here (hiddenProcAttr): Windows would apply it to
// the app's first window, i.e. the login window, which would stay invisible.
// The exe has no console anyway.
func relaunch(args []string) error {
	// Allow the relaunched copy to take the foreground (otherwise Windows
	// leaves its login window behind the others).
	procAllowSetForeground.Call(uintptr(asfwAny))
	return exec.Command(installedExe(), args...).Start()
}

// showAppWindow brings the app window to the front, or opens it in a second
// instance of the exe which, finding the app already running, shows the
// control window (see main). With "--activity" it opens on the recent
// activity.
func showAppWindow(dev bool, args ...string) {
	if focusWindow(appWindowTitle) {
		return
	}
	if dev {
		args = append(args, "--dev")
	}
	procAllowSetForeground.Call(uintptr(asfwAny))
	if err := exec.Command(currentExe(), args...).Start(); err != nil {
		log.Printf("fenêtre de l'appli : %v", err)
	}
}

func setAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	return k.SetStringValue(runValue, fmt.Sprintf(`"%s" --autostart`, installedExe()))
}

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

// uninstall removes autostart, notifications and the protocol handler, then
// deletes the app folder once the process has exited.
func uninstall() {
	// Revoke the tokens on the server now: the app won't run again to do it.
	cfg := loadConfig()
	revokeToken(cfg.Server, cfg.Token)
	for _, r := range loadState().Revoke {
		revokeToken(cfg.Server, r.Token)
	}
	setAutostart(false)
	unregisterShellIntegration()
	cmd := exec.Command("cmd.exe", "/c", "timeout", "/t", "3", "/nobreak", ">nul", "&", "rmdir", "/s", "/q", appDir)
	cmd.SysProcAttr = hiddenProcAttr()
	cmd.Start()
}

// --- Inter-process mailbox ---------------------------------------------------
//
// The tray icon, notification clicks and the control window run in separate
// processes: they drop a command into inbox\ and wake the main process, the
// only one that modifies the state.

type inboxMsg struct {
	Type   string `json:"type"` // mute, resolve, notgame, reload, logout, revoke, showlogin, quit
	ItemID int    `json:"itemId,omitempty"`
	Key    string `json:"key,omitempty"`
	Title  string `json:"title,omitempty"`
	Token  string `json:"token,omitempty"` // revoke: DPAPI-encrypted token
}

func postInbox(m inboxMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%s.json", time.Now().UnixNano(), randomID()[:6])
	tmp := filepath.Join(appDir, "inbox", name+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(appDir, "inbox", name)); err != nil {
		return err
	}
	wakeMain()
	return nil
}

// readInbox reads and deletes pending commands, in arrival order.
func readInbox() []inboxMsg {
	dir := filepath.Join(appDir, "inbox")
	entries, _ := os.ReadDir(dir)
	var out []inboxMsg
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		var m inboxMsg
		if readJSON(p, &m) == nil {
			out = append(out, m)
		}
		os.Remove(p)
	}
	return out
}
