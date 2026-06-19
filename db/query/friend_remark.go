package query

import (
	"fmt"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
)

// GetFriendRemarkBatch 批量获取当前用户给好友设置的备注。
func GetFriendRemarkBatch(uid string, friendUids []string) (map[string]string, error) {
	result := make(map[string]string, len(friendUids))
	if uid == "" || len(friendUids) == 0 {
		return result, nil
	}
	var rows []struct {
		FriendUid string `gorm:"column:friend_uid"`
		Remark    string `gorm:"column:remark"`
	}
	tx := db.GetDB().
		Model(&entity.UserFriend{}).
		Select("friend_uid, remark").
		Where("uid = ? AND friend_uid IN ? AND friend_delete_time = 0", uid, friendUids).
		Find(&rows)
	if tx.Error != nil {
		return nil, tx.Error
	}
	for _, row := range rows {
		result[row.FriendUid] = row.Remark
	}
	return result, nil
}

// UpdateFriendRemark 更新当前用户给好友的备注。
func UpdateFriendRemark(uid, friendUid, remark string) error {
	if uid == "" || friendUid == "" {
		return fmt.Errorf("参数无效")
	}
	result := db.GetDB().
		Model(&entity.UserFriend{}).
		Where("uid = ? AND friend_uid = ? AND friend_delete_time = 0", uid, friendUid).
		Update("remark", remark)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("好友关系不存在")
	}
	return nil
}
