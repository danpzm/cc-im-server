package handler

import (
	"errors"
	"io"

	"github.com/quic-go/quic-go"
	"github.com/xd/quic-server/jwt"
	"github.com/xd/quic-server/pkg/protocol"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/quic/events"
)

const (
	streamTypeResponseOK = "ok" // 流类型响应确认
)

// GetStreamType 获取流类型
func GetStreamType(stream *quic.Stream) (protocol.StreamType, error) {
	// 读取3字节
	var streamTypeBuf [3]byte
	if _, err := io.ReadFull(stream, streamTypeBuf[:]); err != nil {
		return protocol.StreamType{}, err
	}

	streamType, err := protocol.FromBytes(streamTypeBuf[:])
	if err != nil {
		return protocol.StreamType{}, err
	}

	if _, err := stream.Write([]byte(streamTypeResponseOK)); err != nil {
		return protocol.StreamType{}, err
	}

	return streamType, nil
}

func NewStreamHandler(streamType protocol.StreamType) (func(props BaseHandlerProps) StreamHandler, error) {
	switch streamType {
	case protocol.StreamTypeHeartbeat:
		return func(props BaseHandlerProps) StreamHandler {
			return &HeartbeatHandler{
				BaseHandler: BaseHandler{
					BaseHandlerProps: props,
				},
			}
		}, nil
	case protocol.StreamTypeMessage:
		return func(props BaseHandlerProps) StreamHandler {
			return &MessageHandler{
				BaseHandler: BaseHandler{
					BaseHandlerProps: props,
				},
			}
		}, nil
	default:
		return nil, errors.New("unknown stream type")
	}
}
func NewBaseHandlerProps(eventBus *events.EventEngine, stream *quic.Stream, user *types.User, ip, sid string, connID uintptr, sessionKey []byte, connectAccessClaims *jwt.CustomClaims) BaseHandlerProps {
	return BaseHandlerProps{
		eventBus:            eventBus,
		stream:              stream,
		user:                user,
		ip:                  ip,
		sid:                 sid,
		connID:              connID,
		sessionKey:          sessionKey,
		ConnectAccessClaims: connectAccessClaims,
	}
}
