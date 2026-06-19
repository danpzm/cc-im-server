package utils

import (
	"crypto/tls"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	redisClient "github.com/xd/quic-server/redis"
	"gopkg.in/gomail.v2"
)

const EmailVerifyCodePrefix = "email_verify_code:"
const EmailVerifyCodeCountPrefix = "email_verify_code_count:"
const EmailVerifyCodeMaxCount = 5                    // 验证码发送次数
const EmailVerifyCodeCountExpireTime = 1 * time.Hour // 验证码发送频率过期时间

func EmailSend(to string, subject string, body string) error {
	if to == "" {
		return fmt.Errorf("收件人地址不能为空")
	}
	if subject == "" {
		return fmt.Errorf("邮件主题不能为空")
	}
	if body == "" {
		return fmt.Errorf("邮件内容不能为空")
	}
	mailConfig := config.GetMailConfig()
	d := gomail.NewDialer(mailConfig.MailHost, mailConfig.MailPort, mailConfig.MailUser, mailConfig.MailPassword)
	d.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	m := gomail.NewMessage()
	m.SetBody("text/html", body)
	m.SetHeader("To", to)
	m.SetHeader("Subject", subject)
	m.SetHeader("From", mailConfig.MailUser)
	err := d.DialAndSend(m)
	if err != nil {
		log.Errorf("邮件发送失败 %v %v %v %v", to, subject, body, err)
		return err
	}
	log.Infof("邮件发送成功 %v %v %v", to, subject, body)
	return nil
}

// 验证邮箱格式
func IsEmail(email string) bool {
	return regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`).MatchString(email)
}

func GenerateEmailVerifyCode(email string, expireTime time.Duration) (string, error) {
	// 获取已发送验证码的次数
	count, err := redisClient.GetInt64(fmt.Sprintf(EmailVerifyCodeCountPrefix+"%s", email))
	if err == redis.Nil {
		count = 0
	}
	if count >= EmailVerifyCodeMaxCount {
		return "", fmt.Errorf("验证码发送次数过多，请稍后再试")
	}
	// 获取之前是否存在验证码（key 不存在时 GetString 返回 redis.Nil，表示可以发新验证码）
	code, err := redisClient.GetString(fmt.Sprintf(EmailVerifyCodePrefix+"%s", email))
	if err != nil && err != redis.Nil {
		return "", err
	}
	if err != redis.Nil && code != "" {
		return "", fmt.Errorf("验证码未过期，请稍后再试")
	}
	// 生成6位随机验证码
	verifyCode := strconv.Itoa(rand.Intn(900000) + 100000)
	err = redisClient.SetString(fmt.Sprintf(EmailVerifyCodePrefix+"%s", email), verifyCode, expireTime)
	if err != nil {
		return "", err
	}
	// 记录验证码发送次数
	if count == 0 {
		err = redisClient.SetInt64(fmt.Sprintf(EmailVerifyCodeCountPrefix+"%s", email), 1, EmailVerifyCodeCountExpireTime)
		if err != nil {
			return "", err
		}
	} else {
		_, err = redisClient.Incr(fmt.Sprintf(EmailVerifyCodeCountPrefix+"%s", email))
		if err != nil {
			return "", err
		}
	}
	return verifyCode, nil
}

// ClearEmailVerifyCode 清除该邮箱的验证码（发送失败时调用，便于用户重新获取）
// 同时将发送次数 -1，避免失败也占用配额
func ClearEmailVerifyCode(email string) error {
	key := fmt.Sprintf(EmailVerifyCodePrefix+"%s", email)
	if err := redisClient.Delete(key); err != nil {
		return err
	}
	countKey := fmt.Sprintf(EmailVerifyCodeCountPrefix+"%s", email)
	_, err := redisClient.Decr(countKey)
	return err
}

// InvalidateEmailVerifyCode 验证码使用后作废（仅删除验证码 key，不扣减发送次数）
func InvalidateEmailVerifyCode(email string) error {
	key := fmt.Sprintf(EmailVerifyCodePrefix+"%s", email)
	return redisClient.Delete(key)
}

func ValidateEmailVerifyCode(email string, verifyCode string) bool {
	code, err := redisClient.GetString(fmt.Sprintf(EmailVerifyCodePrefix+"%s", email))
	if err != nil {
		return false
	}
	return code == verifyCode
}
