package session

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/xd/quic-server/db/entity"
	"github.com/xd/quic-server/db/query"
	helper "github.com/xd/quic-server/http/handler"
	"github.com/xd/quic-server/http/response"
)

// SessionListResponse 会话列表分页；下一页 GET 带上 cursor_update_time、cursor_last_msg_time、cursor_id（与本次返回的 next_* 一致）。
type SessionListResponse struct {
	List                  []*query.UserRoomSessionDto `json:"list"`
	NextCursorUpdateTime  int64                       `json:"next_cursor_update_time"`
	NextCursorLastMsgTime int64                       `json:"next_cursor_last_msg_time"`
	NextCursorID          int64                       `json:"next_cursor_id"`
	HasMore               bool                        `json:"has_more"`
	/** 全部会话未读总和（含未出现在本页的会话），供客户端侧栏角标 */
	TotalUnread int64 `json:"total_unread"`
}

// SessionList 分页获取会话（每页 15 条）：先按 update_time 倒序，再按 last_room_message_create_time 倒序，再按 id 倒序。
// Query: 三个游标参数均不传表示首屏；三者均传且均为 0 亦视为首屏（显式占位）；否则为续页，须与上一页返回的 next_cursor_* 一致。
// 禁止只传部分游标：缺失会被 ParseInt 成 0，易生成错误 keyset 或反复首屏。
func SessionList(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	utStr := strings.TrimSpace(c.Query("cursor_update_time"))
	lmStr := strings.TrimSpace(c.Query("cursor_last_msg_time"))
	idStr := strings.TrimSpace(c.Query("cursor_id"))
	set := []bool{utStr != "", lmStr != "", idStr != ""}
	if set[0] != set[1] || set[1] != set[2] {
		response.BadRequest(c, "分页须同时传 cursor_update_time、cursor_last_msg_time、cursor_id（与上一页 next_* 一致）；首屏请三者均不传")
		return
	}

	var cursorUT, cursorLM, cursorID int64
	var firstPage bool
	if !set[0] {
		firstPage = true
	} else {
		var err error
		cursorUT, err = strconv.ParseInt(utStr, 10, 64)
		if err != nil {
			response.BadRequest(c, "cursor_update_time 无效")
			return
		}
		cursorLM, err = strconv.ParseInt(lmStr, 10, 64)
		if err != nil {
			response.BadRequest(c, "cursor_last_msg_time 无效")
			return
		}
		cursorID, err = strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			response.BadRequest(c, "cursor_id 无效")
			return
		}
		firstPage = cursorUT == 0 && cursorLM == 0 && cursorID == 0
	}

	sessionList, nextUT, nextLM, nextID, hasMore, err := query.GetUserRoomSessionPage(user.Uid, cursorUT, cursorLM, cursorID, firstPage)
	if err != nil {
		response.ServerError(c, "获取会话列表失败")
		return
	}
	rids := make([]string, 0, len(sessionList))
	for _, s := range sessionList {
		if s.Rid != "" {
			rids = append(rids, s.Rid)
		}
	}
	lastMsgMap, err := query.GetLastMessagesByRidList(user.Uid, rids)
	if err != nil {
		response.ServerError(c, "获取会话最后一条消息失败")
		return
	}

	for _, s := range sessionList {
		if lastMsg, ok := lastMsgMap[s.Rid]; ok && lastMsg != nil {
			s.LastMessage = lastMsg
		}
		s.UnreadCount = query.SessionUnreadCount(s.LastSeqId, s.LastMessage)
	}

	totalUnread, err := query.ComputeUserTotalUnread(user.Uid)
	if err != nil {
		response.ServerError(c, "统计未读失败")
		return
	}

	resp := SessionListResponse{
		List:                  sessionList,
		NextCursorUpdateTime:  nextUT,
		NextCursorLastMsgTime: nextLM,
		NextCursorID:          nextID,
		HasMore:               hasMore,
		TotalUnread:           totalUnread,
	}
	response.Success(c, resp)
}

// SessionSearchResponse 会话搜索分页结果；下一页将 GET 参数 cursor 设为本次返回的 next_cursor。
type SessionSearchResponse struct {
	List       []*query.UserRoomSessionDto `json:"list"`
	NextCursor int64                       `json:"next_cursor"` // has_more 为 true 时填入；否则为 0
	HasMore    bool                        `json:"has_more"`
}

