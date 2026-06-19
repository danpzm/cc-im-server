package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/rs/xid"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	mediacall "github.com/xd/quic-server/http/handler/media"
	pkgEvents "github.com/xd/quic-server/pkg/events"
	"github.com/xd/quic-server/pkg/geoip"
	"github.com/xd/quic-server/pkg/netutil"
	"github.com/xd/quic-server/pkg/publicip"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/push"
	"github.com/xd/quic-server/queue"
	"github.com/xd/quic-server/quic/auth"
	"github.com/xd/quic-server/quic/client"
	quicConfig "github.com/xd/quic-server/quic/config"
	"github.com/xd/quic-server/quic/events"
	quicEntity "github.com/xd/quic-server/quic/handler/entity"
	"github.com/xd/quic-server/quic/heartbeat"
)

const (
	// AuthTimeout 认证超时时间
	AuthTimeout = 10 * time.Second
)

// Server 服务器核心逻辑
type Server struct {
	tlsConfig     *tls.Config
	addr          string
	reg           *clientRegistry
	hbManager     *heartbeat.Manager
	authenticator auth.Authenticator
	eventBus      *events.EventEngine
	isShutdown    bool
	shutdownMux   sync.RWMutex
	queueServer   *queue.Server // 队列服务器实例（用于监听重发消息任务）
	queueClient   *queue.Client // 队列客户端（用于发布任务）
	nodeID        string
	pushCancel    context.CancelFunc
}

var (
	defaultServer   *Server
	defaultServerMu sync.RWMutex
	// ISP信息缓存（避免频繁请求）
	ispCache    = make(map[string]ispCacheEntry)
	ispCacheMu  sync.RWMutex
	ispCacheTTL = 24 * time.Hour // 缓存24小时（ISP信息变化不频繁）
)

type ispCacheEntry struct {
	isp       string
	cacheTime time.Time
}

func heartbeatClientKey(uid, sid string, connID uintptr) string {
	return fmt.Sprintf("%s|%s|%016x", uid, sid, connID)
}

// legacyHeartbeatClientKey 升级前的心跳键（uid|sid），替换连接时需清理避免陈旧超时误触发。
func legacyHeartbeatClientKey(uid, sid string) string {
	return uid + "|" + sid
}

func (s *Server) removeHeartbeatKeys(uid, sid string, connID uintptr) {
	if s.hbManager == nil || uid == "" || sid == "" {
		return
	}
	if connID != 0 {
		s.hbManager.RemoveClient(heartbeatClientKey(uid, sid, connID))
	}
	s.hbManager.RemoveClient(legacyHeartbeatClientKey(uid, sid))
}

func parseHeartbeatClientKey(clientKey string) (uid, sid string, connID uintptr, ok bool) {
	parts := strings.SplitN(clientKey, "|", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", 0, false
	}
	if len(parts) == 2 {
		return parts[0], parts[1], 0, true
	}
	id, err := strconv.ParseUint(parts[2], 16, 64)
	if err != nil {
		return parts[0], parts[1], 0, true
	}
	return parts[0], parts[1], uintptr(id), true
}

// NewServer 创建新的服务器实例
// queueServer: 队列服务器实例（用于监听重发消息任务）
// queueClient: 队列客户端实例（用于发布任务和ACK检查任务，使用默认队列的DB）
// nodeID: 集群节点 ID（与 Redis 定向推送一致）
func NewServer(
	tlsConfig *tls.Config,
	addr string,
	queueServer *queue.Server,
	queueClient *queue.Client,
	nodeID string,
) *Server {
	// 创建事件总线
	eventBus := events.NewEventEngine()

	// 创建心跳管理器
	hbManager := heartbeat.NewManager(quicConfig.HeartbeatTimeout, quicConfig.HeartbeatCheckInterval)

	server := &Server{
		tlsConfig:     tlsConfig,
		addr:          addr,
		reg:           newClientRegistry(),
		hbManager:     hbManager,
		authenticator: auth.NewDefaultAuthenticator(),
		eventBus:      eventBus,
		isShutdown:    false,
		queueServer: queueServer,
		queueClient: queueClient,
		nodeID:      nodeID,
	}

	// 订阅事件
	server.subscribeEvents()

	// 启动心跳管理器，传入超时处理回调（发布事件）
	hbManager.Start(server.handleClientTimeout)

	// 注册重发消息任务处理器
	if queueServer != nil {
		queue.Handle(queueServer, queue.TaskRoomMessageResend, server.HandleRoomMessageResend)
		queue.Handle(queueServer, queue.TaskFileStatusUpdateAckCheck, server.HandleFileStatusUpdateAckCheck)
		queue.Handle(queueServer, queue.TaskRoomMessageWithdrawResend, server.HandleRoomMessageWithdrawResend)
		log.Info("Quic服务器已注册重发消息任务处理器和文件状态更新ACK检查任务处理器")
	} else {
		log.Warn("队列服务器未设置，无法注册重发消息任务处理器")
	}

	pushCtx, pushCancel := context.WithCancel(context.Background())
	server.pushCancel = pushCancel
	go func() {
		sub := push.Subscriber{
			NodeID:  nodeID,
			Handler: server,
			OnDelegatedRoomResend: func(ctx context.Context, p *push.DelegatedRoomResend) error {
				return server.handleDelegatedRoomResend(ctx, p)
			},
		}
		if err := sub.Run(pushCtx); err != nil && err != context.Canceled {
			log.Errorf("push 订阅异常退出: %v", err)
		}
	}()
	log.Infof("已启动 Redis 定向推送订阅 node_id=%s", nodeID)

	return server
}

