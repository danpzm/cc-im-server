package file

import (
	"crypto/sha1"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/config"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
)

func getUploadFilePath(filehash string, mainType string, ext string) string {
	if mainType == "" {
		if ext == "" {
			ext = "unknown"
		}
		mainType = ext
	}
	mainType = strings.TrimPrefix(mainType, ".")
	return fmt.Sprintf("%s/%s.%s", mainType, filehash, ext)
}
func getUploadFileFullPath(hash string, mainType string, ext string) string {
	uploadConfig := config.GetUploadConfig()
	path := getUploadFilePath(hash, mainType, ext)
	return filepath.Join(uploadConfig.UploadDir, path)
}
func getUploadFileThumbFullPath(filename string) string {
	uploadConfig := config.GetUploadConfig()
	return fmt.Sprintf("%s/%s", uploadConfig.UploadDir, filename)
}

type DownloadPrecheckResp struct {
	FileHash string `json:"file_hash"`
	FileSize int64  `json:"file_size"`
	TypeMain string `json:"type_main"`
	Ext      string `json:"ext"`
	Filename string `json:"filename"`
}

// setFileCacheHeaders 为文件响应设置缓存头并处理条件请求。
// 返回 true 表示已直接返回 304，调用方应结束处理。
func setFileCacheHeaders(c *gin.Context, etagSeed string, fileModTime int64, public bool) bool {
	sum := sha1.Sum([]byte(etagSeed))
	etag := fmt.Sprintf(`W/"%x"`, sum[:])
	c.Header("ETag", etag)

	// 公开资源（头像、房间公告等）使用强缓存，URL 中 uf_id 不变即可视为内容稳定。
	if public {
		c.Header("Cache-Control", "public, max-age=2592000, immutable")
	} else {
		// 非公开资源允许浏览器缓存，但要求回源协商。
		c.Header("Cache-Control", "private, max-age=300, must-revalidate")
	}
	_ = fileModTime
	if inm := strings.TrimSpace(c.GetHeader("If-None-Match")); inm != "" {
		for _, tag := range strings.Split(inm, ",") {
			if strings.TrimSpace(tag) == etag {
				c.Status(http.StatusNotModified)
				return true
			}
		}
	}
	return false
}

// DownloadPrecheck 预下载接口，返回文件hash和基本信息
func DownloadPrecheck(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	ufId := c.Query("uf_id")
	if ufId == "" {
		response.BadRequest(c, "uf_id不能为空")
		return
	}
	mcf, err := query.ResolveDownloadByUfId(ufId, user.Uid)
	if err != nil {
		response.BadRequest(c, "文件不存在")
		return
	}
	response.Success(c, DownloadPrecheckResp{
		FileHash: mcf.Hash,
		FileSize: mcf.FileSize,
		TypeMain: mcf.TypeMain,
		Ext:      mcf.Ext,
		Filename: mcf.Filename,
	})
}

func DownloadFile(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	ufId := c.Query("uf_id")
	if ufId == "" {
		response.BadRequest(c, "uf_id不能为空")
		return
	}
	view := c.Query("view")
	mcf, err := query.ResolveDownloadByUfId(ufId, user.Uid)
	if err != nil {
		response.BadRequest(c, "文件不存在")
		return
	}
	var filePath string
	var candidatePaths []string
	if view == "thumb" {
		if mcf.TypeMain != "video" {
			response.BadRequest(c, "缩略图只支持视频文件")
			return
		}
		filePath = getUploadFileThumbFullPath(mcf.Thumb)
		candidatePaths = append(candidatePaths, filePath)
	} else {
		// 优先使用数据库保存的真实路径，兼容历史路径策略；
		// 若该路径失效，再回退到 hash/type/ext 推导路径。
		if strings.TrimSpace(mcf.Path) != "" {
			candidatePaths = append(candidatePaths,
				filepath.Join(config.GetUploadConfig().UploadDir, filepath.FromSlash(mcf.Path)))
		}
		candidatePaths = append(candidatePaths, getUploadFileFullPath(mcf.Hash, mcf.TypeMain, mcf.Ext))
	}

	var file *os.File
	var openErr error
	for _, p := range candidatePaths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		file, openErr = os.Open(p)
		if openErr == nil {
			filePath = p
			break
		}
	}

	log.Infof("download file resolved path: uf_id=%s path=%s candidates=%v", ufId, filePath, candidatePaths)
	if openErr != nil {
		log.Errorf(
			"下载失败: uf_id=%s uid=%s hash=%s type_main=%s ext=%s db_path=%s candidates=%v err=%v",
			ufId,
			user.Uid,
			mcf.Hash,
			mcf.TypeMain,
			mcf.Ext,
			mcf.Path,
			candidatePaths,
			openErr,
		)
		response.BadRequest(c, "下载失败")
		return
	}
	defer file.Close()

	fileInfo, _ := file.Stat()
	c.Header("File-Hash", mcf.Hash)
	http.ServeContent(c.Writer, c.Request, fileInfo.Name(), fileInfo.ModTime(), file)
}

