package streamer

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
)

const (
	ChunkMagic      uint32 = 0x53534348 // "SSCH"
	ChunkHeaderSize        = 16
	DefaultChunkSize       = 60 << 10 // 60 KiB — safe under typical SCTP limits
)

// ChunkReassembler merges SSCH fragments into full DirtyPacket wire buffers.
type ChunkReassembler struct {
	mu        sync.Mutex
	pending   map[uint32]*partialMsg
	onFull    func([]byte)
	pool      sync.Pool
	chunkSize int // must match sender SplitWire chunk size
}

type partialMsg struct {
	total  int
	count  int
	got    int
	buf    []byte
	filled []bool
}

func NewChunkReassembler(onFull func([]byte)) *ChunkReassembler {
	return NewChunkReassemblerSize(onFull, DefaultChunkSize)
}

func NewChunkReassemblerSize(onFull func([]byte), chunkSize int) *ChunkReassembler {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	r := &ChunkReassembler{
		pending:   make(map[uint32]*partialMsg),
		onFull:    onFull,
		chunkSize: chunkSize,
	}
	r.pool.New = func() any {
		b := make([]byte, 0, 1<<20)
		return &b
	}
	return r
}

// Push handles one datagram: either a chunk or a legacy whole packet (SSR1 magic).
func (r *ChunkReassembler) Push(data []byte) {
	if len(data) >= 4 && binary.LittleEndian.Uint32(data) == ChunkMagic {
		r.pushChunk(data)
		return
	}
	if len(data) == 0 || len(data) > MaxWireMessage {
		return
	}
	if r.onFull != nil {
		out := make([]byte, len(data))
		copy(out, data)
		r.onFull(out)
	}
}

func (r *ChunkReassembler) pushChunk(data []byte) {
	if len(data) < ChunkHeaderSize {
		return
	}
	msgID := binary.LittleEndian.Uint32(data[4:])
	index := int(binary.LittleEndian.Uint16(data[8:]))
	count := int(binary.LittleEndian.Uint16(data[10:]))
	total := int(binary.LittleEndian.Uint32(data[12:]))
	payload := data[ChunkHeaderSize:]
	if count <= 0 || count > MaxChunkFragments || index < 0 || index >= count {
		return
	}
	if total <= 0 || total > MaxWireMessage {
		return
	}
	if len(payload) > r.chunkSize+1024 { // small slack for last chunk
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	pm, ok := r.pending[msgID]
	if !ok {
		if len(r.pending) >= MaxPendingChunks {
			// Drop oldest arbitrary entry under flood.
			for id, old := range r.pending {
				r.releaseBuf(old.buf)
				delete(r.pending, id)
				break
			}
		}
		buf := r.allocBuf(total)
		if buf == nil {
			return
		}
		pm = &partialMsg{
			total:  total,
			count:  count,
			buf:    buf,
			filled: make([]bool, count),
		}
		r.pending[msgID] = pm
	} else if pm.total != total || pm.count != count {
		r.releaseBuf(pm.buf)
		delete(r.pending, msgID)
		buf := r.allocBuf(total)
		if buf == nil {
			return
		}
		pm = &partialMsg{
			total:  total,
			count:  count,
			buf:    buf,
			filled: make([]bool, count),
		}
		r.pending[msgID] = pm
	}
	if pm.filled[index] {
		return
	}
	off := index * r.chunkSize
	if off >= total {
		return
	}
	if off+len(payload) > total {
		return
	}
	copy(pm.buf[off:], payload)
	pm.filled[index] = true
	pm.got++
	if pm.got < pm.count {
		return
	}
	delete(r.pending, msgID)
	full := pm.buf
	if r.onFull != nil {
		r.onFull(full)
	}
	r.releaseBuf(full)
}

func (r *ChunkReassembler) allocBuf(total int) []byte {
	if total <= 0 || total > MaxWireMessage {
		return nil
	}
	if bp, ok := r.pool.Get().(*[]byte); ok {
		if cap(*bp) >= total {
			return (*bp)[:total]
		}
		r.pool.Put(bp)
	}
	return make([]byte, total)
}

func (r *ChunkReassembler) releaseBuf(buf []byte) {
	if buf == nil {
		return
	}
	b := buf[:0]
	r.pool.Put(&b)
}

// SplitWire fragments a packed DirtyPacket into SSCH chunks.
func SplitWire(msgID uint32, wire []byte, chunkSize int, scratch []byte, emit func([]byte) error) error {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if len(wire) > MaxWireMessage {
		return ErrTruncated
	}
	need := ChunkHeaderSize + chunkSize
	if cap(scratch) < need {
		scratch = make([]byte, need)
	}
	count := 1
	if len(wire) > chunkSize {
		count = (len(wire) + chunkSize - 1) / chunkSize
	}
	if count > MaxChunkFragments {
		return ErrTruncated
	}
	for i := 0; i < count; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(wire) {
			end = len(wire)
		}
		part := wire[start:end]
		n := ChunkHeaderSize + len(part)
		buf := scratch[:n]
		binary.LittleEndian.PutUint32(buf[0:], ChunkMagic)
		binary.LittleEndian.PutUint32(buf[4:], msgID)
		binary.LittleEndian.PutUint16(buf[8:], uint16(i))
		binary.LittleEndian.PutUint16(buf[10:], uint16(count))
		binary.LittleEndian.PutUint32(buf[12:], uint32(len(wire)))
		copy(buf[ChunkHeaderSize:], part)
		if err := emit(buf); err != nil {
			return err
		}
	}
	return nil
}

var msgIDCounter atomic.Uint32

func NextMsgID() uint32 { return msgIDCounter.Add(1) }
