package room

import (
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/xid"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/queue"
	"github.com/xd/quic-server/notify"
)

func RoomMessageList(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	rid := c.Query("rid")
	if rid == "" {
		response.BadRequest(c, "请输入房间id")
		return
	}
	seqId := c.Query("seq_id")
	if seqId == "" {
		response.BadRequest(c, "请输入序列号")
		return
	}
	seqIdInt, err := strconv.ParseInt(seqId, 10, 64)
	if err != nil {
		response.BadRequest(c, "请输入序列号")
		return
	}
	limit := c.Query("limit")
	if limit == "" {
		response.BadRequest(c, "请输入限制数量")
		return
	}
	limitInt, err := strconv.Atoi(limit)
	if err != nil {
		response.BadRequest(c, "请输入限制数量")
		return
	}
	if limitInt <= 0 {
		response.BadRequest(c, "请输入限制数量")
		return
	}
	page := c.Query("page")
	pageInt, err := strconv.Atoi(page)
	if err != nil {
		response.BadRequest(c, "请输入页码")
		return
	}
	if pageInt <= 0 {
		response.BadRequest(c, "请输入页码")
		return
	}
	offset := (pageInt - 1) * limitInt

	// 获取查询方向，默认为 forward（往前看）
	direction := c.DefaultQuery("direction", "forward")

	roomUserIds, err := query.GetRoomUserIdsCache(rid)
	if err != nil {
		response.BadRequest(c, "获取房间用户列表失败")
		return
	}
	if !slices.Contains(roomUserIds, user.Uid) {
		response.BadRequest(c, "无法获取该房间的消息")
		return
	}
	roomMessages := query.GetRoomMessageList(rid, seqIdInt, limitInt, offset, direction)
	// 屏蔽方不接收：若当前用户已屏蔽该房间，只返回屏蔽时间之前的消息
	if blockTime, hasBlocked := query.GetUserRoomBlockCreateTime(user.Uid, rid); hasBlocked && blockTime > 0 {
		filtered := make([]types.ServerRoomMessage, 0, len(roomMessages))
		for _, rm := range roomMessages {
			if rm.CreateTime > 0 && rm.CreateTime < blockTime {
				filtered = append(filtered, rm)
			}
		}
		roomMessages = filtered
	}
	// 房间内屏蔽某用户：过滤掉当前用户在该房间已屏蔽的用户发送的消息
	if blockedTargets, err := query.GetBlockedTargetUidsInRoom(user.Uid, rid); err == nil && len(blockedTargets) > 0 {
		blockSet := make(map[string]struct{}, len(blockedTargets))
		for _, u := range blockedTargets {
			blockSet[u] = struct{}{}
		}
		filtered := make([]types.ServerRoomMessage, 0, len(roomMessages))
		for _, rm := range roomMessages {
			if _, ok := blockSet[rm.SenderUid]; !ok {
				filtered = append(filtered, rm)
			}
		}
		roomMessages = filtered
	}
	hasMore := query.HasMoreRoomMessages(rid, seqIdInt, direction)
	response.Success(c, gin.H{
		"data":     roomMessages,
		"has_more": hasMore,
	})
}

// 消息撤回
func RoomMessageWithdraw(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	request := struct {
		Rid   string `json:"rid"`
		SeqId int64  `json:"seq_id"`
	}{}
	err := c.ShouldBindJSON(&request)
	if err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, request.Rid) {
		return
	}
	roomMessage := query.GetRoomMessageByRidSeqId(request.Rid, request.SeqId)
	if roomMessage == nil {
		response.BadRequest(c, "消息不存在")
		return
	}
	isSelfWithdraw := roomMessage.SenderUid == user.Uid
	if !isSelfWithdraw {
		ru, err := query.GetRoomUser(request.Rid, user.Uid)
		if err != nil || ru == nil || (ru.Role != entity.RoomUserRoleAdmin && ru.Role != entity.RoomUserRoleOwner) {
			response.BadRequest(c, "您无法撤回该消息")
			return
		}
	}
	if roomMessage.WithdrawTime > 0 {
		response.BadRequest(c, "消息已撤回")
		return
	}
	if isSelfWithdraw && roomMessage.CreateTime < time.Now().UnixMilli()-1000*60*5 {
		response.BadRequest(c, "消息已超过5分钟无法撤回")
		return
	}
	tx := db.GetDB().Begin()
	err = tx.Model(&types.RoomMessage{}).
		Where("rid = ?", request.Rid).
		Where("seq_id = ?", request.SeqId).
		Update("withdraw_time", time.Now().UnixMilli()).Error
	if err != nil {
		tx.Rollback()
		response.ServerError(c, "撤回失败")
		return
	}
	// 删除原本的消息内容
	err = tx.Model(&types.RoomMessageContent{}).
		Where("mid = ?", roomMessage.Mid).
		Update("delete_time", time.Now().UnixMilli()).Error
	if err != nil {
		tx.Rollback()
		response.ServerError(c, "撤回失败")
		return
	}
	// 添加撤回消息内容（保留 action_uid/target_uid，前端可区分“本人撤回”与“管理员撤回”）
	withdrawContent, _ := json.Marshal(map[string]any{
		"action_uid": user.Uid,
		"target_uid": roomMessage.SenderUid,
		"by_admin":   !isSelfWithdraw,
	})
	rmc := &types.RoomMessageContent{
		ClientCid: xid.New().String(),
		Type:      types.RoomMessageContentTypeUserWithdraw,
		TypeId:    user.Uid,
		Mid:       roomMessage.Mid,
		Content:   withdrawContent,
	}
	err = tx.Create(rmc).Error
	if err != nil {
		tx.Rollback()
		response.ServerError(c, "撤回失败")
		return
	}
	tx.Commit()
	if tx.Error != nil {
		response.ServerError(c, "撤回失败")
		return
	}
	_ = query.HardDeleteRoomPinnedMessageByMid(request.Rid, roomMessage.Mid)
	query.InvalidateRoomLastMessageCache(request.Rid)
	beforeData := map[string]any{"mid": roomMessage.Mid, "seq_id": roomMessage.SeqId, "sender_uid": roomMessage.SenderUid, "withdraw_time": roomMessage.WithdrawTime}
	afterData := map[string]any{"mid": roomMessage.Mid, "seq_id": roomMessage.SeqId, "sender_uid": roomMessage.SenderUid, "withdraw_time": time.Now().UnixMilli()}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: request.Rid, OpType: entity.RoomAdminOpMessageWithdraw, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: roomMessage.Mid,
		BeforeData: beforeData, AfterData: afterData,
	}, 0)
	if err := helper.NotifyQuic(notify.MessageTypeRoomMessageWithdrawNotify, notify.RoomMessageWithdrawNotifyPayload{Mid: roomMessage.Mid}); err != nil {
		log.Errorf("发送用户撤回消息通知失败: mid=%s err=%v", roomMessage.Mid, err)
	} else {
		log.Infof("已发送用户撤回消息通知: mid=%s", roomMessage.Mid)
	}
	response.Success(c, "撤回成功")
}
