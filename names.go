package main

// Names: known emulators, and cleanup of folder names / window titles to
// recover the actual game title ("Hades.II.v1.0.2-GOG" -> "Hades II",
// "DiceHoarder-Win64-Shipping" -> "Dice Hoarder").

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// --- Emulators ---------------------------------------------------------------

type emulator struct {
	prefix    string   // start of the exe name (lowercase)
	name      string   // display name, stripped from the window title
	platforms []string // IGDB slugs of the emulated platforms
}

var emulators = []emulator{
	{"pcsx2", "PCSX2", []string{"ps2"}},
	{"duckstation", "DuckStation", []string{"ps"}},
	{"epsxe", "ePSXe", []string{"ps"}},
	{"pcsxr", "PCSX-R", []string{"ps"}},
	{"ppsspp", "PPSSPP", []string{"psp"}},
	{"rpcs3", "RPCS3", []string{"ps3"}},
	{"dolphin", "Dolphin", []string{"wii", "ngc"}},
	{"desmume", "DeSmuME", []string{"nds"}},
	{"melonds", "melonDS", []string{"nds"}},
	{"citra", "Citra", []string{"3ds"}},
	{"azahar", "Azahar", []string{"3ds"}},
	{"lime3ds", "Lime3DS", []string{"3ds"}},
	{"cemu", "Cemu", []string{"wiiu"}},
	{"mgba", "mGBA", []string{"gba", "gbc", "gb"}},
	{"visualboyadvance", "VisualBoyAdvance", []string{"gba", "gbc", "gb"}},
	{"project64", "Project64", []string{"n64"}},
	{"mupen64", "Mupen64", []string{"n64"}},
	{"simple64", "simple64", []string{"n64"}},
	{"snes9x", "Snes9x", []string{"snes"}},
	{"bsnes", "bsnes", []string{"snes"}},
	{"mesen", "Mesen", []string{"nes", "snes"}},
	{"fceux", "FCEUX", []string{"nes"}},
	{"nestopia", "Nestopia", []string{"nes"}},
	{"xemu", "xemu", []string{"xbox"}},
	{"xenia", "Xenia", []string{"xbox360"}},
	{"flycast", "Flycast", []string{"dc"}},
	{"redream", "redream", []string{"dc"}},
	{"retroarch", "RetroArch", nil},
}

func findEmulator(exePath string) *emulator {
	base := strings.ToLower(filepath.Base(exePath))
	for i := range emulators {
		if strings.HasPrefix(base, emulators[i].prefix) {
			return &emulators[i]
		}
	}
	return nil
}

var (
	reVersion  = regexp.MustCompile(`(?i)\bv?\d+(\.\d+){1,3}([-.]\w+)*\b|\b(build|rev|r)\s?\d{3,}\b`)
	reSerial   = regexp.MustCompile(`(?i)\b[A-Z]{4}[-_ ]?\d{3}[._]?\d{2}\b|\[[A-Z]{4}-\d{5}\]|\([A-Z]{4}-\d{5}\)`)
	reRegion   = regexp.MustCompile(`(?i)[(\[](usa|europe|japan|world|france|fr|en|eu|us|jp|pal|ntsc[-\w]*|rev \w+|v\d[\w.]*|!|[a-z]{2}(,[a-z]{2})+|disc \d+|\d{4}-\d{2}-\d{2})[)\]]`)
	reRomExt   = regexp.MustCompile(`(?i)\.(iso|bin|cue|chd|cso|zso|gz|rvz|wbfs|gcm|ciso|nds|3ds|cia|gba|gbc|gb|z64|n64|v64|sfc|smc|nes|wux|wud|rpx|xex|xiso|gdi|cdi|elf|pbp|m3u|zip|7z)\b`)
	reEmuNoise = regexp.MustCompile(`(?i)\b(fps|vps|speed|slot|limiter|gsdx|vulkan|opengl|direct3d\s?\d*|d3d\d+|software|hardware|x64|x86|sse\d?|avx\d?|dev|jit|interpreter|recompiler|paused|running|booting|loading|nightly|stable|qt|wx|frame(skip)?|turbo|normal|unlimited|resolution|\d+x\d+|\d+%|internal|native|mtgs|ee|vu|gs|cpu|gpu)\b`)
)

