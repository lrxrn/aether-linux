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

// minOn is the dimmest a lit key may be; below it the key is fully off.
const minOn = 0.4

// minLevel is the dimmest brightest-channel value a lit key may have (0-255).
const minLevel = 56

// holdFor is the minimum time between small brightness changes of one key.
const holdFor = 140 * time.Millisecond

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
// MusicOpts configures Music. Drift is how fast the palette slides along the keyboard (0 = fixed; only worth it for rainbow).
// Bright is the board brightness to set (-1 = leave it).
type MusicOpts struct {
	Pal    Palette
	Style  string
	FPS    int
	Drift  float64
	Bright int
}

func Music(ctx context.Context, d *Dev, o MusicOpts, onFrame func(map[int]RGB)) error {
	pal, style, fps := o.Pal, o.Style, o.FPS
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

	if err := d.SetMode(d.CustomMode(), nil, o.Bright, -1); err != nil {
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
	var held [128]float64
	var since [128]time.Time
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
		shift := math.Floor(time.Since(t0).Seconds()*o.Drift*64) / 64 // stepped, so lit keys do not change colour every frame
		now := time.Now()
		frame := map[int]RGB{}
		for _, k := range Keys {
			band := min(bands-1, int(keyX(k)*bands))
			level := math.Min(1, lv[band]*1.4) // headroom: loud music should reach the top row
			raw := level
			if style != "pulse" { // anti-aliased bar: the top key of the bar fades in/out instead of switching
				raw = math.Max(0, math.Min(1, level*6-float64(5-k.Row)))
			}
			// Dim levels flicker on the LEDs (low PWM duty): a key is off or at least minOn bright, in coarse steps.
			b := math.Round(raw*5) / 5
			if b < minOn {
				b = 0
			}
			// Per key: a dead band around the current level (ignore wobble near a step) and a minimum hold time, so a key
			// that is lit does not twitch between two levels.
			if h := held[k.Slot]; b != h {
				if math.Abs(raw-h) < 0.3 || (now.Sub(since[k.Slot]) < holdFor && math.Abs(b-h) < 0.5) {
					b = h
				} else {
					held[k.Slot], since[k.Slot] = b, now
				}
			}
			c := mix(RGB{}, pal(keyX(k)+shift), b)
			if max(c[0], c[1], c[2]) < minLevel { // the palette's dark end can still land in the flicker zone
				c = RGB{}
			}
			frame[k.Slot] = c
		}
		if err := d.Paint(func(k Key) RGB { return frame[k.Slot] }); err != nil {
			return err
		}
		m := frame
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
