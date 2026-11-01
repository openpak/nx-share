package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"strings"
)

// OpenVerse keeps screenshots in the sizes Miiverse used and refuses any other
// (miiverse-api images.ts validResolutions); 800x450 is its 16:9 size. A Switch capture is a
// 1280x720 JPEG, so it is scaled here. Re-encoding also drops the capture's EXIF block.
const (
	shotW = 800
	shotH = 450
)

// maxDecode bounds what is decoded at all: a Switch capture is 1280x720.
const maxDecodePixels = 1920 * 1080

func screenshotFromDataURL(v string) ([]byte, error) {
	i := strings.Index(v, ",")
	if i < 0 || !strings.HasPrefix(v, "data:") || !strings.Contains(v[:i], ";base64") {
		return nil, errors.New("not a base64 data URL")
	}
	raw, err := base64.StdEncoding.DecodeString(v[i+1:])
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	return screenshotJPEG(raw)
}

// screenshotJPEG accepts a 16:9 JPEG up to 1920x1080 and returns it as an 800x450 JPEG.
func screenshotJPEG(raw []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if format != "jpeg" {
		return nil, fmt.Errorf("format %s", format)
	}
	if cfg.Width*cfg.Height > maxDecodePixels || cfg.Width < shotW || cfg.Width*9 != cfg.Height*16 {
		return nil, fmt.Errorf("size %dx%d", cfg.Width, cfg.Height)
	}
	src, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, scale(src, shotW, shotH), &jpeg.Options{Quality: 92}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// scale is an area-average downscale: each output pixel is the mean of the source pixels it
// covers (weighted at the edges). For 1280->800 it is sharper than bilinear and has no
// aliasing; the standard library has no resampler, and this is the whole of one.
func scale(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	in := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(in, in.Bounds(), src, b.Min, draw.Src) // fast path for the decoder's YCbCr
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	fx, fy := float64(sw)/float64(w), float64(sh)/float64(h)
	for y := 0; y < h; y++ {
		y0, y1 := float64(y)*fy, float64(y+1)*fy
		for x := 0; x < w; x++ {
			x0, x1 := float64(x)*fx, float64(x+1)*fx
			var r, g, bl, wsum float64
			for sy := int(y0); float64(sy) < y1 && sy < sh; sy++ {
				wy := minf(y1, float64(sy+1)) - maxf(y0, float64(sy))
				row := in.Pix[sy*in.Stride:]
				for sx := int(x0); float64(sx) < x1 && sx < sw; sx++ {
					wt := wy * (minf(x1, float64(sx+1)) - maxf(x0, float64(sx)))
					p := row[sx*4 : sx*4+3]
					r += float64(p[0]) * wt
					g += float64(p[1]) * wt
					bl += float64(p[2]) * wt
					wsum += wt
				}
			}
			o := dst.Pix[y*dst.Stride+x*4:]
			o[0], o[1], o[2], o[3] = uint8(r/wsum+.5), uint8(g/wsum+.5), uint8(bl/wsum+.5), 255
		}
	}
	return dst
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
