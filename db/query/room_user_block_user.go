package query

import (
	"errors"
	"time"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"gorm.io/gorm"
)

// HasUserBlockedUserInRoom 是否在房间内屏蔽了某用户（不接收该用户在该房间的消息）
func HasUserBlockedUserInRoom(uid, rid, targetUid string) (bool, error) {
	var n int64
	err := db.GetDB().Model(&entity.RoomUserBlockUser{}).
		Where("uid = ? AND rid = ? AND target_uid = ? AND delete_time = 0", uid, rid, targetUid).
		Count(&n).Error
	return n > 0, err
}

// GetUidsWhoBlockedUserInRoom 返回在房间 rid 内已屏蔽 senderUid 的用户列表，用于推送时排除（不向其推送该发送者的消息）
func GetUidsWhoBlockedUserInRoom(rid, senderUid string) ([]string, error) {
	var uids []string
	err := db.GetDB().Model(&entity.RoomUserBlockUser{}).
		Where("rid = ? AND target_uid = ? AND delete_time = 0", rid, senderUid).
		Pluck("uid", &uids).Error
	return uids, err
}

// GetBlockedTargetUidsInRoom 返回当前用户在房间内已屏蔽的 target_uid 列表，用于拉取消息时过滤
func GetBlockedTargetUidsInRoom(uid, rid string) ([]string, error) {
	var targetUids []string
	err := db.GetDB().Model(&entity.RoomUserBlockUser{}).
		Where("uid = ? AND rid = ? AND delete_time = 0", uid, rid).
		Pluck("target_uid", &targetUids).Error
	return targetUids, err
}

// BlockUserInRoom 在房间内屏蔽某用户：当前用户不接收该用户在该房间的消息
func BlockUserInRoom(uid, rid, targetUid string) error {
	var rec entity.RoomUserBlockUser
	err := db.GetDB().Where("uid = ? AND rid = ? AND target_uid = ?", uid, rid, targetUid).First(&rec).Error
	if err == nil {
		if rec.DeleteTime != 0 {
			return db.GetDB().Model(&entity.RoomUserBlockUser{}).Where("id = ?", rec.Id).Update("delete_time", 0).Error
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	rec = entity.RoomUserBlockUser{Uid: uid, Rid: rid, TargetUid: targetUid}
	return db.GetDB().Create(&rec).Error
}

// UnblockUserInRoom 在房间内取消屏蔽某用户
func UnblockUserInRoom(uid, rid, targetUid string) error {
	return db.GetDB().Model(&entity.RoomUserBlockUser{}).
		Where("uid = ? AND rid = ? AND target_uid = ? AND delete_time = 0", uid, rid, targetUid).
		Update("delete_time", time.Now().UnixMilli()).Error
}
