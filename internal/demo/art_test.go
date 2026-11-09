package demo

import (
	"image"
	"testing"
)

// TestBackdropHasNoDisc: the title page lays its name and overview over the backdrop, so the generated backdrop
// draws no disc there (it showed as a blurred circle over the overview); posters keep theirs.
func TestBackdropHasNoDisc(t *testing.T) {
	t.Parallel()
	discs := func(img image.Image) int {
		p := img.(*image.Paletted)
		n := 0
		for _, ix := range p.Pix {
			if ix == 12 { // the disc's palette entry, after the 12 sky bands
				n++
			}
		}
		return n
	}
	for _, h := range []uint64{1, 0x9e3779b97f4a7c15, 0xdeadbeefcafef00d} {
		if n := discs(drawArt(780, 780*9/16, h, "Kiraboshi Mechanics", false)); n != 0 {
			t.Errorf("backdrop %x: %d disc pixels", h, n)
		}
		if n := discs(drawArt(342, 513, h, "Kiraboshi Mechanics", true)); n == 0 {
			t.Errorf("poster %x: no disc", h)
		}
	}
}
