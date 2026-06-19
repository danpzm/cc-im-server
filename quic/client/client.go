package client

import (
	"context"
	"errors"
	"sync"
	"unsafe"

	"github.com/quic-go/quic-go"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/jwt"
	pkgEvents "github.com/xd/quic-server/pkg/events"
	pkgProtocol "github.com/xd/quic-server/pkg/protocol"
	"github.com/xd/quic-server/pkg/netutil"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/quic/events"
	"github.com/xd/quic-server/quic/handler"
	quicEntity "github.com/xd/quic-server/quic/handler/entity"
)

// Client 客户端连接管理
type Client struct {
	user                *types.User
	sid                 string // UserSession.Sid，与 uid 同存于 token，由 handleConnect 从 AuthResult 设置
	conn                *quic.Conn
	eventBus            *events.EventEngine
	streamHandlers      map[pkgProtocol.StreamType][]handler.StreamHandler
	deviceInfo          entity.DeviceInfoJSON
	deviceInfoMu        sync.RWMutex      // 保护设备信息的读写
	sidMu               sync.RWMutex      // 保护 sid 的读写
	sessionKey          []byte            // 每次连接会话密钥（用于应用层自定义加解密）
	connectAccessClaims *jwt.CustomClaims // QUIC 认证 access_token 声明，传给消息流 handler
}

// NewClient 创建新的客户端连接（connectAccessClaims 来自认证 access_token，供消息流建立 token 刷新调度）
func NewClient(conn *quic.Conn, user *types.User, deviceInfo entity.DeviceInfoJSON, eventBus *events.EventEngine, connectAccessClaims *jwt.CustomClaims) *Client {
	return &Client{
		conn:                conn,
		user:                user,
		eventBus:            eventBus,
		streamHandlers:      make(map[pkgProtocol.StreamType][]handler.StreamHandler),
		deviceInfo:          deviceInfo,
		connectAccessClaims: connectAccessClaims,
	}
}

// DeviceInfo 返回设备信息
func (c *Client) DeviceInfo() entity.DeviceInfoJSON {
	c.deviceInfoMu.RLock()
	defer c.deviceInfoMu.RUnlock()
	return c.deviceInfo
}

// UpdateDeviceInfo 更新设备信息
func (c *Client) UpdateDeviceInfo(deviceInfo entity.DeviceInfoJSON) {
	c.deviceInfoMu.Lock()
	defer c.deviceInfoMu.Unlock()
	c.deviceInfo = deviceInfo
	log.Infof("客户端 %s 设备信息已更新: %+v", c.user.Uid, deviceInfo)
}

// User 返回用户信息
func (c *Client) User() *types.User {
	return c.user
}

// SetSid 设置会话 ID（来自 token，由 handleConnect 调用）
func (c *Client) SetSid(sid string) {
	c.sidMu.Lock()
	defer c.sidMu.Unlock()
	c.sid = sid
}

// Sid 返回会话 ID
func (c *Client) Sid() string {
	c.sidMu.RLock()
	defer c.sidMu.RUnlock()
	return c.sid
}

// SetSessionKey 设置“会话密钥”（每次连接都会重新协商）
func (c *Client) SetSessionKey(key []byte) {
	c.sessionKey = key
}

// Conn 返回连接（用于服务端管理）
func (c *Client) Conn() *quic.Conn {
	return c.conn
}

// ConnID 返回连接实例标识（用于区分同 uid+sid 下的新旧 QUIC 连接）
func (c *Client) ConnID() uintptr {
	if c.conn == nil {
		return 0
	}
	return uintptr(unsafe.Pointer(c.conn))
}

// Start 启动客户端连接处理
func (c *Client) Start() {
	go c.acceptConn()
}

// acceptConn 接受新的连接流
func (c *Client) acceptConn() {
	// QUIC 连接结束（含客户端优雅关闭）时发布，使 onClientClose -> handleUserOffline 立即执行；
	// 此前从未 Publish EventClientClose，下线只能等心跳超时（约 14s+）。
	defer func() {
		sid := c.Sid()
		if c.eventBus != nil {
			c.eventBus.Publish(pkgEvents.EventClientClose, pkgEvents.ClientCloseEvent{
				Uid:    c.user.Uid,
				Sid:    sid,
				Reason: "quic connection closed",
				ConnID: c.ConnID(),
			})
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("客户端 %s acceptConn panic: %v", c.user.Uid, r)
		}
	}()

	for {
		stream, err := c.conn.AcceptStream(context.Background())
		if err != nil {
			// 连接关闭或错误，退出循环
			log.Debugf("客户端 %s 接受流失败（连接可能已关闭）: %v", c.user.Uid, err)
			return
		}
		// 为每个流启动独立的 goroutine 处理
		go c.handleStream(stream)
	}
}
func (c *Client) registerStreamHandler(streamType pkgProtocol.StreamType, handler handler.StreamHandler) {
	c.streamHandlers[streamType] = append(c.streamHandlers[streamType], handler)
}
func (c *Client) unregisterStreamHandler(streamType pkgProtocol.StreamType, handler handler.StreamHandler) {
	handlers := c.streamHandlers[streamType]
	for i, h := range handlers {
		if h == handler {
			handlers = append(handlers[:i], handlers[i+1:]...)
			c.streamHandlers[streamType] = handlers
			break
		}
	}
}

