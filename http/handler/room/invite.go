package room

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/utils"
)

const roomInviteDefaultTTL = 7 * 24 * time.Hour

//go:embed join_room_landing.html
var joinRoomLandingHTML string

func newInviteToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func inviteLandingBaseURL() string {
	if config.GetServerConfig() == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(config.GetServerConfig().InviteWebBaseURL), "/")
}

func inviteWebURL(token string) string {
	base := inviteLandingBaseURL()
	if base == "" {
		return ""
	}
	return base + "/join/room?t=" + url.QueryEscape(token)
}

func loadInviteByTokenOrBadRequest(c *gin.Context, token string) *entity.RoomInvite {
	token = strings.TrimSpace(token)
	if token == "" {
		response.BadRequest(c, "缺少邀请参数")
		return nil
	}
	invite, err := query.GetActiveRoomInviteByToken(token, time.Now().UnixMilli())
	if err != nil {
		log.Errorf("读取邀请记录失败 token=%s err=%v", token, err)
		response.ServerError(c, "读取邀请失败")
		return nil
	}
	if invite == nil {
		response.BadRequest(c, "邀请无效或已过期")
		return nil
	}
	return invite
}

// RoomInviteCreate 成员为当前房间创建邀请 token，写入数据库并返回网页落地链接与深度链接。
func RoomInviteCreate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	body, err := utils.BodyToObj[struct {
		Rid string `json:"rid"`
	}](c.Request.Body)
	if err != nil || strings.TrimSpace(body.Rid) == "" {
		response.BadRequest(c, "请输入房间id")
		return
	}
	rid := strings.TrimSpace(body.Rid)
	if rejectIfRoomDissolved(c, rid) {
		return
	}
	if !query.HasRoomUser(rid, user.Uid) {
		response.BadRequest(c, "仅房间成员可创建邀请链接")
		return
	}
	room, err := query.GetRoomByRid(rid)
	if err != nil || room == nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	if room.Type == types.PrivateRoom || room.Type == types.GroupPrivateRoom || room.Type == types.SelfChatRoom {
		response.BadRequest(c, "该房间类型不支持邀请链接")
		return
	}
	webJoinBase := inviteLandingBaseURL()
	if webJoinBase == "" {
		response.ServerError(c, "INVITE_WEB_BASE_URL 未配置")
		return
	}
	token, err := newInviteToken()
	if err != nil {
		response.ServerError(c, "生成邀请令牌失败")
		return
	}
	nowMs := time.Now().UnixMilli()
	invite := &entity.RoomInvite{
		Token:      token,
		Rid:        rid,
		InviterUid: user.Uid,
		// 邀请链接仅免房间密码，不免加入审批
		BypassPassword: true,
		ExpiresAt:      nowMs + roomInviteDefaultTTL.Milliseconds(),
	}
	if err := query.CreateRoomInvite(invite); err != nil {
		log.Errorf("写入邀请记录失败: %v", err)
		response.ServerError(c, "创建邀请失败")
		return
	}
	webJoin := inviteWebURL(token)
	response.Success(c, gin.H{
		"invite_id":      invite.InviteId,
		"token":          token,
		"web_join_url":   webJoin,
		"deep_link":      "ccquic://join?t=" + url.QueryEscape(token),
		"expires_in_sec": int(roomInviteDefaultTTL / time.Second),
		"expires_at":     invite.ExpiresAt,
	})
}

