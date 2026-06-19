package streaming

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	log "github.com/sirupsen/logrus"
	mediacall "github.com/xd/quic-server/http/handler/media"
	"github.com/xd/quic-server/jwt"
	"github.com/xd/quic-server/redis"
)

const (
	mediaJoinSignRedisKey = "media:join:sign:%s"
	mediaHeartbeatFrame   = "__cc_media_hb__"
)

type joinRequest struct {
	UID      string `json:"uid"`
	SID      string `json:"sid"`
	RID      string `json:"rid"`
	Role     string `json:"role"`
	Nonce    string `json:"nonce"`
	ExpireAt int64  `json:"expire_at"`
	Sign     string `json:"sign"`
}

type joinResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type roomHub struct {
	mu      sync.RWMutex
	clients map[string]*mediaClient
}

type mediaClient struct {
	uid     string
	rid     string
	role    string
	stream  *quic.Stream
	writeMu sync.Mutex
}

type Server struct {
	addr      string
	tlsConfig *tls.Config
	roomsMu   sync.RWMutex
	rooms     map[string]*roomHub
}

func NewServer(addr string, tlsConfig *tls.Config) *Server {
	return &Server{
		addr:      addr,
		tlsConfig: tlsConfig,
		rooms:     make(map[string]*roomHub),
	}
}

func (s *Server) Run(ctx context.Context) error {
	listener, err := quic.ListenAddr(s.addr, s.tlsConfig, &quic.Config{
		EnableDatagrams: true,
		// 媒体通话可能存在短时静默（尤其测试阶段只建链不发流），
		// 需要显式保活并放宽空闲超时，避免连接被过早判定为 TimedOut。
		KeepAlivePeriod: 10 * time.Second,
		MaxIdleTimeout:  2 * time.Minute,
	})
	if err != nil {
		return err
	}
	log.Infof("媒体 QUIC 服务启动: %s", s.addr)
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Warnf("接受连接失败: %v", err)
			continue
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn *quic.Conn) {
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		log.Warnf("认证流接收失败: %v", err)
		return
	}
	reqBytes, err := readFrame(stream)
	if err != nil {
		log.Warnf("读取认证请求失败: %v", err)
		return
	}
	var req joinRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		_ = writeJSON(stream, joinResponse{OK: false, Message: "认证数据无效"})
		return
	}
	if err := s.consumeAndVerifyJoinSign(req); err != nil {
		_ = writeJSON(stream, joinResponse{OK: false, Message: err.Error()})
		return
	}
	if err := writeJSON(stream, joinResponse{OK: true, Message: "ok"}); err != nil {
		return
	}

	client := &mediaClient{
		uid:    req.UID,
		rid:    req.RID,
		role:   req.Role,
		stream: stream,
	}
	s.addClient(client)
	defer func() {
		empty := s.removeClient(client)
		// 媒体 QUIC 断开时兜底清理 Redis 状态，避免仅断开媒体链路但主 QUIC 仍在线时，
		// 导致通话状态/房间指针未结束，重登后无法创建新的媒体通话。
		mediacall.OnUserMessageQuicOffline(client.uid)
		if empty {
			mediacall.OnMediaRoomEmpty(client.rid, client.uid)
		}
	}()
	s.readLoop(client)
}

func (s *Server) consumeAndVerifyJoinSign(req joinRequest) error {
	if req.UID == "" || req.SID == "" || req.RID == "" || req.Nonce == "" || req.Sign == "" {
		return fmt.Errorf("缺少认证参数")
	}
	if time.Now().UnixMilli() > req.ExpireAt {
		return fmt.Errorf("签名已过期")
	}
	expectedSign := makeMediaJoinSign(req.UID, req.SID, req.RID, req.Role, req.Nonce, req.ExpireAt)
	if !hmac.Equal([]byte(expectedSign), []byte(req.Sign)) {
		return fmt.Errorf("签名非法")
	}

	key := fmt.Sprintf(mediaJoinSignRedisKey, req.Nonce)
	script := `
local key = KEYS[1]
if redis.call("EXISTS", key) == 0 then
	return 0
end
local val = redis.call("GET", key)
redis.call("DEL", key)
return val
`
	cmd := redis.Eval(script, []string{key})
	if cmd.Err() != nil {
		return fmt.Errorf("验签失败")
	}
	raw, ok := cmd.Val().(string)
	if !ok || raw == "" {
		return fmt.Errorf("签名不存在或已使用")
	}
	var cached joinRequest
	if err := json.Unmarshal([]byte(raw), &cached); err != nil {
		return fmt.Errorf("签名数据损坏")
	}
	if cached.UID != req.UID || cached.SID != req.SID || cached.RID != req.RID || cached.Sign != req.Sign || cached.ExpireAt != req.ExpireAt {
		return fmt.Errorf("签名不匹配")
	}
	if time.Now().UnixMilli() > cached.ExpireAt {
		return fmt.Errorf("签名已过期")
	}
	return nil
}

