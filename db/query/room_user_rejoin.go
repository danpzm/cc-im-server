package query

import (
	"errors"

	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/types"
	"gorm.io/gorm"
)

// UpsertActiveRoomUserTx 在事务内恢复已退出成员或新建成员记录。
func UpsertActiveRoomUserTx(tx *gorm.DB, rid, uid, nickname string, nowMs int64) error {
	var existing types.RoomUser
	err := tx.Where("rid = ? AND uid = ?", rid, uid).First(&existing).Error
	if err == nil {
		return tx.Model(&types.RoomUser{}).Where("rid = ? AND uid = ?", rid, uid).Updates(map[string]any{
			"delete_time":       0,
			"role":              entity.RoomUserRoleNormal,
			"mute_until":        0,
			"mute_operator_uid": "",
			"room_nickname":     nickname,
			"join_room_time":    nowMs,
			"update_time":       nowMs,
		}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	ru := &types.RoomUser{
		Rid:          rid,
		Uid:          uid,
		Role:         entity.RoomUserRoleNormal,
		RoomNickname: nickname,
		JoinRoomTime: nowMs,
	}
	return tx.Create(ru).Error
}

// UpsertActiveUserRoomSessionTx 在事务内恢复已退出会话或新建会话，返回 rsid。
func UpsertActiveUserRoomSessionTx(tx *gorm.DB, uid, rid string, lastSeqId, nowMs int64) (rsid string, err error) {
	var existing types.UserRoomSession
	err = tx.Where("uid = ? AND rid = ?", uid, rid).First(&existing).Error
	if err == nil {
		err = tx.Model(&types.UserRoomSession{}).Where("uid = ? AND rid = ?", uid, rid).Updates(map[string]any{
			"delete_time": 0,
			"state":       1,
			"last_seq_id": lastSeqId,
			"is_top":      false,
			"update_time": nowMs,
		}).Error
		return existing.Rsid, err
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	session := &types.UserRoomSession{
		Uid:       uid,
		Rid:       rid,
		LastSeqId: lastSeqId,
	}
	if err = tx.Create(session).Error; err != nil {
		return "", err
	}
	return session.Rsid, nil
}
