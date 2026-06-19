package query

import (
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"gorm.io/gorm"
)

// GetFriendRequestBySenderAndReceiver 获取好友请求（根据发送者和接收者）
func GetFriendRequestBySenderAndReceiver(senderUid, receiverUid string) (*entity.UserFriendRequest, error) {
	var request *entity.UserFriendRequest
	tx := db.GetDB().
		Where("sender_uid = ? AND receiver_uid = ? AND delete_time = ?", senderUid, receiverUid, 0).
		Order("create_time DESC").
		First(&request)
	if tx.Error != nil {
		if tx.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		log.Errorf("GetFriendRequestBySenderAndReceiver err: %v", tx.Error)
		return nil, tx.Error
	}
	return request, nil
}

// GetFriendRequestList 获取好友请求列表（作为接收者或发送者）
func GetFriendRequestList(uid string, asReceiver bool) ([]*entity.UserFriendRequest, error) {
	var requests []*entity.UserFriendRequest
	where := db.GetDB().Model(&entity.UserFriendRequest{}).
		Where("delete_time = ?", 0)
	if asReceiver {
		where = where.Where("receiver_uid = ?", uid)
	} else {
		where = where.Where("sender_uid = ?", uid)
	}
	tx := where.Order("create_time DESC").Find(&requests)
	if tx.Error != nil {
		log.Errorf("GetFriendRequestList err: %v", tx.Error)
		return nil, tx.Error
	}
	return requests, nil
}

// GetFriendRequestByFrId 根据好友请求ID获取请求
func GetFriendRequestByFrId(frId string) (*entity.UserFriendRequest, error) {
	var request *entity.UserFriendRequest
	tx := db.GetDB().
		Where("fr_id = ? AND delete_time = ?", frId, 0).
		First(&request)
	if tx.Error != nil {
		if tx.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		log.Errorf("GetFriendRequestByFrId err: %v", tx.Error)
		return nil, tx.Error
	}
	return request, nil
}

// CreateOrUpdateFriendRequest 创建或更新好友请求（重复添加以最后一次请求为准）
func CreateOrUpdateFriendRequest(request *entity.UserFriendRequest) error {
	// 查找是否存在相同发送者和接收者的请求
	existing, err := GetFriendRequestBySenderAndReceiver(request.SenderUid, request.ReceiverUid)
	if err != nil {
		return err
	}

	if existing != nil {
		// 如果存在，更新现有请求（重置状态为等待验证，更新消息和备注）
		existing.State = 0 // 重置为等待验证
		existing.Message = request.Message
		existing.Remark = request.Remark
		existing.Gid = request.Gid
		existing.ExpiresAt = request.ExpiresAt
		existing.ProcessedAt = 0 // 重置处理时间
		// UpdateTime 会在 BeforeUpdate 中自动更新

		tx := db.GetDB().Save(existing)
		if tx.Error != nil {
			log.Errorf("CreateOrUpdateFriendRequest update err: %v", tx.Error)
			return tx.Error
		}
		*request = *existing
		return nil
	}

	// 如果不存在，创建新请求
	// CreateTime 和 UpdateTime 会在 BeforeCreate 中自动设置
	tx := db.GetDB().Create(request)
	if tx.Error != nil {
		log.Errorf("CreateOrUpdateFriendRequest create err: %v", tx.Error)
		return tx.Error
	}
	return nil
}

// UpdateFriendRequestState 更新好友请求状态
func UpdateFriendRequestState(frId string, state int8) error {
	return UpdateFriendRequestStateWithTx(db.GetDB(), frId, state)
}

// UpdateFriendRequestStateWithTx 使用指定事务更新好友请求状态
func UpdateFriendRequestStateWithTx(tx *gorm.DB, frId string, state int8) error {
	now := time.Now().UnixMilli()
	result := tx.Model(&entity.UserFriendRequest{}).
		Where("fr_id = ?", frId).
		Updates(map[string]any{
			"state":        state,
			"processed_at": now,
		})
	if result.Error != nil {
		log.Errorf("UpdateFriendRequestStateWithTx err: %v", result.Error)
		return result.Error
	}
	return nil
}

