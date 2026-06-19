package query

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/redis"
)

type RoomMuteConfigRuleParams struct {
	IsMuteAll   bool
	Reason      string
	RuleType    int8
	RuleConfig  string
	AllowRoles  []int8
	ExceptUsers []string
	EffectiveAt int64
	ExpiresAt   int64
	IsActive    bool
}

// RuleType 位掩码：1=时间段 2=频率 4=智能，0 或未设任何位=永久
const (
	RuleTypeBitTimeRange = 1
	RuleTypeBitFrequency = 2
	RuleTypeBitSmart     = 4
)

func isMuteRuleWindowActive(ruleType int8, ruleConfig string, now int64) bool {
	if (ruleType & RuleTypeBitTimeRange) == 0 || ruleConfig == "" {
		return true
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(ruleConfig), &cfg); err != nil {
		return true
	}
	startAt, _ := cfg["start_at"].(float64)
	endAt, _ := cfg["end_at"].(float64)
	if int64(startAt) > 0 && now < int64(startAt) {
		return false
	}
	if int64(endAt) > 0 && now > int64(endAt) {
		return false
	}
	return true
}

const (
	redisKeyRoomMuteFreqVersionPrefix = "room_mute_freq_version:"
	redisKeyRoomMuteFreqPrefix        = "room_mute_freq:"
)

// parseFrequencyRuleConfig 解析频率规则，返回 windowMs、windowSec、maxMsg、单位中文、是否有效
func parseFrequencyRuleConfig(ruleConfig string) (windowMs, windowSec int64, maxMsg int64, unitZh string, ok bool) {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(ruleConfig), &cfg); err != nil || cfg == nil {
		return 0, 0, 0, "", false
	}
	unit, _ := cfg["interval_unit"].(string)
	val, _ := cfg["interval_value"].(float64)
	maxMsgF, _ := cfg["max_messages"].(float64)
	if unit == "" || val <= 0 || maxMsgF <= 0 {
		return 0, 0, 0, "", false
	}
	switch unit {
	case "minute":
		windowMs = int64(val) * 60 * 1000
		windowSec = int64(val) * 60
		unitZh = "分钟"
	case "hour":
		windowMs = int64(val) * 60 * 60 * 1000
		windowSec = int64(val) * 60 * 60
		unitZh = "小时"
	case "day":
		windowMs = int64(val) * 24 * 60 * 60 * 1000
		windowSec = int64(val) * 24 * 60 * 60
		unitZh = "天"
	default:
		return 0, 0, 0, "", false
	}
	return windowMs, windowSec, int64(maxMsgF), unitZh, true
}

// isUserOverFrequencyLimit 判断用户是否触发频率限制（基于 Redis 计数，不查表）
func isUserOverFrequencyLimit(rid, uid string, now int64, ruleConfig string) (over bool, reason string) {
	windowMs, _, maxMsg, _, ok := parseFrequencyRuleConfig(ruleConfig)
	if !ok || windowMs <= 0 {
		return false, ""
	}
	versionKey := redisKeyRoomMuteFreqVersionPrefix + rid
	v, err := redis.GetInt64(versionKey)
	if err != nil {
		v = 0
	}
	windowID := now / windowMs
	key := fmt.Sprintf("%s%s:%d:%s:%d", redisKeyRoomMuteFreqPrefix, rid, v, uid, windowID)
	count, err := redis.GetInt64(key)
	if err != nil || count < maxMsg {
		return false, ""
	}
	return true, "发言频率受到限制，具体查看策略禁言"
}

// GetFrequencyMuteUntil 若当前用户因频率被禁言，返回本窗口结束时间戳（毫秒），用于前端倒计时与队列定时
func GetFrequencyMuteUntil(rid, uid string, now int64, ruleConfig string) (muteUntilMs int64, ok bool) {
	windowMs, _, maxMsg, _, ok := parseFrequencyRuleConfig(ruleConfig)
	if !ok || windowMs <= 0 {
		return 0, false
	}
	versionKey := redisKeyRoomMuteFreqVersionPrefix + rid
	v, err := redis.GetInt64(versionKey)
	if err != nil {
		v = 0
	}
	windowID := now / windowMs
	key := fmt.Sprintf("%s%s:%d:%s:%d", redisKeyRoomMuteFreqPrefix, rid, v, uid, windowID)
	count, err := redis.GetInt64(key)
	if err != nil || count < maxMsg {
		return 0, false
	}
	return (windowID + 1) * windowMs, true
}

// IncrRoomMessageFrequencyCount 发送消息后增加频率计数（仅当该房间启用了频率限制时）
func IncrRoomMessageFrequencyCount(rid, uid string) {
	cfg, err := GetRoomMuteConfig(rid)
	if err != nil || (cfg.RuleType&RuleTypeBitFrequency) == 0 || cfg.RuleConfig == "" {
		return
	}
	windowMs, windowSec, _, _, ok := parseFrequencyRuleConfig(cfg.RuleConfig)
	if !ok || windowMs <= 0 {
		return
	}
	now := time.Now().UnixMilli()
	windowID := now / windowMs
	versionKey := redisKeyRoomMuteFreqVersionPrefix + rid
	v, err := redis.GetInt64(versionKey)
	if err != nil {
		v = 0
	}
	key := fmt.Sprintf("%s%s:%d:%s:%d", redisKeyRoomMuteFreqPrefix, rid, v, uid, windowID)
	_, _ = redis.Incr(key)
	_ = redis.Expire(key, time.Duration(windowSec+60)*time.Second)
}

