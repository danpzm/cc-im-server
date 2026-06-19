package user

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/queue"
	"github.com/xd/quic-server/notify"
	"github.com/xd/quic-server/utils"
)

// 允许的展示状态键（与前端选择器一致）
var allowedPresenceKeys = map[string]struct{}{
	"online": {}, "q_me": {}, "away": {}, "busy": {}, "dnd": {}, "invisible": {},
	"battery": {}, "music": {}, "good": {}, "outing": {}, "travel": {},
	"exhausted": {}, "steps": {}, "weather": {}, "crush": {}, "custom": {},
}

// profileUpdateBody 用户个人信息修改请求体（均为可选，只更新传入且合法的字段）
type profileUpdateBody struct {
	Nickname     *string `json:"nickname,omitempty"`
	AvatarUfId   *string `json:"avatar_uf_id,omitempty"`
	Signature    *string `json:"signature,omitempty"`
	Introduction *string `json:"introduction,omitempty"`
	Password     *string `json:"password,omitempty"` // 新密码（明文，接口内会加密后落库，操作记录不落明文）
	// 在线展示状态（需已建立 QUIC 长连）；current_status 为预设键，custom_state 为附加文案（如自定义状态）
	CurrentStatus *string `json:"current_status,omitempty"`
	CustomState   *string `json:"custom_state,omitempty"`
}

