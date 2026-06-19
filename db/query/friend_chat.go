// 本文件提供好友私聊、群私聊、非好友私聊、会话创建与删除等逻辑。
// 建房统一走 createRoomWithMuteAndUsers，避免重复「房间 + 禁言配置 + room_user」的创建顺序。
package query

import (
	"errors"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/types"
	"gorm.io/gorm"
)

// createRoomWithMuteAndUsers 在事务中创建房间、禁言配置与 room_user 成员，保证顺序与幂等。
// 职责：仅做 DB 写入，调用方负责构造 room 与 uids。用于私聊/群私聊等所有二人房间创建。
func createRoomWithMuteAndUsers(tx *gorm.DB, room *types.Room, uids []string) error {
	if err := tx.Create(room).Error; err != nil {
		return err
	}
	if err := tx.Create(&entity.RoomMuteConfig{Rid: room.Rid}).Error; err != nil {
		return err
	}
	roomUsers := make([]*types.RoomUser, 0, len(uids))
	for _, uid := range uids {
		roomUsers = append(roomUsers, &types.RoomUser{Rid: room.Rid, Uid: uid})
	}
	return tx.Create(&roomUsers).Error
}

// createPrivateRoomWithConfig 创建私聊房间（含 AllowNonFriendChat）+ 禁言配置 + room_user。
func createPrivateRoomWithConfig(tx *gorm.DB, uids []string, allowNonFriendChat bool) (*types.Room, error) {
	if len(uids) != 2 {
		return nil, errors.New("private room must have exactly 2 members")
	}
	room := &types.Room{
		Name:               "",
		Description:        "",
		AvatarUfId:         "",
		CreateUid:          "system",
		Type:               types.PrivateRoom,
		MemberCount:        len(uids),
		State:              1,
		AllowNonFriendChat: allowNonFriendChat,
	}
	if err := createRoomWithMuteAndUsers(tx, room, uids); err != nil {
		return nil, err
	}
	return room, nil
}

// CreatePrivateRoomWithConfigTx 对外封装：在调用方事务中创建私聊房间 + 禁言配置 + room_user。
func CreatePrivateRoomWithConfigTx(tx *gorm.DB, uids []string, allowNonFriendChat bool) (*types.Room, error) {
	return createPrivateRoomWithConfig(tx, uids, allowNonFriendChat)
}

// createGroupPrivateRoomWithConfig 创建群私聊房间（type=2，带 from_room_rid）+ 禁言配置 + room_user。
func createGroupPrivateRoomWithConfig(tx *gorm.DB, uids []string, fromRid string) (*types.Room, error) {
	if len(uids) != 2 {
		return nil, errors.New("group private room must have exactly 2 members")
	}
	if fromRid == "" {
		return nil, errors.New("from_room_rid is required for group private room")
	}
	room := &types.Room{
		Name:               "",
		Description:        "",
		AvatarUfId:         "",
		CreateUid:          "system",
		Type:               entity.RoomTypeGroupPrivate,
		MemberCount:        len(uids),
		State:              1,
		AllowNonFriendChat: true,
		FromRoomRid:        fromRid,
	}
	if err := createRoomWithMuteAndUsers(tx, room, uids); err != nil {
		return nil, err
	}
	return room, nil
}

