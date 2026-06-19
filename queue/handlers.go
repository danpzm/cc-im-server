package queue

import (
	"context"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	"github.com/xd/quic-server/pkg/roommsg"
	"github.com/xd/quic-server/pkg/types"
	"github.com/xd/quic-server/redis"
)

// HandleRoomMessageAckCheck 创建处理房间消息ACK检查任务的处理器
// quicQueueClient: quic队列客户端（用于发布重发消息任务）
func HandleRoomMessageAckCheck(quicQueueClient *Client) func(context.Context, RoomMessageAckCheckPayload) error {
	return func(ctx context.Context, payload RoomMessageAckCheckPayload) error {
		log.Infof("开始检查ACK: mid=%s, uid=%s, retry_count=%d", payload.Mid, payload.Uid, payload.RetryCount)

		// 查询ACK记录
		var ack types.RoomMessageAck
		result := db.GetDB().
			Where("mid = ? AND uid = ? AND delete_time = 0", payload.Mid, payload.Uid).
			First(&ack)

		if result.Error != nil {
			log.Errorf("查询ACK记录失败: mid=%s, uid=%s, error=%v", payload.Mid, payload.Uid, result.Error)
			return result.Error
		}

		// 如果已经收到ACK（state=1），则不需要处理
		if ack.State == 1 {
			log.Infof("ACK已确认，无需重发: mid=%s, uid=%s", payload.Mid, payload.Uid)
			return nil
		}

		// 如果已经超时（state=2），则不需要处理
		if ack.State == 2 {
			log.Infof("ACK已超时，无需重发: mid=%s, uid=%s", payload.Mid, payload.Uid)
			return nil
		}

		// 检查是否达到最大重试次数
		if payload.RetryCount >= AckMaxRetries {
			// 更新状态为超时
			now := time.Now().UnixMilli()
			err := db.GetDB().Model(&types.RoomMessageAck{}).
				Where("mid = ? AND uid = ?", payload.Mid, payload.Uid).
				Updates(map[string]any{
					"state":       2, // 2-超时未接收
					"update_time": now,
				}).Error

			if err != nil {
				log.Errorf("更新ACK状态为超时失败: mid=%s, uid=%s, error=%v", payload.Mid, payload.Uid, err)
				return err
			}

			log.Warnf("ACK检查超时，已达到最大重试次数: mid=%s, uid=%s, retry_count=%d", payload.Mid, payload.Uid, payload.RetryCount)
			return nil
		}

		// 查询消息
		rcm := query.GetRoomMessageByMid(payload.Mid)
		if rcm == nil {
			log.Errorf("查询房间消息失败: mid=%s", payload.Mid)
			return nil
		}

		// 通过队列任务触发重发消息
		// 发布重发消息任务到quic服务器的队列服务器
		resendPayload := RoomMessageResendPayload{
			Uid:     payload.Uid,
			Message: *rcm,
		}

		// 使用传入的quic队列客户端发布重发消息任务
		if quicQueueClient == nil {
			log.Errorf("quic队列客户端未配置，无法发布重发消息任务: mid=%s, uid=%s", payload.Mid, payload.Uid)
			return nil
		}

		err := PublishTask(quicQueueClient, TaskRoomMessageResend, resendPayload, 0)
		if err != nil {
			log.Errorf("发布重发消息任务失败: mid=%s, uid=%s, error=%v", payload.Mid, payload.Uid, err)
			return err
		}
		log.Infof("已发布重发消息任务到quic服务器队列: mid=%s, uid=%s", payload.Mid, payload.Uid)

		// 更新ACK记录：增加重试次数，更新最后重试时间
		now := time.Now().UnixMilli()
		err = db.GetDB().Model(&types.RoomMessageAck{}).
			Where("mid = ? AND uid = ?", payload.Mid, payload.Uid).
			Updates(map[string]any{
				"retry_count":   payload.RetryCount + 1,
				"last_try_time": now,
				"update_time":   now,
			}).Error

		if err != nil {
			log.Errorf("更新ACK重试次数失败: mid=%s, uid=%s, error=%v", payload.Mid, payload.Uid, err)
			return err
		}

		log.Infof("已重发消息并更新重试次数: mid=%s, uid=%s, retry_count=%d", payload.Mid, payload.Uid, payload.RetryCount+1)

		// 如果还没达到最大重试次数，继续安排下一次检查
		// 注意：下一次检查任务应该由主队列服务器处理，这里不需要发布
		// 因为下一次检查任务应该由外部触发（比如定时任务或事件）
		if payload.RetryCount+1 < AckMaxRetries {
			log.Infof("需要安排下一次ACK检查: mid=%s, uid=%s, retry_count=%d (需要外部触发)", payload.Mid, payload.Uid, payload.RetryCount+1)
		}

		return nil
	}
}

