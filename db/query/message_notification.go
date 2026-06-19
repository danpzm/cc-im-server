package query

import (
	"encoding/json"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"gorm.io/gorm"
)

// CreateMessageNotification 创建消息通知
func CreateMessageNotification(notification *entity.UserMessageNotification) error {
	tx := db.GetDB().Create(notification)
	if tx.Error != nil {
		log.Errorf("CreateMessageNotification err: %v", tx.Error)
		return tx.Error
	}
	return nil
}

// GetPendingMessageNotificationByUidRelatedIdAndType 获取未处理的同关联通知（申请中仅保留一条时用于覆盖）
func GetPendingMessageNotificationByUidRelatedIdAndType(uid, relatedId string, notificationType entity.NotificationType) (*entity.UserMessageNotification, error) {
	var notification *entity.UserMessageNotification
	tx := db.GetDB().
		Where("uid = ? AND related_id = ? AND type = ? AND state = ? AND delete_time = ?",
			uid, relatedId, notificationType, entity.NotificationStatePending, 0).
		Order("id DESC").
		First(&notification)
	if tx.Error != nil {
		if tx.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		log.Errorf("GetPendingMessageNotificationByUidRelatedIdAndType err: %v", tx.Error)
		return nil, tx.Error
	}
	return notification, nil
}

// UpsertPendingMessageNotification 申请中通知：存在未处理则覆盖，已处理过的保留历史并新建
func UpsertPendingMessageNotification(uid, relatedId string, notificationType entity.NotificationType, content string) (*entity.UserMessageNotification, error) {
	existing, err := GetPendingMessageNotificationByUidRelatedIdAndType(uid, relatedId, notificationType)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		now := time.Now().UnixMilli()
		if tx := db.GetDB().Model(existing).Updates(map[string]any{
			"content":     content,
			"status":      1,
			"read_at":     0,
			"update_time": now,
		}); tx.Error != nil {
			log.Errorf("UpsertPendingMessageNotification update err: %v", tx.Error)
			return nil, tx.Error
		}
		existing.Content = content
		existing.Status = 1
		existing.ReadAt = 0
		existing.UpdateTime = now
		return existing, nil
	}
	notif := &entity.UserMessageNotification{
		Uid:       uid,
		Type:      notificationType,
		RelatedId: relatedId,
		Content:   content,
	}
	if err := CreateMessageNotification(notif); err != nil {
		return nil, err
	}
	return notif, nil
}

// GetMessageNotificationByRelatedIdAndType 根据关联ID和类型获取通知（用于好友请求通知去重）
func GetMessageNotificationByRelatedIdAndType(uid string, relatedId string, notificationType entity.NotificationType) (*entity.UserMessageNotification, error) {
	var notification *entity.UserMessageNotification
	tx := db.GetDB().
		Where("uid = ? AND related_id = ? AND type = ? AND delete_time = ?", uid, relatedId, notificationType, 0).
		Order("id DESC").
		First(&notification)
	if tx.Error != nil {
		if tx.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		log.Errorf("GetMessageNotificationByRelatedIdAndType err: %v", tx.Error)
		return nil, tx.Error
	}
	return notification, nil
}

// GetMessageNotificationsByRelatedIdAndType 获取同一 related_id 下指定类型的全部通知
func GetMessageNotificationsByRelatedIdAndType(relatedId string, notificationType entity.NotificationType) ([]*entity.UserMessageNotification, error) {
	var notifications []*entity.UserMessageNotification
	tx := db.GetDB().
		Where("related_id = ? AND type = ? AND delete_time = ?", relatedId, notificationType, 0).
		Find(&notifications)
	if tx.Error != nil {
		log.Errorf("GetMessageNotificationsByRelatedIdAndType err: %v", tx.Error)
		return nil, tx.Error
	}
	return notifications, nil
}

