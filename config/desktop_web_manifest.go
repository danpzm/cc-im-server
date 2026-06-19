package config

import "strings"

// DesktopWebManifestDir 存放桌面客户端 web 完整性 manifest 的根目录。
// 目录结构：{DesktopWebManifestDir}/{version}/web-manifest.json[.sig]
// 每次请求从磁盘读取，上传新文件后无需重启 HTTP 服务。
var desktopWebManifestDir string

func GetDesktopWebManifestDir() string {
	return desktopWebManifestDir
}

func loadDesktopWebManifestDir(get func(string) string) {
	desktopWebManifestDir = strings.TrimSpace(get("DESKTOP_WEB_MANIFEST_DIR"))
}
