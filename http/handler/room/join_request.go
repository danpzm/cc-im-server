package room

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/notify"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/queue"
	"github.com/xd/quic-server/utils"
)

type roomJoinConfigUpdateBody struct {
	Rid                     string `json:"rid"`
	JoinApprovalRequired    *bool  `json:"join_approval_required"`
	JoinQuestionEnabled     *bool  `json:"join_question_enabled"`
	JoinQuestion            string `json:"join_question"`
	JoinQuestionAnswer      string `json:"join_question_answer"`
	JoinQuestionAutoApprove *bool  `json:"join_question_auto_approve"`
}

type roomJoinApplyBody struct {
	Rid      string `json:"rid"`
	Password string `json:"password"`
	Token    string `json:"token"` // 有效邀请 token 仅免密，不免审批
	Message  string `json:"message"`
	Answer   string `json:"answer"`
}

type roomJoinRequestHandleBody struct {
	RjrId string `json:"rjr_id"`
}

// validatePublicRoomJoin 校验公开群加入前置条件（密码或有效邀请 token、房间类型、是否已是成员）
func validatePublicRoomJoin(c *gin.Context, user *types.User, rid, password, inviteToken string) (*types.Room, bool) {
	rid = strings.TrimSpace(rid)
	if rid == "" {
		response.BadRequest(c, "请输入房间id")
		return nil, false
	}
	room, err := query.GetRoomByRid(rid)
	if room == nil || err != nil {
		response.BadRequest(c, "房间不存在")
		return nil, false
	}
	if room.Password != "" {
		token := strings.TrimSpace(inviteToken)
		if token != "" {
			invite, err := query.GetActiveRoomInviteByToken(token, time.Now().UnixMilli())
			if err != nil {
				log.Errorf("validatePublicRoomJoin invite err: %v", err)
				response.ServerError(c, "校验邀请失败")
				return nil, false
			}
			if invite == nil || invite.Rid != rid || !invite.BypassPassword {
				response.BadRequest(c, "邀请无效或已过期")
				return nil, false
			}
		} else if err := utils.PasswordCompare(room.Password, password); err != nil {
			response.BadRequest(c, "请输入正确的房间密码")
			return nil, false
		}
	}
	if room.Type == types.PrivateRoom {
		response.BadRequest(c, "无法加入该房间")
		return nil, false
	}
	if query.HasRoomUser(rid, user.Uid) {
		response.BadRequest(c, "已加入该房间")
		return nil, false
	}
	return room, true
}

// RoomJoinConfigUpdate 更新房间加入审批设置（房主/管理员）
func RoomJoinConfigUpdate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomJoinConfigUpdateBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.CanUserMuteInRoom(body.Rid, user.Uid) {
		response.BadRequest(c, "仅管理员或房主可修改加入设置")
		return
	}
	room, err := query.GetRoomByRid(body.Rid)
	if room == nil || err != nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	beforeData := map[string]any{
		"join_approval_required":     room.JoinApprovalRequired,
		"join_question_enabled":      room.JoinQuestionEnabled,
		"join_question":              room.JoinQuestion,
		"join_question_auto_approve": room.JoinQuestionAutoApprove,
	}
	updates := map[string]any{}
	if body.JoinApprovalRequired != nil {
		updates["join_approval_required"] = *body.JoinApprovalRequired
	}
	if body.JoinQuestionEnabled != nil {
		updates["join_question_enabled"] = *body.JoinQuestionEnabled
		if *body.JoinQuestionEnabled {
			updates["join_question"] = strings.TrimSpace(body.JoinQuestion)
			updates["join_question_answer"] = strings.TrimSpace(body.JoinQuestionAnswer)
		} else {
			updates["join_question"] = ""
			updates["join_question_answer"] = ""
			updates["join_question_auto_approve"] = false
		}
	}
	if body.JoinQuestionAutoApprove != nil {
		updates["join_question_auto_approve"] = *body.JoinQuestionAutoApprove
	}
	if len(updates) == 0 {
		response.BadRequest(c, "没有可更新的字段")
		return
	}
	if body.JoinQuestionEnabled != nil && *body.JoinQuestionEnabled {
		if strings.TrimSpace(body.JoinQuestion) == "" {
			response.BadRequest(c, "请填写验证问题")
			return
		}
		if strings.TrimSpace(body.JoinQuestionAnswer) == "" {
			response.BadRequest(c, "请填写验证答案")
			return
		}
	}
	if q, ok := updates["join_question"].(string); ok && utf8.RuneCountInString(q) > 256 {
		response.BadRequest(c, "验证问题不能超过256个字符")
		return
	}
	if a, ok := updates["join_question_answer"].(string); ok && utf8.RuneCountInString(a) > 128 {
		response.BadRequest(c, "验证答案不能超过128个字符")
		return
	}
	if err := db.GetDB().Model(&types.Room{}).Where("rid = ?", body.Rid).Updates(updates).Error; err != nil {
		response.ServerError(c, "更新失败")
		return
	}
	afterRoom, _ := query.GetRoomByRid(body.Rid)
	afterData := map[string]any{
		"join_approval_required":     afterRoom.JoinApprovalRequired,
		"join_question_enabled":      afterRoom.JoinQuestionEnabled,
		"join_question":              afterRoom.JoinQuestion,
		"join_question_auto_approve": afterRoom.JoinQuestionAutoApprove,
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpJoinConfigUpdate, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: body.Rid,
		BeforeData: beforeData, AfterData: afterData,
	}, 0)
	response.Success(c, gin.H{
		"join_approval_required":     afterRoom.JoinApprovalRequired,
		"join_question_enabled":      afterRoom.JoinQuestionEnabled,
		"join_question":              afterRoom.JoinQuestion,
		"join_question_auto_approve": afterRoom.JoinQuestionAutoApprove,
		"join_question_answer":       strings.TrimSpace(afterRoom.JoinQuestionAnswer),
	})
}

