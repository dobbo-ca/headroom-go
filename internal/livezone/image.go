package livezone

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
)

// countImageTokens returns the visual token cost of an image with the given
// dimensions. Each 28x28 patch costs one token.
//
// Copied verbatim from platform.claude.com/docs/en/build-with-claude/vision-coordinates
// as the authoritative formula.
func countImageTokens(width, height int) int {
	return int(math.Ceil(float64(width)/28)) * int(math.Ceil(float64(height)/28))
}

// resizedSize returns the largest aspect-preserving size that fits within
// maxEdge on both axes and stays under maxTokens visual tokens. The short
// edge is rounded half-to-even.
//
// Copied verbatim from platform.claude.com/docs/en/build-with-claude/vision-coordinates
// as the authoritative algorithm.
func resizedSize(width, height, maxEdge, maxTokens int) (int, int) {
	if width <= maxEdge && height <= maxEdge && countImageTokens(width, height) <= maxTokens {
		return width, height
	}

	// Binary search on the long edge
	aspectRatio := float64(width) / float64(height)
	var targetWidth, targetHeight int

	if width > height {
		// Landscape
		low, high := 1, maxEdge
		for low <= high {
			mid := (low + high) / 2
			tw := mid
			th := int(math.RoundToEven(float64(tw) / aspectRatio))
			if tw <= maxEdge && th <= maxEdge && countImageTokens(tw, th) <= maxTokens {
				targetWidth, targetHeight = tw, th
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
	} else {
		// Portrait or square
		low, high := 1, maxEdge
		for low <= high {
			mid := (low + high) / 2
			th := mid
			tw := int(math.RoundToEven(float64(th) * aspectRatio))
			if tw <= maxEdge && th <= maxEdge && countImageTokens(tw, th) <= maxTokens {
				targetWidth, targetHeight = tw, th
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
	}

	return targetWidth, targetHeight
}

// boxDownsample reduces src to (dw,dh) using integer box/area-average.
// For each destination pixel, it averages every source pixel in the box
// [x*sw/dw, (x+1)*sw/dw) × [y*sh/dh, (y+1)*sh/dh), computed with integer
// arithmetic so float rounding cannot drift the bounds.
func boxDownsample(src image.Image, dw, dh int) *image.NRGBA {
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()

	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for dy := 0; dy < dh; dy++ {
		for dx := 0; dx < dw; dx++ {
			// Integer box bounds: [x0, x1) × [y0, y1)
			x0 := (dx * sw) / dw
			x1 := ((dx + 1) * sw) / dw
			y0 := (dy * sh) / dh
			y1 := ((dy + 1) * sh) / dh

			var sumR, sumG, sumB, sumA uint64
			count := 0
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					r, g, b, a := src.At(sx+bounds.Min.X, sy+bounds.Min.Y).RGBA()
					// RGBA returns premultiplied 16-bit values
					sumR += uint64(r)
					sumG += uint64(g)
					sumB += uint64(b)
					sumA += uint64(a)
					count++
				}
			}

			// Average and un-premultiply
			if count > 0 {
				avgA := uint16(sumA / uint64(count))
				var avgR, avgG, avgB uint8
				if avgA > 0 {
					// Un-premultiply: (premul * 0xFFFF) / alpha
					avgR = uint8((sumR * 0xFFFF / uint64(count)) / uint64(avgA) >> 8)
					avgG = uint8((sumG * 0xFFFF / uint64(count)) / uint64(avgA) >> 8)
					avgB = uint8((sumB * 0xFFFF / uint64(count)) / uint64(avgA) >> 8)
				}
				dst.SetNRGBA(dx, dy, color.NRGBA{R: avgR, G: avgG, B: avgB, A: uint8(avgA >> 8)})
			}
		}
	}
	return dst
}

// fitImage attempts to downsample an image to the standard vision tier
// (1568px long edge, 1568 visual tokens). Returns the new base64, token
// counts, and ok=true on success. Returns ok=false when the operation is
// declined (unsupported format, already fits, would inflate, etc).
func fitImage(b64, mediaType string) (newB64 string, before, after int, ok bool) {
	// Step 1: validate media type
	if mediaType != "image/png" && mediaType != "image/jpeg" {
		return "", 0, 0, false
	}

	// Step 2: decode base64
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", 0, 0, false
	}

	// Step 3: decode config
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", 0, 0, false
	}
	// Cross-check format matches media type
	if (format == "png" && mediaType != "image/png") ||
		(format == "jpeg" && mediaType != "image/jpeg") {
		return "", 0, 0, false
	}

	// Step 4: compute target size (standard tier: 1568px, 1568 tokens)
	tw, th := resizedSize(cfg.Width, cfg.Height, 1568, 1568)
	if tw == cfg.Width && th == cfg.Height {
		// Already within standard tier
		return "", 0, 0, false
	}

	// Step 5: compute token effect (I5 gate on visual tokens)
	// High-res tier for "before" cost
	hiW, hiH := resizedSize(cfg.Width, cfg.Height, 2576, 4784)
	before = countImageTokens(hiW, hiH)
	after = countImageTokens(tw, th)
	if after >= before {
		return "", 0, 0, false
	}

	// Step 6: decode full image
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", 0, 0, false
	}

	// Step 7: downsample
	dst := boxDownsample(src, tw, th)

	// Step 8: encode in same format
	var buf bytes.Buffer
	if format == "png" {
		enc := &png.Encoder{CompressionLevel: png.DefaultCompression}
		if err := enc.Encode(&buf, dst); err != nil {
			return "", 0, 0, false
		}
	} else {
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
			return "", 0, 0, false
		}
	}

	// Step 9: base64 encode
	newB64 = base64.StdEncoding.EncodeToString(buf.Bytes())

	// Step 10: never inflate
	if len(newB64) >= len(b64) {
		return "", 0, 0, false
	}

	return newB64, before, after, true
}

// imageReplayKey returns a domain-separated replay key for an image.
// The "hr-image\x00" prefix ensures image keys never collide with text keys,
// so an image replacement cannot be spliced into a text field.
func imageReplayKey(mediaType, b64 string) string {
	return ccr.ComputeKey([]byte("hr-image\x00" + mediaType + "\x00" + b64))
}

// visualTokensFromBase64 decodes a base64 image and returns its visual token
// cost at the high-res tier. Used for replay token accounting, where we
// cannot use tok.CountText (that counts text tokens, not visual tokens).
func visualTokensFromBase64(b64 string) int {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return 0
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return 0
	}
	// High-res tier
	w, h := resizedSize(cfg.Width, cfg.Height, 2576, 4784)
	return countImageTokens(w, h)
}
