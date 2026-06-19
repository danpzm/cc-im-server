package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/rs/xid"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/notify"
	"github.com/xd/quic-server/pkg/roommsg"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/queue"
	"github.com/xd/quic-server/redis"
	"github.com/xd/quic-server/utils"
	"gorm.io/gorm"
)

// roomCreateBody 创建房间请求体；AvatarUfId 为上传后的 uf_id（scene=room_avatar）
type roomCreateBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	AvatarUfId  string `json:"avatar_uf_id"`
	Password    string `json:"password"`
}

func RoomCreate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	body, err := utils.BodyToObj[roomCreateBody](c.Request.Body)
	if err != nil {
		response.BadRequest(c, "参数解析失败")
		return
	}
	if body.Name == "" {
		response.BadRequest(c, "请输入房间名称")
		return
	}
	if utf8.RuneCountInString(body.Name) > 36 {
		response.BadRequest(c, "房间名称不能超过36个字符")
		return
	}
	if body.Description == "" {
		body.Description = body.Name
	}
	if utf8.RuneCountInString(body.Description) > 255 {
		response.BadRequest(c, "房间描述不能超过255个字符")
		return
	}
	if body.Password != "" && utf8.RuneCountInString(body.Password) > 20 {
		response.BadRequest(c, "房间密码不能超过20个字符")
		return
	}
	hashedPassword := ""
	if body.Password != "" {
		var errHash error
		hashedPassword, errHash = utils.PasswordHash(body.Password)
		if errHash != nil {
			response.ServerError(c, "密码加密失败")
			return
		}
	}
	room := &types.Room{
		Password:    hashedPassword,
		AvatarUfId:  body.AvatarUfId,
		Name:        body.Name,
		CreateUid:   user.Uid,
		Type:        1,
		Description: body.Description,
	}
	err = db.GetDB().Model(&types.Room{}).Create(room).Error
	if err != nil {
		response.ServerError(c, "创建房间失败")
		return
	}
	// 创建者自动加入该房间，并添加一条「创建房间」的系统消息
	seqId, err := query.GetRoomSeqId(room.Rid)
	if err != nil {
		response.ServerError(c, "获取房间序列号失败")
		return
	}
	tx := db.GetDB().Begin()
	if err = tx.Create(&entity.RoomMuteConfig{Rid: room.Rid}).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "创建房间失败")
		return
	}
	roomUser := &types.RoomUser{
		Rid:          room.Rid,
		Uid:          user.Uid,
		Role:         entity.RoomUserRoleOwner,
		RoomNickname: user.Nickname,
	}
	if err = tx.Create(roomUser).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "加入房间失败")
		return
	}
	if err = tx.Model(&types.Room{}).Where("rid = ?", room.Rid).Update("member_count", gorm.Expr("member_count + 1")).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "加入房间失败")
		return
	}
	rm := &types.RoomMessage{
		Rid:       room.Rid,
		ClientMid: xid.New().String(),
		SenderUid: "system",
		SeqId:     seqId,
		IP:        c.ClientIP(),
	}
	if err = tx.Create(rm).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "加入房间失败")
		return
	}
	rc := &types.RoomMessageContent{
		Type:             types.RoomMessageContentTypeRoomCreate,
		TypeId:           user.Uid,
		ClientCid:        xid.New().String(),
		Mid:              rm.Mid,
		Content:          json.RawMessage(`{}`),
		ClientCreateTime: 0,
	}
	if err = tx.Create(rc).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "创建房间消息失败")
		return
	}
	if err = tx.Model(&types.UserRoomSession{}).Where("uid = ?", user.Uid).Update("is_top", false).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "加入房间失败")
		return
	}
	session := &types.UserRoomSession{
		Uid:       user.Uid,
		Rid:       room.Rid,
		IsTop:     false,
		LastSeqId: query.InitialLastSeqIdOnRoomJoin(seqId),
	}
	if err = tx.Create(session).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "加入房间失败")
		return
	}
	if tx.Commit(); tx.Error != nil {
		response.ServerError(c, "加入房间失败")
		return
	}
	if err := query.BumpUserRoomSessionLastMessageTime(room.Rid, rm.CreateTime); err != nil {
		log.Errorf("更新会话最后消息时间失败 rid=%s: %v", room.Rid, err)
	}
	query.SetRoomUserIdsCache(room.Rid)
	if err := helper.NotifyQuic(notify.MessageTypeRoomMessageNotify, notify.RoomMessageNotifyPayload{Mid: rm.Mid}); err != nil {
		log.Errorf("发送创建房间通知失败: mid=%s err=%v", rm.Mid, err)
	} else {
		log.Infof("已发送创建房间通知: mid=%s", rm.Mid)
	}
	if err := query.AddUserToRoomOnlineSetIfOnline(room.Rid, user.Uid); err != nil {
		log.Errorf("创建房间后更新在线集合失败 rid=%s uid=%s: %v", room.Rid, user.Uid, err)
	}
	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpRoomCreate, room.Rid, nil, map[string]any{
		"rid": room.Rid, "name": room.Name,
	})
	response.Success(c, room)
}

// RoomListResponse 房间搜索分页结果。
type RoomListResponse struct {
	List    []*types.Room `json:"list"`
	HasMore bool          `json:"has_more"`
}

func RoomList(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
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

	rooms, err := query.GetRoomList(keyword, limit+1, offset)
	if err != nil {
		response.ServerError(c, err.Error())
		return
	}

	hasMore := len(rooms) > limit
	if hasMore {
		rooms = rooms[:limit]
	}
	for _, r := range rooms {
		r.HasPassword = r.Password != ""
	}
	response.Success(c, RoomListResponse{
		List:    rooms,
		HasMore: hasMore,
	})
}

// roomDetailResp 房间详情响应（含分类名、标签列表、当前用户是否已加入，供前端卡片展示）
type roomDetailResp struct {
	*types.Room
	CategoryName       string `json:"category_name"`
	Tags               []string `json:"tags"`
	IsMember           bool   `json:"is_member"`
	JoinQuestionAnswer string `json:"join_question_answer,omitempty"` // 仅管理员可见
}

