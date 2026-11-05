package main

import "testing"

func TestHexAndPalette(t *testing.T) {
	if c, err := hexRGB("#ff8000"); err != nil || c != (RGB{255, 128, 0}) {
		t.Fatalf("hexRGB: %v %v", c, err)
	}
	if _, err := hexRGB("zzz"); err == nil {
		t.Fatal("bad hex accepted")
	}
	p, err := NewPalette("ff0000,0000ff")
	if err != nil {
		t.Fatal(err)
	}
	if p(0) != (RGB{255, 0, 0}) || p(0.5) != (RGB{0, 0, 255}) || p(1) != p(0) { // bounces: no seam at the wrap
		t.Fatalf("gradient ends wrong: %v %v %v", p(0), p(0.5), p(1))
	}
	g := vivid([]RGB{{151, 159, 236}}, false)[0] // pastel -> saturated (hue kept: still blue-dominant)
	if _, s, _ := rgb2hsv(g); s < 0.9 || g[2] <= g[0] {
		t.Fatalf("vivid left it washed out: %v (sat %.2f)", g, s)
	}
}

func TestLayoutSlotsFit(t *testing.T) {
	seen := map[int]bool{}
	for _, k := range Keys {
		if k.Slot < 0 || k.Slot > 127 || seen[k.Slot] {
			t.Fatalf("bad or duplicate slot %d", k.Slot)
		}
		seen[k.Slot] = true
	}
}
