package notifycoord

import (
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/pkg/types"
)

// PrepareRoomMessageWithdraw 创建撤回 ACK（全体成员），返回房间内应尝试推送的 uid 列表
func PrepareRoomMessageWithdraw(mid string) ([]string, error) {
	if mid == "" {
		return nil, nil
	}
	rcm := query.GetRoomMessageByMidIncludeWithdraw(mid)
	if rcm == nil {
		log.Errorf("查询房间消息失败: %s", mid)
		return nil, nil
	}
	roomUserIds, err := query.GetRoomUserIdsCache(rcm.Rid)
	if err != nil {
		log.Error("查询房间内的用户失败:", err)
		return nil, err
	}
	acks := make([]*types.RoomMessageWithdrawAck, 0, len(roomUserIds))
	for _, uid := range roomUserIds {
		acks = append(acks, &types.RoomMessageWithdrawAck{
			Rid:   rcm.Rid,
			Uid:   uid,
			Mid:   rcm.Mid,
			SeqId: rcm.SeqId,
			State: 0,
		})
	}
	if len(acks) > 0 {
		tx := db.GetDB().Begin()
		if err := tx.Create(acks).Error; err != nil {
			log.Error("添加房间消息撤回ack失败:", err)
			tx.Rollback()
			return nil, err
		}
		if err := tx.Commit().Error; err != nil {
			log.Error("提交事务失败:", err)
			return nil, err
		}
	}
	return roomUserIds, nil
}
