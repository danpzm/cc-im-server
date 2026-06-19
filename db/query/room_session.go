// Package query 提供会话、房间、消息等与 DB/Redis 相关的查询与更新。
// room_session：用户会话列表、最后一条消息、已读 seq 与在线人数等。
package query

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/types"
	quicEntity "github.com/xd/quic-server/quic/handler/entity"
	appredis "github.com/xd/quic-server/redis"
	"gorm.io/gorm"
)

const defaultRoomOnlineCountCacheTTL = 2 * time.Second

// roomLastMessageCacheKeyPrefix 按 rid 缓存「房间内最后一条展示消息」，供会话列表等高频读共享，避免每人重复打 DB。
const roomLastMessageCacheKeyPrefix = "cc:room:last_msg:v1:"
const roomLastMessageCacheTTL = 7 * 24 * time.Hour // 兜底过期；正常由 Bump / 撤回 主动删 key

func roomLastMessageCacheKey(rid string) string {
	return roomLastMessageCacheKeyPrefix + rid
}

// InvalidateRoomLastMessageCache 在房间最后一条消息可能变化时删除 Redis 条目（新消息落库、撤回等）。
func InvalidateRoomLastMessageCache(rid string) {
	if rid == "" {
		return
	}
	if err := appredis.Delete(roomLastMessageCacheKey(rid)); err != nil {
		log.Warnf("InvalidateRoomLastMessageCache rid=%s: %v", rid, err)
	}
}

func dedupeRidsPreserveOrder(rids []string) []string {
	seen := make(map[string]struct{}, len(rids))
	out := make([]string, 0, len(rids))
	for _, rid := range rids {
		if rid == "" {
			continue
		}
		if _, ok := seen[rid]; ok {
			continue
		}
		seen[rid] = struct{}{}
		out = append(out, rid)
	}
	return out
}

// tryRoomLastMessagesFromRedis 按 rid 批量读缓存；miss 的 rid 在 misses 中返回（顺序与 rids 一致去重后）。
func tryRoomLastMessagesFromRedis(rids []string) (hits map[string]*types.ServerRoomMessage, misses []string) {
	hits = make(map[string]*types.ServerRoomMessage)
	if len(rids) == 0 {
		return hits, nil
	}
	keys := make([]string, len(rids))
	for i := range rids {
		keys[i] = roomLastMessageCacheKey(rids[i])
	}
	cached, err := appredis.MGet[types.ServerRoomMessage](keys)
	if err != nil {
		log.Warnf("tryRoomLastMessagesFromRedis MGet: %v", err)
		return hits, append(misses, rids...)
	}
	for _, rid := range rids {
		k := roomLastMessageCacheKey(rid)
		msg, ok := cached[k]
		if !ok || msg.Rid != rid || msg.Mid == "" {
			misses = append(misses, rid)
			continue
		}
		cp := msg
		hits[rid] = &cp
	}
	return hits, misses
}

// lastMessageMidChunkSize 最后消息正文聚合时对 mid IN 的分片大小，避免单次 IN 过大拖慢规划器。
const lastMessageMidChunkSize = 350

// sessionListPreviewMaxContentRowsPerMid 会话列表「最后一条预览」每个 mid 最多取的 room_message_content 行数。
// 单条消息可含大量块（多段文本/卡片等），全量拉取会拖慢 session/list；进房后消息接口再拉完整正文即可。
// 与 QQ/微信类似：列表只展示摘要，不把整条消息所有块都打上来。
const sessionListPreviewMaxContentRowsPerMid = 20

type roomOnlineCountCacheEntry struct {
	count     int
	expiresAt time.Time
}

var roomOnlineCountCache sync.Map // key: rid, value: roomOnlineCountCacheEntry

func getRoomOnlineCountCacheTTL() time.Duration {
	serverCfg := config.GetServerConfig()
	if serverCfg == nil || serverCfg.SessionOnlineCountCacheTTL <= 0 {
		return defaultRoomOnlineCountCacheTTL
	}
	return serverCfg.SessionOnlineCountCacheTTL
}

