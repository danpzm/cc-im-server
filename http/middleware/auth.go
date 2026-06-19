package middleware

import (
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/jwt"
	"github.com/xd/quic-server/utils"
)

func Authorization() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		if token == "" {
			response.Unauthorized(c, "找不到身份令牌")
			return
		}
		token = token[7:]
		claims, err := jwt.ValidateJWT(token)
		if err != nil {
			if utils.TypeEq(err, &jwt.JWTExpiresError{}) {
				response.PreconditionFailed(c, "身份令牌已失效")
				return
			}
			response.Unauthorized(c, "无法解析身份令牌")
			return
		}
		if claims.Sid == "" {
			response.Unauthorized(c, "令牌无效（缺少会话）")
			return
		}
		ok, err := query.ValidateActiveUserSession(claims.Subject, claims.Sid)
		if err != nil {
			log.Errorf("会话校验失败 uid=%s sid=%s err=%v", claims.Subject, claims.Sid, err)
			response.ServiceUnavailable(c, "会话校验服务不可用")
			return
		}
		if !ok {
			response.Unauthorized(c, "非法令牌")
			return
		}
		user, err := query.GetUserByUid(claims.Subject)
		if err != nil {
			response.TokenExpires(c, "获取用户错误")
			return
		}
		if user == nil {
			response.Unauthorized(c, "用户不存在")
			return
		}
		c.Set("user", user)
		c.Set("sid", claims.Sid) // sid 与 uid 同存于 token
		c.Next()
	}
}
