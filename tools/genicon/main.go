// genicon 用 public/clover-mark.png 生成项目的全套三叶草图标：
//
//	resources/icon.png          1024² 管理端图标（绿色圆角方底 + 三叶草）
//	resources/tray.png           128² 管理端菜单栏/托盘彩色图标
//	resources/tray-template.png  128² macOS 菜单栏模板图标（黑色剪影）
//	internal/trayicon/clover.ico 16/24/32/48 学生机托盘 ICO（PNG 压缩）
//
// 运行：go run ./tools/genicon
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"

	xdraw "golang.org/x/image/draw"
)

const srcPath = "public/clover-mark.png"

func main() {
	f, err := os.Open(srcPath)
	must(err, "打开 "+srcPath)
	defer f.Close()
	src, err := png.Decode(f)
	must(err, "解码 clover-mark.png")

	mark := cutWhite(src) // 白底抠透明
	must(os.WriteFile("resources/icon.png", pngBytes(appIcon(mark, 1024)), 0o644), "写 icon.png")
	must(os.WriteFile("resources/tray.png", pngBytes(scale(mark, 128)), 0o644), "写 tray.png")
	must(os.WriteFile("resources/tray-template.png", pngBytes(template(mark, 128)), 0o644), "写 tray-template.png")
	must(os.WriteFile("internal/trayicon/clover.ico", ico(mark, []int{16, 24, 32, 48, 64}), 0o644), "写 clover.ico")
	fmt.Println("图标已生成：resources/icon.png tray.png tray-template.png internal/trayicon/clover.ico")
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintln(os.Stderr, what+":", err)
		os.Exit(1)
	}
}

// cutWhite 把纯白/近白底抠成透明（保留叶脉浅绿色）。若本已透明则原样返回。
func cutWhite(src image.Image) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	// 检测四角是否已透明
	p := dst.NRGBAAt(0, 0)
	p2 := dst.NRGBAAt(b.Dx()-1, b.Dy()-1)
	if p.A < 16 && p2.A < 16 {
		return dst
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			i := y*dst.Stride + x*4
			r, g, bl := dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2]
			w := min3(r, g, bl) // 白度：三个通道里最暗的分量
			var a uint8 = 255
			if w >= 250 {
				a = 0
			} else if w >= 235 {
				a = uint8((250 - int(w)) * 255 / 15)
			}
			dst.Pix[i+3] = a
		}
	}
	return dst
}

func min3(a, b, c uint8) uint8 {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// scale 双线性缩放到 s×s。
func scale(src image.Image, s int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, s, s))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

// appIcon 绿色圆角方底（微渐变）+ 居中三叶草（约 82% 尺寸）。
func appIcon(mark image.Image, s int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, s, s))
	radius := float64(s) * 0.22 // macOS 风格圆角
	top := color.NRGBA{0x18, 0x6B, 0x45, 0xFF}
	bot := color.NRGBA{0x35, 0x9B, 0x61, 0xFF}
	for y := 0; y < s; y++ {
		t := float64(y) / float64(s-1)
		c := color.NRGBA{
			uint8(float64(top.R)*(1-t) + float64(bot.R)*t),
			uint8(float64(top.G)*(1-t) + float64(bot.G)*t),
			uint8(float64(top.B)*(1-t) + float64(bot.B)*t), 0xFF}
		for x := 0; x < s; x++ {
			if inRounded(float64(x)+0.5, float64(y)+0.5, float64(s), radius) {
				dst.SetNRGBA(x, y, c)
			}
		}
	}
	pad := int(float64(s) * 0.09)
	ms := s - pad*2
	scaled := scale(mark, ms)
	draw.Draw(dst, image.Rect(pad, pad, pad+ms, pad+ms), scaled, image.Point{}, draw.Over)
	return dst
}

// inRounded 点是否在 s×s 圆角矩形内（四角按半径切除）。
func inRounded(px, py, s, r float64) bool {
	qx := math.Abs(px - s/2)
	qy := math.Abs(py - s/2)
	hx := s/2 - r
	if qx > s/2 || qy > s/2 {
		return false
	}
	if qx <= hx || qy <= hx {
		return true
	}
	dx, dy := qx-hx, qy-hx
	return dx*dx+dy*dy <= r*r
}

// template 用三叶草 alpha 形状生成黑色剪影（macOS 菜单栏模板图标）。
func template(mark image.Image, s int) *image.NRGBA {
	m := scale(mark, s)
	dst := image.NewNRGBA(image.Rect(0, 0, s, s))
	for i := 0; i < len(m.Pix); i += 4 {
		dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2] = 0, 0, 0
		dst.Pix[i+3] = m.Pix[i+3]
	}
	return dst
}

// ico 打包多尺寸 PNG 进 ICO 容器（Vista+ 支持 PNG-in-ICO）。
func ico(mark image.Image, sizes []int) []byte {
	var out bytes.Buffer
	var entries []struct {
		w, h  int
		pngB  []byte
	}
	for _, s := range sizes {
		entries = append(entries, struct {
			w, h int
			pngB []byte
		}{s, s, pngBytes(scale(mark, s))})
	}
	// ICONDIR
	binary.Write(&out, binary.LittleEndian, uint16(0))
	binary.Write(&out, binary.LittleEndian, uint16(1))
	binary.Write(&out, binary.LittleEndian, uint16(len(entries)))
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		w8, h8 := byte(e.w), byte(e.h)
		if e.w >= 256 {
			w8 = 0
		}
		if e.h >= 256 {
			h8 = 0
		}
		out.WriteByte(w8)
		out.WriteByte(h8)
		out.WriteByte(0) // palette
		out.WriteByte(0) // reserved
		binary.Write(&out, binary.LittleEndian, uint16(1))  // planes
		binary.Write(&out, binary.LittleEndian, uint16(32)) // bpp
		binary.Write(&out, binary.LittleEndian, uint32(len(e.pngB)))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(e.pngB)
	}
	for _, e := range entries {
		out.Write(e.pngB)
	}
	return out.Bytes()
}

func pngBytes(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
