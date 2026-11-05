package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func die(err error) {
	fmt.Fprintln(os.Stderr, "aetherctl:", err)
	os.Exit(1)
}

func must[T any](v T, err error) T {
	if err != nil {
		die(err)
	}
	return v
}

const usage = `aetherctl - VOICSKY PULSE 96 control (TUI by default)
  aetherctl                     terminal UI
  aetherctl info | led | keymap | backup > file.json
  aetherctl color [widgets|theme|rainbow|RRGGBB,RRGGBB...]   per-key gradient
  aetherctl setled [-mode N] [-rgb RRGGBB] [-bright 0-5] [-speed 0-5]
  aetherctl setkey [-fn] SLOT USAGE   remap a key slot to a HID usage (e.g. 0x29)
  aetherctl music [-palette P] [-style bars|pulse] [-fps 12]   audio-reactive (Ctrl-C stops)
  aetherctl bg start|stop|status [-palette P] [-style S]       run music as a systemd user service`

func main() {
	cmd := "tui"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	args := []string{}
	if len(os.Args) > 2 {
		args = os.Args[2:]
	}
	if cmd == "bg" {
		bg(args)
		return
	}
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		fmt.Println(usage)
		return
	}
	if cmd == "tui" {
		runTUI()
		return
	}
	d := must(Open())
	defer d.Close()
	switch cmd {
	case "info":
		fmt.Printf("%+v\n", d.Info)
	case "led":
		e := must(d.Read(cGetLed, 16))
		fmt.Printf("mode=%d rgb=%02x%02x%02x brightness=%d speed=%d direction=%d\n", e[0], e[1], e[2], e[3], e[9], e[10], e[11])
	case "setled":
		fs := flag.NewFlagSet("setled", flag.ExitOnError)
		mode, rgb := fs.Int("mode", -1, "effect 1-19, or the custom id"), fs.String("rgb", "", "RRGGBB")
		bright, speed := fs.Int("bright", -1, "0-5"), fs.Int("speed", -1, "0-5")
		fs.Parse(args)
		var c *RGB
		if *rgb != "" {
			v := must(hexRGB(*rgb))
			c = &v
		}
		m := byte(*mode)
		if *mode < 0 {
			e := must(d.Read(cGetLed, 16))
			m = e[0]
		}
		if err := d.SetMode(m, c, *bright, *speed); err != nil {
			die(err)
		}
	case "color":
		spec := "widgets"
		if len(args) > 0 {
			spec = args[0]
		}
		pal := must(NewPalette(spec))
		if err := d.SetMode(d.CustomMode(), nil, -1, -1); err != nil {
			die(err)
		}
		if err := d.Paint(func(k Key) RGB { return pal(keyX(k)) }); err != nil {
			die(err)
		}
	case "keymap":
		e := must(d.Read(cGetKey, 512))
		for i := 0; i < 128; i++ {
			if t := e[i*4]; t != 0 {
				fmt.Printf("slot %3d: type=%d usage=0x%02x\n", i, t, e[i*4+2])
			}
		}
	case "setkey":
		fs := flag.NewFlagSet("setkey", flag.ExitOnError)
		fn := fs.Bool("fn", false, "edit the Fn layer")
		fs.Parse(args)
		if fs.NArg() != 2 {
			die(fmt.Errorf("usage: aetherctl setkey [-fn] SLOT USAGE"))
		}
		slot, usage := must(strconv.Atoi(fs.Arg(0))), must(strconv.ParseInt(fs.Arg(1), 0, 32))
		if slot < 0 || slot > 127 {
			die(fmt.Errorf("slot 0-127"))
		}
		if err := setKey(d, *fn, slot, byte(usage)); err != nil {
			die(err)
		}
	case "backup":
		out := map[string]string{}
		for n, r := range map[string][2]int{"settings": {cGetSettings, 56}, "keymap": {cGetKey, 512}, "fn": {cGetFn, 512}, "led": {cGetLed, 16}, "custom": {cGetCustom, 512}} {
			out[n] = hex.EncodeToString(must(d.Read(byte(r[0]), r[1])))
		}
		out["firmware"] = fmt.Sprint(d.Info.Version)
		json.NewEncoder(os.Stdout).Encode(out)
	case "music":
		fs := flag.NewFlagSet("music", flag.ExitOnError)
		p, style, fps := fs.String("palette", "widgets", ""), fs.String("style", "bars", "bars|pulse"), fs.Int("fps", 30, "")
		bright := fs.Int("bright", -1, "board brightness 0-6 (default: leave as is)")
		fs.Parse(args)
		pal := must(NewPalette(*p))
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := Music(ctx, d, MusicOpts{Pal: pal, Style: *style, FPS: *fps, Drift: drift(*p), Bright: *bright}, nil); err != nil {
			die(err)
		}
	default:
		fmt.Println(usage)
		os.Exit(2)
	}
}

// drift: only rainbow slides along the keyboard; a drifting brown gradient just makes lit keys change colour.
func drift(palette string) float64 {
	if palette == "rainbow" {
		return 0.02
	}
	return 0
}

func setKey(d *Dev, fn bool, slot int, usage byte) error {
	get, set := byte(cGetKey), byte(cSetKey)
	if fn {
		get, set = cGetFn, cSetFn
	}
	e, err := d.Read(get, 512)
	if err != nil {
		return err
	}
	copy(e[slot*4:], []byte{2, 0, usage, 0})
	return d.Write(set, e)
}

const unitName = "aether-music"

func sysctl(a ...string) error {
	return exec.Command("systemctl", append([]string{"--user"}, a...)...).Run()
}

func bgActive() bool {
	return exec.Command("systemctl", "--user", "is-active", "--quiet", unitName).Run() == nil
}

func bgStart(palette, style string, bright int) error {
	exe, _ := os.Executable()
	dir := home(".config/systemd/user")
	os.MkdirAll(dir, 0o755)
	unit := fmt.Sprintf("[Unit]\nDescription=Aether music-reactive keyboard lighting\n\n[Service]\nExecStart=%s music -palette %s -style %s -bright %d\nRestart=on-failure\nRestartSec=3\n\n[Install]\nWantedBy=default.target\n", exe, palette, style, bright)
	if err := os.WriteFile(dir+"/"+unitName+".service", []byte(unit), 0o644); err != nil {
		return err
	}
	if err := sysctl("daemon-reload"); err != nil {
		return err
	}
	return sysctl("restart", unitName)
}

func bg(args []string) {
	if len(args) == 0 {
		die(fmt.Errorf("usage: aetherctl bg start|stop|status"))
	}
	fs := flag.NewFlagSet("bg", flag.ExitOnError)
	p, style := fs.String("palette", "widgets", ""), fs.String("style", "bars", "")
	bright := fs.Int("bright", -1, "board brightness 0-6 (default: leave as is)")
	fs.Parse(args[1:])
	switch args[0] {
	case "start":
		if _, err := NewPalette(*p); err != nil {
			die(err)
		}
		if err := bgStart(*p, *style, *bright); err != nil {
			die(err)
		}
		time.Sleep(300 * time.Millisecond)
	case "stop":
		if err := sysctl("stop", unitName); err != nil {
			die(err)
		}
	case "status":
		c := exec.Command("systemctl", "--user", "status", "--no-pager", unitName)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		c.Run()
	default:
		die(fmt.Errorf("bg start|stop|status"))
	}
}
