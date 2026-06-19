package query

import (
	"errors"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	appredis "github.com/xd/quic-server/redis"
	"gorm.io/gorm"
)

func GetUserByUid(uid string) (*entity.User, error) {
	var user *entity.User
	tx := db.GetDB().
		Where("uid = ?", uid).
		Select([]string{
			"uid",
			"username",
			"nickname",
			"avatar_uf_id",
			"signature",
			"introduction",
			"email",
			"create_time",
			"allow_private_chat_from_non_friend"}).First(&user)
	if tx.Error != nil {
		log.Errorf("GetUserByUid err: %v", tx.Error)
	}
	return user, tx.Error
}

type UserStatusPublic struct {
	IsOnline          bool   `json:"is_online" msgpack:"is_online"`
	CurrentStatus     string `json:"current_status" msgpack:"current_status"`
	LastOnline        int64  `json:"last_online" msgpack:"last_online"`
	LastLogin         int64  `json:"last_login" msgpack:"last_login"`
	CustomState       string `json:"custom_state" msgpack:"custom_state"`
	Platform          string `json:"platform" msgpack:"platform"`
	DeviceType        string `json:"device_type" msgpack:"device_type"`
	DeviceModel       string `json:"device_model" msgpack:"device_model"`
	OSVersion         string `json:"os_version" msgpack:"os_version"`
	AppVersion        string `json:"app_version" msgpack:"app_version"`
	ConcurrentDevices int    `json:"concurrent_devices" msgpack:"concurrent_devices"`
}

// buildFriendVisibleStatus 返回好友可见的完整状态视图（包含自定义状态与设备维度）
func buildFriendVisibleStatus(status entity.UserCurrentStatus) *UserStatusPublic {
	return &UserStatusPublic{
		IsOnline:          status.IsOnline,
		CurrentStatus:     status.CurrentStatus,
		LastOnline:        status.LastOnline,
		LastLogin:         status.LastLogin,
		CustomState:       status.CustomState,
		Platform:          status.Platform,
		DeviceType:        status.DeviceType,
		DeviceModel:       status.DeviceModel,
		OSVersion:         status.OSVersion,
		AppVersion:        status.AppVersion,
		ConcurrentDevices: status.ConcurrentDevices,
	}
}

// buildRoomBasicPresenceStatus 返回同房间非好友可见的基础在线态（仅在线/离线与时间字段）
func buildRoomBasicPresenceStatus(status entity.UserCurrentStatus) *UserStatusPublic {
	return &UserStatusPublic{
		IsOnline:   status.IsOnline,
		LastOnline: status.LastOnline,
		LastLogin:  status.LastLogin,
	}
}

type UserWithStatus struct {
	Uid                           string            `json:"uid" msgpack:"uid"`
	Username                      string            `json:"username" msgpack:"username"`
	Nickname                      string            `json:"nickname" msgpack:"nickname"`
	Signature                     string            `json:"signature" msgpack:"signature"`
	Introduction                  string            `json:"introduction" msgpack:"introduction"`
	Email                         string            `json:"email" msgpack:"email"`
	AvatarUfId                    string            `json:"avatar_uf_id" msgpack:"avatar_uf_id"`
	CreateTime                    int64             `json:"create_time" msgpack:"create_time"`
	Status                        *UserStatusPublic `json:"status,omitempty" msgpack:"status,omitempty"`
	IsFriend                      bool              `json:"is_friend" msgpack:"is_friend"`                                                   // 当前请求用户与该用户是否为好友（仅当传入 currentUid 时有效）
	AllowPrivateChatFromNonFriend bool              `json:"allow_private_chat_from_non_friend" msgpack:"allow_private_chat_from_non_friend"` // 是否允许非好友发起私聊
	JoinRoomTime                  int64             `json:"join_room_time,omitempty" msgpack:"join_room_time,omitempty"` // 房间内资料卡：加入房间时间
	LastSpeakTime                 int64             `json:"last_speak_time,omitempty" msgpack:"last_speak_time,omitempty"` // 房间内资料卡：最后发言时间
	MuteUntil                     int64             `json:"mute_until,omitempty" msgpack:"mute_until,omitempty"` // 房间内资料卡：禁言截止时间
	MuteOperatorUid               string            `json:"mute_operator_uid,omitempty" msgpack:"mute_operator_uid,omitempty"` // 房间内资料卡：禁言操作人
	Country                       string            `json:"country,omitempty" msgpack:"country,omitempty"`
	City                          string            `json:"city,omitempty" msgpack:"city,omitempty"`
	County                        string            `json:"county,omitempty" msgpack:"county,omitempty"`
}

