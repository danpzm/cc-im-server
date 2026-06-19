package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/redis/go-redis/v9"
	"github.com/rs/xid"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	appredis "github.com/xd/quic-server/redis"
	"gorm.io/gorm"
)

const userStatusRedisKeyPrefix = "USER:STATUS"

// ErrPresenceRequiresOnline 用户未建立长连接时不能修改展示状态
var ErrPresenceRequiresOnline = errors.New("当前未在线，无法设置展示状态")

func getUserStatusRedisKey(uid string) string {
	return fmt.Sprintf("%s:%s", userStatusRedisKeyPrefix, uid)
}

// updateUserRoomOnlineStatus 在 Redis 中更新用户所在所有房间的在线集合
func updateUserRoomOnlineStatus(uid string, isOnline bool) error {
	roomIds, err := GetRoomIdsByUid(uid)
	if err != nil {
		return err
	}
	if len(roomIds) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), appredis.REDIS_TIMEOUT)
	defer cancel()

	client := appredis.GetClient()
	for _, rid := range roomIds {
		key := ROOM_ONLINE_USERS_KEY + ":" + rid
		var cmdErr error
		if isOnline {
			cmdErr = client.SAdd(ctx, key, uid).Err()
		} else {
			cmdErr = client.SRem(ctx, key, uid).Err()
		}
		if cmdErr != nil {
			log.Errorf("更新房间在线成员失败 rid=%s uid=%s: %v", rid, uid, cmdErr)
		}
	}

	return nil
}

// CreateUserSession 创建用户会话
func CreateUserSession(uid, deviceId, deviceFinger, platform, loginIP, userAgent string, sessionData entity.SessionDataJSON) (*entity.UserSession, error) {
	now := time.Now().UnixMilli()
	session := &entity.UserSession{
		Uid:           uid,
		DeviceId:      deviceId,
		DeviceFinger:  deviceFinger,
		Platform:      platform,
		LoginTime:     now,
		LastActivity:  now,
		IsActive:      true,
		IsExpired:     false,
		LoginIP:       loginIP,
		UserAgent:     userAgent,
		ClientVersion: sessionData.ClientVersion,
		Notification:  sessionData.Notification,
		SessionData:   sessionData,
		ExpiresAt:     0, // 可以根据需要设置过期时间
	}

	if err := db.GetDB().Create(session).Error; err != nil {
		log.Errorf("创建用户会话失败 uid=%s: %v", uid, err)
		return nil, err
	}

	log.Infof("创建用户会话成功 uid=%s, sid=%s", uid, session.Sid)
	return session, nil
}

// UpdateUserSessionLogout 更新用户会话登出信息
func UpdateUserSessionLogout(sid string, reason string) error {
	now := time.Now().UnixMilli()
	result := db.GetDB().Model(&entity.UserSession{}).
		Where("sid = ?", sid).
		Updates(map[string]any{
			"logout_time": now,
			"is_active":   false,
			"is_expired":  true,
			"reason":      reason,
			"update_time": now,
		})

	if result.Error != nil {
		log.Errorf("更新用户会话登出信息失败 sid=%s: %v", sid, result.Error)
		return result.Error
	}

	if result.RowsAffected == 0 {
		log.Warnf("未找到要更新的会话 sid=%s", sid)
	}

	return nil
}

