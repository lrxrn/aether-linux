package main

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Terminal UI modelled on the Aether console: Lighting / Remap / Music / Device tabs around a live keyboard.
// Every device operation opens the board only briefly, so the music service can own it the rest of the time.

var (
	tabs    = []string{"LIGHTING", "REMAP", "MUSIC", "DEVICE"}
	palList = []string{"widgets", "theme", "rainbow"}
	effects = []string{"Static Bright", "Single Point On", "Single Point Off", "Starry Sky", "Snowfall", "Floral Competition",
		"Dynamic Breathing", "Spectrum Cycle", "Color Fountain", "Colorful Interchange", "Flowing with the Waves", "Turning Peaks",
		"One Touch to Fire", "Two Birds with One Stone", "Ripples Spread", "Endless Flow", "Layered Mountains", "Gentle Rain and Wind", "Back and Forth"}
	styles = []string{"bars", "pulse"}
)

const (
	pxPerCol = 10 // layout px -> terminal columns
	kbTop    = 3  // screen row where the keyboard starts
	rowH     = 3  // text rows per key row (2 coloured + 1 gap)
)

type model struct {
	tab, cur, layer, eff, bri, spd, pal, sty int
	brush                                    RGB
	colors                                   map[int]RGB
	keymap                                   []byte // current layer, 512 bytes
	info                                     Info
	status                                   string
	edit                                     string // "" | "brush" | "remap"
	buf                                      string
	bg                                       bool
	w, h                                     int
}

type (
	tickMsg struct{}
	liveMsg struct {
		c    map[int]RGB
		info Info
		bg   bool
	}
	doneMsg struct {
		ok  string
		err error
	}
	keymapMsg struct{ cur []byte }
)

func runTUI() {
	m := model{colors: map[int]RGB{}, bri: 6, spd: 2, brush: RGB{255, 90, 90}, status: "ready"}
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		die(err)
	}
}

func (m model) Init() tea.Cmd { return tea.Batch(pollCmd(), tickCmd()) }

func tickCmd() tea.Cmd {
	return tea.Tick(700*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// with opens the keyboard for one operation; ErrBusy while the music service owns it.
func with(f func(*Dev) error, ok string) tea.Cmd {
	return func() tea.Msg {
		d, err := Open()
		if err != nil {
			return doneMsg{err: err}
		}
		defer d.Close()
		return doneMsg{ok: ok, err: f(d)}
	}
}

func pollCmd() tea.Cmd {
	return func() tea.Msg {
		if bgActive() {
			return liveMsg{bg: true}
		}
		d, err := Open()
		if err != nil {
			return doneMsg{err: err}
		}
		defer d.Close()
		c, err := d.Live()
		if err != nil {
			return doneMsg{err: err}
		}
		return liveMsg{c: c, info: d.Info}
	}
}

func keymapCmd(fn bool) tea.Cmd {
	return func() tea.Msg {
		d, err := Open()
		if err != nil {
			return doneMsg{err: err}
		}
		defer d.Close()
		g := byte(cGetKey)
		if fn {
			g = cGetFn
		}
		cur, err := d.Read(g, 512)
		if err != nil {
			return doneMsg{err: err}
		}
		return keymapMsg{cur}
	}
}

func (m model) palette() Palette { p, _ := NewPalette(palList[m.pal]); return p }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = x.Width, x.Height
	case tickMsg:
		return m, tea.Batch(pollCmd(), tickCmd())
	case liveMsg:
		m.bg = x.bg
		if !x.bg {
			m.colors, m.info = x.c, x.info
		}
	case doneMsg:
		if x.err != nil {
			m.status = "error: " + x.err.Error()
		} else if x.ok != "" {
			m.status = x.ok
		}
		return m, pollCmd()
	case keymapMsg:
		m.keymap = x.cur
	case tea.MouseMsg:
		if x.Action == tea.MouseActionPress && x.Button == tea.MouseButtonLeft {
			for i, k := range Keys {
				c0, c1, r0, r1 := keyBox(k)
				if x.X >= c0 && x.X < c1 && x.Y >= kbTop+r0 && x.Y < kbTop+r1 {
					m.cur = i
					if m.tab == 0 {
						return m, m.paintKey()
					}
				}
			}
		}
	case tea.KeyMsg:
		return m.key(x)
	}
	return m, nil
}

