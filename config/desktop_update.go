package config

import (
	"strings"
)

// DesktopUpdateArtifact 对应 Tauri 动态更新 JSON 中的单平台安装包与签名。
type DesktopUpdateArtifact struct {
	URL       string
	Signature string // 必须为 .sig 文件全文内容（与 Tauri 文档一致），不是路径
}

// DesktopUpdateConfig 桌面端（Tauri updater）动态检查所需数据，由 HTTP 服务 env 加载。
// 未配置 LatestVersion 时表示关闭该能力：接口恒返回 204。
type DesktopUpdateConfig struct {
	LatestVersion string
	Notes         string
	PubDate       string // RFC3339，可选
	Artifacts     map[string]DesktopUpdateArtifact
}

var desktopUpdateConfig *DesktopUpdateConfig

func GetDesktopUpdateConfig() *DesktopUpdateConfig {
	return desktopUpdateConfig
}

func loadDesktopUpdateConfig(get func(string) string) {
	latest := strings.TrimSpace(get("DESKTOP_UPDATE_LATEST_VERSION"))
	if latest == "" {
		desktopUpdateConfig = nil
		return
	}
	// 平台键与 Tauri 默认一致：windows|linux|darwin + "-" + x86_64|aarch64|i686|armv7
	// 环境变量：DESKTOP_UPDATE_ARTIFACT_WINDOWS_X86_64_URL / _SIGNATURE
	keys := []string{
		"windows-x86_64", "windows-aarch64", "windows-i686",
		"linux-x86_64", "linux-aarch64", "linux-i686", "linux-armv7",
		"darwin-x86_64", "darwin-aarch64",
	}
	artifacts := make(map[string]DesktopUpdateArtifact, len(keys))
	for _, pk := range keys {
		suffix := strings.ToUpper(strings.ReplaceAll(pk, "-", "_"))
		url := strings.TrimSpace(get("DESKTOP_UPDATE_ARTIFACT_" + suffix + "_URL"))
		sig := strings.TrimSpace(get("DESKTOP_UPDATE_ARTIFACT_" + suffix + "_SIGNATURE"))
		if url != "" && sig != "" {
			artifacts[pk] = DesktopUpdateArtifact{URL: url, Signature: sig}
		}
	}
	desktopUpdateConfig = &DesktopUpdateConfig{
		LatestVersion: latest,
		Notes:         strings.TrimSpace(get("DESKTOP_UPDATE_NOTES")),
		PubDate:       strings.TrimSpace(get("DESKTOP_UPDATE_PUB_DATE")),
		Artifacts:     artifacts,
	}
}
