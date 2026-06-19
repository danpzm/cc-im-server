package query

import (
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/utils"
)

// CreateRoomMessageMentions 创建@我的消息记录
// mentions: 被@的用户ID列表，如果为空则说明是@all
func CreateRoomMessageMentions(rid string, mid string, seqId int64, senderUid string, mentions []string, isAtAll bool) error {
	if len(mentions) == 0 && !isAtAll {
		// 没有@任何人，不需要创建记录
		return nil
	}

	// 如果是@all，需要获取房间内所有用户
	if isAtAll {
		roomUserIds, err := GetRoomUserIdsCache(rid)
		if err != nil {
			log.Errorf("获取房间用户列表失败 rid=%s: %v", rid, err)
			return err
		}
		mentions = roomUserIds
	}

	// 创建@我的记录
	mentionsList := make([]*entity.RoomMessageMention, 0, len(mentions))
	for _, uid := range mentions {
		// 不给自己创建@我的记录
		if uid == senderUid {
			continue
		}
		mention := &entity.RoomMessageMention{
			Rid:       rid,
			Mid:       mid,
			SeqId:     seqId,
			Uid:       uid,
			SenderUid: senderUid,
			IsAtAll:   isAtAll,
			IsRead:    false,
			ReadAt:    0,
		}
		mentionsList = append(mentionsList, mention)
	}

	if len(mentionsList) == 0 {
		return nil
	}

	// 批量插入
	tx := db.GetDB().Create(mentionsList)
	if tx.Error != nil {
		log.Errorf("创建@我的消息记录失败 rid=%s mid=%s: %v", rid, mid, tx.Error)
		return tx.Error
	}

	return nil
}

// GetUnreadMentions 获取用户未读的@我的消息列表
func GetUnreadMentions(uid string, limit int) ([]*types.ServerRoomMessage, error) {
	if limit <= 0 {
		limit = 50
	}

	var roomMessages []*types.ServerRoomMessage
	err := db.GetDB().
		Table("room_message rm").
		Joins("INNER JOIN room_message_mention rmm ON rmm.mid = rm.mid AND rmm.uid = ? AND rmm.is_read = false AND rmm.delete_time = 0", uid).
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
		log.Errorf("GetUnreadMentions err: %v", err)
		return nil, err
	}

	return roomMessages, nil
}

// MarkMentionsAsRead 标记@我的消息为已读
// rid: 房间ID，如果为空则标记所有房间
// seqId: 最后阅读的seq_id，标记小于等于该seq_id的所有@我的消息为已读
func MarkMentionsAsRead(uid string, rid string, seqId int64) error {
	now := time.Now().UnixMilli()
	query := db.GetDB().Model(&entity.RoomMessageMention{}).
		Where("uid = ? AND is_read = false AND delete_time = 0", uid).
		Where("seq_id <= ?", seqId)

	if rid != "" {
		query = query.Where("rid = ?", rid)
	}

	tx := query.Updates(map[string]any{
		"is_read": true,
		"read_at": now,
	})

	if tx.Error != nil {
		log.Errorf("MarkMentionsAsRead err: %v", tx.Error)
		return tx.Error
	}

	return nil
}

// GetMentionsByRid 获取房间内@我的消息列表（用于会话详情页）
func GetMentionsByRid(uid string, rid string, limit int, offset int) ([]*types.ServerRoomMessage, error) {
	if limit <= 0 {
		limit = 20
	}

	var roomMessages []*types.ServerRoomMessage
	err := db.GetDB().
		Table("room_message rm").
		Joins("INNER JOIN room_message_mention rmm ON rmm.mid = rm.mid AND rmm.uid = ? AND rmm.delete_time = 0", uid).
		Where("rm.rid = ?", rid).
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
		Offset(offset).
		Scan(&roomMessages).Error

	if err != nil {
		log.Errorf("GetMentionsByRid err: %v", err)
		return nil, err
	}

	return roomMessages, nil
}
