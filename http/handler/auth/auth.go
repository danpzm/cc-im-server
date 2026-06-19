package auth

import (
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/jwt"
	"github.com/xd/quic-server/pkg/geoip"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/utils"
)

// ErrMissingDeviceId 缺少 X-Device-Id 请求头，用于登录/会话类接口必须携带设备标识时返回 400。
var ErrMissingDeviceId = errors.New("missing X-Device-Id header")

// ComputeDeviceFingerprint 服务端统一设备指纹计算：仅用 HMAC 在服务端加密，客户端不可伪造。
// 入参为原始字段（可为空）；IP 取前两段。与 QUIC 复用同一逻辑时需传入相同含义的 deviceIdRaw、ua、lang、ip。
func ComputeDeviceFingerprint(deviceIdRaw, ua, lang, ip string) string {
	if ua == "" {
		ua = "unknown"
	}
	if lang == "" {
		lang = "unknown"
	}
	if parts := strings.SplitN(ip, ".", 3); len(parts) >= 2 {
		ip = parts[0] + "." + parts[1]
	}
	raw := deviceIdRaw + "|" + ua + "|" + lang + "|" + ip
	h := hmac.New(sha256.New, []byte(jwt.JWTKey()))
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// getRequestDeviceFingerprint 从当前 HTTP 请求取头与 IP，交服务端统一计算设备指纹。
// 登录/会话类接口必须传 X-Device-Id，缺失时返回 ErrMissingDeviceId，由调用方返回 400。
func getRequestDeviceFingerprint(c *gin.Context) (string, error) {
	deviceIdRaw := strings.TrimSpace(c.Request.Header.Get("X-Device-Id"))
	if deviceIdRaw == "" {
		return "", ErrMissingDeviceId
	}
	ua := c.Request.UserAgent()
	lang := c.Request.Header.Get("Accept-Language")
	ip := c.ClientIP()
	return ComputeDeviceFingerprint(deviceIdRaw, ua, lang, ip), nil
}

func createDefaultFriendGroup(tx *gorm.DB, uid string) error {
	friendGroup := []types.UserFriendGroup{
		{
			Uid:         uid,
			Name:        "我的好友",
			IsDefault:   true,
			Description: "我的好友",
		},
	}
	return tx.Create(&friendGroup).Error
}
func applyRegisterLocation(user *types.User, registerIP string) {
	user.RegisterIP = registerIP
	region := geoip.Lookup(registerIP)
	user.Country = region.Country
	user.City = region.City
	user.County = region.County
}

func createUserByEmail(email string, password string, registerIP string) (*types.User, error) {
	password, err := utils.PasswordHash(password)
	if err != nil {
		log.Error("Error hashing password: ", err)
		return nil, err
	}
	user := &types.User{
		Username:     email,
		Email:        email,
		Password:     password,
		Nickname:     email,
		AvatarUfId:   "",
		Signature:    "这个人很懒，什么都没留下",
		Introduction: "这个人很懒，什么都没留下",
	}
	applyRegisterLocation(user, registerIP)
	tx := db.GetDB().Begin()
	newId := tx.Create(user)
	if newId.Error != nil {
		log.Error("创建用户失败: ", newId.Error)
		return nil, newId.Error
	}
	if err := createDefaultFriendGroup(tx, user.Uid); err != nil {
		log.Error("创建默认好友分组失败: ", err)
		return nil, err
	}
	tx.Commit()
	return user, nil
}
func createUserByUsername(username string, password string, registerIP string) (*types.User, error) {
	password, err := utils.PasswordHash(password)
	if err != nil {
		log.Error("Error hashing password: ", err)
		return nil, err
	}
	user := &types.User{
		Username:     username,
		Password:     password,
		Nickname:     username,
		AvatarUfId:   "",
		Signature:    "这个人很懒，什么都没留下",
		Introduction: "这个人很懒，什么都没留下",
	}
	applyRegisterLocation(user, registerIP)
	tx := db.GetDB().Begin()
	newId := tx.Create(user)
	if newId.Error != nil {
		log.Error("创建用户失败: ", newId.Error)
		return nil, newId.Error
	}
	if err := createDefaultFriendGroup(tx, user.Uid); err != nil {
		log.Error("创建默认好友分组失败: ", err)
		return nil, err
	}
	tx.Commit()
	return user, nil
}

// createHttpUserSession 为 HTTP 登录创建会话，返回 sid（用于写入 token）；device_id 由服务端根据请求计算，要求请求带 X-Device-Id
func createHttpUserSession(c *gin.Context, uid string) (sid string, err error) {
	deviceId, err := getRequestDeviceFingerprint(c)
	if err != nil {
		return "", err
	}
	sessionData := entity.SessionDataJSON{}
	session, err := query.CreateUserSession(uid, deviceId, "", "http", c.ClientIP(), c.Request.UserAgent(), sessionData)
	if err != nil {
		return "", err
	}
	return session.Sid, nil
}

// generateTokenInternal 签发 access/refresh token。clusterRequired=true（登录）时必须从 Redis 拉到各集群地址；false（刷新 token）时拉取失败仅告警，客户端可继续使用已有地址。
func generateTokenInternal(c *gin.Context, user *types.User, extra gin.H, clusterRequired bool) {
	var cluster loginClusterAddrs
	if clusterRequired {
		var err error
		cluster, err = fetchLoginClusterAddrs(c.Request.Context())
		if err != nil {
			log.Errorf("拉取集群节点失败: %v", err)
			response.ServerError(c, "获取集群节点失败，请稍后重试")
			return
		}
	} else {
		cluster = fetchLoginClusterAddrsBestEffort(c.Request.Context())
	}
	if clusterRequired && len(cluster.Quic) == 0 {
		response.ServerError(c, "集群未返回可用 QUIC 节点")
		return
	}
	if clusterRequired && len(cluster.HTTP) == 0 {
		response.ServerError(c, "集群未返回可用 HTTP 节点")
		return
	}
	if clusterRequired && len(cluster.OSS) == 0 {
		response.ServerError(c, "集群未返回可用 OSS 节点")
		return
	}

	tx := db.GetDB().Begin()
	if err := tx.Model(&types.UserRefreshToken{}).Where("uid = ?", user.Uid).Update("revoked", true).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, err.Error())
		return
	}

	sid, err := createHttpUserSession(c, user.Uid)
	if err != nil {
		tx.Rollback()
		if errors.Is(err, ErrMissingDeviceId) {
			response.BadRequest(c, "缺少 X-Device-Id 请求头，无法绑定设备")
			return
		}
		response.ServerError(c, "创建会话失败")
		return
	}

	access_token, err := jwt.CreateAccessToken(user.Uid, sid)
	if err != nil {
		tx.Rollback()
		response.ServerError(c, "生成身份令牌失败 Error: "+err.Error())
		return
	}
	refresh_token, data, err := jwt.CreateRefreshToken(user.Uid, sid)
	if err != nil {
		tx.Rollback()
		response.ServerError(c, "生成刷新令牌失败")
		return
	}
	if err := tx.Create(&types.UserRefreshToken{
		Revoked:   false,
		Uid:       data.Uid,
		Jti:       data.Jti,
		ExpiresAt: data.ExpiresAt.UnixMilli(),
		UserAgent: c.Request.UserAgent(),
		IP:        c.Request.RemoteAddr,
	}).Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "数据库错误")
		return
	}
	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		response.ServerError(c, "数据库错误")
		return
	}

	payload := gin.H{
		"access_token":   access_token,
		"refresh_token":  refresh_token,
		"quic_addrs":     cluster.Quic,
		"http_base_urls": cluster.HTTP,
		"oss_base_urls":  cluster.OSS,
	}
	for k, v := range extra {
		payload[k] = v
	}
	response.Success(c, payload)
}

