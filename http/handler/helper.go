package helper

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/notify"
	"github.com/xd/quic-server/pkg/types"
	pushpkg "github.com/xd/quic-server/push"
	"github.com/xd/quic-server/queue"
)

const ContextKeySid = "sid"

// GetUser 从上下文获取当前登录用户
func GetUser(c *gin.Context) *types.User {
	user, exists := c.Get("user")
	if !exists {
		response.Unauthorized(c, "用户未登录")
		return nil
	}
	if user, ok := user.(*types.User); ok {
		return user
	} else {
		response.Unauthorized(c, "获取用户信息失败")
		return nil
	}
}

// GetSid 从上下文获取当前请求的会话 ID（UserSession.Sid）
// 客户端可通过请求头 X-Session-Id 传递（如长连接建立后获得的 sid），未传则返回空字符串
func GetSid(c *gin.Context) string {
	sid, _ := c.Get(ContextKeySid)
	if s, ok := sid.(string); ok {
		return s
	}
	return ""
}

// NotifyQuic 向持有在线连接的 QUIC 节点定向投递（Redis Pub/Sub）
func NotifyQuic(msgType notify.MessageType, payload any) error {
	return pushpkg.Send(context.Background(), msgType, payload)
}

// PublishUserOperationLog 异步写入用户操作记录。
func PublishUserOperationLog(c *gin.Context, uid string, opType entity.UserOperationType, relatedId string, before, after map[string]any) {
	_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
		Uid:        uid,
		OpType:     opType,
		Sid:        GetSid(c),
		RelatedId:  relatedId,
		BeforeData: before,
		AfterData:  after,
	}, 0)
}