func keyBox(k Key) (c0, c1, r0, r1 int) {
	c0, c1 = k.X/pxPerCol, (k.X+k.W)/pxPerCol-1
	r0 = k.Row * rowH
	r1 = r0 + 2
	if k.H > 56 {
		r1 = r0 + rowH + 2
	}
	return
}

func (m model) paintKey() tea.Cmd {
	slot, rgb := Keys[m.cur].Slot, m.brush
	return with(func(d *Dev) error {
		cur, err := d.Live() // start from what is showing so the other keys keep their colour
		if err != nil {
			return err
		}
		if err := d.SetMode(d.CustomMode(), nil, -1, -1); err != nil {
			return err
		}
		return d.Paint(func(k Key) RGB {
			if k.Slot == slot {
				return rgb
			}
			return cur[k.Slot]
		})
	}, "painted "+Keys[m.cur].Label)
}

func (m model) move(dx, dy int) int { // nearest key in a direction, by centres
	c := Keys[m.cur]
	cx, cy, best, bi := c.X+c.W/2, c.Row*62+28, math.MaxFloat64, m.cur
	for i, k := range Keys {
		kx, ky := k.X+k.W/2-cx, k.Row*62+28-cy
		if (dx != 0 && kx*dx <= 0) || (dy != 0 && ky*dy <= 0) {
			continue
		}
		d := math.Hypot(float64(kx), float64(ky)*2.2)
		if dx != 0 {
			d = math.Hypot(float64(kx), float64(ky)*4)
		}
		if d < best {
			best, bi = d, i
		}
	}
	return bi
}

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if m.edit != "" {
		switch s {
		case "esc":
			m.edit, m.buf = "", ""
		case "enter":
			return m.commitEdit()
		case "backspace":
			if len(m.buf) > 0 {
				m.buf = m.buf[:len(m.buf)-1]
			}
		default:
			if len(k.Runes) == 1 {
				m.buf += string(k.Runes)
			}
		}
		return m, nil
	}
	switch s {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab":
		m.tab = (m.tab + 1) % len(tabs)
		return m.enterTab()
	case "shift+tab":
		m.tab = (m.tab + len(tabs) - 1) % len(tabs)
		return m.enterTab()
	case "left", "h":
		m.cur = m.move(-1, 0)
	case "right", "l":
		m.cur = m.move(1, 0)
	case "up", "k":
		m.cur = m.move(0, -1)
	case "down", "j":
		m.cur = m.move(0, 1)
	}
	switch m.tab {
	case 0:
		switch s {
		case "p":
			m.pal = (m.pal + 1) % len(palList)
		case "g":
			p := m.palette()
			return m, with(func(d *Dev) error {
				if err := d.SetMode(d.CustomMode(), nil, -1, -1); err != nil {
					return err
				}
				return d.Paint(func(k Key) RGB { return p(keyX(k)) })
			}, "gradient '"+palList[m.pal]+"' applied")
		case "e":
			m.eff = (m.eff + 1) % len(effects)
		case "E":
			m.eff = (m.eff + len(effects) - 1) % len(effects)
		case "+", "=":
			m.bri = min(6, m.bri+1)
		case "-":
			m.bri = max(0, m.bri-1)
		case "]":
			m.spd = min(5, m.spd+1)
		case "[":
			m.spd = max(0, m.spd-1)
		case "a":
			eff, b, sp := m.eff+1, m.bri, m.spd
			return m, with(func(d *Dev) error { return d.SetMode(byte(eff), nil, b, sp) }, "effect '"+effects[m.eff]+"' applied")
		case "b":
			rgb, b := m.brush, m.bri
			return m, with(func(d *Dev) error { return d.SetMode(1, &rgb, b, -1) }, "solid colour applied")
		case "c":
			m.edit, m.buf = "brush", ""
		case " ", "enter":
			return m, m.paintKey()
		}
	case 1:
		switch s {
		case "f":
			m.layer ^= 1
			return m, keymapCmd(m.layer == 1)
		case "r":
			m.edit, m.buf = "remap", ""
		case "x":
			if m.layer == 0 && Keys[m.cur].Usage != 0 { // factory layout from Aether (the board's own default-keymap read returns zeros on 1.07)
				return m, m.writeKey(byte(Keys[m.cur].Usage), "restored default")
			}
			m.status = "no factory default known for the Fn layer"
		}
	case 2:
		switch s {
		case "p":
			m.pal = (m.pal + 1) % len(palList)
		case "y":
			m.sty = (m.sty + 1) % len(styles)
		case "enter", " ":
			if bgActive() {
				return m, func() tea.Msg {
					return doneMsg{ok: "music stopped", err: exec.Command("systemctl", "--user", "stop", unitName).Run()}
				}
			}
			pal, sty := palList[m.pal], styles[m.sty]
			return m, func() tea.Msg {
				return doneMsg{ok: "music running in the background · you can quit the TUI", err: bgStart(pal, sty)}
			}
		}
	case 3:
		if s == "b" {
			return m, with(func(d *Dev) error { return backupFile(d) }, "backup saved to ./aether-backup-*.json")
		}
	}
	return m, nil
}