// emulatorGameTitle extracts the loaded game from an emulator's window
// titles. "" means no game (emulator sitting in its menu).
func emulatorGameTitle(e *emulator, titles []string) string {
	best := ""
	for _, t := range titles {
		for _, part := range splitTitle(t) {
			p := strings.ReplaceAll(part, e.name, " ")
			p = strings.ReplaceAll(p, strings.ToLower(e.name), " ")
			p = reRomExt.ReplaceAllString(p, " ")
			p = reSerial.ReplaceAllString(p, " ")
			p = reRegion.ReplaceAllString(p, " ")
			p = reVersion.ReplaceAllString(p, " ")
			p = reEmuNoise.ReplaceAllString(p, " ")
			p = collapseSpaces(strings.Trim(p, " -_:|·•()[]"))
			if letters(p) >= 3 && len(p) > len(best) {
				best = p
			}
		}
	}
	return best
}

// splitTitle splits an emulator title on its info separators ("|", "—",
// "•"). Not on "-": it is often part of the game or ROM name
// ("Pokemon - Platinum Version").
func splitTitle(t string) []string {
	return strings.FieldsFunc(t, func(r rune) bool { return r == '|' || r == '—' || r == '–' || r == '•' })
}

func letters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

func collapseSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

// --- Name cleanup (folders, exe, titles) ------------------------------------

var (
	reBrackets  = regexp.MustCompile(`[\[(][^\])]*[\])]`)
	reSceneTail = regexp.MustCompile(`(?i)[-_. ](codex|plaza|skidrow|reloaded|empress|rune|tenoke|flt|fairlight|razor1911|darksiders|prophet|hoodlum|cpy|dodi|fitgirl|elamigos|gog|repack|multi\d*|p2p|goldberg|onlinefix|online-fix|steamrip|kaos|tinyiso|chronos|i_know|doge|hi2u|anomaly)\b.*$`)
	reEdition   = regexp.MustCompile(`(?i)\b(digital\s+)?(deluxe|ultimate|goty|game of the year|complete|definitive|gold|premium|collector'?s|enhanced|anniversary|director'?s cut)(\s+edition)?\b|\bedition\b`)
	reBuildTail = regexp.MustCompile(`(?i)\b(update|build|patch|hotfix|early access|ea)\b.*$`)
	reCamel     = regexp.MustCompile(`([a-z])([A-Z])|([A-Z])([A-Z][a-z])`)
	reShipping  = regexp.MustCompile(`(?i)-(win64|win32|wingdk|windows)-(shipping|test|development)$`)
)

// cleanName turns "Hades.II.v1.0.2-GOG" into "Hades II".
func cleanName(s string) string {
	s = strings.TrimSpace(s)
	if strings.EqualFold(filepath.Ext(s), ".exe") { // only .exe: "Hades.II" keeps its ".II"
		s = s[:len(s)-4]
	}
	s = reShipping.ReplaceAllString(s, "")
	s = reBrackets.ReplaceAllString(s, " ")
	s = reSceneTail.ReplaceAllString(s, "")
	s = reVersion.ReplaceAllString(s, " ")
	s = reBuildTail.ReplaceAllString(s, " ")
	// Scene-style name without spaces: "The.Witcher.3" -> "The Witcher 3".
	if !strings.Contains(strings.TrimSpace(s), " ") {
		s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	}
	s = reEdition.ReplaceAllString(s, " ")
	if !strings.Contains(strings.TrimSpace(s), " ") {
		s = reCamel.ReplaceAllString(s, "$1$3 $2$4")
	}
	return collapseSpaces(strings.Trim(s, " -_:|·•"))
}

// genericNames are too generic to identify a game.
var genericNames = map[string]bool{
	"game": true, "launcher": true, "start": true, "play": true, "main": true, "app": true,
	"client": true, "bin": true, "binaries": true, "win64": true, "win32": true, "x64": true,
	"x86": true, "release": true, "shipping": true, "windows": true, "unity": true,
	"unrealgame": true, "unreal game": true, "unreal engine": true, "godot engine": true, "nw js": true,
	"electron": true, "rpg maker": true, "bootstrappackagedgame": true, "content": true,
	"desktop": true, "bureau": true, "downloads": true, "téléchargements": true, "games": true,
	"jeux": true, "program files": true, "steamapps": true, "common": true, "rpg_rt": true,
}

func isGenericName(s string) bool {
	n := strings.ToLower(strings.TrimSpace(s))
	return len(n) < 2 || genericNames[n] || strings.Contains(n, "unreal engine") ||
		strings.HasPrefix(n, "microsoft") || strings.HasPrefix(n, "windows")
}

// addName appends a cleaned candidate name if it is useful and not a duplicate.
func addName(names []string, raw string) []string {
	n := cleanName(raw)
	if n == "" || isGenericName(n) || isGenericName(strings.TrimSuffix(strings.ToLower(raw), ".exe")) {
		return names
	}
	for _, x := range names {
		if strings.EqualFold(x, n) {
			return names
		}
	}
	return append(names, n)
}
