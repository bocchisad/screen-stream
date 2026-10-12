package streamer

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/salfetka/screen-stream/engine"
)

var (
	ErrBadMagic   = errors.New("bad packet magic")
	ErrBadVersion = errors.New("unsupported packet version")
	ErrTruncated  = errors.New("truncated packet")
)

const headerSize = 32
const regionSize = 16

// Pack serializes DirtyPacket (allocates). Prefer PackInto on hot path.
func Pack(pkt *engine.DirtyPacket) []byte {
	n := PackSize(pkt)
	buf := make([]byte, n)
	_, _ = PackInto(pkt, buf)
	return buf
}

func PackSize(pkt *engine.DirtyPacket) int {
	return headerSize + len(pkt.Regions)*regionSize + len(pkt.Payload)
}

// PackInto writes packet into dst; returns bytes written.
func PackInto(pkt *engine.DirtyPacket, dst []byte) (int, error) {
	n := PackSize(pkt)
	if len(dst) < n {
		return 0, fmt.Errorf("%w: need %d", engine.ErrBufferTooSmall, n)
	}
	o := 0
	binary.LittleEndian.PutUint32(dst[o:], pkt.Magic)
	o += 4
	binary.LittleEndian.PutUint16(dst[o:], pkt.Version)
	o += 2
	binary.LittleEndian.PutUint16(dst[o:], pkt.Flags)
	o += 2
	binary.LittleEndian.PutUint64(dst[o:], pkt.FrameID)
	o += 8
	binary.LittleEndian.PutUint64(dst[o:], pkt.PTSUs)
	o += 8
	binary.LittleEndian.PutUint16(dst[o:], pkt.Width)
	o += 2
	binary.LittleEndian.PutUint16(dst[o:], pkt.Height)
	o += 2
	binary.LittleEndian.PutUint16(dst[o:], pkt.BlockSize)
	o += 2
	binary.LittleEndian.PutUint16(dst[o:], uint16(len(pkt.Regions)))
	o += 2
	for _, r := range pkt.Regions {
		binary.LittleEndian.PutUint16(dst[o:], r.X)
		o += 2
		binary.LittleEndian.PutUint16(dst[o:], r.Y)
		o += 2
		binary.LittleEndian.PutUint16(dst[o:], r.W)
		o += 2
		binary.LittleEndian.PutUint16(dst[o:], r.H)
		o += 2
		binary.LittleEndian.PutUint32(dst[o:], r.PayloadOffset)
		o += 4
		binary.LittleEndian.PutUint32(dst[o:], r.PayloadLen)
		o += 4
	}
	copy(dst[o:], pkt.Payload)
	o += len(pkt.Payload)
	return o, nil
}

// Unpacked is a decoded DirtyPacket view; Payload/Regions point into the input buffer
// for Regions a copy is made into RegionsOwn when using UnpackOwned.
type Unpacked struct {
	Magic     uint32
	Version   uint16
	Flags     uint16
	FrameID   uint64
	PTSUs     uint64
	Width     uint16
	Height    uint16
	BlockSize uint16
	Regions   []engine.DirtyRegion
	Payload   []byte // slice into src
}

// Unpack decodes wire bytes. Regions slice is newly allocated; Payload aliases src.
func Unpack(src []byte) (*Unpacked, error) {
	if len(src) < headerSize {
		return nil, ErrTruncated
	}
	u := &Unpacked{}
	o := 0
	u.Magic = binary.LittleEndian.Uint32(src[o:])
	o += 4
	if u.Magic != engine.PacketMagic {
		return nil, ErrBadMagic
	}
	u.Version = binary.LittleEndian.Uint16(src[o:])
	o += 2
	if u.Version != 1 {
		return nil, ErrBadVersion
	}
	u.Flags = binary.LittleEndian.Uint16(src[o:])
	o += 2
	u.FrameID = binary.LittleEndian.Uint64(src[o:])
	o += 8
	u.PTSUs = binary.LittleEndian.Uint64(src[o:])
	o += 8
	u.Width = binary.LittleEndian.Uint16(src[o:])
	o += 2
	u.Height = binary.LittleEndian.Uint16(src[o:])
	o += 2
	u.BlockSize = binary.LittleEndian.Uint16(src[o:])
	o += 2
	rc := int(binary.LittleEndian.Uint16(src[o:]))
	o += 2
	if rc > MaxRegionCount {
		return nil, ErrTruncated
	}
	need := headerSize + rc*regionSize
	if len(src) < need {
		return nil, ErrTruncated
	}
	if len(src) > MaxWireMessage {
		return nil, ErrTruncated
	}
	u.Regions = make([]engine.DirtyRegion, rc)
	for i := 0; i < rc; i++ {
		u.Regions[i] = engine.DirtyRegion{
			Rect: engine.Rect{
				X: binary.LittleEndian.Uint16(src[o:]),
				Y: binary.LittleEndian.Uint16(src[o+2:]),
				W: binary.LittleEndian.Uint16(src[o+4:]),
				H: binary.LittleEndian.Uint16(src[o+6:]),
			},
			PayloadOffset: binary.LittleEndian.Uint32(src[o+8:]),
			PayloadLen:    binary.LittleEndian.Uint32(src[o+12:]),
		}
		o += regionSize
	}
	u.Payload = src[o:]
	return u, nil
}