// GetDefaultFriendGroup 获取用户的默认好友分组
func GetDefaultFriendGroup(uid string) (*entity.UserFriendGroup, error) {
	var group *entity.UserFriendGroup
	tx := db.GetDB().
		Where("uid = ? AND is_default = ? AND delete_time = ?", uid, true, 0).
		First(&group)
	if tx.Error != nil {
		if tx.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		log.Errorf("GetDefaultFriendGroup err: %v", tx.Error)
		return nil, tx.Error
	}
	return group, nil
}

// CreateFriendRelation 创建双向好友关系
// receiverUid: 接收者（同意请求的用户）
// senderUid: 发送者（发起请求的用户）
// receiverGid: 接收者的分组ID（从好友请求中获取）
// senderRemark: 发送者给接收者的备注（从好友请求中获取）
func CreateFriendRelation(receiverUid, senderUid, receiverGid, senderRemark string) error {
	return CreateFriendRelationWithTx(db.GetDB(), receiverUid, senderUid, receiverGid, senderRemark)
}

// CreateFriendRelationWithTx 使用指定事务创建双向好友关系
func CreateFriendRelationWithTx(tx *gorm.DB, receiverUid, senderUid, receiverGid, senderRemark string) error {
	// 验证接收者的分组ID是否存在且属于接收者
	if receiverGid != "" {
		var receiverGroup *entity.UserFriendGroup
		result := tx.
			Where("uid = ? AND gid = ? AND delete_time = ?", receiverUid, receiverGid, 0).
			First(&receiverGroup)
		if result.Error != nil {
			if result.Error == gorm.ErrRecordNotFound {
				log.Errorf("CreateFriendRelationWithTx 接收者分组不存在或不属于接收者: receiverUid=%s, gid=%s", receiverUid, receiverGid)
				return gorm.ErrRecordNotFound
			}
			log.Errorf("CreateFriendRelationWithTx 验证接收者分组失败: %v", result.Error)
			return result.Error
		}
	}

	// 在事务中获取发送者的默认分组
	var senderDefaultGroup *entity.UserFriendGroup
	result := tx.
		Where("uid = ? AND is_default = ? AND delete_time = ?", senderUid, true, 0).
		First(&senderDefaultGroup)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			log.Errorf("CreateFriendRelationWithTx 发送者没有默认分组: uid=%s", senderUid)
			return gorm.ErrRecordNotFound
		}
		log.Errorf("CreateFriendRelationWithTx 获取发送者默认分组失败: %v", result.Error)
		return result.Error
	}

	// 检查是否已经是好友（避免重复添加，业务只看 friend_delete_time）
	var existingFriend *entity.UserFriend
	result = tx.
		Where("uid = ? AND friend_uid = ? AND friend_delete_time = ?", receiverUid, senderUid, 0).
		First(&existingFriend)
	if result.Error == nil && existingFriend != nil {
		// 已经是好友，不需要重复创建
		log.Warnf("CreateFriendRelationWithTx 已经是好友关系: receiverUid=%s, senderUid=%s", receiverUid, senderUid)
		return nil
	}

	// 创建两条好友关系记录
	friends := []*entity.UserFriend{
		{
			Uid:              receiverUid,
			FriendUid:        senderUid,
			Gid:              receiverGid,
			Remark:           senderRemark,
			FriendDeleteTime: 0,
		},
		{
			Uid:              senderUid,
			FriendUid:        receiverUid,
			Gid:              senderDefaultGroup.Gid,
			Remark:           "",
			FriendDeleteTime: 0,
		},
	}

	result = tx.Create(&friends)
	if result.Error != nil {
		log.Errorf("CreateFriendRelationWithTx 创建好友关系失败: %v", result.Error)
		return result.Error
	}

	log.Infof("CreateFriendRelationWithTx 成功创建好友关系: receiverUid=%s, senderUid=%s", receiverUid, senderUid)
	return nil
}

