package main

// Launcher libraries: a program installed in one is treated as a game (the
// server then confirms it, to rule out non-game software sold on Steam).
//
//   Steam   : registry (folder + running game) + appmanifest_*.acf
//   Epic    : C:\ProgramData\Epic\EpicGamesLauncher\Data\Manifests\*.item
//   GOG     : registry HKLM\SOFTWARE\WOW6432Node\GOG.com\Games
//   Xbox    : <drive>:\XboxGames
//   Ubisoft : registry HKLM\SOFTWARE\WOW6432Node\Ubisoft\Launcher\Installs

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type libraryGame struct {
	Launcher   string // steam, epic, gog, xbox, ubisoft
	Dir        string // install folder (lowercase, \ separator)
	Name       string
	SteamAppID int
}

type libraries struct {
	games    []libraryGame
	loadedAt time.Time
}

// refresh reloads the libraries at most every 10 minutes.
func (l *libraries) refresh() {
	if time.Since(l.loadedAt) < 10*time.Minute && l.loadedAt.After(time.Time{}) {
		return
	}
	var g []libraryGame
	g = append(g, steamGames()...)
	g = append(g, epicGames()...)
	g = append(g, gogGames()...)
	g = append(g, xboxGames()...)
	g = append(g, ubisoftGames()...)
	l.games = g
	l.loadedAt = time.Now()
}

// match returns the library game whose folder contains this exe path (the most specific one).
func (l *libraries) match(exePath string) *libraryGame {
	p := strings.ToLower(filepath.Clean(exePath))
	var best *libraryGame
	for i := range l.games {
		d := l.games[i].Dir
		if d != "" && strings.HasPrefix(p, d+`\`) && (best == nil || len(d) > len(best.Dir)) {
			best = &l.games[i]
		}
	}
	return best
}

func normDir(d string) string {
	return strings.ToLower(filepath.Clean(strings.ReplaceAll(d, "/", `\`)))
}

// --- Steam -------------------------------------------------------------------

func steamRunningAppID() int {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE)
	if err != nil {
		return 0
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("RunningAppID")
	if err != nil {
		return 0
	}
	return int(v)
}

var (
	vdfPathRe = regexp.MustCompile(`"path"\s+"([^"]+)"`)
	acfKeyRe  = regexp.MustCompile(`"(appid|installdir|name)"\s+"([^"]*)"`)
)

func steamGames() []libraryGame {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	steamPath, _, _ := k.GetStringValue("SteamPath")
	k.Close()
	if steamPath == "" {
		return nil
	}
	libs := []string{steamPath}
	if b, err := os.ReadFile(filepath.Join(steamPath, "steamapps", "libraryfolders.vdf")); err == nil {
		libs = append(libs, parseLibraryFolders(string(b))...)
	}
	seen := map[string]bool{}
	var out []libraryGame
	for _, lib := range libs {
		lib = normDir(lib)
		if seen[lib] {
			continue
		}
		seen[lib] = true
		manifests, _ := filepath.Glob(filepath.Join(lib, "steamapps", "appmanifest_*.acf"))
		for _, m := range manifests {
			b, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			appID, installDir, name := parseAppManifest(string(b))
			if appID == 0 || installDir == "" {
				continue
			}
			out = append(out, libraryGame{
				Launcher:   "steam",
				Dir:        normDir(filepath.Join(lib, "steamapps", "common", installDir)),
				Name:       name,
				SteamAppID: appID,
			})
		}
	}
	return out
}

// parseLibraryFolders returns the library folders listed in libraryfolders.vdf.
func parseLibraryFolders(vdf string) []string {
	var out []string
	for _, m := range vdfPathRe.FindAllStringSubmatch(vdf, -1) {
		out = append(out, strings.ReplaceAll(m[1], `\\`, `\`))
	}
	return out
}

// parseAppManifest reads appid / installdir / name from an appmanifest_*.acf.
func parseAppManifest(acf string) (appID int, installDir, name string) {
	for _, m := range acfKeyRe.FindAllStringSubmatch(acf, -1) {
		switch m[1] {
		case "appid":
			if appID == 0 {
				appID, _ = strconv.Atoi(m[2])
			}
		case "installdir":
			if installDir == "" {
				installDir = m[2]
			}
		case "name":
			if name == "" {
				name = m[2]
			}
		}
	}
	return
}

// --- Epic --------------------------------------------------------------------

func epicGames() []libraryGame {
	files, _ := filepath.Glob(`C:\ProgramData\Epic\EpicGamesLauncher\Data\Manifests\*.item`)
	var out []libraryGame
	for _, f := range files {
		var m struct {
			DisplayName     string   `json:"DisplayName"`
			InstallLocation string   `json:"InstallLocation"`
			AppCategories   []string `json:"AppCategories"`
		}
		if readJSON(f, &m) != nil || m.InstallLocation == "" {
			continue
		}
		isGame := false
		for _, c := range m.AppCategories {
			if strings.EqualFold(c, "games") {
				isGame = true
			}
		}
		if isGame {
			out = append(out, libraryGame{Launcher: "epic", Dir: normDir(m.InstallLocation), Name: m.DisplayName})
		}
	}
	return out
}

// --- GOG ---------------------------------------------------------------------

func gogGames() []libraryGame {
	return registryGames(`SOFTWARE\WOW6432Node\GOG.com\Games`, "gog", "path", "gameName")
}

func ubisoftGames() []libraryGame {
	return registryGames(`SOFTWARE\WOW6432Node\Ubisoft\Launcher\Installs`, "ubisoft", "InstallDir", "")
}

func registryGames(root, launcher, pathValue, nameValue string) []libraryGame {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, root, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer k.Close()
	subs, _ := k.ReadSubKeyNames(-1)
	var out []libraryGame
	for _, s := range subs {
		sk, err := registry.OpenKey(k, s, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		dir, _, _ := sk.GetStringValue(pathValue)
		name := ""
		if nameValue != "" {
			name, _, _ = sk.GetStringValue(nameValue)
		}
		sk.Close()
		if dir == "" {
			continue
		}
		if name == "" {
			name = filepath.Base(filepath.Clean(dir))
		}
		out = append(out, libraryGame{Launcher: launcher, Dir: normDir(dir), Name: name})
	}
	return out
}

// --- Xbox / Game Pass --------------------------------------------------------

func xboxGames() []libraryGame {
	mask, _ := windows.GetLogicalDrives()
	var out []libraryGame
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\XboxGames`
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && !strings.EqualFold(e.Name(), "GameSave") {
				out = append(out, libraryGame{Launcher: "xbox", Dir: normDir(filepath.Join(root, e.Name())), Name: e.Name()})
			}
		}
	}
	return out
}
