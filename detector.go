package main

// Detection: which of the open programs are games?
//
//  1. Only processes with a visible window are considered, excluding the
//     Windows folder and known software (browsers, Discord, OBS...).
//  2. A program is a candidate if it has at least one signal:
//       - it is installed in a game library (Steam, Epic, GOG...);
//       - it is an emulator with a game loaded;
//       - a game fingerprint sits next to its exe (engine, store, crack);
//       - it is in the known-games list (Discord);
//       - it keeps the GPU's 3D engine busy for several minutes in a row
//         (not counted for Electron / CEF web apps).
//  3. The server then confirms that it is a game, and which one (agent.go).
//
// No signal means the program is ignored silently.

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	gpuGameThreshold = 10.0 // % of the 3D engine, averaged over one scan
	gpuStreakNeeded  = 3    // consecutive scans (~3 min) required for the GPU-only signal
)

// excludedExe lists software that is never a game (lowercase exe name).
var excludedExe = map[string]bool{}

func init() {
	for _, n := range strings.Split(`chrome, msedge, firefox, brave, opera, opera_gx, vivaldi, iexplore, arc, librewolf, waterfox, thorium,
discord, discordptb, discordcanary, slack, teams, ms-teams, zoom, skype, telegram, whatsapp, signal, element,
steam, steamwebhelper, steamservice, epicgameslauncher, epicwebhelper, galaxyclient, goggalaxy,
battle.net, eadesktop, eabackgroundservice, origin, ubisoftconnect, upc, uplay, xboxpcapp, gamebar,
playnite.desktopapp, playnite.fullscreenapp, heroic, itch, amazon games, riotclientux, riotclientservices, leagueclientux,
vrmonitor, vrserver, vrcompositor, vrdashboard, steamvr_room_setup,
vlc, mpc-hc, mpc-hc64, mpc-be64, potplayermini, potplayermini64, spotify, wmplayer, music.ui, video.ui,
code, devenv, rider64, idea64, pycharm64, goland64, webstorm64, clion64, studio64, sublime_text, notepad, notepad++,
windowsterminal, wt, powershell, pwsh, cmd, conhost,
blender, unity, unity hub, unityhub, unrealeditor, ue4editor, godot, photoshop, illustrator, afterfx,
adobe premiere pro, resolve, obs64, obs32, streamlabs, gimp-2.10, krita, inkscape, paintdotnet, aseprite,
cinema 4d, 3dsmax, maya, substancepainter, houdini, clipstudiopaint, lightroom, audacity, fl64, reaper, ableton live 12 suite,
winword, excel, powerpnt, outlook, onenote, acrord32, acrobat, sumatrapdf,
explorer, taskmgr, systemsettings, applicationframehost, textinputhost, searchhost, shellexperiencehost,
startmenuexperiencehost, lockapp, mmc, regedit, control,
wallpaper32, wallpaper64, webwallpaper32, livelywpf, lively, rainmeter, soundpad, voicemeeter, voicemeeterpro,
msiafterburner, rtss, nvidia app, nvcontainer, nvidia overlay, amdrsserv, radeonsoftware,
lghub, icue, steelseriesgg, razer synapse, 7zfm, winrar, qbittorrent, utorrent,
obsidian, notion, figma, postman, insomnia, filezilla, putty, winscp, anydesk, teamviewer, parsecd,
t3 code (beta), t3 code, claude, chatgpt, cursor`, ",") {
		if n = strings.TrimSpace(n); n != "" {
			excludedExe[n+".exe"] = true
		}
	}
}

type candidate struct {
	Key        string // detection identity: exe path, or emu:<platforms>:<game>
	PID        uint32
	Path       string
	Started    time.Time
	Source     string   // steam, epic, gog, xbox, ubisoft, emulateur, empreinte, discord, gpu
	Signals    []string // for the diagnostic report
	SteamAppID int
	Names      []string
	Platforms  []string
	GPU        float64
}

// procReport is what the detector concluded about a program (diagnostic).
type procReport struct {
	Path     string
	Titles   []string
	GPU      float64
	Decision string
	Signals  []string
}

type detector struct {
	libs      libraries
	disc      *detectableList
	gpu       *gpuSampler
	gpuStreak map[string]int
	self      string
	windir    string
}