type UserRoomSessionDto struct {
	Id               int64           `json:"id"`
	Rsid             string          `json:"rsid"`
	Rid              string          `json:"rid"`
	RoomType         int8            `json:"room_type"`
	IsTop            bool            `json:"is_top"`
	Name             string          `json:"name"`
	AvatarUfId       string          `json:"avatar_uf_id"`
	MemberCount      int             `json:"member_count"`
	OnlineCount      int             `json:"online_count"`
	CreateUid        string          `json:"create_uid"`
	FriendName       string          `json:"friend_name"`
	FriendUid        string          `json:"friend_uid"` // 对方 uid（私聊/群私聊时用于从 roomMemberRoomNicknameMap 取房间昵称）
	FriendAvatarUfId string          `json:"friend_avatar_uf_id"`
	DisturbType      int8            `json:"disturb_type"`
	DisturbConfig    json.RawMessage `json:"disturb_config"`
	LastSeqId        int64           `json:"last_seq_id"`
	/** 未读条数：last_message.seq_id 与 last_seq_id 之差（无最后一条消息时为 0） */
	UnreadCount             int64                    `json:"unread_count" gorm:"-"`
	LastMentionSeqId        int64                    `json:"last_mention_seq_id"`
	LastMentionMessageSeqId int64                    `json:"last_mention_message_seq_id"`     // 最后一条@我的消息的seq_id
	LastMessage             *types.ServerRoomMessage `json:"last_message,omitempty" gorm:"-"` // 使用 gorm:"-" 告诉 GORM 忽略此字段，它不是数据库字段
	CreateTime              int64                    `json:"create_time"`
	UpdateTime              int64                    `json:"update_time"`
	// LastRoomMessageCreateTime 与列表排序键一致（user_room_session 列）；客户端本地 im_session 须与 update_time、id 组成复合游标，与服务端一致。
	LastRoomMessageCreateTime int64 `json:"last_room_message_create_time"`
	// 当前用户在该房间的备注与禁言状态（会话列表一次查出，无需再开抽屉）
	RoomRemark     string `json:"room_remark"`
	IsMuteAll      bool   `json:"is_mute_all"`
	IsStrategyMute bool   `json:"is_strategy_mute"`
	MyMuteUntil    int64  `json:"my_mute_until"`
	// 群私聊(type=2)时表示来源群 rid/名称，用于展示「来自 xxx」
	FromRoomRid  string `json:"from_room_rid"`
	FromRoomName string `json:"from_room_name"`
	// RoomState 房间状态（1-正常 0-已解散；仅群聊有效）
	RoomState int8 `json:"room_state"`
	// 私聊/群私聊时：当前用户与对方是否为好友；是否已屏蔽该会话（屏蔽后无法发送）
	IsFriend      bool `json:"is_friend"`
	IsRoomBlocked bool `json:"is_room_blocked"`
	// 私聊/群私聊时：当前用户给好友设置的备注
	FriendRemark string `json:"friend_remark"`
}

// sessionListBaseRowsQuery 与列表/搜索共用的 JOIN、SELECT、基础 WHERE（不含 ORDER/LIMIT/搜索条件）。
// 自聊（type=3）会话头像始终跟随创建者（当前用户）资料头像。
const sessionListAvatarUfIdSQL = "CASE WHEN r.type = 3 THEN TRIM(COALESCE(u_creator.avatar_uf_id, '')) ELSE COALESCE(TRIM(COALESCE(r.avatar_uf_id, '')), u_creator.avatar_uf_id) END AS avatar_uf_id"
const sessionListFriendAvatarUfIdSQL = "CASE WHEN r.type = 3 THEN TRIM(COALESCE(u_creator.avatar_uf_id, '')) ELSE u.avatar_uf_id END AS friend_avatar_uf_id"

func sessionListBaseRowsQuery(uid string) *gorm.DB {
	return db.GetDB().
		Table("user_room_session AS urs").
		Select([]string{
			"urs.id",
			"urs.rsid",
			"urs.rid",
			"urs.create_time",
			"urs.is_top",
			"r.type as room_type",
			"r.name",
			sessionListAvatarUfIdSQL,
			"r.member_count",
			"r.create_uid",
			"CASE WHEN r.type = 3 THEN '我的笔记' ELSE u.nickname END AS friend_name",
			"CASE WHEN r.type = 3 THEN r.create_uid ELSE ru.uid END AS friend_uid",
			sessionListFriendAvatarUfIdSQL,
			"urs.disturb_type",
			"urs.disturb_config",
			"urs.last_seq_id",
			"urs.last_mention_seq_id",
			"urs.update_time",
			"urs.last_room_message_create_time",
			"0 AS last_mention_message_seq_id",
			"COALESCE(ru_me.room_remark, '') AS room_remark",
			"COALESCE(rmc.is_mute_all, false) AS is_mute_all",
			"CASE WHEN COALESCE(rmc.is_mute_all, false) = false AND COALESCE(rmc.is_active, false) = true THEN true ELSE false END AS is_strategy_mute",
			"COALESCE(ru_me.mute_until, 0) AS my_mute_until",
			"COALESCE(r.from_room_rid, '') AS from_room_rid",
			"COALESCE(r_from.name, '') AS from_room_name",
			"r.state AS room_state",
		}).
		Joins("INNER JOIN room AS r ON urs.rid = r.rid").
		Joins("LEFT JOIN room AS r_from ON r.from_room_rid = r_from.rid AND r_from.delete_time = 0").
		Joins(`LEFT JOIN "user" AS u_creator ON u_creator.uid = r.create_uid AND u_creator.delete_time = 0`).
		Joins("LEFT JOIN room_user AS ru_me ON ru_me.rid = urs.rid AND ru_me.uid = ? AND ru_me.delete_time = 0", uid).
		Joins("LEFT JOIN room_mute_config AS rmc ON rmc.rid = urs.rid AND rmc.delete_time = 0").
		Joins("LEFT JOIN room_user AS ru ON r.rid = ru.rid AND ru.uid != ? AND (r.type = 0 OR r.type = 2) AND r.member_count = 2 AND r.create_uid = 'system' AND ru.delete_time = 0", uid).
		Joins(`LEFT JOIN "user" AS u ON ru.uid = u.uid AND u.uid != ? AND u.delete_time = 0`, uid).
		Where("urs.uid = ?", uid).
		Where("urs.state = ?", 1).
		Where("urs.delete_time = ?", 0).
		Where("r.delete_time = ?", 0).
		Where("r.state IN ?", []int8{entity.RoomStateDissolved, entity.RoomStateActive})
}

const defaultSessionListPageSize = 15

