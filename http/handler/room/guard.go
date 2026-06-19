package room

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/http/response"
	"gorm.io/gorm"
)

const roomDissolvedCode = "ROOM_DISSOLVED"
const roomDissolvedMessage = "该群已被群主解散或被删除"

// rejectIfRoomDissolved 房间已解散时返回 false 并写入响应；调用方应直接 return。
func rejectIfRoomDissolved(c *gin.Context, rid string) bool {
	if rid == "" {
		return false
	}
	dissolved, err := query.IsRoomDissolved(rid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.BadRequest(c, "房间不存在")
			return true
		}
		response.ServerError(c, "查询房间失败")
		return true
	}
	if dissolved {
		response.Json(c, http.StatusForbidden, gin.H{
			"code":    roomDissolvedCode,
			"message": roomDissolvedMessage,
		})
		return true
	}
	return false
}
