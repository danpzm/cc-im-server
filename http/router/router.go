package router

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/gin-gonic/gin"
	authHandler "github.com/xd/quic-server/http/handler/auth"
	desktopHandler "github.com/xd/quic-server/http/handler/desktop"
	mediaHandler "github.com/xd/quic-server/http/handler/media"
	roomHandler "github.com/xd/quic-server/http/handler/room"
	sessionHandler "github.com/xd/quic-server/http/handler/session"
	userHandler "github.com/xd/quic-server/http/handler/user"
	"github.com/xd/quic-server/http/middleware"
)

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		log.Infof("CORSMiddleware: %v", c.Request.URL.Query())
		// 设置响应头
		c.Header("Access-Control-Allow-Origin", "*") // 允许的源
		c.Header("Access-Control-Allow-Methods", "*")
		c.Header("Access-Control-Allow-Headers", "Origin, X-Requested-With, Content-Type, Accept, Authorization")
		c.Header("Access-Control-Expose-Headers", "Content-Length, Access-Control-Allow-Origin")
		c.Header("Access-Control-Max-Age", "12h")
		c.Header("Access-Control-Allow-Credentials", "true")

		// 如果是预检请求，直接返回
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		// 继续处理后续请求
		c.Next()
	}
}

// servePublicStatic 在无任何路由匹配时，从 ./public 提供根路径静态文件（如 GET /xx.txt）。
// 不能用 GET /*path：会与 /api/... 在 Gin 路由树上冲突（panic）。
func servePublicStatic(c *gin.Context) {
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/api/") {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Status(http.StatusNotFound)
		return
	}

	rel := strings.TrimPrefix(path, "/")
	if rel == "" {
		c.Status(http.StatusNotFound)
		return
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if strings.Contains(rel, "..") {
		c.Status(http.StatusNotFound)
		return
	}

	rootAbs, err := filepath.Abs("public")
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	fullAbs, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(rel)))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	sep := string(filepath.Separator)
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs+sep, rootAbs+sep) {
		c.Status(http.StatusNotFound)
		return
	}

	st, err := os.Stat(fullAbs)
	if err != nil || st.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	c.File(fullAbs)
}