func GetRoomUserInfoByUidList(uidList []string, currentUid string) ([]*UserWithStatus, error) {
	var users []*entity.User
	tx := db.GetDB().Where("uid IN ?", uidList).Select([]string{
		"uid",
		"username",
		"nickname",
		"avatar_uf_id",
		"signature",
		"introduction",
		"email",
		"create_time",
		"allow_private_chat_from_non_friend",
		"country",
		"city",
		"county"}).Find(&users)

	if tx.Error != nil {
		return nil, tx.Error
	}

	// 转换为 UserWithStatus 结构
	result := make([]*UserWithStatus, 0, len(users))

	// 查询用户状态信息（从 Redis 批量查询）
	if len(users) > 0 {
		// 构建 Redis key 列表
		redisKeys := make([]string, 0, len(users))
		uidToKeyMap := make(map[string]string) // uid -> redis key 的映射
		for _, u := range users {
			key := getUserStatusRedisKey(u.Uid)
			redisKeys = append(redisKeys, key)
			uidToKeyMap[u.Uid] = key
		}

		// 从 Redis 批量查询用户状态
		redisStatusMap, err := appredis.MGet[entity.UserCurrentStatus](redisKeys)
		if err != nil {
			log.Errorf("GetRoomUserInfoByUidList 从 Redis 批量查询状态失败: %v", err)
			// Redis 查询失败，继续从数据库查询
			redisStatusMap = make(map[string]entity.UserCurrentStatus)
		}

		// 找出 Redis 中没有的用户状态，需要从数据库查询
		missingUids := make([]string, 0)
		for _, u := range users {
			key := uidToKeyMap[u.Uid]
			if _, exists := redisStatusMap[key]; !exists {
				missingUids = append(missingUids, u.Uid)
			}
		}

		// 从数据库查询缺失的状态并回写 Redis
		if len(missingUids) > 0 {
			var statusList []*entity.UserCurrentStatus
			tx = db.GetDB().Where("uid IN ?", missingUids).Select([]string{
				"uid",
				"is_online",
				"current_status",
				"last_online",
				"last_login",
				"custom_state",
				"platform",
				"device_type",
				"device_model",
				"os_version",
				"app_version",
				"concurrent_devices",
			}).Find(&statusList)

			if tx.Error != nil {
				log.Errorf("GetRoomUserInfoByUidList 从数据库查询状态失败: %v", tx.Error)
			} else {
				// 将数据库查询到的状态批量回写 Redis
				statusToCache := make(map[string]entity.UserCurrentStatus, len(statusList))
				for _, status := range statusList {
					key := getUserStatusRedisKey(status.Uid)
					statusToCache[key] = *status
					redisStatusMap[key] = *status
				}
				if err := appredis.MSetJSON(statusToCache, 0); err != nil {
					log.Errorf("GetRoomUserInfoByUidList 批量回写状态到 Redis 失败: %v", err)
				}
			}
		}

		// 先计算关系，再按关系构建状态可见性
		areFriendsMap := make(map[string]bool, len(users))
		shareRoomMap := make(map[string]bool, len(users))
		if currentUid != "" {
			friendUidList := make([]string, 0, len(users))
			for _, u := range users {
				if u.Uid == currentUid {
					areFriendsMap[u.Uid] = true
					continue
				}
				friendUidList = append(friendUidList, u.Uid)
			}
			batchFriendMap, err := CheckFriendRelationBatch(currentUid, friendUidList)
			if err != nil {
				log.Errorf("GetRoomUserInfoByUidList 批量检查好友关系失败 uid=%s: %v", currentUid, err)
			}
			for _, friendUid := range friendUidList {
				areFriendsMap[friendUid] = batchFriendMap[friendUid]
			}
			shareRoomBatch, err := CheckShareRoomBatch(currentUid, friendUidList)
			if err != nil {
				log.Errorf("GetRoomUserInfoByUidList 批量检查同房间关系失败 uid=%s: %v", currentUid, err)
			} else {
				shareRoomMap = shareRoomBatch
			}
		}

		// 构建状态映射（转换为 UserStatusPublic）
		statusMap := make(map[string]*UserStatusPublic)
		for _, user := range users {
			key := uidToKeyMap[user.Uid]
			if status, exists := redisStatusMap[key]; exists {
				switch {
				case currentUid == "" || areFriendsMap[user.Uid]:
					statusMap[user.Uid] = buildFriendVisibleStatus(status)
				case shareRoomMap[user.Uid]:
					statusMap[user.Uid] = buildRoomBasicPresenceStatus(status)
				}
			}
		}

		// 构建返回结果
		for _, user := range users {
			userWithStatus := &UserWithStatus{
				Uid:                           user.Uid,
				Username:                      user.Username,
				Nickname:                      user.Nickname,
				Signature:                     user.Signature,
				Introduction:                  user.Introduction,
				Email:                         user.Email,
				AvatarUfId:                    user.AvatarUfId,
				CreateTime:                    user.CreateTime,
				AllowPrivateChatFromNonFriend: user.AllowPrivateChatFromNonFriend,
				Country:                       user.Country,
				City:                          user.City,
				County:                        user.County,
			}
			if status, exists := statusMap[user.Uid]; exists {
				userWithStatus.Status = status
			}
			if currentUid != "" && currentUid != user.Uid {
				userWithStatus.IsFriend = areFriendsMap[user.Uid]
			}
			result = append(result, userWithStatus)
		}
	}

	return result, nil
}