// ---------- 房间查找 ----------
// FindGroupPrivateRoomBetweenUsers 查找两人在指定群下已存在的群私聊房间。
func FindGroupPrivateRoomBetweenUsers(uid, targetUid, fromRid string) (*types.Room, error) {
	if fromRid == "" {
		return nil, nil
	}
	var room types.Room
	tx := db.GetDB().
		Table("room AS r").
		Joins("INNER JOIN room_user AS ru1 ON ru1.rid = r.rid AND ru1.uid = ? AND ru1.delete_time = 0", uid).
		Joins("INNER JOIN room_user AS ru2 ON ru2.rid = r.rid AND ru2.uid = ? AND ru2.delete_time = 0", targetUid).
		Where("r.type = ?", entity.RoomTypeGroupPrivate).
		Where("r.from_room_rid = ?", fromRid).
		Where("r.member_count = ?", 2).
		Where("r.state = ?", 1).
		Where("r.delete_time = 0").
		First(&room)
	if tx.Error != nil {
		if errors.Is(tx.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		log.Errorf("FindGroupPrivateRoomBetweenUsers uid=%s target=%s from_rid=%s: %v", uid, targetUid, fromRid, tx.Error)
		return nil, tx.Error
	}
	return &room, nil
}

// ---------- 打开私聊 / 群私聊 ----------
// OpenGroupPrivateChat 从群内打开与某成员的聊天：已是好友则直接使用好友私聊房间，否则查找或创建群私聊(type=2)。
func OpenGroupPrivateChat(uid, targetUid, fromRid string) (*UserRoomSessionDto, error) {
	if uid == "" || targetUid == "" || fromRid == "" || uid == targetUid {
		return nil, errors.New("invalid uid, target_uid or from_rid")
	}
	// 已是好友则直接使用好友私聊房间，不创建群私聊
	if ensureFriendRelation(uid, targetUid) == nil {
		return EnsurePrivateChatSession(uid, targetUid)
	}
	room, err := FindGroupPrivateRoomBetweenUsers(uid, targetUid, fromRid)
	if err != nil {
		return nil, err
	}
	if room == nil {
		tx := db.GetDB().Begin()
		if tx.Error != nil {
			return nil, tx.Error
		}
		var createErr error
		room, createErr = createGroupPrivateRoomWithConfig(tx, []string{uid, targetUid}, fromRid)
		if createErr != nil {
			tx.Rollback()
			log.Errorf("OpenGroupPrivateChat 创建群私聊失败 uid=%s target=%s from_rid=%s: %v", uid, targetUid, fromRid, createErr)
			return nil, createErr
		}
		if err := tx.Commit().Error; err != nil {
			return nil, err
		}
	}
	session, err := ensureSessionForUserInPrivateRoom(uid, room.Rid)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// TryUpgradeGroupPrivateRoomToPrivateTx 若 rid 对应房间是群私聊且仅含 uid1、uid2 两人，则改为普通私聊并返回该房间；否则返回 (nil, false)。
// 用于添加好友时传入已有群私聊 rid，复用该房间为好友私聊。
func TryUpgradeGroupPrivateRoomToPrivateTx(tx *gorm.DB, rid, uid1, uid2 string) (*types.Room, bool, error) {
	if rid == "" || uid1 == "" || uid2 == "" || uid1 == uid2 {
		return nil, false, nil
	}
	var room types.Room
	if err := tx.Model(&entity.Room{}).Where("rid = ? AND delete_time = 0 AND state = 1", rid).First(&room).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if room.Type != entity.RoomTypeGroupPrivate || room.MemberCount != 2 {
		return nil, false, nil
	}
	var uids []string
	if err := tx.Model(&entity.RoomUser{}).Where("rid = ? AND delete_time = 0", rid).Pluck("uid", &uids).Error; err != nil {
		return nil, false, err
	}
	if len(uids) != 2 {
		return nil, false, nil
	}
	ok := (uids[0] == uid1 && uids[1] == uid2) || (uids[0] == uid2 && uids[1] == uid1)
	if !ok {
		return nil, false, nil
	}
	if err := tx.Model(&entity.Room{}).Where("rid = ?", rid).
		Updates(map[string]any{"type": entity.RoomTypePrivate, "from_room_rid": ""}).Error; err != nil {
		return nil, false, err
	}
	room.Type = entity.RoomTypePrivate
	room.FromRoomRid = ""
	log.Infof("TryUpgradeGroupPrivateRoomToPrivateTx 已将群私聊 rid=%s 升级为私聊 uid1=%s uid2=%s", rid, uid1, uid2)
	return &room, true, nil
}

// UpgradeGroupPrivateToPrivate 将两人之间所有群私聊房间改为普通私聊（互加好友后调用）。
func UpgradeGroupPrivateToPrivate(uid, friendUid string) error {
	var rids []string
	err := db.GetDB().Model(&entity.Room{}).
		Select("room.rid").
		Joins("INNER JOIN room_user AS ru1 ON ru1.rid = room.rid AND ru1.uid = ? AND ru1.delete_time = 0", uid).
		Joins("INNER JOIN room_user AS ru2 ON ru2.rid = room.rid AND ru2.uid = ? AND ru2.delete_time = 0", friendUid).
		Where("room.type = ?", entity.RoomTypeGroupPrivate).
		Where("room.member_count = ?", 2).
		Pluck("room.rid", &rids).Error
	if err != nil {
		log.Errorf("UpgradeGroupPrivateToPrivate 查询群私聊房间失败 uid=%s friend_uid=%s: %v", uid, friendUid, err)
		return err
	}
	if len(rids) == 0 {
		return nil
	}
	res := db.GetDB().Model(&entity.Room{}).
		Where("rid IN ?", rids).
		Updates(map[string]any{"type": entity.RoomTypePrivate, "from_room_rid": ""})
	if res.Error != nil {
		log.Errorf("UpgradeGroupPrivateToPrivate 更新失败 uid=%s friend_uid=%s: %v", uid, friendUid, res.Error)
		return res.Error
	}
	log.Infof("UpgradeGroupPrivateToPrivate 已升级 %d 个群私聊为私聊 uid=%s friend_uid=%s", res.RowsAffected, uid, friendUid)
	return nil
}

var (
	// ErrNotFriend 当前用户与目标用户不是好友关系
	ErrNotFriend = errors.New("not friend")
	// ErrPrivateChatNotAllowed 对方未开启非好友私聊
	ErrPrivateChatNotAllowed = errors.New("private chat not allowed")
)

// ensureFriendRelation 确认 uid 与 friendUid 仍然存在有效的好友关系
func ensureFriendRelation(uid, friendUid string) error {
	var friend *entity.UserFriend
	tx := db.GetDB().
		Where("uid = ? AND friend_uid = ? AND friend_delete_time = 0", uid, friendUid).
		First(&friend)
	if tx.Error != nil {
		if errors.Is(tx.Error, gorm.ErrRecordNotFound) {
			return ErrNotFriend
		}
		log.Errorf("ensureFriendRelation 查询好友关系失败 uid=%s friend_uid=%s: %v", uid, friendUid, tx.Error)
		return tx.Error
	}
	return nil
}

// DeleteFriend 删除好友关系（单向删除）：
// 仅将当前用户一侧的 friend_delete_time 置为当前时间，另一侧保持不变。
// 若当前用户与对方不存在有效的好友记录，则返回 ErrNotFriend。
func DeleteFriend(uid, friendUid string) error {
	return DeleteFriendWithTx(db.GetDB(), uid, friendUid)
}

// DeleteFriendWithTx 使用指定事务删除好友关系（单向删除）
func DeleteFriendWithTx(tx *gorm.DB, uid, friendUid string) error {
	now := time.Now().UnixMilli()
	result := tx.Model(&entity.UserFriend{}).
		Where("uid = ? AND friend_uid = ? AND friend_delete_time = 0", uid, friendUid).
		Updates(map[string]any{
			"friend_delete_time": now,
		})
	if result.Error != nil {
		log.Errorf("DeleteFriendWithTx 删除好友失败 uid=%s friend_uid=%s: %v", uid, friendUid, result.Error)
		return result.Error
	}
	if result.RowsAffected == 0 {
		// 当前用户与对方不存在有效好友关系
		return ErrNotFriend
	}
	log.Infof("DeleteFriendWithTx 好友已删除 uid=%s friend_uid=%s", uid, friendUid)
	return nil
}

// FindPrivateRoomBetweenUsers 查找两个人之间已存在的私聊房间（公开函数）
func FindPrivateRoomBetweenUsers(uid, friendUid string) (*types.Room, error) {
	var room *types.Room
	tx := db.GetDB().
		Table("room AS r").
		Joins("INNER JOIN room_user AS ru1 ON ru1.rid = r.rid AND ru1.uid = ? AND ru1.delete_time = 0", uid).
		Joins("INNER JOIN room_user AS ru2 ON ru2.rid = r.rid AND ru2.uid = ? AND ru2.delete_time = 0", friendUid).
		Where("r.type = ?", types.PrivateRoom).
		Where("r.member_count = ?", 2).
		Where("r.create_uid = ?", "system").
		Where("r.state = ?", 1).
		Where("r.delete_time = 0").
		First(&room)

	if tx.Error != nil {
		if errors.Is(tx.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		log.Errorf("findPrivateRoomBetweenUsers 查询私聊房间失败 uid=%s friend_uid=%s: %v", uid, friendUid, tx.Error)
		return nil, tx.Error
	}
	return room, nil
}

// ---------- 好友私聊会话 ----------
// EnsurePrivateChatSession 确认或创建与好友的私聊会话
// 1. 校验双方仍然是好友
// 2. 查找或创建私聊房间（system 创建，member_count=2）并补全 room_user 关系
// 3. 为当前用户创建/恢复 user_room_session，并置顶该会话
// 返回用于打开聊天窗口的会话信息
func EnsurePrivateChatSession(uid, friendUid string) (*UserRoomSessionDto, error) {
	// 先确认仍然是好友
	if err := ensureFriendRelation(uid, friendUid); err != nil {
		return nil, err
	}

	tx := db.GetDB().Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}

	// 查询是否已有私聊房间
	room, err := FindPrivateRoomBetweenUsers(uid, friendUid)
	if err != nil {
		tx.Rollback()
		return nil, err
	}

	// 如果不存在则创建（好友私聊房间，不允许非好友私聊）
	if room == nil {
		var createErr error
		room, createErr = createPrivateRoomWithConfig(tx, []string{uid, friendUid}, false)
		if createErr != nil {
			tx.Rollback()
			log.Errorf("EnsurePrivateChatSession 创建私聊房间失败 uid=%s friend_uid=%s: %v", uid, friendUid, createErr)
			return nil, createErr
		}
	}

	// 置顶当前用户的该会话：先取消其它会话置顶
	if err := tx.Model(&types.UserRoomSession{}).
		Where("uid = ? AND delete_time = 0", uid).
		Update("is_top", false).Error; err != nil {
		tx.Rollback()
		log.Errorf("EnsurePrivateChatSession 取消旧会话置顶失败 uid=%s: %v", uid, err)
		return nil, err
	}

	// 创建或更新当前用户的房间会话
	var userSession *types.UserRoomSession
	if err := tx.
		Where("uid = ? AND rid = ? AND delete_time = 0", uid, room.Rid).
		First(&userSession).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 不存在则创建
			userSession = &types.UserRoomSession{
				Uid:   uid,
				Rid:   room.Rid,
				State: 1,
			}
			if err := tx.Create(userSession).Error; err != nil {
				tx.Rollback()
				log.Errorf("EnsurePrivateChatSession 创建 UserRoomSession 失败 uid=%s rid=%s: %v", uid, room.Rid, err)
				return nil, err
			}
		} else {
			tx.Rollback()
			log.Errorf("EnsurePrivateChatSession 查询 UserRoomSession 失败 uid=%s rid=%s: %v", uid, room.Rid, err)
			return nil, err
		}
	} else {
		// 已存在则更新为激活
		if err := tx.Model(&types.UserRoomSession{}).
			Where("id = ?", userSession.Id).
			Updates(map[string]any{
				"state":       1,
				"update_time": time.Now().UnixMilli(),
			}).Error; err != nil {
			tx.Rollback()
			log.Errorf("EnsurePrivateChatSession 更新 UserRoomSession 失败 uid=%s rid=%s: %v", uid, room.Rid, err)
			return nil, err
		}
	}

	if err := tx.Commit().Error; err != nil {
		log.Errorf("EnsurePrivateChatSession 事务提交失败 uid=%s friend_uid=%s: %v", uid, friendUid, err)
		return nil, err
	}

	// 返回当前用户的会话信息，供前端打开聊天窗口
	session, err := GetUserRoomSessionDtoByRid(uid, room.Rid)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// ensureSessionForUserInPrivateRoom 确保当前用户在指定私聊房间有会话并置顶，返回会话 DTO。
// 不校验好友关系、不创建房间，仅做会话的创建/置顶。
func ensureSessionForUserInPrivateRoom(uid, rid string) (*UserRoomSessionDto, error) {
	tx := db.GetDB().Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	if err := tx.Model(&types.UserRoomSession{}).
		Where("uid = ? AND delete_time = 0", uid).
		Update("is_top", false).Error; err != nil {
		tx.Rollback()
		return nil, err
	}
	var userSession *types.UserRoomSession
	if err := tx.Where("uid = ? AND rid = ? AND delete_time = 0", uid, rid).First(&userSession).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			userSession = &types.UserRoomSession{Uid: uid, Rid: rid, State: 1}
			if err := tx.Create(userSession).Error; err != nil {
				tx.Rollback()
				return nil, err
			}
		} else {
			tx.Rollback()
			return nil, err
		}
	} else {
		if err := tx.Model(&types.UserRoomSession{}).Where("id = ?", userSession.Id).
			Updates(map[string]any{
				"state":       1,
				"update_time": time.Now().UnixMilli(),
			}).Error; err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return GetUserRoomSessionDtoByRid(uid, rid)
}

