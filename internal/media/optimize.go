package media

import (
	"context"
	"os"
)

// OptimizeDecision records whether a file should be converted and, when it
// should not, why. The reason is surfaced on the admin page.
type OptimizeDecision struct {
	Convert bool
	Reason  string
}

// DecideWebpConversion applies the narrowed policy: static 8-bit RGB/RGBA PNG
// only. Everything else keeps its original bytes.
func DecideWebpConversion(mime string, header []byte, enabled bool) OptimizeDecision {
	if !enabled {
		return OptimizeDecision{Reason: "auto_webp is off"}
	}
	if mime != "image/png" {
		// JPEG is excluded because lossless webp of a JPEG is typically
		// larger, and a lossy re-encode would violate the no-quality-loss
		// constraint. GIF is excluded because conversion is animation-risky.
		return OptimizeDecision{Reason: "only PNG is in scope for lossless webp conversion"}
	}
	info, err := InspectPNG(header)
	if err != nil {
		return OptimizeDecision{Reason: "PNG header could not be parsed"}
	}
	switch {
	case info.Animated:
		return OptimizeDecision{Reason: "APNG: cwebp would keep only the first frame"}
	case info.BitDepth != 8:
		return OptimizeDecision{Reason: "not 8-bit: cwebp would downsample the channels"}
	case info.ColorType != PNGColorRGB && info.ColorType != PNGColorRGBA:
		return OptimizeDecision{Reason: "not RGB or RGBA: outside the narrowed lossless scope"}
	case info.Width > webpMaxDimension || info.Height > webpMaxDimension:
		return OptimizeDecision{Reason: "larger than webp's 16383-pixel limit"}
	case info.Width <= 0 || info.Height <= 0:
		return OptimizeDecision{Reason: "zero-sized image"}
	}
	return OptimizeDecision{Convert: true}
}

// Optimizer converts eligible PNGs to lossless webp.
type Optimizer struct {
	Runner Runner
	TmpDir string
}

// NewOptimizer returns an Optimizer writing scratch files into tmpDir.
func NewOptimizer(runner Runner, tmpDir string) *Optimizer {
	return &Optimizer{Runner: runner, TmpDir: tmpDir}
}

// Optimize converts srcPath to lossless webp when policy allows and the result
// is genuinely smaller. It returns the path and mime to store, and whether a
// conversion happened.
//
// Every conversion failure degrades silently to the original: a missing
// cwebp, a non-zero exit, an output that is not smaller, and scratch-space
// I/O failures (the temp file could not be created or closed) all return the
// original path/mime with converted=false and a nil error. The only error
// Optimize returns is one that reflects the original itself being broken:
// currently just os.Stat on srcPath failing, since a source that cannot even
// be stat'd was never going to survive the rest of the job either.
func (o *Optimizer) Optimize(ctx context.Context, srcPath, mime string, header []byte, enabled bool) (string, string, bool, error) {
	if decision := DecideWebpConversion(mime, header, enabled); !decision.Convert {
		return srcPath, mime, false, nil
	}

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return "", "", false, err
	}

	outFile, err := os.CreateTemp(o.TmpDir, "webp-*.webp")
	if err != nil {
		// Scratch space is unwritable/exhausted, but srcPath is untouched: keep
		// the original rather than failing the job over it.
		return srcPath, mime, false, nil
	}
	outPath := outFile.Name()
	// cwebp writes the file itself; close the handle so it can.
	if err := outFile.Close(); err != nil {
		_ = os.Remove(outPath)
		return srcPath, mime, false, nil
	}
	cleanup := func() { _ = os.Remove(outPath) }

	// -lossless is what makes the conversion quality-preserving by
	// construction; -metadata all keeps EXIF and ICC so colour-managed images
	// do not shift.
	if _, err := o.Runner.Run(ctx, "cwebp", "-quiet", "-lossless", "-metadata", "all", "-o", outPath, "--", srcPath); err != nil {
		cleanup()
		return srcPath, mime, false, nil
	}

	outInfo, err := os.Stat(outPath)
	if err != nil || outInfo.Size() == 0 || outInfo.Size() >= srcInfo.Size() {
		cleanup()
		return srcPath, mime, false, nil
	}

	// Sanity check the result really is a webp before adopting it. A bare
	// "RIFF" prefix is not enough (WAV and AVI share it too), so this reuses
	// the same Sniff that guards every other file admitted into storage,
	// rather than maintaining a second, weaker magic check with its own probe
	// length.
	produced, err := os.Open(outPath)
	if err != nil {
		cleanup()
		return srcPath, mime, false, nil
	}
	probe := make([]byte, SniffLen)
	n, _ := produced.Read(probe)
	_ = produced.Close()
	if Sniff(probe[:n]) != "image/webp" {
		cleanup()
		return srcPath, mime, false, nil
	}

	return outPath, "image/webp", true, nil
}
