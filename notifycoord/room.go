package notifycoord

import (
	"encoding/json"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/redis"
)

func presenceKey(uid string) string {
	return "quic:presence:" + uid
}

func filterRoomRecipients(roomUserIds []string, include []string, exclude []string) []string {
	includeSet := make(map[string]struct{}, len(include))
	excludeSet := make(map[string]struct{}, len(exclude))
	for _, uid := range include {
		if uid != "" {
			includeSet[uid] = struct{}{}
		}
	}
	for _, uid := range exclude {
		if uid != "" {
			excludeSet[uid] = struct{}{}
		}
	}
	useInclude := len(includeSet) > 0
	recipients := make([]string, 0, len(roomUserIds))
	for _, uid := range roomUserIds {
		if uid == "" {
			continue
		}
		if _, blocked := excludeSet[uid]; blocked {
			continue
		}
		if useInclude {
			if _, ok := includeSet[uid]; !ok {
				continue
			}
		}
		recipients = append(recipients, uid)
	}
	return recipients
}

// ParseAndCreateMentions 解析 @ 并写入数据库（与原先 quic Server 行为一致）
func ParseAndCreateMentions(rcm *types.ServerRoomMessage) {
	var mentions []string
	var isAtAll bool
	for _, content := range rcm.Contents {
		if content.Type == types.RoomMessageContentTypeUserAtAll {
			isAtAll = true
			break
		} else if content.Type == types.RoomMessageContentTypeUserAtUser {
			var atUserContent struct {
				Uid string `json:"uid"`
			}
			if err := json.Unmarshal(content.Content, &atUserContent); err == nil && atUserContent.Uid != "" {
				mentions = append(mentions, atUserContent.Uid)
			}
		}
	}
	if isAtAll || len(mentions) > 0 {
		if err := query.CreateRoomMessageMentions(rcm.Rid, rcm.Mid, rcm.SeqId, rcm.SenderUid, mentions, isAtAll); err != nil {
			log.Errorf("创建@我的消息记录失败 rid=%s mid=%s: %v", rcm.Rid, rcm.Mid, err)
		}
	}
}

// PrepareRoomMessageNotify 创建 room_message_ack / @ 记录，并返回应 fanout 的 uid 列表（不含发送方；发送方已在写入链路同连接回执）
func PrepareRoomMessageNotify(mid string, include []string, exclude []string) ([]string, error) {
	if mid == "" {
		return nil, nil
	}
	rcm := query.GetRoomMessageByMid(mid)
	if rcm == nil {
		log.Errorf("查询房间消息失败: %s", mid)
		return nil, nil
	}
	roomUserIds, err := query.GetRoomUserIdsCache(rcm.Rid)
	if err != nil {
		log.Error("查询房间内的用户失败:", err)
		return nil, err
	}
	if len(roomUserIds) == 0 {
		log.Warnf("房间内没有用户，尝试刷新缓存: rid=%s mid=%s", rcm.Rid, rcm.Mid)
		query.SetRoomUserIdsCache(rcm.Rid)
		roomUserIds, err = query.GetRoomUserIdsCache(rcm.Rid)
		if err != nil || len(roomUserIds) == 0 {
			log.Warnf("刷新缓存后房间内仍没有用户: rid=%s", rcm.Rid)
			return nil, nil
		}
	}
	blockedUids, err := query.GetUidsWhoBlockedRoom(rcm.Rid)
	if err != nil {
		log.Warnf("查询已屏蔽该房间用户失败 rid=%s: %v", rcm.Rid, err)
	} else if len(blockedUids) > 0 {
		exclude = append(exclude, blockedUids...)
	}
	// 发送方已在 handlerRoomMessage 写入链路同连接收到 ServerRoomMessage，fanout 仅投递其他成员
	if rcm.SenderUid != "" {
		exclude = append(exclude, rcm.SenderUid)
	}
	recipients := filterRoomRecipients(roomUserIds, include, exclude)
	blockSenderUids, err := query.GetUidsWhoBlockedUserInRoom(rcm.Rid, rcm.SenderUid)
	if err != nil {
		log.Warnf("查询房间内已屏蔽发送者的用户失败 rid=%s sender=%s: %v", rcm.Rid, rcm.SenderUid, err)
	} else if len(blockSenderUids) > 0 {
		blockSet := make(map[string]struct{}, len(blockSenderUids))
		for _, u := range blockSenderUids {
			blockSet[u] = struct{}{}
		}
		filtered := recipients[:0]
		for _, u := range recipients {
			if _, ok := blockSet[u]; !ok {
				filtered = append(filtered, u)
			}
		}
		recipients = filtered
	}
	if len(recipients) == 0 {
		log.Infof("房间消息通知无目标用户，跳过 ACK: rid=%s mid=%s", rcm.Rid, mid)
		return nil, nil
	}
	acks := make([]*types.RoomMessageAck, 0, len(recipients))
	for _, uid := range recipients {
		_, perr := redis.GetString(presenceKey(uid))
		isOffline := perr != nil
		acks = append(acks, &types.RoomMessageAck{
			Rid:       rcm.Rid,
			Uid:       uid,
			Mid:       rcm.Mid,
			SeqId:     rcm.SeqId,
			State:     0,
			IsOffline: isOffline,
		})
	}
	if len(acks) > 0 {
		tx := db.GetDB().Begin()
		if err := tx.Create(acks).Error; err != nil {
			log.Error("添加房间消息ack失败:", err)
			tx.Rollback()
			return nil, err
		}
		if err := tx.Commit().Error; err != nil {
			log.Error("提交事务失败:", err)
			return nil, err
		}
	}
	ParseAndCreateMentions(rcm)
	return recipients, nil
}