// RoomJoinApply 提交房间加入申请（密码验证通过后）
func RoomJoinApply(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomJoinApplyBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	room, ok := validatePublicRoomJoin(c, user, body.Rid, body.Password, body.Token)
	if !ok {
		return
	}
	if !room.JoinApprovalRequired {
		response.BadRequest(c, "该房间无需申请，可直接加入")
		return
	}
	answer := strings.TrimSpace(body.Answer)
	message := strings.TrimSpace(body.Message)
	if room.JoinQuestionEnabled {
		if answer == "" {
			response.BadRequest(c, "请回答验证问题")
			return
		}
		if utf8.RuneCountInString(answer) > 128 {
			response.BadRequest(c, "回答不能超过128个字符")
			return
		}
		message = answer
	} else if utf8.RuneCountInString(message) > 255 {
		response.BadRequest(c, "申请留言不能超过255个字符")
		return
	}
	if room.JoinQuestionEnabled && room.JoinQuestionAutoApprove {
		expected := strings.TrimSpace(room.JoinQuestionAnswer)
		if expected != "" && answer == expected {
			rsid, err := joinRoomCommittedAfterValidation(c, user, room, "")
			if err != nil {
				log.Errorf("验证答案正确自动加入失败: %v", err)
				response.ServerError(c, "加入房间失败")
				return
			}
			_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
				Uid: user.Uid, OpType: entity.UserOpRoomJoinApply, Sid: helper.GetSid(c), RelatedId: body.Rid,
				BeforeData: nil, AfterData: map[string]any{"rid": body.Rid, "auto_approved": true},
			}, 0)
			response.Success(c, gin.H{
				"auto_approved": true,
				"rsid":          rsid,
				"rid":           body.Rid,
			})
			return
		}
	}
	now := time.Now()
	joinReq := &entity.RoomJoinRequest{
		Rid:          body.Rid,
		ApplicantUid: user.Uid,
		Message:      message,
		Answer:       answer,
		State:        0,
		ExpiresAt:    now.AddDate(0, 0, 7).UnixMilli(),
	}
	if err := query.CreateOrUpdateRoomJoinRequest(joinReq); err != nil {
		response.ServerError(c, "提交加入申请失败")
		return
	}
	if err := notifyRoomJoinRequest(joinReq, user, room); err != nil {
		log.Errorf("发送加入申请通知失败: %v", err)
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
		Uid: user.Uid, OpType: entity.UserOpRoomJoinApply, Sid: helper.GetSid(c), RelatedId: joinReq.RjrId,
		BeforeData: nil, AfterData: map[string]any{"rid": body.Rid, "message": message},
	}, 0)
	response.Success(c, joinReq)
}

