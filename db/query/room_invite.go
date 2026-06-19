package query

import (
	"errors"
	"time"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"gorm.io/gorm"
)

func CreateRoomInvite(invite *entity.RoomInvite) error {
	return db.GetDB().Create(invite).Error
}

func GetActiveRoomInviteByToken(token string, nowMs int64) (*entity.RoomInvite, error) {
	var invite entity.RoomInvite
	err := db.GetDB().
		Where("token = ? AND delete_time = 0 AND expires_at > ?", token, nowMs).
		First(&invite).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &invite, nil
}

func RecordRoomInviteJoin(invite *entity.RoomInvite, joinUid string) error {
	if invite == nil {
		return errors.New("invite is nil")
	}
	nowMs := time.Now().UnixMilli()
	tx := db.GetDB().Begin()
	if err := tx.Create(&entity.RoomInviteJoin{
		InviteId:   invite.InviteId,
		Token:      invite.Token,
		Rid:        invite.Rid,
		InviterUid: invite.InviterUid,
		JoinUid:    joinUid,
		JoinAt:     nowMs,
	}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Model(&entity.RoomInvite{}).
		Where("invite_id = ? AND delete_time = 0", invite.InviteId).
		Updates(map[string]any{
			"join_success_count": gorm.Expr("join_success_count + 1"),
			"last_join_uid":      joinUid,
			"last_join_at":       nowMs,
		}).Error; err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}
