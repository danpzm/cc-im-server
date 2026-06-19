package query

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/types"
	redisClient "github.com/xd/quic-server/redis"
	"github.com/xd/quic-server/utils"
)

const ROOM_USER_IDS_KEY string = "ROOM:USER:IDS"
const ROOM_SEQ_ID string = "ROOM:SEQ:ID"
const ROOM_ONLINE_USERS_KEY string = "ROOM:ONLINE:USERS"

func GetRoomList(keyword string, limit int, offset int) ([]*types.Room, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	var rooms []*types.Room
	where := db.GetDB().Model(&types.Room{}).
		Where("delete_time = ?", 0).
		Where("state = ?", 1).
		Where("type = ?", 1)
	if keyword != "" {
		where = where.Where("name LIKE ? OR description LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	err := where.
		Order("create_time DESC, id DESC").
		Offset(offset).
		Limit(limit).
		Find(&rooms).Error
	return rooms, err
}

func GetRoomByRid(rid string) (*types.Room, error) {
	var room *types.Room
	tx := db.GetDB().Where("rid = ?", rid).Where("state = ?", 1).Where("delete_time = ?", 0).First(&room)
	return room, tx.Error
}

// GetRoomByRidAny 按 rid 查询房间（含已解散），用于判断房间是否已解散。
func GetRoomByRidAny(rid string) (*types.Room, error) {
	var room types.Room
	tx := db.GetDB().Where("rid = ?", rid).Where("delete_time = ?", 0).First(&room)
	if tx.Error != nil {
		return nil, tx.Error
	}
	return &room, nil
}

// IsRoomDissolved 判断房间是否已解散。
func IsRoomDissolved(rid string) (bool, error) {
	room, err := GetRoomByRidAny(rid)
	if err != nil {
		return false, err
	}
	return room.State == entity.RoomStateDissolved, nil
}

// GetRoomCategoryName 根据分类 id 取分类名称，空 id 返回空字符串
func GetRoomCategoryName(cid string) (string, error) {
	if cid == "" {
		return "", nil
	}
	var name string
	err := db.GetDB().Model(&entity.RoomCategory{}).Where("cid = ?", cid).Where("delete_time = ?", 0).Pluck("name", &name).Error
	if err != nil {
		return "", err
	}
	return name, nil
}

// GetRoomTagNamesByRid 根据房间 rid 取该房间所有标签名称（按 sort 排序）
func GetRoomTagNamesByRid(rid string) ([]string, error) {
	var tagIds []string
	err := db.GetDB().Model(&entity.RoomTagRelation{}).Where("rid = ?", rid).Where("delete_time = ?", 0).Pluck("tag_id", &tagIds).Error
	if err != nil || len(tagIds) == 0 {
		return nil, err
	}
	var names []string
	err = db.GetDB().Model(&entity.RoomTag{}).Where("tid IN ?", tagIds).Where("delete_time = ?", 0).Order("sort ASC, id ASC").Pluck("name", &names).Error
	return names, err
}

// AddUserToRoomOnlineSetIfOnline 如果用户当前在线，则把用户加入指定房间的在线集合
func AddUserToRoomOnlineSetIfOnline(rid, uid string) error {
	// 读取当前用户在线状态（优先 Redis，回源数据库）
	status, err := GetOrCreateUserCurrentStatus(uid)
	if err != nil {
		log.Errorf("AddUserToRoomOnlineSetIfOnline 获取用户状态失败 uid=%s: %v", uid, err)
		return err
	}
	if !status.IsOnline {
		// 用户当前不在线，不需要加入在线集合
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), redisClient.REDIS_TIMEOUT)
	defer cancel()

	key := fmt.Sprintf("%s:%s", ROOM_ONLINE_USERS_KEY, rid)
	if err := redisClient.GetClient().SAdd(ctx, key, uid).Err(); err != nil {
		log.Errorf("AddUserToRoomOnlineSetIfOnline 更新房间在线集合失败 rid=%s uid=%s: %v", rid, uid, err)
		return err
	}

	return nil
}

// RemoveUserFromRoomOnlineSet 将用户从房间在线集合移除
func RemoveUserFromRoomOnlineSet(rid, uid string) error {
	ctx, cancel := context.WithTimeout(context.Background(), redisClient.REDIS_TIMEOUT)
	defer cancel()
	key := fmt.Sprintf("%s:%s", ROOM_ONLINE_USERS_KEY, rid)
	if err := redisClient.GetClient().SRem(ctx, key, uid).Err(); err != nil {
		log.Errorf("RemoveUserFromRoomOnlineSet 失败 rid=%s uid=%s: %v", rid, uid, err)
		return err
	}
	return nil
}

// GetRoomIdsByUid 获取用户所在的所有房间 rid 列表
func GetRoomIdsByUid(uid string) ([]string, error) {
	var roomIds []string
	tx := db.GetDB().Model(&types.RoomUser{}).Where("uid = ? AND delete_time = 0", uid).Pluck("rid", &roomIds)
	if tx.Error != nil {
		log.Errorf("GetRoomIdsByUid err: %v", tx.Error)
	}
	return roomIds, tx.Error
}

// CheckShareRoomBatch 批量判断 targetUids 是否与 viewerUid 同在任意房间
func CheckShareRoomBatch(viewerUid string, targetUids []string) (map[string]bool, error) {
	result := make(map[string]bool, len(targetUids))
	if viewerUid == "" || len(targetUids) == 0 {
		return result, nil
	}
	viewerRids, err := GetRoomIdsByUid(viewerUid)
	if err != nil {
		return result, err
	}
	if len(viewerRids) == 0 {
		return result, nil
	}
	var matched []string
	tx := db.GetDB().Model(&types.RoomUser{}).
		Where("rid IN ? AND uid IN ? AND delete_time = 0", viewerRids, targetUids).
		Distinct("uid").
		Pluck("uid", &matched)
	if tx.Error != nil {
		log.Errorf("CheckShareRoomBatch err viewer=%s: %v", viewerUid, tx.Error)
		return result, tx.Error
	}
	for _, uid := range matched {
		result[uid] = true
	}
	return result, nil
}

// GetUidsSharingRoomWith 获取与指定用户同在任意房间的所有 uid（去重，不含自己）
func GetUidsSharingRoomWith(uid string) ([]string, error) {
	roomIds, err := GetRoomIdsByUid(uid)
	if err != nil || len(roomIds) == 0 {
		return nil, err
	}
	seen := make(map[string]struct{})
	for _, rid := range roomIds {
		userIds, err := GetRoomUserIdsCache(rid)
		if err != nil {
			continue
		}
		for _, u := range userIds {
			if u != uid {
				seen[u] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for u := range seen {
		out = append(out, u)
	}
	return out, nil
}
func getRoomUserIds(rid string) ([]string, error) {
	var userIds []string
	tx := db.GetDB().Model(&types.RoomUser{}).Where("rid = ? AND delete_time = 0", rid).Pluck("uid", &userIds)
	if tx.Error != nil {
		log.Errorf("GetRoomUserIds err: %v", tx.Error)
	}
	return userIds, tx.Error
}
func SetRoomUserIdsCache(rid string) {
	userIds, err := getRoomUserIds(rid)
	if err != nil {
		log.Error("获取房间用户id列表失败")
		return
	}
	key := fmt.Sprintf("%s:%s", ROOM_USER_IDS_KEY, rid)
	err = redisClient.Set(key, userIds, 0)
	if err != nil {
		log.Error("设置房间用户id列表到缓存失败")
	}
}
func GetRoomUserIdsCache(rid string) ([]string, error) {
	key := fmt.Sprintf("%s:%s", ROOM_USER_IDS_KEY, rid)
	userIds, err := redisClient.Get[[]string](key)
	if err != nil {
		if err == redis.Nil {
			SetRoomUserIdsCache(rid)
			userIds, err := redisClient.Get[[]string](key)
			if err != nil {
				return nil, err
			}
			return userIds, nil
		}
		return nil, err
	}
	return userIds, nil
}
func HasRoomUser(rid string, uid string) bool {
	roomUserIds, err := GetRoomUserIdsCache(rid)
	if err == redis.Nil {
		return false
	}
	return utils.Contains(roomUserIds, uid)
}
func GetMaxRoomSeqId(rid string) (int64, error) {
	var seqId int64
	// 使用 COALESCE 将 NULL 转换为 0
	err := db.GetDB().
		Raw(`SELECT COALESCE(MAX(seq_id), 0) FROM room_message WHERE rid = ?`, rid).
		Row().
		Scan(&seqId)

	if err != nil {
		log.Errorf("GetMaxRoomSeqId err: %v", err)
		return 0, err
	}
	return seqId, nil
}
func GetRoomSeqId(rid string) (int64, error) {
	start := time.Now()
	key := fmt.Sprintf("%s:%s", ROOM_SEQ_ID, rid)

	// 先检查 Redis 中是否已经有序列号，没有则从数据库初始化
	ctx, cancel := context.WithTimeout(context.Background(), redisClient.REDIS_TIMEOUT)
	defer cancel()

	_, err := redisClient.GetClient().Get(ctx, key).Int64()
	if err != nil {
		if err != redis.Nil {
			return 0, err
		}
		// key 不存在，从数据库获取当前最大 seq_id（可能为 0）
		maxSeqId, err := GetMaxRoomSeqId(rid)
		if err != nil {
			return 0, err
		}
		// 初始化为数据库里的最大值，后面统一用 INCR 拿下一个
		if err := redisClient.GetClient().SetNX(ctx, key, maxSeqId, 0).Err(); err != nil {
			return 0, err
		}
	}

	// 统一通过 INCR 获取下一个序列号
	result, err := redisClient.GetClient().Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}

	cost := time.Since(start)
	log.Infof("获取房间序列号: %s, 耗时: %s", rid, cost)
	return result, nil
}
