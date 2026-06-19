package roommsg

import (
	"context"
	"encoding/json"

	log "github.com/sirupsen/logrus"
	"github.com/rs/xid"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/push"
	"github.com/xd/quic-server/notify"
)

// CreateSystemMessageAndNotify 创建房间系统消息并通知 QUIC 广播（供队列等无 gin.Context 的后台调用）
func CreateSystemMessageAndNotify(rid string, contentType entity.RoomMessageContentType, typeID string, content any) {
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
		IP:        "",
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
	if err := query.BumpUserRoomSessionLastMessageTime(room.Rid, rm.CreateTime); err != nil {
		log.Warnf("BumpUserRoomSessionLastMessageTime rid=%s: %v", room.Rid, err)
		return
	}
	_ = push.Send(context.Background(), notify.MessageTypeRoomMessageNotify, notify.RoomMessageNotifyPayload{Mid: rm.Mid})
}
