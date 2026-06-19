package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "golang.org/x/image/webp"

	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	"github.com/h2non/filetype"
	rredis "github.com/redis/go-redis/v9"
	"github.com/xd/quic-server/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	log "github.com/sirupsen/logrus"
	ffmpeg "github.com/u2takey/ffmpeg-go"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
	"github.com/xd/quic-server/pkg/types"
	appredis "github.com/xd/quic-server/redis"
	"github.com/zeebo/blake3"
)

func getUploadScene(c *gin.Context) string {
	return strings.TrimSpace(c.GetHeader("X-Scene"))
}

// ensureRoomAnnouncementUploadAllowed 房间公告图片仅房主/管理员可上传；其它 scene 不拦截
func ensureRoomAnnouncementUploadAllowed(c *gin.Context, scene string) bool {
	if strings.TrimSpace(scene) != entity.UploadSceneRoomAnnouncement {
		return true
	}
	user := helper.GetUser(c)
	if user == nil {
		return false
	}
	rid := strings.TrimSpace(c.GetHeader("X-Rid"))
	if rid == "" {
		response.BadRequest(c, "公告图片上传需携带房间 id")
		return false
	}
	if !query.HasRoomUser(rid, user.Uid) {
		response.BadRequest(c, "您不在该房间")
		return false
	}
	if !query.CanUserMuteInRoom(rid, user.Uid) {
		response.BadRequest(c, "仅房主或管理员可上传公告图片")
		return false
	}
	return true
}

type UploadPrecheckReq struct {
	FileHash   string `json:"file_hash"`
	FileSize   uint64 `json:"file_size"`
	ChunkSize  uint64 `json:"chunk_size"`
	ChunkTotal uint64 `json:"chunk_total"`
	FileName   string `json:"file_name"`
	UploadId   string `json:"upload_id"`
	Scene      string `json:"scene"` // 上传场景: user_avatar, room_avatar, room_announcement, room_info，空为消息等
}

type UploadPrecheckResp struct {
	UploadId      string   `json:"upload_id"`
	FileHash      string   `json:"file_hash"`
	FileSize      uint64   `json:"file_size"`
	ChunkSize     uint64   `json:"chunk_size"`
	ChunkTotal    uint64   `json:"chunk_total"`
	UploadedChunk []uint32 `json:"uploaded_chunks"`
	Completed     bool     `json:"completed"`
	FilePath      string   `json:"file_path"`
	Filename      string   `json:"filename"`
}

const (
	uploadChunkSetPrefix  = "upload:chunks:"
	uploadChunkLockPrefix = "upload:chunk:lock:"
	redisExpire           = 24 * time.Hour
	chunkLockExpire       = 5 * time.Minute
)

// uploadCompletePayload 上传完成时统一返回：uf_id + completed
func uploadCompletePayload(ufId string) gin.H {
	return gin.H{
		"uf_id":     ufId,
		"completed": true,
	}
}

func chunkSetKey(fileHash string) string {
	return uploadChunkSetPrefix + fileHash
}

func chunkLockKey(fileHash string, chunkIdx uint64) string {
	return fmt.Sprintf("%s%s:%d", uploadChunkLockPrefix, fileHash, chunkIdx)
}

// cleanupUploadRecords removes upload meta and chunk records inside a dedicated transaction.
func cleanupUploadRecords(fileHash string) error {
	if fileHash == "" {
		return nil
	}
	cleanupTx := db.GetDB().Begin()
	if err := cleanupTx.Error; err != nil {
		return err
	}
	if err := cleanupTx.Where("file_hash = ?", fileHash).Delete(&types.UploadFileChunk{}).Error; err != nil {
		cleanupTx.Rollback()
		return err
	}
	if err := cleanupTx.Where("hash = ?", fileHash).Delete(&types.UploadFile{}).Error; err != nil {
		cleanupTx.Rollback()
		return err
	}
	return cleanupTx.Commit().Error
}

func cleanupUploadCache(ctx context.Context, fileHash string) {
	if fileHash == "" {
		return
	}
	_ = appredis.GetClient().Del(ctx, chunkSetKey(fileHash)).Err()
}

