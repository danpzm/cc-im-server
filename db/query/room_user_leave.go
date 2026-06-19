package query

import (
	"time"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/types"
)

// RemoveRoomUser 软删除房间成员记录
func RemoveRoomUser(rid, uid string) error {
	now := time.Now().UnixMilli()
	return db.GetDB().Model(&types.RoomUser{}).
		Where("rid = ? AND uid = ? AND delete_time = 0", rid, uid).
		Update("delete_time", now).Error
}

// HideUserRoomSession 隐藏用户在该房间的会话（退出房间后不再出现在会话列表）
func HideUserRoomSession(uid, rid string) error {
	now := time.Now().UnixMilli()
	return db.GetDB().Model(&types.UserRoomSession{}).
		Where("uid = ? AND rid = ? AND delete_time = 0", uid, rid).
		Updates(map[string]any{
			"state":       0,
			"delete_time": now,
		}).Error
}

// CanKickMemberInRoom 判断 operator 是否有权将 target 移出房间
func CanKickMemberInRoom(rid, operatorUid, targetUid string) (ok bool, reason string) {
	if operatorUid == targetUid {
		return false, "不能移出自己"
	}
	operatorRu, err := GetRoomUser(rid, operatorUid)
	if err != nil || operatorRu == nil {
		return false, "您不在该房间"
	}
	if operatorRu.Role != entity.RoomUserRoleAdmin && operatorRu.Role != entity.RoomUserRoleOwner {
		return false, "仅管理员或房主可移出成员"
	}
	targetRu, err := GetRoomUser(rid, targetUid)
	if err != nil || targetRu == nil {
		return false, "该用户不在房间内"
	}
	if targetRu.Role == entity.RoomUserRoleOwner {
		return false, "不能移出群主"
	}
	if targetRu.Role == entity.RoomUserRoleAdmin && operatorRu.Role != entity.RoomUserRoleOwner {
		return false, "仅群主可移出管理员"
	}
	return true, ""
}

// GetRoomAdminAndOwnerUids 获取房间内管理员与房主 uid（不含 excludeUid）
func GetRoomAdminAndOwnerUids(rid, excludeUid string) ([]string, error) {
	var uids []string
	tx := db.GetDB().Model(&types.RoomUser{}).
		Where("rid = ? AND delete_time = 0 AND role IN (?, ?)", rid, entity.RoomUserRoleAdmin, entity.RoomUserRoleOwner).
		Where("uid != ?", excludeUid).
		Pluck("uid", &uids)
	if tx.Error != nil {
		return nil, tx.Error
	}
	return uids, nil
}