// sessionListPageRowsQuery 会话列表行（排序：update_time DESC，其次 last_room_message_create_time DESC，再 id DESC）。
func sessionListPageRowsQuery(uid string) *gorm.DB {
	return db.GetDB().
		Table("user_room_session AS urs").
		Select([]string{
			"urs.id",
			"urs.rsid",
			"urs.rid",
			"urs.create_time",
			"urs.is_top",
			"r.type as room_type",
			"r.name",
			sessionListAvatarUfIdSQL,
			"r.member_count",
			"r.create_uid",
			"CASE WHEN r.type = 3 THEN '我的笔记' ELSE u.nickname END AS friend_name",
			"CASE WHEN r.type = 3 THEN r.create_uid ELSE ru.uid END AS friend_uid",
			sessionListFriendAvatarUfIdSQL,
			"urs.disturb_type",
			"urs.disturb_config",
			"urs.last_seq_id",
			"urs.last_mention_seq_id",
			"urs.update_time",
			"urs.last_room_message_create_time",
			"0 AS last_mention_message_seq_id",
			"COALESCE(ru_me.room_remark, '') AS room_remark",
			"COALESCE(rmc.is_mute_all, false) AS is_mute_all",
			"CASE WHEN COALESCE(rmc.is_mute_all, false) = false AND COALESCE(rmc.is_active, false) = true THEN true ELSE false END AS is_strategy_mute",
			"COALESCE(ru_me.mute_until, 0) AS my_mute_until",
			"COALESCE(r.from_room_rid, '') AS from_room_rid",
			"COALESCE(r_from.name, '') AS from_room_name",
			"r.state AS room_state",
		}).
		Joins("INNER JOIN room AS r ON urs.rid = r.rid").
		Joins("LEFT JOIN room AS r_from ON r.from_room_rid = r_from.rid AND r_from.delete_time = 0").
		Joins(`LEFT JOIN "user" AS u_creator ON u_creator.uid = r.create_uid AND u_creator.delete_time = 0`).
		Joins("LEFT JOIN room_user AS ru_me ON ru_me.rid = urs.rid AND ru_me.uid = ? AND ru_me.delete_time = 0", uid).
		Joins("LEFT JOIN room_mute_config AS rmc ON rmc.rid = urs.rid AND rmc.delete_time = 0").
		Joins("LEFT JOIN room_user AS ru ON r.rid = ru.rid AND ru.uid != ? AND (r.type = 0 OR r.type = 2) AND r.member_count = 2 AND r.create_uid = 'system' AND ru.delete_time = 0", uid).
		Joins(`LEFT JOIN "user" AS u ON ru.uid = u.uid AND u.uid != ? AND u.delete_time = 0`, uid).
		Where("urs.uid = ?", uid).
		Where("urs.state = ?", 1).
		Where("urs.delete_time = ?", 0).
		Where("r.delete_time = ?", 0).
		Where("r.state IN ?", []int8{entity.RoomStateDissolved, entity.RoomStateActive})
}

// BumpUserRoomSessionLastMessageTime 在 room_message 落库成功后调用：将该 rid 下所有活跃会话的
// last_room_message_create_time、update_time 一并抬到不小于本条消息的 create_time，
// 使会话列表按「先 update_time」排序时，新消息也会把会话顶到前面。
func BumpUserRoomSessionLastMessageTime(rid string, msgCreateTime int64) error {
	if rid == "" || msgCreateTime <= 0 {
		return nil
	}
	err := db.GetDB().Exec(`
		UPDATE user_room_session SET
			last_room_message_create_time = GREATEST(COALESCE(last_room_message_create_time, 0), ?),
			update_time = GREATEST(COALESCE(update_time, 0), ?)
		WHERE rid = ? AND state = 1 AND delete_time = 0
	`, msgCreateTime, msgCreateTime, rid).Error
	if err != nil {
		log.Errorf("BumpUserRoomSessionLastMessageTime rid=%s create_time=%d: %v", rid, msgCreateTime, err)
		return err
	}
	InvalidateRoomLastMessageCache(rid)
	return nil
}

// GetUserRoomSessionPage 分页拉取会话：按 urs.update_time DESC、urs.last_room_message_create_time DESC、urs.id DESC；每页 defaultSessionListPageSize 条。
// firstPage 为 true 时不加游标条件；否则用上一页返回的 (cursorUpdateTime, cursorLastMsgTime, cursorID) 做 keyset。
func GetUserRoomSessionPage(uid string, cursorUT, cursorLM, cursorID int64, firstPage bool) ([]*UserRoomSessionDto, int64, int64, int64, bool, error) {
	pageSize := defaultSessionListPageSize
	q := sessionListPageRowsQuery(uid)
	if !firstPage {
		q = q.Where(`(
			urs.update_time < ?
			OR (urs.update_time = ? AND COALESCE(urs.last_room_message_create_time, 0) < ?)
			OR (urs.update_time = ? AND COALESCE(urs.last_room_message_create_time, 0) = ? AND urs.id < ?)
		)`, cursorUT, cursorUT, cursorLM, cursorUT, cursorLM, cursorID)
	}
	q = q.Order("urs.update_time DESC, COALESCE(urs.last_room_message_create_time, 0) DESC, urs.id DESC").Limit(pageSize + 1)

	var sessions []*UserRoomSessionDto
	if err := q.Find(&sessions).Error; err != nil {
		return nil, 0, 0, 0, false, err
	}
	hasMore := len(sessions) > pageSize
	if hasMore {
		sessions = sessions[:pageSize]
	}
	var nextUT, nextLM, nextID int64
	if len(sessions) > 0 {
		last := sessions[len(sessions)-1]
		nextUT = last.UpdateTime
		nextLM = last.LastRoomMessageCreateTime
		nextID = last.Id
	}
	fillSessionOnlineCounts(sessions)
	fillSessionFriendAndBlock(uid, sessions)
	fillLastMentionMessageSeqForSessions(uid, sessions)
	return sessions, nextUT, nextLM, nextID, hasMore, nil
}