// SetDefaultServer 设置默认服务器实例
func SetDefaultServer(s *Server) {
	defaultServerMu.Lock()
	defer defaultServerMu.Unlock()
	defaultServer = s
}

// GetDefaultServer 获取默认服务器实例
func GetDefaultServer() *Server {
	defaultServerMu.RLock()
	defer defaultServerMu.RUnlock()
	return defaultServer
}

// subscribeEvents 订阅事件
func (s *Server) subscribeEvents() {
	s.registerEventHandlers()
	// 订阅心跳更新事件
	s.eventBus.Subscribe(pkgEvents.EventHeartbeatUpdate, s.onHeartbeatUpdate)

	// 订阅客户端关闭事件
	s.eventBus.Subscribe(pkgEvents.EventClientClose, s.onClientClose)

	// 订阅客户端超时事件
	s.eventBus.Subscribe(pkgEvents.EventClientTimeout, s.onClientTimeout)

	// 订阅客户端上线事件
	s.eventBus.Subscribe(pkgEvents.EventClientOnline, s.onClientOnline)
}

// onHeartbeatUpdate 处理心跳更新事件
func (s *Server) onHeartbeatUpdate(event pkgEvents.HeartbeatUpdateEvent) {
	if event.Uid == "" || event.Sid == "" {
		log.Warnf("忽略无效心跳更新事件: uid=%q sid=%q", event.Uid, event.Sid)
		return
	}
	if s.hbManager != nil {
		connID := event.ConnID
		if connID == 0 {
			if c, ok := s.reg.get(event.Uid); ok && c.Sid() == event.Sid {
				connID = c.ConnID()
			}
		}
		s.hbManager.UpdateHeartbeat(heartbeatClientKey(event.Uid, event.Sid, connID))
	}
	// 更新用户心跳时间
	go query.UpdateUserCurrentStatusHeartbeat(event.Uid)
	if err := push.Refresh(event.Uid, s.nodeID); err != nil {
		log.Debugf("presence 续期失败 uid=%s: %v", event.Uid, err)
	}
}

// onClientClose 处理客户端关闭事件
func (s *Server) onClientClose(event pkgEvents.ClientCloseEvent) {
	if event.Uid == "" || event.Sid == "" {
		log.Warnf("忽略无效客户端关闭事件: uid=%q sid=%q", event.Uid, event.Sid)
		return
	}
	if !s.reg.isCurrentConn(event.Uid, event.Sid, event.ConnID) {
		log.Infof("忽略陈旧客户端关闭事件: uid=%s sid=%s conn_id=%d reason=%s", event.Uid, event.Sid, event.ConnID, event.Reason)
		return
	}
	log.Infof("客户端 %s 主动关闭连接 sid=%s，原因: %s", event.Uid, event.Sid, event.Reason)
	s.handleUserOffline(event.Uid, "normal", event.Reason)
	s.unregisterLiveClient(event.Uid, event.Sid, event.ConnID, false)
}

// onClientOnline 处理客户端上线事件
func (s *Server) onClientOnline(uid string) {
	// 先完成 handleUserOnline（尽快下发 UserInfo + UserStatusSync），再下发未读，
	// 避免与 pushUnread 并行抢占同一条消息流的 version，并保证首包在线态先于历史消息。
	go func() {
		s.handleUserOnline(uid)
		s.pushUnreadMessages(uid)
		s.pushUnreadWithdrawMessages(uid)
	}()
}

// onClientTimeout 处理客户端超时事件
func (s *Server) onClientTimeout(event pkgEvents.ClientTimeoutEvent) {
	if event.Uid == "" || event.Sid == "" {
		log.Warnf("忽略无效客户端超时事件: uid=%q sid=%q", event.Uid, event.Sid)
		return
	}
	if !s.reg.isCurrentConn(event.Uid, event.Sid, event.ConnID) {
		log.Infof("忽略陈旧客户端超时事件: uid=%s sid=%s conn_id=%d", event.Uid, event.Sid, event.ConnID)
		return
	}
	s.handleUserOffline(event.Uid, "timeout", "心跳超时")
	s.unregisterLiveClient(event.Uid, event.Sid, event.ConnID, true)
}

// Run 启动服务器
func (s *Server) Run() error {
	listener, err := quic.ListenAddr(s.addr, s.tlsConfig, &quic.Config{
		MaxIdleTimeout:  30 * time.Second,
		KeepAlivePeriod: 30 * time.Second,
	})
	if err != nil {
		return err
	}
	go s.listenerAccept(listener)
	return nil
}

// registerLiveClient 登记活跃连接与 presence（须在认证成功、替换旧连接之后调用）。
func (s *Server) registerLiveClient(c *client.Client) {
	if c == nil || c.User() == nil {
		return
	}
	s.reg.register(c)
	if err := push.Register(c.User().Uid, s.nodeID); err != nil {
		log.Warnf("presence 登记失败 uid=%s: %v", c.User().Uid, err)
	}
	if s.hbManager != nil {
		s.removeHeartbeatKeys(c.User().Uid, c.Sid(), c.ConnID())
		s.hbManager.UpdateHeartbeat(heartbeatClientKey(c.User().Uid, c.Sid(), c.ConnID()))
	}
	log.Infof("客户端 %s 已连接 sid=%s conn_id=%d", c.User().Uid, c.Sid(), c.ConnID())
}

