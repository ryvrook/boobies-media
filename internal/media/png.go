package media

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// PNG colour types from the specification. Only these two are in scope for
// lossless webp conversion.
const (
	PNGColorRGB  = 2
	PNGColorRGBA = 6
)

// webpMaxDimension is the hard limit of the WebP format. Larger images cannot
// be encoded at all.
const webpMaxDimension = 16383

var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// PNGInfo is what the optimizer needs to decide whether a PNG is convertible.
type PNGInfo struct {
	Width     int
	Height    int
	BitDepth  int
	ColorType int
	Animated  bool // an acTL chunk appeared before IDAT
}

// InspectPNG parses the IHDR chunk and looks for an acTL chunk before the
// first IDAT. It reads structure only and never decodes pixels.
func InspectPNG(data []byte) (*PNGInfo, error) {
	if len(data) < len(pngMagic)+8+13 {
		return nil, errors.New("media: data is too short to be a PNG")
	}
	for i, b := range pngMagic {
		if data[i] != b {
			return nil, errors.New("media: not a PNG")
		}
	}
	// The first chunk must be IHDR: length(4) type(4) then 13 bytes of payload.
	if string(data[12:16]) != "IHDR" {
		return nil, fmt.Errorf("media: first PNG chunk is %q, want IHDR", string(data[12:16]))
	}
	info := &PNGInfo{
		Width:     int(binary.BigEndian.Uint32(data[16:20])),
		Height:    int(binary.BigEndian.Uint32(data[20:24])),
		BitDepth:  int(data[24]),
		ColorType: int(data[25]),
	}
	info.Animated = hasChunkBeforeIDAT(data, "acTL")
	return info, nil
}

// hasChunkBeforeIDAT walks the chunk list, stopping at the first IDAT. An APNG
// always declares acTL before the image data, so scanning further would only
// produce false positives from arbitrary trailing bytes.
func hasChunkBeforeIDAT(data []byte, want string) bool {
	offset := len(pngMagic)
	for offset+8 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		chunkType := string(data[offset+4 : offset+8])
		if chunkType == want {
			return true
		}
		if chunkType == "IDAT" || chunkType == "IEND" {
			return false
		}
		// Guard against a hostile or corrupt length: 12 is the per-chunk
		// overhead (length + type + CRC).
		if length < 0 || length > len(data) {
			return false
		}
		next := offset + 12 + length
		if next <= offset {
			return false
		}
		offset = next
	}
	return false
}

// EligibleForLosslessWebp reports whether cwebp -lossless can convert this PNG
// with genuinely zero quality loss. Everything outside 8-bit RGB/RGBA is
// excluded: 16-bit channels get downsampled, palette and greyscale variants
// have edge cases, and an APNG would lose every frame but the first.
func (p *PNGInfo) EligibleForLosslessWebp() bool {
	if p == nil || p.Animated {
		return false
	}
	if p.Width <= 0 || p.Height <= 0 {
		return false
	}
	if p.Width > webpMaxDimension || p.Height > webpMaxDimension {
		return false
	}
	if p.BitDepth != 8 {
		return false
	}
	return p.ColorType == PNGColorRGB || p.ColorType == PNGColorRGBA
}
