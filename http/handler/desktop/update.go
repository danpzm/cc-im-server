package desktop

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"golang.org/x/mod/semver"

	"github.com/xd/quic-server/config"
)

// tauriUpdaterDynamicResponse 与 Tauri 2 动态更新服务器约定一致（见 v2.tauri.org.cn plugin/updater）。
type tauriUpdaterDynamicResponse struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	Signature string `json:"signature"`
	Notes     string `json:"notes,omitempty"`
	PubDate   string `json:"pub_date,omitempty"`
}

// TauriUpdaterCheck 供 Tauri `plugins.updater.endpoints` 使用，例如：
// https://your.api/api/v1/public/desktop/update/{{target}}/{{arch}}/{{current_version}}
// 无更新或当前已是最新：204 No Content；有更新：200 + JSON（version/url/signature 必填）。
func TauriUpdaterCheck(c *gin.Context) {
	cfg := config.GetDesktopUpdateConfig()
	if cfg == nil || cfg.LatestVersion == "" {
		c.Status(http.StatusNoContent)
		return
	}

	target := strings.ToLower(strings.TrimSpace(c.Param("target")))
	arch := strings.ToLower(strings.TrimSpace(c.Param("arch")))
	current := strings.TrimSpace(c.Param("current_version"))
	if target == "" || arch == "" || current == "" {
		c.Status(http.StatusNoContent)
		return
	}

	platformKey := target + "-" + arch
	art, ok := cfg.Artifacts[platformKey]
	if !ok {
		log.Warnf("desktop update: 未配置平台构件 %s", platformKey)
		c.Status(http.StatusNoContent)
		return
	}

	curV := normalizeSemver(current)
	latV := normalizeSemver(cfg.LatestVersion)
	if !semver.IsValid(curV) || !semver.IsValid(latV) {
		log.Warnf("desktop update: 非法 SemVer current=%q latest=%q", current, cfg.LatestVersion)
		c.Status(http.StatusNoContent)
		return
	}

	// 无更新：服务端版本不大于客户端
	if semver.Compare(latV, curV) <= 0 {
		c.Status(http.StatusNoContent)
		return
	}

	body := tauriUpdaterDynamicResponse{
		Version:   strings.TrimPrefix(latV, "v"),
		URL:       art.URL,
		Signature: art.Signature,
		Notes:     cfg.Notes,
		PubDate:   cfg.PubDate,
	}
	c.JSON(http.StatusOK, body)
}

func normalizeSemver(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "v") {
		s = "v" + s
	}
	return semver.Canonical(s)
}