// HandleFileStatusUpdateAckCheck 创建处理文件状态更新ACK检查任务的处理器
// quicQueueClient: quic队列客户端（用于发布重发文件状态更新任务）
func HandleFileStatusUpdateAckCheck(quicQueueClient *Client) func(context.Context, FileStatusUpdateAckCheckPayload) error {
	return func(ctx context.Context, payload FileStatusUpdateAckCheckPayload) error {
		log.Infof("开始检查文件状态更新ACK: client_cid=%s, uid=%s, retry_count=%d", payload.ClientCid, payload.Uid, payload.RetryCount)

		// 检查 Redis 中是否还有 ACK 记录（如果已收到 ACK，记录会被删除）
		ackKey := fmt.Sprintf("file_status_update:ack:%s", payload.ClientCid)
		exists, err := redis.Exists(ackKey)
		if err != nil {
			log.Errorf("检查文件状态更新ACK记录失败: client_cid=%s, error=%v", payload.ClientCid, err)
			return err
		}

		// 如果 ACK 记录不存在，说明已收到客户端确认
		if !exists {
			log.Infof("文件状态更新ACK已确认，无需重发: client_cid=%s, uid=%s", payload.ClientCid, payload.Uid)
			return nil
		}

		// 检查是否达到最大重试次数
		if payload.RetryCount >= AckMaxRetries {
			// 删除 Redis 记录
			if err := redis.Delete(ackKey); err != nil {
				log.Errorf("删除文件状态更新ACK记录失败: client_cid=%s, error=%v", payload.ClientCid, err)
			}
			log.Warnf("文件状态更新ACK检查超时，已达到最大重试次数: client_cid=%s, uid=%s, retry_count=%d", payload.ClientCid, payload.Uid, payload.RetryCount)
			return nil
		}

		// 通过队列任务触发重发文件状态更新
		if quicQueueClient == nil {
			log.Errorf("quic队列客户端未配置，无法发布重发文件状态更新任务: client_cid=%s, uid=%s", payload.ClientCid, payload.Uid)
			return nil
		}

		// 发布重发任务到 quic 服务器的队列服务器
		// 注意：这里使用 FileStatusUpdateAckCheckPayload 作为重发任务的载荷
		// 消费端会根据 retry_count > 0 来判断是重发任务
		resendPayload := payload
		resendPayload.RetryCount = payload.RetryCount + 1
		err = PublishTask(quicQueueClient, TaskFileStatusUpdateAckCheck, resendPayload, 0)
		if err != nil {
			log.Errorf("发布重发文件状态更新任务失败: client_cid=%s, uid=%s, error=%v", payload.ClientCid, payload.Uid, err)
			return err
		}
		log.Infof("已发布重发文件状态更新任务到quic服务器队列: client_cid=%s, uid=%s, retry_count=%d", payload.ClientCid, payload.Uid, resendPayload.RetryCount)

		// 如果还没达到最大重试次数，继续安排下一次检查
		if resendPayload.RetryCount < AckMaxRetries {
			nextPayload := payload
			nextPayload.RetryCount = resendPayload.RetryCount
			err := PublishTask(quicQueueClient, TaskFileStatusUpdateAckCheck, nextPayload, AckRetryDelay)
			if err != nil {
				log.Errorf("发布下一次文件状态更新ACK检查任务失败: client_cid=%s, uid=%s, error=%v", payload.ClientCid, payload.Uid, err)
			} else {
				log.Infof("已安排下一次文件状态更新ACK检查: client_cid=%s, uid=%s, retry_count=%d, delay=%v", payload.ClientCid, payload.Uid, nextPayload.RetryCount, AckRetryDelay)
			}
		}

		return nil
	}
}

