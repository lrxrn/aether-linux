package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type Palette func(p float64) RGB

func hexRGB(h string) (RGB, error) {
	h = strings.TrimPrefix(strings.TrimSpace(h), "#")
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil || len(h) != 6 {
		return RGB{}, fmt.Errorf("bad colour %q (want RRGGBB)", h)
	}
	return RGB{byte(v >> 16), byte(v >> 8), byte(v)}, nil
}

func rgb2hsv(c RGB) (h, s, v float64) {
	r, g, b := float64(c[0])/255, float64(c[1])/255, float64(c[2])/255
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	v, d := mx, mx-mn
	if mx > 0 {
		s = d / mx
	}
	switch {
	case d == 0:
	case mx == r:
		h = math.Mod((g-b)/d, 6) / 6
	case mx == g:
		h = ((b-r)/d + 2) / 6
	default:
		h = ((r-g)/d + 4) / 6
	}
	return math.Mod(h+1, 1), s, v
}

func hsv2rgb(h, s, v float64) RGB {
	f := func(n float64) byte {
		k := math.Mod(n+h*6, 6)
		return byte(math.Round(255 * v * (1 - s*math.Max(0, math.Min(1, math.Min(k, 4-k))))))
	}
	return RGB{f(5), f(3), f(1)}
}

func mix(a, b RGB, t float64) RGB {
	var o RGB
	for i := range o {
		o[i] = byte(math.Round(float64(a[i]) + (float64(b[i])-float64(a[i]))*t))
	}
	return o
}

// gamma: LED output is linear while colours are meant for a monitor, so mid channels glare and everything looks washed out.
func gamma(c RGB) RGB {
	for i := range c {
		c[i] = byte(math.Round(255 * math.Pow(float64(c[i])/255, 2.0)))
	}
	return c
}

// vivid: pastel colours wash out to white on RGB LEDs, so push saturation up and gamma-correct; keepValue keeps the dim->bright ramp.
func vivid(cols []RGB, keepValue bool) []RGB {
	type hsv struct{ h, s, v float64 }
	hs, top := make([]hsv, len(cols)), 0.0
	for i, c := range cols {
		h, s, v := rgb2hsv(c)
		hs[i] = hsv{h, math.Min(1, s*2.5), v}
		top = math.Max(top, v)
	}
	out := make([]RGB, len(cols))
	for i, x := range hs {
		v := 1.0
		if keepValue && top > 0 {
			v = x.v / top
		}
		out[i] = gamma(hsv2rgb(x.h, x.s, v))
	}
	return out
}

func home(p string) string { return os.Getenv("HOME") + "/" + p }

// widgetStops: the dim -> accent -> light ramp desktop-recolor.sh gives cava, from the wallpaper palette.
func widgetStops() []RGB {
	b, err := os.ReadFile(home(".cache/desktop-widgets/colors.json"))
	if err != nil {
		return nil
	}
	var c struct{ Bg, Fg, Accent string }
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	bg, e1 := hexRGB(c.Bg)
	fg, e2 := hexRGB(c.Fg)
	ac, e3 := hexRGB(c.Accent)
	if e1 != nil || e2 != nil || e3 != nil {
		return nil
	}
	return []RGB{mix(ac, bg, .35), ac, mix(ac, fg, .3)}
}

var tomlColor = regexp.MustCompile(`(?m)^(\w+)\s*=\s*"#([0-9a-fA-F]{6})"`)

func themeColors() []RGB {
	b, err := os.ReadFile(home(".local/state/omarchy/current/theme/colors.toml"))
	if err != nil {
		return nil
	}
	t := map[string]string{}
	for _, m := range tomlColor.FindAllStringSubmatch(string(b), -1) {
		t[m[1]] = m[2]
	}
	var out []RGB
	for _, k := range []string{"accent", "color1", "color2", "color3", "color4", "color5", "color6"} {
		if c, err := hexRGB(t[k]); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// NewPalette: "widgets" (wallpaper, like your cava) | "theme" | "rainbow" | "RRGGBB,RRGGBB,...".
func NewPalette(spec string) (Palette, error) {
	if spec == "rainbow" {
		return func(p float64) RGB { return hsv2rgb(math.Mod(p+1000, 1), 1, 1) }, nil
	}
	var cols []RGB
	switch spec {
	case "widgets":
		if s := widgetStops(); s != nil {
			cols = vivid(s, true)
		} else if t := themeColors(); t != nil {
			cols = vivid(t, false)
		}
	case "theme":
		if t := themeColors(); t != nil {
			cols = vivid(t, false)
		}
	default:
		for _, h := range strings.Split(spec, ",") {
			c, err := hexRGB(h)
			if err != nil {
				return nil, err
			}
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("palette %q has no colours (missing theme files?)", spec)
	}
	if len(cols) == 1 {
		return func(float64) RGB { return cols[0] }, nil
	}
	return func(p float64) RGB { // bounces back and forth, so a drifting gradient has no seam
		p = math.Mod(p+1000, 1) * 2
		if p > 1 {
			p = 2 - p
		}
		x := p * float64(len(cols)-1)
		i := min(int(x), len(cols)-2)
		return mix(cols[i], cols[i+1], x-float64(i))
	}, nil
}

func keyX(k Key) float64 { return float64(k.X+k.W/2) / 1172 }