// ---------- 自聊（与自己对话） ----------
// FindSelfChatRoom 查找当前用户的自聊房间（type=3，仅自己一人）
func FindSelfChatRoom(uid string) (*types.Room, error) {
	var room types.Room
	err := db.GetDB().
		Table("room AS r").
		Joins("INNER JOIN room_user AS ru ON ru.rid = r.rid AND ru.uid = ? AND ru.delete_time = 0", uid).
		Where("r.type = ?", entity.RoomTypeSelfChat).
		Where("r.create_uid = ?", uid).
		Where("r.member_count = ?", 1).
		Where("r.delete_time = 0").
		Where("r.state = 1").
		First(&room).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &room, nil
}

// EnsureSelfChatSession 获取或创建自聊房间并返回当前用户的会话（用于「向自己发消息」独立房间类型）
func EnsureSelfChatSession(uid string) (*UserRoomSessionDto, error) {
	room, err := FindSelfChatRoom(uid)
	if err != nil {
		return nil, err
	}
	if room == nil {
		tx := db.GetDB().Begin()
		if tx.Error != nil {
			return nil, tx.Error
		}
		room = &types.Room{
			Name:        "我的笔记",
			Description: "",
			AvatarUfId:  "",
			CreateUid:   uid,
			Type:        entity.RoomTypeSelfChat,
			MemberCount: 1,
			State:       1,
		}
		if err := createRoomWithMuteAndUsers(tx, room, []string{uid}); err != nil {
			tx.Rollback()
			log.Errorf("EnsureSelfChatSession 创建自聊房间失败 uid=%s: %v", uid, err)
			return nil, err
		}
		if err := tx.Commit().Error; err != nil {
			return nil, err
		}
	}
	// 置顶并返回会话
	return ensureSessionForUserInPrivateRoom(uid, room.Rid)
}