// HandleRoomMessageWithdrawAckCheck 创建处理消息撤回ACK检查任务的处理器
// quicQueueClient: quic队列客户端（用于发布重发消息任务）
func HandleRoomMessageWithdrawAckCheck(quicQueueClient *Client) func(context.Context, RoomMessageWithdrawAckCheckPayload) error {
	return func(ctx context.Context, payload RoomMessageWithdrawAckCheckPayload) error {
		log.Infof("开始检查消息撤回ACK: mid=%s, uid=%s, retry_count=%d", payload.Mid, payload.Uid, payload.RetryCount)

		// 查询ACK记录
		var ack types.RoomMessageWithdrawAck
		result := db.GetDB().
			Where("mid = ? AND uid = ? AND delete_time = 0", payload.Mid, payload.Uid).
			First(&ack)

		if result.Error != nil {
			log.Errorf("查询消息撤回ACK记录失败: mid=%s, uid=%s, error=%v", payload.Mid, payload.Uid, result.Error)
			return result.Error
		}

		// 如果已经收到ACK（state=1），则不需要处理
		if ack.State == 1 {
			log.Infof("消息撤回ACK已确认，无需重发: mid=%s, uid=%s", payload.Mid, payload.Uid)
			return nil
		}

		// 如果已经超时（state=2），则不需要处理
		if ack.State == 2 {
			log.Infof("消息撤回ACK已超时，无需重发: mid=%s, uid=%s", payload.Mid, payload.Uid)
			return nil
		}

		// 检查是否达到最大重试次数
		if payload.RetryCount >= AckMaxRetries {
			// 更新状态为超时
			now := time.Now().UnixMilli()
			err := db.GetDB().Model(&types.RoomMessageWithdrawAck{}).
				Where("mid = ? AND uid = ?", payload.Mid, payload.Uid).
				Updates(map[string]any{
					"state":       2, // 2-超时未接收
					"update_time": now,
				}).Error

			if err != nil {
				log.Errorf("更新消息撤回ACK状态为超时失败: mid=%s, uid=%s, error=%v", payload.Mid, payload.Uid, err)
				return err
			}

			log.Warnf("消息撤回ACK检查超时，已达到最大重试次数: mid=%s, uid=%s, retry_count=%d", payload.Mid, payload.Uid, payload.RetryCount)
			return nil
		}

		// 查询消息
		rcm := query.GetRoomMessageByMidIncludeWithdraw(payload.Mid)
		if rcm == nil {
			log.Errorf("查询房间消息失败: mid=%s", payload.Mid)
			return nil
		}

		// 将 ServerRoomMessage 转换为 ServerRoomMessageWithdraw
		withdrawMsg := types.ServerRoomMessageWithdraw{
			SeqId:      rcm.SeqId,
			ClientMid:  rcm.ClientMid,
			Mid:        rcm.Mid,
			SenderUid:  rcm.SenderUid,
			Rid:        rcm.Rid,
			Contents:   rcm.Contents,
			CreateTime: rcm.CreateTime,
		}

		// 通过队列任务触发重发撤回消息
		// 发布重发撤回消息任务到quic服务器的队列服务器
		// 使用传入的quic队列客户端发布重发撤回消息任务
		if quicQueueClient == nil {
			log.Errorf("quic队列客户端未配置，无法发布重发撤回消息任务: mid=%s, uid=%s", payload.Mid, payload.Uid)
			return nil
		}

		// 发布重发撤回消息任务
		resendPayload := RoomMessageWithdrawResendPayload{
			Uid:     payload.Uid,
			Message: withdrawMsg,
		}

		err := PublishTask(quicQueueClient, TaskRoomMessageWithdrawResend, resendPayload, 0)
		if err != nil {
			log.Errorf("发布重发撤回消息任务失败: mid=%s, uid=%s, error=%v", payload.Mid, payload.Uid, err)
			return err
		}
		log.Infof("已发布重发撤回消息任务到quic服务器队列: mid=%s, uid=%s", payload.Mid, payload.Uid)

		// 更新ACK记录：增加重试次数，更新最后重试时间
		now := time.Now().UnixMilli()
		err = db.GetDB().Model(&types.RoomMessageWithdrawAck{}).
			Where("mid = ? AND uid = ?", payload.Mid, payload.Uid).
			Updates(map[string]any{
				"retry_count":   payload.RetryCount + 1,
				"last_try_time": now,
				"update_time":   now,
			}).Error

		if err != nil {
			log.Errorf("更新消息撤回ACK重试次数失败: mid=%s, uid=%s, error=%v", payload.Mid, payload.Uid, err)
			return err
		}

		log.Infof("已重发消息并更新重试次数: mid=%s, uid=%s, retry_count=%d", payload.Mid, payload.Uid, payload.RetryCount+1)

		// 如果还没达到最大重试次数，继续安排下一次检查
		if payload.RetryCount+1 < AckMaxRetries {
			log.Infof("需要安排下一次ACK检查: mid=%s, uid=%s, retry_count=%d (需要外部触发)", payload.Mid, payload.Uid, payload.RetryCount+1)
		}

		return nil
	}
}

// HandleRoomMuteStrategyTime 策略禁言开始/结束：到点向房间发送禁言开始或禁言结束系统消息（与频率无关）
func HandleRoomMuteStrategyTime(ctx context.Context, payload RoomMuteStrategyTimePayload) error {
	log.Infof("执行策略禁言定时: rid=%s kind=%s run_at_ms=%d", payload.Rid, payload.Kind, payload.RunAtMs)
	content := map[string]any{"rid": payload.Rid}
	var contentType entity.RoomMessageContentType
	if payload.Kind == "start" {
		contentType = types.RoomMessageContentTypeRoomMuteStrategyStart
	} else {
		contentType = types.RoomMessageContentTypeRoomMuteStrategyEnd
	}
	roommsg.CreateSystemMessageAndNotify(payload.Rid, contentType, "system", content)
	return nil
}

// HandleRoomAdminOperationLog 消费房间管理员操作日志任务，写入 DB
func HandleRoomAdminOperationLog(ctx context.Context, payload RoomAdminOperationLogPayload) error {
	return query.CreateRoomAdminOperation(
		payload.Rid,
		payload.OpType,
		payload.OperatorUid,
		payload.Sid,
		payload.RelatedId,
		payload.BeforeData,
		payload.AfterData,
	)
}

// HandleUserOperationLog 消费用户操作日志任务，写入 DB
func HandleUserOperationLog(ctx context.Context, payload UserOperationLogPayload) error {
	return query.CreateUserOperation(
		payload.Uid,
		payload.OpType,
		payload.Sid,
		payload.RelatedId,
		payload.BeforeData,
		payload.AfterData,
	)
}