// GetUserRoomSessionDtoByRid 按 uid+rid 获取单条会话 DTO（字段与 session/list 一致，供 open chat 等）。
func GetUserRoomSessionDtoByRid(uid, rid string) (*UserRoomSessionDto, error) {
	if uid == "" || rid == "" {
		return nil, errors.New("uid and rid required")
	}
	var session UserRoomSessionDto
	tx := sessionListPageRowsQuery(uid).
		Where("urs.rid = ?", rid).
		Limit(1).
		First(&session)
	if tx.Error != nil {
		if errors.Is(tx.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, tx.Error
	}
	sessions := []*UserRoomSessionDto{&session}
	fillSessionOnlineCounts(sessions)
	fillSessionFriendAndBlock(uid, sessions)
	fillLastMentionMessageSeqForSessions(uid, sessions)
	lastMsgMap, err := GetLastMessagesByRidList(uid, []string{rid})
	if err != nil {
		return nil, err
	}
	if lastMsg, ok := lastMsgMap[rid]; ok && lastMsg != nil {
		session.LastMessage = lastMsg
	}
	session.UnreadCount = SessionUnreadCount(session.LastSeqId, session.LastMessage)
	return &session, nil
}

func escapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// SearchUserRoomSessions 按关键词搜索会话（房间名、备注、来源群名、对端昵称/用户名/uid），按 urs.id DESC 分页。
// scope: all | friends（type 0/2/3）| groups（type 1）；cursorID 为上一页最后一条的 id，首次传 0。
func SearchUserRoomSessions(uid, keyword, scope string, cursorID int64, limit int) ([]*UserRoomSessionDto, int64, bool, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, 0, false, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	pat := "%" + escapeLikePattern(keyword) + "%"
	q := sessionListBaseRowsQuery(uid).
		Where(`(
			r.name ILIKE ? OR
			COALESCE(TRIM(ru_me.room_remark), '') ILIKE ? OR
			COALESCE(TRIM(r_from.name), '') ILIKE ? OR
			COALESCE(TRIM(u.nickname), '') ILIKE ? OR
			COALESCE(TRIM(u.username), '') ILIKE ? OR
			COALESCE(CAST(ru.uid AS TEXT), '') ILIKE ?
		)`, pat, pat, pat, pat, pat, pat)
	switch scope {
	case "friends":
		q = q.Where("r.type IN ?", []int8{0, 2, 3})
	case "groups":
		q = q.Where("r.type = ?", 1)
	}
	if cursorID > 0 {
		q = q.Where("urs.id < ?", cursorID)
	}
	var sessions []*UserRoomSessionDto
	if err := q.Order("urs.id DESC").Limit(limit + 1).Find(&sessions).Error; err != nil {
		return nil, 0, false, err
	}
	hasMore := len(sessions) > limit
	if hasMore {
		sessions = sessions[:limit]
	}
	var nextCursor int64
	if len(sessions) > 0 {
		nextCursor = sessions[len(sessions)-1].Id
	}
	fillSessionOnlineCounts(sessions)
	fillSessionFriendAndBlock(uid, sessions)
	fillLastMentionMessageSeqForSessions(uid, sessions)
	return sessions, nextCursor, hasMore, nil
}

// fillSessionFriendAndBlock 为私聊/群私聊/自聊会话填充 is_friend、is_room_blocked
func fillSessionFriendAndBlock(uid string, sessions []*UserRoomSessionDto) {
	friendUidSet := make(map[string]struct{}, len(sessions))
	ridSet := make(map[string]struct{}, len(sessions))
	friendUids := make([]string, 0, len(sessions))
	rids := make([]string, 0, len(sessions))

	for _, s := range sessions {
		if s.RoomType == 3 {
			// 自聊：自己与自己，视为好友且不屏蔽
			s.IsFriend = true
			s.IsRoomBlocked = false
			continue
		}
		if s.RoomType != 0 && s.RoomType != 2 {
			continue
		}
		if s.FriendUid != "" {
			if _, ok := friendUidSet[s.FriendUid]; !ok {
				friendUidSet[s.FriendUid] = struct{}{}
				friendUids = append(friendUids, s.FriendUid)
			}
		}
		if s.Rid != "" {
			if _, ok := ridSet[s.Rid]; !ok {
				ridSet[s.Rid] = struct{}{}
				rids = append(rids, s.Rid)
			}
		}
	}

	friendMap, err := CheckFriendRelationBatch(uid, friendUids)
	if err != nil {
		log.Errorf("批量检查好友关系失败 uid=%s: %v", uid, err)
	}
	remarkMap, err := GetFriendRemarkBatch(uid, friendUids)
	if err != nil {
		log.Errorf("批量获取好友备注失败 uid=%s: %v", uid, err)
	}
	blockMap, err := HasUserBlockedRoomBatch(uid, rids)
	if err != nil {
		log.Errorf("批量检查会话屏蔽状态失败 uid=%s: %v", uid, err)
	}

	for _, s := range sessions {
		if s.RoomType == 3 {
			s.IsFriend = true
			s.IsRoomBlocked = false
			continue
		}
		if s.RoomType != 0 && s.RoomType != 2 {
			continue
		}
		if s.FriendUid == "" {
			s.IsFriend = true
		} else {
			s.IsFriend = friendMap[s.FriendUid]
			s.FriendRemark = remarkMap[s.FriendUid]
		}
		s.IsRoomBlocked = blockMap[s.Rid]
	}
}

// fillLastMentionMessageSeqForSessions 仅对当前列表中的 rid 聚合「@ 我的」有效消息的最大 seq，避免会话列表主查询 JOIN 全量 mention。
func fillLastMentionMessageSeqForSessions(uid string, sessions []*UserRoomSessionDto) {
	if len(sessions) == 0 {
		return
	}
	ridSeen := make(map[string]struct{}, len(sessions))
	rids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if s == nil || s.Rid == "" {
			continue
		}
		if _, ok := ridSeen[s.Rid]; ok {
			continue
		}
		ridSeen[s.Rid] = struct{}{}
		rids = append(rids, s.Rid)
	}
	if len(rids) == 0 {
		return
	}
	type mentionSeqRow struct {
		Rid                     string `gorm:"column:rid"`
		LastMentionMessageSeqId int64  `gorm:"column:last_mention_message_seq_id"`
	}
	var rows []mentionSeqRow
	err := db.GetDB().Raw(`
		SELECT rmm.rid, MAX(rm.seq_id) AS last_mention_message_seq_id
		FROM room_message_mention AS rmm
		INNER JOIN room_message AS rm ON rm.mid = rmm.mid
			AND rm.delete_time = 0 AND rm.withdraw_time = 0 AND rm.state = 1
		WHERE rmm.uid = ? AND rmm.delete_time = 0 AND rmm.rid IN ?
		GROUP BY rmm.rid
	`, uid, rids).Scan(&rows).Error
	if err != nil {
		log.Errorf("fillLastMentionMessageSeqForSessions uid=%s: %v", uid, err)
		return
	}
	byRid := make(map[string]int64, len(rows))
	for i := range rows {
		byRid[rows[i].Rid] = rows[i].LastMentionMessageSeqId
	}
	for _, s := range sessions {
		if s == nil {
			continue
		}
		if v, ok := byRid[s.Rid]; ok {
			s.LastMentionMessageSeqId = v
		}
	}
}