func RoomDetail(c *gin.Context) {
	if helper.GetUser(c) == nil {
		return
	}
	rid := c.Query("rid")
	if rid == "" {
		response.BadRequest(c, "缺少房间id")
		return
	}
	room, err := query.GetRoomByRid(rid)
	if err != nil || room == nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	room.HasPassword = room.Password != ""
	categoryName, _ := query.GetRoomCategoryName(room.CategoryId)
	tags, _ := query.GetRoomTagNamesByRid(rid)
	isMember := query.HasRoomUser(rid, helper.GetUser(c).Uid)
	resp := &roomDetailResp{Room: room, CategoryName: categoryName, Tags: tags, IsMember: isMember}
	if query.CanUserMuteInRoom(rid, helper.GetUser(c).Uid) {
		resp.JoinQuestionAnswer = strings.TrimSpace(room.JoinQuestionAnswer)
	}
	response.Success(c, resp)
}
func RoomJoin(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	log.Info("处理加入房间")
	body, err := utils.BodyToMap(c.Request.Body)
	if err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	rid, exists := body["rid"].(string)
	if !exists || rid == "" {
		response.BadRequest(c, "请输入房间id")
		return
	}
	password, _ := body["password"].(string)
	room, err := query.GetRoomByRid(rid)
	if room == nil || err != nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	if room.Password != "" {
		if err := utils.PasswordCompare(room.Password, password); err != nil {
			response.BadRequest(c, "请输入正确的房间密码")
			return
		}
	}
	if room.Type == types.PrivateRoom {
		response.BadRequest(c, "无法加入该房间")
		return
	}
	if query.HasRoomUser(rid, user.Uid) {
		response.BadRequest(c, "已加入该房间")
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
	rsid, err := joinRoomCommittedAfterValidation(c, user, room, "")
	if err != nil {
		log.Errorf("加入房间事务失败: %v", err)
		response.ServerError(c, "加入房间失败")
		return
	}
	response.Success(c, rsid)
}

// joinRoomCommittedAfterValidation 在已通过房间类型与成员关系等校验后执行加入事务与通知（RoomJoin / RoomJoinInvite 共用）。
// inviteInviterUid 非空时表示经邀请加入：写入 user:invite:join 系统消息（邀请人 + 被邀请人），否则为普通 user:join。
func joinRoomCommittedAfterValidation(c *gin.Context, user *types.User, room *types.Room, inviteInviterUid string) (rsid string, err error) {
	rid := room.Rid
	nowMs := time.Now().UnixMilli()
	log.Info("获取房间序列号")
	seqId, err := query.GetRoomSeqId(room.Rid)
	if err != nil {
		return "", err
	}
	tx := db.GetDB().Begin()
	if err = query.UpsertActiveRoomUserTx(tx, rid, user.Uid, user.Nickname, nowMs); err != nil {
		tx.Rollback()
		return "", err
	}
	if err = tx.Model(&types.Room{}).Where("rid = ?", rid).Update("member_count", gorm.Expr("member_count + 1")).Error; err != nil {
		tx.Rollback()
		return "", err
	}
	rm := &types.RoomMessage{
		Rid:       room.Rid,
		ClientMid: xid.New().String(),
		SenderUid: "system",
		SeqId:     seqId,
		IP:        c.ClientIP(),
	}
	if err = tx.Create(rm).Error; err != nil {
		tx.Rollback()
		return "", err
	}
	inviter := strings.TrimSpace(inviteInviterUid)
	contentType := types.RoomMessageContentTypeUserJoin
	typeID := user.Uid
	contentBytes := json.RawMessage(`{}`)
	if inviter != "" {
		contentType = types.RoomMessageContentTypeUserInvite
		typeID = user.Uid
		var errMarshal error
		contentBytes, errMarshal = json.Marshal(map[string]string{
			"inviter_uid": inviter,
			"join_uid":    user.Uid,
		})
		if errMarshal != nil {
			tx.Rollback()
			return "", errMarshal
		}
	}
	rc := &types.RoomMessageContent{
		Type:      contentType,
		TypeId:    typeID,
		ClientCid: xid.New().String(),
		Mid:       rm.Mid,
		Content:   contentBytes,
	}
	if err = tx.Create(rc).Error; err != nil {
		tx.Rollback()
		return "", err
	}
	if err = tx.Model(&types.UserRoomSession{}).Where("uid = ?", user.Uid).Update("is_top", false).Error; err != nil {
		tx.Rollback()
		return "", err
	}
	rsid, err = query.UpsertActiveUserRoomSessionTx(tx, user.Uid, room.Rid, query.InitialLastSeqIdOnRoomJoin(seqId), nowMs)
	if err != nil {
		tx.Rollback()
		return "", err
	}
	if err = tx.Commit().Error; err != nil {
		return "", err
	}
	if err := query.BumpUserRoomSessionLastMessageTime(room.Rid, rm.CreateTime); err != nil {
		log.Errorf("更新会话最后消息时间失败 rid=%s: %v", room.Rid, err)
	}
	// 须在广播前刷新成员缓存，否则刚加入/重新加入的用户不在 fanout 列表内
	query.SetRoomUserIdsCache(room.Rid)
	if err := helper.NotifyQuic(notify.MessageTypeRoomMessageNotify, notify.RoomMessageNotifyPayload{Mid: rm.Mid}); err != nil {
		log.Errorf("发送房间加入通知失败: mid=%s err=%v", rm.Mid, err)
	} else {
		log.Infof("已发送房间加入通知: mid=%s", rm.Mid)
	}
	if err := query.AddUserToRoomOnlineSetIfOnline(room.Rid, user.Uid); err != nil {
		log.Errorf("加入房间后更新在线集合失败 rid=%s uid=%s: %v", room.Rid, user.Uid, err)
	}
	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpRoomJoin, rid, nil, map[string]any{
		"rid": rid, "room_name": room.Name,
	})
	return rsid, nil
}

func RoomUserIds(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	rid := c.Query("rid")
	if rid == "" {
		response.BadRequest(c, "请输入房间id")
		return
	}
	if rejectIfRoomDissolved(c, rid) {
		return
	}
	userIds, err := query.GetRoomUserIdsCache(rid)
	if err != nil {
		response.ServerError(c, err.Error())
		return
	}
	response.Success(c, userIds)
}

func RoomNameUpdate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	request, err := utils.BodyToObj[struct {
		Rid  string `json:"rid"`
		Name string `json:"name"`
	}](c.Request.Body)
	if err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, request.Rid) {
		return
	}
	room, err := query.GetRoomByRid(request.Rid)
	if room == nil || err != nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	if room.CreateUid != user.Uid {
		response.BadRequest(c, "无权修改")
		return
	}
	if request.Name == "" {
		response.BadRequest(c, "房间名称不能为空")
		return
	}
	if utf8.RuneCountInString(request.Name) > 36 {
		response.BadRequest(c, "房间名称不能超过36个字符")
		return
	}
	if request.Name == room.Name {
		response.BadRequest(c, "房间名称不能与原名称相同")
		return
	}
	log.Infof("修改房间名称: rid=%s name=%s", request.Rid, request.Name)
	tx := db.GetDB().Begin()
	err = tx.Model(&types.Room{}).Where("rid = ?", request.Rid).Update("name", request.Name).Error
	if err != nil {
		tx.Rollback()
		response.ServerError(c, "修改失败")
		return
	}
	seqId, err := query.GetRoomSeqId(room.Rid)
	if err != nil {
		tx.Rollback()
		response.ServerError(c, "修改失败")
		return
	}
	contentJSON, _ := json.Marshal(map[string]string{"name": request.Name})
	rm := &types.RoomMessage{
		Rid:       room.Rid,
		ClientMid: xid.New().String(),
		SenderUid: "system",
		SeqId:     seqId,
		IP:        c.ClientIP(),
	}
	if err = tx.Create(rm).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "修改失败")
		return
	}
	rc := &types.RoomMessageContent{
		Type:      types.RoomMessageContentTypeUserUpdateRoomName,
		TypeId:    user.Uid,
		ClientCid: xid.New().String(),
		Mid:       rm.Mid,
		Content:   contentJSON,
	}
	if err = tx.Create(rc).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "修改失败")
		return
	}
	if tx.Commit(); tx.Error != nil {
		response.ServerError(c, "修改失败")
		return
	}
	if err := query.BumpUserRoomSessionLastMessageTime(request.Rid, rm.CreateTime); err != nil {
		log.Errorf("更新会话最后消息时间失败 rid=%s: %v", request.Rid, err)
	}
	if err := helper.NotifyQuic(notify.MessageTypeRoomMessageNotify, notify.RoomMessageNotifyPayload{Mid: rm.Mid}); err != nil {
		log.Errorf("发送房间名称更新通知失败: mid=%s err=%v", rm.Mid, err)
	} else {
		log.Infof("已发送房间名称更新通知: mid=%s", rm.Mid)
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: request.Rid, OpType: entity.RoomAdminOpRoomNameUpdate, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: request.Rid,
		BeforeData: map[string]any{"name": room.Name}, AfterData: map[string]any{"name": request.Name},
	}, 0)
	response.Success(c, "修改成功")
}

// roomPasswordUpdateBody 修改房间密码请求体（仅房主/管理员）
type roomPasswordUpdateBody struct {
	Rid      string `json:"rid"`
	Password string `json:"password"` // 为空表示清除密码
}

// RoomPasswordUpdate 修改房间密码（房主 + 管理员）
func RoomPasswordUpdate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomPasswordUpdateBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	room, err := query.GetRoomByRid(body.Rid)
	if room == nil || err != nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	// 仅房主或管理员可修改（沿用禁言权限判断逻辑）
	if !query.CanUserMuteInRoom(body.Rid, user.Uid) {
		response.BadRequest(c, "仅管理员或房主可修改房间密码")
		return
	}
	// 校验新密码长度（可为空表示清除）
	if body.Password != "" && utf8.RuneCountInString(body.Password) > 20 {
		response.BadRequest(c, "房间密码不能超过20个字符")
		return
	}
	oldHasPassword := room.Password != ""
	hashedPassword := ""
	if body.Password != "" {
		var errHash error
		hashedPassword, errHash = utils.PasswordHash(body.Password)
		if errHash != nil {
			response.ServerError(c, "密码加密失败")
			return
		}
	}
	if err := db.GetDB().Model(&types.Room{}).Where("rid = ?", body.Rid).Update("password", hashedPassword).Error; err != nil {
		response.ServerError(c, "修改失败")
		return
	}
	beforeData := map[string]any{"has_password": oldHasPassword}
	afterData := map[string]any{"has_password": hashedPassword != ""}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpRoomPasswordUpdate, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: body.Rid,
		BeforeData: beforeData, AfterData: afterData,
	}, 0)
	response.Success(c, "修改成功")
}

// roomAvatarUpdateBody 修改房间头像请求体；AvatarUfId 为上传后的 uf_id（scene=room_avatar）
type roomAvatarUpdateBody struct {
	Rid        string `json:"rid"`
	AvatarUfId string `json:"avatar_uf_id"`
}

// RoomAvatarUpdate 修改房间头像（仅房主）
func RoomAvatarUpdate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomAvatarUpdateBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	room, err := query.GetRoomByRid(body.Rid)
	if room == nil || err != nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	if room.CreateUid != user.Uid {
		response.BadRequest(c, "仅房主可修改房间头像")
		return
	}
	if body.AvatarUfId == "" {
		response.BadRequest(c, "请上传房间头像")
		return
	}
	oldAvatarUfId := room.AvatarUfId
	if err := db.GetDB().Model(&types.Room{}).Where("rid = ?", body.Rid).Update("avatar_uf_id", body.AvatarUfId).Error; err != nil {
		response.ServerError(c, "修改失败")
		return
	}
	if err := helper.NotifyQuic(notify.MessageTypeRoomAvatarNotify, notify.RoomAvatarNotifyPayload{Rid: body.Rid, AvatarUfId: body.AvatarUfId}); err != nil {
		log.Errorf("发送房间头像更新通知失败: rid=%s err=%v", body.Rid, err)
	} else {
		log.Infof("已发送房间头像更新通知: rid=%s", body.Rid)
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpRoomAvatarUpdate, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: body.Rid,
		BeforeData: map[string]any{"avatar_uf_id": oldAvatarUfId}, AfterData: map[string]any{"avatar_uf_id": body.AvatarUfId},
	}, 0)
	response.Success(c, "修改成功")
}

