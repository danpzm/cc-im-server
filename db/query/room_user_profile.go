package query

import (
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/types"
)

// UpdateRoomUserRole 更新房间成员角色（仅房主可操作，且不能将他人设为房主）
func UpdateRoomUserRole(rid, targetUid string, newRole entity.RoomUserRole) (*types.RoomUser, error) {
	ru, err := GetRoomUser(rid, targetUid)
	if err != nil || ru == nil {
		return nil, err
	}
	if err := db.GetDB().
		Model(&types.RoomUser{}).
		Where("rid = ? AND uid = ? AND delete_time = 0", rid, targetUid).
		Update("role", newRole).
		Error; err != nil {
		return nil, err
	}
	return GetRoomUser(rid, targetUid)
}

// UpdateRoomUserNickname 仅更新本群昵称
func UpdateRoomUserNickname(rid, uid, roomNickname string) (*types.RoomUser, error) {
	if err := db.GetDB().
		Model(&types.RoomUser{}).
		Where("rid = ? AND uid = ? AND delete_time = 0", rid, uid).
		Update("room_nickname", roomNickname).
		Error; err != nil {
		return nil, err
	}
	return GetRoomUser(rid, uid)
}

// UpdateRoomUserRemark 仅更新房间备注
func UpdateRoomUserRemark(rid, uid, roomRemark string) (*types.RoomUser, error) {
	if err := db.GetDB().
		Model(&types.RoomUser{}).
		Where("rid = ? AND uid = ? AND delete_time = 0", rid, uid).
		Update("room_remark", roomRemark).
		Error; err != nil {
		return nil, err
	}
	return GetRoomUser(rid, uid)
}