// UpdateMessageNotificationState 更新已有消息通知的状态
// 仅在通知已存在时更新其 State 字段，不负责创建新通知
func UpdateMessageNotificationState(notification *entity.UserMessageNotification) error {
	// 查询是否存在对应通知
	existing, err := GetMessageNotificationByRelatedIdAndType(notification.Uid, notification.RelatedId, notification.Type)
	if err != nil {
		return err
	}

	if existing == nil {
		// 不存在则报错，不创建新通知
		log.Errorf("UpdateMessageNotificationState not found, uid=%s related_id=%s type=%d", notification.Uid, notification.RelatedId, notification.Type)
		return gorm.ErrRecordNotFound
	}

	// 仅使用 UPDATE 修改状态字段，其他字段（内容、是否显示、已读状态等）保持不变
	tx := db.GetDB().Model(existing).Update("state", notification.State)
	if tx.Error != nil {
		log.Errorf("UpdateMessageNotificationState update err: %v", tx.Error)
		return tx.Error
	}

	// 同步内存中的对象状态，便于后续使用（如拿 Nid 等字段）
	existing.State = notification.State
	*notification = *existing
	return nil
}

// GetMessageNotificationByNid 根据通知ID获取通知
func GetMessageNotificationByNid(nid string) (*entity.UserMessageNotification, error) {
	var notification *entity.UserMessageNotification
	tx := db.GetDB().
		Where("nid = ? AND delete_time = ?", nid, 0).
		First(&notification)
	if tx.Error != nil {
		log.Errorf("GetMessageNotificationByNid err: %v", tx.Error)
		return nil, tx.Error
	}
	return notification, nil
}

// messageNotificationListRow 一次 JOIN 查询的扫描行（扁平结构）
type messageNotificationListRow struct {
	Id             int64           `json:"id"`
	UpdateTime     int64           `json:"update_time"`
	Nid            string          `json:"nid"`
	Uid            string          `json:"uid"`
	Type           int8            `json:"type"`
	RelatedId      string          `json:"related_id"`
	Content        json.RawMessage `json:"content"`
	State          int8            `json:"state"`
	ReadAt         int64           `json:"read_at"`
	FriendUid      string          `json:"friend_uid"`
	FriendNickname string          `json:"friend_nickname"`
	FriendAvatarUfId string        `json:"friend_avatar_uf_id"`
	RoomRid        string          `json:"room_rid"`
	RoomName       string          `json:"room_name"`
	RoomAvatarUfId string          `json:"room_avatar_uf_id"`
}