// fillSessionOnlineCounts 从 Redis 房间在线集合为会话列表填充 OnlineCount，便于列表展示。
// 职责单一：仅负责在线人数统计，失败时置 0 并打日志。
func fillSessionOnlineCounts(sessions []*UserRoomSessionDto) {
	if len(sessions) == 0 {
		return
	}

	now := time.Now()
	missingIndexes := make([]int, 0, len(sessions))
	for i, session := range sessions {
		if session == nil || session.Rid == "" {
			if session != nil {
				session.OnlineCount = 0
			}
			continue
		}
		if v, ok := roomOnlineCountCache.Load(session.Rid); ok {
			if entry, typeOK := v.(roomOnlineCountCacheEntry); typeOK && now.Before(entry.expiresAt) {
				session.OnlineCount = entry.count
				continue
			}
		}
		missingIndexes = append(missingIndexes, i)
	}
	if len(missingIndexes) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), appredis.REDIS_TIMEOUT)
	defer cancel()
	client := appredis.GetClient()

	// 高并发下逐个 SCARD 容易因为多次网络往返触发超时，这里改为批量 pipeline。
	commands := make([]*redis.IntCmd, len(missingIndexes))
	_, err := client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for i, sessionIndex := range missingIndexes {
			session := sessions[sessionIndex]
			key := ROOM_ONLINE_USERS_KEY + ":" + session.Rid
			commands[i] = pipe.SCard(ctx, key)
		}
		return nil
	})
	if err != nil && err != redis.Nil {
		log.Errorf("批量获取房间在线人数失败: %v", err)
		for _, sessionIndex := range missingIndexes {
			session := sessions[sessionIndex]
			session.OnlineCount = 0
		}
		return
	}

	expiresAt := time.Now().Add(getRoomOnlineCountCacheTTL())
	for i, sessionIndex := range missingIndexes {
		session := sessions[sessionIndex]
		count, cmdErr := commands[i].Result()
		if cmdErr != nil {
			if cmdErr != redis.Nil {
				log.Errorf("获取房间在线人数失败 rid=%s: %v", session.Rid, cmdErr)
			}
			session.OnlineCount = 0
			continue
		}
		if count < 0 {
			count = 0
		}
		session.OnlineCount = int(count)
		roomOnlineCountCache.Store(session.Rid, roomOnlineCountCacheEntry{
			count:     session.OnlineCount,
			expiresAt: expiresAt,
		})
	}
}

// ---------- 最后一条消息 ----------

type lastMsgMetaRow struct {
	SeqId      int64  `gorm:"column:seq_id"`
	Mid        string `gorm:"column:mid"`
	Rid        string `gorm:"column:rid"`
	SenderUid  string `gorm:"column:sender_uid"`
	ClientMid  string `gorm:"column:client_mid"`
	CreateTime int64  `gorm:"column:create_time"`
}

