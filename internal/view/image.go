package view

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"strconv"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"golang.org/x/term"
)

// renderImage 将图片渲染为半块字符(▀)+ ANSI 真彩色字符画,任何终端可见。
// 不依赖任何终端图形协议;颜色效果取决于终端的 24-bit 真彩色支持。
func renderImage(s *Source, opts *Options) error {
	max := int64(10 << 20)
	if opts.MaxImageBytes > 0 {
		max = opts.MaxImageBytes
	}
	b, err := readBounded(s, max, opts.Force)
	if err != nil {
		return err
	}
	img, format, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("解码图片失败: %w", err)
	}
	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()

	cols, rows := termSize()
	if opts.Width > 0 {
		cols = opts.Width
	}
	maxPixH := rows * 2
	if maxPixH <= 0 {
		maxPixH = 80
	}

	// 按宽高比缩放,先适配宽度,若高度超终端则改为适配高度。
	scale := float64(cols) / float64(srcW)
	pixW := cols
	pixH := int(math.Round(float64(srcH) * scale))
	if pixH > maxPixH {
		scale = float64(maxPixH) / float64(srcH)
		pixH = maxPixH
		pixW = int(math.Round(float64(srcW) * scale))
	}
	if pixW < 1 {
		pixW = 1
	}
	if pixH < 1 {
		pixH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, pixW, pixH))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, bounds, draw.Src, nil)

	ct := s.ContentType
	if ct == "" {
		ct = "image/" + format
	}
	sizeStr := "未知"
	if s.Size >= 0 {
		sizeStr = humanBytes(s.Size)
	}
	fmt.Printf("image: %s  %dx%d  %s  %s\n", s.Name, srcW, srcH, sizeStr, ct)

	var line []byte
	for y := 0; y < pixH; y += 2 {
		line = line[:0]
		for x := 0; x < pixW; x++ {
			r1, g1, b1 := rgba(dst, x, y)
			if y+1 < pixH {
				r2, g2, b2 := rgba(dst, x, y+1)
				line = append(line, ansiFG(r1, g1, b1)...)
				line = append(line, ansiBG(r2, g2, b2)...)
				line = append(line, upperBlock...)
			} else {
				line = append(line, ansiFG(r1, g1, b1)...)
				line = append(line, fullBlock...)
			}
		}
		line = append(line, reset...)
		fmt.Println(string(line))
	}
	return nil
}

func rgba(img *image.RGBA, x, y int) (int, int, int) {
	i := (y*img.Stride + x*4)
	c := img.Pix[i : i+4]
	return int(c[0]), int(c[1]), int(c[2])
}

func ansiFG(r, g, b int) []byte {
	return []byte(fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b))
}

func ansiBG(r, g, b int) []byte {
	return []byte(fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b))
}

var (
	upperBlock = []byte("▀") // U+2580 上半块:fg 色=上半,bg 色=下半
	fullBlock  = []byte("█") // U+2588 满块:用于奇数行末行
	reset      = []byte("\x1b[0m")
)

func termSize() (w, h int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		w = 80
	}
	if err != nil || h <= 0 {
		if c := os.Getenv("COLUMNS"); c != "" {
			if n, err := strconv.Atoi(c); err == nil && n > 0 {
				w = n
			}
		}
		h = 40
	}
	return w, h
}