// GetFileURL 按 uf_id 获取文件：公开场景（用户头像、房间头像、房间公告、房间信息）无需鉴权即可访问；其他场景需登录且具备权限
func GetFileURL(c *gin.Context) {
	ufId := strings.TrimSpace(c.Query("uf_id"))
	if ufId == "" {
		response.BadRequest(c, "uf_id不能为空")
		return
	}
	row, err := query.GetUfFileByUfId(ufId)
	if err != nil {
		response.BadRequest(c, "文件不存在")
		return
	}
	// 公开场景：直接返回文件流，无需鉴权
	if entity.IsPublicUploadScene(row.Scene) {
		uploadCfg := config.GetUploadConfig()
		fullPath := filepath.Join(uploadCfg.UploadDir, row.Path)
		file, err := os.Open(fullPath)
		if err != nil {
			log.Errorf("打开文件失败 uf_id=%s path=%s: %v", ufId, fullPath, err)
			response.BadRequest(c, "文件不存在")
			return
		}
		defer file.Close()
		fileInfo, _ := file.Stat()
		c.Header("File-Hash", row.Hash)
		if setFileCacheHeaders(c, row.Hash+":"+row.Path, fileInfo.ModTime().Unix(), true) {
			return
		}
		http.ServeContent(c.Writer, c.Request, fileInfo.Name(), fileInfo.ModTime(), file)
		return
	}
	// 非公开场景：需登录并按消息文件权限校验
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	mcf, err := query.GetMsgContentFile(ufId, user.Uid)
	if err != nil {
		response.BadRequest(c, "文件不存在")
		return
	}
	fullPath := getUploadFileFullPath(mcf.Hash, mcf.TypeMain, mcf.Ext)
	file, err := os.Open(fullPath)
	if err != nil {
		log.Errorf("打开文件失败 uf_id=%s: %v", ufId, err)
		response.BadRequest(c, "下载失败")
		return
	}
	defer file.Close()
	fileInfo, _ := file.Stat()
	c.Header("File-Hash", mcf.Hash)
	if setFileCacheHeaders(c, mcf.Hash+":"+mcf.Ext, fileInfo.ModTime().Unix(), false) {
		return
	}
	http.ServeContent(c.Writer, c.Request, fileInfo.Name(), fileInfo.ModTime(), file)
}

func openMsgContentFile(mcf *query.MsgContentFile) (*os.File, string, []string, error) {
	var candidatePaths []string
	if strings.TrimSpace(mcf.Path) != "" {
		candidatePaths = append(candidatePaths,
			filepath.Join(config.GetUploadConfig().UploadDir, filepath.FromSlash(mcf.Path)))
	}
	candidatePaths = append(candidatePaths, getUploadFileFullPath(mcf.Hash, mcf.TypeMain, mcf.Ext))

	var file *os.File
	var openErr error
	var filePath string
	for _, p := range candidatePaths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		file, openErr = os.Open(p)
		if openErr == nil {
			filePath = p
			break
		}
	}
	if openErr != nil {
		return nil, filePath, candidatePaths, openErr
	}
	return file, filePath, candidatePaths, nil
}

// FavoriteDownloadPrecheck 收藏图片预下载：校验用户已收藏后返回文件 hash 等信息。
func FavoriteDownloadPrecheck(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	fileHash := strings.TrimSpace(c.Query("file_hash"))
	if fileHash == "" {
		response.BadRequest(c, "file_hash不能为空")
		return
	}
	mcf, err := query.GetFavoriteImageFileByHash(fileHash, user.Uid)
	if err != nil {
		response.BadRequest(c, "文件不存在")
		return
	}
	response.Success(c, DownloadPrecheckResp{
		FileHash: mcf.Hash,
		FileSize: mcf.FileSize,
		TypeMain: mcf.TypeMain,
		Ext:      mcf.Ext,
		Filename: mcf.Filename,
	})
}

// FavoriteDownloadFile 收藏图片下载：校验用户已收藏后直接读取上传文件。
func FavoriteDownloadFile(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	fileHash := strings.TrimSpace(c.Query("file_hash"))
	if fileHash == "" {
		response.BadRequest(c, "file_hash不能为空")
		return
	}
	mcf, err := query.GetFavoriteImageFileByHash(fileHash, user.Uid)
	if err != nil {
		response.BadRequest(c, "文件不存在")
		return
	}
	file, filePath, candidatePaths, openErr := openMsgContentFile(mcf)
	log.Infof("favorite download resolved path: file_hash=%s path=%s candidates=%v", fileHash, filePath, candidatePaths)
	if openErr != nil {
		log.Errorf(
			"收藏图片下载失败: file_hash=%s uid=%s hash=%s type_main=%s ext=%s db_path=%s candidates=%v err=%v",
			fileHash,
			user.Uid,
			mcf.Hash,
			mcf.TypeMain,
			mcf.Ext,
			mcf.Path,
			candidatePaths,
			openErr,
		)
		response.BadRequest(c, "下载失败")
		return
	}
	defer file.Close()

	fileInfo, _ := file.Stat()
	c.Header("File-Hash", mcf.Hash)
	http.ServeContent(c.Writer, c.Request, fileInfo.Name(), fileInfo.ModTime(), file)
}
