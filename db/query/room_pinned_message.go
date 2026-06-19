package query

import (
	"errors"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/types"
	"gorm.io/gorm"
)

// RoomPinnedMessageDto 房间置顶消息（含消息预览）
type RoomPinnedMessageDto struct {
	Rid         string                   `json:"rid"`
	Mid         string                   `json:"mid"`
	SeqId       int64                    `json:"seq_id"`
	OperatorUid string                   `json:"operator_uid"`
	PinnedAt    int64                    `json:"pinned_at"`
	Message     *types.ServerRoomMessage `json:"message,omitempty"`
}

func GetRoomPinnedMessageRecord(rid string) (*entity.RoomPinnedMessage, error) {
	if rid == "" {
		return nil, nil
	}
	var row entity.RoomPinnedMessage
	err := db.GetDB().
		Where("rid = ?", rid).
		Where("delete_time = 0").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// BuildRoomPinnedMessageDto 获取房间当前置顶消息；消息已撤回或不存在时自动清除置顶记录
func BuildRoomPinnedMessageDto(rid string) (*RoomPinnedMessageDto, error) {
	row, err := GetRoomPinnedMessageRecord(rid)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	msg := GetRoomMessageByMid(row.Mid)
	if msg == nil || msg.Mid == "" {
		if err := HardDeleteRoomPinnedMessage(rid); err != nil {
			log.Errorf("BuildRoomPinnedMessageDto 清除无效置顶 rid=%s mid=%s: %v", rid, row.Mid, err)
		}
		return nil, nil
	}
	return &RoomPinnedMessageDto{
		Rid:         row.Rid,
		Mid:         row.Mid,
		SeqId:       row.SeqId,
		OperatorUid: row.OperatorUid,
		PinnedAt:    row.CreateTime,
		Message:     msg,
	}, nil
}

func PinRoomMessage(rid, mid string, seqId int64, operatorUid string) (*entity.RoomPinnedMessage, error) {
	var out entity.RoomPinnedMessage
	err := db.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("rid = ?", rid).Delete(&entity.RoomPinnedMessage{}).Error; err != nil {
			return err
		}
		out = entity.RoomPinnedMessage{
			Rid:         rid,
			Mid:         mid,
			SeqId:       seqId,
			OperatorUid: operatorUid,
		}
		return tx.Create(&out).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func HardDeleteRoomPinnedMessage(rid string) error {
	return db.GetDB().Unscoped().Where("rid = ?", rid).Delete(&entity.RoomPinnedMessage{}).Error
}

func HardDeleteRoomPinnedMessageByMid(rid, mid string) error {
	return db.GetDB().Unscoped().Where("rid = ? AND mid = ?", rid, mid).Delete(&entity.RoomPinnedMessage{}).Error
}