func (m model) enterTab() (tea.Model, tea.Cmd) {
	if m.tab == 1 {
		return m, keymapCmd(m.layer == 1)
	}
	return m, nil
}

func (m model) writeKey(usage byte, ok string) tea.Cmd {
	slot, fn := Keys[m.cur].Slot, m.layer == 1
	return tea.Sequence(with(func(d *Dev) error { return setKey(d, fn, slot, usage) }, ok), keymapCmd(fn))
}

func (m model) commitEdit() (tea.Model, tea.Cmd) {
	kind, in := m.edit, strings.TrimSpace(m.buf)
	m.edit, m.buf = "", ""
	switch kind {
	case "brush":
		c, err := hexRGB(in)
		if err != nil {
			m.status = "error: " + err.Error()
		} else {
			m.brush, m.status = c, "brush set"
		}
	case "remap": // a key label ("Caps", "F5") or a HID usage in hex ("0x39")
		if u, err := strconv.ParseInt(in, 0, 32); err == nil {
			return m, m.writeKey(byte(u), "remapped")
		}
		for _, k := range Keys {
			if strings.EqualFold(k.Label, in) && k.Usage != 0 {
				return m, m.writeKey(byte(k.Usage), fmt.Sprintf("%s now sends %s", Keys[m.cur].Label, k.Label))
			}
		}
		m.status = "error: no key named " + in + " (or use a HID usage like 0x39)"
	}
	return m, nil
}

func backupFile(d *Dev) error {
	f, err := os.CreateTemp(".", "aether-backup-*.json")
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "{")
	for i, r := range [][2]int{{cGetSettings, 56}, {cGetKey, 512}, {cGetFn, 512}, {cGetLed, 16}, {cGetCustom, 512}} {
		b, err := d.Read(byte(r[0]), r[1])
		if err != nil {
			return err
		}
		if i > 0 {
			fmt.Fprint(f, ",")
		}
		fmt.Fprintf(f, "%q:\"%x\"", fmt.Sprintf("cmd_0x%02x", r[0]), b)
	}
	fmt.Fprintf(f, ",\"firmware\":\"%v\"}\n", d.Info.Version)
	return nil
}

// ---- rendering -------------------------------------------------------------