// unregisterLiveClient 仅当 map 中仍为同一连接实例时移除。
func (s *Server) unregisterLiveClient(uid, sid string, connID uintptr, forceClose bool) {
	removed, ok := s.reg.removeIfMatch(uid, sid, connID)
	if !ok || removed == nil {
		return
	}
	if err := push.Unregister(uid); err != nil {
		log.Warnf("presence 注销失败 uid=%s: %v", uid, err)
	}
	if forceClose && removed.Conn() != nil {
		removed.Conn().CloseWithError(quic.ApplicationErrorCode(0), "心跳超时")
	}
	s.removeHeartbeatKeys(uid, sid, connID)
}

// disconnectReplacedClient 关闭被新连接顶替的旧 QUIC（不调用 handleUserOffline，避免同 sid 会话被误标失效）。
func (s *Server) disconnectReplacedClient(old *client.Client, newSid string) {
	if old == nil || old.User() == nil {
		return
	}
	uid := old.User().Uid
	oldSid := old.Sid()
	oldConnID := old.ConnID()
	log.Infof("用户 %s 重复连接，替换旧连接 old_sid=%s new_sid=%s conn_id=%d", uid, oldSid, newSid, oldConnID)

	if oldSid != "" && newSid != "" && oldSid != newSid {
		if err := query.UpdateUserSessionLogout(oldSid, "connection_replaced"); err != nil {
			log.Warnf("注销被替换的旧会话失败 old_sid=%s: %v", oldSid, err)
		}
	}

	forceOfflineMsg := types.ServerForceOffline{Reason: "连接已被新连接替换"}
	messageSent := false
	if data, err := old.EncodeServerMessageData(forceOfflineMsg); err == nil {
		if err := old.SendServerMessage(types.ServerMessageEntity{
			MessageType: quicEntity.TypeForceOffline,
			Data:        data,
		}); err == nil {
			messageSent = true
		}
	}
	if messageSent {
		time.Sleep(500 * time.Millisecond)
	}
	if old.Conn() != nil {
		old.Conn().CloseWithError(quic.ApplicationErrorCode(0), "连接已被新连接替换")
	}
	s.unregisterLiveClient(uid, oldSid, oldConnID, false)
}

// handleClientTimeout 处理客户端超时（由心跳管理器调用，发布事件）
func (s *Server) handleClientTimeout(clientKey string) {
	uid, sid, connID, ok := parseHeartbeatClientKey(clientKey)
	if !ok {
		log.Warnf("忽略格式错误的心跳超时键: %s", clientKey)
		return
	}
	if connID == 0 {
		// 旧格式 uid|sid：若 map 中已无该 sid，说明连接已替换，仅清理堆项
		c, live := s.reg.get(uid)
		if !live || c.Sid() != sid {
			s.removeHeartbeatKeys(uid, sid, 0)
			return
		}
		connID = c.ConnID()
	}
	if s.eventBus != nil {
		s.eventBus.Publish(pkgEvents.EventClientTimeout, pkgEvents.ClientTimeoutEvent{
			Uid:    uid,
			Sid:    sid,
			ConnID: connID,
		})
	}
}

// GetClientByUid 获取当前活跃 QUIC 客户端
func (s *Server) GetClientByUid(uid string) *client.Client {
	c, _ := s.reg.get(uid)
	return c
}

// GetClientCount 获取客户端数量
func (s *Server) GetClientCount() int {
	return s.reg.len()
}

// Shutdown 优雅关闭服务器
func (s *Server) Shutdown() {
	s.shutdownMux.Lock()
	if s.isShutdown {
		s.shutdownMux.Unlock()
		return
	}
	s.isShutdown = true
	s.shutdownMux.Unlock()

	log.Info("正在关闭服务器...")

	// 取消订阅事件
	if s.eventBus != nil {
		s.eventBus.Unsubscribe(pkgEvents.EventHeartbeatUpdate, s.onHeartbeatUpdate)
		s.eventBus.Unsubscribe(pkgEvents.EventClientClose, s.onClientClose)
		s.eventBus.Unsubscribe(pkgEvents.EventClientTimeout, s.onClientTimeout)
	}

	// 停止心跳管理器
	if s.hbManager != nil {
		s.hbManager.Stop()
	}

	geoip.Close()

	if s.pushCancel != nil {
		s.pushCancel()
	}

	// 注意：队列服务器和客户端由外部管理，这里不需要关闭

	clients := s.reg.snapshotAll()
	uids := make([]string, 0, len(clients))
	for _, c := range clients {
		if c != nil && c.User() != nil {
			uids = append(uids, c.User().Uid)
		}
	}
	s.reg.clear()

	// 处理所有用户的下线逻辑（服务器关闭）
	log.Infof("开始处理 %d 个在线用户的下线逻辑", len(uids))
	var offlineWg sync.WaitGroup
	for _, uid := range uids {
		offlineWg.Add(1)
		go func(userID string) {
			defer offlineWg.Done()
			s.handleUserOffline(userID, "server_shutdown", "服务器关闭")
		}(uid)
	}
	offlineWg.Wait()
	log.Info("所有用户下线逻辑处理完成")

	// 并发关闭所有连接
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(cli *client.Client) {
			defer wg.Done()
			if cli.Conn() != nil {
				cli.Conn().CloseWithError(quic.ApplicationErrorCode(0), "服务器关闭")
			}
		}(c)
	}
	wg.Wait()

	log.Infof("已关闭 %d 个客户端连接", len(clients))
	log.Info("服务器已关闭")
}

