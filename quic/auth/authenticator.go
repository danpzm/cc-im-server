package auth

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/quic-go/quic-go"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/jwt"
	"github.com/xd/quic-server/pkg/protocol"
	"github.com/xd/quic-server/pkg/types"
	quicConfig "github.com/xd/quic-server/quic/config"
	"github.com/xd/quic-server/quic/handler"
	"golang.org/x/crypto/hkdf"
)

// AuthResult 认证结果，包含用户信息、设备信息和会话 ID（来自 token）
type AuthResult struct {
	User       *types.User
	Sid        string // UserSession.Sid，与 uid 同存于 token
	DeviceInfo entity.DeviceInfoJSON
	AuthStream *quic.Stream // 认证流，用于异步读取设备信息
	SessionKey []byte       // 每次连接会话密钥（用于消息流自定义加密）
	// AccessClaims QUIC 认证所用 access_token 的 JWT 声明；用于消息流建立时即调度过期前提醒（无需等待首条上行消息）。
	AccessClaims *jwt.CustomClaims
}

type AuthBootstrapPayload struct {
	Version         int32                 `json:"version"`
	DbKeySeed       string                `json:"db_key_seed"`
	DbKeyIterations int32                 `json:"db_key_iterations"`
	CurrentUser     *query.UserWithStatus `json:"current_user,omitempty"`
	ClientRuntime   *ClientRuntimeConfig  `json:"client_runtime,omitempty"`
}

type ClientRuntimeConfig struct {
	Heartbeat *HeartbeatRuntimeConfig `json:"heartbeat,omitempty"`
	Token     *TokenRuntimeConfig     `json:"token,omitempty"`
}

type HeartbeatRuntimeConfig struct {
	IntervalMs int64 `json:"interval_ms"`
	TimeoutMs  int64 `json:"timeout_ms"`
}

type TokenRuntimeConfig struct {
	MinRemainingSeconds int64 `json:"min_remaining_seconds"`
	RefreshAheadSeconds int64 `json:"refresh_ahead_seconds"`
	TtlSafetySeconds    int64 `json:"ttl_safety_seconds"`
}

// Authenticator 认证器接口
type Authenticator interface {
	Authenticate(s *quic.Stream) (*AuthResult, error)
}

// DefaultAuthenticator 默认认证器实现
type DefaultAuthenticator struct{}

// NewDefaultAuthenticator 创建默认认证器
func NewDefaultAuthenticator() *DefaultAuthenticator {
	return &DefaultAuthenticator{}
}

// Authenticate 执行认证
// 认证成功后立即返回，设备信息将在后台异步读取
func (a *DefaultAuthenticator) Authenticate(s *quic.Stream) (*AuthResult, error) {
	streamType, err := handler.GetStreamType(s)
	if err != nil {
		return nil, err
	}
	log.Infof("流类型: %s", streamType)
	if streamType != protocol.StreamTypeAuth {
		return nil, errors.New("流类型不匹配")
	}
	// 读取token长度（4字节）
	lengthBuf := make([]byte, 4)
	if _, err := io.ReadFull(s, lengthBuf); err != nil {
		return nil, err
	}
	authTokenSize := int(binary.BigEndian.Uint32(lengthBuf))

	// 读取token
	tokenBuf := make([]byte, authTokenSize)
	if _, err := io.ReadFull(s, tokenBuf); err != nil {
		return nil, err
	}
	token := string(tokenBuf)
	if len(token) != authTokenSize {
		return nil, errors.New("认证失败")
	}

	// 读取客户端 X25519 公钥（32字节），用于派生“会话密钥”
	clientPubKeyBytes := make([]byte, 32)
	if _, err := io.ReadFull(s, clientPubKeyBytes); err != nil {
		return nil, err
	}

	// 生成服务端临时 ECDHE 密钥并派生会话密钥（AES-256-GCM 32字节）
	curve := ecdh.X25519()
	serverPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成服务端临时密钥失败: %w", err)
	}
	serverPubKeyBytes := serverPriv.PublicKey().Bytes()

	clientPubKey, err := curve.NewPublicKey(clientPubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("解析客户端公钥失败: %w", err)
	}
	sharedSecret, err := serverPriv.ECDH(clientPubKey)
	if err != nil {
		return nil, fmt.Errorf("计算共享密钥失败: %w", err)
	}
	sessionKey := make([]byte, 32)
	h := hkdf.New(sha256.New, sharedSecret, []byte("cc:quic:session:salt"), []byte("cc:quic:session:info"))
	if _, err := io.ReadFull(h, sessionKey); err != nil {
		return nil, fmt.Errorf("派生会话密钥失败: %w", err)
	}

	user, sid, claims, err := a.getUserAndSidByConnectToken(token)
	if err != nil {
		return nil, err
	}

	bootstrapPayload := AuthBootstrapPayload{
		Version:         1,
		DbKeySeed:       user.DbKeySeed,
		DbKeyIterations: user.DbKeyIterations,
		CurrentUser:     query.BuildLoginUserWithStatus(user),
		ClientRuntime: &ClientRuntimeConfig{
			Heartbeat: &HeartbeatRuntimeConfig{
				IntervalMs: int64(quicConfig.HeartbeatCheckInterval / time.Millisecond),
				TimeoutMs:  int64(quicConfig.HeartbeatTimeout / time.Millisecond),
			},
			Token: &TokenRuntimeConfig{
				MinRemainingSeconds: int64(quicConfig.TokenMinRemaining().Seconds()),
				RefreshAheadSeconds: int64(quicConfig.TokenRefreshNoticeAhead().Seconds()),
				TtlSafetySeconds:    int64(quicConfig.TokenTtlSafety().Seconds()),
			},
		},
	}
	bootstrapBytes, err := json.Marshal(bootstrapPayload)
	if err != nil {
		return nil, fmt.Errorf("序列化认证下发参数失败: %w", err)
	}

	// 立即发送认证成功响应（不等待设备信息）
	if _, err := s.Write([]byte("success")); err != nil {
		log.Error("写入认证响应失败", err)
		return nil, err
	}
	lengthBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBytes, uint32(len(bootstrapBytes)))
	if _, err := s.Write(lengthBytes); err != nil {
		log.Error("写入认证参数长度失败", err)
		return nil, err
	}
	if _, err := s.Write(bootstrapBytes); err != nil {
		log.Error("写入认证参数失败", err)
		return nil, err
	}
	// 追加发送服务端公钥（32字节），客户端用于派生会话密钥
	if _, err := s.Write(serverPubKeyBytes); err != nil {
		log.Error("写入会话公钥失败", err)
		return nil, err
	}

	log.Infof("认证成功: %+v sid=%s (设备信息将异步接收)", user, sid)

	// 使用默认设备信息，将在后台异步更新
	deviceInfo := entity.DeviceInfoJSON{
		Platform:     "desktop",
		DeviceType:   "desktop",
		DeviceModel:  "Unknown",
		OSVersion:    "Unknown",
		AppVersion:   "1.0.0",
		Manufacturer: "Unknown",
		Browser:      "QUIC",
		BrowserVer:   "1.0.0",
		ScreenWidth:  0,
		ScreenHeight: 0,
		Language:     "zh-CN",
		Timezone:     "Asia/Shanghai",
		NetworkType:  "unknown",
		PushToken:    "",
	}

	return &AuthResult{
		User:         user,
		Sid:          sid,
		DeviceInfo:   deviceInfo,
		AuthStream:   s, // 返回认证流用于异步读取设备信息
		SessionKey:   sessionKey,
		AccessClaims: claims,
	}, nil
}

