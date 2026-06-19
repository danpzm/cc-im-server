package user

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/utils"
	"gorm.io/gorm"
)

type emojiRecentRecordBody struct {
	EmojiId string `json:"emoji_id" binding:"required"`
	Label   string `json:"label"`
}

// EmojiRecentList GET /api/v1/user/emoji/recent/list
func EmojiRecentList(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	list, err := query.ListUserEmojiRecent(user.Uid, 0)
	if err != nil {
		response.ServerError(c, "获取最近使用表情失败")
		return
	}
	type item struct {
		EmojiId string `json:"emoji_id"`
		Label   string `json:"label"`
		UsedAt  int64  `json:"used_at"`
	}
	out := make([]item, 0, len(list))
	for _, row := range list {
		out = append(out, item{
			EmojiId: row.EmojiId,
			Label:   row.Label,
			UsedAt:  row.UsedAt,
		})
	}
	response.Success(c, gin.H{"list": out})
}

// EmojiRecentRecord POST /api/v1/user/emoji/recent/record
func EmojiRecentRecord(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body emojiRecentRecordBody
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	emojiId := strings.TrimSpace(body.EmojiId)
	if emojiId == "" {
		response.BadRequest(c, "emoji_id 不能为空")
		return
	}
	if err := query.UpsertUserEmojiRecent(user.Uid, emojiId, strings.TrimSpace(body.Label)); err != nil {
		response.ServerError(c, "记录最近使用表情失败")
		return
	}
	response.Success(c, nil)
}

type emojiFavoriteAddBody struct {
	Kind    string         `json:"kind" binding:"required"`
	RefKey  string         `json:"ref_key" binding:"required"`
	Label   string         `json:"label"`
	Payload map[string]any `json:"payload"`
}

// EmojiFavoriteList GET /api/v1/user/emoji/favorite/list
func EmojiFavoriteList(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	list, err := query.ListUserEmojiFavorites(user.Uid)
	if err != nil {
		response.ServerError(c, "获取收藏失败")
		return
	}
	type item struct {
		Id         int64          `json:"id"`
		Kind       string         `json:"kind"`
		RefKey     string         `json:"ref_key"`
		Label      string         `json:"label"`
		Payload    map[string]any `json:"payload"`
		CreateTime int64          `json:"create_time"`
	}
	out := make([]item, 0, len(list))
	for _, row := range list {
		payload := map[string]any{}
		_ = json.Unmarshal([]byte(row.Payload), &payload)
		out = append(out, item{
			Id:         row.Id,
			Kind:       row.Kind,
			RefKey:     row.RefKey,
			Label:      row.Label,
			Payload:    payload,
			CreateTime: row.CreateTime,
		})
	}
	response.Success(c, gin.H{"list": out})
}

// EmojiFavoriteAdd POST /api/v1/user/emoji/favorite/add
func EmojiFavoriteAdd(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	req, err := utils.BodyToObj[emojiFavoriteAddBody](c.Request.Body)
	if err != nil {
		response.BadRequest(c, "参数解析失败")
		return
	}
	kind := strings.TrimSpace(req.Kind)
	refKey := strings.TrimSpace(req.RefKey)
	if kind != entity.UserEmojiFavoriteKindEmoji && kind != entity.UserEmojiFavoriteKindImage {
		response.BadRequest(c, "kind 仅支持 emoji 或 image")
		return
	}
	if refKey == "" {
		response.BadRequest(c, "ref_key 不能为空")
		return
	}
	row, err := query.AddUserEmojiFavorite(user.Uid, kind, refKey, strings.TrimSpace(req.Label), req.Payload)
	if err != nil {
		if errors.Is(err, query.ErrEmojiFavoriteLimit) {
			response.BadRequest(c, "收藏数量已达上限")
			return
		}
		if errors.Is(err, query.ErrEmojiFavoriteDuplicate) {
			response.BadRequest(c, "收藏重复")
			return
		}
		response.ServerError(c, "收藏失败")
		return
	}
	payload := map[string]any{}
	_ = json.Unmarshal([]byte(row.Payload), &payload)
	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpEmojiFavoriteAdd, refKey, nil, map[string]any{
		"kind": kind, "ref_key": refKey, "label": row.Label, "payload": payload,
	})
	response.Success(c, gin.H{
		"id":          row.Id,
		"kind":        row.Kind,
		"ref_key":     row.RefKey,
		"label":       row.Label,
		"payload":     payload,
		"create_time": row.CreateTime,
	})
}

