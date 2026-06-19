package event

// 房间消息事件
type RoomMessagePayload struct {
	Mid string
}
type RoomMessageContentUpdatePayload struct {
	Mid string
	Cid string
}

var EventRoomMessage = NewEventKey[RoomMessagePayload]("room:message")
var EventRoomMessageContentUpdate = NewEventKey[RoomMessageContentUpdatePayload]("room:message:content:update")
var EventRoomMessageWithdraw = NewEventKey[RoomMessagePayload]("room:message:withdraw")
