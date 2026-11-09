package demo

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Generated artwork: a poster and a backdrop per title, drawn here (no network, no outside images) and written
// into the /img cache in its layout ({dir}/{size}/{first two chars}/{file}), so the proxy serves them as cached TMDb
// art. Posters carry the title in a small pixel font; backdrops are the same scene, wider, without the disc.

// posterSizes and backdropSizes are the sizes the UI asks for (internal/web/art.go, the SPA's poster-card), and the
// widths those names stand for.
var (
	posterSizes   = map[string]int{"w92": 92, "w185": 185, "w342": 342, "w500": 500}
	backdropSizes = map[string]int{"w780": 780, "w1280": 1280}
)

// writeArt draws and writes every title's art. It returns the number of files written.
func writeArt(dir string, titles []*title) (int, error) {
	n := 0
	for _, t := range titles {
		if t.poster == "" {
			continue
		}
		seed := fnv.New64a()
		fmt.Fprintf(seed, "%d/%s", t.id, t.name)
		h := seed.Sum64()
		for size, w := range posterSizes {
			if err := writePNG(dir, size, t.poster, drawArt(w, w*3/2, h, t.name, true)); err != nil {
				return n, err
			}
			n++
		}
		for size, w := range backdropSizes {
			if err := writePNG(dir, size, t.backdrop, drawArt(w, w*9/16, h, t.name, false)); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

func writePNG(dir, size, artPath string, img image.Image) error {
	file := strings.TrimPrefix(artPath, "/")
	p := filepath.Join(dir, size, file[:2], file)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(p, buf.Bytes(), 0o644)
}

// drawArt draws a scene: a banded sky in two hues picked by h, a disc, two ridges of hills, and (posters) the title.
func drawArt(w, hgt int, h uint64, name string, poster bool) image.Image {
	hue := float64(h % 360)
	hue2 := math.Mod(hue+30+float64(h>>9%90), 360)
	pal := color.Palette{}
	const bands = 12
	for i := range bands {
		f := float64(i) / (bands - 1)
		pal = append(pal, hsl(hue+(hue2-hue)*f, 0.45, 0.18+0.32*f))
	}
	disc := len(pal)
	pal = append(pal, hsl(math.Mod(hue+180, 360), 0.55, 0.78))
	ridge1 := len(pal)
	pal = append(pal, hsl(hue2, 0.35, 0.12))
	ridge2 := len(pal)
	pal = append(pal, hsl(hue2, 0.30, 0.07))
	white := len(pal)
	pal = append(pal, color.RGBA{0xf5, 0xf5, 0xf4, 0xff})
	shadow := len(pal)
	pal = append(pal, color.RGBA{0x0a, 0x0a, 0x0c, 0xff})
	img := image.NewPaletted(image.Rect(0, 0, w, hgt), pal)
	horizon := hgt * 62 / 100
	for y := range hgt {
		band := uint8(min(bands-1, y*bands/max(1, horizon)))
		for x := range w {
			img.Pix[y*img.Stride+x] = band
		}
	}
	// The disc, below the title on posters only: the title page lays its name and overview across the backdrop's
	// upper part, where a disc showed through as a blurred circle over the text.
	cx := w * int(25+h>>17%50) / 100
	cy := horizon * int(62+h>>23%25) / 100
	r := min(w, hgt) * int(10+h>>29%10) / 100
	for y := max(0, cy-r); poster && y < min(hgt, cy+r); y++ {
		for x := max(0, cx-r); x < min(w, cx+r); x++ {
			if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r {
				img.Pix[y*img.Stride+x] = uint8(disc)
			}
		}
	}
	// Two ridges.
	ridge := func(base int, amp, freq, phase float64, idx int) {
		for x := range w {
			fx := float64(x) / float64(w)
			top := base - int(amp*float64(hgt)*(0.5+0.5*math.Sin(fx*freq+phase))*(0.6+0.4*math.Sin(fx*freq*2.3+phase*1.7)))
			for y := max(0, top); y < hgt; y++ {
				img.Pix[y*img.Stride+x] = uint8(idx)
			}
		}
	}
	ridge(horizon+hgt/20, 0.16, 5+float64(h>>33%5), float64(h>>37%628)/100, ridge1)
	ridge(horizon+hgt/7, 0.10, 3+float64(h>>41%4), float64(h>>45%628)/100, ridge2)
	if poster {
		drawTitle(img, name, uint8(white), uint8(shadow))
	}
	return img
}

// drawTitle writes name in the pixel font, wrapped and centred in the upper third (the cards lay their own words
// over the lower part).
func drawTitle(img *image.Paletted, name string, fg, bg uint8) {
	w, hgt := img.Rect.Dx(), img.Rect.Dy()
	text := strings.ToUpper(name)
	maxChars := 12
	lines := wrapWords(text, maxChars)
	if len(lines) > 4 {
		lines = lines[:4]
	}
	longest := 1
	for _, l := range lines {
		longest = max(longest, len([]rune(l)))
	}
	scale := max(1, min((w*86/100)/(longest*6), hgt/(10*8)))
	lineH := 9 * scale
	y := hgt * 17 / 100
	for _, l := range lines {
		lw := len([]rune(l))*6*scale - scale
		x := (w - lw) / 2
		drawText(img, x+max(1, scale/2), y+max(1, scale/2), l, scale, bg)
		drawText(img, x, y, l, scale, fg)
		y += lineH
	}
}

func wrapWords(s string, width int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case len([]rune(cur))+1+len([]rune(word)) <= width:
			cur += " " + word
		default:
			lines, cur = append(lines, cur), word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func drawText(img *image.Paletted, x, y int, s string, scale int, c uint8) {
	for _, ch := range s {
		g, ok := glyphs[ch]
		if !ok {
			g = glyphs[' ']
		}
		for row, bits := range g {
			for col := range 5 {
				if bits[col] != '#' {
					continue
				}
				for dy := range scale {
					for dx := range scale {
						px, py := x+col*scale+dx, y+row*scale+dy
						if image.Pt(px, py).In(img.Rect) {
							img.Pix[py*img.Stride+px] = c
						}
					}
				}
			}
		}
		x += 6 * scale
	}
}

func hsl(h, s, l float64) color.RGBA {
	h = math.Mod(h, 360) / 360
	var r, g, b float64
	if s == 0 {
		r, g, b = l, l, l
	} else {
		q := l * (1 + s)
		if l >= 0.5 {
			q = l + s - l*s
		}
		p := 2*l - q
		r, g, b = hue2rgb(p, q, h+1.0/3), hue2rgb(p, q, h), hue2rgb(p, q, h-1.0/3)
	}
	return color.RGBA{uint8(r * 255), uint8(g * 255), uint8(b * 255), 0xff}
}

func hue2rgb(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	switch {
	case t < 1.0/6:
		return p + (q-p)*6*t
	case t < 0.5:
		return q
	case t < 2.0/3:
		return p + (q-p)*(2.0/3-t)*6
	}
	return p
}

// glyphs is a 5x7 pixel font: capitals, digits and the punctuation the titles use.
var glyphs = map[rune][7]string{
	' ':  {".....", ".....", ".....", ".....", ".....", ".....", "....."},
	'A':  {".###.", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
	'B':  {"####.", "#...#", "#...#", "####.", "#...#", "#...#", "####."},
	'C':  {".###.", "#...#", "#....", "#....", "#....", "#...#", ".###."},
	'D':  {"####.", "#...#", "#...#", "#...#", "#...#", "#...#", "####."},
	'E':  {"#####", "#....", "#....", "####.", "#....", "#....", "#####"},
	'F':  {"#####", "#....", "#....", "####.", "#....", "#....", "#...."},
	'G':  {".###.", "#...#", "#....", "#.###", "#...#", "#...#", ".####"},
	'H':  {"#...#", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
	'I':  {".###.", "..#..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'J':  {"..###", "...#.", "...#.", "...#.", "...#.", "#..#.", ".##.."},
	'K':  {"#...#", "#..#.", "#.#..", "##...", "#.#..", "#..#.", "#...#"},
	'L':  {"#....", "#....", "#....", "#....", "#....", "#....", "#####"},
	'M':  {"#...#", "##.##", "#.#.#", "#.#.#", "#...#", "#...#", "#...#"},
	'N':  {"#...#", "#...#", "##..#", "#.#.#", "#..##", "#...#", "#...#"},
	'O':  {".###.", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."},
	'P':  {"####.", "#...#", "#...#", "####.", "#....", "#....", "#...."},
	'Q':  {".###.", "#...#", "#...#", "#...#", "#.#.#", "#..#.", ".##.#"},
	'R':  {"####.", "#...#", "#...#", "####.", "#.#..", "#..#.", "#...#"},
	'S':  {".####", "#....", "#....", ".###.", "....#", "....#", "####."},
	'T':  {"#####", "..#..", "..#..", "..#..", "..#..", "..#..", "..#.."},
	'U':  {"#...#", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."},
	'V':  {"#...#", "#...#", "#...#", "#...#", "#...#", ".#.#.", "..#.."},
	'W':  {"#...#", "#...#", "#...#", "#.#.#", "#.#.#", "#.#.#", ".#.#."},
	'X':  {"#...#", "#...#", ".#.#.", "..#..", ".#.#.", "#...#", "#...#"},
	'Y':  {"#...#", "#...#", ".#.#.", "..#..", "..#..", "..#..", "..#.."},
	'Z':  {"#####", "....#", "...#.", "..#..", ".#...", "#....", "#####"},
	'0':  {".###.", "#...#", "#..##", "#.#.#", "##..#", "#...#", ".###."},
	'1':  {"..#..", ".##..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'2':  {".###.", "#...#", "....#", "...#.", "..#..", ".#...", "#####"},
	'3':  {"####.", "....#", "....#", ".###.", "....#", "....#", "####."},
	'4':  {"...#.", "..##.", ".#.#.", "#..#.", "#####", "...#.", "...#."},
	'5':  {"#####", "#....", "####.", "....#", "....#", "#...#", ".###."},
	'6':  {".###.", "#....", "#....", "####.", "#...#", "#...#", ".###."},
	'7':  {"#####", "....#", "...#.", "..#..", ".#...", ".#...", ".#..."},
	'8':  {".###.", "#...#", "#...#", ".###.", "#...#", "#...#", ".###."},
	'9':  {".###.", "#...#", "#...#", ".####", "....#", "....#", ".###."},
	':':  {".....", "..#..", "..#..", ".....", "..#..", "..#..", "....."},
	'\'': {"..#..", "..#..", ".#...", ".....", ".....", ".....", "....."},
	'-':  {".....", ".....", ".....", ".###.", ".....", ".....", "....."},
	'.':  {".....", ".....", ".....", ".....", ".....", "..#..", "..#.."},
	',':  {".....", ".....", ".....", ".....", "..#..", "..#..", ".#..."},
	'&':  {".##..", "#..#.", ".##..", ".#...", "#.#.#", "#..#.", ".##.#"},
}