func generateToken(c *gin.Context, user *types.User) {
	generateTokenInternal(c, user, nil, true)
}

// generateTokenWithExtra 同 generateToken，可附加额外返回字段（如 remember_token）
func generateTokenWithExtra(c *gin.Context, user *types.User, extra gin.H) {
	generateTokenInternal(c, user, extra, true)
}

// createOrUpdateRememberToken 为 uid 创建或更新记住登录 token，绑定的 device_id 由调用方传入（须为服务端计算的指纹，不信任客户端）
func createOrUpdateRememberToken(uid, deviceId string) (token string, expiresAt int64, err error) {
	b := make([]byte, 32)
	if _, err := crand.Read(b); err != nil {
		return "", 0, err
	}
	token = hex.EncodeToString(b)
	expiresAt = time.Now().Add(30 * 24 * time.Hour).UnixMilli()
	var rec types.UserRememberToken
	err = db.GetDB().Where("uid = ?", uid).First(&rec).Error
	if err == nil {
		rec.Token = token
		rec.DeviceId = deviceId
		rec.ExpiresAt = expiresAt
		return token, expiresAt, db.GetDB().Save(&rec).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		rec = types.UserRememberToken{Uid: uid, Token: token, DeviceId: deviceId, ExpiresAt: expiresAt}
		return token, expiresAt, db.GetDB().Create(&rec).Error
	}
	return "", 0, err
}

