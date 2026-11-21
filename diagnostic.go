package main

// Diagnostic: "CoopRatings-Agent.exe --diagnostic" writes to a text file what
// the app sees and decides for each open program, without sending anything to
// the server. Useful to understand why a game is or isn't tracked.

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

func runDiagnostic(open bool) string {
	d := newDetector()
	d.libs.refresh()
	// Spaced GPU samples give an average over a few seconds; repeating the
	// scan also simulates the 3-minute streak of the GPU-only signal.
	time.Sleep(3 * time.Second)
	var reports []procReport
	var cands []candidate
	for i := 0; i < gpuStreakNeeded; i++ {
		cands, reports = d.scan()
		if i < gpuStreakNeeded-1 {
			time.Sleep(2 * time.Second)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "CoopRatings Agent %s — diagnostic du %s\n\n", version, time.Now().Format("02/01/2006 15:04"))
	fmt.Fprintf(&b, "Bibliothèques de jeux : %d jeux installés connus", len(d.libs.games))
	count := map[string]int{}
	for _, g := range d.libs.games {
		count[g.Launcher]++
	}
	var parts []string
	for k, v := range count {
		parts = append(parts, fmt.Sprintf("%s %d", k, v))
	}
	sort.Strings(parts)
	fmt.Fprintf(&b, " (%s)\n", strings.Join(parts, ", "))
	fmt.Fprintf(&b, "Steam, jeu en cours : %d\n", steamRunningAppID())
	fmt.Fprintf(&b, "Liste des jeux connus (Discord) : %d exe\n", len(d.disc.byExe)+len(d.disc.names))
	fmt.Fprintf(&b, "Mesure carte graphique : %v\n\n", d.gpu.ok)

	sort.Slice(reports, func(i, j int) bool {
		gi, gj := strings.HasPrefix(reports[i].Decision, "JEU"), strings.HasPrefix(reports[j].Decision, "JEU")
		if gi != gj {
			return gi
		}
		return reports[i].GPU > reports[j].GPU
	})
	fmt.Fprintf(&b, "Programmes avec une fenêtre : %d — jeux détectés : %d\n\n", len(reports), len(cands))
	for _, r := range reports {
		fmt.Fprintf(&b, "%s\n  %s\n  3D : %.1f %%", r.Decision, r.Path, r.GPU)
		if len(r.Titles) > 0 {
			fmt.Fprintf(&b, " · fenêtre : %q", r.Titles[0])
		}
		b.WriteString("\n")
		if len(r.Signals) > 0 {
			fmt.Fprintf(&b, "  signaux : %s\n", strings.Join(r.Signals, " ; "))
		}
		b.WriteString("\n")
	}
	for _, c := range cands {
		fmt.Fprintf(&b, "→ envoyé au site pour identification : clé %q, Steam %d, noms %q, plateformes %v, source %s\n",
			c.Key, c.SteamAppID, c.Names, c.Platforms, c.Source)
	}
	path := appFile("diagnostic.txt")
	bom := []byte{0xEF, 0xBB, 0xBF} // so Notepad reads the file as UTF-8
	os.WriteFile(path, append(bom, b.String()...), 0o644)
	if open {
		exec.Command("notepad.exe", path).Start()
	}
	return path
}
