package query

import (
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"gorm.io/gorm"
)

// GetRoomJoinRequestByRjrId 根据申请 ID 获取加入申请
func GetRoomJoinRequestByRjrId(rjrId string) (*entity.RoomJoinRequest, error) {
	var req *entity.RoomJoinRequest
	tx := db.GetDB().
		Where("rjr_id = ? AND delete_time = ?", rjrId, 0).
		First(&req)
	if tx.Error != nil {
		if tx.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		log.Errorf("GetRoomJoinRequestByRjrId err: %v", tx.Error)
		return nil, tx.Error
	}
	return req, nil
}

// GetRoomJoinRequestByRidAndApplicant 获取某用户对某房间的最新加入申请
func GetRoomJoinRequestByRidAndApplicant(rid, applicantUid string) (*entity.RoomJoinRequest, error) {
	var req *entity.RoomJoinRequest
	tx := db.GetDB().
		Where("rid = ? AND applicant_uid = ? AND delete_time = ?", rid, applicantUid, 0).
		Order("create_time DESC").
		First(&req)
	if tx.Error != nil {
		if tx.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		log.Errorf("GetRoomJoinRequestByRidAndApplicant err: %v", tx.Error)
		return nil, tx.Error
	}
	return req, nil
}

// GetPendingRoomJoinRequestByRidAndApplicant 获取待处理的加入申请
func GetPendingRoomJoinRequestByRidAndApplicant(rid, applicantUid string) (*entity.RoomJoinRequest, error) {
	req, err := GetRoomJoinRequestByRidAndApplicant(rid, applicantUid)
	if err != nil || req == nil {
		return req, err
	}
	if req.State != 0 {
		return nil, nil
	}
	if req.ExpiresAt > 0 && req.ExpiresAt < time.Now().UnixMilli() {
		return nil, nil
	}
	return req, nil
}

// CreateOrUpdateRoomJoinRequest 创建或更新加入申请（重复申请以最后一次为准）
func CreateOrUpdateRoomJoinRequest(request *entity.RoomJoinRequest) error {
	existing, err := GetRoomJoinRequestByRidAndApplicant(request.Rid, request.ApplicantUid)
	if err != nil {
		return err
	}
	if existing != nil {
		existing.State = 0
		existing.Message = request.Message
		existing.Answer = request.Answer
		existing.ExpiresAt = request.ExpiresAt
		existing.ProcessedAt = 0
		existing.HandlerUid = ""
		tx := db.GetDB().Save(existing)
		if tx.Error != nil {
			log.Errorf("CreateOrUpdateRoomJoinRequest update err: %v", tx.Error)
			return tx.Error
		}
		*request = *existing
		return nil
	}
	tx := db.GetDB().Create(request)
	if tx.Error != nil {
		log.Errorf("CreateOrUpdateRoomJoinRequest create err: %v", tx.Error)
		return tx.Error
	}
	return nil
}

// UpdateRoomJoinRequestStateWithTx 在事务内更新加入申请状态
func UpdateRoomJoinRequestStateWithTx(tx *gorm.DB, rjrId, handlerUid string, state int8) error {
	now := time.Now().UnixMilli()
	res := tx.Model(&entity.RoomJoinRequest{}).
		Where("rjr_id = ?", rjrId).
		Updates(map[string]any{
			"state":        state,
			"handler_uid":  handlerUid,
			"processed_at": now,
		})
	if res.Error != nil {
		log.Errorf("UpdateRoomJoinRequestStateWithTx err: %v", res.Error)
		return res.Error
	}
	return nil
}

// UpdateAllMessageNotificationStatesByRelatedIdAndType 更新同一 related_id 下指定类型的全部通知状态
func UpdateAllMessageNotificationStatesByRelatedIdAndType(relatedId string, notificationType entity.NotificationType, state entity.NotificationState) error {
	res := db.GetDB().Model(&entity.UserMessageNotification{}).
		Where("related_id = ? AND type = ? AND delete_time = ?", relatedId, notificationType, 0).
		Update("state", state)
	if res.Error != nil {
		log.Errorf("UpdateAllMessageNotificationStatesByRelatedIdAndType err: %v", res.Error)
		return res.Error
	}
	return nil
}