// handleConnect 处理新连接
func (s *Server) handleConnect(conn *quic.Conn) {
	// 使用 defer recover 保护，避免单个连接的错误影响服务器
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("处理连接时 panic: %v", r)
			conn.CloseWithError(quic.ApplicationErrorCode(0), "服务器内部错误")
		}
	}()

	contextTimeout, cancel := context.WithTimeout(context.Background(), AuthTimeout)
	defer cancel()

	// 接受认证流
	authStream, err := conn.AcceptStream(contextTimeout)
	if err != nil {
		log.Errorf("接受认证流失败: %v", err)
		conn.CloseWithError(quic.ApplicationErrorCode(quic.InvalidToken), "认证失败")
		return
	}

	// 执行认证
	authResult, err := s.authenticator.Authenticate(authStream)
	if err != nil {
		log.Errorf("认证失败: %v", err)
		conn.CloseWithError(quic.ApplicationErrorCode(quic.InvalidToken), "认证失败")
		return
	}

	user := authResult.User
	deviceInfo := authResult.DeviceInfo
	authStreamPtr := authResult.AuthStream // 保存认证流用于异步读取设备信息

	if oldClient := s.reg.snapshotForReplace(user.Uid); oldClient != nil {
		s.disconnectReplacedClient(oldClient, authResult.Sid)
	}

	// 认证成功后尽早预写在线态，缩短“已连上但状态仍离线”的时间窗。
	// 后续 handleUserOnline 会再次更新并补全更多字段（会话映射、地理位置等）。
	if authResult.Sid != "" {
		earlyIP := netutil.ExtractIP(conn.RemoteAddr().String())
		if err := query.UpdateUserCurrentStatusOnline(user.Uid, authResult.Sid, "", earlyIP, deviceInfo); err != nil {
			log.Warnf("预写用户在线状态失败 uid=%s sid=%s: %v", user.Uid, authResult.Sid, err)
		}
	} else {
		log.Warnf("预写用户在线状态跳过：sid为空 uid=%s", user.Uid)
	}

	// 创建客户端实例（使用默认设备信息，将在后台异步更新）
	client := client.NewClient(conn, user, deviceInfo, s.eventBus, authResult.AccessClaims)
	client.SetSid(authResult.Sid) // sid 与 uid 同存于 token，用于操作记录等
	client.SetSessionKey(authResult.SessionKey)

	// 先登记再 Start，避免消息/心跳流在 map 注册前更新导致竞态
	s.registerLiveClient(client)
	client.Start()

	// 异步读取设备信息并更新客户端
	if authStreamPtr != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Errorf("异步读取设备信息时 panic: %v", r)
				}
			}()

			// 异步读取设备信息
			updatedDeviceInfo, err := auth.ReadDeviceInfoAsync(authStreamPtr)
			if err != nil {
				log.Warnf("异步读取设备信息失败 uid=%s: %v，将使用默认设备信息", user.Uid, err)
				return
			}

			// 更新客户端设备信息
			client.UpdateDeviceInfo(updatedDeviceInfo)
			log.Infof("客户端 %s 设备信息已异步更新", user.Uid)
		}()
	}
}

// listenerAccept 监听接受新连接
func (s *Server) listenerAccept(listener *quic.Listener) {
	defer listener.Close()
	for {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			log.Error("quic listener accept error: ", err)
			break
		}
		go s.handleConnect(conn)
	}
}

// filterUnreadRoomMessagesByBlock 按屏蔽关系过滤未读消息：已屏蔽会话只保留屏蔽前的消息，已屏蔽某用户则过滤掉其消息
func filterUnreadRoomMessagesByBlock(uid string, messages []types.ServerRoomMessage) []types.ServerRoomMessage {
	if len(messages) == 0 {
		return messages
	}
	ridToBlockTime := make(map[string]int64)
	ridToBlockedTargets := make(map[string]map[string]struct{})
	filtered := make([]types.ServerRoomMessage, 0, len(messages))
	for _, rm := range messages {
		rid := rm.Rid
		if _, ok := ridToBlockTime[rid]; !ok {
			t, blocked := query.GetUserRoomBlockCreateTime(uid, rid)
			if blocked && t > 0 {
				ridToBlockTime[rid] = t
			} else {
				ridToBlockTime[rid] = 0
			}
		}
		if ridToBlockTime[rid] > 0 && rm.CreateTime >= ridToBlockTime[rid] {
			continue
		}
		if _, ok := ridToBlockedTargets[rid]; !ok {
			targets, _ := query.GetBlockedTargetUidsInRoom(uid, rid)
			set := make(map[string]struct{}, len(targets))
			for _, u := range targets {
				set[u] = struct{}{}
			}
			ridToBlockedTargets[rid] = set
		}
		if set := ridToBlockedTargets[rid]; len(set) > 0 {
			if _, blocked := set[rm.SenderUid]; blocked {
				continue
			}
		}
		filtered = append(filtered, rm)
	}
	return filtered
}

// pushUnreadMessages 基于 RoomMessageAck 未读记录获取最新 15 条并按顺序下发（已按屏蔽关系过滤）
func (s *Server) pushUnreadMessages(uid string) {
	client := s.GetClientByUid(uid)
	if client == nil {
		return
	}
	roomMessages := query.GetUnreadRoomMessagesByAck(uid, 15)
	if roomMessages == nil || len(*roomMessages) == 0 {
		return
	}
	filtered := filterUnreadRoomMessagesByBlock(uid, *roomMessages)
	if len(filtered) == 0 {
		return
	}
	// 当前查询按 seq_id DESC，发送时按 ASC；批量下发
	for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}
	if err := client.SendRoomMessages(filtered); err != nil {
		log.Errorf("推送未读消息批量失败 uid=%s: %v", uid, err)
	}
}