func notifyRoomJoinRequest(joinReq *entity.RoomJoinRequest, applicant *types.User, room *types.Room) error {
	adminContent, _ := json.Marshal(map[string]any{
		"rid":                    room.Rid,
		"room_name":              room.Name,
		"applicant_uid":          applicant.Uid,
		"applicant_name":         applicant.Nickname,
		"message":                joinReq.Message,
		"answer":                 joinReq.Answer,
		"join_question":          room.JoinQuestion,
		"join_question_enabled":  room.JoinQuestionEnabled,
	})
	applicantContent, _ := json.Marshal(map[string]any{
		"rid":                   room.Rid,
		"room_name":             room.Name,
		"message":               joinReq.Message,
		"answer":                joinReq.Answer,
		"join_question":         room.JoinQuestion,
		"join_question_enabled": room.JoinQuestionEnabled,
	})
	adminUids, err := query.GetRoomAdminAndOwnerUids(room.Rid, applicant.Uid)
	if err != nil {
		return err
	}
	for _, adminUid := range adminUids {
		if err := upsertAndPushJoinRequestNotification(adminUid, joinReq.RjrId, entity.NotificationTypeRoomJoinRequest, string(adminContent)); err != nil {
			log.Errorf("通知管理员加入申请失败 uid=%s: %v", adminUid, err)
		}
	}
	return upsertAndPushJoinRequestNotification(applicant.Uid, joinReq.RjrId, entity.NotificationTypeRoomJoinRequestSend, string(applicantContent))
}

func upsertAndPushJoinRequestNotification(uid, relatedId string, notificationType entity.NotificationType, content string) error {
	notif, err := query.UpsertPendingMessageNotification(uid, relatedId, notificationType, content)
	if err != nil {
		return err
	}
	return helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: notif.Nid})
}

// pushRoomJoinRequestHandledNotifications 申请被同意/拒绝后，推送申请人与管理员的最新通知状态
func pushRoomJoinRequestHandledNotifications(rjrId, applicantUid string) {
	if applicantUid != "" {
		applicantNotifs, err := query.GetMessageNotificationsByRelatedIdAndType(
			rjrId, entity.NotificationTypeRoomJoinRequestSend,
		)
		if err != nil {
			log.Errorf("查询申请人加入申请通知失败 rjr_id=%s uid=%s: %v", rjrId, applicantUid, err)
		} else {
			for _, notif := range applicantNotifs {
				if notif == nil || notif.Uid != applicantUid {
					continue
				}
				if err := helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: notif.Nid}); err != nil {
					log.Errorf("推送申请人加入申请通知失败 nid=%s err=%v", notif.Nid, err)
				}
			}
		}
	}
	adminNotifs, err := query.GetMessageNotificationsByRelatedIdAndType(rjrId, entity.NotificationTypeRoomJoinRequest)
	if err != nil {
		log.Errorf("查询管理员加入申请通知失败 rjr_id=%s: %v", rjrId, err)
		return
	}
	for _, notif := range adminNotifs {
		if notif == nil {
			continue
		}
		if err := helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: notif.Nid}); err != nil {
			log.Errorf("推送管理员加入申请通知失败 nid=%s err=%v", notif.Nid, err)
		}
	}
}

// RoomJoinRequestAccept 同意加入申请（房主/管理员）
func RoomJoinRequestAccept(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomJoinRequestHandleBody
	if err := c.ShouldBindJSON(&body); err != nil || body.RjrId == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	joinReq, err := query.GetRoomJoinRequestByRjrId(body.RjrId)
	if err != nil || joinReq == nil {
		response.BadRequest(c, "加入申请不存在")
		return
	}
	if joinReq.State != 0 {
		response.BadRequest(c, "该申请已处理")
		return
	}
	if joinReq.ExpiresAt > 0 && joinReq.ExpiresAt < time.Now().UnixMilli() {
		response.BadRequest(c, "该申请已过期")
		return
	}
	if !query.CanUserMuteInRoom(joinReq.Rid, user.Uid) {
		response.BadRequest(c, "仅管理员或房主可处理加入申请")
		return
	}
	if query.HasRoomUser(joinReq.Rid, joinReq.ApplicantUid) {
		response.BadRequest(c, "该用户已在群内")
		return
	}
	room, err := query.GetRoomByRid(joinReq.Rid)
	if room == nil || err != nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	applicant, err := query.GetUserByUid(joinReq.ApplicantUid)
	if err != nil || applicant == nil {
		response.BadRequest(c, "申请人不存在")
		return
	}

	tx := db.GetDB().Begin()
	if err = query.UpdateRoomJoinRequestStateWithTx(tx, body.RjrId, user.Uid, 2); err != nil {
		tx.Rollback()
		response.ServerError(c, "处理加入申请失败")
		return
	}
	if err = query.UpdateMessageNotificationStateWithTx(tx, joinReq.ApplicantUid, body.RjrId, entity.NotificationTypeRoomJoinRequestSend, entity.NotificationRoomStateAccepted); err != nil {
		tx.Rollback()
		response.ServerError(c, "处理加入申请失败")
		return
	}
	if err = tx.Commit().Error; err != nil {
		response.ServerError(c, "处理加入申请失败")
		return
	}
	_ = query.UpdateAllMessageNotificationStatesByRelatedIdAndType(body.RjrId, entity.NotificationTypeRoomJoinRequest, entity.NotificationRoomStateAccepted)

	// 须先完成加群再推送通知：客户端收到「已通过」后会拉会话/禁言等成员接口，过早推送会返回「您不在该房间」
	rsid, err := joinRoomCommittedAfterValidation(c, applicant, room, "")
	if err != nil {
		log.Errorf("同意加入申请后执行加入失败: %v", err)
		response.ServerError(c, "加入房间失败")
		return
	}
	pushRoomJoinRequestHandledNotifications(body.RjrId, joinReq.ApplicantUid)
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: joinReq.Rid, OpType: entity.RoomAdminOpJoinRequestAccept, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: body.RjrId,
		BeforeData: map[string]any{"state": 0}, AfterData: map[string]any{"state": 2, "applicant_uid": joinReq.ApplicantUid},
	}, 0)
	_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
		Uid: user.Uid, OpType: entity.UserOpRoomJoinRequestAccept, Sid: helper.GetSid(c), RelatedId: body.RjrId,
		BeforeData: nil, AfterData: map[string]any{"rid": joinReq.Rid, "applicant_uid": joinReq.ApplicantUid},
	}, 0)
	response.Success(c, gin.H{"rsid": rsid, "rid": joinReq.Rid, "state": 2})
}