// RoomAvatarHistory 获取房间头像历史（操作记录中的头像 uf_id 列表，仅房间成员可读）
func RoomAvatarHistory(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	rid := c.Query("rid")
	if rid == "" {
		response.BadRequest(c, "请输入房间id")
		return
	}
	if rejectIfRoomDissolved(c, rid) {
		return
	}
	if !query.HasRoomUser(rid, user.Uid) {
		response.BadRequest(c, "您不在该房间")
		return
	}
	list, err := query.ListRoomAvatarHistory(rid, 20)
	if err != nil {
		response.ServerError(c, "查询失败")
		return
	}
	response.Success(c, gin.H{"list": list})
}

// RoomAnnouncementList 获取房间公告列表（仅房间成员可读）
func RoomAnnouncementList(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	rid := c.Query("rid")
	if rid == "" {
		response.BadRequest(c, "请输入房间id")
		return
	}
	if rejectIfRoomDissolved(c, rid) {
		return
	}
	if !query.HasRoomUser(rid, user.Uid) {
		response.BadRequest(c, "您不在该房间")
		return
	}
	list, err := query.ListRoomAnnouncements(rid)
	if err != nil {
		response.ServerError(c, err.Error())
		return
	}
	response.Success(c, gin.H{"list": list})
}

// RoomAnnouncementGet 获取单条公告（仅房间成员可读）
func RoomAnnouncementGet(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	rid := c.Query("rid")
	idStr := c.Query("id")
	if rid == "" || idStr == "" {
		response.BadRequest(c, "缺少房间id或公告id")
		return
	}
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id <= 0 {
		response.BadRequest(c, "公告id不合法")
		return
	}
	if rejectIfRoomDissolved(c, rid) {
		return
	}
	if !query.HasRoomUser(rid, user.Uid) {
		response.BadRequest(c, "您不在该房间")
		return
	}
	ann, err := query.GetRoomAnnouncementByID(id, rid)
	if err != nil {
		response.ServerError(c, err.Error())
		return
	}
	if ann == nil {
		response.BadRequest(c, "公告不存在或已删除")
		return
	}
	response.Success(c, ann)
}

// roomAnnouncementCreateBody 创建公告请求体
type roomAnnouncementCreateBody struct {
	Rid     string          `json:"rid"`
	Content json.RawMessage `json:"content"`
	Pinned  bool            `json:"pinned"`
}

// RoomAnnouncementCreate 创建公告（仅房主或管理员）
func RoomAnnouncementCreate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomAnnouncementCreateBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.CanUserMuteInRoom(body.Rid, user.Uid) {
		response.BadRequest(c, "仅房主或管理员可添加公告")
		return
	}
	if len(body.Content) == 0 || !json.Valid(body.Content) {
		response.BadRequest(c, "公告内容必须为合法JSON")
		return
	}
	if len(body.Content) > 100000 {
		response.BadRequest(c, "公告内容过长")
		return
	}
	ann, err := query.CreateRoomAnnouncement(body.Rid, body.Content, user.Uid, body.Pinned)
	if err != nil {
		response.ServerError(c, "创建失败")
		return
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpRoomAnnouncementCreate, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: fmt.Sprintf("%d", ann.Id),
		BeforeData: nil, AfterData: map[string]any{"id": ann.Id, "content_length": len(ann.Content), "pinned": ann.Pinned},
	}, 0)
	createRoomSystemMessageAndNotify(
		c,
		body.Rid,
		entity.RoomMessageContentType("room:announcement:create"),
		user.Uid,
		map[string]any{
			"announcement_id": ann.Id,
			"operator_uid":    user.Uid,
			"pinned":          ann.Pinned,
		},
		nil,
		nil,
	)
	response.Success(c, ann)
}

// roomAnnouncementUpdateBody 更新房间公告请求体
type roomAnnouncementUpdateBody struct {
	Rid     string          `json:"rid"`
	Id      int64           `json:"id"`      // 公告 id
	Content json.RawMessage `json:"content"` // 公告 JSON 内容
	Pinned  *bool           `json:"pinned"`  // 是否置顶，不传则不修改
}

// RoomAnnouncementUpdate 更新房间公告（仅房主或管理员）
func RoomAnnouncementUpdate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomAnnouncementUpdateBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" || body.Id <= 0 {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.CanUserMuteInRoom(body.Rid, user.Uid) {
		response.BadRequest(c, "仅房主或管理员可修改公告")
		return
	}
	if len(body.Content) == 0 || !json.Valid(body.Content) {
		response.BadRequest(c, "公告内容必须为合法JSON")
		return
	}
	if len(body.Content) > 100000 {
		response.BadRequest(c, "公告内容过长")
		return
	}
	prev, _ := query.GetRoomAnnouncementByID(body.Id, body.Rid)
	if prev == nil {
		response.BadRequest(c, "公告不存在或已删除")
		return
	}
	ann, err := query.UpdateRoomAnnouncement(body.Id, body.Rid, body.Content, user.Uid, body.Pinned)
	if err != nil {
		response.ServerError(c, "更新失败")
		return
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpRoomAnnouncementUpdate, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: fmt.Sprintf("%d", body.Id),
		BeforeData: map[string]any{"content_length": len(prev.Content), "pinned": prev.Pinned}, AfterData: map[string]any{"content_length": len(ann.Content), "pinned": ann.Pinned},
	}, 0)
	createRoomSystemMessageAndNotify(
		c,
		body.Rid,
		entity.RoomMessageContentType("room:announcement:update"),
		user.Uid,
		map[string]any{
			"announcement_id": ann.Id,
			"operator_uid":    user.Uid,
			"pinned":          ann.Pinned,
		},
		nil,
		nil,
	)
	response.Success(c, ann)
}

// roomAnnouncementDeleteBody 删除公告请求体（软删除）
type roomAnnouncementDeleteBody struct {
	Rid string `json:"rid"`
	Id  int64  `json:"id"`
}

// RoomAnnouncementDelete 软删除公告（仅房主或管理员），并记操作日志
func RoomAnnouncementDelete(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomAnnouncementDeleteBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" || body.Id <= 0 {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.CanUserMuteInRoom(body.Rid, user.Uid) {
		response.BadRequest(c, "仅房主或管理员可删除公告")
		return
	}
	prev, _ := query.GetRoomAnnouncementByID(body.Id, body.Rid)
	if prev == nil {
		response.BadRequest(c, "公告不存在或已删除")
		return
	}
	_, err := query.SoftDeleteRoomAnnouncement(body.Id, body.Rid)
	if err != nil {
		response.ServerError(c, "删除失败")
		return
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpRoomAnnouncementDelete, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: fmt.Sprintf("%d", body.Id),
		BeforeData: map[string]any{"content_length": len(prev.Content), "pinned": prev.Pinned}, AfterData: map[string]any{"deleted": true},
	}, 0)
	createRoomSystemMessageAndNotify(
		c,
		body.Rid,
		entity.RoomMessageContentType("room:announcement:delete"),
		user.Uid,
		map[string]any{
			"announcement_id": body.Id,
			"operator_uid":    user.Uid,
			"deleted":         true,
		},
		nil,
		nil,
	)
	response.Success(c, nil)
}

// roomUserRoleUpdateBody 房主授权/取消管理员请求体
type roomUserRoleUpdateBody struct {
	Rid       string `json:"rid"`
	TargetUid string `json:"target_uid"`
	Role      int8   `json:"role"` // 0-普通成员 1-管理员（仅房主可设置，不可设置为房主）
}

