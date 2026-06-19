package utils

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// 生成密码哈希
func PasswordHash(password string) (string, error) {

	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

// 密码比较
func PasswordCompare(hashedPassword string, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
}
func PasswordEncode(password string) string {
	// Base64 编码
	base64Encoded := base64.StdEncoding.EncodeToString([]byte(password))

	// MD5 哈希
	md5Hash := md5.Sum([]byte(base64Encoded))

	// 转换为十六进制小写字符串
	return hex.EncodeToString(md5Hash[:])
}

// 判断字符串是否为数字字母加特殊符号，不包含空格
func IsAlphanumeric(str string) bool {
	// 数字字母加特殊符号
	return regexp.MustCompile(`^[a-zA-Z0-9_&%#$@!~*^/\[\]\\\-\+\(\)\{\}\<\>\?\'\"\.,]+$`).MatchString(str) && !strings.Contains(str, " ")
}