// GetUserSessionBySid 根据会话ID获取会话
func GetUserSessionBySid(sid string) (*entity.UserSession, error) {
	var session entity.UserSession
	err := db.GetDB().Where("sid = ?", sid).First(&session).Error
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// TouchUserSessionActivity 刷新会话最后活动时间（token 轮换时续期，不新建 sid）
func TouchUserSessionActivity(sid string) error {
	now := time.Now().UnixMilli()
	result := db.GetDB().Model(&entity.UserSession{}).
		Where("sid = ? AND is_active = ? AND is_expired = ?", sid, true, false).
		Update("last_activity", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ValidateActiveUserSession 校验 sid 是否属于 uid 且会话有效（未登出、未过期）
func ValidateActiveUserSession(uid, sid string) (bool, error) {
	session, err := GetUserSessionBySid(sid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if session.Uid != uid {
		return false, nil
	}
	if !session.IsActive || session.IsExpired {
		return false, nil
	}
	if session.ExpiresAt > 0 && time.Now().UnixMilli() > session.ExpiresAt {
		return false, nil
	}
	return true, nil
}

// GetUserCurrentStatus 获取用户当前状态（优先从 Redis 读取，Redis 没有时再查数据库，如库中也没有则创建）
func GetOrCreateUserCurrentStatus(uid string) (*entity.UserCurrentStatus, error) {
	key := getUserStatusRedisKey(uid)

	// 1. 先从 Redis 获取
	statusFromCache, err := appredis.Get[entity.UserCurrentStatus](key)
	if err == nil && statusFromCache.Uid != "" {
		return &statusFromCache, nil
	}
	if err != nil && err != redis.Nil {
		log.Errorf("从 Redis 获取用户当前状态失败 uid=%s: %v", uid, err)
	}

	// 2. Redis 没有，再从数据库查询
	var status entity.UserCurrentStatus
	dbErr := db.GetDB().Where("uid = ?", uid).First(&status).Error

	// 3. 如果数据库中也不存在，则创建默认记录
	if dbErr != nil {
		status = entity.UserCurrentStatus{
			Uid:               uid,
			IsOnline:          true,
			CurrentStatus:     "offline",
			LastOnline:        0,
			LastLogin:         0,
			LastLogout:        0,
			LastHeartbeat:     0,
			CustomState:       "",
			CurrentSessionId:  "",
			WebsocketId:       "",
			DeviceInfo:        entity.DeviceInfoJSON{},
			ConcurrentDevices: 0,
			TotalOnlineToday:  0,
			IP:                "",
		}
		if err := db.GetDB().Create(&status).Error; err != nil {
			log.Errorf("创建用户当前状态失败 uid=%s: %v", uid, err)
			return nil, err
		}
		log.Infof("创建用户当前状态成功 uid=%s", uid)
	}

	// 4. 同步到 Redis（不过期）
	if err := appredis.Set(key, status, 0); err != nil {
		log.Errorf("同步用户当前状态到 Redis 失败 uid=%s: %v", uid, err)
	}

	return &status, nil
}

// UpdateUserCurrentStatusOnline 更新用户当前状态为在线
func UpdateUserCurrentStatusOnline(uid, sid, websocketId, ip string, deviceInfo entity.DeviceInfoJSON) error {
	now := time.Now().UnixMilli()

	// 先获取当前状态（会优先从 Redis 读取）
	status, err := GetOrCreateUserCurrentStatus(uid)
	if err != nil {
		log.Errorf("获取用户当前状态失败（在线更新） uid=%s: %v", uid, err)
		return err
	}

	// 登录时恢复/保留展示状态，不强制重置为 online（兼容历史数据 current_status=offline 的场景）。
	nextDisplayStatus := strings.TrimSpace(status.CurrentStatus)
	if nextDisplayStatus == "" || nextDisplayStatus == "offline" {
		nextDisplayStatus = "online"
	}

	// 更新内存中的状态
	status.IsOnline = true
	status.CurrentStatus = nextDisplayStatus
	status.LastOnline = now
	status.LastLogin = now
	status.LastHeartbeat = now
	status.CurrentSessionId = sid
	status.WebsocketId = websocketId
	status.Platform = deviceInfo.Platform
	status.DeviceType = deviceInfo.DeviceType
	status.DeviceModel = deviceInfo.DeviceModel
	status.OSVersion = deviceInfo.OSVersion
	status.AppVersion = deviceInfo.AppVersion
	status.DeviceInfo = deviceInfo
	status.IP = ip

	// 先更新 Redis
	if err := appredis.Set(getUserStatusRedisKey(uid), status, 0); err != nil {
		log.Errorf("更新用户当前状态到 Redis 失败 uid=%s: %v", uid, err)
	}

	// 再更新数据库
	result := db.GetDB().Model(&entity.UserCurrentStatus{}).
		Where("uid = ?", uid).
		Updates(map[string]any{
			"is_online":          true,
			"current_status":     nextDisplayStatus,
			"custom_state":       status.CustomState,
			"last_online":        now,
			"last_login":         now,
			"last_heartbeat":     now,
			"current_session_id": sid,
			"websocket_id":       websocketId,
			// 从 DeviceInfo 提取字段
			"platform":     deviceInfo.Platform,
			"device_type":  deviceInfo.DeviceType,
			"device_model": deviceInfo.DeviceModel,
			"os_version":   deviceInfo.OSVersion,
			"app_version":  deviceInfo.AppVersion,
			// 保留完整的设备信息
			"device_info": deviceInfo,
			"ip":          ip,
			"update_time": now,
		})

	if result.Error != nil {
		log.Errorf("更新用户当前状态为在线失败 uid=%s: %v", uid, result.Error)
		return result.Error
	}

	if result.RowsAffected == 0 {
		log.Warnf("未找到要更新的用户状态 uid=%s", uid)
	}

	// 更新房间在线成员信息（Redis）
	if err := updateUserRoomOnlineStatus(uid, true); err != nil {
		log.Errorf("更新房间在线成员信息失败 uid=%s: %v", uid, err)
	}

	return nil
}

// UpdateUserCurrentStatusOffline 更新用户当前状态为离线
func UpdateUserCurrentStatusOffline(uid string, reason string) error {
	now := time.Now().UnixMilli()

	// 先获取当前状态（优先从 Redis 读取），以便记录最后在线时间
	currentStatus, err := GetOrCreateUserCurrentStatus(uid)
	if err != nil {
		log.Errorf("获取用户当前状态失败 uid=%s: %v", uid, err)
		return err
	}

	// 下线时只改在线标记，不覆盖展示状态；上线后可恢复用户上次自定义状态。
	currentStatus.IsOnline = false
	currentStatus.LastLogout = now
	currentStatus.CurrentSessionId = ""
	currentStatus.WebsocketId = ""

	// 先更新 Redis
	if err := appredis.Set(getUserStatusRedisKey(uid), currentStatus, 0); err != nil {
		log.Errorf("更新用户当前状态为离线到 Redis 失败 uid=%s: %v", uid, err)
	}

	result := db.GetDB().Model(&entity.UserCurrentStatus{}).
		Where("uid = ?", uid).
		Updates(map[string]any{
			"is_online":          false,
			"last_logout":        now,
			"current_session_id": "",
			"websocket_id":       "",
			"update_time":        now,
		})

	if result.Error != nil {
		log.Errorf("更新用户当前状态为离线失败 uid=%s: %v", uid, result.Error)
		return result.Error
	}

	if result.RowsAffected == 0 {
		log.Warnf("未找到要更新的用户状态 uid=%s", uid)
	}

	// 更新房间在线成员信息（Redis）
	if err := updateUserRoomOnlineStatus(uid, false); err != nil {
		log.Errorf("更新房间在线成员信息失败 uid=%s: %v", uid, err)
	}

	return nil
}

// UpdateUserPresenceDisplay 更新在线用户的展示状态（在线/离开/忙碌等 + 自定义文案），并同步 Redis + DB。
// 注意：是否允许修改由调用方基于“物理连接”独立判断；本函数仅负责展示状态写入。
func UpdateUserPresenceDisplay(uid, currentStatus, customState string) error {
	st, err := GetOrCreateUserCurrentStatus(uid)
	if err != nil {
		return err
	}
	if !st.IsOnline {
		return ErrPresenceRequiresOnline
	}
	currentStatus = strings.TrimSpace(currentStatus)
	customState = strings.TrimSpace(customState)
	if currentStatus == "" {
		currentStatus = "online"
	}
	if utf8.RuneCountInString(currentStatus) > 32 {
		return fmt.Errorf("状态标识过长")
	}
	if utf8.RuneCountInString(customState) > 10 {
		return fmt.Errorf("自定义状态不能超过10个字")
	}
	now := time.Now().UnixMilli()
	st.CurrentStatus = currentStatus
	st.CustomState = customState
	if err := appredis.Set(getUserStatusRedisKey(uid), st, 0); err != nil {
		log.Errorf("更新展示状态到 Redis 失败 uid=%s: %v", uid, err)
	}
	res := db.GetDB().Model(&entity.UserCurrentStatus{}).Where("uid = ?", uid).Updates(map[string]any{
		"current_status": currentStatus,
		"custom_state":   customState,
		"update_time":    now,
	})
	if res.Error != nil {
		return res.Error
	}
	return nil
}

// UpdateUserCurrentStatusHeartbeat 更新用户心跳时间
func UpdateUserCurrentStatusHeartbeat(uid string) error {
	now := time.Now().UnixMilli()

	// 先获取当前状态（优先从 Redis）
	status, err := GetOrCreateUserCurrentStatus(uid)
	if err != nil {
		log.Errorf("获取用户当前状态失败（心跳） uid=%s: %v", uid, err)
		return err
	}

	// 更新内存状态
	status.LastHeartbeat = now

	// 先更新 Redis
	if err := appredis.Set(getUserStatusRedisKey(uid), status, 0); err != nil {
		log.Errorf("更新用户心跳到 Redis 失败 uid=%s: %v", uid, err)
	}

	result := db.GetDB().Model(&entity.UserCurrentStatus{}).
		Where("uid = ?", uid).
		Updates(map[string]any{
			"last_heartbeat": now,
			"update_time":    now,
		})

	if result.Error != nil {
		log.Errorf("更新用户心跳时间失败 uid=%s: %v", uid, result.Error)
		return result.Error
	}

	return nil
}

// CreateUserOnlineHistory 创建用户在线历史记录
func CreateUserOnlineHistory(sid, uid, eventType, eventSubtype, statusBefore, statusAfter, reason string, deviceInfo entity.DeviceInfoJSON, networkInfo entity.NetworkInfoJSON, locationInfo entity.LocationInfoJSON, appState entity.AppStateJSON) error {
	now := time.Now().UnixMilli()
	history := &entity.UserOnlineHistory{
		Hid:          xid.New().String(),
		Sid:          sid,
		Uid:          uid,
		EventType:    eventType,
		EventSubtype: eventSubtype,
		StatusBefore: statusBefore,
		StatusAfter:  statusAfter,
		Reason:       reason,
		EventTime:    now,
		// 从 DeviceInfo 提取字段
		Platform:    deviceInfo.Platform,
		DeviceType:  deviceInfo.DeviceType,
		DeviceModel: deviceInfo.DeviceModel,
		OSVersion:   deviceInfo.OSVersion,
		// 从 LocationInfo 提取字段
		IP:        locationInfo.IP,
		Country:   locationInfo.Country,
		CountryEn: locationInfo.CountryEn,
		Region:    locationInfo.Region,
		RegionEn:  locationInfo.RegionEn,
		City:      locationInfo.City,
		CityEn:    locationInfo.CityEn,
		Latitude:  locationInfo.Latitude,
		Longitude: locationInfo.Longitude,
		Timezone:  locationInfo.Timezone,
		// 从 NetworkInfo 提取字段
		NetworkType:    networkInfo.Type,
		ISP:            networkInfo.ISP,
		NetworkSignal:  networkInfo.Signal,
		NetworkLatency: networkInfo.Latency,
		// 从 AppState 提取字段
		IsForeground: appState.IsForeground,
		BatteryLevel: appState.BatteryLevel,
		IsCharging:   appState.IsCharging,
		// 保留完整的 JSON 数据
		DeviceInfo:   deviceInfo,
		NetworkInfo:  networkInfo,
		LocationInfo: locationInfo,
		AppState:     appState,
	}

	if err := db.GetDB().Create(history).Error; err != nil {
		log.Errorf("创建用户在线历史记录失败 uid=%s: %v", uid, err)
		return err
	}

	log.Infof("创建用户在线历史记录成功 uid=%s, event_type=%s", uid, eventType)
	return nil
}

// GetOrCreateUserDeviceSession 获取或创建设备会话映射（如果不存在则创建，存在则更新）
func GetOrCreateUserDeviceSession(uid, deviceId, deviceFinger, sid, platform, deviceName string, locationInfo entity.LocationInfoJSON) (*entity.UserDeviceSession, error) {
	now := time.Now().UnixMilli()
	var deviceSession entity.UserDeviceSession
	err := db.GetDB().Where("uid = ? AND device_id = ?", uid, deviceId).First(&deviceSession).Error

	if err != nil {
		// 不存在，创建新记录
		deviceSession = entity.UserDeviceSession{
			Uid:           uid,
			DeviceId:      deviceId,
			DeviceFinger:  deviceFinger,
			CurrentSid:    sid,
			Platform:      platform,
			DeviceName:    deviceName,
			LastLogin:     now,
			LastLogout:    0,
			TotalSessions: 1,
			IsTrusted:     false,
			IsBlocked:     false,
			// 从 MetaInfo 提取字段
			FirstSeen:           now,
			LoginCount:          1,
			TotalOnline:         0,
			AvgDuration:         0,
			LastLocationIP:      locationInfo.IP,
			LastLocationCountry: locationInfo.Country,
			LastLocationCity:    locationInfo.City,
			// 保留完整的元信息
			MetaInfo: entity.MetaInfoJSON{
				FirstSeen:    now,
				LoginCount:   1,
				TotalOnline:  0,
				AvgDuration:  0,
				LastLocation: locationInfo,
				CustomFields: make(map[string]any),
			},
		}
		if err := db.GetDB().Create(&deviceSession).Error; err != nil {
			log.Errorf("创建设备会话映射失败 uid=%s, device_id=%s: %v", uid, deviceId, err)
			return nil, err
		}
		log.Infof("创建设备会话映射成功 uid=%s, device_id=%s", uid, deviceId)
	} else {
		// 存在，更新记录（同一设备再次登录）
		metaInfo := deviceSession.MetaInfo
		metaInfo.LoginCount++
		metaInfo.LastLocation = locationInfo
		if metaInfo.CustomFields == nil {
			metaInfo.CustomFields = make(map[string]any)
		}

		// 更新设备会话映射：更新当前会话ID、最后登录时间、总会话数等
		result := db.GetDB().Model(&deviceSession).
			Updates(map[string]any{
				"device_finger":  deviceFinger,                    // 更新设备指纹（可能变化）
				"current_sid":    sid,                             // 更新当前会话ID
				"platform":       platform,                        // 更新平台信息（可能变化）
				"device_name":    deviceName,                      // 更新设备名称（可能变化）
				"last_login":     now,                             // 更新最后登录时间
				"total_sessions": deviceSession.TotalSessions + 1, // 增加总会话数
				// 从 MetaInfo 提取字段
				"login_count":           metaInfo.LoginCount,
				"total_online":          metaInfo.TotalOnline,
				"avg_duration":          metaInfo.AvgDuration,
				"last_location_ip":      locationInfo.IP,
				"last_location_country": locationInfo.Country,
				"last_location_city":    locationInfo.City,
				// 保留完整的元信息
				"meta_info":   metaInfo,
				"update_time": now,
			})

		if result.Error != nil {
			log.Errorf("更新设备会话映射失败 uid=%s, device_id=%s: %v", uid, deviceId, result.Error)
			return nil, result.Error
		}
		log.Infof("更新设备会话映射成功 uid=%s, device_id=%s (同一设备再次登录)", uid, deviceId)
	}

	return &deviceSession, nil
}

// UpdateUserDeviceSessionLogout 更新设备会话映射的登出信息
func UpdateUserDeviceSessionLogout(uid, deviceId string, onlineDuration int) error {
	now := time.Now().UnixMilli()
	var deviceSession entity.UserDeviceSession
	err := db.GetDB().Where("uid = ? AND device_id = ?", uid, deviceId).First(&deviceSession).Error
	if err != nil {
		log.Warnf("未找到设备会话映射 uid=%s, device_id=%s", uid, deviceId)
		return nil // 不存在不算错误
	}

	// 更新元信息中的在线时长
	metaInfo := deviceSession.MetaInfo
	metaInfo.TotalOnline += onlineDuration
	if metaInfo.LoginCount > 0 {
		metaInfo.AvgDuration = float64(metaInfo.TotalOnline) / float64(metaInfo.LoginCount)
	}
	if metaInfo.CustomFields == nil {
		metaInfo.CustomFields = make(map[string]any)
	}

	result := db.GetDB().Model(&deviceSession).
		Updates(map[string]any{
			"last_logout": now,
			"current_sid": "",
			// 从 MetaInfo 提取字段
			"total_online": metaInfo.TotalOnline,
			"avg_duration": metaInfo.AvgDuration,
			// 保留完整的元信息
			"meta_info":   metaInfo,
			"update_time": now,
		})

	if result.Error != nil {
		log.Errorf("更新设备会话映射登出信息失败 uid=%s, device_id=%s: %v", uid, deviceId, result.Error)
		return result.Error
	}

	return nil
}
