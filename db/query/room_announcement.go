package query

import (
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
)

// ListRoomAnnouncements 获取房间公告列表（未软删除），按置顶优先、创建时间倒序
func ListRoomAnnouncements(rid string) ([]*entity.RoomAnnouncement, error) {
	var list []*entity.RoomAnnouncement
	err := db.GetDB().Where("rid = ? AND delete_time = ?", rid, 0).
		Order("pinned DESC, create_time DESC").
		Find(&list).Error
	return list, err
}

// GetRoomAnnouncementByID 获取单条公告（需属该房间且未删除）
func GetRoomAnnouncementByID(id int64, rid string) (*entity.RoomAnnouncement, error) {
	var a entity.RoomAnnouncement
	err := db.GetDB().Where("id = ? AND rid = ? AND delete_time = ?", id, rid, 0).First(&a).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// CreateRoomAnnouncement 创建一条公告
func CreateRoomAnnouncement(rid string, content json.RawMessage, operatorUid string, pinned bool) (*entity.RoomAnnouncement, error) {
	a := &entity.RoomAnnouncement{
		Rid:       rid,
		Content:   content,
		UpdatedBy: operatorUid,
		Pinned:    pinned,
	}
	if err := db.GetDB().Create(a).Error; err != nil {
		return nil, err
	}
	return a, nil
}

// UpdateRoomAnnouncement 更新公告（需属该房间且未删除）
func UpdateRoomAnnouncement(id int64, rid string, content json.RawMessage, operatorUid string, pinned *bool) (*entity.RoomAnnouncement, error) {
	a, err := GetRoomAnnouncementByID(id, rid)
	if err != nil || a == nil {
		return nil, err
	}
	a.Content = content
	a.UpdatedBy = operatorUid
	if pinned != nil {
		a.Pinned = *pinned
	}
	if err := db.GetDB().Save(a).Error; err != nil {
		return nil, err
	}
	return a, nil
}

// SoftDeleteRoomAnnouncement 软删除公告（需属该房间且未删除）
func SoftDeleteRoomAnnouncement(id int64, rid string) (*entity.RoomAnnouncement, error) {
	a, err := GetRoomAnnouncementByID(id, rid)
	if err != nil || a == nil {
		return nil, err
	}
	a.SoftDelete()
	if err := db.GetDB().Save(a).Error; err != nil {
		return nil, err
	}
	return a, nil
}
