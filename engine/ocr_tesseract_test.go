package engine

import (
	"context"
	"testing"
)

func TestTesseractUnavailableNoPanic(t *testing.T) {
	b := &TesseractOCRBackend{Bin: "tesseract-definitely-missing-xyz"}
	_, err := b.ScanFrame(context.Background(), FrameMeta{Width: 8, Height: 8, Stride: 32}, make([]byte, 256))
	if err == nil {
		t.Fatal("expected error when binary missing")
	}
}

func TestParseTesseractTSV(t *testing.T) {
	tsv := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"5\t1\t1\t1\t1\t1\t10\t20\t100\t24\t90\tghp_ABCDEFGHIJKLMNOPQRSTUV\n"
	lines := parseTesseractTSV(tsv)
	if len(lines) != 1 || lines[0].Text == "" {
		t.Fatalf("got %+v", lines)
	}
}