// RoomUserRoleUpdate 房主授权管理员或取消管理员（仅房主可操作）
func RoomUserRoleUpdate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomUserRoleUpdateBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" || body.TargetUid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	room, err := query.GetRoomByRid(body.Rid)
	if room == nil || err != nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	if room.CreateUid != user.Uid {
		response.BadRequest(c, "仅房主可设置成员角色")
		return
	}
	if body.Role != 0 && body.Role != 1 {
		response.BadRequest(c, "角色只能设为普通成员(0)或管理员(1)")
		return
	}
	if !query.HasRoomUser(body.Rid, body.TargetUid) {
		response.BadRequest(c, "该用户不在房间内")
		return
	}
	ru, err := query.GetRoomUser(body.Rid, body.TargetUid)
	if err != nil || ru == nil {
		response.ServerError(c, "获取成员信息失败")
		return
	}
	oldRole := int8(ru.Role)
	if oldRole == body.Role {
		response.BadRequest(c, "角色未变更")
		return
	}
	newRole := entity.RoomUserRole(body.Role)
	if _, err := query.UpdateRoomUserRole(body.Rid, body.TargetUid, newRole); err != nil {
		response.ServerError(c, "设置失败")
		return
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpMemberRoleUpdate, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: body.TargetUid,
		BeforeData: map[string]any{"role": oldRole}, AfterData: map[string]any{"role": body.Role},
	}, 0)
	// 插入一条系统房间消息并广播，通知所有成员角色变更
	createRoomSystemMessageAndNotify(
		c,
		body.Rid,
		types.RoomMessageContentTypeRoomMemberRoleUpdate,
		user.Uid,
		map[string]any{
			"target_uid":   body.TargetUid,
			"operator_uid": user.Uid,
			"old_role":     oldRole,
			"new_role":     body.Role,
		},
		nil,
		nil,
	)
	response.Success(c, "设置成功")
}

type roomOwnerTransferBody struct {
	Rid       string `json:"rid"`
	TargetUid string `json:"target_uid"`
}

// RoomOwnerTransfer 房主将群主转让给指定成员
func RoomOwnerTransfer(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomOwnerTransferBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" || body.TargetUid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	room, err := query.GetRoomByRid(body.Rid)
	if room == nil || err != nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	if room.Type != entity.RoomTypeGroup {
		response.BadRequest(c, "仅群聊支持转让群主")
		return
	}
	if body.TargetUid == user.Uid {
		response.BadRequest(c, "不能转让给自己")
		return
	}
	if !query.HasRoomUser(body.Rid, body.TargetUid) {
		response.BadRequest(c, "该用户不在房间内")
		return
	}
	oldOwnerUid, err := query.TransferRoomOwner(body.Rid, user.Uid, body.TargetUid)
	if err != nil {
		if errors.Is(err, query.ErrNotRoomOwner) {
			response.BadRequest(c, "仅群主可转让群主")
			return
		}
		if errors.Is(err, query.ErrInvalidTransferTarget) {
			response.BadRequest(c, "转让目标无效")
			return
		}
		response.ServerError(c, "转让失败")
		return
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpOwnerTransfer, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: body.TargetUid,
		BeforeData: map[string]any{"owner_uid": oldOwnerUid}, AfterData: map[string]any{"owner_uid": body.TargetUid},
	}, 0)
	createRoomSystemMessageAndNotify(
		c,
		body.Rid,
		types.RoomMessageContentTypeRoomOwnerTransfer,
		body.TargetUid,
		map[string]any{
			"from_uid":     oldOwnerUid,
			"to_uid":       body.TargetUid,
			"operator_uid": user.Uid,
		},
		nil,
		nil,
	)
	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpRoomOwnerTransfer, body.Rid, map[string]any{
		"owner_uid": oldOwnerUid,
	}, map[string]any{
		"owner_uid": body.TargetUid,
	})
	response.Success(c, gin.H{"rid": body.Rid, "owner_uid": body.TargetUid})
}

type roomDissolveBody struct {
	Rid string `json:"rid"`
}

// RoomDissolve 群主解散群聊：保留成员会话与历史，房间不可搜索且不可发消息
func RoomDissolve(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomDissolveBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "缺少房间 rid")
		return
	}
	rid := strings.TrimSpace(body.Rid)
	room, err := query.GetRoomByRid(rid)
	if err != nil || room == nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	if room.Type != entity.RoomTypeGroup {
		response.BadRequest(c, "仅群聊支持解散")
		return
	}
	ru, err := query.GetRoomUser(rid, user.Uid)
	if err != nil || ru == nil {
		response.ServerError(c, "获取成员信息失败")
		return
	}
	if ru.Role != entity.RoomUserRoleOwner {
		response.BadRequest(c, "仅群主可解散群聊")
		return
	}
	if err := query.DissolveRoom(rid); err != nil {
		if errors.Is(err, query.ErrRoomAlreadyDissolved) {
			response.BadRequest(c, "该房间已解散")
			return
		}
		response.ServerError(c, "解散失败")
		return
	}
	memberUids, err := query.GetRoomUserIdsCache(rid)
	if err != nil {
		log.Errorf("获取房间成员列表失败 rid=%s: %v", rid, err)
	} else {
		notifyContent, _ := json.Marshal(map[string]any{
			"event":        "room_dissolved",
			"room_rid":     rid,
			"room_name":    room.Name,
			"operator_uid": user.Uid,
		})
		for _, targetUid := range memberUids {
			notif := &entity.UserMessageNotification{
				Uid:       targetUid,
				Type:      entity.NotificationTypeRoomNotification,
				RelatedId: rid,
				Content:   string(notifyContent),
			}
			if err := query.CreateMessageNotification(notif); err != nil {
				log.Errorf("创建房间解散通知失败 uid=%s rid=%s: %v", targetUid, rid, err)
				continue
			}
			if err := helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: notif.Nid}); err != nil {
				log.Errorf("推送房间解散通知失败 nid=%s err=%v", notif.Nid, err)
			}
		}
		if err := helper.NotifyQuic(notify.MessageTypeRoomDissolvedNotify, notify.RoomDissolvedNotifyPayload{
			Rid:       rid,
			RoomState: entity.RoomStateDissolved,
		}); err != nil {
			log.Errorf("推送房间解散状态失败 rid=%s: %v", rid, err)
		}
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: rid, OpType: entity.RoomAdminOpRoomDissolve, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: rid,
		BeforeData: map[string]any{"state": entity.RoomStateActive}, AfterData: map[string]any{"state": entity.RoomStateDissolved},
	}, 0)
	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpRoomDissolve, rid, map[string]any{
		"state": entity.RoomStateActive, "room_name": room.Name,
	}, map[string]any{
		"state": entity.RoomStateDissolved,
	})
	response.Success(c, gin.H{"rid": rid, "room_state": entity.RoomStateDissolved})
}

// RoomUserList 房间成员列表（含角色、禁言截止时间），用于会话详情成员与禁言管理
func RoomUserList(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	rid := c.Query("rid")
	if rid == "" {
		response.BadRequest(c, "请输入房间id")
		return
	}
	// 已解散房间仍允许成员拉取列表（只读），供历史记录筛选与昵称/头像缓存
	if !query.HasRoomUser(rid, user.Uid) {
		response.BadRequest(c, "您不在该房间")
		return
	}
	list, err := query.GetRoomUsers(rid)
	if err != nil {
		response.ServerError(c, err.Error())
		return
	}
	response.Success(c, list)
}

// roomUserNicknameUpdateBody 更新本群昵称
type roomUserNicknameUpdateBody struct {
	Rid          string `json:"rid"`
	RoomNickname string `json:"room_nickname"`
}

// RoomUserNicknameUpdate 更新当前用户在该房间的本群昵称
func RoomUserNicknameUpdate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomUserNicknameUpdateBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.HasRoomUser(body.Rid, user.Uid) {
		response.BadRequest(c, "您不在该房间")
		return
	}
	if utf8.RuneCountInString(body.RoomNickname) > 36 {
		response.BadRequest(c, "本群昵称不能超过36个字符")
		return
	}
	beforeRu, _ := query.GetRoomUser(body.Rid, user.Uid)
	beforeNickname := ""
	if beforeRu != nil {
		beforeNickname = beforeRu.RoomNickname
	}
	ru, err := query.UpdateRoomUserNickname(body.Rid, user.Uid, body.RoomNickname)
	if err != nil {
		response.ServerError(c, "更新失败")
		return
	}
	// 不插入房间消息，仅广播给房间内用户静默更新 UI/缓存
	_ = helper.NotifyQuic(notify.MessageTypeRoomUserRoomNicknameNotify, notify.RoomUserRoomNicknameNotifyPayload{
		Rid:          body.Rid,
		Uid:          user.Uid,
		RoomNickname: body.RoomNickname,
	})
	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpRoomUserNickname, body.Rid, map[string]any{
		"rid": body.Rid, "room_nickname": beforeNickname,
	}, map[string]any{
		"rid": body.Rid, "room_nickname": body.RoomNickname,
	})
	response.Success(c, ru)
}

// roomUserRemarkUpdateBody 更新房间备注
type roomUserRemarkUpdateBody struct {
	Rid        string `json:"rid"`
	RoomRemark string `json:"room_remark"`
}

// RoomUserRemarkUpdate 更新当前用户在该房间的备注
func RoomUserRemarkUpdate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomUserRemarkUpdateBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.HasRoomUser(body.Rid, user.Uid) {
		response.BadRequest(c, "您不在该房间")
		return
	}
	if utf8.RuneCountInString(body.RoomRemark) > 255 {
		response.BadRequest(c, "房间备注不能超过255个字符")
		return
	}
	beforeRu, _ := query.GetRoomUser(body.Rid, user.Uid)
	beforeRemark := ""
	if beforeRu != nil {
		beforeRemark = beforeRu.RoomRemark
	}
	ru, err := query.UpdateRoomUserRemark(body.Rid, user.Uid, body.RoomRemark)
	if err != nil {
		response.ServerError(c, "更新失败")
		return
	}
	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpRoomUserRemark, body.Rid, map[string]any{
		"rid": body.Rid, "room_remark": beforeRemark,
	}, map[string]any{
		"rid": body.Rid, "room_remark": body.RoomRemark,
	})
	response.Success(c, ru)
}