// pushUnreadWithdrawMessages 基于 RoomMessageWithdrawAck 未读记录获取所有已撤回消息并按顺序分批下发（限流）
func (s *Server) pushUnreadWithdrawMessages(uid string) {
	client := s.GetClientByUid(uid)
	if client == nil {
		return
	}
	// 查询所有未读的撤回消息（使用一个很大的 limit 来获取所有记录）
	roomMessages := query.GetUnreadRoomMessagesWithdrawByAck(uid, 10000)
	if len(roomMessages) == 0 {
		return
	}
	// 当前查询按 seq_id DESC，发送时按 ASC；反转顺序
	for i, j := 0, len(roomMessages)-1; i < j; i, j = i+1, j-1 {
		roomMessages[i], roomMessages[j] = roomMessages[j], roomMessages[i]
	}
	// 将 ServerRoomMessage 转换为 ServerRoomMessageWithdraw
	withdrawMessages := make([]types.ServerRoomMessageWithdraw, 0, len(roomMessages))
	for _, msg := range roomMessages {
		withdrawMsg := types.ServerRoomMessageWithdraw{
			SeqId:      msg.SeqId,
			ClientMid:  msg.ClientMid,
			Mid:        msg.Mid,
			SenderUid:  msg.SenderUid,
			Rid:        msg.Rid,
			Contents:   msg.Contents,
			CreateTime: msg.CreateTime,
		}
		withdrawMessages = append(withdrawMessages, withdrawMsg)
	}
	// 限流：分批发送，每批50条，每批之间延迟100ms
	const batchSize = 50
	const batchDelay = 100 * time.Millisecond
	total := len(withdrawMessages)
	for i := 0; i < total; i += batchSize {
		end := min(i+batchSize, total)
		batch := withdrawMessages[i:end]
		if err := client.SendRoomMessagesWithdraw(batch); err != nil {
			log.Errorf("推送未读撤回消息批量失败 uid=%s 批次 %d-%d: %v", uid, i, end-1, err)
			// 如果发送失败，可以选择继续发送下一批或中断
			// 这里选择继续发送下一批，避免因单批失败导致后续消息无法下发
		}
		// 如果不是最后一批，添加延迟
		if end < total {
			time.Sleep(batchDelay)
		}
	}
	log.Infof("推送未读撤回消息完成 uid=%s 总数=%d", uid, total)
}

// handleUserOnline 处理用户上线逻辑
func (s *Server) handleUserOnline(uid string) {
	client := s.GetClientByUid(uid)
	if client == nil {
		log.Warnf("用户上线处理失败：客户端不存在 uid=%s", uid)
		return
	}

	// 获取连接信息
	conn := client.Conn()
	if conn == nil {
		log.Warnf("用户上线处理失败：连接不存在 uid=%s", uid)
		return
	}

	// 获取IP地址
	ip := netutil.ExtractIP(conn.RemoteAddr().String())

	// 从客户端获取设备信息（仅用于展示/统计，不参与设备标识计算）
	deviceInfo := client.DeviceInfo()

	// 设备 ID 与 HTTP 一致：使用登录时服务端计算的指纹，从会话中取，不信任客户端上报
	sid := client.Sid()
	if sid == "" {
		log.Errorf("用户上线失败：客户端 sid 为空 uid=%s", uid)
		return
	}
	session, err := query.GetUserSessionBySid(sid)
	if err != nil || session == nil {
		log.Errorf("用户上线失败：根据 sid 获取会话失败 uid=%s sid=%s: %v", uid, sid, err)
		return
	}
	deviceId := session.DeviceId
	// 设备指纹每次可不同（用于安全检测）
	deviceFinger := xid.New().String()
	platform := deviceInfo.Platform
	if platform == "" {
		platform = "desktop"
	}

	// 创建设备名称（从设备信息中获取）
	deviceName := deviceInfo.DeviceModel
	if deviceName == "" || deviceName == "Unknown" {
		deviceName = fmt.Sprintf("%s %s", deviceInfo.Platform, deviceInfo.OSVersion)
	}

	// 获取或创建用户当前状态
	currentStatus, err := query.GetOrCreateUserCurrentStatus(uid)
	if err != nil {
		log.Errorf("获取用户当前状态失败 uid=%s: %v", uid, err)
		return
	}

	// 记录状态变更前状态
	statusBefore := currentStatus.CurrentStatus

	// 快速更新用户当前状态为在线
	websocketId := "" // QUIC连接不使用WebSocket ID
	if err := query.UpdateUserCurrentStatusOnline(uid, sid, websocketId, ip, deviceInfo); err != nil {
		log.Errorf("更新用户当前状态为在线失败 uid=%s: %v", uid, err)
		return
	}

	// 立即下发资料与在线态（不等待 GeoIP / 设备会话落库）
	s.sendUserInfoToClient(uid)

	// 异步：GeoIP、设备会话映射、ISP、上线历史（不阻塞首包）
	go func() {
		locationInfo := geoip.LookupLocation(ip)
		log.Infof("地理位置信息: %+v", locationInfo)
		if locationInfo.IP == "" {
			locationInfo.IP = ip
		}
		if locationInfo.Timezone == "" {
			locationInfo.Timezone = deviceInfo.Timezone
		}

		if _, err := query.GetOrCreateUserDeviceSession(uid, deviceId, deviceFinger, sid, platform, deviceName, locationInfo); err != nil {
			log.Errorf("创建或更新设备会话映射失败 uid=%s: %v", uid, err)
		}

		isp := deviceInfo.Network.ISP
		if isp == "" {
			queryIP := ip
			if locationInfo.IP != "" && locationInfo.IP != ip {
				queryIP = locationInfo.IP
			}
			serverISP, err := getISPFromIP(queryIP)
			if err != nil {
				log.Debugf("获取ISP信息失败 IP=%s: %v", queryIP, err)
			} else if serverISP != "" {
				isp = serverISP
				log.Debugf("从服务器获取ISP信息: %s (IP: %s)", isp, queryIP)
			}
		}

		networkInfo := entity.NetworkInfoJSON{
			Type:     deviceInfo.Network.NetworkType,
			ISP:      isp,
			Signal:   0,
			Upload:   0,
			Download: 0,
			Latency:  0,
		}

		appState := entity.AppStateJSON{
			IsForeground: deviceInfo.AppState.IsForeground,
			BatteryLevel: deviceInfo.AppState.BatteryLevel,
			IsCharging:   deviceInfo.AppState.IsCharging,
			MemoryUsage:  deviceInfo.AppState.MemoryUsage,
			CPUUsage:     deviceInfo.AppState.CPUUsage,
		}

		if err := query.CreateUserOnlineHistory(
			sid,
			uid,
			"login",
			"normal",
			statusBefore,
			"online",
			"用户上线",
			deviceInfo,
			networkInfo,
			locationInfo,
			appState,
		); err != nil {
			log.Errorf("创建用户上线历史记录失败 uid=%s: %v", uid, err)
		}
	}()

	// 好友可见在线态广播（含 custom_state/current_status）
	s.broadcastFriendUserStatusSync(uid)
	// 房间成员在线态广播（仅更新在线/离线与时间字段）
	s.broadcastRoomUserPresenceSync(uid)
	// 向刚上线的用户补发同房间成员的在线态快照
	s.deliverRoomMembersPresenceSnapshotToClient(uid)

	// 注意：UserInfo/UserStatusSync 已在 GeoIP 之前发送

	log.Infof("用户上线处理完成 uid=%s, sid=%s", uid, sid)
}