// handleStream 处理新的流
func (c *Client) handleStream(s *quic.Stream) {
	ip := netutil.ExtractIP(c.conn.RemoteAddr().String())
	streamType, err := handler.GetStreamType(s)
	if err != nil {
		log.Errorf("客户端 %s 获取流类型失败: %v", c.user.Uid, err)
		return
	}
	handlerFunc, err := handler.NewStreamHandler(streamType)
	if err != nil {
		log.Errorf("客户端 %s 创建流处理器失败: %v", c.user.Uid, err)
		return
	}
	handler := handlerFunc(handler.NewBaseHandlerProps(c.eventBus, s, c.user, ip, c.Sid(), c.ConnID(), c.sessionKey, c.connectAccessClaims))
	c.registerStreamHandler(streamType, handler)
	err = handler.Handle()
	if err != nil {
		log.Errorf("客户端 %s 处理流失败: %v", c.user.Uid, err)
		c.unregisterStreamHandler(streamType, handler)
		return
	}
}
func (c *Client) GetMessageStreamHandler() *handler.MessageHandler {
	handlers := c.streamHandlers[pkgProtocol.StreamTypeMessage]
	if len(handlers) == 0 {
		return nil
	}
	return handlers[0].(*handler.MessageHandler)
}
func (c *Client) SendRoomMessage(message types.ServerRoomMessage) error {
	handler := c.GetMessageStreamHandler()
	if handler == nil {
		return errors.New("消息流处理器不存在")
	}
	data, err := handler.DataEncode(message)
	if err != nil {
		log.Errorf("客户端 %s 编码消息失败: %v", c.user.Uid, err)
		return err
	}
	log.Infof("\n\n客户端 %s 发送消息: %+v\n\n", c.user.Uid, message)
	err = handler.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomMessage,
		Data:        data,
	})
	if err != nil {
		log.Errorf("客户端 %s 发送消息失败: %v", c.user.Uid, err)
		return err
	}
	return nil
}

// SendRoomMessageWithdraw 发送房间消息撤回
func (c *Client) SendRoomMessageWithdraw(message types.ServerRoomMessageWithdraw) error {
	handler := c.GetMessageStreamHandler()
	if handler == nil {
		return errors.New("消息流处理器不存在")
	}
	data, err := handler.DataEncode(message)
	if err != nil {
		log.Errorf("客户端 %s 编码消息撤回失败: %v", c.user.Uid, err)
		return err
	}
	err = handler.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomMessageWithdraw,
		Data:        data,
	})
	if err != nil {
		log.Errorf("客户端 %s 发送消息撤回失败: %v", c.user.Uid, err)
		return err
	}
	return nil
}
func (c *Client) EncodeServerMessageData(data any) ([]byte, error) {
	handler := c.GetMessageStreamHandler()
	if handler == nil {
		return nil, errors.New("消息流处理器不存在")
	}
	return handler.DataEncode(data)
}
func (c *Client) SendServerMessage(message types.ServerMessageEntity) error {
	handler := c.GetMessageStreamHandler()
	if handler == nil {
		return errors.New("消息流处理器不存在")
	}
	err := handler.SendMessage(message)
	if err != nil {
		log.Errorf("客户端 %s 发送消息失败: %v", c.user.Uid, err)
		return err
	}
	return nil
}

// SendRoomMessages 批量发送房间消息（下发未读）
func (c *Client) SendRoomMessages(messages []types.ServerRoomMessage) error {
	handler := c.GetMessageStreamHandler()
	if handler == nil {
		return errors.New("消息流处理器不存在")
	}
	batch := quicEntity.ServerRoomMessageBatch{
		Messages: messages,
	}
	data, err := handler.DataEncode(batch)
	if err != nil {
		log.Errorf("客户端 %s 编码批量消息失败: %v", c.user.Uid, err)
		return err
	}
	err = handler.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomMessageBatch,
		Data:        data,
	})
	if err != nil {
		log.Errorf("客户端 %s 发送批量消息失败: %v", c.user.Uid, err)
		return err
	}
	return nil
}

