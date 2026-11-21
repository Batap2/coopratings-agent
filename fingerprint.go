package main

// Game fingerprints: files next to the exe that give away a game, wherever
// it is installed (desktop, arbitrary folder, cracked copy...).
//
//   engines     Unity, Unreal, Godot, GameMaker, RPG Maker, Ren'Py, Source
//   stores      steam_api, Epic Online Services, GOG Galaxy
//   middleware  Bink, FMOD, Wwise, PhysX, DLSS / FSR / XeSS
//   cracks      steam_appid.txt, steam_emu.ini, OnlineFix.ini... (often with
//               the game's Steam app ID, which gives an exact match)
//
// Electron / CEF apps (Discord-like apps, custom launchers) are flagged too:
// they use the GPU without being games, so GPU usage alone is not enough to
// select them.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type fingerprint struct {
	Engine     string   // recognised engine, "" if none
	Markers    []string // clues found (for the diagnostic report)
	SteamAppID int      // read from crack files / steam_appid.txt
	Electron   bool
	RootDir    string // game root folder (used for its name)
}

// isGame reports whether there is at least one strong clue (engine, store, crack, middleware).
func (f fingerprint) isGame() bool { return f.Engine != "" || len(f.Markers) > 0 }

var (
	fpMu    sync.Mutex
	fpCache = map[string]fingerprint{}
)

func inspectExe(exePath string) fingerprint {
	key := strings.ToLower(exePath)
	fpMu.Lock()
	f, ok := fpCache[key]
	fpMu.Unlock()
	if ok {
		return f
	}
	f = computeFingerprint(exePath)
	fpMu.Lock()
	fpCache[key] = f
	fpMu.Unlock()
	return f
}

var (
	middlewareRe = regexp.MustCompile(`^(bink2?w(32|64)|fmod.*|aksoundengine.*|physx.*|nvngx_dlss.*|amd_fidelityfx.*|ffx_fsr.*|libxess|gfsdk_.*|discord_game_sdk|xinput1_3|d3dx9_\d+|d3dx11_\d+|openvr_api)\.dll$`)
	storeRe      = regexp.MustCompile(`^(steam_api(64)?|eossdk-win(32|64)-shipping|galaxy(64)?|galaxypeer(64)?|uplay_r\d.*|upc_r\d.*)\.dll$`)
	crackFileRe  = regexp.MustCompile(`^(steam_appid\.txt|steam_emu\.ini|cream_api\.ini|onlinefix\.ini|codex\.ini|rune\.ini|smartsteamemu\.ini|ali213\.ini|3dmgame\.ini|coldclientloader\.ini|steam_interfaces\.txt|.*\.cdx)$`)
	appIDRe      = regexp.MustCompile(`(?i)\b(real)?app_?id\s*=\s*(\d{2,9})`)
)

func computeFingerprint(exePath string) fingerprint {
	var f fingerprint
	dir := filepath.Dir(exePath)
	base := strings.ToLower(filepath.Base(exePath))
	stem := strings.TrimSuffix(base, ".exe")
	f.RootDir = dir

	entries, _ := os.ReadDir(dir)
	if len(entries) > 600 { // catch-all folder (desktop...): don't read everything
		entries = entries[:600]
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[strings.ToLower(e.Name())] = true
	}
	has := func(n string) bool { return names[n] }

	switch {
	case has("unityplayer.dll") || has("gameassembly.dll") || has(stem+"_data"):
		f.Engine = "Unity"
	case strings.Contains(base, "-win64-shipping") || strings.Contains(base, "-win32-shipping") || strings.Contains(base, "-wingdk-shipping"):
		f.Engine = "Unreal"
	case has("data.win"):
		f.Engine = "GameMaker"
	case has(stem + ".pck"):
		f.Engine = "Godot"
	case has("renpy") && has("lib"):
		f.Engine = "Ren'Py"
	case has("www") && (exists(filepath.Join(dir, "www", "js", "rpg_core.js")) || exists(filepath.Join(dir, "www", "js", "rmmz_core.js"))),
		has("js") && exists(filepath.Join(dir, "js", "rmmz_core.js")):
		f.Engine = "RPG Maker MV/MZ"
	case has("engine.dll") || exists(filepath.Join(dir, "bin", "engine.dll")):
		f.Engine = "Source"
	}
	for n := range names {
		switch {
		case strings.HasPrefix(n, "rgss") && strings.HasSuffix(n, ".dll"),
			strings.HasSuffix(n, ".rgssad"), strings.HasSuffix(n, ".rgss2a"), strings.HasSuffix(n, ".rgss3a"):
			if f.Engine == "" {
				f.Engine = "RPG Maker"
			}
		case storeRe.MatchString(n):
			f.Markers = append(f.Markers, n)
		case middlewareRe.MatchString(n):
			f.Markers = append(f.Markers, n)
		case crackFileRe.MatchString(n):
			f.Markers = append(f.Markers, n)
			if id := readAppID(filepath.Join(dir, n)); id > 0 && f.SteamAppID == 0 {
				f.SteamAppID = id
			}
		}
	}
	if has("steam_settings") {
		f.Markers = append(f.Markers, "steam_settings")
		if id := readAppID(filepath.Join(dir, "steam_settings", "steam_appid.txt")); id > 0 && f.SteamAppID == 0 {
			f.SteamAppID = id
		}
	}

	// Unreal: the exe lives in <Game>\<Project>\Binaries\Win64; the game root
	// (with the Engine folder) is 3 levels up.
	lower := strings.ToLower(dir)
	if strings.HasSuffix(lower, `\binaries\win64`) || strings.HasSuffix(lower, `\binaries\win32`) || strings.HasSuffix(lower, `\binaries\wingdk`) {
		root := filepath.Dir(filepath.Dir(filepath.Dir(dir)))
		if exists(filepath.Join(root, "Engine")) {
			if f.Engine == "" {
				f.Engine = "Unreal"
			}
			f.RootDir = root
		} else {
			f.RootDir = filepath.Dir(filepath.Dir(dir))
		}
	} else if isGenericName(filepath.Base(dir)) {
		f.RootDir = filepath.Dir(dir)
	}

	// Electron / CEF / nw.js: a "web" app (not a game on its own).
	if exists(filepath.Join(dir, "resources", "app.asar")) || has("libcef.dll") ||
		(has("chrome_100_percent.pak") && has("resources.pak")) || has("nw.dll") {
		f.Electron = true
	}
	return f
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// readAppID reads a Steam app ID from steam_appid.txt (a bare number) or
// from a crack .ini ("AppId=1234", "RealAppId=1234").
func readAppID(path string) int {
	fh, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	for i := 0; sc.Scan() && i < 200; i++ {
		line := strings.TrimSpace(sc.Text())
		if n, err := strconv.Atoi(line); err == nil && n > 0 {
			return n
		}
		if m := appIDRe.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[2]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}