func (s *Server) readLoop(client *mediaClient) {
	for {
		payload, err := readFrame(client.stream)
		if err != nil {
			log.Debugf("读取媒体流失败 uid=%s rid=%s err=%v", client.uid, client.rid, err)
			return
		}
		// 业务层心跳包只用于保活，不参与房间转发。
		if string(payload) == mediaHeartbeatFrame {
			continue
		}
		// 为了支持双方互发测试文本，这里不再区分 publisher/subscriber，都允许广播。
		s.broadcast(client.rid, client.uid, payload)
	}
}

func (s *Server) getOrCreateRoom(rid string) *roomHub {
	s.roomsMu.Lock()
	defer s.roomsMu.Unlock()
	if room, ok := s.rooms[rid]; ok {
		return room
	}
	room := &roomHub{clients: make(map[string]*mediaClient)}
	s.rooms[rid] = room
	return room
}

func (s *Server) addClient(client *mediaClient) {
	room := s.getOrCreateRoom(client.rid)
	room.mu.Lock()
	defer room.mu.Unlock()
	room.clients[client.uid] = client
}

func (s *Server) removeClient(client *mediaClient) bool {
	s.roomsMu.RLock()
	room := s.rooms[client.rid]
	s.roomsMu.RUnlock()
	if room == nil {
		return true
	}
	room.mu.Lock()
	delete(room.clients, client.uid)
	empty := len(room.clients) == 0
	room.mu.Unlock()
	if empty {
		s.roomsMu.Lock()
		delete(s.rooms, client.rid)
		s.roomsMu.Unlock()
	}
	return empty
}

func (s *Server) broadcast(rid, sender string, payload []byte) {
	s.roomsMu.RLock()
	room := s.rooms[rid]
	s.roomsMu.RUnlock()
	if room == nil {
		return
	}
	room.mu.RLock()
	defer room.mu.RUnlock()
	for uid, client := range room.clients {
		if uid == sender {
			continue
		}
		uidBytes := []byte(sender)
		data := make([]byte, 2+len(uidBytes)+len(payload))
		data[0] = byte(len(uidBytes) >> 8)
		data[1] = byte(len(uidBytes))
		copy(data[2:2+len(uidBytes)], uidBytes)
		copy(data[2+len(uidBytes):], payload)
		client.writeMu.Lock()
		err := writeFrame(client.stream, data)
		client.writeMu.Unlock()
		if err != nil {
			log.Debugf("发送媒体流失败 target=%s rid=%s err=%v", uid, rid, err)
		}
	}
}

func readFrame(stream *quic.Stream) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(lenBuf[:])
	data := make([]byte, size)
	if size == 0 {
		return data, nil
	}
	if _, err := io.ReadFull(stream, data); err != nil {
		return nil, err
	}
	return data, nil
}

func writeFrame(stream *quic.Stream, data []byte) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if _, err := stream.Write(lenBuf[:]); err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	_, err := stream.Write(data)
	return err
}

func writeJSON(stream *quic.Stream, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFrame(stream, payload)
}

func makeMediaJoinSign(uid, sid, rid, role, nonce string, expireAt int64) string {
	raw := uid + "|" + sid + "|" + rid + "|" + role + "|" + nonce + "|" + strconv.FormatInt(expireAt, 10)
	mac := hmac.New(sha256.New, []byte(jwt.JWTKey()))
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}
