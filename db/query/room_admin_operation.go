package query

import (
	"encoding/json"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
)

// ListRoomAvatarHistory 查询房间头像变更记录中的头像 uf_id 列表（按操作时间倒序，去重，最多 limit 条）
func ListRoomAvatarHistory(rid string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 20
	}
	var records []entity.RoomAdminOperation
	err := db.GetDB().Where("rid = ? AND op_type = ?", rid, entity.RoomAdminOpRoomAvatarUpdate).
		Order("id DESC").Limit(limit * 2).Find(&records).Error
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var list []string
	for _, r := range records {
		for _, raw := range []string{r.AfterData, r.BeforeData} {
			var m map[string]any
			if _ = json.Unmarshal([]byte(raw), &m); m == nil {
				continue
			}
			if v, _ := m["avatar_uf_id"]; v != nil {
				if s, ok := v.(string); ok && s != "" && !seen[s] {
					seen[s] = true
					list = append(list, s)
				}
			}
		}
		if len(list) >= limit {
			break
		}
	}
	if len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}

// CreateRoomAdminOperation 记录房间管理员操作（改群名、改群头像、撤回消息、改成员权限、禁言等）
// sid 为 UserSession.Sid，与 uid 同存于 token，不能为空。
func CreateRoomAdminOperation(rid string, opType entity.RoomAdminOperationType, operatorUid, sid, relatedId string, beforeData, afterData map[string]any) error {
	beforeJSON := "{}"
	if beforeData != nil {
		b, _ := json.Marshal(beforeData)
		beforeJSON = string(b)
	}
	afterJSON := "{}"
	if afterData != nil {
		b, _ := json.Marshal(afterData)
		afterJSON = string(b)
	}
	record := &entity.RoomAdminOperation{
		Rid:         rid,
		OpType:      opType,
		OperatorUid: operatorUid,
		Sid:         sid,
		RelatedId:   relatedId,
		BeforeData:  beforeJSON,
		AfterData:   afterJSON,
	}
	return db.GetDB().Create(record).Error
}