// SessionSearch 按关键词搜索会话，游标 + limit 下拉加载更多。
// Query: q=关键词（必填）&cursor=上一页的 next_cursor（首屏不传或 0）&limit=默认20最大50&scope=all|friends|groups
// 排序按 user_room_session.id 降序（与列表置顶顺序无关，便于稳定游标）。
func SessionSearch(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		response.BadRequest(c, "请输入搜索关键词")
		return
	}
	scope := strings.ToLower(strings.TrimSpace(c.DefaultQuery("scope", "all")))
	if scope != "all" && scope != "friends" && scope != "groups" {
		response.BadRequest(c, "scope 仅支持 all、friends、groups")
		return
	}
	cursor, _ := strconv.ParseInt(strings.TrimSpace(c.Query("cursor")), 10, 64)
	if cursor < 0 {
		cursor = 0
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("limit", "20")))
	list, nextCursor, hasMore, err := query.SearchUserRoomSessions(user.Uid, q, scope, cursor, limit)
	if err != nil {
		response.ServerError(c, "搜索会话失败")
		return
	}
	rids := make([]string, 0, len(list))
	for _, s := range list {
		if s.Rid != "" {
			rids = append(rids, s.Rid)
		}
	}
	lastMsgMap, err := query.GetLastMessagesByRidList(user.Uid, rids)
	if err != nil {
		response.ServerError(c, "获取会话最后一条消息失败")
		return
	}
	for _, s := range list {
		if lastMsg, ok := lastMsgMap[s.Rid]; ok && lastMsg != nil {
			s.LastMessage = lastMsg
		}
		s.UnreadCount = query.SessionUnreadCount(s.LastSeqId, s.LastMessage)
	}
	resp := SessionSearchResponse{List: list, HasMore: hasMore, NextCursor: 0}
	if hasMore {
		resp.NextCursor = nextCursor
	}
	response.Success(c, resp)
}

type UpdateLastSeqIdRequest struct {
	Rid   string `json:"rid" binding:"required"`
	SeqId int64  `json:"seq_id" binding:"required"`
}

func UpdateLastSeqId(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	var req UpdateLastSeqIdRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	// 查看是否存在会话
	session, err := query.GetUserRoomSession(user.Uid, req.Rid)
	if err != nil {
		response.ServerError(c, "会话不存在")
		return
	}
	if session == nil {
		response.ServerError(c, "会话不存在")
		return
	}
	if session.LastSeqId >= req.SeqId {
		response.Success(c, nil)
		return
	}
	err = query.UpdateUserRoomSessionLastSeqId(user.Uid, req.Rid, req.SeqId)
	if err != nil {
		response.ServerError(c, "更新已读状态失败")
		return
	}

	response.Success(c, nil)
}

type UpdateLastMentionSeqIdRequest struct {
	Rid          string `json:"rid" binding:"required"`
	MentionSeqId int64  `json:"mention_seq_id" binding:"required"`
}

func UpdateLastMentionSeqId(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}

	var req UpdateLastMentionSeqIdRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	session, err := query.GetUserRoomSession(user.Uid, req.Rid)
	if err != nil {
		response.ServerError(c, "会话不存在")
		return
	}
	if session == nil {
		response.ServerError(c, "会话不存在")
		return
	}
	if session.LastMentionSeqId >= req.MentionSeqId {
		response.Success(c, nil)
		return
	}
	err = query.UpdateUserRoomSessionLastMentionSeqId(user.Uid, req.Rid, req.MentionSeqId)
	if err != nil {
		response.ServerError(c, "更新@已读状态失败")
		return
	}

	response.Success(c, nil)
}

type UpdateSessionTopRequest struct {
	Rid   string `json:"rid" binding:"required"`
	IsTop bool   `json:"is_top"`
}

func UpdateSessionTop(c *gin.Context) {
	user := helper.GetUser(c)
	if user == nil {
		return
	}
	var req UpdateSessionTopRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	session, err := query.GetUserRoomSession(user.Uid, req.Rid)
	if err != nil {
		response.ServerError(c, "会话不存在")
		return
	}
	if session == nil {
		response.ServerError(c, "会话不存在")
		return
	}
	if err := query.SetUserRoomSessionTop(user.Uid, req.Rid, req.IsTop); err != nil {
		response.ServerError(c, "更新置顶失败")
		return
	}
	helper.PublishUserOperationLog(c, user.Uid, entity.UserOpSessionTop, req.Rid, map[string]any{
		"rid": req.Rid, "is_top": session.IsTop,
	}, map[string]any{
		"rid": req.Rid, "is_top": req.IsTop,
	})
	response.Success(c, nil)
}
