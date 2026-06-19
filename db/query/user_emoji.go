package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"gorm.io/gorm"
)

const (
	userEmojiRecentMax    = 50
	userEmojiFavoriteMax  = 200
)

var ErrEmojiFavoriteLimit = errors.New("收藏数量已达上限")
var ErrEmojiFavoriteDuplicate = errors.New("收藏重复")

// ListUserEmojiRecent 最近使用的表情，按使用时间倒序。
func ListUserEmojiRecent(uid string, limit int) ([]entity.UserEmojiRecent, error) {
	if limit <= 0 {
		limit = userEmojiRecentMax
	}
	var list []entity.UserEmojiRecent
	err := db.GetDB().
		Where("uid = ? AND delete_time = 0", uid).
		Order("used_at DESC").
		Limit(limit).
		Find(&list).Error
	return list, err
}

// UpsertUserEmojiRecent 记录或更新最近使用表情。
func UpsertUserEmojiRecent(uid, emojiId, label string) error {
	if uid == "" || emojiId == "" {
		return fmt.Errorf("参数无效")
	}
	now := time.Now().UnixMilli()
	var existing entity.UserEmojiRecent
	err := db.GetDB().
		Where("uid = ? AND emoji_id = ? AND delete_time = 0", uid, emojiId).
		First(&existing).Error
	if err == nil {
		if e := db.GetDB().Model(&existing).Updates(map[string]any{
			"used_at":     now,
			"label":       label,
			"update_time": now,
		}).Error; e != nil {
			return e
		}
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		if e := db.GetDB().Create(&entity.UserEmojiRecent{
			Uid:     uid,
			EmojiId: emojiId,
			Label:   label,
			UsedAt:  now,
		}).Error; e != nil {
			return e
		}
	} else {
		return err
	}
	return trimUserEmojiRecent(uid, userEmojiRecentMax)
}

func trimUserEmojiRecent(uid string, limit int) error {
	var overflow []entity.UserEmojiRecent
	err := db.GetDB().
		Where("uid = ? AND delete_time = 0", uid).
		Order("used_at DESC").
		Offset(limit).
		Find(&overflow).Error
	if err != nil || len(overflow) == 0 {
		return err
	}
	now := time.Now().UnixMilli()
	ids := make([]int64, 0, len(overflow))
	for _, row := range overflow {
		ids = append(ids, row.Id)
	}
	return db.GetDB().Model(&entity.UserEmojiRecent{}).
		Where("id IN ?", ids).
		Update("delete_time", now).Error
}

// ListUserEmojiFavorites 收藏列表，按创建时间倒序。
func ListUserEmojiFavorites(uid string) ([]entity.UserEmojiFavorite, error) {
	var list []entity.UserEmojiFavorite
	err := db.GetDB().
		Where("uid = ? AND delete_time = 0", uid).
		Order("create_time DESC").
		Find(&list).Error
	return list, err
}

// GetUserEmojiFavorite 获取单条收藏。
func GetUserEmojiFavorite(uid, kind, refKey string) (*entity.UserEmojiFavorite, error) {
	var row entity.UserEmojiFavorite
	err := db.GetDB().
		Where("uid = ? AND kind = ? AND ref_key = ? AND delete_time = 0", uid, kind, refKey).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func imageFavoritePayloadString(payload map[string]any) string {
	if payload == nil {
		return "{}"
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

func normalizeEmojiFavoritePayload(kind, refKey string, payload map[string]any) (map[string]any, string, error) {
	refKey = strings.TrimSpace(refKey)
	if kind != entity.UserEmojiFavoriteKindImage {
		return payload, refKey, nil
	}
	if refKey == "" {
		return nil, "", fmt.Errorf("file_hash 不能为空")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["file_hash"] = refKey
	return payload, refKey, nil
}

// AddUserEmojiFavorite 新增收藏；图片按 file_hash 去重。
func AddUserEmojiFavorite(uid, kind, refKey, label string, payload map[string]any) (*entity.UserEmojiFavorite, error) {
	if uid == "" || kind == "" || refKey == "" {
		return nil, fmt.Errorf("参数无效")
	}
	var err error
	payload, refKey, err = normalizeEmojiFavoritePayload(kind, refKey, payload)
	if err != nil {
		return nil, err
	}
	payloadJSON := imageFavoritePayloadString(payload)
	existing, err := GetUserEmojiFavorite(uid, kind, refKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrEmojiFavoriteDuplicate
	}
	var count int64
	if e := db.GetDB().Model(&entity.UserEmojiFavorite{}).
		Where("uid = ? AND delete_time = 0", uid).
		Count(&count).Error; e != nil {
		return nil, e
	}
	if count >= userEmojiFavoriteMax {
		return nil, ErrEmojiFavoriteLimit
	}
	row := &entity.UserEmojiFavorite{
		Uid:     uid,
		Kind:    kind,
		RefKey:  refKey,
		Label:   label,
		Payload: payloadJSON,
	}
	if e := db.GetDB().Create(row).Error; e != nil {
		return nil, e
	}
	return row, nil
}

// RemoveUserEmojiFavorite 取消收藏（软删）。
func RemoveUserEmojiFavorite(uid, kind, refKey string) error {
	now := time.Now().UnixMilli()
	result := db.GetDB().Model(&entity.UserEmojiFavorite{}).
		Where("uid = ? AND kind = ? AND ref_key = ? AND delete_time = 0", uid, kind, refKey).
		Update("delete_time", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
