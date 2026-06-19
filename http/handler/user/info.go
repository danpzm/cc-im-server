package user

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
)

func RoomUserInfoByUidList(c *gin.Context) {
	uidList := c.Query("uid_list")
	if uidList == "" {
		response.BadRequest(c, "uid_list is required")
		return
	}
	currentUser := helper.GetUser(c)
	currentUid := ""
	if currentUser != nil {
		currentUid = currentUser.Uid
	}
	users, err := query.GetRoomUserInfoByUidList(strings.Split(uidList, ","), currentUid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	response.Success(c, users)
}

func CardUserInfoByUid(c *gin.Context) {
	uid := strings.TrimSpace(c.Query("uid"))
	if uid == "" {
		response.BadRequest(c, "uid is required")
		return
	}
	currentUser := helper.GetUser(c)
	currentUid := ""
	if currentUser != nil {
		currentUid = currentUser.Uid
	}
	rid := strings.TrimSpace(c.Query("rid"))
	user, err := query.GetCardUserInfoByUid(uid, currentUid, rid)
	if err != nil {
		if rid != "" && (err.Error() == "rid requires login" || err.Error() == "viewer not in room" || err.Error() == "target not in room") {
			response.BadRequest(c, err.Error())
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if user == nil {
		response.Success(c, gin.H{})
		return
	}
	response.Success(c, user)
}
