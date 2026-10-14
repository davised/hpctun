package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// iconSize is drawn at twice the menu bar's 16 points, for Retina screens.
const iconSize = 32

// Icons returns the PNGs for icon: a template image (black on clear; macOS
// recolours it to suit the menu bar) and a coloured one for Linux trays.
// The shapes differ by state, so the template still tells them apart:
// a filled disc (on), a ring half filled (partial), an empty ring (off)
// and a ring struck through (error).
func Icons(icon Icon) (template, regular []byte) {
	colour := map[Icon]color.NRGBA{
		IconOn:      {0x2e, 0xa0, 0x43, 0xff}, // green
		IconPartial: {0xd2, 0x99, 0x22, 0xff}, // amber
		IconOff:     {0x8b, 0x8b, 0x8b, 0xff}, // grey
		IconError:   {0xcf, 0x22, 0x2e, 0xff}, // red
	}[icon]
	black := color.NRGBA{0, 0, 0, 0xff}
	return drawIcon(icon, black), drawIcon(icon, colour)
}

func drawIcon(icon Icon, c color.NRGBA) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	const mid, outer, inner = iconSize / 2, 13.0, 9.5
	for y := range iconSize {
		for x := range iconSize {
			// Sample the pixel's centre; cover blends the edges.
			dx, dy := float64(x)+0.5-mid, float64(y)+0.5-mid
			d := math.Hypot(dx, dy)
			ring := cover(outer-d) * cover(d-inner)
			disc := cover(outer - d)
			var a float64
			switch icon {
			case IconOn:
				a = disc
			case IconPartial:
				a = ring
				if dx < 0 {
					a = disc
				}
			case IconOff:
				a = ring
			case IconError:
				// a slash from bottom left to top right, 3px wide
				slash := cover(1.6 - math.Abs(dx+dy)/math.Sqrt2)
				a = math.Max(ring, slash*disc)
			}
			if a > 0 {
				img.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, uint8(float64(c.A) * a)})
			}
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// cover turns a signed distance (positive inside) into a pixel's coverage.
func cover(d float64) float64 { return math.Max(0, math.Min(1, d+0.5)) }

// ICO wraps a PNG from Icons in a one-image .ico file: Windows' tray takes
// only ICO, which may hold a PNG since Windows Vista.
func ICO(png []byte) []byte {
	b := binary.LittleEndian.AppendUint16(nil, 0) // reserved
	b = binary.LittleEndian.AppendUint16(b, 1)    // type: icon
	b = binary.LittleEndian.AppendUint16(b, 1)    // one image
	b = append(b, iconSize, iconSize, 0, 0)       // width, height, no palette, reserved
	b = binary.LittleEndian.AppendUint16(b, 1)    // colour planes
	b = binary.LittleEndian.AppendUint16(b, 32)   // bits per pixel
	b = binary.LittleEndian.AppendUint32(b, uint32(len(png)))
	b = binary.LittleEndian.AppendUint32(b, 22) // the PNG follows this 22-byte header
	return append(b, png...)
}
