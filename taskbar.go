package main

// Shows the notification-area icon directly in the taskbar while something
// needs the user (a game to identify or to rate, a login), and puts it back
// in the overflow (^) afterwards.
//
// Windows 11 stores, for each notification-area icon, whether it is shown in
// the taskbar: HKCU\Control Panel\NotifyIconSettings\<id>\IsPromoted, the
// setting behind Settings > Personalization > Taskbar > Other system tray
// icons. Explorer applies changes immediately. Windows 10 has no such key:
// nothing happens there.

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

const notifyIconSettings = `Control Panel\NotifyIconSettings`

// trayIconKey returns the settings key of this exe's icon, or "" if Windows
// has not registered it (yet).
func trayIconKey() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, notifyIconSettings, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return ""
	}
	defer k.Close()
	names, _ := k.ReadSubKeyNames(-1)
	exe := currentExe()
	for _, n := range names {
		sk, err := registry.OpenKey(k, n, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		p, _, _ := sk.GetStringValue("ExecutablePath")
		sk.Close()
		if strings.EqualFold(p, exe) {
			return notifyIconSettings + `\` + n
		}
	}
	return ""
}

// syncTrayIcon applies the rule above. The user's own choice wins: an icon
// they pinned to the taskbar stays there, and one they hide while it needs
// attention is not shown again until the next time (st.TrayShown).
func (a *agent) syncTrayIcon(attention bool) {
	if tray == nil {
		return
	}
	key := trayIconKey()
	if key == "" {
		return
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	v, _, _ := k.GetIntegerValue("IsPromoted")
	shown := v == 1
	switch {
	case attention && !shown && !a.st.TrayShown:
		if k.SetDWordValue("IsPromoted", 1) == nil {
			a.st.TrayShown = true
		}
	case !attention && shown && a.st.TrayShown:
		k.SetDWordValue("IsPromoted", 0)
		a.st.TrayShown = false
	case !attention:
		a.st.TrayShown = false
	}
}