// handleUserOffline 处理用户下线逻辑
func (s *Server) handleUserOffline(uid, eventSubtype, reason string) {
	// 主 QUIC 断开/心跳超时：若 Redis 仍记录该用户在媒体通话中，则服务端兜底离房（与客户端 media_quic_disconnect 双保险）
	mediacall.OnUserMessageQuicOffline(uid)

	// 获取用户当前状态
	currentStatus, err := query.GetOrCreateUserCurrentStatus(uid)
	if err != nil {
		log.Errorf("获取用户当前状态失败 uid=%s: %v", uid, err)
		return
	}

	// 如果没有会话ID，说明用户可能没有正常上线过
	if currentStatus.CurrentSessionId == "" {
		log.Warnf("用户下线处理：没有找到会话ID uid=%s", uid)
		return
	}

	// 获取会话信息
	session, err := query.GetUserSessionBySid(currentStatus.CurrentSessionId)
	if err != nil {
		log.Errorf("获取用户会话失败 sid=%s: %v", currentStatus.CurrentSessionId, err)
		// 即使获取会话失败，也继续更新状态
	} else {
		// 更新会话登出信息
		if err := query.UpdateUserSessionLogout(session.Sid, reason); err != nil {
			log.Errorf("更新用户会话登出信息失败 sid=%s: %v", session.Sid, err)
		}

		// 计算在线时长（秒）
		onlineDuration := 0
		if session.LoginTime > 0 {
			now := time.Now().UnixMilli()
			onlineDuration = int((now - session.LoginTime) / 1000)
		}

		// 更新设备会话映射的登出信息（与UserSession成对更新）
		if err := query.UpdateUserDeviceSessionLogout(session.Uid, session.DeviceId, onlineDuration); err != nil {
			log.Errorf("更新设备会话映射登出信息失败 uid=%s, device_id=%s: %v", session.Uid, session.DeviceId, err)
			// 不返回错误，继续处理
		}
	}

	// 记录状态变更前状态
	statusBefore := currentStatus.CurrentStatus

	// 更新用户当前状态为离线
	if err := query.UpdateUserCurrentStatusOffline(uid, reason); err != nil {
		log.Errorf("更新用户当前状态为离线失败 uid=%s: %v", uid, err)
		return
	}

	// 好友可见在线态广播（含 custom_state/current_status）
	s.broadcastFriendUserStatusSync(uid)
	// 房间成员在线态广播（仅更新在线/离线与时间字段）
	s.broadcastRoomUserPresenceSync(uid)

	// 创建下线历史记录
	deviceInfo := currentStatus.DeviceInfo
	networkInfo := entity.NetworkInfoJSON{}
	locationInfo := entity.LocationInfoJSON{
		IP: currentStatus.IP,
	}
	appState := entity.AppStateJSON{}

	sid := currentStatus.CurrentSessionId
	if session != nil {
		sid = session.Sid
	}

	if err := query.CreateUserOnlineHistory(
		sid,
		uid,
		"logout",
		eventSubtype,
		statusBefore,
		"offline",
		reason,
		deviceInfo,
		networkInfo,
		locationInfo,
		appState,
	); err != nil {
		log.Errorf("创建用户下线历史记录失败 uid=%s: %v", uid, err)
	}

	log.Infof("用户下线处理完成 uid=%s, reason=%s", uid, reason)
}

