package engine

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// TesseractAvailable reports whether the tesseract binary is on PATH.
func TesseractAvailable() bool {
	_, err := exec.LookPath("tesseract")
	return err == nil
}

// TesseractOCRBackend shells out to `tesseract` (TSV) — no libtesseract link.
type TesseractOCRBackend struct {
	Bin string // default "tesseract"
}

func NewTesseractOCRBackend() *TesseractOCRBackend {
	return &TesseractOCRBackend{Bin: "tesseract"}
}

func (t *TesseractOCRBackend) ScanFrame(ctx context.Context, meta FrameMeta, bgra []byte) ([]OCRLine, error) {
	bin := t.Bin
	if bin == "" {
		bin = "tesseract"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("%w: tesseract not found", ErrNotSupported)
	}
	stride := meta.Stride
	if stride <= 0 {
		stride = int(meta.Width) * 4
	}
	w, h := int(meta.Width), int(meta.Height)
	if w <= 0 || h <= 0 || len(bgra) < stride*h {
		return nil, nil
	}

	dir, err := os.MkdirTemp("", "ss-ocr-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	imgPath := filepath.Join(dir, "frame.png")
	if err := writeBGRAAsPNG(imgPath, bgra, w, h, stride); err != nil {
		return nil, err
	}

	outBase := filepath.Join(dir, "out")
	cmd := exec.CommandContext(ctx, bin, imgPath, outBase, "-c", "tessedit_create_tsv=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tesseract: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	tsv, err := os.ReadFile(outBase + ".tsv")
	if err != nil {
		return nil, err
	}
	return parseTesseractTSV(string(tsv)), nil
}

func writeBGRAAsPNG(path string, bgra []byte, w, h, stride int) error {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			si := y*stride + x*4
			di := y*img.Stride + x*4
			img.Pix[di+0] = bgra[si+2] // R
			img.Pix[di+1] = bgra[si+1] // G
			img.Pix[di+2] = bgra[si+0] // B
			img.Pix[di+3] = 255
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// TSV columns: level page_num block_num par_num line_num word_num left top width height conf text
func parseTesseractTSV(tsv string) []OCRLine {
	lines := strings.Split(tsv, "\n")
	var out []OCRLine
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 12 {
			continue
		}
		level, _ := strconv.Atoi(cols[0])
		if level != 5 { // word
			continue
		}
		text := strings.TrimSpace(cols[11])
		if text == "" || text == "\t" {
			continue
		}
		left, _ := strconv.Atoi(cols[6])
		top, _ := strconv.Atoi(cols[7])
		ww, _ := strconv.Atoi(cols[8])
		hh, _ := strconv.Atoi(cols[9])
		if ww <= 0 || hh <= 0 {
			continue
		}
		out = append(out, OCRLine{
			Text: text,
			Rect: Rect{X: uint16(left), Y: uint16(top), W: uint16(ww), H: uint16(hh)},
		})
	}
	return out
}

var _ OCRBackend = (*TesseractOCRBackend)(nil)
