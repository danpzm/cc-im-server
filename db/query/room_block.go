package query

import (
	"errors"
	"time"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"gorm.io/gorm"
)

// HasUserBlockedRoom 当前用户是否已屏蔽该房间（屏蔽方不接收该会话消息，可正常发送）
func HasUserBlockedRoom(uid, rid string) (bool, error) {
	var n int64
	err := db.GetDB().Model(&entity.UserRoomBlock{}).
		Where("uid = ? AND rid = ? AND delete_time = 0", uid, rid).
		Count(&n).Error
	return n > 0, err
}

// HasUserBlockedRoomBatch 批量查询用户对多个房间的屏蔽状态，返回 rid -> blocked。
func HasUserBlockedRoomBatch(uid string, rids []string) (map[string]bool, error) {
	result := make(map[string]bool, len(rids))
	if uid == "" || len(rids) == 0 {
		return result, nil
	}

	var blockedRids []string
	err := db.GetDB().Model(&entity.UserRoomBlock{}).
		Where("uid = ? AND rid IN ? AND delete_time = 0", uid, rids).
		Pluck("rid", &blockedRids).Error
	if err != nil {
		return result, err
	}
	for _, rid := range blockedRids {
		result[rid] = true
	}
	return result, nil
}

// GetUidsWhoBlockedRoom 返回已屏蔽该房间的 uid 列表，用于推送消息时排除（屏蔽方不接收）
func GetUidsWhoBlockedRoom(rid string) ([]string, error) {
	var uids []string
	err := db.GetDB().Model(&entity.UserRoomBlock{}).
		Where("rid = ? AND delete_time = 0", rid).
		Pluck("uid", &uids).Error
	return uids, err
}

// GetUserRoomBlockCreateTime 若用户已屏蔽该房间，返回屏蔽时间（毫秒），用于拉取消息时过滤屏蔽后的消息
func GetUserRoomBlockCreateTime(uid, rid string) (createTime int64, hasBlocked bool) {
	var block entity.UserRoomBlock
	err := db.GetDB().Where("uid = ? AND rid = ? AND delete_time = 0", uid, rid).First(&block).Error
	if err != nil || block.CreateTime == 0 {
		return 0, false
	}
	return block.CreateTime, true
}

// BlockRoom 屏蔽会话：屏蔽方不接收该会话消息，可正常发送。若曾取消屏蔽则恢复为屏蔽。
func BlockRoom(uid, rid string) error {
	var block entity.UserRoomBlock
	err := db.GetDB().Where("uid = ? AND rid = ?", uid, rid).First(&block).Error
	if err == nil {
		if block.DeleteTime != 0 {
			return db.GetDB().Model(&entity.UserRoomBlock{}).Where("id = ?", block.Id).Update("delete_time", 0).Error
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	block = entity.UserRoomBlock{Uid: uid, Rid: rid}
	return db.GetDB().Create(&block).Error
}

// UnblockRoom 取消屏蔽
func UnblockRoom(uid, rid string) error {
	return db.GetDB().Model(&entity.UserRoomBlock{}).
		Where("uid = ? AND rid = ? AND delete_time = 0", uid, rid).
		Update("delete_time", time.Now().UnixMilli()).Error
}