// deliverFriendUserStatusSyncPayload 向单个在线连接下发一条好友可见在线态快照
func (s *Server) deliverFriendUserStatusSyncPayload(rc *client.Client, payload types.ServerUserStatusSync) {
	if rc == nil {
		return
	}
	data, err := rc.EncodeServerMessageData(payload)
	if err != nil {
		log.Errorf("编码用户状态同步消息失败: %v", err)
		return
	}
	if err := rc.SendServerMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeUserStatusSync,
		Data:        data,
	}); err != nil {
		log.Errorf("发送用户状态同步消息失败: %v", err)
	}
}

func buildFriendVisibleUserStatusSyncPayload(uid string, st *entity.UserCurrentStatus) types.ServerUserStatusSync {
	return types.ServerUserStatusSync{
		Uid:               uid,
		IsOnline:          st.IsOnline,
		CurrentStatus:     st.CurrentStatus,
		LastOnline:        st.LastOnline,
		LastLogin:         st.LastLogin,
		CustomState:       st.CustomState,
		Platform:          st.Platform,
		DeviceType:        st.DeviceType,
		DeviceModel:       st.DeviceModel,
		OSVersion:         st.OSVersion,
		AppVersion:        st.AppVersion,
		ConcurrentDevices: st.ConcurrentDevices,
	}
}

func buildRoomUserPresenceSyncPayload(uid string, st *entity.UserCurrentStatus) types.ServerRoomUserPresenceSync {
	return types.ServerRoomUserPresenceSync{
		Uid:        uid,
		IsOnline:   st.IsOnline,
		LastOnline: st.LastOnline,
		LastLogin:  st.LastLogin,
	}
}

// broadcastFriendUserStatusSync 将 subjectUid 的当前在线态快照推送给：本人 + 在线好友。
// 该通道仅承担“好友可见状态”（包含 custom_state/current_status）。
func (s *Server) broadcastFriendUserStatusSync(uid string) {
	currentStatus, err := query.GetOrCreateUserCurrentStatus(uid)
	if err != nil {
		log.Errorf("broadcastFriendUserStatusSync 获取用户当前状态失败 uid=%s: %v", uid, err)
		return
	}

	// 职责分离：UserStatusSync 事件通道只用于「好友可见状态」的广播（不再混入房间用户）。
	friendPayload := buildFriendVisibleUserStatusSyncPayload(uid, currentStatus)

	// 本人优先收到权威快照（含 custom_state/current_status）
	if self := s.GetClientByUid(uid); self != nil {
		s.deliverFriendUserStatusSyncPayload(self, friendPayload)
	}

	// 好友接收：仅在线好友
	friendUidSet := make(map[string]struct{})
	friendGroups, err := query.GetFriendGroupList(uid)
	if err != nil {
		log.Errorf("broadcastFriendUserStatusSync 获取好友分组失败 uid=%s: %v", uid, err)
		return
	}
	for _, g := range friendGroups {
		if g == nil {
			continue
		}
		for _, f := range g.FriendList {
			if f == nil || f.Uid == uid {
				continue
			}
			if f.Status == nil || !f.Status.IsOnline {
				continue
			}
			friendUidSet[f.Uid] = struct{}{}
		}
	}

	for receiverUid := range friendUidSet {
		rc := s.GetClientByUid(receiverUid)
		if rc == nil {
			continue
		}
		s.deliverFriendUserStatusSyncPayload(rc, friendPayload)
	}
}

// deliverRoomUserPresenceSyncPayload 向单个在线连接下发一条“房间成员在线态同步”消息
func (s *Server) deliverRoomUserPresenceSyncPayload(rc *client.Client, payload types.ServerRoomUserPresenceSync) {
	if rc == nil {
		return
	}
	data, err := rc.EncodeServerMessageData(payload)
	if err != nil {
		log.Errorf("编码房间在线态同步消息失败: %v", err)
		return
	}
	if err := rc.SendServerMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeRoomUserPresenceSync,
		Data:        data,
	}); err != nil {
		log.Errorf("发送房间在线态同步消息失败: %v", err)
	}
}

// broadcastRoomUserPresenceSync 将 subjectUid 的基础在线态快照推送给：同在任意房间且当前在线的成员。
// 好友完整状态（含 custom_state/current_status）仍由 UserStatusSync 通道承担。
func (s *Server) broadcastRoomUserPresenceSync(uid string) {
	currentStatus, err := query.GetOrCreateUserCurrentStatus(uid)
	if err != nil {
		log.Errorf("broadcastRoomUserPresenceSync 获取用户当前状态失败 uid=%s: %v", uid, err)
		return
	}

	roomRids, err := query.GetRoomIdsByUid(uid)
	if err != nil {
		log.Errorf("broadcastRoomUserPresenceSync 获取房间 rid 失败 uid=%s: %v", uid, err)
		return
	}
	if len(roomRids) == 0 {
		return
	}

	payload := buildRoomUserPresenceSyncPayload(uid, currentStatus)

	// 同一 uid 可能加入多个房间：去重接收者
	receiverUidSet := make(map[string]struct{})
	for _, rid := range roomRids {
		if rid == "" {
			continue
		}
		roomUserIds, err := query.GetRoomUserIdsCache(rid)
		if err != nil || len(roomUserIds) == 0 {
			continue
		}
		for _, receiverUid := range roomUserIds {
			if receiverUid == uid {
				continue
			}
			if s.GetClientByUid(receiverUid) != nil {
				receiverUidSet[receiverUid] = struct{}{}
			}
		}
	}

	for receiverUid := range receiverUidSet {
		rc := s.GetClientByUid(receiverUid)
		if rc == nil {
			continue
		}
		s.deliverRoomUserPresenceSyncPayload(rc, payload)
	}
}

