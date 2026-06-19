package serviceregistry

import "errors"

var errLoopbackRegister = errors.New("非开发模式禁止将 127.0.0.1 / localhost 注册到 Redis（请配置 HTTP_CLIENT_BASE_URL 等公网地址，或使用 scripts/dev.ps1 本地开发）")