type emojiFavoriteImageDownloadBody struct {
	FileHash string `json:"file_hash" binding:"required"`
}

// EmojiFavoriteImageDownload POST /api/v1/user/emoji/favorite/image/download
// 校验用户已收藏该图片后返回下载元数据，客户端再向 OSS 拉取文件到本地收藏目录。
func EmojiFavoriteImageDownload(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var body emojiFavoriteImageDownloadBody
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	fileHash := strings.TrimSpace(body.FileHash)
	fav, err := query.GetUserEmojiFavorite(user.Uid, entity.UserEmojiFavoriteKindImage, fileHash)
	if err != nil {
		response.ServerError(c, "获取收藏失败")
		return
	}
	if fav == nil {
		response.BadRequest(c, "未收藏该图片")
		return
	}
	mcf, err := query.GetFavoriteImageFileByHash(fileHash, user.Uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.BadRequest(c, "文件不存在")
			return
		}
		response.ServerError(c, "获取收藏图片失败")
		return
	}
	payload := map[string]any{}
	_ = json.Unmarshal([]byte(fav.Payload), &payload)
	filename := strings.TrimSpace(mcf.Filename)
	if filename == "" {
		filename = strings.TrimSpace(fav.Label)
	}
	if v, ok := payload["filename"].(string); ok && strings.TrimSpace(v) != "" {
		filename = strings.TrimSpace(v)
	}
	if filename == "" {
		filename = "image"
	}
	fileSize := mcf.FileSize
	if fileSize <= 0 {
		if v, ok := payload["file_size"].(float64); ok {
			fileSize = int64(v)
		}
	}
	fileTypeMain := strings.TrimSpace(mcf.TypeMain)
	if fileTypeMain == "" {
		fileTypeMain = "image"
	}
	if v, ok := payload["file_type_main"].(string); ok && strings.TrimSpace(v) != "" {
		fileTypeMain = strings.TrimSpace(v)
	}
	fileTypeSub := strings.TrimSpace(mcf.TypeSub)
	if v, ok := payload["file_type_sub"].(string); ok {
		fileTypeSub = strings.TrimSpace(v)
	}
	response.Success(c, gin.H{
		"filename":       filename,
		"file_size":      fileSize,
		"file_type_main": fileTypeMain,
		"file_type_sub":  fileTypeSub,
		"file_hash":      mcf.Hash,
		"ext":            mcf.Ext,
	})
}

type emojiFavoriteRemoveBody struct {
	Kind   string `json:"kind" binding:"required"`
	RefKey string `json:"ref_key" binding:"required"`
}

// EmojiFavoriteRemove POST /api/v1/user/emoji/favorite/remove
func EmojiFavoriteRemove(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	req, err := utils.BodyToObj[emojiFavoriteRemoveBody](c.Request.Body)
	if err != nil {
		response.BadRequest(c, "参数解析失败")
		return
	}
	kind := strings.TrimSpace(req.Kind)
	refKey := strings.TrimSpace(req.RefKey)
	if kind != entity.UserEmojiFavoriteKindEmoji && kind != entity.UserEmojiFavoriteKindImage {
		response.BadRequest(c, "kind 仅支持 emoji 或 image")
		return
	}
	if refKey == "" {
		response.BadRequest(c, "ref_key 不能为空")
		return
	}
	before, _ := query.GetUserEmojiFavorite(user.Uid, kind, refKey)
	if err := query.RemoveUserEmojiFavorite(user.Uid, kind, refKey); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.BadRequest(c, "收藏不存在")
			return
		}
		response.ServerError(c, "取消收藏失败")
		return
	}
	beforePayload := map[string]any{}
	beforeLabel := ""
	if before != nil {
		beforeLabel = before.Label
		_ = json.Unmarshal([]byte(before.Payload), &beforePayload)
	}
	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpEmojiFavoriteRemove, refKey, map[string]any{
		"kind": kind, "ref_key": refKey, "label": beforeLabel, "payload": beforePayload,
	}, nil)
	response.Success(c, nil)
}
