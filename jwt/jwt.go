package jwt

import (
	"fmt"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/google/uuid"
	"github.com/xd/quic-server/config"
)

const (
	defaultJWTKey             = "eW91cl9nZW5lcmF0ZWRfc2VjcmV0X2tleV9oZXJl"
	defaultAccessTokenExpire  = 2 * time.Hour
	defaultConnectTokenExpire = 5 * time.Minute
	defaultRefreshTokenExpire = 7 * 24 * time.Hour
)

func JWTKey() string {
	if cfg := config.GetJWTConfig(); cfg != nil && cfg.JWTKey != "" {
		return cfg.JWTKey
	}
	return defaultJWTKey
}

func AccessTokenExpire() time.Duration {
	if cfg := config.GetJWTConfig(); cfg != nil && cfg.AccessTokenExpire > 0 {
		return cfg.AccessTokenExpire
	}
	return defaultAccessTokenExpire
}

func ConnectTokenExpire() time.Duration {
	if cfg := config.GetJWTConfig(); cfg != nil && cfg.ConnectTokenExpire > 0 {
		return cfg.ConnectTokenExpire
	}
	return defaultConnectTokenExpire
}

func RefreshTokenExpire() time.Duration {
	if cfg := config.GetJWTConfig(); cfg != nil && cfg.RefreshTokenExpire > 0 {
		return cfg.RefreshTokenExpire
	}
	return defaultRefreshTokenExpire
}

// 定义一个结构体来表示 JWT 的声明

type JWTData struct {
	Jti       string
	Uid       string
	Sid       string // 会话 ID（UserSession.Sid），与 uid 一起写入 token
	Exp       time.Duration
	ExpiresAt time.Time // 非零时使用绝对过期时间（刷新轮换继承登录时的截止点）
}

// CustomClaims 自定义 JWT 声明（含 sid，与 uid 同存于 token）
type CustomClaims struct {
	jwt.StandardClaims
	Sid string `json:"sid"`
}

type JWTParseError struct {
	message string
}
type JWTExpiresError struct {
	Message string
}

func (e *JWTExpiresError) Error() string {
	return e.Message
}
func (e *JWTParseError) Error() string {
	return e.message
}

// 生成 JWT（含 uid 与 sid）
func generateJWT(data *JWTData) (string, error) {
	now := time.Now()
	expiresAt := now.Add(data.Exp)
	if !data.ExpiresAt.IsZero() {
		expiresAt = data.ExpiresAt
	}
	claims := &CustomClaims{
		StandardClaims: jwt.StandardClaims{
			Audience:  "pc",
			ExpiresAt: expiresAt.Unix(),
			IssuedAt:  now.Unix(),
			Issuer:    "server",
			NotBefore: now.Unix(),
			Id:        data.Jti,
			Subject:   data.Uid,
		},
		Sid: data.Sid,
	}

	// 创建一个新的 JWT 并签名
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(JWTKey()))
}

// CreateAccessToken 创建访问令牌，sid 为当前会话 ID（不能为空）
func CreateAccessToken(uid, sid string) (string, error) {
	if sid == "" {
		return "", fmt.Errorf("sid 不能为空")
	}
	jd := &JWTData{
		Jti: uuid.NewString(),
		Uid: uid,
		Sid: sid,
		Exp: AccessTokenExpire(),
	}
	token, err := generateJWT(jd)
	if err != nil {
		return "", err
	}
	return token, nil
}

// CreateRefreshToken 创建刷新令牌（登录时）：绝对过期 = now + REFRESH_TOKEN_EXPIRE。
func CreateRefreshToken(uid, sid string) (string, *JWTData, error) {
	if sid == "" {
		return "", nil, fmt.Errorf("sid 不能为空")
	}
	expiresAt := time.Now().Add(RefreshTokenExpire())
	jd := &JWTData{
		Jti:       uuid.NewString(),
		Uid:       uid,
		Sid:       sid,
		ExpiresAt: expiresAt,
	}
	token, err := generateJWT(jd)
	return token, jd, err
}

// CreateRefreshTokenWithExpiresAt 刷新轮换：继承原 refresh 链的绝对过期时间，不得续期。
func CreateRefreshTokenWithExpiresAt(uid, sid string, expiresAt time.Time) (string, *JWTData, error) {
	if sid == "" {
		return "", nil, fmt.Errorf("sid 不能为空")
	}
	if expiresAt.IsZero() {
		return "", nil, fmt.Errorf("expiresAt 不能为空")
	}
	jd := &JWTData{
		Jti:       uuid.NewString(),
		Uid:       uid,
		Sid:       sid,
		ExpiresAt: expiresAt,
	}
	token, err := generateJWT(jd)
	return token, jd, err
}

// ValidateJWT 验证 JWT 并返回自定义声明（含 Subject/uid 与 Sid）
func ValidateJWT(tokenString string) (*CustomClaims, error) {
	var claims = &CustomClaims{}
	// 解析并验证 JWT
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		return []byte(JWTKey()), nil
	})

	if err != nil {
		// 获取 exp 声明
		exp := claims.ExpiresAt
		// 获取当前时间的时间戳
		now := time.Now().Unix()
		// 比较当前时间与 exp 时间
		if now > exp {
			return nil, &JWTExpiresError{Message: err.Error()}
		}
	}
	if err != nil {
		return nil, &JWTParseError{message: err.Error()}
	}

	if !token.Valid {
		return nil, &JWTParseError{message: "token 验证失败"}
	}

	return claims, nil
}
