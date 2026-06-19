package query

import (
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/utils"
)

func GetDBRoomMessageByMid(mid string) *types.RoomMessage {
	var roomMessage *types.RoomMessage
	err := db.GetDB().Model(&types.RoomMessage{}).Where("mid = ?", mid).First(&roomMessage).Error
	if err != nil {
		log.Errorf("GetDBRoomMessageByMid err: %v", err)
		return nil
	}
	return roomMessage
}
func GetRoomMessageByMid(mid string) *types.ServerRoomMessage {
	var roomMessage *types.ServerRoomMessage
	db.GetDB().
		Table("room_message rm").
		Where("rm.mid = ?", mid).
		Where("rm.state = ?", 1).
		Where("rm.delete_time = 0").
		Where("rm.withdraw_time = 0").
		Joins("LEFT JOIN room_message_content AS rmc ON rmc.mid = rm.mid AND rmc.delete_time = 0").
		Joins("LEFT JOIN user_upload_file AS uuf ON uuf.uf_id = rmc.type_id AND rmc.type IN('file','image','video','audio') AND uuf.delete_time = 0").
		Joins("LEFT JOIN upload_file AS uf ON uf.fid = uuf.fid AND uf.delete_time = 0").
		Select(utils.JoinField("rm", ".", []string{
			"seq_id",
			"mid",
			"rid",
			"sender_uid",
			"client_mid",
			"create_time",
		}), utils.BuildDynamicJSONAggSQL("rmc", map[string]string{
			"mid":         "mid",
			"cid":         "cid",
			"client_cid":  "client_cid",
			"content":     "content",
			"type":        "type",
			"type_id":     "type_id",
			"create_time": "create_time",
			"file": `-CASE WHEN uuf.uf_id IS NOT NULL THEN
				jsonb_build_object(
					'duration', uf.duration,
					'filename', uuf.filename,
					'file_hash', uf.hash,
					'file_size', uf.total_size,
					'file_type_main', uf.type_main,
					'file_type_sub', uf.type_sub,
					'height', uf.height,
					'width', uf.width,
					'uf_id', uuf.uf_id
				)
				ELSE NULL
			END`,
		}, "Contents", "rmc.id ASC")).
		Group("rm.mid, rm.seq_id, rm.rid, rm.sender_uid, rm.client_mid, rm.create_time").
		Find(&roomMessage)
	return roomMessage
}

// GetRoomMessageByMidIncludeWithdraw 获取房间消息（包括已撤回的消息）
func GetRoomMessageByMidIncludeWithdraw(mid string) *types.ServerRoomMessage {
	var roomMessage *types.ServerRoomMessage
	db.GetDB().
		Table("room_message rm").
		Where("rm.mid = ?", mid).
		Where("rm.state = ?", 1).
		Where("rm.delete_time = 0").
		// 不筛选 withdraw_time，允许查询已撤回的消息
		Joins("LEFT JOIN room_message_content AS rmc ON rmc.mid = rm.mid AND rmc.delete_time = 0").
		Joins("LEFT JOIN user_upload_file AS uuf ON uuf.uf_id = rmc.type_id AND rmc.type IN('file','image','video','audio') AND uuf.delete_time = 0").
		Joins("LEFT JOIN upload_file AS uf ON uf.fid = uuf.fid AND uf.delete_time = 0").
		Select(utils.JoinField("rm", ".", []string{
			"seq_id",
			"mid",
			"rid",
			"sender_uid",
			"client_mid",
			"create_time",
		}), utils.BuildDynamicJSONAggSQL("rmc", map[string]string{
			"mid":         "mid",
			"cid":         "cid",
			"client_cid":  "client_cid",
			"content":     "content",
			"type":        "type",
			"type_id":     "type_id",
			"create_time": "create_time",
			"file": `-CASE WHEN uuf.uf_id IS NOT NULL THEN
				jsonb_build_object(
					'duration', uf.duration,
					'filename', uuf.filename,
					'file_hash', uf.hash,
					'file_size', uf.total_size,
					'file_type_main', uf.type_main,
					'file_type_sub', uf.type_sub,
					'height', uf.height,
					'width', uf.width,
					'uf_id', uuf.uf_id
				)
				ELSE NULL
			END`,
		}, "Contents", "rmc.id ASC")).
		Group("rm.mid, rm.seq_id, rm.rid, rm.sender_uid, rm.client_mid, rm.create_time").
		Find(&roomMessage)
	return roomMessage
}

