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
	"github.com/charmbracelet/x/ansi"
)

// btop-style terminal UI: one screen of rounded, titled boxes (keyboard, lighting, music, key). Every device operation
// opens the keyboard only briefly, so the music service can own it the rest of the time.

var (
	palList = []string{"widgets", "theme", "rainbow"}
	effects = []string{"Static Bright", "Single Point On", "Single Point Off", "Starry Sky", "Snowfall", "Floral Competition",
		"Dynamic Breathing", "Spectrum Cycle", "Color Fountain", "Colorful Interchange", "Flowing with the Waves", "Turning Peaks",
		"One Touch to Fire", "Two Birds with One Stone", "Ripples Spread", "Endless Flow", "Layered Mountains", "Gentle Rain and Wind", "Back and Forth"}
	styles = []string{"bars", "pulse"}
)

const (
	rowH   = 3 // text rows per key row (2 coloured + 1 gap)
	kbTop  = 2 // screen row of the first keyboard row (header line, box border)
	kbLeft = 2 // screen column of the first keyboard column (box border + padding)
)

// ppc: layout pixels per terminal column; widened on narrow terminals so the whole board still fits.
var ppc = 10

func fitScale(w int) {
	switch {
	case w == 0 || w >= 122:
		ppc = 10
	case w >= 104:
		ppc = 12
	default:
		ppc = 14
	}
}

type model struct {
	cur, layer, eff, bri, spd, pal, sty int
	brush                               RGB
	colors                              map[int]RGB
	keymap                              []byte // current layer, 512 bytes
	info                                Info
	status                              string
	edit                                string // "" | "brush" | "remap"
	buf                                 string
	bg                                  bool
	saved                               prefs
	w, h                                int
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
	ledMsg    struct{ mode, bri, spd int }
)

func indexOf(list []string, s string, def int) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return def
}

func runTUI() {
	m := model{colors: map[int]RGB{}, bri: 6, spd: 2, brush: RGB{255, 90, 90}, status: "ready"}
	// Start from what was chosen last time, and from what the music service is really running with.
	p := loadPrefs()
	m.pal, m.sty = indexOf(palList, p.Palette, 0), indexOf(styles, p.Style, 0)
	if c, err := hexRGB(p.Brush); err == nil {
		m.brush = c
	}
	if p.Bright > 0 {
		m.bri = p.Bright
	}
	if pal, sty, br, ok := runningMusic(); ok && bgActive() {
		m.pal, m.sty = indexOf(palList, pal, m.pal), indexOf(styles, sty, m.sty)
		if br >= 0 {
			m.bri = br
		}
		m.status = "music service running with " + pal + " / " + sty
	}
	m.saved = m.prefs()
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		die(err)
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(pollCmd(), tickCmd(), initCmd(), keymapCmd(false, true))
}

func (m model) prefs() prefs {
	return prefs{palList[m.pal], styles[m.sty], fmt.Sprintf("%02x%02x%02x", m.brush[0], m.brush[1], m.brush[2]), m.bri}
}