func UsernameLogin(c *gin.Context) {
	post, err := utils.BodyToMap(c.Request.Body)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	username, exists := post["username"].(string)
	if !exists {
		response.BadRequest(c, "用户名必填")
		return
	}
	password, exists := post["password"].(string)
	if !exists {
		response.BadRequest(c, "请输入密码")
	}
	var user types.User
	if err = db.GetDB().Model(&types.User{}).Where("username = ?", username).First(&user).Error; err != nil {
		response.BadRequest(c, "用户名不存在")
		return
	}
	if err = utils.PasswordCompare(user.Password, password); err != nil {
		response.BadRequest(c, "用户名或密码错误")
		return
	}
	generateToken(c, &user)
}
func EmailLogin(c *gin.Context) {
	post, err := utils.BodyToMap(c.Request.Body)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	password, exists := post["password"].(string)
	if !exists {
		response.BadRequest(c, "请输入密码")
		return
	}
	email, exists := post["email"].(string)
	if !exists {
		response.BadRequest(c, "邮箱必填")
		return
	}
	if !utils.IsEmail(email) {
		response.BadRequest(c, "邮箱格式错误")
		return
	}
	user, err := query.GetUserByUsername(email)
	if err != nil {
		response.BadRequest(c, "邮箱未注册")
		return
	}

	if err = utils.PasswordCompare(user.Password, password); err != nil {
		response.BadRequest(c, "密码错误")
		return
	}
	// 记住密码：后端签发 remember_token，并与本次请求的服务端计算 device 指纹绑定；下次同环境请求指纹一致才通过
	rememberMe, _ := post["remember_me"].(bool)
	if rememberMe {
		deviceId, err := getRequestDeviceFingerprint(c)
		if err != nil {
			if errors.Is(err, ErrMissingDeviceId) {
				response.BadRequest(c, "缺少 X-Device-Id 请求头，无法绑定设备")
				return
			}
			response.ServerError(c, "获取设备指纹失败")
			return
		}
		rememberToken, expAt, err := createOrUpdateRememberToken(user.Uid, deviceId)
		if err != nil {
			response.ServerError(c, "生成记住登录凭证失败")
			return
		}
		generateTokenWithExtra(c, user, gin.H{"remember_token": rememberToken, "remember_token_expires_at": expAt})
		return
	}
	generateToken(c, user)
}
func EmailVerifyCodeSend(c *gin.Context) {
	post, err := utils.BodyToMap(c.Request.Body)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	email, exists := post["email"].(string)
	if !exists {
		response.BadRequest(c, "邮箱必填")
		return
	}
	if !utils.IsEmail(email) {
		response.BadRequest(c, "邮箱格式错误")
		return
	}
	if query.IsUsernameExist(email) {
		response.BadRequest(c, "邮箱已存在")
		return
	}
	verifyCode, err := utils.GenerateEmailVerifyCode(email, 10*time.Minute)
	if err != nil {
		response.ServerError(c, "生成验证码失败: "+err.Error())
		return
	}
	text := fmt.Sprintf("您注册的邮箱验证码是: %s，请在10分钟内使用。如非本人操作，请忽略。", verifyCode)
	err = utils.EmailSend(email, "邮箱验证码", text)
	if err != nil {
		_ = utils.ClearEmailVerifyCode(email) // 发送失败则清除验证码，允许重新获取
		response.ServerError(c, "发送验证码失败: "+err.Error())
		return
	}
	response.Success(c, gin.H{"expire_time": 10 * time.Minute})
}
func EmailRegister(c *gin.Context) {
	post, err := utils.BodyToMap(c.Request.Body)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	email, exists := post["email"].(string)
	if !exists {
		response.BadRequest(c, "邮箱必填")
		return
	}
	if !utils.IsEmail(email) {
		response.BadRequest(c, "邮箱格式错误")
		return
	}
	if query.IsUsernameExist(email) {
		response.BadRequest(c, "邮箱已存在")
		return
	}
	code, exists := post["code"].(string)
	if !exists {
		response.BadRequest(c, "验证码必填")
		return
	}
	password, exists := post["password"].(string)
	if !exists {
		response.BadRequest(c, "请输入密码")
		return
	}
	if len(password) < 6 {
		response.BadRequest(c, "密码至少6位")
		return
	}
	if len(password) > 20 {
		response.BadRequest(c, "密码最多20位")
		return
	}
	if !utils.IsAlphanumeric(password) {
		response.BadRequest(c, "密码只能包含数字字母和特殊字符:_&%#$@!~*^/\\-=+()[]{}<>?.'\",")
		return
	}
	if !utils.ValidateEmailVerifyCode(email, code) {
		response.BadRequest(c, "验证码错误")
		return
	}
	_ = utils.InvalidateEmailVerifyCode(email) // 验证码使用后立即作废，不可重复使用
	password = utils.PasswordEncode(password + "__quicim__")
	_, err = createUserByEmail(email, password, c.ClientIP())
	if err != nil {
		response.ServerError(c, "注册失败")
		return
	}
	response.Success(c, gin.H{"message": "注册成功"})
}

