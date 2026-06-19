package push

import (
	"encoding/json"

	"github.com/xd/quic-server/notify"
	"github.com/xd/quic-server/pkg/types"
)

// DelegatedRoomResend 跨节点委托重发（与 queue.RoomMessageResendPayload 同构，避免 push 依赖 queue 产生循环引用）
type DelegatedRoomResend struct {
	Uid     string                  `json:"uid"`
	Message types.ServerRoomMessage `json:"message"`
}

// Envelope Redis Pub/Sub 载荷：定向到某一 QUIC 节点
type Envelope struct {
	Message               *notify.Message        `json:"msg,omitempty"`
	DelegatedRoomResend   *DelegatedRoomResend   `json:"delegated_room_resend,omitempty"`
}

// MarshalEnvelope JSON 编码
func MarshalEnvelope(e Envelope) ([]byte, error) {
	return json.Marshal(e)
}

// UnmarshalEnvelope JSON 解码
func UnmarshalEnvelope(data []byte) (Envelope, error) {
	var e Envelope
	err := json.Unmarshal(data, &e)
	return e, err
}
