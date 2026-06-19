package room

import (
	"slices"

	"github.com/gin-gonic/gin"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/queue"
)

// RoomPinnedMessageGet 获取房间当前置顶消息（仅房间成员可读）
func RoomPinnedMessageGet(c *gin.Context) {
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
	roomUserIds, err := query.GetRoomUserIdsCache(rid)
	if err != nil {
		response.BadRequest(c, "获取房间用户列表失败")
		return
	}
	if !slices.Contains(roomUserIds, user.Uid) {
		response.BadRequest(c, "无法获取该房间置顶消息")
		return
	}
	dto, err := query.BuildRoomPinnedMessageDto(rid)
	if err != nil {
		response.ServerError(c, "获取置顶消息失败")
		return
	}
	response.Success(c, dto)
}

type roomPinnedMessagePinBody struct {
	Rid   string `json:"rid"`
	SeqId int64  `json:"seq_id"`
}

// RoomPinnedMessagePin 置顶消息（仅房主或管理员）
func RoomPinnedMessagePin(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomPinnedMessagePinBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" || body.SeqId <= 0 {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.CanUserMuteInRoom(body.Rid, user.Uid) {
		response.BadRequest(c, "仅房主或管理员可置顶消息")
		return
	}
	roomUserIds, err := query.GetRoomUserIdsCache(body.Rid)
	if err != nil {
		response.BadRequest(c, "获取房间用户列表失败")
		return
	}
	if !slices.Contains(roomUserIds, user.Uid) {
		response.BadRequest(c, "无法操作该房间")
		return
	}
	roomMessage := query.GetRoomMessageByRidSeqId(body.Rid, body.SeqId)
	if roomMessage == nil {
		response.BadRequest(c, "消息不存在")
		return
	}
	if roomMessage.WithdrawTime > 0 {
		response.BadRequest(c, "消息已撤回，无法置顶")
		return
	}
	if roomMessage.State != 1 || roomMessage.DeleteTime != 0 {
		response.BadRequest(c, "消息不可置顶")
		return
	}
	if roomMessage.SenderUid == "system" {
		response.BadRequest(c, "系统消息不可置顶")
		return
	}

	prev, _ := query.GetRoomPinnedMessageRecord(body.Rid)
	row, err := query.PinRoomMessage(body.Rid, roomMessage.Mid, roomMessage.SeqId, user.Uid)
	if err != nil {
		response.ServerError(c, "置顶失败")
		return
	}
	beforeData := map[string]any(nil)
	if prev != nil {
		beforeData = map[string]any{
			"mid":          prev.Mid,
			"seq_id":       prev.SeqId,
			"operator_uid": prev.OperatorUid,
			"pinned_at":    prev.CreateTime,
		}
	}
	afterData := map[string]any{
		"mid":          row.Mid,
		"seq_id":       row.SeqId,
		"operator_uid": row.OperatorUid,
		"pinned_at":    row.CreateTime,
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpMessagePin, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: row.Mid,
		BeforeData: beforeData, AfterData: afterData,
	}, 0)
	createRoomSystemMessageAndNotify(
		c,
		body.Rid,
		entity.RoomMessageContentType("room:message:pin"),
		user.Uid,
		map[string]any{
			"mid":          row.Mid,
			"seq_id":       row.SeqId,
			"operator_uid": user.Uid,
			"sender_uid":   roomMessage.SenderUid,
		},
		nil,
		nil,
	)
	dto, err := query.BuildRoomPinnedMessageDto(body.Rid)
	if err != nil {
		response.ServerError(c, "获取置顶消息失败")
		return
	}
	response.Success(c, dto)
}

type roomPinnedMessageUnpinBody struct {
	Rid string `json:"rid"`
}

// RoomPinnedMessageUnpin 取消置顶（仅房主或管理员）
func RoomPinnedMessageUnpin(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body roomPinnedMessageUnpinBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Rid == "" {
		response.BadRequest(c, "参数错误")
		return
	}
	if rejectIfRoomDissolved(c, body.Rid) {
		return
	}
	if !query.CanUserMuteInRoom(body.Rid, user.Uid) {
		response.BadRequest(c, "仅房主或管理员可取消置顶")
		return
	}
	prev, err := query.GetRoomPinnedMessageRecord(body.Rid)
	if err != nil {
		response.ServerError(c, "取消置顶失败")
		return
	}
	if prev == nil {
		response.BadRequest(c, "当前无置顶消息")
		return
	}
	if err := query.HardDeleteRoomPinnedMessage(body.Rid); err != nil {
		response.ServerError(c, "取消置顶失败")
		return
	}
	beforeData := map[string]any{
		"mid":          prev.Mid,
		"seq_id":       prev.SeqId,
		"operator_uid": prev.OperatorUid,
		"pinned_at":    prev.CreateTime,
	}
	_ = queue.PublishOpLogTaskDefault(queue.TaskRoomAdminOperationLog, queue.RoomAdminOperationLogPayload{
		Rid: body.Rid, OpType: entity.RoomAdminOpMessageUnpin, OperatorUid: user.Uid, Sid: helper.GetSid(c), RelatedId: prev.Mid,
		BeforeData: beforeData, AfterData: map[string]any{"deleted": true},
	}, 0)
	createRoomSystemMessageAndNotify(
		c,
		body.Rid,
		entity.RoomMessageContentType("room:message:unpin"),
		user.Uid,
		map[string]any{
			"mid":          prev.Mid,
			"seq_id":       prev.SeqId,
			"operator_uid": user.Uid,
		},
		nil,
		nil,
	)
	response.Success(c, nil)
}