// ResetRoomMuteFrequencyCount 规则更新时重置该房间的频率计数（通过递增版本使旧 key 失效）
func ResetRoomMuteFrequencyCount(rid string) {
	versionKey := redisKeyRoomMuteFreqVersionPrefix + rid
	_, _ = redis.Incr(versionKey)
}

// GetRoomUser 获取房间成员信息（含角色、禁言截止时间）
func GetRoomUser(rid, uid string) (*types.RoomUser, error) {
	var ru types.RoomUser
	err := db.GetDB().Where("rid = ? AND uid = ? AND delete_time = 0", rid, uid).First(&ru).Error
	if err != nil {
		return nil, err
	}
	return &ru, nil
}

// TouchRoomUserLastSpeak 更新成员最后发言时间与 IP（仅当新时间更晚时写入）。
func TouchRoomUserLastSpeak(rid, uid string, speakTime int64, speakIP string) error {
	return db.GetDB().Model(&entity.RoomUser{}).
		Where("rid = ? AND uid = ? AND delete_time = 0 AND last_speak_time < ?", rid, uid, speakTime).
		Updates(map[string]any{
			"last_speak_time": speakTime,
			"last_speak_ip":   speakIP,
		}).Error
}

// GetRoomUsers 获取房间内所有成员（含角色、禁言截止时间）
func GetRoomUsers(rid string) ([]*types.RoomUser, error) {
	var list []*types.RoomUser
	err := db.GetDB().Where("rid = ? AND delete_time = 0", rid).Find(&list).Error
	return list, err
}

// 禁言错误码，供客户端按码展示固定文案，避免刷新后文案被覆盖
const (
	MuteCodeMuted          = "MUTED"           // 个人禁言或全体禁言
	MuteCodeTimeRange      = "MUTED_TIME_RANGE" // 策略禁言-时间段/角色不允许
	MuteCodeFrequency      = "MUTED_FREQUENCY"  // 策略禁言-频率限制
)

// IsUserMutedInRoom 判断用户在该房间是否被禁言（含个人禁言与全体禁言）。
// 返回 (是否禁言, 错误码)。错误码用于客户端固定文案：MUTED / MUTED_TIME_RANGE / MUTED_FREQUENCY。
func IsUserMutedInRoom(rid, uid string) (bool, string) {
	now := time.Now().UnixMilli()
	// 1. 先查询房间成员信息（角色 + 个人禁言）
	var ru types.RoomUser
	if err := db.GetDB().Where("rid = ? AND uid = ? AND delete_time = 0", rid, uid).First(&ru).Error; err == nil && ru.MuteUntil > 0 && ru.MuteUntil > now {
		return true, MuteCodeMuted
	}
	// 2. 检查房间禁言配置（全体禁言 / 策略禁言）
	var cfg entity.RoomMuteConfig
	if err := db.GetDB().Where("rid = ? AND delete_time = 0", rid).First(&cfg).Error; err == nil {
		// 全体禁言是独立模式：只看开关，不看时间/角色白名单/例外用户
		isMuteAllActive := cfg.IsMuteAll
		isStrategyMuteActive := !cfg.IsMuteAll && cfg.IsActive
		if isStrategyMuteActive {
			// 仅当启用了“时间段”时校验生效/过期时间；永久(rule_type=0)不校验
			if (cfg.RuleType & RuleTypeBitTimeRange) != 0 {
				if cfg.EffectiveAt > 0 && now < cfg.EffectiveAt {
					isStrategyMuteActive = false
				}
				if cfg.ExpiresAt > 0 && now > cfg.ExpiresAt {
					isStrategyMuteActive = false
				}
			}
			if !isMuteRuleWindowActive(cfg.RuleType, cfg.RuleConfig, now) {
				isStrategyMuteActive = false
			}
		}
		if !isMuteAllActive && !isStrategyMuteActive {
			return false, ""
		}
		if isMuteAllActive {
			if ru.Role == entity.RoomUserRoleAdmin || ru.Role == entity.RoomUserRoleOwner {
				return false, ""
			}
			return true, MuteCodeMuted
		}
		// 策略禁言：先按例外与角色判断是否允许发言，再对允许发言者做频率校验
		canSpeakByExcept := false
		if cfg.ExceptUsers != "" {
			var exceptUsers []string
			if err := json.Unmarshal([]byte(cfg.ExceptUsers), &exceptUsers); err == nil {
				for _, exceptUid := range exceptUsers {
					if exceptUid == uid {
						canSpeakByExcept = true
						break
					}
				}
			}
		}
		canSpeakByRole := false
		allowRoles := []int8{}
		if cfg.AllowRoles != "" {
			var parsed []int8
			if err := json.Unmarshal([]byte(cfg.AllowRoles), &parsed); err == nil && len(parsed) > 0 {
				allowRoles = parsed
			}
		}
		for _, role := range allowRoles {
			if int8(ru.Role) == role {
				canSpeakByRole = true
				break
			}
		}
		if !canSpeakByExcept && !canSpeakByRole {
			return true, MuteCodeTimeRange
		}
		// 允许发言者仍受频率限制
		if (cfg.RuleType & RuleTypeBitFrequency) != 0 && cfg.RuleConfig != "" {
			if over, _ := isUserOverFrequencyLimit(cfg.Rid, uid, now, cfg.RuleConfig); over {
				return true, MuteCodeFrequency
			}
		}
		return false, ""
	}
	return false, ""
}

