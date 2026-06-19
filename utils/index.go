package utils

import (
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// 判断两个类型是否一致
func TypeEq(target any, other any) bool {
	if target == nil || other == nil {
		return target == other
	}
	targetType := reflect.TypeOf(target)
	otherType := reflect.TypeOf(other)
	if targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	if otherType.Kind() == reflect.Ptr {
		otherType = otherType.Elem()
	}
	return otherType == targetType
}
func IsPointerType(t reflect.Type) bool {
	return t.Kind() == reflect.Ptr
}

// 复制一个map
func CopyMap[K comparable, V any](original map[K]V) map[K]V {
	snapshot := make(map[K]V)
	maps.Copy(snapshot, original)
	return snapshot
}

// 复制一个数组
func CopyArray[T any](original []T) []T {
	snapshot := make([]T, len(original))
	copy(snapshot, original)
	return snapshot
}
func BodyToMap(body io.ReadCloser) (map[string]any, error) {
	return BodyToObj[map[string]any](body)
}
func BodyToObj[T any](body io.ReadCloser) (T, error) {
	defer body.Close()
	var data T
	if err := json.NewDecoder(body).Decode(&data); err != nil {
		return data, fmt.Errorf("BODY 解析错误")
	}
	return data, nil
}
func UUID() string {
	return uuid.New().String()
}
func Contains[T any](slice []T, element T) bool {
	s := reflect.ValueOf(slice)
	e := reflect.ValueOf(element)
	for i := range s.Len() {
		if s.Index(i).Interface() == e.Interface() {
			return true
		}
	}
	return false
}

func ParseInt64(str string) int64 {
	id, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return 0
	}
	return id
}
func ParseUint64(str string) uint64 {
	id, err := strconv.ParseUint(str, 10, 64)
	if err != nil {
		return 0
	}
	return id
}
func JoinField(prefix string, separator string, fields []string) []string {
	joinedFields := make([]string, len(fields))
	for i, field := range fields {
		joinedFields[i] = fmt.Sprintf("%s%s%s", prefix, separator, field)
	}
	return joinedFields
}
func IsDirExists(dirPath string) bool {
	info, err := os.Stat(dirPath)
	if os.IsNotExist(err) {
		return false
	}
	return info.IsDir()
}

// 不存在则创建目录
func CreateDirIfNotExists(path string) error {
	dir := filepath.Dir(path)
	if !IsDirExists(dir) {
		return os.MkdirAll(dir, 0755)
	}
	return nil
}
func BuildDynamicJSONAggSQL(tableName string, fields map[string]string, field string, when ...string) string {
	var builder strings.Builder

	builder.WriteString("jsonb_build_object(")
	fieldCount := len(fields)
	i := 0 // 初始化计数器
	for key, value := range fields {
		// 处理数值类型
		if _, err := fmt.Sscanf(value, "%d", &i); err == nil {
			builder.WriteString(fmt.Sprintf("%s.%s", tableName, value))
		} else {
			if value[0] == '-' {
				// 删除第一个字符
				value = value[1:]
				builder.WriteString(fmt.Sprintf("'%s', %s", key, value))
			} else if strings.Contains(value, ".") {
				builder.WriteString(fmt.Sprintf("'%s', %s", key, value))
			} else {
				builder.WriteString(fmt.Sprintf("'%s', %s.%s", key, tableName, value))
			}
		}

		// 分隔符逻辑（在非最后一项时添加逗号）
		i++ // 正确递增计数器
		if i < fieldCount {
			builder.WriteString(", ")
		}
	}
	builder.WriteString(")")

	// 处理 ORDER BY 子句和 WHEN 子句
	// when 参数规则：
	// - 如果 len(when) == 0: 没有 when 和 orderBy
	// - 如果 len(when) == 1:
	//   - 如果包含 "EXISTS"，则是 when 子句
	//   - 否则是 orderBy
	// - 如果 len(when) == 2: when[0] 是 when 子句，when[1] 是 orderBy
	orderBy := ""
	whenClause := ""
	if len(when) > 0 {
		if len(when) == 1 {
			if strings.Contains(when[0], "EXISTS") {
				whenClause = when[0]
			} else {
				orderBy = when[0]
			}
		} else if len(when) >= 2 {
			whenClause = when[0]
			orderBy = when[1]
		}
	}

	// 构建 jsonb_agg 表达式，如果有 orderBy 则添加 ORDER BY
	aggExpression := builder.String()
	if orderBy != "" {
		aggExpression = fmt.Sprintf("%s ORDER BY %s", aggExpression, orderBy)
	}

	if whenClause != "" {
		return fmt.Sprintf("CASE WHEN EXISTS (%s) THEN jsonb_agg(%s) ELSE '[]'::jsonb END as %s", whenClause, aggExpression, field)
	}
	return fmt.Sprintf("COALESCE(jsonb_agg(%s), '[]'::jsonb) as %s", aggExpression, field)
}
func GetImageSize(filepath string) (uint32, uint32) {
	file, err := os.Open(filepath)
	if err != nil {
		return 0, 0
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		log.Errorf("获取图片尺寸失败: %v", err)
		return 0, 0
	}
	return uint32(config.Width), uint32(config.Height)
}
