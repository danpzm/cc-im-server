package auth

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/jwt"
	"github.com/xd/quic-server/pkg/types"
	"gorm.io/gorm"
)

type refreshTokenClaims struct {
	User      *types.User
	Claims    *jwt.CustomClaims
	Jti       string
	ExpiresAt int64 // 毫秒；登录时确立的绝对过期，轮换时继承
}

func parseValidRefreshToken(refreshToken string) (*refreshTokenClaims, error) {
	claims, err := jwt.ValidateJWT(refreshToken)
	if err != nil {
		return nil, errors.New("refresh_token无法验证")
	}
	var row types.UserRefreshToken
	if err := db.GetDB().Where("jti = ? AND revoked = ?", claims.Id, false).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("refresh_token无效")
		}
		return nil, err
	}
	if time.Now().UnixMilli() > row.ExpiresAt {
		return nil, errors.New("refresh_token已过期")
	}
	if row.Uid != claims.Subject {
		return nil, errors.New("refresh_token无效")
	}
	user, err := query.GetUserByUid(row.Uid)
	if err != nil || user == nil || user.Uid == "" {
		return nil, errors.New("用户不存在")
	}
	return &refreshTokenClaims{User: user, Claims: claims, Jti: claims.Id, ExpiresAt: row.ExpiresAt}, nil
}

// resolveSessionForRefresh 刷新时优先复用 refresh token 内仍有效的 sid，避免无谓新建会话导致 QUIC 鉴权 sid 失效。
func resolveSessionForRefresh(c *gin.Context, uid string, claims *jwt.CustomClaims) (string, error) {
	if claims != nil && claims.Sid != "" {
		ok, err := query.ValidateActiveUserSession(uid, claims.Sid)
		if err != nil {
			return "", err
		}
		if ok {
			if err := query.TouchUserSessionActivity(claims.Sid); err != nil {
				log.Warnf("刷新时会话已失效 sid=%s: %v，将创建新会话", claims.Sid, err)
			} else {
				return claims.Sid, nil
			}
		}
	}
	return createHttpUserSession(c, uid)
}

func revokeRefreshTokenJti(tx *gorm.DB, jti string) error {
	return tx.Model(&types.UserRefreshToken{}).
		Where("jti = ? AND revoked = ?", jti, false).
		Update("revoked", true).Error
}

// issueRotatedTokens 刷新令牌：仅吊销当前 jti，复用 sid；refresh 绝对过期时间继承自登录，不续期。
func issueRotatedTokens(c *gin.Context, rtc *refreshTokenClaims, clusterRequired bool) {
	cluster := fetchLoginClusterAddrsBestEffort(c.Request.Context())
	if clusterRequired {
		var err error
		cluster, err = fetchLoginClusterAddrs(c.Request.Context())
		if err != nil {
			log.Errorf("拉取集群节点失败: %v", err)
			response.ServerError(c, "获取集群节点失败，请稍后重试")
			return
		}
		if len(cluster.Quic) == 0 || len(cluster.HTTP) == 0 || len(cluster.OSS) == 0 {
			response.ServerError(c, "集群节点不可用")
			return
		}
	}

	sid, err := resolveSessionForRefresh(c, rtc.User.Uid, rtc.Claims)
	if err != nil {
		if errors.Is(err, ErrMissingDeviceId) {
			response.BadRequest(c, "缺少 X-Device-Id 请求头，无法绑定设备")
			return
		}
		response.ServerError(c, "续期会话失败")
		return
	}

	tx := db.GetDB().Begin()
	if err := revokeRefreshTokenJti(tx, rtc.Jti); err != nil {
		tx.Rollback()
		response.ServerError(c, "吊销刷新令牌失败")
		return
	}

	accessToken, err := jwt.CreateAccessToken(rtc.User.Uid, sid)
	if err != nil {
		tx.Rollback()
		response.ServerError(c, "生成身份令牌失败")
		return
	}
	refreshToken, data, err := jwt.CreateRefreshTokenWithExpiresAt(
		rtc.User.Uid,
		sid,
		time.UnixMilli(rtc.ExpiresAt),
	)
	if err != nil {
		tx.Rollback()
		response.ServerError(c, "生成刷新令牌失败")
		return
	}
	if err := tx.Create(&types.UserRefreshToken{
		Revoked:   false,
		Uid:       data.Uid,
		Jti:       data.Jti,
		ExpiresAt: rtc.ExpiresAt,
		UserAgent: c.Request.UserAgent(),
		IP:        c.ClientIP(),
	}).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "数据库错误")
		return
	}
	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "数据库错误")
		return
	}

	payload := gin.H{
		"access_token":   accessToken,
		"refresh_token":  refreshToken,
		"quic_addrs":     cluster.Quic,
		"http_base_urls": cluster.HTTP,
		"oss_base_urls":  cluster.OSS,
	}
	response.Success(c, payload)
}
