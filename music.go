package main

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"os/exec"
	"strings"
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
	var lv [bands]float64
	peak, t0 := 1e-3, time.Now()
	raw, smp := make([]byte, nSamp*2), make([]float64, nSamp)
	for ctx.Err() == nil {
		if _, err := io.ReadFull(out, raw); err != nil {
			return nil
		}
		for i := range smp {
			smp[i] = float64(int16(binary.LittleEndian.Uint16(raw[i*2:])))
		}
		e, mx := [bands]float64{}, 0.0
		for i, f := range freqs {
			e[i] = goertzel(smp, f)
			mx = math.Max(mx, e[i])
		}
		peak = math.Max(math.Max(peak*0.995, mx), 50) // auto-gain
		for i := range lv {
			lv[i] = math.Max(lv[i]*0.8, math.Min(1, e[i]/peak)) // fast attack, soft decay
		}
		shift := time.Since(t0).Seconds() * 0.05
		colorOf := func(k Key) RGB {
			band := min(bands-1, int(keyX(k)*bands))
			c := pal(keyX(k) + shift)
			if style == "pulse" {
				return mix(RGB{}, c, math.Min(1, lv[band]*1.5))
			}
			if float64(5-k.Row) < lv[band]*6 {
				return c
			}
			return RGB{}
		}
		if err := d.Paint(colorOf); err != nil {
			return err
		}
		if onFrame != nil {
			m := map[int]RGB{}
			for _, k := range Keys {
				m[k.Slot] = colorOf(k)
			}
			onFrame(m)
		}
		time.Sleep(time.Second / time.Duration(fps))
	}
	return nil
}
