package router

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xd/quic-server/http/middleware"
	fileHandler "github.com/xd/quic-server/oss/handler/file"
)

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 设置响应头
		c.Header("Access-Control-Allow-Origin", "*") // 允许的源
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, X-Requested-With, Content-Type, Accept, Authorization")
		c.Header("Access-Control-Expose-Headers", "Content-Length, Access-Control-Allow-Origin")
		c.Header("Access-Control-Max-Age", "12h")
		c.Header("Access-Control-Allow-Credentials", "true")

		// 如果是预检请求，直接返回
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		// 继续处理后续请求
		c.Next()
	}
}

// servePublicStatic 在无任何路由匹配时，从 ./public 提供根路径静态文件（如 GET /xx.txt）。
// 不能用 GET /*path：会与 /api/... 在 Gin 路由树上冲突（panic）。
func servePublicStatic(c *gin.Context) {
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/api/") {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Status(http.StatusNotFound)
		return
	}

	rel := strings.TrimPrefix(path, "/")
	if rel == "" {
		c.Status(http.StatusNotFound)
		return
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if strings.Contains(rel, "..") {
		c.Status(http.StatusNotFound)
		return
	}

	rootAbs, err := filepath.Abs("public")
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	fullAbs, err := filepath.Abs(filepath.Join(rootAbs, filepath.FromSlash(rel)))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	sep := string(filepath.Separator)
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs+sep, rootAbs+sep) {
		c.Status(http.StatusNotFound)
		return
	}

	st, err := os.Stat(fullAbs)
	if err != nil || st.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	c.File(fullAbs)
}

func Register(g *gin.Engine) {
	g.Use(CORSMiddleware())

	oss := g.Group("/api/v1/")
	// 文件上传、按 uf_id 查询（与主站客户端使用的路径一致，客户端将 API_OSS_BASE_URL 指向 OSS 端口即可）
	file := g.Group("/file")
	{
		file.GET("/url", fileHandler.GetFileURL) // 按 uf_id 取文件，公开场景无需鉴权
	}
	fileAuth := oss.Group("/file").Use(middleware.Authorization())
	{
		fileAuth.POST("/upload/precheck", fileHandler.UploadPrecheck)
		fileAuth.POST("/upload", fileHandler.ChunkUpload)

		fileAuth.GET("/download/precheck", fileHandler.DownloadPrecheck)
		fileAuth.GET("/download", fileHandler.DownloadFile)
		fileAuth.GET("/favorite/download/precheck", fileHandler.FavoriteDownloadPrecheck)
		fileAuth.GET("/favorite/download", fileHandler.FavoriteDownloadFile)
	}

	// 根路径静态文件：GET /foo.txt -> ./public/foo.txt（须在全部业务路由之后注册）
	g.NoRoute(servePublicStatic)
}