// RoomMuteConfig 获取房间禁言配置
func RoomMuteConfig(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	rid := c.Query("rid")
	if rid == "" {
		response.BadRequest(c, "请输入房间id")
		return
	}
	if rejectIfRoomDissolved(c, rid) {
		return
	}
	if !query.HasRoomUser(rid, user.Uid) {
		response.BadRequest(c, "您不在该房间")
		return
	}
	cfg, err := query.GetRoomMuteConfig(rid)
	if err != nil {
		response.ServerError(c, err.Error())
		return
	}
	response.Success(c, cfg)
}

// roomMuteBody 禁言请求体
type roomMuteBody struct {
	Rid         string `json:"rid"`
	TargetUid   string `json:"target_uid"`
	DurationSec int64  `json:"duration_sec"` // 禁言时长（秒），如 3600 表示 1 小时
	Reason      string `json:"reason"`
}

type roomMuteConfigUpdateBody struct {
	Rid         string          `json:"rid"`
	IsMuteAll   bool            `json:"is_mute_all"`
	Reason      string          `json:"reason"`
	RuleType    int8            `json:"rule_type"`
	RuleConfig  json.RawMessage `json:"rule_config"`
	AllowRoles  []int8          `json:"allow_roles"`
	ExceptUsers []string        `json:"except_users"`
	EffectiveAt int64           `json:"effective_at"`
	ExpiresAt   int64           `json:"expires_at"`
	IsActive    bool            `json:"is_active"`
}

// RoomMuteConfigUpdate 更新房间禁言配置（全量配置）
func RoomMuteConfigUpdate(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomMuteConfigUpdateBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.CanUserMuteInRoom(body.Rid, user.Uid) {
		response.BadRequest(c, "仅管理员或房主可操作禁言配置")
		return
	}
	prevCfg, _ := query.GetRoomMuteConfig(body.Rid)
	if body.EffectiveAt > 0 && body.ExpiresAt > 0 && body.EffectiveAt > body.ExpiresAt {
		response.BadRequest(c, "生效时间不能晚于过期时间")
		return
	}
	roleSet := make(map[int8]struct{}, len(body.AllowRoles))
	validRole := map[int8]struct{}{
		int8(entity.RoomUserRoleNormal): {},
		int8(entity.RoomUserRoleAdmin):  {},
		int8(entity.RoomUserRoleOwner):  {},
	}
	allowRoles := make([]int8, 0, len(body.AllowRoles))
	for _, role := range body.AllowRoles {
		if _, ok := validRole[role]; !ok {
			response.BadRequest(c, "allow_roles 含非法角色")
			return
		}
		if _, exists := roleSet[role]; exists {
			continue
		}
		roleSet[role] = struct{}{}
		allowRoles = append(allowRoles, role)
	}
	exceptUsers := make([]string, 0, len(body.ExceptUsers))
	exceptSet := make(map[string]struct{}, len(body.ExceptUsers))
	for _, uid := range body.ExceptUsers {
		if uid == "" {
			continue
		}
		if _, exists := exceptSet[uid]; exists {
			continue
		}
		if !query.HasRoomUser(body.Rid, uid) {
			response.BadRequest(c, "except_users 存在不在房间内的用户")
			return
		}
		exceptSet[uid] = struct{}{}
		exceptUsers = append(exceptUsers, uid)
	}
	ruleConfig := "{}"
	if len(body.RuleConfig) > 0 {
		if !json.Valid(body.RuleConfig) {
			response.BadRequest(c, "rule_config 不是合法 JSON")
			return
		}
		ruleConfig = string(body.RuleConfig)
	}
	// 互斥规则：全体禁言 与 策略禁言不能同时开启
	if body.IsMuteAll {
		body.IsActive = false
	}
	if body.IsActive {
		body.IsMuteAll = false
	}
	if err := query.SetRoomMuteConfigRule(body.Rid, user.Uid, query.RoomMuteConfigRuleParams{
		IsMuteAll:   body.IsMuteAll,
		Reason:      body.Reason,
		RuleType:    body.RuleType,
		RuleConfig:  ruleConfig,
		AllowRoles:  allowRoles,
		ExceptUsers: exceptUsers,
		EffectiveAt: body.EffectiveAt,
		ExpiresAt:   body.ExpiresAt,
		IsActive:    body.IsActive,
	}); err != nil {
		response.ServerError(c, "更新禁言配置失败")
		return
	}
	cfg, err := query.GetRoomMuteConfig(body.Rid)
	if err != nil {
		response.ServerError(c, "获取禁言配置失败")
		return
	}
	if prevCfg != nil && prevCfg.IsMuteAll != cfg.IsMuteAll {
		contentType := types.RoomMessageContentTypeRoomMuteAllOff
		if cfg.IsMuteAll {
			contentType = types.RoomMessageContentTypeRoomMuteAllOn
		}
		createRoomSystemMessageAndNotify(
			c,
			body.Rid,
			contentType,
			user.Uid,
			map[string]any{"by": user.Uid},
			nil,
			nil,
		)
		opType := entity.RoomAdminOpRoomMuteAllOff
		if cfg.IsMuteAll {
			opType = entity.RoomAdminOpRoomMuteAllOn
		}
		_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
			Rid: body.Rid, OpType: opType, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: body.Rid,
			BeforeData: map[string]any{"is_mute_all": prevCfg.IsMuteAll}, AfterData: map[string]any{"is_mute_all": cfg.IsMuteAll},
		}, 0)
	}
	prevStrategy := prevCfg != nil && prevCfg.IsActive && !prevCfg.IsMuteAll
	nextStrategy := cfg.IsActive && !cfg.IsMuteAll
	strategyConfigChanged := prevCfg == nil ||
		prevCfg.RuleType != cfg.RuleType ||
		prevCfg.RuleConfig != cfg.RuleConfig ||
		prevCfg.AllowRoles != cfg.AllowRoles ||
		prevCfg.ExceptUsers != cfg.ExceptUsers ||
		prevCfg.EffectiveAt != cfg.EffectiveAt ||
		prevCfg.ExpiresAt != cfg.ExpiresAt
	if prevStrategy != nextStrategy || (nextStrategy && strategyConfigChanged) {
		var strategyType entity.RoomMessageContentType
		var opType entity.RoomAdminOperationType
		if !prevStrategy && nextStrategy {
			strategyType = types.RoomMessageContentTypeRoomMuteStrategyEnable
			opType = entity.RoomAdminOpRoomMuteStrategyEnable
		} else if prevStrategy && !nextStrategy {
			strategyType = types.RoomMessageContentTypeRoomMuteStrategyDisable
			opType = entity.RoomAdminOpRoomMuteStrategyDisable
		} else {
			strategyType = types.RoomMessageContentTypeRoomMuteStrategyUpdate
			opType = entity.RoomAdminOpRoomMuteStrategyUpdate
		}
		createRoomSystemMessageAndNotify(
			c,
			body.Rid,
			strategyType,
			user.Uid,
			map[string]any{
				"by":           user.Uid,
				"rule_type":    cfg.RuleType,
				"rule_config":  cfg.RuleConfig,
				"allow_roles":  cfg.AllowRoles,
				"except_users": cfg.ExceptUsers,
				"effective_at": cfg.EffectiveAt,
				"expires_at":   cfg.ExpiresAt,
				"is_active":    cfg.IsActive,
				"is_mute_all":  cfg.IsMuteAll,
			},
			nil,
			nil,
		)
		beforeStrategy := map[string]any{}
		if prevCfg != nil {
			beforeStrategy = map[string]any{"rule_type": prevCfg.RuleType, "rule_config": prevCfg.RuleConfig, "allow_roles": prevCfg.AllowRoles, "except_users": prevCfg.ExceptUsers, "effective_at": prevCfg.EffectiveAt, "expires_at": prevCfg.ExpiresAt, "is_active": prevCfg.IsActive}
		}
		afterStrategy := map[string]any{"rule_type": cfg.RuleType, "rule_config": cfg.RuleConfig, "allow_roles": cfg.AllowRoles, "except_users": cfg.ExceptUsers, "effective_at": cfg.EffectiveAt, "expires_at": cfg.ExpiresAt, "is_active": cfg.IsActive}
		_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
			Rid: body.Rid, OpType: opType, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: cfg.ConfigId,
			BeforeData: beforeStrategy, AfterData: afterStrategy,
		}, 0)
	}
	// 策略时间段：到生效时间发「禁言开始」，到过期时间发「禁言结束」（与频率无关，仅前端定时器做频率到时可发）
	scheduleStrategyMuteTime(body.Rid, cfg.EffectiveAt, cfg.ExpiresAt, cfg.RuleType)
	response.Success(c, cfg)
}