// lastMsgContentFlatRow 拉取每条 room_message_content 一行（无 jsonb_agg），在内存中按 mid 组装为 ServerRoomMessageContentList。
type lastMsgContentFlatRow struct {
	Mid          string                        `gorm:"column:mid"`
	Cid          string                        `gorm:"column:cid"`
	ClientCid    string                        `gorm:"column:client_cid"`
	Type         entity.RoomMessageContentType `gorm:"column:type"`
	TypeId       string                        `gorm:"column:type_id"`
	Content      json.RawMessage               `gorm:"column:content"`
	CreateTime   int64                         `gorm:"column:create_time"`
	FileUfID     string                        `gorm:"column:file_uf_id"`
	FileDuration int64                         `gorm:"column:file_duration"`
	FileFilename string                        `gorm:"column:file_filename"`
	FileHash     string                        `gorm:"column:file_hash"`
	FileSize     int64                         `gorm:"column:file_size"`
	FileTypeMain string                        `gorm:"column:file_type_main"`
	FileTypeSub  string                        `gorm:"column:file_type_sub"`
	FileHeight   int64                         `gorm:"column:file_height"`
	FileWidth    int64                         `gorm:"column:file_width"`
}

func flatRowToServerRoomMessageContent(row lastMsgContentFlatRow) quicEntity.ServerRoomMessageContent {
	c := quicEntity.ServerRoomMessageContent{
		Mid:        row.Mid,
		Cid:        row.Cid,
		ClientCid:  row.ClientCid,
		Type:       row.Type,
		TypeId:     row.TypeId,
		Content:    row.Content,
		CreateTime: row.CreateTime,
	}
	if row.FileUfID != "" {
		c.File = &quicEntity.ServerMessageContentFile{
			Filename:     row.FileFilename,
			FileSize:     uint64(row.FileSize),
			FileTypeMain: row.FileTypeMain,
			FileTypeSub:  row.FileTypeSub,
			FileHash:     row.FileHash,
			Height:       uint64(row.FileHeight),
			Width:        uint64(row.FileWidth),
			Duration:     uint64(row.FileDuration),
			UfId:         row.FileUfID,
		}
	}
	return c
}

// fetchLastMessageContentsByMids 按 mid 批量拉正文行（ORDER BY mid, id），在 Go 内分组；避免 jsonb_agg + GROUP BY 在大内容量下过慢。
// 仅用于会话列表等预览场景：每 mid 最多取前 maxRowsPerMid 行（按 id），显著降低大消息下的 jsonb 与 JOIN 开销。
//
// 建议在 PostgreSQL 上补充索引（生产可 CONCURRENTLY）：
//
//	CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_room_message_content_mid_alive_id
//	ON room_message_content (mid, id) WHERE delete_time = 0;
func fetchLastMessageContentsByMids(mids []string, maxRowsPerMid int) (map[string]quicEntity.ServerRoomMessageContentList, error) {
	out := make(map[string]quicEntity.ServerRoomMessageContentList)
	if len(mids) == 0 {
		return out, nil
	}
	if maxRowsPerMid <= 0 {
		maxRowsPerMid = sessionListPreviewMaxContentRowsPerMid
	}
	const q = `
SELECT
	rmc.mid, rmc.cid, rmc.client_cid, rmc.type, rmc.type_id, rmc.content, rmc.create_time,
	COALESCE(uuf.uf_id, '') AS file_uf_id,
	COALESCE(uf.duration, 0) AS file_duration,
	COALESCE(uuf.filename, '') AS file_filename,
	COALESCE(uf.hash, '') AS file_hash,
	COALESCE(uf.total_size, 0) AS file_size,
	COALESCE(uf.type_main, '') AS file_type_main,
	COALESCE(uf.type_sub, '') AS file_type_sub,
	COALESCE(uf.height, 0) AS file_height,
	COALESCE(uf.width, 0) AS file_width
FROM (
	SELECT mid, cid, client_cid, type, type_id, content, create_time, id,
		ROW_NUMBER() OVER (PARTITION BY mid ORDER BY id ASC) AS rn
	FROM room_message_content
	WHERE delete_time = 0 AND mid IN ?
) AS rmc
LEFT JOIN user_upload_file AS uuf ON uuf.uf_id = rmc.type_id AND rmc.type IN ('file','image','video','audio') AND uuf.delete_time = 0
LEFT JOIN upload_file AS uf ON uf.fid = uuf.fid AND uf.delete_time = 0
WHERE rmc.rn <= ?
ORDER BY rmc.mid, rmc.id ASC`

	for i := 0; i < len(mids); i += lastMessageMidChunkSize {
		j := i + lastMessageMidChunkSize
		if j > len(mids) {
			j = len(mids)
		}
		chunk := mids[i:j]
		var rows []lastMsgContentFlatRow
		if err := db.GetDB().Raw(q, chunk, maxRowsPerMid).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for k := range rows {
			r := rows[k]
			out[r.Mid] = append(out[r.Mid], flatRowToServerRoomMessageContent(r))
		}
	}
	return out, nil
}