// 用户名注册
func UsernameRegister(c *gin.Context) {
	post, err := utils.BodyToMap(c.Request.Body)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	username, exists := post["username"].(string)
	if !exists {
		response.BadRequest(c, "用户名必填")
		return
	}
	if len(username) < 5 {
		response.BadRequest(c, "用户名至少5位")
		return
	}
	if len(username) > 20 {
		response.BadRequest(c, "用户名最多20位")
		return
	}
	password, exists := post["password"].(string)
	if !exists {
		response.BadRequest(c, "请输入密码")
		return
	}
	if len(password) < 6 {
		response.BadRequest(c, "密码至少6位")
		return
	}
	if len(password) > 20 {
		response.BadRequest(c, "密码最多20位")
		return
	}
	if !utils.IsAlphanumeric(password) {
		response.BadRequest(c, "密码只能包含数字字母和特殊字符:_&%#$@!~*^/\\-=+()[]{}<>?.'\",")
		return
	}
	if query.IsUsernameExist(username) {
		response.BadRequest(c, "用户名已存在")
		return
	}
	_, err = createUserByUsername(username, password, c.ClientIP())
	if err != nil {
		response.ServerError(c, "注册失败")
		return
	}
	response.Success(c, gin.H{"message": "注册成功"})
}

// TestUsernameRegister 测试注册接口：仅账号+密码，不需要验证码。
func TestUsernameRegister(c *gin.Context) {
	post, err := utils.BodyToMap(c.Request.Body)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	username, exists := post["username"].(string)
	if !exists {
		response.BadRequest(c, "用户名必填")
		return
	}
	password, exists := post["password"].(string)
	if !exists {
		response.BadRequest(c, "请输入密码")
		return
	}
	if len(username) < 3 {
		response.BadRequest(c, "用户名至少3位")
		return
	}
	if len(username) > 32 {
		response.BadRequest(c, "用户名最多32位")
		return
	}
	if len(password) < 6 {
		response.BadRequest(c, "密码至少6位")
		return
	}
	if len(password) > 32 {
		response.BadRequest(c, "密码最多32位")
		return
	}
	if query.IsUsernameExist(username) {
		response.BadRequest(c, "用户名已存在")
		return
	}
	_, err = createUserByUsername(username, password, c.ClientIP())
	if err != nil {
		response.ServerError(c, "注册失败")
		return
	}
	response.Success(c, gin.H{"message": "测试注册成功", "username": username})
}

