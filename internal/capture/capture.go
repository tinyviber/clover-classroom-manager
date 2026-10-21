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

// Grab 截取画面（mode="window" 取前台窗口，其余取主屏），供调用方自行编码。
func Grab(mode string) (image.Image, error) {
	if mode == "window" {
		return grabWindow()
	}
	return grab()
}

// Encode 缩放并编码为 JPEG（params.MaxWidth=0 表示原生分辨率）。
func Encode(src image.Image, params Params) []byte {
	return encodeJPEG(src, params.MaxWidth, params.Quality)
}

// Params 广播/监视的编码参数。IntervalMs 由发送端循环使用。
type Params struct {
	MaxWidth   int
	Quality    int
	IntervalMs int
}

// Fingerprinter 轻量"画面指纹"，检测画面没变从而跳帧省带宽：
// 每隔一段采样一个像素，逐样本比较差值，变化占比低于阈值视为静止。
// 对静态桌面/静止 PPT 这类机房常见画面，可以把流量降到接近零。
type Fingerprinter struct {
	last  []sample
	lastW int
	lastH int
	has   bool
}

type sample struct{ r, g, b uint8 }

// Changed 判断画面是否有可见变化（约 0.4% 采样点变化即视为变）。
func (f *Fingerprinter) Changed(img image.Image) bool {
	const gw, gh = 48, 27 // 采样网格 48×27
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return false
	}
	cur := make([]sample, gw*gh)
	sx := float64(w) / gw
	sy := float64(h) / gh
	for gy := 0; gy < gh; gy++ {
		y := b.Min.Y + int((float64(gy)+0.5)*sy)
		for gx := 0; gx < gw; gx++ {
			x := b.Min.X + int((float64(gx)+0.5)*sx)
			r, g, bl, _ := img.At(x, y).RGBA()
			cur[gy*gw+gx] = sample{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8)}
		}
	}
	changed := true
	if f.has && f.lastW == w && f.lastH == h && len(f.last) == len(cur) {
		diff := 0
		for i := range cur {
			d := int(cur[i].r) - int(f.last[i].r)
			if d < 0 {
				d = -d
			}
			if d > 10 {
				diff++
				continue
			}
			d = int(cur[i].g) - int(f.last[i].g)
			if d < 0 {
				d = -d
			}
			if d > 10 {
				diff++
				continue
			}
			d = int(cur[i].b) - int(f.last[i].b)
			if d < 0 {
				d = -d
			}
			if d > 10 {
				diff++
			}
		}
		changed = diff*250 > len(cur) // >0.4% 样本变化才算动
	}
	f.last, f.lastW, f.lastH, f.has = cur, w, h, true
	return changed
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