// ---------- 非好友私聊 ----------
// OpenPrivateChat 打开与目标用户的私聊：好友走 EnsurePrivateChatSession；非好友则校验 AllowPrivateChatFromNonFriend 后创建/复用房间。
// 返回 (session, createdForNonFriend, error)。
func OpenPrivateChat(uid, targetUid string) (*UserRoomSessionDto, bool, error) {
	if uid == "" || targetUid == "" || uid == targetUid {
		return nil, false, errors.New("invalid uid or target_uid")
	}
	// 是好友：直接走好友私聊
	errRel := ensureFriendRelation(uid, targetUid)
	if errRel == nil {
		session, err := EnsurePrivateChatSession(uid, targetUid)
		if err != nil {
			return nil, false, err
		}
		return session, false, nil
	}
	if !errors.Is(errRel, ErrNotFriend) {
		return nil, false, errRel
	}
	// 非好友：查询对方是否允许非好友私聊
	targetUser, err := GetUserByUid(targetUid)
	if err != nil || targetUser == nil {
		return nil, false, ErrPrivateChatNotAllowed
	}
	if !targetUser.AllowPrivateChatFromNonFriend {
		return nil, false, ErrPrivateChatNotAllowed
	}
	room, err := FindPrivateRoomBetweenUsers(uid, targetUid)
	if err != nil {
		return nil, false, err
	}
	if room == nil {
		tx := db.GetDB().Begin()
		if tx.Error != nil {
			return nil, false, tx.Error
		}
		var createErr error
		room, createErr = createPrivateRoomWithConfig(tx, []string{uid, targetUid}, true)
		if createErr != nil {
			tx.Rollback()
			return nil, false, createErr
		}
		if err := tx.Commit().Error; err != nil {
			return nil, false, err
		}
	}
	// 若已有房间但非 AllowNonFriendChat（例如之前是好友后删了好友），不允许通过非好友入口进入
	if !room.AllowNonFriendChat {
		return nil, false, ErrPrivateChatNotAllowed
	}
	session, err := ensureSessionForUserInPrivateRoom(uid, room.Rid)
	if err != nil {
		return nil, false, err
	}
	return session, true, nil
}