func GetRoomMessageByRidSeqId(rid string, seqId int64) *types.RoomMessage {
	var roomMessage *types.RoomMessage
	err := db.GetDB().Model(&types.RoomMessage{}).Where("rid = ?", rid).Where("seq_id = ?", seqId).First(&roomMessage).Error
	if err != nil {
		log.Errorf("GetRoomMessageByRidSeqId err: %v", err)
		return nil
	}
	return roomMessage
}
func GetRoomMessageList(rid string, seqId int64, limit int, offset int, direction string) []types.ServerRoomMessage {
	roomMessages := []types.ServerRoomMessage{}
	query := db.GetDB().
		Table("room_message rm").
		Where("rm.rid = ?", rid).
		Where("rm.state = ?", 1).
		Where("rm.delete_time = 0").
		Where("rm.withdraw_time = 0")

	// 根据 direction 参数决定查询方向
	if direction == "backward" {
		// 往回看：查询 seq_id < seqId 的消息（历史消息）
		query = query.Where("rm.seq_id < ?", seqId)
	} else {
		// 往前看：查询 seq_id > seqId 的消息（新消息，默认行为）
		query = query.Where("rm.seq_id > ?", seqId)
	}

	err := query.
		Joins("LEFT JOIN room_message_content AS rmc ON rmc.mid = rm.mid AND rmc.delete_time = 0").
		Joins("LEFT JOIN user_upload_file AS uuf ON uuf.uf_id = rmc.type_id AND rmc.type IN('file','image','video','audio') AND uuf.delete_time = 0").
		Joins("LEFT JOIN upload_file AS uf ON uf.fid = uuf.fid AND uf.delete_time = 0").
		Select(utils.JoinField("rm", ".", []string{
			"seq_id",
			"mid",
			"rid",
			"sender_uid",
			"client_mid",
			"create_time",
		}), utils.BuildDynamicJSONAggSQL("rmc", map[string]string{
			"mid":         "mid",
			"cid":         "cid",
			"client_cid":  "client_cid",
			"content":     "content",
			"type":        "type",
			"type_id":     "type_id",
			"create_time": "create_time",
			"file": `-CASE WHEN uuf.uf_id IS NOT NULL THEN
				jsonb_build_object(
					'duration', uf.duration,
					'filename', uuf.filename,
					'file_hash', uf.hash,
					'file_size', uf.total_size,
					'file_type_main', uf.type_main,
					'file_type_sub', uf.type_sub,
					'height', uf.height,
					'width', uf.width,
					'uf_id', uuf.uf_id
				)
				ELSE NULL
			END`,
		}, "Contents", "rmc.id ASC")).
		Group("rm.mid, rm.seq_id, rm.rid, rm.sender_uid, rm.client_mid, rm.create_time").
		Order("rm.seq_id DESC").
		Limit(limit).
		Offset(offset).
		Scan(&roomMessages).Error
	if err != nil {
		log.Errorf("GetRoomMessageList err: %v", err)
		return []types.ServerRoomMessage{}
	}
	return roomMessages
}
func HasMoreRoomMessages(rid string, seqId int64, direction string) bool {
	var count int64
	var where string
	if direction == "backward" {
		where = "rm.seq_id < ?"
	} else {
		where = "rm.seq_id > ?"
	}
	err := db.GetDB().
		Table("room_message rm").
		Where("rm.rid = ?", rid).
		Where("rm.state = ?", 1).
		Where("rm.delete_time = 0").
		Where("rm.withdraw_time = 0").
		Where(where, seqId).
		Count(&count).Error
	return err == nil && count > 0
}

