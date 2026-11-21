package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"Hades.II.v1.0.2-GOG":                        "Hades II",
		"DiceHoarder-Win64-Shipping.exe":             "Dice Hoarder",
		"Elden Ring Deluxe Edition [FitGirl Repack]": "Elden Ring",
		"Cyberpunk 2077 v2.12 (GOG)":                 "Cyberpunk 2077",
		"Hollow_Knight":                              "Hollow Knight",
		"Stardew Valley":                             "Stardew Valley",
		"The.Witcher.3.Wild.Hunt-CODEX":              "The Witcher 3 Wild Hunt",
		"Baldur's Gate 3 - Build 1234":               "Baldur's Gate 3",
	}
	for in, want := range cases {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func TestGenericNames(t *testing.T) {
	for _, n := range []string{"Game", "bin", "Win64", "UnrealGame", "Unity", "Desktop"} {
		if names := addName(nil, n); len(names) != 0 {
			t.Errorf("%q devrait être ignoré, obtenu %v", n, names)
		}
	}
}

func TestEmulatorTitle(t *testing.T) {
	pcsx2 := findEmulator(`E:\jeux\Pcsx2\pcsx2-v1.7.2559\pcsx2x64.exe`)
	desmume := findEmulator(`E:\logiciel\desmume\DeSmuME_0.9.13_x64.exe`)
	if pcsx2 == nil || desmume == nil {
		t.Fatal("émulateurs non reconnus")
	}
	cases := []struct {
		e      *emulator
		titles []string
		want   string
	}{
		{pcsx2, []string{"PCSX2 v1.7.2559"}, ""},
		{pcsx2, []string{"Ratchet & Clank 3 [SCES-51608]"}, "Ratchet & Clank 3"},
		{pcsx2, []string{"Slot: 0 | Speed: 100% | VPS: 60 | Ratchet & Clank 3"}, "Ratchet & Clank 3"},
		{desmume, []string{"DeSmuME 0.9.13 x64 SSE2"}, ""},
		{desmume, []string{"DeSmuME 0.9.13 x64 SSE2 | Pokemon - Platinum Version (USA).nds"}, "Pokemon - Platinum Version"},
	}
	for _, c := range cases {
		if got := emulatorGameTitle(c.e, c.titles); got != c.want {
			t.Errorf("emulatorGameTitle(%q) = %q, attendu %q", c.titles, got, c.want)
		}
	}
}

func TestSteamFiles(t *testing.T) {
	vdf := `"libraryfolders" { "0" { "path" "C:\\Program Files (x86)\\Steam" } "1" { "path" "D:\\SteamLibrary" } }`
	libs := parseLibraryFolders(vdf)
	if len(libs) != 2 || libs[1] != `D:\SteamLibrary` {
		t.Errorf("bibliothèques : %v", libs)
	}
	acf := `"AppState" { "appid" "3687090" "name" "Dice Hoarder" "installdir" "Dice Hoarder" }`
	id, dir, name := parseAppManifest(acf)
	if id != 3687090 || dir != "Dice Hoarder" || name != "Dice Hoarder" {
		t.Errorf("manifest : %d %q %q", id, dir, name)
	}
}

func TestFingerprint(t *testing.T) {
	root := t.TempDir()
	touch := func(p string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	// Cracked Unity game in an arbitrary folder, with its Steam app ID.
	unity := filepath.Join(root, "Desktop", "Hollow.Knight.v1.5-GOG")
	touch(filepath.Join(unity, "hollow_knight.exe"))
	touch(filepath.Join(unity, "UnityPlayer.dll"))
	os.WriteFile(filepath.Join(unity, "steam_emu.ini"), []byte("[Settings]\nAppId=367520\n"), 0o644)
	f := computeFingerprint(filepath.Join(unity, "hollow_knight.exe"))
	if f.Engine != "Unity" || f.SteamAppID != 367520 || !f.isGame() {
		t.Errorf("Unity : %+v", f)
	}
	// Unreal game: root is 3 levels above the exe.
	ue := filepath.Join(root, "Dice Hoarder")
	touch(filepath.Join(ue, "Engine", "x.txt"))
	touch(filepath.Join(ue, "DiceHoarder", "Binaries", "Win64", "DiceHoarder-Win64-Shipping.exe"))
	f = computeFingerprint(filepath.Join(ue, "DiceHoarder", "Binaries", "Win64", "DiceHoarder-Win64-Shipping.exe"))
	if f.Engine != "Unreal" || filepath.Base(f.RootDir) != "Dice Hoarder" {
		t.Errorf("Unreal : %+v", f)
	}
	// Ordinary software: no clues.
	tool := filepath.Join(root, "Outil")
	touch(filepath.Join(tool, "outil.exe"))
	touch(filepath.Join(tool, "Qt6Core.dll"))
	if f = computeFingerprint(filepath.Join(tool, "outil.exe")); f.isGame() {
		t.Errorf("logiciel pris pour un jeu : %+v", f)
	}
	// Electron app: flagged as such, not a game.
	el := filepath.Join(root, "Appli")
	touch(filepath.Join(el, "appli.exe"))
	touch(filepath.Join(el, "resources", "app.asar"))
	if f = computeFingerprint(filepath.Join(el, "appli.exe")); !f.Electron || f.isGame() {
		t.Errorf("Electron : %+v", f)
	}
}

func TestDetectableMatch(t *testing.T) {
	d := &detectableList{
		byExe:    map[string]string{"hades.exe": "Hades"},
		bySuffix: map[string][]string{"game.exe": {"diablo ii/game.exe"}},
		names:    map[string]string{"diablo ii/game.exe": "Diablo II"},
	}
	if n, folder := d.match(`D:\Jeux\Diablo II\Game.exe`); n != "Diablo II" || !folder {
		t.Errorf("Diablo II : %q %v", n, folder)
	}
	if n, _ := d.match(`C:\Autre\Game.exe`); n != "" {
		t.Errorf("game.exe seul ne doit pas matcher : %q", n)
	}
	if n, folder := d.match(`C:\Users\x\Desktop\Hades\Hades.exe`); n != "Hades" || folder {
		t.Errorf("Hades : %q %v", n, folder)
	}
}

func TestFormatDuration(t *testing.T) {
	for secs, want := range map[int]string{300: "5 min", 3600: "1 h", 4500: "1 h 15"} {
		if got := formatDuration(secs); got != want {
			t.Errorf("formatDuration(%d) = %q, attendu %q", secs, got, want)
		}
	}
}

func TestPidFromInstance(t *testing.T) {
	if pidFromInstance("pid_1234_luid_0x00000000_0x0000D1C5_phys_0_eng_0_engtype_3D") != 1234 {
		t.Error("pid mal lu")
	}
}
