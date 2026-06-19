package user

import (
	"github.com/gin-gonic/gin"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
)

// FriendGroupList 获取当前用户的好友分组列表
func FriendGroupList(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	groups, err := query.GetFriendGroupList(user.Uid)
	if err != nil {
		response.ServerError(c, "获取好友分组列表失败")
		return
	}
	response.Success(c, groups)
}
