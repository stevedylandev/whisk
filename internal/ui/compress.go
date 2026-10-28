package ui

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// maxImageUploadSize is the size an image is compressed below before upload.
const maxImageUploadSize = 5 * 1024 * 1024

// compressImage shrinks an image over maxImageUploadSize until it fits,
// re-encoding at a fairly high quality and downscaling step by step so it
// loses no more than needed. Opaque images become JPEG; images with
// transparency stay PNG. ok is false when the image can't be decoded or
// can't be brought under the limit, and the caller should send the original.
func compressImage(data []byte, mimeType, name string) (out []byte, outType, outName string, ok bool) {
	switch mimeType {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return nil, "", "", false
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", "", false
	}

	asPNG := !isOpaque(src)
	outType, ext := "image/jpeg", ".jpg"
	if asPNG {
		outType, ext = "image/png", ".png"
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	img := src
	for range 12 {
		var buf bytes.Buffer
		if asPNG {
			err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img)
		} else {
			err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
		}
		if err != nil {
			return nil, "", "", false
		}
		if buf.Len() <= maxImageUploadSize {
			outName = strings.TrimSuffix(name, filepath.Ext(name)) + ext
			return buf.Bytes(), outType, outName, true
		}
		w, h = w*4/5, h*4/5
		if w < 1 || h < 1 {
			break
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
		img = dst
	}
	return nil, "", "", false
}

// isOpaque reports whether an image has no transparent pixels.
func isOpaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	return false
}