func newDetector() *detector {
	return &detector{
		disc:      loadDetectable(),
		gpu:       newGPUSampler(),
		gpuStreak: map[string]int{},
		self:      strings.ToLower(currentExe()),
		windir:    strings.ToLower(filepath.Clean(os.Getenv("WINDIR"))) + `\`,
	}
}

func (d *detector) scan() ([]candidate, []procReport) {
	d.libs.refresh()
	gpu := d.gpu.sample()
	running := steamRunningAppID()
	procs := listWindowedProcesses()

	var cands []candidate
	var reports []procReport
	seenStreak := map[string]bool{}
	for pid, p := range procs {
		lower := strings.ToLower(p.Path)
		base := filepath.Base(lower)
		rep := procReport{Path: p.Path, Titles: p.Titles, GPU: gpu[pid]}
		switch {
		case lower == d.self:
			continue
		case strings.HasPrefix(lower, d.windir):
			rep.Decision = "ignoré (Windows)"
			reports = append(reports, rep)
			continue
		case excludedExe[base]:
			rep.Decision = "ignoré (logiciel connu)"
			reports = append(reports, rep)
			continue
		}

		// Emulators only count with a game loaded.
		if e := findEmulator(p.Path); e != nil {
			title := emulatorGameTitle(e, p.Titles)
			if title == "" {
				rep.Decision = "émulateur sans jeu chargé"
				reports = append(reports, rep)
				continue
			}
			c := candidate{
				Key:       "emu:" + strings.Join(e.platforms, ",") + ":" + strings.ToLower(title),
				PID:       pid,
				Path:      p.Path,
				Started:   p.Started,
				Source:    "emulateur",
				Signals:   []string{"émulateur " + e.name},
				Names:     []string{title},
				Platforms: e.platforms,
				GPU:       gpu[pid],
			}
			rep.Decision, rep.Signals = "JEU « "+title+" »", c.Signals
			reports = append(reports, rep)
			cands = append(cands, c)
			continue
		}

		lib := d.libs.match(p.Path)
		fp := inspectExe(p.Path)
		discName, withFolder := d.disc.match(p.Path)
		var signals []string
		source := ""
		if lib != nil {
			signals = append(signals, "bibliothèque "+lib.Launcher)
			source = lib.Launcher
			if lib.SteamAppID > 0 && lib.SteamAppID == running {
				signals = append(signals, "Steam : en cours")
			}
		}
		if fp.isGame() {
			s := "empreinte"
			if fp.Engine != "" {
				s += " " + fp.Engine
			}
			if len(fp.Markers) > 0 {
				s += " (" + strings.Join(fp.Markers, ", ") + ")"
			}
			signals = append(signals, s)
			if source == "" {
				source = "empreinte"
			}
		}
		if discName != "" && (withFolder || gpu[pid] >= 2) {
			signals = append(signals, "liste Discord : "+discName)
			if source == "" {
				source = "discord"
			}
		}
		if gpu[pid] >= gpuGameThreshold {
			d.gpuStreak[lower]++
		} else {
			d.gpuStreak[lower] = 0
		}
		seenStreak[lower] = true
		gpuOnly := d.gpuStreak[lower] >= gpuStreakNeeded && !fp.Electron
		if len(signals) == 0 && gpuOnly {
			signals = append(signals, "carte graphique 3D soutenue")
			source = "gpu"
		}
		rep.Signals = signals
		if len(signals) == 0 {
			rep.Decision = "pas un jeu"
			if d.gpuStreak[lower] > 0 {
				rep.Decision = "à surveiller (3D depuis " + itoa(d.gpuStreak[lower]) + " min)"
			}
			reports = append(reports, rep)
			continue
		}

		c := candidate{
			Key:     lower,
			PID:     pid,
			Path:    p.Path,
			Started: p.Started,
			Source:  source,
			Signals: signals,
			GPU:     gpu[pid],
		}
		if lib != nil {
			c.SteamAppID = lib.SteamAppID
			c.Names = addName(c.Names, lib.Name)
		}
		if c.SteamAppID == 0 {
			c.SteamAppID = fp.SteamAppID
		}
		if discName != "" {
			c.Names = addName(c.Names, discName)
		}
		c.Names = addName(c.Names, exeProductName(p.Path))
		c.Names = addName(c.Names, gameFolderName(fp.RootDir))
		c.Names = addName(c.Names, filepath.Base(p.Path))
		if len(p.Titles) > 0 {
			c.Names = addName(c.Names, p.Titles[0])
		}
		rep.Decision = "JEU"
		if len(c.Names) > 0 {
			rep.Decision += " « " + c.Names[0] + " »"
		}
		reports = append(reports, rep)
		cands = append(cands, c)
	}
	for k := range d.gpuStreak {
		if !seenStreak[k] {
			delete(d.gpuStreak, k)
		}
	}
	return cands, reports
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