// getLastMessagesForUserSessions 拆成三步：每房最新一条元数据 → 按 mid 分片拉正文行并在内存组装 → 批量查发送者昵称/头像/群名片后拼装。
// 避免对全表 room_message 做 DISTINCT ON 再 JOIN urs，以及对正文做 jsonb_agg 聚合过慢。
// 当 restrictRids 非空时先按 rid 读 Redis（同房间多用户共享），miss 再查库并回写。
func getLastMessagesForUserSessions(uid string, restrictRids []string) (map[string]*types.ServerRoomMessage, error) {
	result := make(map[string]*types.ServerRoomMessage)

	queryRids := restrictRids
	if len(restrictRids) > 0 {
		uniq := dedupeRidsPreserveOrder(restrictRids)
		redisHits, misses := tryRoomLastMessagesFromRedis(uniq)
		for rid, msg := range redisHits {
			result[rid] = msg
		}
		if len(misses) == 0 {
			return result, nil
		}
		queryRids = misses
	}

	sql := `SELECT DISTINCT ON (rm.rid) rm.seq_id, rm.mid, rm.rid, rm.sender_uid, rm.client_mid, rm.create_time
FROM room_message rm
INNER JOIN user_room_session AS urs ON urs.rid = rm.rid AND urs.uid = ? AND urs.state = 1 AND urs.delete_time = 0
WHERE rm.state = 1 AND rm.delete_time = 0 AND rm.withdraw_time = 0`
	args := []any{uid}
	if len(queryRids) > 0 {
		sql += ` AND rm.rid IN ?`
		args = append(args, queryRids)
	}
	sql += ` ORDER BY rm.rid, rm.seq_id DESC`

	var metas []lastMsgMetaRow
	if err := db.GetDB().Raw(sql, args...).Scan(&metas).Error; err != nil {
		return nil, err
	}
	if len(metas) == 0 {
		return result, nil
	}

	mids := make([]string, len(metas))
	senderSet := make(map[string]struct{}, len(metas))
	ridSet := make(map[string]struct{}, len(metas))
	for i := range metas {
		mids[i] = metas[i].Mid
		senderSet[metas[i].SenderUid] = struct{}{}
		ridSet[metas[i].Rid] = struct{}{}
	}

	contentsByMid, err := fetchLastMessageContentsByMids(mids, sessionListPreviewMaxContentRowsPerMid)
	if err != nil {
		return nil, err
	}

	senderUids := make([]string, 0, len(senderSet))
	for u := range senderSet {
		senderUids = append(senderUids, u)
	}
	rids := make([]string, 0, len(ridSet))
	for r := range ridSet {
		rids = append(rids, r)
	}

	nickByUID := make(map[string]string, len(senderUids))
	avatarByUID := make(map[string]string, len(senderUids))
	if len(senderUids) > 0 {
		var users []entity.User
		if err := db.GetDB().Model(&entity.User{}).
			Select("uid", "nickname", "avatar_uf_id").
			Where("uid IN ? AND delete_time = 0", senderUids).
			Find(&users).Error; err != nil {
			return nil, err
		}
		for k := range users {
			nickByUID[users[k].Uid] = users[k].Nickname
			avatarByUID[users[k].Uid] = users[k].AvatarUfId
		}
	}

	roomNickByRidUID := make(map[string]string, len(metas))
	if len(rids) > 0 && len(senderUids) > 0 {
		var rus []entity.RoomUser
		if err := db.GetDB().Model(&entity.RoomUser{}).
			Select("rid", "uid", "room_nickname").
			Where("delete_time = 0 AND rid IN ? AND uid IN ?", rids, senderUids).
			Find(&rus).Error; err != nil {
			return nil, err
		}
		for k := range rus {
			key := rus[k].Rid + "\x00" + rus[k].Uid
			roomNickByRidUID[key] = rus[k].RoomNickname
		}
	}

	for i := range metas {
		meta := metas[i]
		msg := types.ServerRoomMessage{
			SeqId:              meta.SeqId,
			Mid:                meta.Mid,
			Rid:                meta.Rid,
			SenderUid:          meta.SenderUid,
			ClientMid:          meta.ClientMid,
			CreateTime:         meta.CreateTime,
			SenderNickname:     nickByUID[meta.SenderUid],
			SenderAvatarUfId:   avatarByUID[meta.SenderUid],
			SenderRoomNickname: roomNickByRidUID[meta.Rid+"\x00"+meta.SenderUid],
		}
		if c, ok := contentsByMid[meta.Mid]; ok {
			msg.Contents = c
		}
		cp := msg
		result[meta.Rid] = &cp
	}

	if len(restrictRids) > 0 && len(metas) > 0 {
		toRedis := make(map[string]types.ServerRoomMessage, len(metas))
		for i := range metas {
			rid := metas[i].Rid
			if m, ok := result[rid]; ok && m != nil {
				toRedis[roomLastMessageCacheKey(rid)] = *m
			}
		}
		if err := appredis.MSetJSON(toRedis, roomLastMessageCacheTTL); err != nil {
			log.Warnf("room last message MSetJSON: %v", err)
		}
	}

	return result, nil
}

// GetLastMessagesByUid 批量获取用户各会话房间的最后一条消息（全量会话）。
func GetLastMessagesByUid(uid string) (map[string]*types.ServerRoomMessage, error) {
	return getLastMessagesForUserSessions(uid, nil)
}

// GetLastMessagesByRidList 仅对指定 rid 拉取最后一条消息（搜索分页等，避免扫全量会话）。
func GetLastMessagesByRidList(uid string, rids []string) (map[string]*types.ServerRoomMessage, error) {
	if len(rids) == 0 {
		return make(map[string]*types.ServerRoomMessage), nil
	}
	return getLastMessagesForUserSessions(uid, rids)
}

// ---------- 会话 seq 与更新 ----------
type UserRoomSessionSeq struct {
	Rid       string `json:"rid"`
	LastSeqId int64  `json:"last_seq_id"`
}

