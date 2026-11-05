package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// prefs are the TUI selections that survive between runs (never the keyboard's own state, that is read from the board).
type prefs struct {
	Palette string `json:"palette"`
	Style   string `json:"style"`
	Brush   string `json:"brush"`
	Bright  int    `json:"bright"`
}

func prefsPath() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = home(".config")
	}
	return filepath.Join(d, "aetherctl", "state.json")
}

func loadPrefs() (p prefs) {
	if b, err := os.ReadFile(prefsPath()); err == nil {
		json.Unmarshal(b, &p)
	}
	return
}

func savePrefs(p prefs) {
	if b, err := json.Marshal(p); err == nil {
		os.MkdirAll(filepath.Dir(prefsPath()), 0o755)
		os.WriteFile(prefsPath(), b, 0o644)
	}
}

var unitArg = regexp.MustCompile(`-(palette|style|bright) (\S+)`)

// runningMusic returns the palette/style/bright the music service was started with, read from its unit file.
func runningMusic() (palette, style string, bright int, ok bool) {
	b, err := os.ReadFile(home(".config/systemd/user/" + unitName + ".service"))
	if err != nil {
		return
	}
	bright = -1
	for _, m := range unitArg.FindAllStringSubmatch(string(b), -1) {
		switch m[1] {
		case "palette":
			palette = m[2]
		case "style":
			style = m[2]
		case "bright":
			bright, _ = strconv.Atoi(m[2])
		}
	}
	return palette, style, bright, palette != ""
}
