package query

import (
	"errors"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/types"
)

var (
	ErrNotRoomOwner         = errors.New("not room owner")
	ErrInvalidTransferTarget = errors.New("invalid transfer target")
	ErrRoomAlreadyDissolved = errors.New("room already dissolved")
)

// TransferRoomOwner 将群主转让给目标成员：更新 room.create_uid 与双方 role。
func TransferRoomOwner(rid, operatorUid, targetUid string) (oldOwnerUid string, err error) {
	if rid == "" || operatorUid == "" || targetUid == "" {
		return "", ErrInvalidTransferTarget
	}
	if operatorUid == targetUid {
		return "", ErrInvalidTransferTarget
	}
	operator, err := GetRoomUser(rid, operatorUid)
	if err != nil || operator == nil {
		return "", err
	}
	if operator.Role != entity.RoomUserRoleOwner {
		return "", ErrNotRoomOwner
	}
	target, err := GetRoomUser(rid, targetUid)
	if err != nil || target == nil {
		return "", ErrInvalidTransferTarget
	}
	if target.Role == entity.RoomUserRoleOwner {
		return "", ErrInvalidTransferTarget
	}
	tx := db.GetDB().Begin()
	if err = tx.Model(&types.Room{}).
		Where("rid = ? AND delete_time = 0 AND state = ?", rid, entity.RoomStateActive).
		Update("create_uid", targetUid).Error; err != nil {
		tx.Rollback()
		return "", err
	}
	if err = tx.Model(&types.RoomUser{}).
		Where("rid = ? AND uid = ? AND delete_time = 0", rid, operatorUid).
		Update("role", entity.RoomUserRoleNormal).Error; err != nil {
		tx.Rollback()
		return "", err
	}
	if err = tx.Model(&types.RoomUser{}).
		Where("rid = ? AND uid = ? AND delete_time = 0", rid, targetUid).
		Update("role", entity.RoomUserRoleOwner).Error; err != nil {
		tx.Rollback()
		return "", err
	}
	if err = tx.Commit().Error; err != nil {
		return "", err
	}
	return operatorUid, nil
}

// DissolveRoom 解散群聊：将 room.state 置为已解散，不删除成员与会话。
func DissolveRoom(rid string) error {
	res := db.GetDB().Model(&types.Room{}).
		Where("rid = ? AND delete_time = 0 AND state = ?", rid, entity.RoomStateActive).
		Update("state", entity.RoomStateDissolved)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrRoomAlreadyDissolved
	}
	return nil
}
