package server

import (
	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/pkg/types"
	quicEntity "github.com/xd/quic-server/quic/handler/entity"
)

// handleNotificationNotifyInternal 内部方法：处理通知推送
func (s *Server) handleNotificationNotifyInternal(nid string) error {
	if nid == "" {
		log.Warn("通知推送的nid为空，忽略处理")
		return nil
	}

	log.Infof("收到通知推送任务，准备推送 nid=%s", nid)

	// 查询通知信息
	notification, err := query.GetMessageNotificationByNid(nid)
	if err != nil || notification == nil {
		log.Errorf("查询消息通知失败 nid=%s: %v", nid, err)
		return nil
	}

	// 获取客户端
	client := s.GetClientByUid(notification.Uid)
	if client == nil {
		log.Debugf("用户 %s 不在线，无法推送通知 nid=%s", notification.Uid, nid)
		return nil // 用户不在线不算错误，直接返回
	}

	// 构建通知消息
	serverNotification := types.ServerNotification{
		Nid:        notification.Nid,
		Uid:        notification.Uid,
		Type:       notification.Type,
		RelatedId:  notification.RelatedId,
		Content:    notification.Content,
		State:      notification.State,
		Status:     notification.Status,
		ReadAt:     notification.ReadAt,
		CreateTime: notification.CreateTime,
	}

	// 编码消息数据
	data, err := client.EncodeServerMessageData(serverNotification)
	if err != nil {
		log.Errorf("编码通知消息失败 uid=%s nid=%s: %v", notification.Uid, notification.Nid, err)
		return err
	}

	// 发送消息
	err = client.SendServerMessage(types.ServerMessageEntity{
		MessageType: quicEntity.TypeNotification,
		Data:        data,
	})
	if err != nil {
		log.Errorf("推送通知失败 uid=%s nid=%s: %v", notification.Uid, notification.Nid, err)
		return err
	}

	log.Infof("成功推送通知给用户 %s, nid=%s", notification.Uid, notification.Nid)
	return nil
}

// HandleNotificationNotify 处理消息通知推送任务（队列版本，由HTTP服务通过队列发布）
// func (s *Server) HandleNotificationNotify(ctx context.Context, payload queue.NotificationNotifyPayload) error {
// 	return s.handleNotificationNotifyInternal(payload.Nid)
// }