// CanUserMuteInRoom 判断用户是否有权限在该房间执行禁言操作（仅管理员、房主）
func CanUserMuteInRoom(rid, uid string) bool {
	ru, err := GetRoomUser(rid, uid)
	if err != nil || ru == nil {
		return false
	}
	return ru.Role == entity.RoomUserRoleAdmin || ru.Role == entity.RoomUserRoleOwner
}

// MuteUserInRoom 禁言指定用户（更新 room_user.mute_until）
func MuteUserInRoom(rid, targetUid, operatorUid string, durationSec int64, reason string) error {
	until := time.Now().Add(time.Duration(durationSec) * time.Second).UnixMilli()
	tx := db.GetDB().Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var ru types.RoomUser
	if err := tx.Where("rid = ? AND uid = ? AND delete_time = 0", rid, targetUid).First(&ru).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Model(&types.RoomUser{}).Where("rid = ? AND uid = ?", rid, targetUid).Updates(map[string]any{
		"mute_until":        until,
		"mute_operator_uid": operatorUid,
	}).Error; err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}

// UnmuteUserInRoom 解除用户禁言
func UnmuteUserInRoom(rid, targetUid, operatorUid string, reason string) error {
	tx := db.GetDB().Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var ru types.RoomUser
	if err := tx.Where("rid = ? AND uid = ? AND delete_time = 0", rid, targetUid).First(&ru).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Model(&types.RoomUser{}).Where("rid = ? AND uid = ?", rid, targetUid).Updates(map[string]any{
		"mute_until":        0,
		"mute_operator_uid": "",
	}).Error; err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}

// GetRoomMuteConfig 获取房间禁言配置（创建房间时已创建对应配置）
func GetRoomMuteConfig(rid string) (*entity.RoomMuteConfig, error) {
	var cfg entity.RoomMuteConfig
	err := db.GetDB().Where("rid = ? AND delete_time = 0", rid).First(&cfg).Error
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SetRoomMuteAll 设置/取消全体禁言
func SetRoomMuteAll(rid string, mute bool, byUid, reason string) error {
	defaultAllowRoles := []int8{int8(entity.RoomUserRoleAdmin), int8(entity.RoomUserRoleOwner)}
	return SetRoomMuteConfigRule(rid, byUid, RoomMuteConfigRuleParams{
		IsMuteAll:   mute,
		Reason:      reason,
		RuleType:    0,
		RuleConfig:  "{}",
		AllowRoles:  defaultAllowRoles,
		ExceptUsers: []string{},
		EffectiveAt: time.Now().UnixMilli(),
		ExpiresAt:   0,
		// 全体禁言和策略禁言互斥：开启全体禁言时关闭策略开关
		IsActive: false,
	})
}

func SetRoomMuteConfigRule(rid string, byUid string, params RoomMuteConfigRuleParams) error {
	cfg, err := GetRoomMuteConfig(rid)
	if err != nil {
		return err
	}
	if params.RuleConfig == "" {
		params.RuleConfig = "{}"
	}
	if params.AllowRoles == nil {
		params.AllowRoles = []int8{}
	}
	if params.ExceptUsers == nil {
		params.ExceptUsers = []string{}
	}
	allowRolesBytes, _ := json.Marshal(params.AllowRoles)
	exceptUsersBytes, _ := json.Marshal(params.ExceptUsers)
	updates := map[string]any{
		"is_mute_all":     params.IsMuteAll,
		"mute_all_by":     byUid,
		"mute_all_reason": params.Reason,
		"rule_type":       params.RuleType,
		"rule_config":     params.RuleConfig,
		"allow_roles":     string(allowRolesBytes),
		"except_users":    string(exceptUsersBytes),
		"effective_at":    params.EffectiveAt,
		"expires_at":      params.ExpiresAt,
		"is_active":       params.IsActive,
		"version":         cfg.Version + 1,
	}
	if err := db.GetDB().Model(&entity.RoomMuteConfig{}).Where("rid = ?", rid).Updates(updates).Error; err != nil {
		return err
	}
	// 规则更新后重置该房间频率计数（使旧窗口 key 失效）
	ResetRoomMuteFrequencyCount(rid)
	return nil
}
