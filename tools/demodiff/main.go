// Command demodiff tells whether a re-rendered demo image shows the same
// thing as the committed one, so `just demo` replaces only images that
// changed. It exits 0 when they look the same and 1 when they differ.
//
//	go run ./tools/demodiff docs/img/search.gif /tmp/new/docs/img/search.gif
//
// PNG screenshots come out byte for byte the same when nothing changed. GIFs
// never do: frame timing varies from run to run, and each render gets its own
// palette and dithering. So a GIF is compared by its screens, the frames that
// stay up for a while (the output after a command, leaving out typing and
// progress bars), each averaged over small blocks so dithering cancels out.
//
// It is a heuristic that errs towards "changed". A GIF whose screens depend
// on timing (downloads running behind them) can come out with a screen more
// or less; mark it "# live:" in its tape (see tools/demo.sh).
package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"os"
	"path/filepath"
)

// minScreen is how long a frame must stay up to count as a screen, in
// hundredths of a second. The tapes sleep a few seconds after each command.
const minScreen = 100

// Screens are compared block x block pixels at a time. Measured on the
// search, collections and browse tapes, two renders of the same screen differ
// by at most 1.7 levels in a block's average colour, and by at most 9.4 in its
// mean absolute difference; a changed character moves the average by 3 to 30
// or more. The absolute measure catches ink that moved within a block while
// its average stayed put.
const (
	block       = 8
	maxMean     = 2.0  // how far a block's average colour may move
	maxAbsolute = 16.0 // how far its pixels may move, on average
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: demodiff OLD NEW")
		os.Exit(2)
	}
	why, err := differ(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if why != "" {
		fmt.Println(why)
		os.Exit(1)
	}
}

// differ says how old and new differ, or "" when they look the same.
func differ(oldPath, newPath string) (string, error) {
	a, err := os.ReadFile(oldPath)
	if errors.Is(err, os.ErrNotExist) {
		return "new image", nil
	}
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(newPath)
	if err != nil {
		return "", err
	}
	if bytes.Equal(a, b) {
		return "", nil
	}
	if filepath.Ext(newPath) != ".gif" {
		return "the image changed", nil
	}
	sa, err := screens(a)
	if err != nil {
		return "", fmt.Errorf("%s: %w", oldPath, err)
	}
	sb, err := screens(b)
	if err != nil {
		return "", fmt.Errorf("%s: %w", newPath, err)
	}
	return compare(sa, sb), nil
}

// compare says how two screen sequences differ, or "" when they match.
func compare(a, b []*image.RGBA) string {
	if len(a) != len(b) {
		return fmt.Sprintf("%d screens, was %d", len(b), len(a))
	}
	for i := range a {
		if !alike(a[i], b[i]) {
			return fmt.Sprintf("screen %d of %d changed", i+1, len(a))
		}
	}
	return ""
}

// screens decodes a GIF and returns the frames that stay up at least
// minScreen, in order. A frame repeated across several GIF frames counts once,
// with their delays added up.
func screens(data []byte) ([]*image.RGBA, error) {
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	canvas := image.NewRGBA(bounds)
	var out []*image.RGBA
	var cur *image.RGBA // the screen showing now
	shown := 0          // for how long, in hundredths of a second
	flush := func() {
		if cur != nil && shown >= minScreen {
			out = append(out, cur)
		}
	}
	for i, frame := range g.Image {
		var before *image.RGBA
		if g.Disposal != nil && g.Disposal[i] == gif.DisposalPrevious {
			before = clone(canvas)
		}
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		if cur == nil || !bytes.Equal(cur.Pix, canvas.Pix) {
			flush()
			cur, shown = clone(canvas), 0
		}
		shown += g.Delay[i]
		switch {
		case before != nil:
			canvas = before
		case g.Disposal != nil && g.Disposal[i] == gif.DisposalBackground:
			draw.Draw(canvas, frame.Bounds(), image.Transparent, image.Point{}, draw.Src)
		}
	}
	flush()
	return out, nil
}

func clone(m *image.RGBA) *image.RGBA {
	c := image.NewRGBA(m.Rect)
	copy(c.Pix, m.Pix)
	return c
}

// alike reports whether a and b show the same picture: in every block and
// channel, the average moved no more than maxMean and the pixels no more than
// maxAbsolute on average.
func alike(a, b *image.RGBA) bool {
	if a.Rect != b.Rect {
		return false
	}
	r := a.Rect
	for y0 := r.Min.Y; y0 < r.Max.Y; y0 += block {
		for x0 := r.Min.X; x0 < r.Max.X; x0 += block {
			var sum, abs [4]int // per channel: a minus b, and its size, over the block
			n := 0
			for y := y0; y < min(y0+block, r.Max.Y); y++ {
				for x := x0; x < min(x0+block, r.Max.X); x++ {
					i := a.PixOffset(x, y)
					for c := range 4 {
						d := int(a.Pix[i+c]) - int(b.Pix[i+c])
						sum[c] += d
						abs[c] += max(d, -d)
					}
					n++
				}
			}
			for c := range 4 {
				mean := float64(sum[c]) / float64(n)
				if mean > maxMean || mean < -maxMean || float64(abs[c])/float64(n) > maxAbsolute {
					return false
				}
			}
		}
	}
	return true
}