// SendRoomUserRoomNicknameUpdate 发送房间内用户群昵称变更（不落库，仅静默更新 UI）
func (c *Client) SendRoomUserRoomNicknameUpdate(rid, uid, roomNickname string) error {
	handler := c.GetMessageStreamHandler()
	if handler == nil {
		return errors.New("消息流处理器不存在")
	}
	payload := quicEntity.ServerRoomUserRoomNicknameUpdate{
		Rid:          rid,
		Uid:          uid,
		RoomNickname: roomNickname,
	}
	data, err := handler.DataEncode(payload)
	if err != nil {
		log.Errorf("客户端 %s 编码群昵称变更失败: %v", c.user.Uid, err)
		return err
	}
	err = handler.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomUserRoomNicknameUpdate,
		Data:        data,
	})
	if err != nil {
		log.Errorf("客户端 %s 发送群昵称变更失败: %v", c.user.Uid, err)
		return err
	}
	return nil
}

// SendRoomUserIdsUpdate 发送房间成员列表变更（不落库，仅静默更新 UI）
func (c *Client) SendRoomUserIdsUpdate(rid string, userIds []string) error {
	handler := c.GetMessageStreamHandler()
	if handler == nil {
		return errors.New("消息流处理器不存在")
	}
	payload := quicEntity.ServerRoomUserIdsUpdate{
		Rid:     rid,
		UserIds: userIds,
	}
	data, err := handler.DataEncode(payload)
	if err != nil {
		log.Errorf("客户端 %s 编码房间成员列表变更失败: %v", c.user.Uid, err)
		return err
	}
	err = handler.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomUserIdsUpdate,
		Data:        data,
	})
	if err != nil {
		log.Errorf("客户端 %s 发送房间成员列表变更失败: %v", c.user.Uid, err)
		return err
	}
	return nil
}

// SendRoomDissolvedUpdate 发送房间已解散（不落库，仅静默更新 UI）
func (c *Client) SendRoomDissolvedUpdate(rid string, roomState int8) error {
	handler := c.GetMessageStreamHandler()
	if handler == nil {
		return errors.New("消息流处理器不存在")
	}
	payload := quicEntity.ServerRoomDissolvedUpdate{
		Rid:       rid,
		RoomState: roomState,
	}
	data, err := handler.DataEncode(payload)
	if err != nil {
		log.Errorf("客户端 %s 编码房间解散状态失败: %v", c.user.Uid, err)
		return err
	}
	err = handler.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomDissolvedUpdate,
		Data:        data,
	})
	if err != nil {
		log.Errorf("客户端 %s 发送房间解散状态失败: %v", c.user.Uid, err)
		return err
	}
	return nil
}

// SendRoomAvatarUpdate 发送房间头像变更（不落库，仅静默更新 UI）
func (c *Client) SendRoomAvatarUpdate(rid, avatar string) error {
	handler := c.GetMessageStreamHandler()
	if handler == nil {
		return errors.New("消息流处理器不存在")
	}
	payload := quicEntity.ServerRoomAvatarUpdate{
		Rid:        rid,
		AvatarUfId: avatar,
	}
	data, err := handler.DataEncode(payload)
	if err != nil {
		log.Errorf("客户端 %s 编码房间头像变更失败: %v", c.user.Uid, err)
		return err
	}
	err = handler.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomAvatarUpdate,
		Data:        data,
	})
	if err != nil {
		log.Errorf("客户端 %s 发送房间头像变更失败: %v", c.user.Uid, err)
		return err
	}
	return nil
}

// SendUserProfileChange 发送用户资料变更（懒推送，同房间用户去重后每人一条）
func (c *Client) SendUserProfileChange(userInfo quicEntity.ServerUserInfo) error {
	handler := c.GetMessageStreamHandler()
	if handler == nil {
		return errors.New("消息流处理器不存在")
	}
	data, err := handler.DataEncode(userInfo)
	if err != nil {
		log.Errorf("客户端 %s 编码用户资料变更失败: %v", c.user.Uid, err)
		return err
	}
	err = handler.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeUserProfileChange,
		Data:        data,
	})
	if err != nil {
		log.Errorf("客户端 %s 发送用户资料变更失败: %v", c.user.Uid, err)
		return err
	}
	return nil
}

// SendRoomMessagesWithdraw 批量发送房间消息撤回（下发未读）
func (c *Client) SendRoomMessagesWithdraw(messages []types.ServerRoomMessageWithdraw) error {
	handler := c.GetMessageStreamHandler()
	if handler == nil {
		return errors.New("消息流处理器不存在")
	}
	batch := quicEntity.ServerRoomMessageWithdrawBatch{
		Messages: messages,
	}
	data, err := handler.DataEncode(batch)
	if err != nil {
		log.Errorf("客户端 %s 编码批量撤回消息失败: %v", c.user.Uid, err)
		return err
	}
	err = handler.SendMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomMessageWithdrawBatch,
		Data:        data,
	})
	if err != nil {
		log.Errorf("客户端 %s 发送批量撤回消息失败: %v", c.user.Uid, err)
		return err
	}
	return nil
}