// RoomJoinRequestReject 拒绝加入申请（房主/管理员）
func RoomJoinRequestReject(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomJoinRequestHandleBody
	if err := c.ShouldBindJSON(&body); err != nil || body.RjrId == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	joinReq, err := query.GetRoomJoinRequestByRjrId(body.RjrId)
	if err != nil || joinReq == nil {
		response.BadRequest(c, "加入申请不存在")
		return
	}
	if joinReq.State != 0 {
		response.BadRequest(c, "该申请已处理")
		return
	}
	if !query.CanUserMuteInRoom(joinReq.Rid, user.Uid) {
		response.BadRequest(c, "仅管理员或房主可处理加入申请")
		return
	}
	tx := db.GetDB().Begin()
	if err = query.UpdateRoomJoinRequestStateWithTx(tx, body.RjrId, user.Uid, 1); err != nil {
		tx.Rollback()
		response.ServerError(c, "处理加入申请失败")
		return
	}
	if err = query.UpdateMessageNotificationStateWithTx(tx, joinReq.ApplicantUid, body.RjrId, entity.NotificationTypeRoomJoinRequestSend, entity.NotificationRoomStateRejected); err != nil {
		tx.Rollback()
		response.ServerError(c, "处理加入申请失败")
		return
	}
	if err = tx.Commit().Error; err != nil {
		response.ServerError(c, "处理加入申请失败")
		return
	}
	_ = query.UpdateAllMessageNotificationStatesByRelatedIdAndType(body.RjrId, entity.NotificationTypeRoomJoinRequest, entity.NotificationRoomStateRejected)

	pushRoomJoinRequestHandledNotifications(body.RjrId, joinReq.ApplicantUid)

	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: joinReq.Rid, OpType: entity.RoomAdminOpJoinRequestReject, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: body.RjrId,
		BeforeData: map[string]any{"state": 0}, AfterData: map[string]any{"state": 1, "applicant_uid": joinReq.ApplicantUid},
	}, 0)
	_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
		Uid: user.Uid, OpType: entity.UserOpRoomJoinRequestReject, Sid: helper.GetSid(c), RelatedId: body.RjrId,
		BeforeData: nil, AfterData: map[string]any{"rid": joinReq.Rid, "applicant_uid": joinReq.ApplicantUid},
	}, 0)
	response.Success(c, gin.H{"rjr_id": body.RjrId, "state": 1})
}

// respondJoinApprovalRequired 返回需审批响应（密码已通过）
func respondJoinApprovalRequired(c *gin.Context, room *types.Room) {
	response.Json(c, http.StatusConflict, gin.H{
		"message":                "加入该群聊需要管理员审批",
		"code":                   "JOIN_APPROVAL_REQUIRED",
		"join_approval_required": true,
		"join_question_enabled":  room.JoinQuestionEnabled,
		"join_question":          room.JoinQuestion,
	})
}
