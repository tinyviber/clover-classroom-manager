//go:build !windows

package capture

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

var tick atomic.Uint64

// grab 生成测试画面：渐变底 + 机器名 + 时钟——开发期充当"学生机屏幕"。
func grab() (image.Image, error) {
	w, h := 1280, 800
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	t := tick.Add(1)
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		g := uint8(32 + y*60/h)
		b0 := uint8(46 + y*30/h)
		for x := 0; x < w; x++ {
			i := x * 4
			row[i] = uint8(30 + x*80/w + int(t)%40)
			row[i+1] = g
			row[i+2] = b0 + uint8(x*40/w)
			row[i+3] = 255
		}
	}
	name, _ := os.Hostname()
	drawText(img, fmt.Sprintf("%s  %s", name, time.Now().Format("15:04:05")), 40, h/2)
	return img, nil
}

func grabWindow() (image.Image, error) { return grab() }

func drawText(img *image.RGBA, s string, x, y int) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(color.RGBA{235, 236, 240, 255}),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
	d.Dot = fixed.P(x, y+18)
	d.DrawString("(demo screen)")
}