// createRoomSystemMessageAndNotify 创建系统消息并通知 QUIC 广播。
//
// 推送策略约定（后续新增系统消息请按此选择）：
//  1. 默认全员广播（include/exclude 都为空）：
//     适用于房间公共事件，如 room:create、user:join、room:mute:all、user:update:room:name。
//  2. 指定用户发送（includeUids 非空）：
//     适用于私有可见事件，如“好友验证仅双方可见”等。
//  3. 排除用户发送（excludeUids 非空）：
//     适用于“通知房间其余成员，但不回显给操作者”等场景。
//  4. include/exclude 同时传入时，以 include 优先。
func createRoomSystemMessageAndNotify(c *gin.Context, rid string, contentType entity.RoomMessageContentType, typeID string, content any, includeUids []string, excludeUids []string) {
	room, _ := query.GetRoomByRid(rid)
	if room == nil {
		return
	}
	seqId, errSeq := query.GetRoomSeqId(room.Rid)
	if errSeq != nil {
		return
	}
	contentJSON, _ := json.Marshal(content)
	rm := &types.RoomMessage{
		Rid:       room.Rid,
		ClientMid: xid.New().String(),
		SenderUid: "system",
		SeqId:     seqId,
		IP:        c.ClientIP(),
	}
	if errCreate := db.GetDB().Create(rm).Error; errCreate != nil {
		return
	}
	rc := &types.RoomMessageContent{
		Type:      contentType,
		TypeId:    typeID,
		ClientCid: xid.New().String(),
		Mid:       rm.Mid,
		Content:   contentJSON,
	}
	if errCreate := db.GetDB().Create(rc).Error; errCreate != nil {
		return
	}
	if err := query.BumpUserRoomSessionLastMessageTime(rid, rm.CreateTime); err != nil {
		log.Errorf("更新会话最后消息时间失败 rid=%s: %v", rid, err)
	}
	switch {
	case len(includeUids) > 0:
		_ = helper.NotifyQuic(notify.MessageTypeRoomMessageNotifyIncludeUids, notify.RoomMessageNotifyIncludeUidsPayload{
			Mid:     rm.Mid,
			UidList: includeUids,
		})
	case len(excludeUids) > 0:
		_ = helper.NotifyQuic(notify.MessageTypeRoomMessageNotifyExcludeUids, notify.RoomMessageNotifyExcludeUidsPayload{
			Mid:            rm.Mid,
			ExcludeUidList: excludeUids,
		})
	default:
		_ = helper.NotifyQuic(notify.MessageTypeRoomMessageNotify, notify.RoomMessageNotifyPayload{Mid: rm.Mid})
	}
}

func RoomMute(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomMuteBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" || body.TargetUid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.CanUserMuteInRoom(body.Rid, user.Uid) {
		response.BadRequest(c, "仅管理员或房主可操作禁言")
		return
	}
	if body.DurationSec <= 0 {
		body.DurationSec = 3600 // 默认 1 小时
	}
	ruBefore, _ := query.GetRoomUser(body.Rid, body.TargetUid)
	beforeMuteUntil := int64(0)
	if ruBefore != nil {
		beforeMuteUntil = ruBefore.MuteUntil
	}
	if err := query.MuteUserInRoom(body.Rid, body.TargetUid, user.Uid, body.DurationSec, body.Reason); err != nil {
		response.ServerError(c, "禁言失败")
		return
	}
	ru, err := query.GetRoomUser(body.Rid, body.TargetUid)
	if err != nil || ru == nil {
		response.ServerError(c, "禁言失败")
		return
	}
	createRoomSystemMessageAndNotify(
		c,
		body.Rid,
		types.RoomMessageContentTypeRoomMuteUser,
		user.Uid,
		map[string]any{
			"target_uid":   body.TargetUid,
			"operator_uid": user.Uid,
			"mute_until":   ru.MuteUntil,
			"duration_sec": body.DurationSec,
			"reason":       body.Reason,
		},
		nil,
		nil,
	)
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpRoomMute, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: body.TargetUid,
		BeforeData: map[string]any{"target_uid": body.TargetUid, "mute_until": beforeMuteUntil}, AfterData: map[string]any{"target_uid": body.TargetUid, "mute_until": ru.MuteUntil},
	}, 0)
	response.Success(c, map[string]any{
		"target_uid": body.TargetUid,
		"mute_until": ru.MuteUntil,
	})
}

// roomUnmuteBody 解除禁言请求体
type roomUnmuteBody struct {
	Rid       string `json:"rid"`
	TargetUid string `json:"target_uid"`
	Reason    string `json:"reason"`
}

func RoomUnmute(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomUnmuteBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" || body.TargetUid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.CanUserMuteInRoom(body.Rid, user.Uid) {
		response.BadRequest(c, "仅管理员或房主可操作禁言")
		return
	}
	ru, _ := query.GetRoomUser(body.Rid, body.TargetUid)
	beforeMuteUntil := int64(0)
	if ru != nil {
		beforeMuteUntil = ru.MuteUntil
	}
	if err := query.UnmuteUserInRoom(body.Rid, body.TargetUid, user.Uid, body.Reason); err != nil {
		response.ServerError(c, "解除禁言失败")
		return
	}
	createRoomSystemMessageAndNotify(
		c,
		body.Rid,
		types.RoomMessageContentTypeRoomUnmuteUser,
		user.Uid,
		map[string]any{
			"target_uid":   body.TargetUid,
			"operator_uid": user.Uid,
			"mute_until":   int64(0),
			"reason":       body.Reason,
		},
		nil,
		nil,
	)
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpRoomUnmute, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: body.TargetUid,
		BeforeData: map[string]any{"target_uid": body.TargetUid, "mute_until": beforeMuteUntil}, AfterData: map[string]any{"target_uid": body.TargetUid, "mute_until": 0},
	}, 0)
	response.Success(c, map[string]any{
		"target_uid": body.TargetUid,
		"mute_until": 0,
	})
}

// roomMuteAllBody 全体禁言请求体
type roomMuteAllBody struct {
	Rid         string `json:"rid"`
	Mute        bool   `json:"mute"`         // true 开启全体禁言，false 关闭
	DurationSec int64  `json:"duration_sec"` // 兼容字段：全体禁言模式下忽略
	Reason      string `json:"reason"`
}

func RoomMuteAll(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomMuteAllBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.CanUserMuteInRoom(body.Rid, user.Uid) {
		response.BadRequest(c, "仅管理员或房主可操作全体禁言")
		return
	}
	prevCfg, _ := query.GetRoomMuteConfig(body.Rid)
	prevIsMuteAll := false
	if prevCfg != nil {
		prevIsMuteAll = prevCfg.IsMuteAll
	}
	if err := query.SetRoomMuteAll(body.Rid, body.Mute, user.Uid, body.Reason); err != nil {
		response.ServerError(c, "操作失败")
		return
	}
	opType := entity.RoomAdminOpRoomMuteAllOff
	if body.Mute {
		opType = entity.RoomAdminOpRoomMuteAllOn
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: opType, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: body.Rid,
		BeforeData: map[string]any{"is_mute_all": prevIsMuteAll}, AfterData: map[string]any{"is_mute_all": body.Mute, "reason": body.Reason},
	}, 0)
	// 插入一条全体禁言系统消息并通知房间内所有人
	room, _ := query.GetRoomByRid(body.Rid)
	if room != nil {
		seqId, errSeq := query.GetRoomSeqId(room.Rid)
		if errSeq == nil {
			contentType := types.RoomMessageContentTypeRoomMuteAllOff
			if body.Mute {
				contentType = types.RoomMessageContentTypeRoomMuteAllOn
			}
			contentJSON, _ := json.Marshal(map[string]any{"by": user.Uid})
			rm := &types.RoomMessage{
				Rid:       room.Rid,
				ClientMid: xid.New().String(),
				SenderUid: "system",
				SeqId:     seqId,
				IP:        c.ClientIP(),
			}
			if errCreate := db.GetDB().Create(rm).Error; errCreate == nil {
				rc := &types.RoomMessageContent{
					Type:      contentType,
					TypeId:    user.Uid,
					ClientCid: xid.New().String(),
					Mid:       rm.Mid,
					Content:   contentJSON,
				}
				if db.GetDB().Create(rc).Error == nil {
					if err := query.BumpUserRoomSessionLastMessageTime(room.Rid, rm.CreateTime); err != nil {
						log.Errorf("更新会话最后消息时间失败 rid=%s: %v", room.Rid, err)
					}
					_ = helper.NotifyQuic(notify.MessageTypeRoomMessageNotify, notify.RoomMessageNotifyPayload{Mid: rm.Mid})
				}
			}
		}
	}
	response.Success(c, "操作成功")
}