// deliverRoomMembersPresenceSnapshotToClient 向 viewerUid 补发其所在各房间成员的在线态快照
func (s *Server) deliverRoomMembersPresenceSnapshotToClient(viewerUid string) {
	rc := s.GetClientByUid(viewerUid)
	if rc == nil {
		return
	}
	memberUids, err := query.GetUidsSharingRoomWith(viewerUid)
	if err != nil || len(memberUids) == 0 {
		return
	}
	for _, memberUid := range memberUids {
		currentStatus, err := query.GetOrCreateUserCurrentStatus(memberUid)
		if err != nil {
			log.Errorf("deliverRoomMembersPresenceSnapshotToClient 获取成员状态失败 member=%s viewer=%s: %v", memberUid, viewerUid, err)
			continue
		}
		payload := buildRoomUserPresenceSyncPayload(memberUid, currentStatus)
		s.deliverRoomUserPresenceSyncPayload(rc, payload)
	}
}

// sendUserInfoToClient 向客户端发送用户完整信息（包含状态信息）
func (s *Server) sendUserInfoToClient(uid string) {
	client := s.GetClientByUid(uid)
	if client == nil {
		log.Warnf("发送用户信息失败：客户端不存在 uid=%s", uid)
		return
	}

	// 获取用户完整信息（包含状态）
	usersWithStatus, err := query.GetRoomUserInfoByUidList([]string{uid}, "")
	if err != nil || len(usersWithStatus) == 0 {
		log.Errorf("获取用户信息失败 uid=%s: %v", uid, err)
		return
	}

	userWithStatus := usersWithStatus[0]

	// 仅资料字段；在线态由 broadcastFriendUserStatusSync / broadcastRoomUserPresenceSync 下发
	userInfo := quicEntity.ServerUserInfo{
		Uid:          userWithStatus.Uid,
		Username:     userWithStatus.Username,
		Nickname:     userWithStatus.Nickname,
		Signature:    userWithStatus.Signature,
		Introduction: userWithStatus.Introduction,
		Email:        userWithStatus.Email,
		AvatarUfId:   userWithStatus.AvatarUfId,
		CreateTime:   userWithStatus.CreateTime,
	}

	// 编码并发送消息
	data, err := client.EncodeServerMessageData(userInfo)
	if err != nil {
		log.Errorf("编码用户信息失败 uid=%s: %v", uid, err)
		return
	}

	if err := client.SendServerMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeUserInfo,
		Data:        data,
	}); err != nil {
		log.Errorf("发送用户信息失败 uid=%s: %v", uid, err)
		return
	}

	log.Infof("已发送用户完整信息给客户端 uid=%s", uid)
}

// IPSBGeoResponse ip.sb API响应结构
type IPSBGeoResponse struct {
	IP          string  `json:"ip"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Region      string  `json:"region"`
	RegionName  string  `json:"region_name"`
	City        string  `json:"city"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Timezone    string  `json:"timezone"`
	ISP         string  `json:"isp"`
	Org         string  `json:"org"`
	AS          string  `json:"as"`
	ASName      string  `json:"asname"`
}

// getISPFromIP 通过 ip.sb API 获取ISP信息
// API地址: https://api.ip.sb/geoip/{ip}
// 带缓存机制，避免频繁请求
func getISPFromIP(ip string) (string, error) {
	// 如果是本地地址，不需要查询ISP
	ipAddr, err := netip.ParseAddr(ip)
	if err == nil && publicip.IsLocal(ipAddr) {
		return "", nil
	}

	// 检查缓存
	ispCacheMu.RLock()
	if entry, exists := ispCache[ip]; exists {
		if time.Since(entry.cacheTime) < ispCacheTTL {
			cachedISP := entry.isp
			ispCacheMu.RUnlock()
			log.Debugf("使用缓存的ISP信息: %s (IP: %s)", cachedISP, ip)
			return cachedISP, nil
		}
	}
	ispCacheMu.RUnlock()

	// 创建HTTP客户端，设置超时
	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	// 构建API URL
	url := fmt.Sprintf("https://api.ip.sb/geoip/%s", ip)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("User-Agent", "QUIC-Server/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API返回错误状态码: %d", resp.StatusCode)
	}

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %v", err)
	}

	// 解析JSON响应
	var result IPSBGeoResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析响应失败: %v", err)
	}

	// 优先返回ISP，如果没有则返回Org，再没有则返回ASName
	isp := ""
	if result.ISP != "" {
		isp = result.ISP
	} else if result.Org != "" {
		isp = result.Org
	} else if result.ASName != "" {
		isp = result.ASName
	}

	// 更新缓存
	if isp != "" {
		ispCacheMu.Lock()
		ispCache[ip] = ispCacheEntry{
			isp:       isp,
			cacheTime: time.Now(),
		}
		ispCacheMu.Unlock()
	}

	return isp, nil
}
