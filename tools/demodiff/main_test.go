package main

import (
	"bytes"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"os"
	"path/filepath"
	"testing"
)

// screen is a 64x24 picture: a background with an 8 pixel wide block of
// "text" at x.
func screen(bg color.Color, x int) *image.Paletted { return bar(bg, x, 8) }

// bar is screen with text w pixels wide.
func bar(bg color.Color, x, w int) *image.Paletted {
	m := image.NewPaletted(image.Rect(0, 0, 64, 24), palette.Plan9)
	for py := range 24 {
		for px := range 64 {
			m.Set(px, py, bg)
		}
	}
	for py := 8; py < 16; py++ {
		for px := x; px < x+w; px++ {
			m.Set(px, py, color.White)
		}
	}
	return m
}

// anim is a GIF of frames shown for delays (hundredths of a second).
func anim(t *testing.T, frames []*image.Paletted, delays []int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: frames, Delay: delays}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDiffer(t *testing.T) {
	dark := color.RGBA{30, 30, 46, 255}
	other := color.RGBA{90, 90, 110, 255}
	// dithered is the same screen with one pixel in each 8x8 block a shade
	// off, as another render's palette and dithering leave it.
	dithered := func(x int) *image.Paletted {
		m := screen(dark, x)
		bg := m.ColorIndexAt(0, 0)
		for py := 5; py < 24; py += 8 {
			for px := 3; px < 64; px += 8 {
				if m.ColorIndexAt(px, py) == bg {
					m.Set(px, py, color.RGBA{70, 70, 90, 255})
				}
			}
		}
		return m
	}
	typing := func(x int) *image.Paletted { return screen(dark, x) }

	base := anim(t, []*image.Paletted{typing(1), typing(2), screen(dark, 10), typing(3), screen(dark, 30)}, []int{5, 5, 250, 5, 300})
	for _, c := range []struct {
		name     string
		old, new []byte // old defaults to base
		want     string
	}{
		{"same bytes", nil, base, ""},
		{"other typing frames and timing", nil, anim(t, []*image.Paletted{typing(4), screen(dark, 10), typing(5), typing(6), screen(dark, 30)},
			[]int{7, 240, 4, 4, 320}), ""},
		{"dithered differently", nil, anim(t, []*image.Paletted{dithered(10), dithered(30)}, []int{250, 300}), ""},
		{"a screen changed", nil, anim(t, []*image.Paletted{screen(dark, 10), screen(dark, 40)}, []int{250, 300}), "screen 2 of 2 changed"},
		{"another colour", nil, anim(t, []*image.Paletted{screen(other, 10), screen(other, 30)}, []int{250, 300}), "screen 1 of 2 changed"},
		{"a screen more", nil, anim(t, []*image.Paletted{screen(dark, 10), screen(dark, 30), screen(dark, 50)}, []int{250, 300, 200}),
			"3 screens, was 2"},
		// A 4 pixel bar moves within its 8x8 block: the average stays, the pixels do not.
		{"moved within a block", anim(t, []*image.Paletted{bar(dark, 24, 4)}, []int{300}),
			anim(t, []*image.Paletted{bar(dark, 28, 4)}, []int{300}), "screen 1 of 1 changed"},
	} {
		dir := t.TempDir()
		oldPath, newPath := filepath.Join(dir, "old.gif"), filepath.Join(dir, "new.gif")
		if c.old == nil {
			c.old = base
		}
		if err := os.WriteFile(oldPath, c.old, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(newPath, c.new, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := differ(oldPath, newPath)
		if err != nil || got != c.want {
			t.Errorf("%s: differ = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func TestDifferStill(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b, c := write("a.png", "one"), write("b.png", "one"), write("c.png", "two")
	for _, tc := range []struct {
		oldPath, newPath, want string
	}{
		{a, b, ""},
		{a, c, "the image changed"},
		{filepath.Join(dir, "missing.png"), c, "new image"},
	} {
		if got, err := differ(tc.oldPath, tc.newPath); err != nil || got != tc.want {
			t.Errorf("differ(%s, %s) = %q, %v; want %q", tc.oldPath, tc.newPath, got, err, tc.want)
		}
	}
}
