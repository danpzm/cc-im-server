package handler

import (
	"io"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/pkg/events"
)

type HeartbeatHandler struct {
	BaseHandler
}

func (h *HeartbeatHandler) Handle() error {
	go h.run()
	return nil
}
func (h *HeartbeatHandler) run() {
	defer h.stream.Close()

	// 心跳信号大小（与客户端保持一致）
	const heartbeatSignalSize = 1
	heartbeatSignal := make([]byte, heartbeatSignalSize)

	for {
		n, err := h.stream.Read(heartbeatSignal)

		if err != nil {
			if err == io.EOF {
				log.Debugf("客户端 %s 心跳流已关闭", h.user.Uid)
				return
			}
			log.Errorf("客户端 %s 读取心跳信号失败: %v", h.user.Uid, err)
			return
		}

		if n != heartbeatSignalSize {
			log.Warnf("客户端 %s 心跳信号大小不正确: 期望 %d, 实际 %d",
				h.user.Uid, heartbeatSignalSize, n)
			continue
		}

		// 验证心跳信号（客户端发送 "p"）
		if heartbeatSignal[0] != 'p' {
			log.Warnf("客户端 %s 心跳信号不正确: %v", h.user.Uid, heartbeatSignal)
			continue
		}

		// 通过事件总线发布心跳更新事件
		if h.eventBus != nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Errorf("客户端 %s 心跳更新事件发布 panic: %v", h.user.Uid, r)
					}
				}()
				h.eventBus.Publish(events.EventHeartbeatUpdate, events.HeartbeatUpdateEvent{
					Uid:    h.user.Uid,
					Sid:    h.sid,
					ConnID: h.connID,
				})
			}()
		}

		// 回显心跳信号（可选，客户端也可以不读）
		if _, err := h.stream.Write(heartbeatSignal); err != nil {
			log.Errorf("客户端 %s 回显心跳信号失败: %v", h.user.Uid, err)
			return
		}

		log.Debugf("客户端 %s 心跳已接收", h.user.Uid)
	}
}