// ---------- 接收方会话（发消息时） ----------
// EnsureReceiverSessionForPrivateRoom 确保接收方有私聊会话：二人私聊/群私聊且接收方无 session 时创建并置顶。
func EnsureReceiverSessionForPrivateRoom(rid string, senderUid string) error {
	// 查询房间信息
	var room *types.Room
	if err := db.GetDB().
		Where("rid = ? AND delete_time = 0 AND state = 1", rid).
		First(&room).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // 房间不存在，忽略
		}
		log.Errorf("EnsureReceiverSessionForPrivateRoom 查询房间失败 rid=%s: %v", rid, err)
		return err
	}

	// 只处理私聊或群私聊（2人房间）
	if room.MemberCount != 2 || (room.Type != types.PrivateRoom && room.Type != entity.RoomTypeGroupPrivate) {
		return nil // 不是二人私聊/群私聊，不需要处理
	}

	// 获取房间内的所有用户
	userIds, err := GetRoomUserIdsCache(rid)
	if err != nil {
		log.Errorf("EnsureReceiverSessionForPrivateRoom 获取房间用户列表失败 rid=%s: %v", rid, err)
		return err
	}

	// 找到接收方（非发送者）
	var receiverUid string
	for _, uid := range userIds {
		if uid != senderUid {
			receiverUid = uid
			break
		}
	}

	if receiverUid == "" {
		log.Warnf("EnsureReceiverSessionForPrivateRoom 未找到接收方 rid=%s sender_uid=%s", rid, senderUid)
		return nil
	}

	tx := db.GetDB().Begin()
	if tx.Error != nil {
		return tx.Error
	}

	// 检查接收方是否已有session
	var receiverSession *types.UserRoomSession
	err = tx.
		Where("uid = ? AND rid = ? AND delete_time = 0", receiverUid, rid).
		First(&receiverSession).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 接收方没有session，创建并激活
			// 先取消接收方的其他会话激活
			if err := tx.Model(&types.UserRoomSession{}).
				Where("uid = ? AND delete_time = 0", receiverUid).
				Update("state", 1).Error; err != nil {
				tx.Rollback()
				log.Errorf("EnsureReceiverSessionForPrivateRoom 更新旧会话状态失败 uid=%s: %v", receiverUid, err)
				return err
			}

			// 创建新session并激活
			receiverSession = &types.UserRoomSession{
				Uid:   receiverUid,
				Rid:   rid,
				State: 1,
			}
			if err := tx.Create(receiverSession).Error; err != nil {
				tx.Rollback()
				log.Errorf("EnsureReceiverSessionForPrivateRoom 创建接收方 UserRoomSession 失败 uid=%s rid=%s: %v", receiverUid, rid, err)
				return err
			}
			log.Infof("EnsureReceiverSessionForPrivateRoom 为接收方创建并置顶session: uid=%s rid=%s", receiverUid, rid)
		} else {
			tx.Rollback()
			log.Errorf("EnsureReceiverSessionForPrivateRoom 查询接收方 UserRoomSession 失败 uid=%s rid=%s: %v", receiverUid, rid, err)
			return err
		}
	} else {
		// 接收方已有session，更新为激活（如果还未激活）
		if receiverSession.State != 1 {
			if err := tx.Model(&types.UserRoomSession{}).
				Where("id = ?", receiverSession.Id).
				Updates(map[string]any{
					"state": 1,
				}).Error; err != nil {
				tx.Rollback()
				log.Errorf("EnsureReceiverSessionForPrivateRoom 更新接收方 UserRoomSession 置顶失败 uid=%s rid=%s: %v", receiverUid, rid, err)
				return err
			}
			log.Infof("EnsureReceiverSessionForPrivateRoom 更新接收方session为置顶: uid=%s rid=%s", receiverUid, rid)
		}
	}

	if err := tx.Commit().Error; err != nil {
		log.Errorf("EnsureReceiverSessionForPrivateRoom 事务提交失败 rid=%s receiver_uid=%s: %v", rid, receiverUid, err)
		return err
	}

	return nil
}