// RoomMuteStatus 获取当前用户在该房间的禁言状态（用于发送按钮禁用）
func RoomMuteStatus(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	rid := c.Query("rid")
	if rid == "" {
		response.BadRequest(c, "请输入房间id")
		return
	}
	if rejectIfRoomDissolved(c, rid) {
		return
	}
	if !query.HasRoomUser(rid, user.Uid) {
		response.BadRequest(c, "您不在该房间")
		return
	}
	cfg, err := query.GetRoomMuteConfig(rid)
	if err != nil {
		response.ServerError(c, err.Error())
		return
	}
	ru, err := query.GetRoomUser(rid, user.Uid)
	if err != nil || ru == nil {
		response.ServerError(c, "获取成员信息失败")
		return
	}
	resp := map[string]any{
		"is_mute_all":           cfg.IsMuteAll,
		"my_mute_until":         ru.MuteUntil,
		"my_mute_operator_uid":  ru.MuteOperatorUid,
		"is_strategy_mute":      cfg.IsActive && !cfg.IsMuteAll,
	}
	isMuted, mutedReason := query.IsUserMutedInRoom(rid, user.Uid)
	resp["is_muted"] = isMuted
	resp["mute_reason"] = mutedReason
	nowMs := time.Now().UnixMilli()
	// 频率禁言仅用于 API 返回 mute_until，供前端 UI 定时器到时刷新、方便用户再次发送；不在后端做定时任务
	if isMuted && (cfg.RuleType&query.RuleTypeBitFrequency) != 0 && cfg.RuleConfig != "" {
		if muteUntilMs, ok := query.GetFrequencyMuteUntil(rid, user.Uid, nowMs, cfg.RuleConfig); ok && muteUntilMs > 0 {
			resp["mute_until"] = muteUntilMs
		}
	}
	response.Success(c, resp)
}

// roomBlockBody 屏蔽/取消屏蔽会话
type roomBlockBody struct {
	Rid string `json:"rid" binding:"required"`
}

// RoomBlock 屏蔽会话：当前用户无法再向该房间发送消息（仅私聊/群私聊）
func RoomBlock(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomBlockBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "缺少房间 rid")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	room, err := query.GetRoomByRid(body.Rid)
	if err != nil || room == nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	if room.Type != entity.RoomTypeGroupPrivate {
		response.BadRequest(c, "仅支持屏蔽群私聊会话，私聊为好友房间无需屏蔽")
		return
	}
	userIds, err := query.GetRoomUserIdsCache(body.Rid)
	if err != nil || len(userIds) == 0 {
		response.ServerError(c, "获取房间成员失败")
		return
	}
	var inRoom bool
	for _, uid := range userIds {
		if uid == user.Uid {
			inRoom = true
			break
		}
	}
	if !inRoom {
		response.BadRequest(c, "您不在该房间")
		return
	}
	if err := query.BlockRoom(user.Uid, body.Rid); err != nil {
		log.Errorf("RoomBlock 失败 uid=%s rid=%s: %v", user.Uid, body.Rid, err)
		response.ServerError(c, "屏蔽失败")
		return
	}
	sid := helper.GetSid(c)
	if sid != "" {
		_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
			Uid: user.Uid, OpType: entity.UserOpRoomBlock, Sid: sid, RelatedId: body.Rid,
			BeforeData: nil, AfterData: map[string]any{"rid": body.Rid},
		}, 0)
	}
	response.Success(c, nil)
}

// RoomUnblock 取消屏蔽会话
func RoomUnblock(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomBlockBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "缺少房间 rid")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if err := query.UnblockRoom(user.Uid, body.Rid); err != nil {
		log.Errorf("RoomUnblock 失败 uid=%s rid=%s: %v", user.Uid, body.Rid, err)
		response.ServerError(c, "取消屏蔽失败")
		return
	}
	sid := helper.GetSid(c)
	if sid != "" {
		_ = queue.PublishOpLogTaskDefault(queue.TaskUserOperationLog, queue.UserOperationLogPayload{
			Uid: user.Uid, OpType: entity.UserOpRoomUnblock, Sid: sid, RelatedId: body.Rid,
			BeforeData: nil, AfterData: map[string]any{"rid": body.Rid},
		}, 0)
	}
	response.Success(c, nil)
}

// roomBlockUserBody 房间内屏蔽/取消屏蔽某用户
type roomBlockUserBody struct {
	Rid       string `json:"rid" binding:"required"`
	TargetUid string `json:"target_uid" binding:"required"`
}

// RoomBlockUser 在房间内屏蔽某用户：当前用户不接收该用户在该房间的消息
func RoomBlockUser(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomBlockUserBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" || body.TargetUid == "" {
		response.BadRequest(c, "缺少 rid 或 target_uid")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if body.TargetUid == user.Uid {
		response.BadRequest(c, "不能屏蔽自己")
		return
	}
	roomUserIds, err := query.GetRoomUserIdsCache(body.Rid)
	if err != nil || len(roomUserIds) == 0 {
		response.BadRequest(c, "房间不存在或获取成员失败")
		return
	}
	var inRoom, targetInRoom bool
	for _, uid := range roomUserIds {
		if uid == user.Uid {
			inRoom = true
		}
		if uid == body.TargetUid {
			targetInRoom = true
		}
		if inRoom && targetInRoom {
			break
		}
	}
	if !inRoom {
		response.BadRequest(c, "您不在该房间")
		return
	}
	if !targetInRoom {
		response.BadRequest(c, "对方不在该房间")
		return
	}
	if err := query.BlockUserInRoom(user.Uid, body.Rid, body.TargetUid); err != nil {
		log.Errorf("RoomBlockUser 失败 uid=%s rid=%s target=%s: %v", user.Uid, body.Rid, body.TargetUid, err)
		response.ServerError(c, "屏蔽失败")
		return
	}
	sid := helper.GetSid(c)
	if sid != "" {
		_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
			Rid: body.Rid, OpType: entity.RoomAdminOpRoomBlockUser, OperatorUid: user.Uid, Sid: sid, RelatedId: body.TargetUid,
			BeforeData: nil, AfterData: map[string]any{"target_uid": body.TargetUid},
		}, 0)
	}
	response.Success(c, nil)
}

// RoomUnblockUser 在房间内取消屏蔽某用户
func RoomUnblockUser(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomBlockUserBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" || body.TargetUid == "" {
		response.BadRequest(c, "缺少 rid 或 target_uid")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if err := query.UnblockUserInRoom(user.Uid, body.Rid, body.TargetUid); err != nil {
		log.Errorf("RoomUnblockUser 失败 uid=%s rid=%s target=%s: %v", user.Uid, body.Rid, body.TargetUid, err)
		response.ServerError(c, "取消屏蔽失败")
		return
	}
	sid := helper.GetSid(c)
	if sid != "" {
		_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
			Rid: body.Rid, OpType: entity.RoomAdminOpRoomUnblockUser, OperatorUid: user.Uid, Sid: sid, RelatedId: body.TargetUid,
			BeforeData: nil, AfterData: map[string]any{"target_uid": body.TargetUid},
		}, 0)
	}
	response.Success(c, nil)
}

// roomMemberKickBody 移出群成员请求体
type roomMemberKickBody struct {
	Rid       string `json:"rid" binding:"required"`
	TargetUid string `json:"target_uid" binding:"required"`
}