// initCmd reads the board's current effect, brightness and speed so "apply" never resets what is set.
func initCmd() tea.Cmd {
	return func() tea.Msg {
		var d *Dev
		var err error
		for i := 0; i < 8; i++ { // the first poll may hold the lock for a moment
			if d, err = Open(); err != ErrBusy {
				break
			}
			time.Sleep(120 * time.Millisecond)
		}
		if err != nil {
			return nil // the music service owns it: keep the unit file's values
		}
		defer d.Close()
		e, err := d.Read(cGetLed, 16)
		if err != nil {
			return nil
		}
		return ledMsg{mode: int(e[0]), bri: int(e[9]), spd: int(e[10])}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(700*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// with opens the keyboard for one operation; ErrBusy while the music service owns it.
func with(f func(*Dev) error, ok string) tea.Cmd {
	return func() tea.Msg {
		d, err := Open()
		if err == ErrBusy {
			return doneMsg{err: fmt.Errorf("the music service owns the keyboard: stop it first (m)")}
		} else if err != nil {
			return doneMsg{err: err}
		}
		defer d.Close()
		return doneMsg{ok: ok, err: f(d)}
	}
}

func pollCmd() tea.Cmd {
	return func() tea.Msg {
		if f := readFrame(); f != nil { // fresh frame file = the music service is running (cheaper than asking systemd)
			return liveMsg{bg: true, c: f}
		}
		if bgActive() {
			return liveMsg{bg: true}
		}
		d, err := Open()
		if err == ErrBusy {
			return nil // someone else (a button press) has it this instant: skip this poll quietly
		} else if err != nil {
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

func keymapCmd(fn, quiet bool) tea.Cmd {
	return func() tea.Msg {
		d, err := Open()
		if err == ErrBusy {
			if quiet {
				return nil
			}
			return doneMsg{err: fmt.Errorf("the music service owns the keyboard: stop it first (m)")}
		} else if err != nil {
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

func (m model) toggleMusic() tea.Cmd {
	if bgActive() {
		return func() tea.Msg {
			return doneMsg{ok: "music stopped", err: exec.Command("systemctl", "--user", "stop", unitName).Run()}
		}
	}
	pal, sty, bri := palList[m.pal], styles[m.sty], m.bri
	return func() tea.Msg {
		return doneMsg{ok: "music running in the background, you can quit the TUI", err: bgStart(pal, sty, bri)}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = x.Width, x.Height
		fitScale(x.Width)
	case tickMsg:
		next := 700 * time.Millisecond
		if m.bg {
			next = 120 * time.Millisecond // music service running: follow its frames closely
		}
		return m, tea.Batch(pollCmd(), tea.Tick(next, func(time.Time) tea.Msg { return tickMsg{} }))
	case liveMsg:
		m.bg = x.bg
		if !x.bg {
			m.colors, m.info = x.c, x.info
		} else if x.c != nil {
			m.colors = x.c
		}
	case doneMsg:
		if x.err != nil {
			m.status = "error: " + x.err.Error()
		} else if x.ok != "" {
			m.status = x.ok
		}
		return m, pollCmd()
	case ledMsg:
		if x.mode >= 1 && x.mode <= len(effects) {
			m.eff = x.mode - 1
		}
		if x.bri <= 6 {
			m.bri = x.bri
		}
		if x.spd <= 5 {
			m.spd = x.spd
		}
		m.saved = m.prefs() // values read from the board are not "choices": do not write them to the prefs file
	case keymapMsg:
		m.keymap = x.cur
	case tea.MouseMsg:
		if x.Action == tea.MouseActionPress && x.Button == tea.MouseButtonLeft {
			if x.Y == 0 { // header: the music status on the right is a button
				if l, r := m.header(); x.X >= m.w-ansi.StringWidth(r) && ansi.StringWidth(l)+ansi.StringWidth(r) <= m.w {
					return m, m.toggleMusic()
				}
			}
			for i, k := range Keys {
				c0, c1, r0, r1 := keyBox(k)
				if x.X >= kbLeft+c0 && x.X < kbLeft+c1 && x.Y >= kbTop+r0 && x.Y < kbTop+r1 {
					m.cur = i
					return m, m.paintKey()
				}
			}
		}
	case tea.KeyMsg:
		nm, cmd := m.key(x)
		if mm, ok := nm.(model); ok {
			if p := mm.prefs(); p != mm.saved {
				savePrefs(p)
				mm.saved = p
			}
			nm = mm
		}
		return nm, cmd
	}
	return m, nil
}

func keyBox(k Key) (c0, c1, r0, r1 int) {
	c0, c1 = k.X/ppc, (k.X+k.W)/ppc-1
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
	case "left", "h":
		m.cur = m.move(-1, 0)
	case "right", "l":
		m.cur = m.move(1, 0)
	case "up", "k":
		m.cur = m.move(0, -1)
	case "down", "j":
		m.cur = m.move(0, 1)
	case "m":
		return m, m.toggleMusic()
	case "p":
		m.pal = (m.pal + 1) % len(palList)
	case "y":
		m.sty = (m.sty + 1) % len(styles)
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
	case "f":
		m.layer ^= 1
		return m, keymapCmd(m.layer == 1, false)
	case "r":
		m.edit, m.buf = "remap", ""
	case "x":
		if m.layer == 0 && Keys[m.cur].Usage != 0 { // factory layout from Aether (the board's own default-keymap read returns zeros on 1.07)
			return m, m.writeKey(byte(Keys[m.cur].Usage), "restored default")
		}
		m.status = "no factory default known for the Fn layer"
	case "B":
		return m, with(func(d *Dev) error { return backupFile(d) }, "backup saved to ./aether-backup-*.json")
	}
	return m, nil
}

func (m model) writeKey(usage byte, ok string) tea.Cmd {
	slot, fn := Keys[m.cur].Slot, m.layer == 1
	return tea.Sequence(with(func(d *Dev) error { return setKey(d, fn, slot, usage) }, ok), keymapCmd(fn, false))
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

// ---- rendering (btop style: rounded boxes with titles in the border) ------------------------------------------

func fg(c RGB) string  { return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2]) }
func bgc(c RGB) string { return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", c[0], c[1], c[2]) }

const (
	reset = "\x1b[0m"
	bold  = "\x1b[1m"
	dim   = "\x1b[2m"
)

var (
	green = RGB{140, 205, 140}
	red   = RGB{225, 110, 110}
)

// uiColors: border, highlight and plain text colours from the wallpaper palette (same source as the desktop widgets).
func uiColors() (border, hi, text RGB) {
	if bg, f, ac, ok := widgetBase(); ok {
		return mix(ac, bg, .1), mix(ac, f, .45), mix(f, bg, .15)
	}
	return RGB{150, 120, 118}, RGB{205, 185, 180}, RGB{200, 195, 195}
}

func pad(s string, w int) string {
	if n := ansi.StringWidth(s); n > w {
		return ansi.Truncate(s, w, "…")
	} else {
		return s + strings.Repeat(" ", w-n)
	}
}

// box draws a rounded box of total width w around lines, with the title set into the top border.
func box(title string, lines []string, w, h int) []string {
	bd, hi, _ := uiColors()
	b := func(s string) string { return fg(bd) + s + reset }
	inner := w - 2
	top := b("╭─┤ ") + bold + fg(hi) + title + reset + b(" ├")
	top += b(strings.Repeat("─", max(0, w-2-(ansi.StringWidth(title)+6)+1)) + "╮")
	out := []string{top}
	for i := 0; i < h; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, b("│")+pad(" "+l, inner)+b("│"))
	}
	return append(out, b("╰"+strings.Repeat("─", inner)+"╯"))
}

func bar(n, of int) string { return strings.Repeat("■", n) + strings.Repeat("□", max(0, of-n)) }

// strip previews a palette as a row of coloured cells.
func strip(name string, n int) string {
	p, err := NewPalette(name)
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(bgc(p(float64(i)/float64(max(1, n-1))/2)) + " ")
	}
	return sb.String() + reset
}

// meter draws two blocks per frequency band from how many keys of that column are lit in the music frame.
func (m model) meter() string {
	const blocks = "▁▂▃▄▅▆▇█"
	var cnt [bands]int
	for _, k := range Keys {
		if c := m.colors[k.Slot]; max(c[0], c[1], c[2]) > 0 && k.Row > 0 {
			cnt[min(bands-1, int(keyX(k)*bands))]++
		}
	}
	var sb strings.Builder
	for _, c := range cnt {
		sb.WriteString(strings.Repeat(string([]rune(blocks)[min(7, c*8/6)]), 2))
	}
	return sb.String()
}

func (m model) header() (left, right string) {
	_, hi, _ := uiColors()
	conn := "wired"
	if m.info.Charging {
		conn = "wired"
	}
	left = bold + fg(hi) + " ▌aetherctl " + reset + dim + "VOICSKY PULSE 96"
	if m.info.Version > 0 {
		left += fmt.Sprintf("  ·  fw %.2f  ·  %s", m.info.Version, conn)
	}
	left += reset
	if m.bg {
		right = fg(green) + bold + "♪ music ● running" + reset + dim + " [m] " + reset
	} else {
		right = dim + "♪ music ○ stopped [m] " + reset
	}
	return
}

type cell struct {
	r        rune
	fg, bg   RGB
	on, bold bool
}

func (m model) keyboardLines() []string {
	cols, rows := 1172/ppc+1, 6*rowH
	grid := make([][]cell, rows)
	for i := range grid {
		grid[i] = make([]cell, cols)
		for j := range grid[i] {
			grid[i][j].r = ' '
		}
	}
	for i, k := range Keys {
		c := m.colors[k.Slot]
		c0, c1, r0, r1 := keyBox(k)
		lum := 0.3*float64(c[0]) + 0.59*float64(c[1]) + 0.11*float64(c[2])
		text := RGB{255, 255, 255}
		if lum > 130 {
			text = RGB{0, 0, 0}
		}
		sel := i == m.cur
		label := k.Label
		if sel && len([]rune(label))+2 <= c1-c0 {
			label = "‹" + label + "›"
		}
		for r := r0; r < r1; r++ {
			for cc := c0; cc < c1; cc++ {
				grid[r][cc] = cell{' ', text, c, true, false}
			}
		}
		lc := c0 + max(0, (c1-c0-len([]rune(label)))/2)
		for j, ch := range []rune(label) {
			if lc+j < c1 {
				grid[r0][lc+j].r, grid[r0][lc+j].bold = ch, sel
			}
		}
		if sel { // underline the second row of the selected key so it stands out whatever its colour
			for cc := c0; cc < c1; cc++ {
				grid[r0+1][cc].r, grid[r0+1][cc].fg = '▔', text
			}
		}
	}
	lines := make([]string, rows)
	for ri, row := range grid {
		var sb strings.Builder
		cur := ""
		for _, c := range row {
			want := reset
			if c.on {
				want = bgc(c.bg) + fg(c.fg)
				if c.bold {
					want += bold
				}
			}
			if want != cur {
				sb.WriteString(want)
				cur = want
			}
			sb.WriteRune(c.r)
		}
		lines[ri] = sb.String() + reset
	}
	return lines
}

func (m model) View() string {
	bd, hi, text := uiColors()
	_ = bd
	key := func(k string) string { return bold + fg(hi) + k + reset + fg(text) }
	kbW := 1172/ppc + 1
	total := kbW + 4
	var sb strings.Builder

	l, r := m.header()
	sb.WriteString(l + strings.Repeat(" ", max(1, total-ansi.StringWidth(l)-ansi.StringWidth(r))) + r + "\n")

	// keyboard
	for _, ln := range box("keyboard · "+[]string{"main", "fn"}[m.layer]+" layer", m.keyboardLines(), total, 6*rowH) {
		sb.WriteString(ln + "\n")
	}

	// three info boxes
	bw := (total - 2) / 3
	inner := bw - 3
	k := Keys[m.cur]
	pal := palList[m.pal]
	light := []string{
		fg(text) + "palette  " + bold + fg(hi) + "‹" + pal + "›" + reset + fg(text) + "  " + key("p"),
		"preview  " + strip(pal, max(8, inner-9)),
		fmt.Sprintf("effect   %s%s%s %s", bold+fg(hi), effects[m.eff], reset+fg(text), key("e/E")),
		fmt.Sprintf("bright   %s %s   speed %s", bar(m.bri, 6), key("+/-"), bar(m.spd, 5)),
		fmt.Sprintf("brush    %s  #%02x%02x%02x %s", bgc(m.brush)+"    "+reset, m.brush[0], m.brush[1], m.brush[2], key("c")),
		"",
		key("g") + " gradient  " + key("a") + " apply  " + key("b") + " solid",
		key("space") + "/click paint selected key",
	}
	status, title := dim+"○ stopped"+reset+fg(text), "music ○"
	if m.bg {
		status, title = fg(green)+bold+"● RUNNING"+reset+fg(text), "music ●"
	}
	mus := []string{
		"service  " + status + "  " + key("m") + " " + map[bool]string{true: "stop", false: "start"}[m.bg],
		fmt.Sprintf("palette  %s%s%s  %s", bold+fg(hi), pal, reset+fg(text), key("p")),
		fmt.Sprintf("style    %s%s%s  %s", bold+fg(hi), styles[m.sty], reset+fg(text), key("y")),
		fmt.Sprintf("bright   %s %s", bar(m.bri, 6), key("+/-")),
		"",
	}
	if m.bg {
		mus = append(mus, "levels   "+fg(hi)+m.meter()+reset)
	} else {
		mus = append(mus, dim+"levels   (start the service)"+reset)
	}
	mus = append(mus, dim+"runs in the background: you can quit"+reset)
	usage := "…"
	if m.keymap != nil {
		usage = fmt.Sprintf("0x%02x", m.keymap[k.Slot*4+2])
		if m.layer == 0 && k.Usage != 0 && byte(k.Usage) != m.keymap[k.Slot*4+2] {
			usage += fmt.Sprintf(" (factory 0x%02x)", k.Usage)
		}
	} else if m.layer == 0 && k.Usage != 0 {
		usage = fmt.Sprintf("0x%02x (factory)", k.Usage)
	}
	conn := ""
	if m.info.Version > 0 {
		conn = fmt.Sprintf("fw %.2f · profile %d", m.info.Version, m.info.Profile+1)
	}
	keyb := []string{
		fmt.Sprintf("key      %s%s%s  (slot %d)", bold+fg(hi), k.Label, reset+fg(text), k.Slot),
		"sends    usage " + usage,
		"layer    " + []string{"main", "fn"}[m.layer] + "  " + key("f"),
		"",
		key("r") + " remap  " + key("x") + " restore factory key",
		key("B") + " backup all (json, in cwd)",
		"",
		dim + conn + reset,
	}
	h := max(len(light), max(len(mus), len(keyb)))
	cols := [][]string{box("lighting", light, bw, h), box(title, mus, bw, h), box("key", keyb, total-2*bw-2, h)}
	for i := range cols[0] {
		sb.WriteString(cols[0][i] + " " + cols[1][i] + " " + cols[2][i] + "\n")
	}

	// status / prompt / hints
	if m.edit != "" {
		sb.WriteString(fmt.Sprintf(" %s%s>%s %s: %s█  %s(enter · esc)%s\n", bold, fg(hi), reset, m.edit, m.buf, dim, reset))
	} else {
		col := text
		if strings.HasPrefix(m.status, "error") {
			col = red
		}
		sb.WriteString(" " + fg(hi) + "$ " + reset + fg(col) + m.status + reset + "\n")
	}
	sb.WriteString(dim + " arrows/hjkl/click select · space paint · m music · q quit" + reset + "\n")
	return sb.String()
}