// RestoreFriendRelationWithTx 恢复单方面删除的好友关系（同意好友请求时若存在旧记录则恢复并更新）
// receiverUid: 接收者（同意请求的用户）, senderUid: 发送者（发起请求的用户）
// receiverGid/receiverRemark: 接收者把发送者放入的分组与备注
func RestoreFriendRelationWithTx(tx *gorm.DB, receiverUid, senderUid, receiverGid, receiverRemark string) error {
	if receiverGid != "" {
		var receiverGroup *entity.UserFriendGroup
		result := tx.
			Where("uid = ? AND gid = ? AND delete_time = ?", receiverUid, receiverGid, 0).
			First(&receiverGroup)
		if result.Error != nil {
			if result.Error == gorm.ErrRecordNotFound {
				log.Errorf("RestoreFriendRelationWithTx 接收者分组不存在: receiverUid=%s, gid=%s", receiverUid, receiverGid)
				return gorm.ErrRecordNotFound
			}
			return result.Error
		}
	}

	var senderDefaultGroup *entity.UserFriendGroup
	result := tx.
		Where("uid = ? AND is_default = ? AND delete_time = ?", senderUid, true, 0).
		First(&senderDefaultGroup)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			log.Errorf("RestoreFriendRelationWithTx 发送者没有默认分组: uid=%s", senderUid)
			return gorm.ErrRecordNotFound
		}
		return result.Error
	}

	// 接收者 -> 发送者
	var recToSend *entity.UserFriend
	result = tx.Where("uid = ? AND friend_uid = ?", receiverUid, senderUid).First(&recToSend)
	if result.Error == nil && recToSend != nil {
		result = tx.Model(&entity.UserFriend{}).Where("id = ?", recToSend.Id).Updates(map[string]any{
			"friend_delete_time": int64(0),
			"gid":                receiverGid,
			"remark":             receiverRemark,
		})
		if result.Error != nil {
			log.Errorf("RestoreFriendRelationWithTx 更新接收者侧失败: %v", result.Error)
			return result.Error
		}
	} else {
		if err := tx.Create(&entity.UserFriend{
			Uid:              receiverUid,
			FriendUid:        senderUid,
			Gid:              receiverGid,
			Remark:           receiverRemark,
			FriendDeleteTime: 0,
		}).Error; err != nil {
			log.Errorf("RestoreFriendRelationWithTx 创建接收者侧失败: %v", err)
			return err
		}
	}

	// 发送者 -> 接收者
	var sendToRec *entity.UserFriend
	result = tx.Where("uid = ? AND friend_uid = ?", senderUid, receiverUid).First(&sendToRec)
	if result.Error == nil && sendToRec != nil {
		result = tx.Model(&entity.UserFriend{}).Where("id = ?", sendToRec.Id).Updates(map[string]any{
			"friend_delete_time": int64(0),
			"gid":                senderDefaultGroup.Gid,
			"remark":             "",
		})
		if result.Error != nil {
			log.Errorf("RestoreFriendRelationWithTx 更新发送者侧失败: %v", result.Error)
			return result.Error
		}
	} else {
		if err := tx.Create(&entity.UserFriend{
			Uid:              senderUid,
			FriendUid:        receiverUid,
			Gid:              senderDefaultGroup.Gid,
			Remark:           "",
			FriendDeleteTime: 0,
		}).Error; err != nil {
			log.Errorf("RestoreFriendRelationWithTx 创建发送者侧失败: %v", err)
			return err
		}
	}

	log.Infof("RestoreFriendRelationWithTx 已恢复好友关系: receiverUid=%s, senderUid=%s", receiverUid, senderUid)
	return nil
}
