package query

import (
	"errors"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"gorm.io/gorm"
)

// GetUserTheme 查询用户主题；记录不存在时返回空 JSON（不报错）
func GetUserTheme(uid string) (*entity.UserTheme, error) {
	var theme entity.UserTheme
	err := db.GetDB().Where("uid = ? AND delete_time = 0", uid).First(&theme).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &entity.UserTheme{Uid: uid, ThemeJson: "{}"}, nil
		}
		return nil, err
	}
	return &theme, nil
}

// UpsertUserTheme 存在则更新，不存在则插入
func UpsertUserTheme(uid string, themeJson string) error {
	var theme entity.UserTheme
	err := db.GetDB().Where("uid = ? AND delete_time = 0", uid).First(&theme).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return db.GetDB().Create(&entity.UserTheme{Uid: uid, ThemeJson: themeJson}).Error
		}
		return err
	}
	return db.GetDB().Model(&theme).Update("theme_json", themeJson).Error
}