func getUploadedFromRedis(ctx context.Context, fileHash string) ([]uint32, error) {
	members, err := appredis.GetClient().SMembers(ctx, chunkSetKey(fileHash)).Result()
	if err != nil && err != rredis.Nil {
		return nil, err
	}
	res := make([]uint32, 0, len(members))
	for _, m := range members {
		idx, convErr := strconv.ParseUint(m, 10, 32)
		if convErr != nil {
			continue
		}
		res = append(res, uint32(idx))
	}
	return res, nil
}

func cacheUploaded(ctx context.Context, fid string, chunkIdx uint64) {
	_ = appredis.GetClient().SAdd(ctx, chunkSetKey(fid), fmt.Sprintf("%d", chunkIdx)).Err()
	_ = appredis.GetClient().Expire(ctx, chunkSetKey(fid), redisExpire).Err()
}

// tryLockChunk returns true if lock acquired.
func tryLockChunk(ctx context.Context, fid string, chunkIdx uint64) bool {
	ok, err := appredis.GetClient().SetNX(ctx, chunkLockKey(fid, chunkIdx), "1", chunkLockExpire).Result()
	if err != nil {
		return false
	}
	return ok
}

func unlockChunk(ctx context.Context, fileHash string, chunkIdx uint64) {
	_ = appredis.GetClient().Del(ctx, chunkLockKey(fileHash, chunkIdx)).Err()
}

func getUploadedFromDB(fileHash string) ([]uint32, error) {
	var chunks []types.UploadFileChunk
	if err := db.GetDB().Select("chunk_idx").Where("file_hash = ?", fileHash).Find(&chunks).Error; err != nil {
		return nil, err
	}
	res := make([]uint32, 0, len(chunks))
	for _, c := range chunks {
		res = append(res, c.ChunkIdx)
	}
	return res, nil
}

// UploadPrecheck returns upload progress; if file is already completed, respond directly.
func UploadPrecheck(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	var req UploadPrecheckReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求参数错误")
		return
	}

	if !ensureRoomAnnouncementUploadAllowed(c, strings.TrimSpace(req.Scene)) {
		return
	}

	if req.FileHash == "" || req.ChunkSize == 0 || req.ChunkTotal == 0 || req.FileSize == 0 {
		response.BadRequest(c, "缺少文件信息")
		return
	}
	// 统一 hash 为小写，避免大小写不一致导致重复查询不到
	req.FileHash = strings.ToLower(req.FileHash)

	cfg := config.GetUploadConfig()
	if req.FileSize > cfg.UploadMaxSize {
		response.BadRequest(c, "文件大小超过限制")
		return
	}
	if req.ChunkSize > cfg.UploadChunkSize {
		response.BadRequest(c, "分片大小超过限制")
		return
	}
	log.Infof("预检请求: %+v", req)
	var uploadFile types.UploadFile
	tx := db.GetDB()

	// 使用 Find 避免“未找到”时触发 GORM 的 record not found 日志（新文件预检时未存在为正常）
	if err := tx.Where("hash = ?", req.FileHash).Limit(1).Find(&uploadFile).Error; err != nil {
		response.ServerError(c, fmt.Sprintf("查询上传记录失败: %v", err))
		return
	}

	// 已完成直接返回统一完成态
	if uploadFile.Fid != "" && uploadFile.State == 2 {
		scene := strings.TrimSpace(req.Scene)
		userFile := types.UserUploadFile{
			Filename:   req.FileName,
			Uid:        user.Uid,
			Fid:        uploadFile.Fid,
			Scene:      scene,
			ClientType: 1,
			IP:         c.ClientIP(),
		}
		if err := tx.Model(&types.UserUploadFile{}).Create(&userFile).Error; err != nil {
			response.ServerError(c, fmt.Sprintf("保存用户文件记录失败: %v", err))
			return
		}
		response.Success(c, uploadCompletePayload(userFile.UfId))
		return
	}

	// 如果不存在则创建记录
	if uploadFile.Fid == "" {
		uploadFile = types.UploadFile{
			Hash:       strings.ToLower(req.FileHash),
			TotalSize:  req.FileSize,
			ChunkSize:  uint32(req.ChunkSize),
			ChunkCount: uint32(req.ChunkTotal),
			State:      1,
		}
		if err := tx.Model(&types.UploadFile{}).Create(&uploadFile).Error; err != nil {
			response.ServerError(c, fmt.Sprintf("创建上传记录失败: %v", err))
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), appredis.REDIS_TIMEOUT)
	defer cancel()

	uploaded, err := getUploadedFromRedis(ctx, uploadFile.Fid)
	if err != nil {
		uploaded, _ = getUploadedFromDB(uploadFile.Fid)
	}

	completed := uint64(len(uploaded)) >= uint64(uploadFile.ChunkCount) && uploadFile.State == 2

	response.Success(c, UploadPrecheckResp{
		UploadId:      uploadFile.Fid,
		FileHash:      uploadFile.Hash,
		FileSize:      uploadFile.TotalSize,
		ChunkSize:     uint64(uploadFile.ChunkSize),
		ChunkTotal:    uint64(uploadFile.ChunkCount),
		UploadedChunk: uploaded,
		Completed:     completed,
		Filename:      req.FileName,
		FilePath:      uploadFile.Path,
	})
}

