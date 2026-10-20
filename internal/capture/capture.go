// Package capture 提供跨平台屏幕截图为 JPEG。
// Windows 走真实截图（kbinani/screenshot，纯 syscall 无 cgo）；
// 其余平台（开发用）生成带机器名与时间戳的测试画面，便于联调。
package capture

import (
	"bytes"
	"image"
	"image/jpeg"
)

// Frame 截取主屏并编码为 JPEG，宽缩放到 maxWidth（0=不缩放）。
func Frame(maxWidth, quality int) ([]byte, error) {
	img, err := grab()
	if err != nil {
		return nil, err
	}
	return encodeJPEG(img, maxWidth, quality), nil
}

// FrameMode captures the primary display for fullscreen mode and the active
// foreground window's screen rectangle for window mode. The active window is
// sampled each frame so the teacher can switch the shared application after
// starting the broadcast.
func FrameMode(mode string, maxWidth, quality int) ([]byte, error) {
	var img image.Image
	var err error
	if mode == "window" {
		img, err = grabWindow()
	} else {
		img, err = grab()
	}
	if err != nil {
		return nil, err
	}
	return encodeJPEG(img, maxWidth, quality), nil
}

// encodeJPEG 缩放并编码（双线性足够缩略图/广播用）。
func encodeJPEG(src image.Image, maxWidth, quality int) []byte {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxWidth > 0 && w > maxWidth {
		nh := h * maxWidth / w
		src = scaleBilinear(src, maxWidth, nh)
		w, h = maxWidth, nh
	}
	if quality <= 0 || quality > 100 {
		quality = 60
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality})
	return buf.Bytes()
}

// scaleBilinear 简单双线性缩放到 (w,h)。
func scaleBilinear(src image.Image, w, h int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := float64(y)*float64(sh)/float64(h) + float64(sb.Min.Y)
		y0 := int(sy)
		fy := sy - float64(y0)
		y1 := min(y0+1, sb.Max.Y-1)
		for x := 0; x < w; x++ {
			sx := float64(x)*float64(sw)/float64(w) + float64(sb.Min.X)
			x0 := int(sx)
			fx := sx - float64(x0)
			x1 := min(x0+1, sb.Max.X-1)
			c00 := src.At(x0, y0)
			c10 := src.At(x1, y0)
			c01 := src.At(x0, y1)
			c11 := src.At(x1, y1)
			dst.SetRGBA(x, y, bilinear(c00, c10, c01, c11, fx, fy))
		}
	}
	return dst
}
