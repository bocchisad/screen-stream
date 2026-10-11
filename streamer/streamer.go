package streamer

import "fmt"

func DescribeHandoff() string {
	return fmt.Sprintf("engine.DirtyPacket → DeliveryRing(3) → DataChannel(%q) binary", DefaultChannelLabel)
}