// parseUintHeader reads an unsigned integer from a required header.
func parseUintHeader(c *gin.Context, key string) (uint64, error) {
	value := strings.TrimSpace(c.GetHeader(key))
	if value == "" {
		return 0, fmt.Errorf("%s header is required", key)
	}
	num, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s header is invalid", key)
	}
	return num, nil
}

// ChunkUpload handles resumable chunk uploads and verifies the file hash with BLAKE3.
func ChunkUpload(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	if !ensureRoomAnnouncementUploadAllowed(c, getUploadScene(c)) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), appredis.REDIS_TIMEOUT)
	defer cancel()

	cfg := config.GetUploadConfig()

	ip := c.ClientIP()

	fileName := filepath.Base(strings.TrimSpace(c.GetHeader("X-File-Name")))
	if fileName == "" {
		response.BadRequest(c, "缺少文件名")
		return
	}
	fileHash := strings.ToLower(strings.TrimSpace(c.GetHeader("X-File-Hash")))

	chunkIndex, err := parseUintHeader(c, "X-Chunk-Index")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	chunkTotal, err := parseUintHeader(c, "X-Chunk-Total")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	chunkSize, err := parseUintHeader(c, "X-Chunk-Size")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	fileSize, err := parseUintHeader(c, "X-File-Size")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	if chunkTotal == 0 || chunkIndex >= chunkTotal {
		response.BadRequest(c, "分片序号不合法")
		return
	}
	if chunkSize == 0 {
		response.BadRequest(c, "分片大小不合法")
		return
	}
	if fileSize > cfg.UploadMaxSize {
		response.BadRequest(c, "文件大小超过限制")
		return
	}
	if chunkSize > cfg.UploadChunkSize {
		response.BadRequest(c, "分片大小超过限制")
		return
	}

	limitedReader := io.LimitReader(c.Request.Body, int64(cfg.UploadChunkSize)+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		response.ServerError(c, fmt.Sprintf("读取分片数据失败: %v", err))
		return
	}
	if uint64(len(data)) == 0 {
		response.BadRequest(c, "分片数据为空")
		return
	}
	if uint64(len(data)) > cfg.UploadChunkSize {
		response.BadRequest(c, "分片数据超出限制")
		return
	}
	if uint64(len(data)) != chunkSize {
		response.BadRequest(c, "分片大小与数据不匹配")
		return
	}

	// 查询或创建上传文件记录，使用事务保证失败时回滚
	var uploadFile types.UploadFile
	tx := db.GetDB().Begin()
	if err := tx.Error; err != nil {
		response.ServerError(c, fmt.Sprintf("开启事务失败: %v", err))
		return
	}

	rollbackWithCleanup := func(msg string, isServerErr bool) {
		_ = tx.Rollback().Error
		_ = cleanupUploadRecords(uploadFile.Fid)
		cleanupUploadCache(ctx, fileHash)
		if isServerErr {
			response.ServerError(c, msg)
		} else {
			response.BadRequest(c, msg)
		}
	}

	if err := tx.Where("hash = ?", fileHash).First(&uploadFile).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			uploadFile = types.UploadFile{
				Hash:       fileHash,
				TotalSize:  fileSize,
				ChunkSize:  uint32(chunkSize),
				ChunkCount: uint32(chunkTotal),
				State:      1,
			}
			if err := tx.Model(&types.UploadFile{}).Create(&uploadFile).Error; err != nil {
				rollbackWithCleanup(fmt.Sprintf("创建上传记录失败: %v", err), true)
				return
			}
		} else {
			rollbackWithCleanup(fmt.Sprintf("查询上传记录失败: %v", err), true)
			return
		}
	}

	// 已完成则直接返回统一完成态
	if uploadFile.State == 2 {
		scene := getUploadScene(c)
		userFile := types.UserUploadFile{
			Filename:   fileName,
			Uid:        user.Uid,
			Fid:        uploadFile.Fid,
			Scene:      scene,
			ClientType: 1,
			IP:         ip,
		}
		if err := tx.Model(&types.UserUploadFile{}).Create(&userFile).Error; err != nil {
			_ = tx.Rollback().Error
			response.ServerError(c, fmt.Sprintf("保存用户文件记录失败: %v", err))
			return
		}
		if err := tx.Commit().Error; err != nil {
			response.ServerError(c, fmt.Sprintf("提交上传事务失败: %v", err))
			return
		}
		response.Success(c, uploadCompletePayload(userFile.UfId))
		return
	}

	if uint64(uploadFile.ChunkCount) != chunkTotal {
		rollbackWithCleanup("分片信息与记录不一致", false)
		return
	}
	// 允许最后一个分片小于 chunkSize，其余分片必须一致
	recordChunkSize := uint64(uploadFile.ChunkSize)
	isLastChunk := chunkIndex+1 == chunkTotal
	if (!isLastChunk && recordChunkSize != chunkSize) || (isLastChunk && chunkSize > recordChunkSize) {
		rollbackWithCleanup("分片信息与记录不一致", false)
		return
	}

	// 如果分片已存在则跳过写入
	isMember, _ := appredis.GetClient().SIsMember(ctx, chunkSetKey(fileHash), fmt.Sprintf("%d", chunkIndex)).Result()
	if isMember {
		uploadedCount, _ := appredis.GetClient().SCard(ctx, chunkSetKey(fileHash)).Result()
		response.Success(c, gin.H{
			"completed": false,
			"chunk":     chunkIndex,
			"uploaded":  uploadedCount,
		})
		_ = tx.Rollback().Error
		return
	}

	// 尝试锁定分片，避免多人同时上传同一块
	locked := tryLockChunk(ctx, fileHash, chunkIndex)
	defer func() {
		if locked {
			unlockChunk(ctx, fileHash, chunkIndex)
		}
	}()
	if !locked {
		// 如果有其他上传在进行，直接返回当前进度（不阻塞）
		uploadedCount, _ := appredis.GetClient().SCard(ctx, chunkSetKey(fileHash)).Result()
		response.Success(c, gin.H{
			"completed": false,
			"chunk":     chunkIndex,
			"uploaded":  uploadedCount,
			"uploading": true,
		})
		_ = tx.Rollback().Error
		return
	}

	chunkDir := filepath.Join(cfg.UploadDir, fileHash, "chunks")
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		rollbackWithCleanup(fmt.Sprintf("创建分片目录失败: %v", err), true)
		return
	}

	chunkPath := filepath.Join(chunkDir, fmt.Sprintf("%08d.part", chunkIndex))
	if err := os.WriteFile(chunkPath, data, 0o644); err != nil {
		rollbackWithCleanup(fmt.Sprintf("写入分片失败: %v", err), true)
		return
	}

	// 记录分片 hash 以便去重
	chunkHash := blake3.Sum256(data)
	chunkEntity := types.UploadFileChunk{
		Hash:     fmt.Sprintf("%x", chunkHash[:]),
		Size:     int64(len(data)),
		FileHash: fileHash,
		Fid:      uploadFile.Fid,
		ChunkIdx: uint32(chunkIndex),
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Model(&types.UploadFileChunk{}).Create(&chunkEntity).Error; err != nil {
		rollbackWithCleanup(fmt.Sprintf("保存分片记录失败: %v", err), true)
		return
	}

	cacheUploaded(ctx, fileHash, chunkIndex)

	uploadedCount, _ := appredis.GetClient().SCard(ctx, chunkSetKey(fileHash)).Result()

	// 如果还有分片未完成，直接返回进度
	if uint64(uploadedCount) < chunkTotal {
		response.Success(c, gin.H{
			"completed": false,
			"chunk":     chunkIndex,
			"uploaded":  uploadedCount,
		})
		if err := tx.Commit().Error; err != nil {
			response.ServerError(c, fmt.Sprintf("提交上传事务失败: %v", err))
		}
		return
	}

	// 合并分片并计算 blake3，先合并到上传目录根下的临时文件
	if err := os.MkdirAll(cfg.UploadDir, 0o755); err != nil {
		rollbackWithCleanup(fmt.Sprintf("创建上传目录失败: %v", err), true)
		return
	}
	ext := strings.TrimPrefix(filepath.Ext(fileName), ".")
	tempPath := filepath.Join(cfg.UploadDir, fileHash+".ing")
	finalFile, err := os.Create(tempPath)
	if err != nil {
		rollbackWithCleanup(fmt.Sprintf("创建文件失败: %v", err), true)
		return
	}

	hasher := blake3.New()
	writer := io.MultiWriter(finalFile, hasher)

	for i := uint64(0); i < chunkTotal; i++ {
		partPath := filepath.Join(chunkDir, fmt.Sprintf("%08d.part", i))
		partFile, err := os.Open(partPath)
		if err != nil {
			rollbackWithCleanup(fmt.Sprintf("读取分片失败: %v", err), true)
			return
		}
		if _, err := io.Copy(writer, partFile); err != nil {
			partFile.Close()
			rollbackWithCleanup(fmt.Sprintf("写入文件失败: %v", err), true)
			return
		}
		partFile.Close()
	}

	// 关闭合并文件句柄，避免后续重命名被占用
	if err := finalFile.Close(); err != nil {
		rollbackWithCleanup(fmt.Sprintf("关闭合并文件失败: %v", err), true)
		return
	}

	computedHash := fmt.Sprintf("%x", hasher.Sum(nil))
	if fileHash != "" && computedHash != fileHash {
		_ = os.Remove(tempPath)
		_ = os.RemoveAll(chunkDir)
		uploadFile.State = 10
		uploadFile.Path = ""
		_ = tx.Save(&uploadFile).Error
		rollbackWithCleanup("文件哈希校验失败", false)
		return
	}

	// 使用 h2non/filetype 检测媒体类型
	f, err := os.Open(tempPath)
	if err != nil {
		rollbackWithCleanup(fmt.Sprintf("打开合并文件失败: %v", err), true)
		return
	}
	header := make([]byte, 262)
	n, _ := f.Read(header)
	kind, err := filetype.Match(header[:n])
	if err != nil || kind == filetype.Unknown {
		uploadFile.TypeMain = "application"
		uploadFile.TypeSub = "octet-stream"
	} else {
		uploadFile.TypeMain = kind.MIME.Type
		uploadFile.TypeSub = kind.MIME.Subtype
		if kind.Extension != "" {
			ext = kind.Extension
		}
	}

	// 如果是图片，获取宽高
	if uploadFile.TypeMain == "image" {
		if _, err := f.Seek(0, 0); err == nil {
			if cfgImg, _, err := image.DecodeConfig(f); err == nil {
				uploadFile.Width = uint32(cfgImg.Width)
				uploadFile.Height = uint32(cfgImg.Height)
			}
		}
	}
	_ = f.Close()

	// 生成相对存储路径： [type_main]/[hash].[ext]
	if uploadFile.TypeMain == "" {
		uploadFile.TypeMain = "file"
	}
	if ext == "" {
		ext = "bin"
	}
	uploadFile.Ext = ext
	storedRel := filepath.ToSlash(filepath.Join(uploadFile.TypeMain, computedHash+"."+ext))
	finalPath := filepath.Join(cfg.UploadDir, filepath.FromSlash(storedRel))

	// 确保子目录存在并移动文件到最终位置
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		rollbackWithCleanup(fmt.Sprintf("创建文件类型目录失败: %v", err), true)
		return
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		rollbackWithCleanup(fmt.Sprintf("移动文件到最终目录失败: %v", err), true)
		return
	}

	// 清理临时分片目录以及上级缓存目录（fileHash 根目录）
	_ = os.RemoveAll(chunkDir)
	_ = os.Remove(filepath.Dir(chunkDir))

	// 如果是视频，生成缩略图并记录宽高等信息
	if uploadFile.TypeMain == "video" {
		if thumb, w, h, err := processVideoFile(&uploadFile, finalPath); err != nil {
			log.Errorf("生成视频缩略图失败: %v", err)
		} else {
			uploadFile.Thumb = thumb
			// 仅当原本未设置宽高时才赋值，避免覆盖其他逻辑设置的值
			if uploadFile.Width == 0 {
				uploadFile.Width = w
			}
			if uploadFile.Height == 0 {
				uploadFile.Height = h
			}
		}
	}

	uploadFile.State = 2
	uploadFile.Path = storedRel
	if err := tx.Save(&uploadFile).Error; err != nil {
		rollbackWithCleanup(fmt.Sprintf("更新上传记录失败: %v", err), true)
		return
	}

	scene := getUploadScene(c)
	userFile := types.UserUploadFile{
		Filename:   fileName,
		Uid:        user.Uid,
		Fid:        uploadFile.Fid,
		Scene:      scene,
		ClientType: 1,
		IP:         ip,
	}
	if err := tx.Model(&types.UserUploadFile{}).Create(&userFile).Error; err != nil {
		rollbackWithCleanup(fmt.Sprintf("保存用户文件记录失败: %v", err), true)
		return
	}

	if err := tx.Commit().Error; err != nil {
		rollbackWithCleanup(fmt.Sprintf("提交上传事务失败: %v", err), true)
		return
	}

	cleanupUploadCache(ctx, fileHash)
	response.Success(c, uploadCompletePayload(userFile.UfId))
}
func ReadVideoFrameAsJpeg(filepath string, frameNum int) io.Reader {
	buf := bytes.NewBuffer(nil)
	err := ffmpeg.Input(filepath).
		Filter("select", ffmpeg.Args{fmt.Sprintf("gte(n,%d)", frameNum)}).
		Output("pipe:", ffmpeg.KwArgs{"vframes": 1, "format": "image2", "vcodec": "mjpeg"}).
		WithOutput(buf, os.Stdout).
		Run()
	if err != nil {
		panic(err)
	}

	return buf
}
func processVideoFile(file *types.UploadFile, filePath string) (string, uint32, uint32, error) {
	reader := ReadVideoFrameAsJpeg(filePath, 1)
	img, err := imaging.Decode(reader)
	if err != nil {
		return "", 0, 0, fmt.Errorf("failed to decode video frame: %w", err)
	}

	filename := fmt.Sprintf("%s.jpeg", file.Hash)
	thumb := fmt.Sprintf("thumbs/%s/%s", "video", filename)
	thumbPath := fmt.Sprintf("%s/%s", config.GetUploadConfig().UploadDir, thumb)

	if err := utils.CreateDirIfNotExists(thumbPath); err != nil {
		return "", 0, 0, fmt.Errorf("failed to create thumb dir: %w", err)
	}

	if err := imaging.Save(img, thumbPath); err != nil {
		return "", 0, 0, fmt.Errorf("failed to save video thumb: %w", err)
	}

	width, height := utils.GetImageSize(thumbPath)
	return thumb, width, height, nil
}