func RefreshAccessToken(c *gin.Context) {
	post, err := utils.BodyToMap(c.Request.Body)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	refresh_token, exists := post["refresh_token"].(string)
	if !exists {
		response.BadRequest(c, "refresh_token必填")
		return
	}
	rtc, err := parseValidRefreshToken(refresh_token)
	if err != nil {
		response.TokenExpires(c, err.Error())
		return
	}
	issueRotatedTokens(c, rtc, false)
}

// RememberLogin 使用本地保存的 remember_token 登录；设备校验由服务端根据当前请求计算指纹与绑定时的指纹对比，不信任客户端传入的 device_id
func RememberLogin(c *gin.Context) {
	post, err := utils.BodyToMap(c.Request.Body)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	token, _ := post["token"].(string)
	if token == "" {
		response.BadRequest(c, "token 必填")
		return
	}
	var rec types.UserRememberToken
	if err := db.GetDB().Where("token = ?", token).First(&rec).Error; err != nil {
		response.Unauthorized(c, "无效的记住登录凭证")
		return
	}
	// 仅服务端根据当前请求计算指纹，与签发 remember_token 时保存的指纹对比；须带 X-Device-Id
	currentFingerprint, err := getRequestDeviceFingerprint(c)
	if err != nil {
		if errors.Is(err, ErrMissingDeviceId) {
			response.BadRequest(c, "缺少 X-Device-Id 请求头")
			return
		}
		response.ServerError(c, "获取设备指纹失败")
		return
	}
	if rec.DeviceId != currentFingerprint {
		response.Unauthorized(c, "该凭证已绑定其他设备，拒绝登录")
		return
	}
	if time.Now().UnixMilli() > rec.ExpiresAt {
		response.Unauthorized(c, "记住登录已过期")
		return
	}
	user, err := query.GetUserByUid(rec.Uid)
	if err != nil {
		response.ServerError(c, "获取用户失败")
		return
	}
	generateToken(c, user)
}

func Logout(c *gin.Context) {

}
func GetUser(c *gin.Context) *types.User {
	user, exists := c.Get("user")
	if !exists {
		response.Unauthorized(c, "用户未登录")
		return nil
	}
	if user, ok := user.(*types.User); ok {
		return user
	} else {
		response.Unauthorized(c, "获取用户信息失败")
		return nil
	}
}
func UserInfoGet(c *gin.Context) {
	user := GetUser(c)
	if user == nil {
		return
	}
	response.Success(c, gin.H{"user": user})
}
func Test() {
	for i := range 100 {
		email := fmt.Sprintf("test%d@test.com", i)
		if !query.IsUsernameExist(email) {
			_, err := createUserByEmail(email, utils.PasswordEncode("123456__quicim__"), "")
			if err != nil {
				log.Error("创建测试用户失败: ", err)
			}
		}
	}
}
