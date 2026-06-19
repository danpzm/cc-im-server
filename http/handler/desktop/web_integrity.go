package desktop

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/xd/quic-server/config"
)

// WebIntegrityManifest 提供已签名的 web-manifest.json（供客户端校验安装目录 web/）。
// GET /api/v1/public/desktop/web-integrity/:version/web-manifest.json
func WebIntegrityManifest(c *gin.Context) {
	serveWebIntegrityFile(c, "web-manifest.json")
}

// WebIntegrityManifestSig 提供 web-manifest.json.sig。
// GET /api/v1/public/desktop/web-integrity/:version/web-manifest.json.sig
func WebIntegrityManifestSig(c *gin.Context) {
	serveWebIntegrityFile(c, "web-manifest.json.sig")
}

func serveWebIntegrityFile(c *gin.Context, fileName string) {
	root := strings.TrimSpace(config.GetDesktopWebManifestDir())
	if root == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "web integrity 未配置（DESKTOP_WEB_MANIFEST_DIR 为空）"})
		return
	}

	version := strings.TrimSpace(c.Param("version"))
	if version == "" || strings.Contains(version, "..") || strings.ContainsAny(version, `/\`) {
		c.Status(http.StatusBadRequest)
		return
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	fullAbs, err := filepath.Abs(filepath.Join(rootAbs, version, fileName))
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
        log.Debugf("desktop web-integrity: 未找到 %s", fullAbs)
        c.Status(http.StatusNotFound)
        return
    }
    c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
    c.Header("Pragma", "no-cache")
    c.File(fullAbs)
}
