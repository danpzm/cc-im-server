package user

import (
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/db/entity"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/utils"
)

type FriendRemarkUpdate struct {
	FriendUid string `json:"friend_uid" binding:"required"`
	Remark    string `json:"remark"`
}

// UpdateFriendRemark 更新当前用户给好友的备注。
func UpdateFriendRemark(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	req, err := utils.BodyToObj[FriendRemarkUpdate](c.Request.Body)
	if err != nil {
		response.BadRequest(c, "参数解析失败")
		return
	}
	if utf8.RuneCountInString(req.Remark) > 255 {
		response.BadRequest(c, "好友备注不能超过255个字符")
		return
	}
	areFriends, _, err := query.CheckFriendRelation(user.Uid, req.FriendUid)
	if err != nil {
		response.ServerError(c, "检查好友关系失败")
		return
	}
	if !areFriends {
		response.BadRequest(c, "对方不是您的好友")
		return
	}
	remarks, _ := query.GetFriendRemarkBatch(user.Uid, []string{req.FriendUid})
	beforeRemark := remarks[req.FriendUid]
	if err := query.UpdateFriendRemark(user.Uid, req.FriendUid, req.Remark); err != nil {
		response.ServerError(c, "更新好友备注失败")
		return
	}
	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpFriendRemark, req.FriendUid, map[string]any{
		"friend_uid": req.FriendUid, "remark": beforeRemark,
	}, map[string]any{
		"friend_uid": req.FriendUid, "remark": req.Remark,
	})
	response.Success(c, gin.H{
		"friend_uid": req.FriendUid,
		"remark":     req.Remark,
	})
}
