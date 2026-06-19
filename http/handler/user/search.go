package user

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
)

// UserSearchResponse 用户搜索分页结果。
type UserSearchResponse struct {
	List    []*query.UserWithStatus `json:"list"`
	HasMore bool                    `json:"has_more"`
}

// UserSearch 用户搜索
func UserSearch(c *gin.Context) {
	// 获取当前用户
	user := helper.GetUser(c)
	if user == nil {
		response.Unauthorized(c, "未登录")
		return
	}

	keyword := c.Query("keyword")
	limitStr := c.DefaultQuery("limit", "20")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	offsetStr := c.DefaultQuery("offset", "0")
	offset, err := strconv.Atoi(offsetStr)
	if err != nil || offset < 0 {
		offset = 0
	}

	// 多取一条用于判断 has_more
	users, err := query.SearchUsers(keyword, limit+1, offset, user.Uid)
	if err != nil {
		response.ServerError(c, "搜索用户失败")
		return
	}

	hasMore := len(users) > limit
	if hasMore {
		users = users[:limit]
	}

	response.Success(c, UserSearchResponse{
		List:    users,
		HasMore: hasMore,
	})
}