// ReadDeviceInfoAsync 异步读取设备信息
func ReadDeviceInfoAsync(s *quic.Stream) (entity.DeviceInfoJSON, error) {
	// 尝试读取设备信息长度（4字节）
	deviceInfoLengthBuf := make([]byte, 4)
	if _, err := io.ReadFull(s, deviceInfoLengthBuf); err != nil {
		return entity.DeviceInfoJSON{}, fmt.Errorf("读取设备信息长度失败: %v", err)
	}
	deviceInfoSize := int(binary.BigEndian.Uint32(deviceInfoLengthBuf))

	// 读取设备信息JSON
	deviceInfoBuf := make([]byte, deviceInfoSize)
	if _, err := io.ReadFull(s, deviceInfoBuf); err != nil {
		return entity.DeviceInfoJSON{}, fmt.Errorf("读取设备信息失败: %v", err)
	}

	// 解析设备信息
	var deviceInfo entity.DeviceInfoJSON
	if err := json.Unmarshal(deviceInfoBuf, &deviceInfo); err != nil {
		return entity.DeviceInfoJSON{}, fmt.Errorf("解析设备信息失败: %v", err)
	}

	log.Infof("异步读取设备信息成功: %+v", deviceInfo)
	return deviceInfo, nil
}
func (a *DefaultAuthenticator) getUserAndSidByConnectToken(token string) (*types.User, string, *jwt.CustomClaims, error) {
	claims, err := jwt.ValidateJWT(token)
	if err != nil {
		return nil, "", nil, err
	}
	if claims.Sid == "" {
		return nil, "", nil, fmt.Errorf("令牌无效（缺少会话），请重新登录")
	}
	ok, err := query.ValidateActiveUserSession(claims.Subject, claims.Sid)
	if err != nil {
		log.Errorf("QUIC会话校验失败 uid=%s sid=%s err=%v", claims.Subject, claims.Sid, err)
		return nil, "", nil, fmt.Errorf("会话校验服务不可用")
	}
	if !ok {
		log.Warnf("QUIC 会话无效 uid=%s sid=%s（已登出/被踢下线，或 refresh_token 已在其他端登录被吊销；JWT 可能仍未过期）", claims.Subject, claims.Sid)
		return nil, "", nil, &jwt.JWTExpiresError{Message: "身份令牌已失效"}
	}
	var user *types.User
	db.GetDB().Where("uid = ?", claims.Subject).First(&user)
	if user == nil || user.Uid == "" {
		return nil, "", nil, fmt.Errorf("用户不存在")
	}
	return user, claims.Sid, claims, nil
}