func Register(g *gin.Engine) {
	authHandler.Test()
	g.Use(CORSMiddleware())
	g.GET("/join/room", roomHandler.RoomJoinLandingPage)
	g.GET("/api/v1/public/room/invite/preview", roomHandler.RoomInvitePreview)
	// Tauri 2 桌面端动态更新（plugins.updater.endpoints，无鉴权；无更新返回 204）
	g.GET("/api/v1/public/desktop/update/:target/:arch/:current_version", desktopHandler.TauriUpdaterCheck)
	g.GET("/api/v1/public/desktop/web-integrity/:version/web-manifest.json", desktopHandler.WebIntegrityManifest)
	g.GET("/api/v1/public/desktop/web-integrity/:version/web-manifest.json.sig", desktopHandler.WebIntegrityManifestSig)
	// 文件上传、按 uf_id 查询已迁移至 OSS 服务端口，此处不再注册
	api := g.Group("/api/v1/")
	{
		// 动态添加路由和中间件
		api.POST("/login/email", authHandler.EmailLogin)
		api.POST("/login/remember", authHandler.RememberLogin)
		api.POST("/auth/email/verify-code/send", authHandler.EmailVerifyCodeSend)
		api.PUT("/register/email", authHandler.EmailRegister)
		api.PUT("/register/username", authHandler.UsernameRegister)
		api.PUT("/test/register/username", authHandler.TestUsernameRegister)
		api.POST("/refresh/access_token", authHandler.RefreshAccessToken)
		api.POST("/logout", authHandler.Logout).Use(middleware.Authorization())
	}
	room := g.Group("/api/v1/room").Use(middleware.Authorization())
	{
		room.POST("/create", roomHandler.RoomCreate)
		room.GET("/list", roomHandler.RoomList)
		room.GET("/detail", roomHandler.RoomDetail)
		room.POST("/join", roomHandler.RoomJoin)
		room.POST("/leave", roomHandler.RoomLeave)
		room.POST("/member/kick", roomHandler.RoomMemberKick)
		room.POST("/invite/create", roomHandler.RoomInviteCreate)
		room.POST("/join/invite", roomHandler.RoomJoinInvite)
		room.GET("/user/ids", roomHandler.RoomUserIds)
		room.POST("/user/nickname/update", roomHandler.RoomUserNicknameUpdate)
		room.POST("/user/remark/update", roomHandler.RoomUserRemarkUpdate)
		room.GET("/message/list", roomHandler.RoomMessageList)
		room.POST("/message/withdraw", roomHandler.RoomMessageWithdraw)
		room.GET("/pinned/message", roomHandler.RoomPinnedMessageGet)
		room.POST("/pinned/message/pin", roomHandler.RoomPinnedMessagePin)
		room.POST("/pinned/message/unpin", roomHandler.RoomPinnedMessageUnpin)
		room.POST("/name/update", roomHandler.RoomNameUpdate)
		room.POST("/password/update", roomHandler.RoomPasswordUpdate)
		room.POST("/join/config/update", roomHandler.RoomJoinConfigUpdate)
		room.POST("/join/apply", roomHandler.RoomJoinApply)
		room.POST("/join/request/accept", roomHandler.RoomJoinRequestAccept)
		room.POST("/join/request/reject", roomHandler.RoomJoinRequestReject)
		room.POST("/avatar/update", roomHandler.RoomAvatarUpdate)
		room.GET("/avatar/history", roomHandler.RoomAvatarHistory)
		room.POST("/user/role/update", roomHandler.RoomUserRoleUpdate)
		room.POST("/owner/transfer", roomHandler.RoomOwnerTransfer)
		room.POST("/dissolve", roomHandler.RoomDissolve)
		room.GET("/user/list", roomHandler.RoomUserList)
		room.GET("/mute/config", roomHandler.RoomMuteConfig)
		room.POST("/mute/config/update", roomHandler.RoomMuteConfigUpdate)
		room.GET("/mute/status", roomHandler.RoomMuteStatus)
		room.POST("/mute", roomHandler.RoomMute)
		room.POST("/mute/unmute", roomHandler.RoomUnmute)
		room.POST("/mute/all", roomHandler.RoomMuteAll)
		room.GET("/announcement/list", roomHandler.RoomAnnouncementList)
		room.GET("/announcement", roomHandler.RoomAnnouncementGet)
		room.POST("/announcement/create", roomHandler.RoomAnnouncementCreate)
		room.POST("/announcement/update", roomHandler.RoomAnnouncementUpdate)
		room.POST("/announcement/delete", roomHandler.RoomAnnouncementDelete)
		room.POST("/block", roomHandler.RoomBlock)
		room.POST("/unblock", roomHandler.RoomUnblock)
		room.POST("/block-user", roomHandler.RoomBlockUser)
		room.POST("/unblock-user", roomHandler.RoomUnblockUser)
	}
	session := g.Group("/api/v1/session").Use(middleware.Authorization())
	{
		session.GET("/list", sessionHandler.SessionList)
		session.GET("/search", sessionHandler.SessionSearch)
		session.POST("/update-last-seq-id", sessionHandler.UpdateLastSeqId)
		session.POST("/update-last-mention-seq-id", sessionHandler.UpdateLastMentionSeqId)
		session.POST("/update-top", sessionHandler.UpdateSessionTop)
	}
	user := g.Group("/api/v1/user").Use(middleware.Authorization())
	{
		user.POST("/profile/update", userHandler.ProfileUpdate)
		user.GET("/avatar/history", userHandler.UserAvatarHistory)
		user.GET("/room-user/list", userHandler.RoomUserInfoByUidList)
		user.GET("/card/info", userHandler.CardUserInfoByUid)
		user.GET("/search", userHandler.UserSearch)
		user.GET("/friend/group/list", userHandler.FriendGroupList)
		user.POST("/friend/request/create", userHandler.CreateFriendRequest)
		user.GET("/friend/request/list", userHandler.FriendRequestList)
		user.POST("/friend/request/accept", userHandler.AcceptFriendRequest)
		user.POST("/friend/request/reject", userHandler.RejectFriendRequest)
		user.POST("/friend/delete", userHandler.DeleteFriend)
		user.POST("/friend/remark/update", userHandler.UpdateFriendRemark)
		user.POST("/friend/chat/open", userHandler.StartFriendChat)
		user.POST("/group-private-chat/open", userHandler.StartGroupPrivateChat)
		user.GET("/notification/list", userHandler.NotificationList)
		user.POST("/notification/mark-read", userHandler.NotificationMarkAsRead)
		user.POST("/notification/mark-all-read", userHandler.NotificationMarkAllAsRead)
		user.GET("/notification/unread-count", userHandler.NotificationUnreadCount)
		user.GET("/theme", userHandler.GetUserTheme)
		user.PUT("/theme", userHandler.UpdateUserTheme)
		user.GET("/emoji/recent/list", userHandler.EmojiRecentList)
		user.POST("/emoji/recent/record", userHandler.EmojiRecentRecord)
		user.GET("/emoji/favorite/list", userHandler.EmojiFavoriteList)
		user.POST("/emoji/favorite/add", userHandler.EmojiFavoriteAdd)
		user.POST("/emoji/favorite/remove", userHandler.EmojiFavoriteRemove)
		user.POST("/emoji/favorite/image/download", userHandler.EmojiFavoriteImageDownload)
	}
	media := g.Group("/api/v1/media").Use(middleware.Authorization())
	{
		media.POST("/stream/join-sign", mediaHandler.IssueJoinSign)
		media.POST("/stream/call/create", mediaHandler.CreateCallInvite)
		media.GET("/stream/call/current", mediaHandler.CurrentCall)
		media.POST("/stream/call/accept", mediaHandler.AcceptCallInvite)
		media.POST("/stream/call/joined", mediaHandler.MarkCallMemberJoined)
		media.POST("/stream/call/leave", mediaHandler.LeaveCall)
		media.POST("/stream/call/reject", mediaHandler.RejectCallInvite)
		media.POST("/stream/call/hangup", mediaHandler.HangupCall)
	}

	// 根路径静态文件：GET /foo.txt -> ./public/foo.txt（须在全部业务路由之后注册）
	g.NoRoute(servePublicStatic)
}