func fg(c RGB) string  { return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2]) }
func bgc(c RGB) string { return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", c[0], c[1], c[2]) }

const reset = "\x1b[0m"

func (m model) keyboard() string {
	type cell struct {
		r  rune
		fg RGB
		bg RGB
		on bool
	}
	cols, rows := 1172/pxPerCol+1, 6*rowH+3
	grid := make([][]cell, rows)
	for i := range grid {
		grid[i] = make([]cell, cols)
		for j := range grid[i] {
			grid[i][j].r = ' '
		}
	}
	for i, k := range Keys {
		c := m.colors[k.Slot]
		if m.bg {
			c = RGB{40, 40, 40}
		}
		c0, c1, r0, r1 := keyBox(k)
		lum := 0.3*float64(c[0]) + 0.59*float64(c[1]) + 0.11*float64(c[2])
		text := RGB{255, 255, 255}
		if lum > 130 {
			text = RGB{0, 0, 0}
		}
		label := k.Label
		if i == m.cur {
			label = "‹" + label + "›"
		}
		for r := r0; r < r1; r++ {
			for cc := c0; cc < c1; cc++ {
				grid[r][cc] = cell{' ', text, c, true}
			}
		}
		lr, lc := r0, c0+max(0, (c1-c0-len([]rune(label)))/2)
		for j, ch := range []rune(label) {
			if lc+j < c1 {
				g := &grid[lr][lc+j]
				g.r = ch
			}
		}
	}
	var sb strings.Builder
	for _, row := range grid {
		var cur string
		for _, c := range row {
			want := reset
			if c.on {
				want = bgc(c.bg) + fg(c.fg)
			}
			if want != cur {
				sb.WriteString(want)
				cur = want
			}
			sb.WriteRune(c.r)
		}
		sb.WriteString(reset + "\n")
	}
	return sb.String()
}

func (m model) View() string {
	acc := "\x1b[1;38;2;126;100;98m"
	if s := widgetStops(); s != nil {
		acc = "\x1b[1m" + fg(s[1])
	}
	dim := "\x1b[2m"
	var sb strings.Builder
	fw := ""
	if m.info.Version > 0 {
		fw = fmt.Sprintf("fw %.2f", m.info.Version)
	}
	fmt.Fprintf(&sb, "%s▌AETHER%s %slinux · VOICSKY PULSE 96   %s%s\n", acc, reset, dim, fw, reset)
	for i, t := range tabs {
		if i == m.tab {
			fmt.Fprintf(&sb, "%s[ %s ]%s ", acc, t, reset)
		} else {
			fmt.Fprintf(&sb, "%s  %s  %s ", dim, t, reset)
		}
	}
	sb.WriteString("\n\n")
	sb.WriteString(m.keyboard())
	k := Keys[m.cur]
	sw := func(c RGB) string { return bgc(c) + "  " + reset }
	switch m.tab {
	case 0:
		fmt.Fprintf(&sb, "%s┤ colour ├%s palette %s%s%s   brush %s #%02x%02x%02x   (g apply gradient · p palette · c brush · space/click paint key · b solid)\n",
			acc, reset, acc, palList[m.pal], reset, sw(m.brush), m.brush[0], m.brush[1], m.brush[2])
		fmt.Fprintf(&sb, "%s┤ effect ├%s %s%d/%d %s%s   bright %s   speed %s   (e/E effect · +/- bright · [ ] speed · a apply)\n",
			acc, reset, acc, m.eff+1, len(effects), effects[m.eff], reset, bar(m.bri), bar(m.spd))
	case 1:
		layer := []string{"Main layer", "Fn layer"}[m.layer]
		cur := "…"
		if m.keymap != nil {
			cur = fmt.Sprintf("usage 0x%02x", m.keymap[k.Slot*4+2])
			if m.layer == 0 && k.Usage != 0 && byte(k.Usage) != m.keymap[k.Slot*4+2] {
				cur += fmt.Sprintf("  (default 0x%02x)", k.Usage)
			}
		}
		fmt.Fprintf(&sb, "%s┤ remap ├%s %s · key %s%s%s (slot %d) sends %s\n   f switch layer · r remap (type a key name or 0xUSAGE) · x restore default\n", acc, reset, layer, acc, k.Label, reset, k.Slot, cur)
	case 2:
		st := "stopped"
		if m.bg {
			st = acc + "RUNNING in background" + reset
		}
		fmt.Fprintf(&sb, "%s┤ music ├%s %s   palette %s%s%s   style %s%s%s\n   p palette · y style · enter start/stop (a systemd user service: keeps running after you quit)\n",
			acc, reset, st, acc, palList[m.pal], reset, acc, styles[m.sty], reset)
	case 3:
		fmt.Fprintf(&sb, "%s┤ device ├%s firmware %.2f · %s · profile %d\n   b backup everything to ./aether-backup-*.json\n   macros, SOCD and factory reset are intentionally not in the TUI (use the Aether web console)\n",
			acc, reset, m.info.Version, map[bool]string{true: "charging/wired", false: "wired"}[m.info.Charging], m.info.Profile+1)
	}
	if m.edit != "" {
		fmt.Fprintf(&sb, "%s> %s: %s█%s  (enter · esc)\n", acc, m.edit, m.buf, reset)
	} else {
		fmt.Fprintf(&sb, "%s$ %s%s%s\n", acc, reset, m.status, dim+"   tab/arrows/mouse · q quit"+reset)
	}
	return sb.String()
}

func bar(n int) string { return strings.Repeat("■", n) + strings.Repeat("□", max(0, 6-n)) }
