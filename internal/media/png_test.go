package media

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

// buildPNG assembles a syntactically valid PNG with the given IHDR values and
// optional extra chunks placed before IDAT.
func buildPNG(t *testing.T, width, height uint32, bitDepth, colorType byte, chunksBeforeIDAT ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})

	writeChunk := func(typ string, payload []byte) {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
		buf.Write(length[:])
		body := append([]byte(typ), payload...)
		buf.Write(body)
		var crc [4]byte
		binary.BigEndian.PutUint32(crc[:], crc32.ChecksumIEEE(body))
		buf.Write(crc[:])
	}

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = bitDepth
	ihdr[9] = colorType
	writeChunk("IHDR", ihdr)

	for _, typ := range chunksBeforeIDAT {
		writeChunk(typ, []byte{0, 0, 0, 1, 0, 0, 0, 0})
	}
	writeChunk("IDAT", []byte{0x78, 0x9c, 0x63, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01})
	writeChunk("IEND", nil)
	return buf.Bytes()
}

func TestInspectPNGReadsIHDR(t *testing.T) {
	data := buildPNG(t, 640, 480, 8, PNGColorRGB)
	info, err := InspectPNG(data)
	if err != nil {
		t.Fatalf("InspectPNG: %v", err)
	}
	if info.Width != 640 || info.Height != 480 {
		t.Errorf("dimensions = %dx%d, want 640x480", info.Width, info.Height)
	}
	if info.BitDepth != 8 {
		t.Errorf("BitDepth = %d, want 8", info.BitDepth)
	}
	if info.ColorType != PNGColorRGB {
		t.Errorf("ColorType = %d, want %d", info.ColorType, PNGColorRGB)
	}
	if info.Animated {
		t.Error("Animated = true for a still PNG")
	}
}

func TestInspectPNGDetectsAPNG(t *testing.T) {
	// acTL before IDAT is what makes a PNG an APNG.
	data := buildPNG(t, 64, 64, 8, PNGColorRGBA, "acTL")
	info, err := InspectPNG(data)
	if err != nil {
		t.Fatalf("InspectPNG: %v", err)
	}
	if !info.Animated {
		t.Fatal("Animated = false for an APNG; cwebp would silently keep only frame 1")
	}
	if info.EligibleForLosslessWebp() {
		t.Fatal("an APNG must never be eligible for webp conversion")
	}
}

func TestInspectPNGIgnoresChunksAfterIDAT(t *testing.T) {
	// A trailing acTL is not a valid animation control chunk; the walk must
	// stop at IDAT rather than scanning the whole file for the four bytes.
	data := buildPNG(t, 64, 64, 8, PNGColorRGB)
	data = append(data, []byte("\x00\x00\x00\x08acTLxxxxxxxxCRC1")...)
	info, err := InspectPNG(data)
	if err != nil {
		t.Fatalf("InspectPNG: %v", err)
	}
	if info.Animated {
		t.Error("Animated = true because of bytes after IDAT; the chunk walk does not stop at IDAT")
	}
}

func TestEligibleForLosslessWebp(t *testing.T) {
	cases := []struct {
		name string
		info PNGInfo
		want bool
		why  string
	}{
		{"8-bit RGB", PNGInfo{Width: 100, Height: 100, BitDepth: 8, ColorType: PNGColorRGB}, true, ""},
		{"8-bit RGBA", PNGInfo{Width: 100, Height: 100, BitDepth: 8, ColorType: PNGColorRGBA}, true, ""},
		{"16-bit RGB", PNGInfo{Width: 100, Height: 100, BitDepth: 16, ColorType: PNGColorRGB}, false,
			"cwebp downsamples 16-bit to 8-bit, which is real quality loss"},
		{"palette", PNGInfo{Width: 100, Height: 100, BitDepth: 8, ColorType: 3}, false, "outside the narrowed scope"},
		{"greyscale", PNGInfo{Width: 100, Height: 100, BitDepth: 8, ColorType: 0}, false, "outside the narrowed scope"},
		{"greyscale+alpha", PNGInfo{Width: 100, Height: 100, BitDepth: 8, ColorType: 4}, false, "outside the narrowed scope"},
		{"animated", PNGInfo{Width: 100, Height: 100, BitDepth: 8, ColorType: PNGColorRGB, Animated: true}, false,
			"cwebp would keep only the first frame"},
		{"too wide", PNGInfo{Width: 16384, Height: 100, BitDepth: 8, ColorType: PNGColorRGB}, false,
			"webp maxes out at 16383 pixels per side"},
		{"too tall", PNGInfo{Width: 100, Height: 16384, BitDepth: 8, ColorType: PNGColorRGB}, false,
			"webp maxes out at 16383 pixels per side"},
		{"exactly at the limit", PNGInfo{Width: 16383, Height: 16383, BitDepth: 8, ColorType: PNGColorRGB}, true, ""},
		{"zero size", PNGInfo{Width: 0, Height: 0, BitDepth: 8, ColorType: PNGColorRGB}, false, "not a real image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.EligibleForLosslessWebp(); got != tc.want {
				t.Errorf("EligibleForLosslessWebp() = %v, want %v (%s)", got, tc.want, tc.why)
			}
		})
	}
}

func TestInspectPNGRejectsMalformedInput(t *testing.T) {
	cases := map[string][]byte{
		"empty":            {},
		"not a png":        []byte("GIF89a and then some padding bytes here"),
		"truncated header": {0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a},
		"no IHDR": append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a},
			[]byte("\x00\x00\x00\x0dNOPEaaaaaaaaaaaaaCRC1")...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectPNG(data); err == nil {
				t.Fatal("InspectPNG accepted malformed input, want an error")
			}
		})
	}
}

func TestInspectPNGSurvivesAHostileChunkLength(t *testing.T) {
	// A chunk claiming a gigantic length must not panic or loop forever.
	data := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a},
		[]byte("\x00\x00\x00\x0dIHDR\x00\x00\x00\x64\x00\x00\x00\x64\x08\x02\x00\x00\x00CRC1")...)
	data = append(data, []byte("\x7f\xff\xff\xffjunk")...)
	info, err := InspectPNG(data)
	if err != nil {
		t.Fatalf("InspectPNG: %v", err)
	}
	if info.Animated {
		t.Error("a hostile chunk length produced Animated = true")
	}
}