// SessionUnreadCount 单会话未读（与客户端 sessionUnreadCount 口径一致）。
func SessionUnreadCount(lastSeqId int64, lastMessage *types.ServerRoomMessage) int64 {
	if lastMessage == nil {
		return 0
	}
	msgSeq := lastMessage.SeqId
	if msgSeq > lastSeqId {
		return msgSeq - lastSeqId
	}
	return 0
}

// InitialLastSeqIdOnRoomJoin 新成员加入/创建房间时初始化已读游标。
// joinSystemMessageSeqId 为本次加入（或创建）系统消息的 seq_id：加入前历史及该条系统消息均视为已读，仅之后消息计未读。
func InitialLastSeqIdOnRoomJoin(joinSystemMessageSeqId int64) int64 {
	if joinSystemMessageSeqId < 0 {
		return 0
	}
	return joinSystemMessageSeqId
}

// ComputeUserTotalUnread 用户全部有效会话未读总和（侧栏角标）。
func ComputeUserTotalUnread(uid string) (int64, error) {
	seqRows, err := GetUserRoomSessionSeq(uid)
	if err != nil {
		return 0, err
	}
	lastMsgMap, err := GetLastMessagesByUid(uid)
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, row := range seqRows {
		if row == nil || row.Rid == "" {
			continue
		}
		sum += SessionUnreadCount(row.LastSeqId, lastMsgMap[row.Rid])
	}
	return sum, nil
}

// GetUserRoomSessionSeq 获取用户各房间最近已读的 seq_id，用于未读数等。
func GetUserRoomSessionSeq(uid string) ([]*UserRoomSessionSeq, error) {
	var sessions []*UserRoomSessionSeq
	err := db.GetDB().
		Table("user_room_session AS urs").
		Select("urs.rid", "urs.last_seq_id").
		Where("urs.uid = ?", uid).
		Where("urs.state = ?", 1).
		Where("urs.delete_time = 0").
		Find(&sessions).Error
	if err != nil {
		log.Errorf("查询用户会话最近seq_id失败 uid=%s: %v", uid, err)
		return nil, err
	}
	return sessions, nil
}

// GetUserRoomSession 按 uid+rid 查询单条 user_room_session 记录。
func GetUserRoomSession(uid string, rid string) (*types.UserRoomSession, error) {
	var session types.UserRoomSession
	err := db.GetDB().
		Table("user_room_session AS urs").
		Where("urs.uid = ?", uid).
		Where("urs.rid = ?", rid).
		Where("urs.state = ?", 1).
		Where("urs.delete_time = ?", 0).
		Find(&session).Error
	if err != nil {
		log.Errorf("查询用户房间会话失败 uid=%s rid=%s: %v", uid, rid, err)
		return nil, err
	}
	return &session, nil
}

// UpdateUserRoomSessionLastSeqId 更新用户房间会话的最后已读 seq_id，并重新计算未读数
func UpdateUserRoomSessionLastSeqId(uid string, rid string, seqId int64) error {
	err := db.GetDB().
		Table("user_room_session").
		Where("uid = ?", uid).
		Where("rid = ?", rid).
		Where("state = ?", 1).
		Where("delete_time = ?", 0).
		Updates(map[string]any{
			"last_seq_id": seqId,
		}).Error
	if err != nil {
		log.Errorf("更新用户会话最后seq_id失败 uid=%s rid=%s seq_id=%d: %v", uid, rid, seqId, err)
		return err
	}
	return nil
}

func UpdateUserRoomSessionLastMentionSeqId(uid string, rid string, mentionSeqId int64) error {
	err := db.GetDB().
		Table("user_room_session").
		Where("uid = ?", uid).
		Where("rid = ?", rid).
		Where("state = ?", 1).
		Where("delete_time = ?", 0).
		Updates(map[string]any{
			"last_mention_seq_id": mentionSeqId,
		}).Error
	if err != nil {
		log.Errorf("更新用户会话最后@seq_id失败 uid=%s rid=%s mention_seq_id=%d: %v", uid, rid, mentionSeqId, err)
		return err
	}
	return nil
}

// SetUserRoomSessionTop 设置会话置顶；置顶时取消该用户其它会话的置顶（同时仅一条置顶）。
func SetUserRoomSessionTop(uid string, rid string, isTop bool) error {
	tx := db.GetDB().Begin()
	if tx.Error != nil {
		return tx.Error
	}
	if isTop {
		if err := tx.Model(&types.UserRoomSession{}).
			Where("uid = ?", uid).
			Where("delete_time = ?", 0).
			Update("is_top", false).Error; err != nil {
			tx.Rollback()
			log.Errorf("取消用户其它会话置顶失败 uid=%s: %v", uid, err)
			return err
		}
	}
	res := tx.Model(&types.UserRoomSession{}).
		Where("uid = ?", uid).
		Where("rid = ?", rid).
		Where("state = ?", 1).
		Where("delete_time = ?", 0).
		Update("is_top", isTop)
	if res.Error != nil {
		tx.Rollback()
		log.Errorf("更新会话置顶失败 uid=%s rid=%s is_top=%v: %v", uid, rid, isTop, res.Error)
		return res.Error
	}
	if res.RowsAffected == 0 {
		tx.Rollback()
		return gorm.ErrRecordNotFound
	}
	return tx.Commit().Error
}
