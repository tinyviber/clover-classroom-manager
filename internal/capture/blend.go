package capture

import "image/color"

// bilinear 对四角 RGBA 做双线性插值。
func bilinear(c00, c10, c01, c11 color.Color, fx, fy float64) color.RGBA {
	lerp := func(a, b uint32, f float64) float64 {
		return float64(a)*(1-f) + float64(b)*f
	}
	chn := func(get func(color.Color) uint32) uint8 {
		top := lerp(get(c00), get(c10), fx)
		bot := lerp(get(c01), get(c11), fx)
		return uint8(lerp(uint32(top), uint32(bot), fy) / 257)
	}
	r := func(c color.Color) uint32 { v, _, _, _ := c.RGBA(); return v }
	g := func(c color.Color) uint32 { _, v, _, _ := c.RGBA(); return v }
	b := func(c color.Color) uint32 { _, _, v, _ := c.RGBA(); return v }
	a := func(c color.Color) uint32 { _, _, _, v := c.RGBA(); return v }
	return color.RGBA{R: chn(r), G: chn(g), B: chn(b), A: chn(a)}
}
