package user

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
)

// NotificationList 获取消息通知列表
func NotificationList(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	pageStr := c.DefaultQuery("page", "1")
	page, err := strconv.Atoi(pageStr)
	if err != nil || page <= 0 {
		page = 1
	}
	limitStr := c.DefaultQuery("limit", "15")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 15
	}

	notifications, err := query.GetMessageNotificationList(user.Uid, page, limit)
	if err != nil {
		response.ServerError(c, "获取通知列表失败")
		return
	}
	response.Success(c, notifications)
}

// NotificationMarkAsRead 标记通知为已读
func NotificationMarkAsRead(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	nid := c.Query("nid")
	if nid == "" {
		response.BadRequest(c, "nid is required")
		return
	}
	notification, err := query.GetMessageNotificationByNid(nid)
	if err != nil {
		response.ServerError(c, "获取通知失败")
		return
	}
	if notification == nil {
		response.BadRequest(c, "通知不存在")
		return
	}
	now := time.Now().UnixMilli()
	err = query.MarkMessageNotificationAsRead(nid, now)
	if err != nil {
		response.ServerError(c, "标记通知为已读失败")
		return
	}
	notification.ReadAt = now
	response.Success(c, gin.H{"notification": notification})
}

// NotificationMarkAllAsRead 标记所有通知为已读
func NotificationMarkAllAsRead(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	err := query.MarkAllMessageNotificationAsRead(user.Uid)
	if err != nil {
		response.ServerError(c, "标记所有通知为已读失败")
		return
	}
	response.Success(c, gin.H{"uid": user.Uid})
}

// NotificationUnreadCount 获取未读通知数量
func NotificationUnreadCount(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	count, err := query.GetUnreadMessageNotificationCount(user.Uid)
	if err != nil {
		response.ServerError(c, "获取未读通知数量失败")
		return
	}
	response.Success(c, gin.H{"count": count})
}