// 获取用户未读的房间消息（基于 room_message_ack），按 seq_id 倒序取最新 limit 条
func GetUnreadRoomMessagesByAck(uid string, limit int) *[]types.ServerRoomMessage {
	var roomMessages *[]types.ServerRoomMessage
	err := db.GetDB().
		Table("room_message rm").
		Joins("INNER JOIN room_message_ack rma ON rma.mid = rm.mid AND rma.uid = ? AND rma.state = 0 AND rma.delete_time = 0", uid).
		Where("rm.state = ?", 1).
		Where("rm.delete_time = 0").
		Where("rm.withdraw_time = 0").
		Joins("LEFT JOIN room_message_content AS rmc ON rmc.mid = rm.mid AND rmc.delete_time = 0").
		Joins("LEFT JOIN user_upload_file AS uuf ON uuf.uf_id = rmc.type_id AND rmc.type IN('file','image','video','audio') AND uuf.delete_time = 0").
		Joins("LEFT JOIN upload_file AS uf ON uf.fid = uuf.fid AND uf.delete_time = 0").
		Select(utils.JoinField("rm", ".", []string{
			"seq_id",
			"mid",
			"rid",
			"sender_uid",
			"client_mid",
			"create_time",
		}), utils.BuildDynamicJSONAggSQL("rmc", map[string]string{
			"mid":         "mid",
			"cid":         "cid",
			"client_cid":  "client_cid",
			"content":     "content",
			"type":        "type",
			"type_id":     "type_id",
			"create_time": "create_time",
			"file": `-CASE WHEN uuf.uf_id IS NOT NULL THEN
				jsonb_build_object(
					'duration', uf.duration,
					'filename', uuf.filename,
					'file_hash', uf.hash,
					'file_size', uf.total_size,
					'file_type_main', uf.type_main,
					'file_type_sub', uf.type_sub,
					'height', uf.height,
					'width', uf.width,
					'uf_id', uuf.uf_id
				)
				ELSE NULL
			END`,
		}, "Contents", "rmc.id ASC")).
		Group("rm.mid, rm.seq_id, rm.rid, rm.sender_uid, rm.client_mid, rm.create_time").
		Order("rm.seq_id DESC").
		Limit(limit).
		Scan(&roomMessages).Error
	if err != nil {
		log.Errorf("GetUnreadRoomMessagesByAck err: %v", err)
		return &[]types.ServerRoomMessage{}
	}
	return roomMessages
}

// GetUnreadRoomMessagesWithdrawByAck 获取用户未读的撤回消息（基于 room_message_withdraw_ack），按 seq_id 倒序取最新 limit 条
func GetUnreadRoomMessagesWithdrawByAck(uid string, limit int) []types.ServerRoomMessage {
	roomMessages := []types.ServerRoomMessage{}
	err := db.GetDB().
		Table("room_message rm").
		Joins("INNER JOIN room_message_withdraw_ack rmwa ON rmwa.mid = rm.mid AND rmwa.uid = ? AND rmwa.state = 0 AND rmwa.delete_time = 0", uid).
		Where("rm.state = ?", 1).
		Where("rm.delete_time = 0").
		// 不筛选 withdraw_time，允许查询已撤回的消息
		Joins("LEFT JOIN room_message_content AS rmc ON rmc.mid = rm.mid AND rmc.delete_time = 0").
		Joins("LEFT JOIN user_upload_file AS uuf ON uuf.uf_id = rmc.type_id AND rmc.type IN('file','image','video','audio') AND uuf.delete_time = 0").
		Joins("LEFT JOIN upload_file AS uf ON uf.fid = uuf.fid AND uf.delete_time = 0").
		Select(utils.JoinField("rm", ".", []string{
			"seq_id",
			"mid",
			"rid",
			"sender_uid",
			"client_mid",
			"create_time",
		}), utils.BuildDynamicJSONAggSQL("rmc", map[string]string{
			"mid":         "mid",
			"cid":         "cid",
			"client_cid":  "client_cid",
			"content":     "content",
			"type":        "type",
			"type_id":     "type_id",
			"create_time": "create_time",
			"file": `-CASE WHEN uuf.uf_id IS NOT NULL THEN
				jsonb_build_object(
					'duration', uf.duration,
					'filename', uuf.filename,
					'file_hash', uf.hash,
					'file_size', uf.total_size,
					'file_type_main', uf.type_main,
					'file_type_sub', uf.type_sub,
					'height', uf.height,
					'width', uf.width,
					'uf_id', uuf.uf_id
				)
				ELSE NULL
			END`,
		}, "Contents", "rmc.id ASC")).
		Group("rm.mid, rm.seq_id, rm.rid, rm.sender_uid, rm.client_mid, rm.create_time").
		Order("rm.seq_id DESC").
		Limit(limit).
		Scan(&roomMessages).Error
	if err != nil {
		log.Errorf("GetUnreadRoomMessagesWithdrawByAck err: %v", err)
		return []types.ServerRoomMessage{}
	}
	return roomMessages
}

func GetRoomMessageContentByMidAndCid(mid string, cid string) *types.RoomMessageContent {
	var roomMessageContent *types.RoomMessageContent
	err := db.GetDB().
		Table("room_message_content AS rmc").
		Joins("INNER JOIN room_message AS rm ON rm.mid = rmc.mid AND rm.delete_time = 0 AND rm.withdraw_time = 0 AND rm.state = 1").
		Where("rm.mid = ?", mid).
		Where("rmc.mid = ?", mid).
		Where("rmc.cid = ?", cid).
		Where("rmc.delete_time = 0").
		Select("rmc.*").
		Scan(&roomMessageContent).Error
	if err != nil {
		log.Errorf("GetRoomMessageContentByMidAndCid err: %v", err)
		return nil
	}
	return roomMessageContent
}