// RoomInvitePreview 公开：校验 token 后返回房间概要（不含密码）。
func RoomInvitePreview(c *gin.Context) {
	invite := loadInviteByTokenOrBadRequest(c, c.Query("t"))
	if invite == nil {
		return
	}
	room, err := query.GetRoomByRid(invite.Rid)
	if err != nil || room == nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	inviter, _ := query.GetUserByUid(invite.InviterUid)
	inviterName := invite.InviterUid
	if inviter != nil && strings.TrimSpace(inviter.Nickname) != "" {
		inviterName = inviter.Nickname
	}
	response.Success(c, gin.H{
		"invite_id":          invite.InviteId,
		"rid":                room.Rid,
		"name":               room.Name,
		"avatar_uf_id":       room.AvatarUfId,
		"member_count":       room.MemberCount,
		"has_password":       room.Password != "",
		"description":        room.Description,
		"inviter_uid":        invite.InviterUid,
		"inviter_name":       inviterName,
		"invite_token":       invite.Token,
		"web_join_url":       inviteWebURL(invite.Token),
		"deep_link":          "ccquic://join?t=" + url.QueryEscape(invite.Token),
		"expires_at":         invite.ExpiresAt,
		"join_success_count": invite.JoinSuccessCount,
		"join_approval_required": room.JoinApprovalRequired,
		"join_question_enabled":  room.JoinQuestionEnabled,
		"join_question":          room.JoinQuestion,
	})
}

// RoomJoinInvite 使用邀请 token 加入房间（有效邀请可免房间密码，但不免加入审批）。
func RoomJoinInvite(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	body, err := utils.BodyToObj[struct {
		Token string `json:"token"`
	}](c.Request.Body)
	if err != nil || strings.TrimSpace(body.Token) == "" {
		response.BadRequest(c, "缺少邀请令牌")
		return
	}
	invite := loadInviteByTokenOrBadRequest(c, body.Token)
	if invite == nil {
		return
	}
	rid := invite.Rid
	room, err := query.GetRoomByRid(rid)
	if err != nil || room == nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	if room.Type == types.PrivateRoom || room.Type == types.GroupPrivateRoom || room.Type == types.SelfChatRoom {
		response.BadRequest(c, "无法加入该房间")
		return
	}
	if room.Password != "" && !invite.BypassPassword {
		response.BadRequest(c, "该邀请无法用于加入有密码房间")
		return
	}
	if query.HasRoomUser(rid, user.Uid) {
		response.Success(c, gin.H{
			"rid":            invite.Rid,
			"invite_id":      invite.InviteId,
			"inviter_uid":    invite.InviterUid,
			"join_uid":       user.Uid,
			"already_joined": true,
		})
		return
	}
	if room.JoinApprovalRequired {
		if pending, _ := query.GetPendingRoomJoinRequestByRidAndApplicant(rid, user.Uid); pending != nil {
			response.BadRequest(c, "您已提交加入申请，请等待管理员审批")
			return
		}
		respondJoinApprovalRequired(c, room)
		return
	}
	rsid, errJoin := joinRoomCommittedAfterValidation(c, user, room, invite.InviterUid)
	if errJoin != nil {
		log.Errorf("邀请加入房间失败: %v", errJoin)
		response.ServerError(c, "加入房间失败")
		return
	}
	if err := query.RecordRoomInviteJoin(invite, user.Uid); err != nil {
		log.Errorf("记录邀请加入失败 invite_id=%s join_uid=%s err=%v", invite.InviteId, user.Uid, err)
	}
	response.Success(c, gin.H{
		"rsid":        rsid,
		"rid":         invite.Rid,
		"invite_id":   invite.InviteId,
		"inviter_uid": invite.InviterUid,
		"join_uid":    user.Uid,
	})
}

// RoomJoinLandingPage 浏览器落地页：尝试唤起客户端并展示说明。
func RoomJoinLandingPage(c *gin.Context) {
	log.Infof("RoomJoinLandingPage: %v", c.Request.URL.Query())
	t := strings.TrimSpace(c.Query("t"))
	deep := "ccquic://join"
	if t != "" {
		deep = "ccquic://join?t=" + url.QueryEscape(t)
	}
	deepEsc := html.EscapeString(deep)
	page := strings.ReplaceAll(joinRoomLandingHTML, "{{DEEP_LINK}}", deepEsc)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(page))
}