// GetCardUserInfoByUid 查询用户卡片信息（按当前用户权限裁剪状态字段）。
// rid 非空时附带该房间内目标的加入时间与最后发言时间（要求查看者与目标均为活跃成员）。
func GetCardUserInfoByUid(targetUid string, currentUid string, rid string) (*UserWithStatus, error) {
	users, err := GetRoomUserInfoByUidList([]string{targetUid}, currentUid)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, nil
	}
	user := users[0]
	if rid == "" {
		return user, nil
	}
	if currentUid == "" {
		return nil, errors.New("rid requires login")
	}
	if _, err := GetRoomUser(rid, currentUid); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("viewer not in room")
		}
		return nil, err
	}
	targetRu, err := GetRoomUser(rid, targetUid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("target not in room")
		}
		return nil, err
	}
	user.JoinRoomTime = targetRu.JoinRoomTime
	user.LastSpeakTime = targetRu.LastSpeakTime
	if targetRu.MuteUntil > time.Now().UnixMilli() {
		user.MuteUntil = targetRu.MuteUntil
		user.MuteOperatorUid = targetRu.MuteOperatorUid
	}
	return user, nil
}
func IsUsernameExist(username string) bool {
	var user *entity.User
	db.GetDB().Where("username = ?", username).Select("uid").First(&user)
	return user != nil && user.Uid != ""
}

func GetUserByUsername(username string) (*entity.User, error) {
	var user *entity.User
	tx := db.GetDB().
		Where("username = ?", username).
		Select([]string{
			"uid",
			"username",
			"nickname",
			"password",
			"avatar_uf_id",
			"signature",
			"introduction",
			"email",
			"create_time"}).First(&user)
	return user, tx.Error
}

// SearchUsers 搜索用户（根据用户名、昵称、邮箱）
// currentUid: 当前用户；用于排除自己并标注 is_friend（不返回在线状态）
func SearchUsers(keyword string, limit int, offset int, currentUid string) ([]*UserWithStatus, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	var users []*entity.User
	where := db.GetDB().Model(&entity.User{}).
		Where("delete_time = ?", 0)

	// 排除当前用户自己
	if currentUid != "" {
		where = where.Where("uid != ?", currentUid)
	}

	if keyword != "" {
		where = where.Where("username LIKE ? OR nickname LIKE ? OR email LIKE ?", "%"+keyword+"%", "%"+keyword+"%", "%"+keyword+"%")
	}
	tx := where.Select([]string{
		"uid",
		"username",
		"nickname",
		"avatar_uf_id",
		"signature",
		"introduction",
		"email",
		"create_time"}).
		Order("create_time DESC, uid DESC").
		Offset(offset).
		Limit(limit).
		Find(&users)

	if tx.Error != nil {
		return nil, tx.Error
	}

	friendMap := make(map[string]bool)
	if currentUid != "" && len(users) > 0 {
		uids := make([]string, 0, len(users))
		for _, u := range users {
			uids = append(uids, u.Uid)
		}
		batch, err := CheckFriendRelationBatch(currentUid, uids)
		if err != nil {
			log.Errorf("SearchUsers 批量检查好友关系失败 uid=%s: %v", currentUid, err)
		} else {
			friendMap = batch
		}
	}

	// 转换为 UserWithStatus 结构（不包含在线状态）
	result := make([]*UserWithStatus, 0, len(users))
	for _, user := range users {
		userWithStatus := &UserWithStatus{
			Uid:          user.Uid,
			Username:     user.Username,
			Nickname:     user.Nickname,
			Signature:    user.Signature,
			Introduction: user.Introduction,
			Email:        user.Email,
			AvatarUfId:   user.AvatarUfId,
			CreateTime:   user.CreateTime,
			IsFriend:     friendMap[user.Uid],
		}
		result = append(result, userWithStatus)
	}

	return result, nil
}

// BuildLoginUserWithStatus 登录成功响应用：资料来自用户表，好友可见的完整在线态（含 custom_state、current_status、设备等）来自 Redis/DB，与 QUIC UserStatusSync 语义一致，便于客户端首次注入完整当前用户快照。
func BuildLoginUserWithStatus(u *entity.User) *UserWithStatus {
	out := &UserWithStatus{
		Uid:                           u.Uid,
		Username:                      u.Username,
		Nickname:                      u.Nickname,
		Signature:                     u.Signature,
		Introduction:                  u.Introduction,
		Email:                         u.Email,
		AvatarUfId:                    u.AvatarUfId,
		CreateTime:                    u.CreateTime,
		IsFriend:                      false,
		AllowPrivateChatFromNonFriend: u.AllowPrivateChatFromNonFriend,
	}
	st, err := GetOrCreateUserCurrentStatus(u.Uid)
	if err != nil {
		log.Warnf("BuildLoginUserWithStatus GetOrCreateUserCurrentStatus uid=%s: %v", u.Uid, err)
		return out
	}
	out.Status = buildFriendVisibleStatus(*st)
	return out
}
