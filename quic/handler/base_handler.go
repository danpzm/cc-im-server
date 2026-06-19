package handler

import (
	"github.com/quic-go/quic-go"
	"github.com/xd/quic-server/jwt"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/quic/events"
)

type StreamHandler interface {
	Handle() error
}
type BaseHandlerProps struct {
	eventBus   *events.EventEngine
	stream     *quic.Stream
	user       *types.User
	ip         string
	sid        string // UserSession.Sid，与 uid 同存于 token
	connID     uintptr // QUIC 连接实例，心跳/下线事件用于区分同 sid 重连
	sessionKey []byte
	// ConnectAccessClaims QUIC 认证 access_token 的声明；仅消息流用于建立 token 过期前下发提醒。
	ConnectAccessClaims *jwt.CustomClaims
}
type BaseHandler struct {
	StreamHandler
	BaseHandlerProps
}

func (h *BaseHandler) Handle() error {
	return nil
}