// RoomMemberKick 管理员/房主将成员移出群聊：软删成员与会话，通知被移出用户并广播成员列表更新
func RoomMemberKick(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomMemberKickBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" || body.TargetUid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	rid := strings.TrimSpace(body.Rid)
	targetUid := strings.TrimSpace(body.TargetUid)
	if rejectIfRoomDissolved(c, rid) {
		return
	}
	room, err := query.GetRoomByRid(rid)
	if err != nil || room == nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	if room.Type != entity.RoomTypeGroup {
		response.BadRequest(c, "仅群聊支持移出成员")
		return
	}
	if ok, reason := query.CanKickMemberInRoom(rid, user.Uid, targetUid); !ok {
		response.BadRequest(c, reason)
		return
	}
	targetRu, err := query.GetRoomUser(rid, targetUid)
	if err != nil || targetRu == nil {
		response.BadRequest(c, "该用户不在房间内")
		return
	}
	beforeRole := int8(targetRu.Role)
	targetNickname := strings.TrimSpace(targetRu.RoomNickname)
	if targetNickname == "" {
		if targetUser, uErr := query.GetUserByUid(targetUid); uErr == nil && targetUser != nil {
			targetNickname = targetUser.Nickname
		}
	}

	nowMs := time.Now().UnixMilli()
	tx := db.GetDB().Begin()
	if err = tx.Model(&types.RoomUser{}).
		Where("rid = ? AND uid = ? AND delete_time = 0", rid, targetUid).
		Update("delete_time", nowMs).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "移出成员失败")
		return
	}
	if err = tx.Model(&types.Room{}).Where("rid = ?", rid).
		Update("member_count", gorm.Expr("GREATEST(member_count - 1, 0)")).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "移出成员失败")
		return
	}
	if err = tx.Model(&types.UserRoomSession{}).
		Where("uid = ? AND rid = ? AND delete_time = 0", targetUid, rid).
		Updates(map[string]any{"state": 0, "delete_time": nowMs}).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "移出成员失败")
		return
	}
	if err = tx.Commit().Error; err != nil {
		response.ServerError(c, "移出成员失败")
		return
	}

	query.SetRoomUserIdsCache(rid)
	_ = query.RemoveUserFromRoomOnlineSet(rid, targetUid)

	userIds, err := query.GetRoomUserIdsCache(rid)
	if err != nil {
		log.Errorf("获取房间成员列表失败 rid=%s: %v", rid, err)
	} else if len(userIds) > 0 {
		if err := helper.NotifyQuic(notify.MessageTypeRoomUserIdsNotify, notify.RoomUserIdsNotifyPayload{
			Rid:     rid,
			UserIds: userIds,
		}); err != nil {
			log.Errorf("推送房间成员列表更新失败 rid=%s: %v", rid, err)
		}
	}

	operatorNickname := strings.TrimSpace(user.Nickname)
	if operatorRu, oErr := query.GetRoomUser(rid, user.Uid); oErr == nil && operatorRu != nil {
		if nn := strings.TrimSpace(operatorRu.RoomNickname); nn != "" {
			operatorNickname = nn
		}
	}
	notifyContent, _ := json.Marshal(map[string]any{
		"event":              "member_kick",
		"operator_uid":       user.Uid,
		"operator_nickname":  operatorNickname,
		"target_uid":         targetUid,
		"target_nickname":    targetNickname,
		"room_rid":           rid,
		"room_name":          room.Name,
	})
	notif := &entity.UserMessageNotification{
		Uid:       targetUid,
		Type:      entity.NotificationTypeRoomNotification,
		RelatedId: rid,
		Content:   string(notifyContent),
	}
	if err := query.CreateMessageNotification(notif); err != nil {
		log.Errorf("创建移出群聊通知失败 uid=%s rid=%s: %v", targetUid, rid, err)
	} else if err := helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: notif.Nid}); err != nil {
		log.Errorf("推送移出群聊通知失败 nid=%s err=%v", notif.Nid, err)
	}

	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: rid, OpType: entity.RoomAdminOpMemberKick, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: targetUid,
		BeforeData: map[string]any{"target_uid": targetUid, "role": beforeRole},
		AfterData:  map[string]any{"target_uid": targetUid, "removed": true},
	}, 0)
	response.Success(c, gin.H{"rid": rid, "target_uid": targetUid})
}

// roomLeaveBody 退出房间请求体
type roomLeaveBody struct {
	Rid string `json:"rid" binding:"required"`
}

// RoomLeave 成员主动退出群聊：软删成员与会话，静默广播成员列表更新并通知房主与管理员
func RoomLeave(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomLeaveBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "缺少房间 rid")
		return
	}
	rid := strings.TrimSpace(body.Rid)
	room, err := query.GetRoomByRid(rid)
	if err != nil || room == nil {
		response.BadRequest(c, "房间不存在")
		return
	}
	if room.Type != entity.RoomTypeGroup {
		response.BadRequest(c, "仅群聊支持退出房间")
		return
	}
	if !query.HasRoomUser(rid, user.Uid) {
		response.BadRequest(c, "您不在该房间")
		return
	}
	ru, err := query.GetRoomUser(rid, user.Uid)
	if err != nil || ru == nil {
		response.ServerError(c, "获取成员信息失败")
		return
	}
	if ru.Role == entity.RoomUserRoleOwner {
		response.BadRequest(c, "房主无法退出房间，请先转让房主")
		return
	}

	nowMs := time.Now().UnixMilli()
	tx := db.GetDB().Begin()
	if err = tx.Model(&types.RoomUser{}).
		Where("rid = ? AND uid = ? AND delete_time = 0", rid, user.Uid).
		Update("delete_time", nowMs).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "退出房间失败")
		return
	}
	if err = tx.Model(&types.Room{}).Where("rid = ?", rid).
		Update("member_count", gorm.Expr("GREATEST(member_count - 1, 0)")).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "退出房间失败")
		return
	}
	if err = tx.Model(&types.UserRoomSession{}).
		Where("uid = ? AND rid = ? AND delete_time = 0", user.Uid, rid).
		Updates(map[string]any{"state": 0, "delete_time": nowMs}).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "退出房间失败")
		return
	}
	if err = tx.Commit().Error; err != nil {
		response.ServerError(c, "退出房间失败")
		return
	}

	query.SetRoomUserIdsCache(rid)
	_ = query.RemoveUserFromRoomOnlineSet(rid, user.Uid)

	userIds, err := query.GetRoomUserIdsCache(rid)
	if err != nil {
		log.Errorf("获取房间成员列表失败 rid=%s: %v", rid, err)
	} else if len(userIds) > 0 {
		if err := helper.NotifyQuic(notify.MessageTypeRoomUserIdsNotify, notify.RoomUserIdsNotifyPayload{
			Rid:     rid,
			UserIds: userIds,
		}); err != nil {
			log.Errorf("推送房间成员列表更新失败 rid=%s: %v", rid, err)
		}
	}

	leaveNickname := strings.TrimSpace(ru.RoomNickname)
	if leaveNickname == "" {
		leaveNickname = user.Nickname
	}
	notifyContent, _ := json.Marshal(map[string]any{
		"event":          "member_leave",
		"leave_uid":      user.Uid,
		"leave_nickname": leaveNickname,
		"room_rid":       rid,
		"room_name":      room.Name,
	})
	adminUids, err := query.GetRoomAdminAndOwnerUids(rid, user.Uid)
	if err != nil {
		log.Errorf("获取房间管理员列表失败 rid=%s: %v", rid, err)
	} else {
		for _, targetUid := range adminUids {
			notif := &entity.UserMessageNotification{
				Uid:       targetUid,
				Type:      entity.NotificationTypeRoomNotification,
				RelatedId: rid,
				Content:   string(notifyContent),
			}
			if err := query.CreateMessageNotification(notif); err != nil {
				log.Errorf("创建成员退出通知失败 uid=%s rid=%s: %v", targetUid, rid, err)
				continue
			}
			if err := helper.NotifyQuic(notify.MessageTypeNotificationNotify, notify.NotificationNotifyPayload{Nid: notif.Nid}); err != nil {
				log.Errorf("推送成员退出通知失败 nid=%s err=%v", notif.Nid, err)
			}
		}
	}

	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpRoomLeave, rid, nil, map[string]any{
		"rid": rid, "room_name": room.Name,
	})
	response.Success(c, gin.H{"rid": rid})
}

const (
	redisKeyRoomMuteStrategyScheduledPrefix = "room_mute_strategy_scheduled:"
)

// scheduleStrategyMuteTime 按策略生效时间、过期时间投递「禁言开始」「禁言结束」定时任务（与频率无关）
func scheduleStrategyMuteTime(rid string, effectiveAt, expiresAt int64, ruleType int8) {
	if (ruleType & query.RuleTypeBitTimeRange) == 0 {
		return
	}
	nowMs := time.Now().UnixMilli()
	if effectiveAt > 0 {
		if effectiveAt <= nowMs {
			roommsg.CreateSystemMessageAndNotify(rid, types.RoomMessageContentTypeRoomMuteStrategyStart, "system", map[string]any{"rid": rid})
		} else {
			key := redisKeyRoomMuteStrategyScheduledPrefix + rid + ":start:" + strconv.FormatInt(effectiveAt, 10)
			ttl := time.Duration(effectiveAt-nowMs)*time.Millisecond + 60*time.Second
			if ok, err := redis.SetNX(key, "1", ttl); err == nil && ok {
				if err := queue.PublishTaskAtDefault(queue.TaskRoomMuteStrategyTime, queue.RoomMuteStrategyTimePayload{Rid: rid, RunAtMs: effectiveAt, Kind: "start"}, time.UnixMilli(effectiveAt)); err != nil {
					log.Warnf("投递策略禁言开始任务失败: rid=%s err=%v", rid, err)
				}
			}
		}
	}
	if expiresAt > 0 && expiresAt > nowMs {
		key := redisKeyRoomMuteStrategyScheduledPrefix + rid + ":end:" + strconv.FormatInt(expiresAt, 10)
		ttl := time.Duration(expiresAt-nowMs)*time.Millisecond + 60*time.Second
		if ok, err := redis.SetNX(key, "1", ttl); err == nil && ok {
			if err := queue.PublishTaskAtDefault(queue.TaskRoomMuteStrategyTime, queue.RoomMuteStrategyTimePayload{Rid: rid, RunAtMs: expiresAt, Kind: "end"}, time.UnixMilli(expiresAt)); err != nil {
				log.Warnf("投递策略禁言结束任务失败: rid=%s err=%v", rid, err)
			}
		}
	}
}
