package query

import (
	"encoding/json"

	"github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	appredis "github.com/xd/quic-server/redis"
	"github.com/xd/quic-server/utils"
	"gorm.io/gorm"
)

// CheckFriendRelation 检查两人是否为有效好友关系，或是否存在单方面/双方删除或单向关系
//
// 业务只看 friend_delete_time，不参与 delete_time。
// 规则：
// - 双向都存在记录，且双方 friend_delete_time=0 => areFriends=true
// - 任意一条 friend_delete_time!=0，或仅存在单向记录 => oneSideDeleted=true（含双方都删除）
// - 两条都不存在 => areFriends=false 且 oneSideDeleted=false
func CheckFriendRelation(uid, friendUid string) (areFriends bool, oneSideDeleted bool, err error) {
	if uid == "" || friendUid == "" || uid == friendUid {
		return false, false, nil
	}

	type row struct {
		Uid              string `gorm:"column:uid"`
		FriendUid        string `gorm:"column:friend_uid"`
		FriendDeleteTime int64  `gorm:"column:friend_delete_time"`
	}

	var rows []*row
	tx := db.GetDB().
		Table("user_friend").
		Select([]string{"uid", "friend_uid", "friend_delete_time"}).
		Where("(uid = ? AND friend_uid = ?) OR (uid = ? AND friend_uid = ?)", uid, friendUid, friendUid, uid).
		Find(&rows)
	if tx.Error != nil {
		logrus.Errorf("CheckFriendRelation err uid=%s friend_uid=%s: %v", uid, friendUid, tx.Error)
		return false, false, tx.Error
	}

	var (
		existsAB bool
		existsBA bool
		delAB    bool
		delBA    bool
	)
	for _, r := range rows {
		if r == nil {
			continue
		}
		if r.Uid == uid && r.FriendUid == friendUid {
			existsAB = true
			delAB = r.FriendDeleteTime != 0
		} else if r.Uid == friendUid && r.FriendUid == uid {
			existsBA = true
			delBA = r.FriendDeleteTime != 0
		}
	}

	// 互为好友：双向存在且双方都未删除
	if existsAB && existsBA && !delAB && !delBA {
		return true, false, nil
	}

	// 单方面删除/单向关系：任意一侧存在记录但状态不完整
	if existsAB || existsBA {
		if (existsAB && delAB) || (existsBA && delBA) || (existsAB != existsBA) {
			return false, true, nil
		}
	}

	return false, false, nil
}

// CheckFriendRelationBatch 批量检查当前用户与多个用户的好友关系，返回 friend_uid -> areFriends。
func CheckFriendRelationBatch(uid string, friendUids []string) (map[string]bool, error) {
	result := make(map[string]bool, len(friendUids))
	if uid == "" || len(friendUids) == 0 {
		return result, nil
	}

	uniqFriendUids := make([]string, 0, len(friendUids))
	seen := make(map[string]struct{}, len(friendUids))
	for _, friendUid := range friendUids {
		if friendUid == "" || friendUid == uid {
			result[friendUid] = friendUid == uid
			continue
		}
		if _, ok := seen[friendUid]; ok {
			continue
		}
		seen[friendUid] = struct{}{}
		uniqFriendUids = append(uniqFriendUids, friendUid)
	}
	if len(uniqFriendUids) == 0 {
		return result, nil
	}

	type row struct {
		Uid              string `gorm:"column:uid"`
		FriendUid        string `gorm:"column:friend_uid"`
		FriendDeleteTime int64  `gorm:"column:friend_delete_time"`
	}
	var rows []*row
	tx := db.GetDB().
		Table("user_friend").
		Select([]string{"uid", "friend_uid", "friend_delete_time"}).
		Where("(uid = ? AND friend_uid IN ?) OR (uid IN ? AND friend_uid = ?)", uid, uniqFriendUids, uniqFriendUids, uid).
		Find(&rows)
	if tx.Error != nil {
		logrus.Errorf("CheckFriendRelationBatch err uid=%s: %v", uid, tx.Error)
		return result, tx.Error
	}

	existsAB := make(map[string]bool, len(uniqFriendUids))
	existsBA := make(map[string]bool, len(uniqFriendUids))
	delAB := make(map[string]bool, len(uniqFriendUids))
	delBA := make(map[string]bool, len(uniqFriendUids))
	for _, r := range rows {
		if r == nil {
			continue
		}
		if r.Uid == uid {
			fid := r.FriendUid
			existsAB[fid] = true
			delAB[fid] = r.FriendDeleteTime != 0
			continue
		}
		if r.FriendUid == uid {
			fid := r.Uid
			existsBA[fid] = true
			delBA[fid] = r.FriendDeleteTime != 0
		}
	}

	for _, friendUid := range uniqFriendUids {
		result[friendUid] = existsAB[friendUid] && existsBA[friendUid] && !delAB[friendUid] && !delBA[friendUid]
	}
	return result, nil
}

