package main

// Icons drawn in code (no image file to embed):
//   - orange "!" badge for the tray icon (game to identify), ICO format;
//   - white "C" logo on a blue background (icon, notifications), ICO and PNG.

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

// shape reports whether the point (x, y), in 0..1 coordinates, is inside the drawing.
type shape func(x, y float64) (inside bool, c color.NRGBA)

// render draws s at size×size with anti-aliasing (4×4 samples per pixel).
func render(size int, s shape) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					if in, c := s(x, y); in {
						r += float64(c.R)
						g += float64(c.G)
						b += float64(c.B)
						a += float64(c.A)
					}
				}
			}
			n := float64(ss * ss)
			if a > 0 {
				// Average colour of the covered samples (all opaque);
				// alpha = covered fraction.
				img.SetNRGBA(px, py, color.NRGBA{
					R: uint8(r * 255 / a),
					G: uint8(g * 255 / a),
					B: uint8(b * 255 / a),
					A: uint8(a / n),
				})
			}
		}
	}
	return img
}

var (
	orange = color.NRGBA{0xF5, 0x9E, 0x0B, 0xFF}
	blue   = color.NRGBA{0x26, 0x8B, 0xD2, 0xFF}
	white  = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
)

// alertShape is a white "!" on an orange disc.
func alertShape(x, y float64) (bool, color.NRGBA) {
	dx, dy := x-0.5, y-0.5
	if dx*dx+dy*dy > 0.25 {
		return false, color.NRGBA{}
	}
	bar := math.Abs(dx) < 0.075 && y > 0.17 && y < 0.62
	dot := (dx*dx + (y-0.77)*(y-0.77)) < 0.085*0.085
	if bar || dot {
		return true, white
	}
	return true, orange
}

// The CoopRatings "C": a ring open on the right. Same proportions as the
// window logo (LOGO in ui/app.html).
const (
	cCenterX, cCenterY = 0.52, 0.5 // slightly right: the opening makes that side look lighter
	cOuter, cInner     = 0.31, 0.18
	cGapDeg            = 42 // half of the opening, in degrees
)

// logoShape is a white "C" on a blue rounded square.
func logoShape(x, y float64) (bool, color.NRGBA) {
	// rounded square
	r := 0.2
	cx := math.Max(math.Abs(x-0.5)-(0.5-r), 0)
	cy := math.Max(math.Abs(y-0.5)-(0.5-r), 0)
	if cx*cx+cy*cy > r*r {
		return false, color.NRGBA{}
	}
	dx, dy := x-cCenterX, y-cCenterY
	if d := math.Hypot(dx, dy); d <= cOuter && d >= cInner &&
		math.Abs(math.Atan2(dy, dx)) > cGapDeg*math.Pi/180 {
		return true, white
	}
	return true, blue
}

// encodeICO builds a .ico file (32-bit images) from several sizes.
func encodeICO(imgs ...*image.NRGBA) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, uint16(len(imgs))})
	offset := 6 + 16*len(imgs)
	var data [][]byte
	for _, img := range imgs {
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		var d bytes.Buffer
		binary.Write(&d, binary.LittleEndian, struct {
			Size, Width, Height               int32
			Planes, BitCount                  uint16
			Compression, SizeImage            uint32
			XPPM, YPPM, ClrUsed, ClrImportant int32
		}{40, int32(w), int32(2 * h), 1, 32, 0, 0, 0, 0, 0, 0})
		for y := h - 1; y >= 0; y-- { // rows bottom-up
			for x := 0; x < w; x++ {
				c := img.NRGBAAt(x, y)
				d.Write([]byte{c.B, c.G, c.R, c.A})
			}
		}
		d.Write(make([]byte, ((w+31)/32)*4*h)) // AND mask (transparency comes from alpha)
		data = append(data, d.Bytes())
		bw, bh := byte(w), byte(h)
		if w >= 256 {
			bw, bh = 0, 0
		}
		buf.Write([]byte{bw, bh, 0, 0})
		binary.Write(&buf, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&buf, binary.LittleEndian, [2]uint32{uint32(d.Len()), uint32(offset)})
		offset += d.Len()
	}
	for _, d := range data {
		buf.Write(d)
	}
	return buf.Bytes()
}

// badgeShape is the logo with an orange "!" badge bottom right (game to identify).
func badgeShape(x, y float64) (bool, color.NRGBA) {
	const cx, cy, r = 0.73, 0.73, 0.27
	dx, dy := x-cx, y-cy
	d := math.Sqrt(dx*dx + dy*dy)
	switch {
	case d < r:
		return alertShape(dx/(2*r)+0.5, dy/(2*r)+0.5)
	case d < r+0.06: // transparent gap separating the badge from the "C"
		return false, color.NRGBA{}
	}
	return logoShape(x, y)
}

func iconICO(s shape) []byte {
	return encodeICO(render(16, s), render(20, s), render(24, s), render(32, s), render(48, s))
}

// appICO is the exe icon (Explorer, startup apps list, windows), written to
// assets/icon.ico by --write-icon.
func appICO() []byte {
	var imgs []*image.NRGBA
	for _, s := range []int{16, 20, 24, 32, 40, 48, 64, 128, 256} {
		imgs = append(imgs, render(s, logoShape))
	}
	return encodeICO(imgs...)
}

var (
	trayIconNormal = iconICO(logoShape)
	trayIconBadge  = iconICO(badgeShape)
)

// writeLogoPNG writes the notification logo (icon.png).
func writeLogoPNG(path string) error {
	var buf bytes.Buffer
	if err := png.Encode(&buf, render(128, logoShape)); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
