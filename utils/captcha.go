package utils

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/xd/quic-server/redis"
)

type CaptchaType string

func EmailCaptchaSend(email string, expireTime time.Duration) error {
	code := GenerateCaptchaCode()
	return redis.SetString(fmt.Sprintf("captcha:%s", email), code, expireTime)
}

func GenerateCaptchaCode() string {
	code := rand.Intn(1000000)
	return fmt.Sprintf("%06d", code)
}
func VerifyEmailCaptcha(email string, code string) (bool, error) {
	redisCode, err := redis.GetString(fmt.Sprintf("captcha:%s", email))
	if err != nil {
		return false, err
	}
	return code == redisCode, nil
}
