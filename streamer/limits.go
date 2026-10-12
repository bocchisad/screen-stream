package streamer

const (
	// MaxWireMessage is the largest reassembled DirtyPacket (DoS cap).
	MaxWireMessage = 32 << 20 // 32 MiB
	// MaxChunkFragments caps SSCH fragment count per message.
	MaxChunkFragments = 2048
	// MaxPendingChunks caps concurrent incomplete messages.
	MaxPendingChunks = 64
	// MaxDecompressedPayload caps zlib expansion.
	MaxDecompressedPayload = 64 << 20 // 64 MiB
	// MaxFramePixels caps reconstructed canvas (≈ 8K UHD).
	MaxFramePixels = 7680 * 4320
	// MaxRegionCount caps regions in one DirtyPacket.
	MaxRegionCount = 8192
	// MaxSignalingWait is how long hub GETs may block.
	MaxSignalingWaitSeconds = 90
	// MaxPreviewClients limits concurrent MJPEG viewers.
	MaxPreviewClients = 8
)