// GetMessageNotificationList 获取用户的消息通知列表，一次 JOIN 查出好友/房间基础信息
func GetMessageNotificationList(uid string, page int, limit int) ([]*messageNotificationListRow, error) {
	if limit <= 0 {
		limit = 15
	}
	offset := (page - 1) * limit

	var rows []*messageNotificationListRow
	tx := db.GetDB().
		Table("user_message_notification AS n").
		Select(`n.id, n.update_time, n.nid, n.uid, n.type, n.related_id, n.content, n.state, n.read_at,
			u_friend.uid AS friend_uid,
			u_friend.nickname AS friend_nickname,
			u_friend.avatar_uf_id AS friend_avatar_uf_id,
			r.rid AS room_rid,
			r.name AS room_name,
			COALESCE(TRIM(COALESCE(r.avatar_uf_id, '')), u_creator.avatar_uf_id) AS room_avatar_uf_id`).
		Joins(`LEFT JOIN user_friend_request ufr ON ufr.fr_id = n.related_id AND (n.type = ? OR n.type = ?)`, entity.NotificationTypeFriendNotification, entity.NotificationTypeFriendAddRequest).
		Joins(`LEFT JOIN room_join_request rjr ON rjr.rjr_id = n.related_id AND n.type IN (?, ?)`, entity.NotificationTypeRoomJoinRequest, entity.NotificationTypeRoomJoinRequestSend).
		Joins(`LEFT JOIN "user" u_friend ON u_friend.uid = (CASE WHEN n.type = ? AND ufr.receiver_uid = n.uid THEN ufr.sender_uid WHEN n.type = ? AND ufr.sender_uid = n.uid THEN ufr.receiver_uid WHEN n.type = ? THEN rjr.applicant_uid ELSE NULL END) AND u_friend.delete_time = 0`, entity.NotificationTypeFriendNotification, entity.NotificationTypeFriendAddRequest, entity.NotificationTypeRoomJoinRequest).
		Joins(`LEFT JOIN room r ON r.rid = (CASE WHEN n.type = ? THEN n.related_id WHEN n.type IN (?, ?) THEN rjr.rid ELSE NULL END) AND r.delete_time = 0`, entity.NotificationTypeRoomNotification, entity.NotificationTypeRoomJoinRequest, entity.NotificationTypeRoomJoinRequestSend).
		Joins(`LEFT JOIN "user" u_creator ON u_creator.uid = r.create_uid AND u_creator.delete_time = 0`).
		Where("n.uid = ? AND n.delete_time = ?", uid, 0).
		Where("n.status = ?", 1).
		Order("n.create_time DESC").
		Offset(offset).
		Limit(limit).
		Find(&rows)

	if tx.Error != nil {
		log.Errorf("GetMessageNotificationList err: %v", tx.Error)
		return nil, tx.Error
	}

	return rows, nil
}

// GetUnreadMessageNotificationCount 获取未读消息通知数量。
// 若 COUNT 偏慢，可在库上增加部分索引，例如：
// CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_umnotif_uid_unread ON user_message_notification (uid) WHERE read_at = 0 AND delete_time = 0;
func GetUnreadMessageNotificationCount(uid string) (int64, error) {
	var count int64
	tx := db.GetDB().Model(&entity.UserMessageNotification{}).
		Where("uid = ? AND read_at = 0 AND delete_time = 0", uid).
		Count(&count)
	if tx.Error != nil {
		log.Errorf("GetUnreadMessageNotificationCount err: %v", tx.Error)
		return 0, tx.Error
	}
	return count, nil
}

// UpdateMessageNotificationStateWithTx 在事务内根据 uid、related_id、类型更新通知状态（用于好友请求处理完成后更新接收方通知）
func UpdateMessageNotificationStateWithTx(tx *gorm.DB, uid string, relatedId string, notificationType entity.NotificationType, state entity.NotificationState) error {
	res := tx.Model(&entity.UserMessageNotification{}).
		Where("uid = ? AND related_id = ? AND type = ? AND delete_time = ?", uid, relatedId, notificationType, 0).
		Update("state", state)
	if res.Error != nil {
		log.Errorf("UpdateMessageNotificationStateWithTx err: %v", res.Error)
		return res.Error
	}
	return nil
}

// MarkMessageNotificationAsRead 标记消息通知为已读
func MarkMessageNotificationAsRead(nid string, readAt int64) error {
	tx := db.GetDB().Model(&entity.UserMessageNotification{}).
		Where("nid = ?", nid).
		Updates(map[string]any{
			"read_at": readAt,
		})
	if tx.Error != nil {
		log.Errorf("MarkMessageNotificationAsRead err: %v", tx.Error)
		return tx.Error
	}
	return nil
}

// MarkAllMessageNotificationAsRead 标记所有消息通知为已读
func MarkAllMessageNotificationAsRead(uid string) error {
	now := time.Now().UnixMilli()
	tx := db.GetDB().Model(&entity.UserMessageNotification{}).
		Where("uid = ? AND read_at = 0 AND delete_time = 0", uid).
		Updates(map[string]any{
			"read_at": now,
		})
	if tx.Error != nil {
		log.Errorf("MarkAllMessageNotificationAsRead err: %v", tx.Error)
		return tx.Error
	}
	return nil
}
