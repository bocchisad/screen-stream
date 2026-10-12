package engine

import "context"

// RedactBGRA runs PII detection and blacks out hits in-place (H.264 path, no DirtyPacket).
func RedactBGRA(ctx context.Context, d PIIDetector, meta FrameMeta, bgra []byte, stride int) error {
	if d == nil || len(bgra) == 0 {
		return nil
	}
	if stride <= 0 {
		stride = int(meta.Width) * 4
	}
	rects, err := d.Detect(ctx, meta, bgra)
	if err != nil {
		return err
	}
	for _, r := range rects {
		fillBlack(bgra, stride, int(r.X), int(r.Y), int(r.W), int(r.H))
	}
	return nil
}
