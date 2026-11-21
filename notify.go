package main

// Windows notifications ("toasts") with buttons.
//
// An app not installed from the Store must register its identity
// (AppUserModelID) in the registry for Windows to show its name and icon.
// Buttons open a link: https://… (the spreadsheet) or coopratings-agent:…
// (our registered protocol: Windows relaunches the app with that link, which
// drops the command into inbox\).
//
// Toasts are shown through PowerShell (WinRT notification API), started
// without a window.

import (
	"encoding/base64"
	"fmt"
	"html"
	"os"
	"os/exec"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	appUserModelID = "CoopRatings.Agent"
	protocolScheme = "coopratings-agent"
)

// registerShellIntegration registers the notification identity and the
// coopratings-agent: protocol (under HKCU, no administrator rights needed).
func registerShellIntegration(exePath string) error {
	icon := appFile("icon.png")
	if err := writeLogoPNG(icon); err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+appUserModelID, registry.SET_VALUE)
	if err != nil {
		return err
	}
	k.SetStringValue("DisplayName", "CoopRatings")
	k.SetStringValue("IconUri", icon)
	k.SetStringValue("IconBackgroundColor", "FF268BD2")
	k.Close()

	k, _, err = registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+protocolScheme, registry.SET_VALUE)
	if err != nil {
		return err
	}
	k.SetStringValue("", "URL:CoopRatings Agent")
	k.SetStringValue("URL Protocol", "")
	k.Close()
	k, _, err = registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+protocolScheme+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("", fmt.Sprintf(`"%s" "%%1"`, exePath))
}

func unregisterShellIntegration() {
	registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+appUserModelID)
	registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\`+protocolScheme+`\shell\open\command`)
	registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\`+protocolScheme+`\shell\open`)
	registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\`+protocolScheme+`\shell`)
	registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\`+protocolScheme)
}

type toastAction struct {
	Label string
	URL   string
}

// showToast shows a quiet (silent) notification. open is the link opened
// when the notification itself is clicked.
func showToast(title, body, open string, actions ...toastAction) error {
	var b strings.Builder
	b.WriteString(`<toast duration="short"`)
	if open != "" {
		fmt.Fprintf(&b, ` activationType="protocol" launch="%s"`, html.EscapeString(open))
	}
	b.WriteString(`><visual><binding template="ToastGeneric">`)
	fmt.Fprintf(&b, `<text>%s</text>`, html.EscapeString(title))
	if body != "" {
		fmt.Fprintf(&b, `<text>%s</text>`, html.EscapeString(body))
	}
	b.WriteString(`</binding></visual>`)
	if len(actions) > 0 {
		b.WriteString(`<actions>`)
		for _, a := range actions {
			fmt.Fprintf(&b, `<action content="%s" activationType="protocol" arguments="%s"/>`,
				html.EscapeString(a.Label), html.EscapeString(a.URL))
		}
		b.WriteString(`</actions>`)
	}
	b.WriteString(`<audio silent="true"/></toast>`)

	script := `$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml(@'
` + b.String() + `
'@)
$toast = New-Object Windows.UI.Notifications.ToastNotification $xml
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('` + appUserModelID + `').Show($toast)`

	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-WindowStyle", "Hidden", "-EncodedCommand", encodePS(script))
	cmd.SysProcAttr = hiddenProcAttr()
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("notification : %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// encodePS encodes a script as base64 UTF-16LE for -EncodedCommand, avoiding quoting issues.
func encodePS(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i] = byte(c)
		b[2*i+1] = byte(c >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// openURL opens a link in the default browser (normal window).
func openURL(u string) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(u)
	windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
