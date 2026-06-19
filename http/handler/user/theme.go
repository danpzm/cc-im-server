package user

import (
	"github.com/gin-gonic/gin"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/http/response"
)

// GetUserTheme GET /api/v1/user/theme — 返回当前用户的主题 JSON
func GetUserTheme(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	theme, err := query.GetUserTheme(user.Uid)
	if err != nil {
		response.ServerError(c, "获取主题失败")
		return
	}
	response.Success(c, gin.H{"theme_json": theme.ThemeJson})
}

type updateThemeBody struct {
	ThemeJson string `json:"theme_json"`
}

// UpdateUserTheme PUT /api/v1/user/theme — 保存当前用户的主题 JSON
func UpdateUserTheme(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body updateThemeBody
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if len(body.ThemeJson) > 2*1024*1024 {
		response.BadRequest(c, "主题数据过大")
		return
	}
	beforeTheme, _ := query.GetUserTheme(user.Uid)
	beforeJSON := ""
	if beforeTheme != nil {
		beforeJSON = beforeTheme.ThemeJson
	}
	if err := query.UpsertUserTheme(user.Uid, body.ThemeJson); err != nil {
		response.ServerError(c, "保存主题失败")
		return
	}
	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpThemeUpdate, user.Uid, map[string]any{
		"theme_json": beforeJSON,
	}, map[string]any{
		"theme_json": body.ThemeJson,
	})
	response.Success(c, gin.H{"theme_json": body.ThemeJson})
}