// FriendGroupWithFriends 好友分组包含好友列表
type FriendGroupWithFriends struct {
	entity.UserFriendGroup
	FriendList []*UserWithStatus `json:"friend_list" gorm:"-"` // 好友列表，不映射到数据库
}

// GetFriendGroupList 获取用户好友分组列表，包含每个分组下的好友信息
// 状态信息优先从 Redis 查询
func GetFriendGroupList(uid string) ([]*FriendGroupWithFriends, error) {
	var results []struct {
		entity.UserFriendGroup
		FriendListJSON string `gorm:"column:friend_list"`
	}

	// 先查询分组和好友基本信息（不包含状态，状态从 Redis 查询）
	tx := db.GetDB().
		Table("user_friend_group AS g").
		Select(utils.JoinField("g", ".", []string{
			"id",
			"uid",
			"gid",
			"name",
			"is_default",
			"sort",
			"description",
			"create_time",
			"update_time",
		}), `COALESCE(
			(
				SELECT json_agg(
					json_build_object(
						'uid', u.uid,
						'username', u.username,
						'nickname', u.nickname,
						'avatar_uf_id', u.avatar_uf_id,
						'signature', u.signature,
						'introduction', u.introduction,
						'email', u.email,
						'create_time', u.create_time
					)
				)
				FROM user_friend f
				JOIN "user" u ON f.friend_uid = u.uid AND u.delete_time = 0
				WHERE f.uid = g.uid
				AND f.gid = g.gid
				AND f.friend_delete_time = 0
			),
			'[]'::json
		) AS friend_list`).
		Where("g.uid = ?", uid).
		Where("g.delete_time = ?", 0).
		Group("g.id, g.uid, g.gid, g.name, g.is_default, g.sort, g.description, g.create_time, g.update_time").
		Order("g.is_default DESC, g.sort ASC, g.create_time ASC").
		Find(&results)

	if tx.Error != nil {
		logrus.Errorf("GetFriendGroupList err: %v", tx.Error)
		return nil, tx.Error
	}

	// 收集所有好友的 UID（去重），用于批量查询 Redis 状态
	allFriendUids := make([]string, 0)
	allFriendUidSet := make(map[string]struct{})
	for _, r := range results {
		if r.FriendListJSON != "" && r.FriendListJSON != "[]" {
			var friendList []*UserWithStatus
			if err := json.Unmarshal([]byte(r.FriendListJSON), &friendList); err == nil {
				for _, friend := range friendList {
					if friend == nil || friend.Uid == "" {
						continue
					}
					if _, ok := allFriendUidSet[friend.Uid]; ok {
						continue
					}
					allFriendUidSet[friend.Uid] = struct{}{}
					allFriendUids = append(allFriendUids, friend.Uid)
				}
			}
		}
	}

	// 从 Redis 批量查询用户状态
	statusMap := make(map[string]*UserStatusPublic)
	if len(allFriendUids) > 0 {
		// 构建 Redis key 列表
		redisKeys := make([]string, 0, len(allFriendUids))
		uidToKeyMap := make(map[string]string)
		for _, friendUid := range allFriendUids {
			key := getUserStatusRedisKey(friendUid)
			redisKeys = append(redisKeys, key)
			uidToKeyMap[friendUid] = key
		}

		// 从 Redis 批量查询用户状态
		redisStatusMap, err := appredis.MGet[entity.UserCurrentStatus](redisKeys)
		if err != nil {
			logrus.Errorf("GetFriendGroupList 从 Redis 批量查询状态失败: %v", err)
			redisStatusMap = make(map[string]entity.UserCurrentStatus)
		}

		// 找出 Redis 中没有的用户状态，需要从数据库查询
		missingUids := make([]string, 0)
		for _, friendUid := range allFriendUids {
			key := uidToKeyMap[friendUid]
			if _, exists := redisStatusMap[key]; !exists {
				missingUids = append(missingUids, friendUid)
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
				logrus.Errorf("GetFriendGroupList 从数据库查询状态失败: %v", tx.Error)
			} else {
				// 将数据库查询到的状态批量回写 Redis
				statusToCache := make(map[string]entity.UserCurrentStatus, len(statusList))
				for _, status := range statusList {
					key := getUserStatusRedisKey(status.Uid)
					statusToCache[key] = *status
					redisStatusMap[key] = *status
				}
				if err := appredis.MSetJSON(statusToCache, 0); err != nil {
					logrus.Errorf("GetFriendGroupList 批量回写状态到 Redis 失败: %v", err)
				}
			}
		}

		// 构建状态映射（转换为 UserStatusPublic）
		for _, friendUid := range allFriendUids {
			key := uidToKeyMap[friendUid]
			if status, exists := redisStatusMap[key]; exists {
				statusMap[friendUid] = &UserStatusPublic{
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
		}
	}

	// 转换结果并填充状态信息
	groups := make([]*FriendGroupWithFriends, 0, len(results))
	for _, r := range results {
		group := &FriendGroupWithFriends{
			UserFriendGroup: r.UserFriendGroup,
			FriendList:      []*UserWithStatus{},
		}

		// 解析好友列表 JSON
		if r.FriendListJSON != "" && r.FriendListJSON != "[]" {
			var friendList []*UserWithStatus
			if err := json.Unmarshal([]byte(r.FriendListJSON), &friendList); err != nil {
				logrus.Errorf("解析好友列表 JSON 失败: %v, json: %s", err, r.FriendListJSON)
			} else {
				// 为每个好友填充状态信息（必须尽最大努力保证返回最新 status，避免客户端点详情用旧对象覆盖缓存）
				for _, friend := range friendList {
					if status, exists := statusMap[friend.Uid]; exists {
						friend.Status = status
						continue
					}

					// 兜底：Redis/MGet 反序列化失败或缺失时，按 uid 单查并回写 Redis，确保 status 不为空且为最新
					st, err := GetOrCreateUserCurrentStatus(friend.Uid)
					if err != nil {
						logrus.Errorf("GetFriendGroupList GetOrCreateUserCurrentStatus 失败 uid=%s: %v", friend.Uid, err)
						continue
					}
					// 好友列表只包含好友，直接返回好友可见完整状态
					friend.Status = &UserStatusPublic{
						IsOnline:          st.IsOnline,
						CurrentStatus:     st.CurrentStatus,
						LastOnline:        st.LastOnline,
						LastLogin:         st.LastLogin,
						CustomState:       st.CustomState,
						Platform:          st.Platform,
						DeviceType:        st.DeviceType,
						DeviceModel:       st.DeviceModel,
						OSVersion:         st.OSVersion,
						AppVersion:        st.AppVersion,
						ConcurrentDevices: st.ConcurrentDevices,
					}
				}
				group.FriendList = friendList
			}
		}

		groups = append(groups, group)
	}

	return groups, nil
}

// GetFriendGroupByGid 根据分组ID获取好友分组（验证是否属于指定用户）
func GetFriendGroupByGid(uid, gid string) (*entity.UserFriendGroup, error) {
	var group *entity.UserFriendGroup
	tx := db.GetDB().
		Where("uid = ? AND gid = ? AND delete_time = ?", uid, gid, 0).
		First(&group)
	if tx.Error != nil {
		if tx.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		logrus.Errorf("GetFriendGroupByGid err: %v", tx.Error)
		return nil, tx.Error
	}
	return group, nil
}