// ProfileUpdate 修改当前用户头像、昵称、签名、简介、密码等；每项修改都会写入用户操作记录表
func ProfileUpdate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body profileUpdateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	// 从 DB 拉取最新用户信息，避免并发下用缓存的 user 做 before 对比
	var current entity.User
	if err := db.GetDB().Model(&entity.User{}).Where("uid = ?", user.Uid).First(&current).Error; err != nil {
		response.ServerError(c, "获取用户信息失败")
		return
	}
	sid := helper.GetSid(c)

	// 昵称
	if body.Nickname != nil {
		newVal := *body.Nickname
		if utf8.RuneCountInString(newVal) > 32 {
			response.BadRequest(c, "昵称不能超过32个字符")
			return
		}
		if newVal != current.Nickname {
			if err := db.GetDB().Model(&entity.User{}).Where("uid = ?", user.Uid).Update("nickname", newVal).Error; err != nil {
				response.ServerError(c, "修改昵称失败")
				return
			}
			_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
				Uid: user.Uid, OpType: entity.UserOpNickname, Sid: sid, RelatedId: "nickname",
				BeforeData: map[string]any{"nickname": current.Nickname}, AfterData: map[string]any{"nickname": newVal},
			}, 0)
		}
	}
	// 头像（存 avatar_uf_id，展示时用 /file/url?uf_id=xxx）
	if body.AvatarUfId != nil {
		newVal := *body.AvatarUfId
		if newVal != current.AvatarUfId {
			if err := db.GetDB().Model(&entity.User{}).Where("uid = ?", user.Uid).Update("avatar_uf_id", newVal).Error; err != nil {
				response.ServerError(c, "修改头像失败")
				return
			}
			_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
				Uid: user.Uid, OpType: entity.UserOpAvatar, Sid: sid, RelatedId: "avatar_uf_id",
				BeforeData: map[string]any{"avatar_uf_id": current.AvatarUfId}, AfterData: map[string]any{"avatar_uf_id": newVal},
			}, 0)
		}
	}
	// 个性签名
	if body.Signature != nil {
		newVal := *body.Signature
		if utf8.RuneCountInString(newVal) > 128 {
			response.BadRequest(c, "个性签名不能超过128个字符")
			return
		}
		if newVal != current.Signature {
			if err := db.GetDB().Model(&entity.User{}).Where("uid = ?", user.Uid).Update("signature", newVal).Error; err != nil {
				response.ServerError(c, "修改个性签名失败")
				return
			}
			_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
				Uid: user.Uid, OpType: entity.UserOpSignature, Sid: sid, RelatedId: "signature",
				BeforeData: map[string]any{"signature": current.Signature}, AfterData: map[string]any{"signature": newVal},
			}, 0)
		}
	}
	// 个人简介
	if body.Introduction != nil {
		newVal := *body.Introduction
		if utf8.RuneCountInString(newVal) > 512 {
			response.BadRequest(c, "个人简介不能超过512个字符")
			return
		}
		if newVal != current.Introduction {
			if err := db.GetDB().Model(&entity.User{}).Where("uid = ?", user.Uid).Update("introduction", newVal).Error; err != nil {
				response.ServerError(c, "修改个人简介失败")
				return
			}
			_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
				Uid: user.Uid, OpType: entity.UserOpIntroduction, Sid: sid, RelatedId: "introduction",
				BeforeData: map[string]any{"introduction": current.Introduction}, AfterData: map[string]any{"introduction": newVal},
			}, 0)
		}
	}
	// 密码（不记录明文，仅记录“已修改”）
	if body.Password != nil {
		newVal := *body.Password
		if utf8.RuneCountInString(newVal) < 6 {
			response.BadRequest(c, "密码不能少于6个字符")
			return
		}
		if utf8.RuneCountInString(newVal) > 32 {
			response.BadRequest(c, "密码不能超过32个字符")
			return
		}
		hashed, err := utils.PasswordHash(newVal)
		if err != nil {
			response.ServerError(c, "密码加密失败")
			return
		}
		if err := db.GetDB().Model(&entity.User{}).Where("uid = ?", user.Uid).Update("password", hashed).Error; err != nil {
			response.ServerError(c, "修改密码失败")
			return
		}
		_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
			Uid: user.Uid, OpType: entity.UserOpPassword, Sid: sid, RelatedId: "password",
			BeforeData: map[string]any{"changed": false}, AfterData: map[string]any{"changed": true},
		}, 0)
	}

	// 在线展示状态
	if body.CurrentStatus != nil || body.CustomState != nil {
		stBefore, err := query.GetOrCreateUserCurrentStatus(user.Uid)
		if err != nil {
			response.ServerError(c, "获取用户在线状态失败")
			return
		}
		beforeCS := stBefore.CurrentStatus
		beforeCust := stBefore.CustomState
		newCS := beforeCS
		if body.CurrentStatus != nil {
			v := strings.TrimSpace(*body.CurrentStatus)
			if v != "" {
				newCS = v
			}
		}
		if newCS == "" {
			newCS = "online"
		}
		if _, ok := allowedPresenceKeys[newCS]; !ok {
			response.BadRequest(c, "无效的展示状态")
			return
		}
		newCust := beforeCust
		if body.CustomState != nil {
			newCust = strings.TrimSpace(*body.CustomState)
		}
		if newCS != "custom" {
			newCust = ""
		}
		if err := query.UpdateUserPresenceDisplay(user.Uid, newCS, newCust); err != nil {
			if errors.Is(err, query.ErrPresenceRequiresOnline) {
				response.BadRequest(c, err.Error())
				return
			}
			response.ServerError(c, "更新展示状态失败")
			return
		}
		_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
			Uid: user.Uid, OpType: entity.UserOpPresenceStatus, Sid: sid, RelatedId: "presence",
			BeforeData: map[string]any{"current_status": beforeCS, "custom_state": beforeCust},
			AfterData:  map[string]any{"current_status": newCS, "custom_state": newCust},
		}, 0)
		if err := helper.NotifyQuic(notify.MessageTypeUserStatusSyncNotify, notify.UserStatusSyncNotifyPayload{Uid: user.Uid}); err != nil {
			log.Warnf("发送用户状态同步通知失败: uid=%s err=%v", user.Uid, err)
		}
	}

	// 若有资料类字段变更，懒推送给同房间用户（客户端按 uid 去重）
	if body.Nickname != nil || body.AvatarUfId != nil || body.Signature != nil || body.Introduction != nil {
		users, err := query.GetRoomUserInfoByUidList([]string{user.Uid}, "")
		if err == nil && len(users) > 0 {
			u := users[0]
			payload := notify.UserProfileNotifyPayload{
				User: notify.UserProfileNotifyUser{
					Uid: u.Uid, Username: u.Username, Nickname: u.Nickname,
					Signature: u.Signature, Introduction: u.Introduction,
					Email: u.Email, AvatarUfId: u.AvatarUfId, CreateTime: u.CreateTime,
				},
			}
			if err := helper.NotifyQuic(notify.MessageTypeUserProfileNotify, payload); err != nil {
				log.Warnf("发送用户资料变更通知失败: uid=%s err=%v", user.Uid, err)
			}
		}
	}

	response.Success(c, "修改成功")
}

// UserAvatarHistory 获取当前用户头像历史（操作记录中的头像 uf_id 列表）
func UserAvatarHistory(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	list, err := query.ListUserAvatarHistory(user.Uid, 20)
	if err != nil {
		response.ServerError(c, "查询失败")
		return
	}
	response.Success(c, gin.H{"list": list})
}
