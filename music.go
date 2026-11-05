package main

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	rate  = 22048
	nSamp = 512
	bands = 17
)

func goertzel(buf []float64, freq float64) float64 {
	k := 2 * math.Cos(2*math.Pi*freq/rate)
	var s1, s2 float64
	for _, v := range buf {
		s1, s2 = v+k*s1-s2, s1
	}
	return math.Sqrt(math.Max(0, s1*s1+s2*s2-k*s1*s2)) / float64(len(buf))
}

// Music lights the keys from the default audio output until ctx ends, then leaves a plain gradient (not a frozen frame).
// style "bars": 17 log-spaced bands rise through the key rows; "pulse": whole board brightens with the band under each key.
func Music(ctx context.Context, d *Dev, pal Palette, style string, fps int, onFrame func(map[int]RGB)) error {
	sink, err := exec.Command("pactl", "get-default-sink").Output()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "parec", "-d", strings.TrimSpace(string(sink))+".monitor", "--format=s16le",
		"--rate=22048", "--channels=1", "--latency-msec=20")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer cmd.Wait()
	defer func() { d.Paint(func(k Key) RGB { return pal(keyX(k)) }) }()

	if err := d.SetMode(d.CustomMode(), nil, -1, -1); err != nil {
		return err
	}
	freqs := make([]float64, bands)
	for i := range freqs {
		freqs[i] = 40 * math.Pow(7000.0/40, float64(i)/(bands-1))
	}
	// Read audio on its own goroutine and only ever analyse the newest window: if painting is slower than audio arrives,
	// a plain read loop would fall further and further behind (seconds of delay, and flicker after the music stops).
	var mu sync.Mutex
	win := make([]float64, nSamp)
	go func() {
		blk := make([]byte, nSamp) // 256 samples
		for {
			if _, err := io.ReadFull(out, blk); err != nil {
				return
			}
			mu.Lock()
			copy(win, win[nSamp/2:])
			for i := 0; i < nSamp/2; i++ {
				win[nSamp/2+i] = float64(int16(binary.LittleEndian.Uint16(blk[i*2:])))
			}
			mu.Unlock()
		}
	}()
	var lv [bands]float64
	peak, t0, smp := 1e-3, time.Now(), make([]float64, nSamp)
	tick := time.NewTicker(time.Second / time.Duration(fps))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		mu.Lock()
		copy(smp, win)
		mu.Unlock()
		e, mx := [bands]float64{}, 0.0
		for i, f := range freqs {
			e[i] = goertzel(smp, f)
			mx = math.Max(mx, e[i])
		}
		peak = math.Max(math.Max(peak*0.998, mx), 50) // auto-gain, slow release so quiet passages do not blow up
		var tgt [bands]float64
		for i := range tgt { // blend neighbouring bands, then gate out tiny levels
			l, r := e[max(0, i-1)], e[min(bands-1, i+1)]
			tgt[i] = math.Min(1, (0.25*l+0.5*e[i]+0.25*r)/peak)
			if tgt[i] < 0.05 {
				tgt[i] = 0
			}
		}
		for i := range lv { // rise quickly but not instantly, fall slowly: no more snapping on/off
			if tgt[i] > lv[i] {
				lv[i] += (tgt[i] - lv[i]) * 0.55
			} else {
				lv[i] = math.Max(tgt[i], lv[i]*0.9)
			}
		}
		shift := math.Floor(time.Since(t0).Seconds()*0.05*32) / 32 // stepped, so lit keys do not change colour (and need resending) every frame
		colorOf := func(k Key) RGB {
			band := min(bands-1, int(keyX(k)*bands))
			c := pal(keyX(k) + shift)
			level := math.Min(1, lv[band]*1.4) // headroom: loud music should reach the top row
			var b float64
			if style == "pulse" {
				b = level
			} else { // anti-aliased bar: the top key of the bar fades in/out instead of switching
				b = math.Max(0, math.Min(1, level*6-float64(5-k.Row)))
			}
			b = 0.1 + 0.9*b                         // faint idle glow, so silence does not look like "off"
			return mix(RGB{}, c, math.Round(b*6)/6) // coarse steps: fewer changed packets per frame
		}
		if err := d.Paint(colorOf); err != nil {
			return err
		}
		m := map[int]RGB{}
		for _, k := range Keys {
			m[k.Slot] = colorOf(k)
		}
		writeFrame(m)
		if onFrame != nil {
			onFrame(m)
		}
	}
}

// writeFrame publishes what the keys show (128 x rgb) so the TUI can display it while this process owns the keyboard.
func writeFrame(m map[int]RGB) {
	b := make([]byte, 384)
	for s, c := range m {
		copy(b[s*3:], c[:])
	}
	tmp := framePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, framePath())
	}
}

func framePath() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d + "/aetherctl.frame"
	}
	return os.TempDir() + "/aetherctl.frame"
}

func readFrame() map[int]RGB {
	st, err := os.Stat(framePath())
	if err != nil || time.Since(st.ModTime()) > 2*time.Second {
		return nil
	}
	b, err := os.ReadFile(framePath())
	if err != nil || len(b) != 384 {
		return nil
	}
	m := map[int]RGB{}
	for s := 0; s < 128; s++ {
		m[s] = RGB{b[s*3], b[s*3+1], b[s*3+2]}
	}
	return m
}
