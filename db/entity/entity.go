package entity

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/xid"
	"gorm.io/gorm"
)

type BaseModel struct {
	Id         int64 `gorm:"autoIncrement;primaryKey;common:主键ID" json:"id" msgpack:"id"`
	CreateTime int64 `gorm:"not null;comment:创建时间" json:"create_time" msgpack:"create_time"`
	UpdateTime int64 `gorm:"not null;comment:最后更新时间" json:"-" msgpack:"_"`
	DeleteTime int64 `gorm:"not null;default:0;index;comment:软删除时间(0-未删除,非0-删除时间毫秒时间戳)" json:"-" msgpack:"delete_time"`
}

// setTimestamps 设置创建时间和更新时间（毫秒时间戳）
func (model *BaseModel) setTimestamps() {
	now := time.Now().UnixMilli()
	if model.CreateTime == 0 {
		model.CreateTime = now
	}
	if model.UpdateTime == 0 {
		model.UpdateTime = now
	}
}

// BeforeCreate 在创建前设置创建时间和更新时间（毫秒时间戳）
func (model *BaseModel) BeforeCreate(tx *gorm.DB) (err error) {
	model.setTimestamps()
	return
}

// BeforeUpdate 在更新前设置更新时间（毫秒时间戳）
func (model *BaseModel) BeforeUpdate(tx *gorm.DB) (err error) {
	model.UpdateTime = time.Now().UnixMilli()
	return
}

// SoftDelete 软删除，设置删除时间为当前毫秒时间戳
func (model *BaseModel) SoftDelete() {
	model.DeleteTime = time.Now().UnixMilli()
}

// IsDeleted 判断是否已软删除
func (model *BaseModel) IsDeleted() bool {
	return model.DeleteTime != 0
}

// 用户表
type User struct { // size=104 (0x68)
	BaseModel
	Uid          string `gorm:"uniqueIndex;not null;type:char(20);comment:用户唯一id" json:"uid" msgpack:"uid"`
	Username     string `gorm:"not null;unique;comment:用户名" json:"username" msgpack:"username"`
	Nickname     string `gorm:"not null;comment:昵称" json:"nickname" msgpack:"nickname"`
	Signature    string `gorm:"not null;default:'';comment:个性签名" json:"signature" msgpack:"signature"`
	Introduction string `gorm:"not null;default:'';comment:个人简介" json:"introduction" msgpack:"introduction"`
	Email        string `gorm:"unique;size:320;comment:邮箱" json:"email" msgpack:"email"`
	Password     string `gorm:"not null;type:varchar(60);comment:密码" json:"-" msgpack:"_"`
	// 头像文件 uf_id：使用 varchar(20)，避免 CHAR 右填充空格带来的显示问题
	AvatarUfId string `gorm:"column:avatar_uf_id;not null;default:'';type:varchar(20);comment:头像文件uf_id" json:"avatar_uf_id" msgpack:"avatar_uf_id"`
	// 是否允许非好友发起私聊（未加好友时对方是否可先开私聊房间）
	AllowPrivateChatFromNonFriend bool `gorm:"not null;default:true;comment:是否允许非好友发起私聊" json:"allow_private_chat_from_non_friend" msgpack:"allow_private_chat_from_non_friend"`
	// 用户本地数据库加密参数（用于客户端派生 user db key）
	DbKeySeed       string `gorm:"not null;default:'';type:varchar(64);comment:用户数据库密钥种子" json:"db_key_seed" msgpack:"db_key_seed"`
	DbKeyIterations int32  `gorm:"not null;default:120000;comment:用户数据库PBKDF2基础迭代次数" json:"db_key_iterations" msgpack:"db_key_iterations"`
	// 注册 IP 及解析地区
	RegisterIP string `gorm:"not null;default:'';type:varchar(45);comment:注册IP" json:"register_ip" msgpack:"register_ip"`
	Country    string `gorm:"not null;default:'';type:varchar(50);comment:国家" json:"country" msgpack:"country"`
	City       string `gorm:"not null;default:'';type:varchar(100);comment:城市" json:"city" msgpack:"city"`
	County     string `gorm:"not null;default:'';type:varchar(100);comment:区县" json:"county" msgpack:"county"`
}

func (model *User) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.Uid = xid.New().String()
	if model.DbKeySeed == "" {
		model.DbKeySeed = xid.New().String()
	}
	if model.DbKeyIterations <= 0 {
		model.DbKeyIterations = 120000
	}
	return
}

// UserTheme 用户外观主题配置（以 JSON 字符串整体存储，便于后续扩展字段）
type UserTheme struct {
	BaseModel
	Uid       string `gorm:"type:char(20);not null;uniqueIndex;comment:用户uid" json:"uid"`
	ThemeJson string `gorm:"type:text;not null;default:'{}';comment:主题配置JSON" json:"theme_json"`
}

// UserRememberToken 记住登录 token，与设备绑定；后端仅校验 token 合法来源（device_id 一致）
type UserRememberToken struct {
	BaseModel
	Uid       string `gorm:"not null;type:char(20);uniqueIndex:uid_unique;comment:用户UID" json:"uid"`
	Token     string `gorm:"not null;type:varchar(128);uniqueIndex:token_unique;comment:记住登录token" json:"token"`
	DeviceId  string `gorm:"not null;type:varchar(128);comment:绑定的机器码" json:"device_id"`
	ExpiresAt int64  `gorm:"not null;comment:过期时间(毫秒时间戳)" json:"expires_at"`
}

// 用户Token表,只存储refresh_token
type UserRefreshToken struct {
	BaseModel
	Uid       string `gorm:"not null;type:char(20);index:uid_type_index,priority:1;comment:关联的用户ID;" json:"uid" msgpack:"uid"`
	Jti       string `gorm:"unique;not null;type:char(36);comment:Token的ID" json:"jti" msgpack:"jti"`
	ExpiresAt int64  `gorm:"not null;comment:Token过期时间" json:"expires_at" msgpack:"expires_at"`
	Revoked   bool   `gorm:"not null;default:false;comment:Token是否被撤销" json:"revoked" msgpack:"revoked"`
	UserAgent string `gorm:"not null;type:varchar(4096);comment:用户代理" json:"user_agent" msgpack:"user_agent"`
	IP        string `gorm:"not null;type:varchar(45);comment:用户IP" json:"ip" msgpack:"ip"`
}
type UserFriendGroup struct {
	BaseModel
	Uid         string `gorm:"not null;type:char(20);index:uid_index;comment:用户ID" json:"uid" msgpack:"uid"`
	Gid         string `gorm:"not null;type:char(20);uniqueIndex:gid_unique_index;comment:分组ID" json:"gid" msgpack:"gid"`
	Name        string `gorm:"not null;comment:分组名称" json:"name" msgpack:"name"`
	IsDefault   bool   `gorm:"not null;default:false;comment:是否默认分组" json:"is_default" msgpack:"is_default"`
	Sort        int    `gorm:"not null;default:0;comment:排序" json:"sort" msgpack:"sort"`
	Description string `gorm:"not null;default:'';comment:分组描述" json:"description" msgpack:"description"`
}

func (model *UserFriendGroup) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.Gid = xid.New().String()
	return
}

// 用户好友关系表
// 业务层“是否删除”只看 FriendDeleteTime：0=未删除，非0=删除时间(毫秒)，不参与 delete_time
type UserFriend struct {
	BaseModel
	Uid              string `gorm:"not null;type:char(20);uniqueIndex:uid_friend_uid_index,priority:1;index:uid_index;comment:用户ID" json:"uid" msgpack:"uid"`
	FriendUid        string `gorm:"not null;type:char(20);uniqueIndex:uid_friend_uid_index,priority:2;index:friend_uid_index;comment:好友用户ID" json:"friend_uid" msgpack:"friend_uid"`
	Gid              string `gorm:"not null;type:char(20);index:gid_index;comment:好友分组ID" json:"gid" msgpack:"gid"`
	Remark           string `gorm:"not null;default:'';comment:好友备注" json:"remark" msgpack:"remark"`
	FriendDeleteTime int64  `gorm:"not null;default:0;comment:好友删除时间(0-未删除,非0-毫秒时间戳)" json:"friend_delete_time" msgpack:"friend_delete_time"`
}

type RoomType = int8

// RoomState 房间状态（群聊解散等）
type RoomState = int8

const (
	RoomStateDissolved RoomState = 0 // 已解散
	RoomStateActive    RoomState = 1 // 正常
)

const (
	RoomTypePrivate      RoomType = 0 // 私聊
	RoomTypeGroup        RoomType = 1 // 群聊
	RoomTypeGroupPrivate RoomType = 2 // 群私聊（从群聊里拉出的临时私聊）
	RoomTypeSelfChat     RoomType = 3 // 自聊（与自己对话/笔记，独立房间类型）
)

// RoomCategory 房间分类表（用于房间列表筛选与卡片展示）
type RoomCategory struct {
	BaseModel
	Cid  string `gorm:"not null;type:char(20);uniqueIndex:cid_unique;comment:分类id" json:"cid" msgpack:"cid"`
	Name string `gorm:"not null;type:varchar(64);comment:分类名称" json:"name" msgpack:"name"`
	Sort int    `gorm:"not null;default:0;comment:排序(越小越靠前)" json:"sort" msgpack:"sort"`
}

func (model *RoomCategory) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	if model.Cid == "" {
		model.Cid = xid.New().String()
	}
	return
}

// RoomTag 房间标签表（多对多：一房间多标签）
type RoomTag struct {
	BaseModel
	Tid  string `gorm:"not null;type:char(20);uniqueIndex:tid_unique;comment:标签id" json:"tid" msgpack:"tid"`
	Name string `gorm:"not null;type:varchar(32);comment:标签名称" json:"name" msgpack:"name"`
	Sort int    `gorm:"not null;default:0;comment:排序(越小越靠前)" json:"sort" msgpack:"sort"`
}

func (model *RoomTag) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	if model.Tid == "" {
		model.Tid = xid.New().String()
	}
	return
}

// RoomTagRelation 房间与标签关联表
type RoomTagRelation struct {
	BaseModel
	Rid   string `gorm:"not null;type:char(20);uniqueIndex:rid_tid_index,priority:1;index:idx_rid;comment:房间rid" json:"rid" msgpack:"rid"`
	TagId string `gorm:"not null;type:char(20);uniqueIndex:rid_tid_index,priority:2;index:idx_tag_id;comment:标签tid" json:"tag_id" msgpack:"tag_id"`
}

// 房间表
type Room struct {
	BaseModel
	Rid         string   `gorm:"not null;type:char(20);unique;comment:房间id" json:"rid" msgpack:"rid"`
	Name        string   `gorm:"not null;type:varchar(360);comment:房间名称" json:"name" msgpack:"name"`
	CreateUid   string   `gorm:"not null;type:char(20);comment:创建者uid;" json:"create_uid" msgpack:"create_uid"`
	Description string   `gorm:"not null;default:'';type:varchar(1024);comment:房间描述" json:"description" msgpack:"description"`
	State       int8     `gorm:"not null;default:1;comment:房间状态" json:"state" msgpack:"state"`
	Sort        int      `gorm:"not null;default:0;comment:排序" json:"sort" msgpack:"sort"`
	Password    string   `gorm:"not null;default:'';type:varchar(60);comment:房间密码" json:"-" msgpack:"_"`
	HasPassword bool     `gorm:"-" json:"has_password" msgpack:"-"` // 仅 API 返回用，不落库
	AvatarUfId  string   `gorm:"column:avatar_uf_id;not null;default:'';type:varchar(20);comment:房间头像uf_id" json:"avatar_uf_id" msgpack:"avatar_uf_id"`
	Type        RoomType `gorm:"not null;default:0;comment:房间类型(0-私聊,1-群聊,2-群私聊,3-自聊)" json:"type" msgpack:"type"`
	MemberCount int      `gorm:"not null;default:0;comment:成员数量" json:"member_count" msgpack:"member_count"`
	// 是否允许非好友私聊（仅私聊房间有效；true 表示该房间可在双方非好友时创建并发消息）
	AllowNonFriendChat bool `gorm:"not null;default:true;comment:是否允许非好友私聊(仅私聊房间)" json:"allow_non_friend_chat" msgpack:"allow_non_friend_chat"`
	// 来源群 rid（仅群私聊 type=2 时有效；互加好友后改为私聊 type=0 时可清空）
	FromRoomRid string `gorm:"not null;default:'';type:char(20);comment:来源群rid(仅群私聊)" json:"from_room_rid" msgpack:"from_room_rid"`
	// 分类 id（关联 RoomCategory.Cid，可选）
	CategoryId string `gorm:"not null;default:'';type:char(20);index:idx_category_id;comment:房间分类id" json:"category_id" msgpack:"category_id"`
	// 加入房间是否需管理员/房主审批（默认需要）
	JoinApprovalRequired bool `gorm:"not null;default:true;comment:加入是否需审批" json:"join_approval_required" msgpack:"join_approval_required"`
	// 申请加入时是否需回答验证问题
	JoinQuestionEnabled bool `gorm:"not null;default:false;comment:是否启用加入验证问题" json:"join_question_enabled" msgpack:"join_question_enabled"`
	// 加入验证问题文案（仅 API 返回，不含答案）
	JoinQuestion string `gorm:"not null;default:'';type:varchar(256);comment:加入验证问题" json:"join_question" msgpack:"join_question"`
	// 验证答案与预设一致时是否免审批直接加入
	JoinQuestionAutoApprove bool `gorm:"not null;default:false;comment:验证答案正确时免审批" json:"join_question_auto_approve" msgpack:"join_question_auto_approve"`
	// 加入验证问题答案（不落公开 API）
	JoinQuestionAnswer string `gorm:"not null;default:'';type:varchar(128);comment:加入验证问题答案" json:"-" msgpack:"-"`
}

func (model *Room) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.Rid = xid.New().String()
	return
}

// RoomInvite 邀请链接（每次创建一条独立记录，token 全局唯一）
type RoomInvite struct {
	BaseModel
	InviteId         string `gorm:"not null;type:char(20);uniqueIndex:invite_id_unique;comment:邀请记录ID" json:"invite_id"`
	Token            string `gorm:"not null;type:varchar(64);uniqueIndex:invite_token_unique;index:idx_invite_token;comment:邀请token" json:"token"`
	Rid              string `gorm:"not null;type:char(20);index:idx_invite_rid;comment:房间rid" json:"rid"`
	InviterUid       string `gorm:"not null;type:char(20);index:idx_invite_inviter_uid;comment:邀请人uid" json:"inviter_uid"`
	BypassPassword   bool   `gorm:"not null;default:false;comment:是否可免密加入" json:"bypass_password"`
	ExpiresAt        int64  `gorm:"not null;index:idx_invite_expires_at;comment:过期时间(毫秒时间戳)" json:"expires_at"`
	JoinSuccessCount int64  `gorm:"not null;default:0;comment:成功加入次数" json:"join_success_count"`
	LastJoinUid      string `gorm:"not null;default:'';type:char(20);comment:最后加入人uid" json:"last_join_uid"`
	LastJoinAt       int64  `gorm:"not null;default:0;comment:最后加入时间(毫秒时间戳)" json:"last_join_at"`
}

func (model *RoomInvite) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	if model.InviteId == "" {
		model.InviteId = xid.New().String()
	}
	return
}

// RoomInviteJoin 邀请加入记录（成功加入后写入，便于审计邀请链路）
type RoomInviteJoin struct {
	BaseModel
	InviteId   string `gorm:"not null;type:char(20);index:idx_invite_join_invite_id;comment:邀请记录ID" json:"invite_id"`
	Token      string `gorm:"not null;type:varchar(64);index:idx_invite_join_token;comment:邀请token" json:"token"`
	Rid        string `gorm:"not null;type:char(20);index:idx_invite_join_rid;comment:房间rid" json:"rid"`
	InviterUid string `gorm:"not null;type:char(20);index:idx_invite_join_inviter_uid;comment:邀请人uid" json:"inviter_uid"`
	JoinUid    string `gorm:"not null;type:char(20);index:idx_invite_join_join_uid;comment:加入人uid" json:"join_uid"`
	JoinAt     int64  `gorm:"not null;index:idx_invite_join_join_at;comment:加入时间(毫秒时间戳)" json:"join_at"`
}

// RoomPinnedMessage 房间置顶消息表，每房间仅一条置顶记录
type RoomPinnedMessage struct {
	BaseModel
	Rid         string `gorm:"not null;type:char(20);uniqueIndex:idx_room_pinned_rid;comment:房间ID" json:"rid"`
	Mid         string `gorm:"not null;type:char(20);index:idx_room_pinned_mid;comment:消息mid" json:"mid"`
	SeqId       int64  `gorm:"not null;comment:消息seq_id" json:"seq_id"`
	OperatorUid string `gorm:"not null;type:char(20);comment:置顶操作人uid" json:"operator_uid"`
}

// RoomAnnouncement 房间公告表，每房间可多条；支持富文本（HTML 存 content）；软删除用 delete_time
type RoomAnnouncement struct {
	BaseModel
	Rid       string          `gorm:"not null;type:char(20);index:idx_rid;comment:房间ID" json:"rid"`
	Content   json.RawMessage `gorm:"not null;type:jsonb;default:'[]';comment:公告内容(JSON结构)" json:"content"`
	UpdatedBy string          `gorm:"not null;default:'';type:char(20);comment:最后更新人UID" json:"updated_by"`
	Pinned    bool            `gorm:"not null;default:false;comment:是否置顶" json:"pinned"`
}

// RoomUserRole 房间成员角色
type RoomUserRole int8

const (
	RoomUserRoleNormal RoomUserRole = 0 // 普通用户
	RoomUserRoleAdmin  RoomUserRole = 1 // 管理员
	RoomUserRoleOwner  RoomUserRole = 2 // 房主
)

type RoomUser struct {
	BaseModel
	Rid           string       `gorm:"not null;type:char(20);uniqueIndex:room_user_index,priority:1;comment:房间id" json:"rid" msgpack:"rid"`
	Uid           string       `gorm:"not null;type:char(20);uniqueIndex:room_user_index,priority:2;comment:用户id" json:"uid" msgpack:"uid"`
	Role          RoomUserRole `gorm:"not null;default:0;comment:角色(0-普通用户,1-管理员,2-房主)" json:"role" msgpack:"role"`
	MuteUntil       int64  `gorm:"not null;default:0;comment:禁言截止时间(毫秒时间戳,0-未禁言)" json:"mute_until" msgpack:"mute_until"`
	MuteOperatorUid string `gorm:"not null;default:'';type:char(20);comment:最近一次禁言操作人uid" json:"mute_operator_uid" msgpack:"mute_operator_uid"`
	RoomNickname  string       `gorm:"not null;default:'';type:varchar(64);comment:我的本群昵称" json:"room_nickname" msgpack:"room_nickname"`
	RoomRemark    string       `gorm:"not null;default:'';type:varchar(255);comment:房间备注(仅自己可见)" json:"room_remark" msgpack:"room_remark"`
	JoinRoomTime  int64        `gorm:"not null;default:0;comment:加入房间时间(毫秒时间戳)" json:"join_room_time" msgpack:"join_room_time"`
	LastSpeakTime int64        `gorm:"not null;default:0;comment:最后发言时间(毫秒时间戳)" json:"last_speak_time" msgpack:"last_speak_time"`
	LastSpeakIP   string       `gorm:"not null;default:'';type:varchar(45);comment:最后发言IP" json:"-" msgpack:"_"`
}

func (model *RoomUser) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	if model.JoinRoomTime == 0 {
		model.JoinRoomTime = model.CreateTime
	}
	return
}

type RoomMessage struct {
	BaseModel
	ClientCreateTime int64  `gorm:"not null;comment:客户端创建时间(毫秒时间戳)" json:"client_create_time" msgpack:"client_create_time"`
	// idx_room_message_open_rid_seq：每房间「当前可见」最后一条消息（DISTINCT ON rid ORDER BY seq_id DESC）与会话列表 lm 子查询
	Rid              string `gorm:"not null;index:rid_sender_client_mid_index,priority:1;uniqueIndex:rid_sender_client_mid_unique_idx,priority:1;uniqueIndex:room_seq_unique_idx,priority:1;index:idx_room_message_open_rid_seq,priority:1,where:state = 1 AND delete_time = 0 AND withdraw_time = 0;type:char(20);comment:房间id" json:"rid" msgpack:"rid"`
	SeqId            int64  `gorm:"not null;uniqueIndex:room_seq_unique_idx,priority:2;index:idx_room_message_open_rid_seq,priority:2,sort:desc;comment:消息顺序号"  json:"seq_id" msgpack:"seq_id"`
	Mid              string `gorm:"not null;uniqueIndex:msg_unique_index;type:char(20);comment:消息唯一id" json:"mid" msgpack:"mid"`
	ClientMid        string `gorm:"not null;index:rid_sender_client_mid_index,priority:3;uniqueIndex:rid_sender_client_mid_unique_idx,priority:3;type:char(20);comment:客户端消息id" json:"client_mid" msgpack:"client_mid"`
	SenderUid        string `gorm:"not null;type:varchar(20);index:rid_sender_client_mid_index,priority:2;uniqueIndex:rid_sender_client_mid_unique_idx,priority:2;comment:发送消息用户id" json:"sender_uid" msgpack:"sender_uid"`
	State            int8   `gorm:"not null;default:1;comment:消息状态(0-隐藏,1-正常)" json:"state" msgpack:"state"`
	IP               string `gorm:"not null;type:varchar(45);comment:用户Ip" json:"ip" msgpack:"ip"`
	WithdrawTime     int64  `gorm:"not null;default:0;comment:消息撤回时间" json:"withdraw_time" msgpack:"withdraw_time"`
}

func (model *RoomMessage) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.Mid = xid.New().String()
	return
}

type RoomMessageWithdrawAck struct {
	BaseModel
	State         int8   `gorm:"not null;default:0;comment:状态(0-撤回中,1-撤回成功,2-撤回失败)"  json:"state" msgpack:"state"`
	ConfirmedTime int64  `gorm:"not null;default:0;comment:收到用户确认接收时间"  json:"confirmed_time" msgpack:"confirmed_time"`
	LastTryTime   int64  `gorm:"not null;default:0;comment:最后重试时间" json:"last_try_time" msgpack:"last_try_time"`
	RetryCount    int32  `gorm:"not null;default:0;comment:重试次数" json:"retry_count" msgpack:"retry_count"`
	Rid           string `gorm:"not null;index:room_index;type:char(20);comment:房间id" json:"rid" msgpack:"rid"`
	SeqId         int64  `gorm:"not null;comment:消息顺序号" json:"seq_id" msgpack:"seq_id"`
	Uid           string `gorm:"not null;uniqueIndex:unique_idx_user_msg,priority:1;type:char(20);comment:发送消息用户id" json:"uid" msgpack:"uid"`
	Mid           string `gorm:"not null;uniqueIndex:unique_idx_user_msg,priority:2;type:char(20);comment:消息唯一id"  json:"mid" msgpack:"mid"`
}

// RoomMessageMention @我的消息表，用于快速查询@我的消息
type RoomMessageMention struct {
	BaseModel
	Rid       string `gorm:"not null;index:idx_uid_rid_isread_seq,priority:2;type:char(20);comment:房间id" json:"rid"`
	Mid       string `gorm:"not null;index:idx_mid;type:char(20);comment:消息id" json:"mid"`
	SeqId     int64  `gorm:"not null;index:idx_uid_rid_isread_seq,priority:4;comment:消息顺序号" json:"seq_id"`
	Uid       string `gorm:"not null;index:idx_uid_rid_isread_seq,priority:1;type:char(20);comment:被@的用户id" json:"uid"`
	SenderUid string `gorm:"not null;type:char(20);comment:发送消息的用户id" json:"sender_uid"`
	IsAtAll   bool   `gorm:"not null;default:false;comment:是否是@all" json:"is_at_all"`
	IsRead    bool   `gorm:"not null;default:false;index:idx_uid_rid_isread_seq,priority:3;comment:是否已读" json:"is_read"`
	ReadAt    int64  `gorm:"not null;default:0;comment:阅读时间" json:"read_at"`
}

func (model *RoomMessageMention) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	return
}

type RoomMessageContentType string
type RoomMessageContent struct {
	BaseModel
	// idx_rmc_mid_ct：按 mid 拉正文行（与 client_create_time 组合）
	// idx_rmc_mid_alive：仅未删除行上的 mid btree，加速 session/list 等「mid IN ? AND delete_time=0」与 ROW_NUMBER 子查询（理想形态为 (mid,id) WHERE delete_time=0，id 在嵌入 BaseModel 上无法与本 struct 同声明复合索引，生产可由 DBA 手工补 btree(mid,id) WHERE delete_time=0）
	ClientCreateTime int64                  `gorm:"not null;comment:客户端创建时间(毫秒时间戳);index:idx_rmc_mid_ct,priority:2" json:"client_create_time" msgpack:"client_create_time"`
	Type             RoomMessageContentType `gorm:"not null;type:varchar(32);comment:内容类型" json:"type" msgpack:"type"`
	TypeId           string                 `gorm:"not null;default:'';type:varchar(20);comment:内容类型" json:"type_id" msgpack:"type_id"`
	Content          json.RawMessage        `gorm:"not null;type:jsonb;default:'{}';comment:消息内容" json:"content" msgpack:"content"`
	Mid              string                 `gorm:"not null;index:msg_index;uniqueIndex:msg_mid_client_cid_unique_index,priority:1;index:idx_rmc_mid_ct,priority:1;index:idx_rmc_mid_alive,where:delete_time = 0;type:char(20);comment:消息唯一id" json:"mid" msgpack:"mid"`
	ClientCid        string                 `gorm:"not null;uniqueIndex:msg_mid_client_cid_unique_index,priority:2;index:client_cid_index;type:char(20);comment:客户端消息id" json:"client_cid" msgpack:"client_cid"`
	Cid              string                 `gorm:"not null;uniqueIndex:msg_cid_unique_index;type:char(20);comment:内容id" json:"cid" msgpack:"cid"`
}

func (model *RoomMessageContent) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.Cid = xid.New().String()
	return
}

type RoomMessageAck struct {
	BaseModel
	State         int8   `gorm:"not null;default:0;comment:状态(0-已入库 1-用户已接收,2-超时未接收)"  json:"state" msgpack:"state"`
	ConfirmedTime int64  `gorm:"not null;default:0;comment:收到用户确认接收时间"  json:"confirmed_time" msgpack:"confirmed_time"`
	LastTryTime   int64  `gorm:"not null;default:0;comment:最后重试时间" json:"last_try_time" msgpack:"last_try_time"`
	RetryCount    int32  `gorm:"not null;default:0;comment:'重试次数'" json:"retry_count" msgpack:"retry_count"`
	Rid           string `gorm:"not null;index:room_index;type:char(20);comment:房间id" json:"rid" msgpack:"rid"`
	SeqId         int64  `gorm:"not null;comment:消息顺序号" json:"seq_id" msgpack:"seq_id"`
	Uid           string `gorm:"not null;uniqueIndex:unique_idx_user_msg,priority:1;type:char(20);comment:用户id" json:"uid" msgpack:"uid"`
	Mid           string `gorm:"not null;uniqueIndex:unique_idx_user_msg,priority:2;type:char(20);comment:消息id"  json:"mid" msgpack:"mid"`
	IsOffline     bool   `gorm:"not null;default:false;comment:是否是离线消息" json:"is_offline" msgpack:"is_offline"`
}

type DisturbType int8

const (
	DisturbTypeNormal  DisturbType = 0 // 正常提醒（声音+振动+计数）
	DisturbTypeQuiet   DisturbType = 1 // 静音模式（无声音，有计数）
	DisturbTypeMute    DisturbType = 2 // 完全免打扰（无声音，无计数）
	DisturbTypeMention DisturbType = 3 // 仅@我提醒
	DisturbTypeCustom  DisturbType = 4 // 自定义模式
)

// 免打扰配置详情
type DisturbConfig struct {
	// 工作时间设置
	WorkStartTime string `json:"work_start_time,omitempty"` // "09:00"
	WorkEndTime   string `json:"work_end_time,omitempty"`   // "18:00"
	Workdays      []int  `json:"workdays,omitempty"`        // [1,2,3,4,5] 周一到周五

	// 关键词设置
	Keywords       []string `json:"keywords,omitempty"`        // 触发提醒的关键词
	WhitelistUsers []string `json:"whitelist_users,omitempty"` // 白名单用户

	// 其他设置
	EnablePush bool `json:"enable_push"` // 是否推送
	ShowUnread bool `json:"show_unread"` // 显示未读计数
}

type UserRoomSession struct {
	BaseModel
	// idx_urs_uid_state：会话列表 WHERE uid AND state AND delete_time（delete_time 在 BaseModel，由单列索引覆盖）
	// idx_urs_session_open：活跃会话按 uid 过滤的部分索引，减轻 GetUserRoomSessionPage 大表上 uid=? AND state=1 AND delete_time=0 的扫描
	Uid         string      `gorm:"not null;type:char(20);uniqueIndex:uid_session_index,priority:1;index:idx_urs_uid_state,priority:1;index:idx_urs_session_open,where:delete_time = 0 AND state = 1" json:"uid"`
	Rid         string      `gorm:"not null;type:char(20);uniqueIndex:uid_session_index,priority:2" json:"rid"`
	Rsid        string      `gorm:"not null;type:char(20);uniqueIndex:session_unique_index" json:"rsid"`
	DisturbType DisturbType `gorm:"not null;default:0;comment:免打扰类型" json:"disturb_type"`
	// 从 DisturbConfig 提取的常用字段
	EnablePush    bool   `gorm:"not null;default:true;comment:是否推送" json:"enable_push"`
	ShowUnread    bool   `gorm:"not null;default:true;comment:显示未读计数" json:"show_unread"`
	WorkStartTime string `gorm:"type:varchar(10);comment:工作开始时间" json:"work_start_time"`
	WorkEndTime   string `gorm:"type:varchar(10);comment:工作结束时间" json:"work_end_time"`
	// 完整的 JSON 配置（保留用于详细配置）
	DisturbConfig    string `gorm:"not null;default:'{}';type:jsonb;comment:完整免打扰配置" json:"disturb_config"`
	LastSeqId        int64  `gorm:"not null;default:0;comment:最后阅读的seq_id" json:"last_seq_id"`
	LastMentionSeqId int64  `gorm:"not null;default:0;comment:最后阅读的@我消息seq_id" json:"last_mention_seq_id"`
	IsTop            bool   `gorm:"not null;default:false" json:"is_top"`
	State            int8   `gorm:"not null;default:1;index:idx_urs_uid_state,priority:2" json:"state"`
	// LastRoomMessageCreateTime 该房间当前最后一条有效消息的创建时间(毫秒)；会话列表第二排序键（第一键为 update_time）。
	LastRoomMessageCreateTime int64 `gorm:"not null;default:0;comment:房间最后有效消息创建时间(ms)" json:"last_room_message_create_time"`
}

func (model *UserRoomSession) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.Rsid = xid.New().String()
	return
}

// UserRoomBlock 用户屏蔽的私聊/群私聊会话：屏蔽后该用户无法向该房间发送任何消息
type UserRoomBlock struct {
	BaseModel
	Uid string `gorm:"not null;uniqueIndex:uid_rid_index,priority:1;type:char(20);comment:用户uid" json:"uid" msgpack:"uid"`
	// idx_urb_rid_open：按 rid 查「谁屏蔽了该房间」GetUidsWhoBlockedRoom（unique 为 uid 优先，无法单独扫 rid）
	Rid string `gorm:"not null;uniqueIndex:uid_rid_index,priority:2;index:idx_urb_rid_open,where:delete_time = 0;type:char(20);comment:房间rid(仅私聊/群私聊)" json:"rid" msgpack:"rid"`
}

func (UserRoomBlock) TableName() string {
	return "user_room_block"
}

// RoomUserBlockUser 房间内屏蔽某用户：uid 在该房间不接收 target_uid 的消息（仅影响推送与拉取列表）
type RoomUserBlockUser struct {
	BaseModel
	Uid       string `gorm:"not null;uniqueIndex:uid_rid_target_index,priority:1;type:char(20);comment:屏蔽方uid" json:"uid" msgpack:"uid"`
	// idx_rubusr_rid_target_open：GetUidsWhoBlockedUserInRoom(rid, senderUid) 按 rid + target_uid 且未删除
	Rid       string `gorm:"not null;uniqueIndex:uid_rid_target_index,priority:2;index:idx_rubusr_rid_target_open,priority:1,where:delete_time = 0;type:char(20);comment:房间rid" json:"rid" msgpack:"rid"`
	TargetUid string `gorm:"not null;uniqueIndex:uid_rid_target_index,priority:3;index:idx_rubusr_rid_target_open,priority:2;type:char(20);comment:被屏蔽用户uid" json:"target_uid" msgpack:"target_uid"`
}

func (RoomUserBlockUser) TableName() string {
	return "room_user_block_user"
}

// MediaCallRecord 媒体通话记录表：用于通话结束后的审计与历史查询
type MediaCallRecord struct {
	BaseModel
	CallID      string `gorm:"not null;type:char(36);uniqueIndex:media_call_id_unique;comment:通话ID" json:"call_id" msgpack:"call_id"`
	Rid         string `gorm:"not null;type:char(20);index:idx_media_call_rid;comment:房间rid" json:"rid" msgpack:"rid"`
	CallType    string `gorm:"not null;type:varchar(16);comment:通话类型(audio/video)" json:"call_type" msgpack:"call_type"`
	CallScene   string `gorm:"not null;type:varchar(16);comment:通话场景(friend/room等)" json:"call_scene" msgpack:"call_scene"`
	InviterUID  string `gorm:"not null;type:char(20);index:idx_media_call_inviter;comment:发起人uid" json:"inviter_uid" msgpack:"inviter_uid"`
	InviteeUIDs string `gorm:"not null;type:jsonb;comment:被邀请人uid列表(JSON数组)" json:"invitee_uids" msgpack:"invitee_uids"`
	StartedAt   int64  `gorm:"not null;comment:通话开始时间(毫秒时间戳)" json:"started_at" msgpack:"started_at"`
	EndedAt     int64  `gorm:"not null;default:0;comment:通话结束时间(毫秒时间戳,0表示未知)" json:"ended_at" msgpack:"ended_at"`
	DurationSec int64  `gorm:"not null;default:0;comment:通话持续时长(秒)" json:"duration_sec" msgpack:"duration_sec"`
	EndReason   string `gorm:"not null;type:varchar(32);comment:结束原因(hangup/ended/all_rejected等)" json:"end_reason" msgpack:"end_reason"`
	OperatorUID string `gorm:"not null;default:'';type:char(20);comment:结束操作人uid" json:"operator_uid" msgpack:"operator_uid"`
}

func (MediaCallRecord) TableName() string {
	return "media_call_record"
}

type UploadFileChunk struct {
	BaseModel
	Hash     string `gorm:"not null;type:char(64);comment:文件块哈希" json:"hash"`
	Size     int64  `gorm:"not null;comment:文件块大小" json:"size"`
	FileHash string `gorm:"not null;index:chunk_idx_file;type:char(64);comment:文件哈希" json:"file_hash"`
	Fid      string `gorm:"not null;uniqueIndex:chunk_idx_file_unique;type:char(20);comment:完整文件id" json:"fid"`
	ChunkIdx uint32 `gorm:"not null;uniqueIndex:chunk_idx_file_unique;comment:块索引" json:"chunk_idx"`
}

type UploadFile struct {
	BaseModel
	Fid        string `gorm:"not null;uniqueIndex:fid_idx_unique;type:char(20);comment:完整文件id" json:"fid"`
	Hash       string `gorm:"not null;uniqueIndex:hash_idx_unique;type:char(64);comment:完整文件哈希" json:"hash"`
	Ext        string `gorm:"not null;default:'';comment:文件扩展名" json:"ext"`
	TypeMain   string `gorm:"not null;default:'';type:varchar(128);comment:文件主类型" json:"type_main"`
	TypeSub    string `gorm:"not null;default:'';type:varchar(128);comment:文件子类型" json:"type_sub"`
	Path       string `gorm:"not null;default:'';type:varchar(4096);comment:文件储存路径" json:"path"`
	TotalSize  uint64 `gorm:"not null;comment:文件总大小" json:"total_size"`
	ChunkSize  uint32 `gorm:"not null;comment:块大小" json:"chunk_size"`
	ChunkCount uint32 `gorm:"not null;comment:块总数" json:"chunk_count"`
	Width      uint32 `gorm:"not null;default:0;comment:宽度" json:"width"`
	Height     uint32 `gorm:"not null;default:0;comment:高度" json:"height"`
	Thumb      string `gorm:"not null;default:'';comment:缩略图" json:"thumb"`
	Duration   uint64 `gorm:"not null;default:0;comment:媒体时长(微秒)" json:"duration"`
	State      uint8  `gorm:"not null;default:0;comment:文件状态(0-未上传,1-上传中,2-合并完成,10-文件校验失败)" json:"state"`
}

func (model *UploadFile) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.Fid = xid.New().String()
	return
}

// UploadScene 上传场景：仅公开场景（用户头像、房间头像、房间公告、房间信息）可按 uf_id 公开访问
const (
	UploadSceneUserAvatar       = "user_avatar"       // 用户头像
	UploadSceneRoomAvatar       = "room_avatar"       // 房间头像
	UploadSceneRoomAnnouncement = "room_announcement" // 房间公告
	UploadSceneRoomInfo         = "room_info"         // 房间信息
)

func IsPublicUploadScene(scene string) bool {
	switch scene {
	case UploadSceneUserAvatar, UploadSceneRoomAvatar, UploadSceneRoomAnnouncement, UploadSceneRoomInfo:
		return true
	default:
		return false
	}
}

type UserUploadFile struct {
	BaseModel
	Filename   string `gorm:"not null;comment:文件名" json:"filename"`
	UfId       string `gorm:"not null;uniqueIndex:uf_idx_unique;type:char(20);comment:用户文件id" json:"uf_id"`
	Uid        string `gorm:"not null;index:uid_index;type:char(20);comment:用户id" json:"uid"`
	Fid        string `gorm:"not null;index:fid_index;type:char(20);comment:完整文件id" json:"fid"`
	Scene      string `gorm:"not null;default:'';index:idx_scene;type:varchar(32);comment:上传场景:user_avatar,room_avatar,room_announcement,room_info,空为消息等" json:"scene"`
	ClientType int8   `gorm:"not null;default:0;comment:客户端类型(0-未知,1-PC,2-移动端,3-小程序)" json:"client_type"`
	IP         string `gorm:"not null;type:varchar(45);comment:用户Ip" json:"ip"`
}

func (model *UserUploadFile) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.UfId = xid.New().String()
	return
}

type UserCurrentStatus struct {
	BaseModel
	Uid              string `gorm:"not null;type:char(20);uniqueIndex:uid_unique_index;comment:用户UID" json:"uid" msgpack:"uid"`
	IsOnline         bool   `gorm:"not null;default:false;index;comment:是否在线" json:"is_online" msgpack:"is_online"`
	CurrentStatus    string `gorm:"not null;type:varchar(32);default:'offline';comment:当前状态:online/offline 或 展示状态键(away/busy等)" json:"current_status" msgpack:"current_status"`
	LastOnline       int64  `gorm:"not null;default:0;comment:最后在线时间" json:"last_online" msgpack:"last_online"`
	LastLogin        int64  `gorm:"not null;default:0;comment:最后登录时间" json:"last_login" msgpack:"last_login"`
	LastLogout       int64  `gorm:"not null;default:0;comment:最后登出时间" json:"last_logout" msgpack:"last_logout"`
	LastHeartbeat    int64  `gorm:"not null;default:0;comment:最后心跳时间" json:"last_heartbeat" msgpack:"last_heartbeat"`
	CustomState      string `gorm:"not null;default:'';type:varchar(100);comment:自定义状态" json:"custom_state" msgpack:"custom_state"`
	CurrentSessionId string `gorm:"not null;type:char(64);comment:当前会话ID" json:"current_session_id" msgpack:"current_session_id"`
	WebsocketId      string `gorm:"not null;type:char(100);comment:WebSocket连接ID" json:"websocket_id" msgpack:"websocket_id"`
	// 从 DeviceInfo 提取的常用字段
	Platform    string `gorm:"type:varchar(20);index:idx_platform;comment:平台:web/ios/android/desktop" json:"platform" msgpack:"platform"`
	DeviceType  string `gorm:"type:varchar(20);comment:设备类型:phone/tablet/desktop" json:"device_type" msgpack:"device_type"`
	DeviceModel string `gorm:"type:varchar(100);comment:设备型号" json:"device_model" msgpack:"device_model"`
	OSVersion   string `gorm:"type:varchar(50);comment:操作系统版本" json:"os_version" msgpack:"os_version"`
	AppVersion  string `gorm:"type:varchar(20);comment:应用版本" json:"app_version" msgpack:"app_version"`
	// 完整的设备信息（保留用于详细查询）
	DeviceInfo        DeviceInfoJSON `gorm:"not null;type:jsonb;comment:完整设备信息" json:"device_info" msgpack:"device_info"`
	ConcurrentDevices int            `gorm:"not null;default:0;comment:并发登录设备数" json:"concurrent_devices" msgpack:"concurrent_devices"`
	TotalOnlineToday  int            `gorm:"not null;default:0;comment:今日累计在线时长(秒)" json:"total_online_today" msgpack:"total_online_today"`
	IP                string         `gorm:"not null;type:varchar(45);comment:IP地址" json:"ip" msgpack:"ip"`
}

// UserStatusPublic 用户公开状态信息（不包含敏感信息）

// UserWithStatus 用户信息包含状态信息（用于API返回）

// CPU信息
type CpuInfo struct {
	Brand        string `json:"brand" msgpack:"brand"`                 // CPU品牌和型号
	VendorId     string `json:"vendor_id" msgpack:"vendor_id"`         // CPU厂商ID
	Frequency    uint64 `json:"frequency" msgpack:"frequency"`         // CPU频率 (MHz)
	Cores        int    `json:"cores" msgpack:"cores"`                 // 物理核心数
	LogicalCores int    `json:"logical_cores" msgpack:"logical_cores"` // 逻辑核心数
}

// 主板信息
type MotherboardInfo struct {
	Name         string `json:"name,omitempty" msgpack:"name"`                 // 主板名称
	Manufacturer string `json:"manufacturer,omitempty" msgpack:"manufacturer"` // 主板制造商
	Version      string `json:"version,omitempty" msgpack:"version"`           // 主板版本
	Serial       string `json:"serial,omitempty" msgpack:"serial"`             // 主板序列号
}

// 内存信息
type MemoryInfo struct {
	Total     uint64 `json:"total" msgpack:"total"`         // 总内存 (bytes)
	Available uint64 `json:"available" msgpack:"available"` // 可用内存 (bytes)
	Used      uint64 `json:"used" msgpack:"used"`           // 已用内存 (bytes)
}

// 位置信息（客户端发送）
type LocationInfoClient struct {
	IP        string  `json:"ip" msgpack:"ip"`
	Country   string  `json:"country" msgpack:"country"`
	Region    string  `json:"region" msgpack:"region"`
	City      string  `json:"city" msgpack:"city"`
	Latitude  float64 `json:"latitude" msgpack:"latitude"`
	Longitude float64 `json:"longitude" msgpack:"longitude"`
	Timezone  string  `json:"timezone" msgpack:"timezone"`
}

// 网络信息（客户端发送）
type NetworkInfoClient struct {
	NetworkType string `json:"network_type" msgpack:"network_type"` // wifi/4g/5g/ethernet
	Gateway     string `json:"gateway" msgpack:"gateway"`           // 网关IP地址
	ISP         string `json:"isp" msgpack:"isp"`                   // 运营商
}

// 设备信息JSON结构
type DeviceInfo struct {
	Platform     string             `json:"platform" msgpack:"platform"`            // web/ios/android/desktop
	DeviceType   string             `json:"device_type" msgpack:"device_type"`      // phone/tablet/desktop
	DeviceModel  string             `json:"device_model" msgpack:"device_model"`    // iPhone 14, MacBook Pro
	OSVersion    string             `json:"os_version" msgpack:"os_version"`        // iOS 16.0, Windows 11
	AppVersion   string             `json:"app_version" msgpack:"app_version"`      // 1.0.0
	Manufacturer string             `json:"manufacturer" msgpack:"manufacturer"`    // Apple, Samsung
	Browser      string             `json:"browser" msgpack:"browser"`              // Chrome, Safari
	BrowserVer   string             `json:"browser_ver" msgpack:"browser_ver"`      // 120.0.0.0
	ScreenWidth  int                `json:"screen_width" msgpack:"screen_width"`    // 屏幕宽度
	ScreenHeight int                `json:"screen_height" msgpack:"screen_height"`  // 屏幕高度
	Language     string             `json:"language" msgpack:"language"`            // zh-CN, en-US
	Timezone     string             `json:"timezone" msgpack:"timezone"`            // Asia/Shanghai
	NetworkType  string             `json:"network_type" msgpack:"network_type"`    // wifi/4g/5g
	PushToken    string             `json:"push_token" msgpack:"push_token"`        // 推送token
	Cpu          CpuInfo            `json:"cpu" msgpack:"cpu"`                      // CPU详细信息
	Motherboard  MotherboardInfo    `json:"motherboard" msgpack:"motherboard"`      // 主板信息
	Memory       MemoryInfo         `json:"memory" msgpack:"memory"`                // 内存信息
	AppState     AppState           `json:"app_state,omitzero" msgpack:"app_state"` // 应用状态
	Location     LocationInfoClient `json:"location" msgpack:"location"`            // 位置信息
	Network      NetworkInfoClient  `json:"network" msgpack:"network"`              // 网络信息
}

type DeviceInfoJSON DeviceInfo

// 实现Scanner和Valuer接口
func (d *DeviceInfoJSON) Scan(value any) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("无法解析设备信息: %T", value)
	}
	return json.Unmarshal(bytes, d)
}

func (d DeviceInfoJSON) Value() (driver.Value, error) {
	return json.Marshal(d)
}

type UserSession struct {
	BaseModel
	Sid          string `gorm:"not null;type:char(20);uniqueIndex:session_id_unique_index;comment:会话ID" json:"sid" msgpack:"sid"`
	Uid          string `gorm:"not null;type:char(20);index:idx_uid_login;comment:用户ID" json:"uid" msgpack:"uid"`
	DeviceId     string `gorm:"not null;type:char(64);index:idx_device;comment:设备唯一标识" json:"device_id" msgpack:"device_id"`
	DeviceFinger string `gorm:"not null;type:char(128);comment:设备指纹" json:"device_finger" msgpack:"device_finger"`
	Platform     string `gorm:"not null;type:char(20);comment:平台:web/ios/android/desktop" json:"platform" msgpack:"platform"`
	LoginTime    int64  `gorm:"not null;default:0;index:idx_login_time;comment:登录时间" json:"login_time" msgpack:"login_time"`
	LogoutTime   int64  `gorm:"not null;default:0;comment:登出时间" json:"logout_time" msgpack:"logout_time"`
	LastActivity int64  `gorm:"not null;default:0;comment:最后活动时间" json:"last_activity" msgpack:"last_activity"`
	IsActive     bool   `gorm:"not null;default:true;index:idx_active;comment:是否活跃" json:"is_active" msgpack:"is_active"`
	IsExpired    bool   `gorm:"not null;default:false;comment:是否过期" json:"is_expired" msgpack:"is_expired"`
	LoginIP      string `gorm:"not null;type:varchar(45);comment:登录IP" json:"login_ip" msgpack:"login_ip"`
	UserAgent    string `gorm:"not null;default:'';type:text;comment:用户代理" json:"user_agent" msgpack:"user_agent"`
	// 从 SessionData 提取的常用字段
	ClientVersion string `gorm:"type:varchar(50);comment:客户端版本" json:"client_version" msgpack:"client_version"`
	Notification  bool   `gorm:"default:true;comment:是否允许通知" json:"notification" msgpack:"notification"`
	// 完整的会话数据（保留用于详细查询）
	SessionData SessionDataJSON `gorm:"not null;type:jsonb;default:'{}';comment:完整会话数据" json:"session_data" msgpack:"session_data"`
	ExpiresAt   int64           `gorm:"not null;default:0;comment:过期时间" json:"expires_at" msgpack:"expires_at"`
	Reason      string          `gorm:"not null;default:'';type:varchar(100);comment:登出原因" json:"reason" msgpack:"reason"`
}

// 会话数据
type SessionData struct {
	ClientVersion string         `json:"client_version" msgpack:"client_version"`
	ScreenSize    string         `json:"screen_size" msgpack:"screen_size"`
	GeoLocation   map[string]any `json:"geo_location" msgpack:"geo_location"`
	CustomData    map[string]any `json:"custom_data" msgpack:"custom_data"`
	Notification  bool           `json:"notification" msgpack:"notification"`
}

type SessionDataJSON SessionData

func (s *SessionDataJSON) Scan(value any) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("无法解析会话数据: %T", value)
	}
	return json.Unmarshal(bytes, s)
}

func (s SessionDataJSON) Value() (driver.Value, error) {
	return json.Marshal(s)
}

func (model *UserSession) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.Sid = xid.New().String()
	return
}

type UserOnlineHistory struct {
	BaseModel
	Hid          string `gorm:"not null;type:char(20);uniqueIndex:hid_unique_index;comment:历史ID" json:"hid" msgpack:"hid"`
	Sid          string `gorm:"not null;type:char(20);index:idx_session;comment:会话ID" json:"sid" msgpack:"sid"`
	Uid          string `gorm:"not null;type:char(20);index:idx_user;comment:用户ID" json:"uid" msgpack:"uid"`
	EventType    string `gorm:"not null;type:varchar(20);index:idx_event_type;comment:事件类型:login/logout/heartbeat/status_change" json:"event_type" msgpack:"event_type"`
	EventSubtype string `gorm:"type:varchar(50);comment:事件子类型:normal/timeout/kick/error" json:"event_subtype" msgpack:"event_subtype"`
	StatusBefore string `gorm:"type:varchar(20);comment:变更前状态" json:"status_before" msgpack:"status_before"`
	StatusAfter  string `gorm:"not null;type:varchar(20);comment:变更后状态" json:"status_after" msgpack:"status_after"`
	OnlineSec    int    `gorm:"default:0;comment:在线时长(秒)" json:"online_sec" msgpack:"online_sec"`
	Reason       string `gorm:"not null;default:'';type:varchar(100);comment:事件原因" json:"reason" msgpack:"reason"`
	EventTime    int64  `gorm:"not null;default:0;index:idx_event_time;comment:事件时间" json:"event_time" msgpack:"event_time"`
	// 从 DeviceInfo 提取的常用字段
	Platform    string `gorm:"type:varchar(20);index:idx_platform;comment:平台:web/ios/android/desktop" json:"platform" msgpack:"platform"`
	DeviceType  string `gorm:"type:varchar(20);comment:设备类型:phone/tablet/desktop" json:"device_type" msgpack:"device_type"`
	DeviceModel string `gorm:"type:varchar(100);comment:设备型号" json:"device_model" msgpack:"device_model"`
	OSVersion   string `gorm:"type:varchar(50);comment:操作系统版本" json:"os_version" msgpack:"os_version"`
	// 从 LocationInfo 提取的常用字段
	IP        string  `gorm:"type:varchar(45);index:idx_ip;comment:IP地址" json:"ip" msgpack:"ip"`
	Country   string  `gorm:"type:varchar(50);index:idx_country;comment:国家" json:"country" msgpack:"country"`
	CountryEn string  `gorm:"type:varchar(50);comment:国家(英文)" json:"country_en" msgpack:"country_en"`
	Region    string  `gorm:"type:varchar(100);index:idx_region;comment:地区/省份" json:"region" msgpack:"region"`
	RegionEn  string  `gorm:"type:varchar(100);comment:地区/省份(英文)" json:"region_en" msgpack:"region_en"`
	City      string  `gorm:"type:varchar(100);index:idx_city;comment:城市" json:"city" msgpack:"city"`
	CityEn    string  `gorm:"type:varchar(100);comment:城市(英文)" json:"city_en" msgpack:"city_en"`
	Latitude  float64 `gorm:"comment:纬度" json:"latitude" msgpack:"latitude"`
	Longitude float64 `gorm:"comment:经度" json:"longitude" msgpack:"longitude"`
	Timezone  string  `gorm:"type:varchar(50);comment:时区" json:"timezone" msgpack:"timezone"`
	// 从 NetworkInfo 提取的常用字段
	NetworkType    string `gorm:"type:varchar(20);index:idx_network_type;comment:网络类型:wifi/4g/5g/ethernet" json:"network_type" msgpack:"network_type"`
	ISP            string `gorm:"type:varchar(100);comment:运营商" json:"isp" msgpack:"isp"`
	NetworkSignal  int    `gorm:"default:0;comment:信号强度" json:"network_signal" msgpack:"network_signal"`
	NetworkLatency int    `gorm:"default:0;comment:网络延迟(ms)" json:"network_latency" msgpack:"network_latency"`
	// 从 AppState 提取的常用字段
	IsForeground bool    `gorm:"default:false;comment:是否前台运行" json:"is_foreground" msgpack:"is_foreground"`
	BatteryLevel float64 `gorm:"default:0;comment:电池电量(0-100)" json:"battery_level" msgpack:"battery_level"`
	IsCharging   bool    `gorm:"default:false;comment:是否充电" json:"is_charging" msgpack:"is_charging"`
	// 完整的 JSON 数据（保留用于详细查询）
	DeviceInfo   DeviceInfoJSON   `gorm:"type:jsonb;comment:完整设备信息" json:"device_info" msgpack:"device_info"`
	NetworkInfo  NetworkInfoJSON  `gorm:"type:jsonb;comment:完整网络信息" json:"network_info" msgpack:"network_info"`
	LocationInfo LocationInfoJSON `gorm:"type:jsonb;comment:完整位置信息" json:"location_info" msgpack:"location_info"`
	AppState     AppStateJSON     `gorm:"type:jsonb;comment:完整应用状态" json:"app_state" msgpack:"app_state"`
}

// 网络信息
type NetworkInfo struct {
	Type     string  `json:"type"`     // wifi/4g/5g
	ISP      string  `json:"isp"`      // 运营商
	Signal   int     `json:"signal"`   // 信号强度
	Upload   float64 `json:"upload"`   // 上传速度
	Download float64 `json:"download"` // 下载速度
	Latency  int     `json:"latency"`  // 延迟
}

type NetworkInfoJSON NetworkInfo

func (n *NetworkInfoJSON) Scan(value any) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("无法解析网络信息: %T", value)
	}
	return json.Unmarshal(bytes, n)
}

func (n NetworkInfoJSON) Value() (driver.Value, error) {
	return json.Marshal(n)
}

// 位置信息
type LocationInfo struct {
	IP        string  `json:"ip"`
	Country   string  `json:"country"`    // 国家（中文）
	CountryEn string  `json:"country_en"` // 国家（英文）
	Region    string  `json:"region"`     // 地区/省份（中文）
	RegionEn  string  `json:"region_en"`  // 地区/省份（英文）
	City      string  `json:"city"`       // 城市（中文）
	CityEn    string  `json:"city_en"`    // 城市（英文）
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
}

type LocationInfoJSON LocationInfo

func (l *LocationInfoJSON) Scan(value any) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("无法解析位置信息: %T", value)
	}
	return json.Unmarshal(bytes, l)
}

func (l LocationInfoJSON) Value() (driver.Value, error) {
	return json.Marshal(l)
}

// 应用状态
type AppState struct {
	IsForeground bool    `json:"is_foreground"`
	BatteryLevel float64 `json:"battery_level"`
	IsCharging   bool    `json:"is_charging"`
	MemoryUsage  float64 `json:"memory_usage"`
	CPUUsage     float64 `json:"cpu_usage"`
}

type AppStateJSON AppState

func (a *AppStateJSON) Scan(value any) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("无法解析应用状态: %T", value)
	}
	return json.Unmarshal(bytes, a)
}

func (a AppStateJSON) Value() (driver.Value, error) {
	return json.Marshal(a)
}

// 设备会话映射表
type UserDeviceSession struct {
	BaseModel
	Uid           string `gorm:"not null;type:char(20);uniqueIndex:idx_uid_device,priority:1;comment:用户ID" json:"uid" msgpack:"uid"`
	DeviceId      string `gorm:"not null;type:char(64);uniqueIndex:idx_uid_device,priority:2;comment:设备ID" json:"device_id" msgpack:"device_id"`
	DeviceFinger  string `gorm:"not null;type:char(128);comment:设备指纹" json:"device_finger" msgpack:"device_finger"`
	CurrentSid    string `gorm:"not null;type:char(64);comment:当前会话ID" json:"current_sid" msgpack:"current_sid"`
	Platform      string `gorm:"not null;type:char(20);comment:平台" json:"platform" msgpack:"platform"`
	DeviceName    string `gorm:"not null;type:varchar(100);comment:设备名称" json:"device_name" msgpack:"device_name"`
	LastLogin     int64  `gorm:"not null;default:0;index:idx_last_login;comment:最后登录时间" json:"last_login" msgpack:"last_login"`
	LastLogout    int64  `gorm:"not null;default:0;comment:最后登出时间" json:"last_logout" msgpack:"last_logout"`
	TotalSessions int    `gorm:"default:0;comment:总会话数" json:"total_sessions" msgpack:"total_sessions"`
	IsTrusted     bool   `gorm:"default:false;comment:是否受信任" json:"is_trusted" msgpack:"is_trusted"`
	IsBlocked     bool   `gorm:"default:false;comment:是否被阻止" json:"is_blocked" msgpack:"is_blocked"`
	// 从 MetaInfo 提取的常用字段
	FirstSeen           int64   `gorm:"default:0;comment:首次出现时间" json:"first_seen" msgpack:"first_seen"`
	LoginCount          int     `gorm:"default:0;comment:登录次数" json:"login_count" msgpack:"login_count"`
	TotalOnline         int     `gorm:"default:0;comment:总在线时长(秒)" json:"total_online" msgpack:"total_online"`
	AvgDuration         float64 `gorm:"default:0;comment:平均在线时长(秒)" json:"avg_duration" msgpack:"avg_duration"`
	LastLocationIP      string  `gorm:"type:varchar(45);comment:最后位置IP" json:"last_location_ip" msgpack:"last_location_ip"`
	LastLocationCountry string  `gorm:"type:varchar(50);comment:最后位置国家" json:"last_location_country" msgpack:"last_location_country"`
	LastLocationCity    string  `gorm:"type:varchar(100);comment:最后位置城市" json:"last_location_city" msgpack:"last_location_city"`
	// 完整的元信息（保留用于详细查询）
	MetaInfo MetaInfoJSON `gorm:"type:jsonb;comment:完整元信息" json:"meta_info" msgpack:"meta_info"`
}

// 元信息
type MetaInfo struct {
	FirstSeen    int64            `json:"first_seen,omitempty" msgpack:"first_seen"`
	LoginCount   int              `json:"login_count" msgpack:"login_count"`
	TotalOnline  int              `json:"total_online" msgpack:"total_online"` // 总在线时长(秒)
	AvgDuration  float64          `json:"avg_duration" msgpack:"avg_duration"` // 平均在线时长
	LastLocation LocationInfoJSON `json:"last_location" msgpack:"last_location"`
	CustomFields map[string]any   `json:"custom_fields,omitempty" msgpack:"custom_fields"`
}

type MetaInfoJSON MetaInfo

func (m *MetaInfoJSON) Scan(value any) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("无法解析元信息: %T", value)
	}
	return json.Unmarshal(bytes, m)
}

func (m MetaInfoJSON) Value() (driver.Value, error) {
	return json.Marshal(m)
}

// 用户在线统计表
type UserOnlineStat struct {
	BaseModel
	StatId       string  `gorm:"not null;type:char(20);uniqueIndex:stat_id_unique_index;comment:统计ID" json:"stat_id"`
	Uid          string  `gorm:"not null;type:char(20);index:idx_uid_date;comment:用户ID" json:"uid"`
	StatDate     int64   `gorm:"not null;default:0;index:idx_stat_date;comment:统计日期" json:"stat_date"`
	StatType     string  `gorm:"not null;type:varchar(10);comment:统计类型:day/week/month" json:"stat_type"`
	LoginCount   int     `gorm:"not null;default:0;comment:登录次数" json:"login_count"`
	TotalSeconds int     `gorm:"not null;default:0;comment:总在线时长(秒)" json:"total_seconds"`
	AvgDuration  float64 `gorm:"not null;default:0;comment:平均在线时长" json:"avg_duration"`
	MaxDuration  int     `gorm:"not null;default:0;comment:最长在线时长" json:"max_duration"`
	FirstLogin   int64   `gorm:"not null;default:0;comment:首次登录时间" json:"first_login"`
	LastLogin    int64   `gorm:"not null;default:0;comment:最后登录时间" json:"last_login"`
	PeakHour     int     `gorm:"not null;default:0;comment:高峰时段(小时)" json:"peak_hour"`
}

func (model *UserOnlineStat) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.StatId = xid.New().String()
	return
}

// 用户好友请求表
type UserFriendRequest struct {
	BaseModel
	FrId string `gorm:"not null;type:char(20);uniqueIndex:fr_idx_unique;comment:好友请求ID" json:"fr_id"`
	// 接收人uid
	ReceiverUid string `gorm:"not null;type:char(20);uniqueIndex:sender_uid_receiver_uid_index,priority:1;index:receiver_uid_index;comment:接收人uid" json:"receiver_uid"`
	// 申请人uid
	SenderUid string `gorm:"not null;type:char(20);uniqueIndex:sender_uid_receiver_uid_index,priority:2;comment:申请人uid" json:"sender_uid"`
	// 好友分组ID
	Gid string `gorm:"not null;type:char(20);comment:好友分组ID" json:"gid"`
	// 好友备注
	Remark string `gorm:"comment:好友备注" json:"remark"`
	// 验证信息
	Message string `gorm:"comment:验证信息" json:"message"`
	State   int8   `gorm:"not null;default:0;comment:好友请求状态(0-等待验证,1-已拒绝,2-已同意,3-已过期)" json:"state"`
	// 过期时间（毫秒时间戳，0表示永不过期）
	ExpiresAt int64 `gorm:"not null;default:0;comment:过期时间" json:"expires_at"`
	// 处理时间（毫秒时间戳，0表示未处理）
	ProcessedAt int64 `gorm:"not null;default:0;comment:处理时间" json:"processed_at"`
}

func (model *UserFriendRequest) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.FrId = xid.New().String()
	// 默认7天过期
	if model.ExpiresAt == 0 {
		model.ExpiresAt = time.Now().AddDate(0, 0, 7).UnixMilli()
	}
	return
}

// RoomJoinRequest 房间加入申请（类似好友请求）
type RoomJoinRequest struct {
	BaseModel
	RjrId         string `gorm:"not null;type:char(20);uniqueIndex:rjr_id_unique;comment:加入申请ID" json:"rjr_id"`
	Rid           string `gorm:"not null;type:char(20);uniqueIndex:rid_applicant_uid_index,priority:1;index:idx_rjr_rid;comment:房间rid" json:"rid"`
	ApplicantUid  string `gorm:"not null;type:char(20);uniqueIndex:rid_applicant_uid_index,priority:2;index:idx_rjr_applicant_uid;comment:申请人uid" json:"applicant_uid"`
	Message       string `gorm:"not null;default:'';type:varchar(255);comment:申请留言" json:"message"`
	Answer        string `gorm:"not null;default:'';type:varchar(128);comment:验证问题回答" json:"answer"`
	State         int8   `gorm:"not null;default:0;comment:状态(0-等待验证,1-已拒绝,2-已同意,3-已过期)" json:"state"`
	HandlerUid    string `gorm:"not null;default:'';type:char(20);comment:处理人uid" json:"handler_uid"`
	ExpiresAt     int64  `gorm:"not null;default:0;comment:过期时间(毫秒)" json:"expires_at"`
	ProcessedAt   int64  `gorm:"not null;default:0;comment:处理时间(毫秒)" json:"processed_at"`
}

func (model *RoomJoinRequest) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	if model.RjrId == "" {
		model.RjrId = xid.New().String()
	}
	if model.ExpiresAt == 0 {
		model.ExpiresAt = time.Now().AddDate(0, 0, 7).UnixMilli()
	}
	return
}

// NotificationType 消息通知类型
type NotificationType int8
type NotificationState int8

const (
	// 10-好友请求通知
	NotificationTypeFriendNotification NotificationType = 10
	// 11-好友发起请求通知
	NotificationTypeFriendAddRequest NotificationType = 11

	// 20-房间通知
	NotificationTypeRoomNotification NotificationType = 20
	// 21-房间邀请通知
	NotificationTypeRoomInvite NotificationType = 21
	// 22-收到房间加入申请（管理员/房主）
	NotificationTypeRoomJoinRequest NotificationType = 22
	// 23-发送房间加入申请（申请人）
	NotificationTypeRoomJoinRequestSend NotificationType = 23

	NotificationStatePending        NotificationState = 0
	NotificationFriendStateAccepted NotificationState = 11
	NotificationFriendStateRejected NotificationState = 12
	NotificationRoomStateAccepted   NotificationState = 21
	NotificationRoomStateRejected   NotificationState = 22
)

// 用户消息通知表
type UserMessageNotification struct {
	BaseModel
	Nid       string            `gorm:"not null;type:char(20);uniqueIndex:nid_unique_index;comment:消息通知ID" json:"nid"`
	// idx_umnotif_uid_unread：COUNT 未读通知 WHERE uid AND read_at=0 AND delete_time=0
	Uid       string            `gorm:"not null;type:char(20);index:uid_type_index;index:idx_umnotif_uid_unread,where:read_at = 0 AND delete_time = 0;comment:用户ID" json:"uid"`
	Type      NotificationType  `gorm:"not null;default:10;comment:消息类型(10-好友通知,20-房间通知)" json:"type"`
	RelatedId string            `gorm:"not null;type:char(20);index:related_id_index;comment:关联的实体ID" json:"related_id"`
	Content   string            `gorm:"not null;type:json;comment:消息内容" json:"content"`
	State     NotificationState `gorm:"not null;default:0;comment:消息状态(0-未处理,11-已同意好友请求,12-已拒绝好友请求,21-已同意房间邀请,22-已拒绝房间邀请)" json:"state"`
	Status    int8              `gorm:"not null;default:1;comment:消息状态(0-隐藏,1-显示)" json:"status"`
	// 读取时间（毫秒时间戳，0表示未读）
	ReadAt int64 `gorm:"not null;default:0;comment:读取时间" json:"read_at"`
}

func (model *UserMessageNotification) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	model.Nid = xid.New().String()
	return
}

// RoomAdminOperationType 房间管理员操作类型
type RoomAdminOperationType string

const (
	RoomAdminOpRoomNameUpdate   RoomAdminOperationType = "room_name_update"
	RoomAdminOpRoomAvatarUpdate RoomAdminOperationType = "room_avatar_update"
	// RoomAdminOpRoomPasswordUpdate 房间密码修改（仅记录是否有密码，不记录明文）
	RoomAdminOpRoomPasswordUpdate RoomAdminOperationType = "room_password_update"
	RoomAdminOpMessageWithdraw    RoomAdminOperationType = "message_withdraw"
	RoomAdminOpMemberRoleUpdate   RoomAdminOperationType = "member_role_update"
	RoomAdminOpRoomMute           RoomAdminOperationType = "room_mute"
	RoomAdminOpRoomUnmute         RoomAdminOperationType = "room_unmute"
	RoomAdminOpRoomMuteConfig     RoomAdminOperationType = "room_mute_config"
	// 开启全体禁言
	RoomAdminOpRoomMuteAllOn RoomAdminOperationType = "room_mute_all_on"
	// 关闭全体禁言
	RoomAdminOpRoomMuteAllOff RoomAdminOperationType = "room_mute_all_off"
	// 开启策略禁言
	RoomAdminOpRoomMuteStrategyEnable RoomAdminOperationType = "room_mute_strategy_enable"
	// 关闭策略禁言
	RoomAdminOpRoomMuteStrategyDisable RoomAdminOperationType = "room_mute_strategy_disable"
	// 修改策略禁言
	RoomAdminOpRoomMuteStrategyUpdate RoomAdminOperationType = "room_mute_strategy_update"
	RoomAdminOpRoomAnnouncementCreate RoomAdminOperationType = "room_announcement_create"
	RoomAdminOpRoomAnnouncementUpdate RoomAdminOperationType = "room_announcement_update"
	RoomAdminOpRoomAnnouncementDelete RoomAdminOperationType = "room_announcement_delete"
	RoomAdminOpMessagePin             RoomAdminOperationType = "message_pin"
	RoomAdminOpMessageUnpin           RoomAdminOperationType = "message_unpin"
	// 创建允许非好友私聊的私聊房间（由系统/发起人创建，记录审计）
	RoomAdminOpPrivateChatCreate RoomAdminOperationType = "private_chat_create"
	RoomAdminOpRoomBlockUser     RoomAdminOperationType = "room_block_user"   // 房间内屏蔽某用户（不接收该用户在该房间的消息）
	RoomAdminOpRoomUnblockUser   RoomAdminOperationType = "room_unblock_user" // 房间内取消屏蔽某用户
	RoomAdminOpOwnerTransfer     RoomAdminOperationType = "owner_transfer"    // 转让群主
	RoomAdminOpRoomDissolve      RoomAdminOperationType = "room_dissolve"     // 解散群聊
	RoomAdminOpMemberKick        RoomAdminOperationType = "member_kick"       // 移出群成员
	RoomAdminOpJoinConfigUpdate  RoomAdminOperationType = "join_config_update" // 更新加入审批设置
	RoomAdminOpJoinRequestAccept RoomAdminOperationType = "join_request_accept" // 同意加入申请
	RoomAdminOpJoinRequestReject RoomAdminOperationType = "join_request_reject" // 拒绝加入申请
)

// RoomAdminOperation 房间管理员操作记录表（通用审计：改群名、改群头像、撤回消息、改成员权限、禁言等）
// Sid 关联 UserSession.Sid，标识发起操作的会话（与 uid 同存于 token，不能为空）
type RoomAdminOperation struct {
	BaseModel
	Rid         string                 `gorm:"not null;type:char(20);index:idx_rid;comment:房间ID" json:"rid"`
	OpType      RoomAdminOperationType `gorm:"not null;type:varchar(32);index:idx_op_type;comment:操作类型" json:"op_type"`
	OperatorUid string                 `gorm:"not null;type:char(20);index:idx_operator;comment:操作人UID" json:"operator_uid"`
	Sid         string                 `gorm:"not null;type:char(20);index:idx_sid;comment:操作人会话ID(关联UserSession.Sid)" json:"sid"`
	RelatedId   string                 `gorm:"not null;default:'';type:varchar(64);index:idx_related;comment:关联ID(如mid、target_uid等)" json:"related_id"`
	BeforeData  string                 `gorm:"not null;default:'{}';type:jsonb;comment:操作前数据快照" json:"before_data"`
	AfterData   string                 `gorm:"not null;default:'{}';type:jsonb;comment:操作后数据快照" json:"after_data"`
}

// UserOperationType 用户个人信息操作类型
type UserOperationType string

const (
	UserOpAvatar                        UserOperationType = "avatar"                             // 修改头像
	UserOpNickname                      UserOperationType = "nickname"                           // 修改昵称
	UserOpPassword                      UserOperationType = "password"                           // 修改密码（仅记录是否修改，不落明文）
	UserOpSignature                     UserOperationType = "signature"                          // 修改个性签名
	UserOpIntroduction                  UserOperationType = "introduction"                       // 修改个人简介
	UserOpPresenceStatus                UserOperationType = "presence_status"                    // 修改在线展示状态
	UserOpEmail                         UserOperationType = "email"                              // 修改邮箱
	UserOpAllowPrivateChatFromNonFriend UserOperationType = "allow_private_chat_from_non_friend" // 是否允许非好友发起私聊
	UserOpRoomBlock                     UserOperationType = "room_block"                         // 屏蔽群私聊会话（不接收该会话消息）
	UserOpRoomUnblock                   UserOperationType = "room_unblock"                       // 取消屏蔽群私聊会话
	UserOpRoomCreate                    UserOperationType = "room_create"                        // 创建房间
	UserOpRoomJoin                      UserOperationType = "room_join"                          // 加入房间
	UserOpRoomLeave                     UserOperationType = "room_leave"                         // 退出房间
	UserOpRoomOwnerTransfer             UserOperationType = "room_owner_transfer"              // 转让群主
	UserOpRoomDissolve                  UserOperationType = "room_dissolve"                    // 解散群聊
	UserOpEmojiFavoriteAdd              UserOperationType = "emoji_favorite_add"                 // 收藏表情/图片
	UserOpEmojiFavoriteRemove           UserOperationType = "emoji_favorite_remove"              // 取消收藏表情/图片
	UserOpFriendRemark                  UserOperationType = "friend_remark"                      // 修改好友备注
	UserOpFriendDelete                  UserOperationType = "friend_delete"                      // 删除好友
	UserOpFriendRequestAccept           UserOperationType = "friend_request_accept"              // 同意好友请求
	UserOpFriendRequestReject           UserOperationType = "friend_request_reject"              // 拒绝好友请求
	UserOpRoomJoinApply                 UserOperationType = "room_join_apply"                    // 提交房间加入申请
	UserOpRoomJoinRequestAccept         UserOperationType = "room_join_request_accept"           // 同意房间加入申请
	UserOpRoomJoinRequestReject         UserOperationType = "room_join_request_reject"           // 拒绝房间加入申请
	UserOpSessionTop                    UserOperationType = "session_top"                        // 会话置顶
	UserOpThemeUpdate                   UserOperationType = "theme_update"                       // 更新主题
	UserOpRoomUserNickname              UserOperationType = "room_user_nickname"                 // 修改本群昵称
	UserOpRoomUserRemark                UserOperationType = "room_user_remark"                   // 修改房间备注
)

const (
	UserEmojiFavoriteKindEmoji = "emoji"
	UserEmojiFavoriteKindImage = "image"
)

// UserEmojiRecent 用户最近使用的表情（表情选择器）
type UserEmojiRecent struct {
	BaseModel
	Uid     string `gorm:"not null;type:char(20);uniqueIndex:uid_emoji_unique,priority:1;index:idx_uid_used;comment:用户UID" json:"uid"`
	EmojiId string `gorm:"not null;type:varchar(64);uniqueIndex:uid_emoji_unique,priority:2;comment:表情ID" json:"emoji_id"`
	Label   string `gorm:"not null;default:'';type:varchar(128);comment:表情标签" json:"label"`
	UsedAt  int64  `gorm:"not null;index:idx_uid_used;comment:最近使用时间毫秒" json:"used_at"`
}

// UserEmojiFavorite 用户收藏的表情或图片
type UserEmojiFavorite struct {
	BaseModel
	Uid     string `gorm:"not null;type:char(20);uniqueIndex:uid_kind_ref_unique,priority:1;index:idx_uid;comment:用户UID" json:"uid"`
	Kind    string `gorm:"not null;type:varchar(16);uniqueIndex:uid_kind_ref_unique,priority:2;comment:类型 emoji/image" json:"kind"`
	RefKey  string `gorm:"not null;type:varchar(64);uniqueIndex:uid_kind_ref_unique,priority:3;comment:引用键 emoji_id或file_hash" json:"ref_key"`
	Label   string `gorm:"not null;default:'';type:varchar(255);comment:展示标签" json:"label"`
	Payload string `gorm:"not null;default:'{}';type:jsonb;comment:扩展数据" json:"payload"`
}

// UserOperation 用户操作记录表（修改头像、昵称、密码、简介等个人信息时记录）
// Sid 关联 UserSession.Sid，标识发起操作的会话（与 uid 同存于 token，不能为空）
type UserOperation struct {
	BaseModel
	Uid        string            `gorm:"not null;type:char(20);index:idx_uid;comment:被操作用户UID" json:"uid"`
	OpType     UserOperationType `gorm:"not null;type:varchar(32);index:idx_op_type;comment:操作类型" json:"op_type"`
	Sid        string            `gorm:"not null;type:char(20);index:idx_sid;comment:操作人会话ID(关联UserSession.Sid)" json:"sid"`
	RelatedId  string            `gorm:"not null;default:'';type:varchar(64);comment:关联ID(如字段名等)" json:"related_id"`
	BeforeData string            `gorm:"not null;default:'{}';type:jsonb;comment:操作前数据快照" json:"before_data"`
	AfterData  string            `gorm:"not null;default:'{}';type:jsonb;comment:操作后数据快照" json:"after_data"`
}

// RoomMuteConfig 房间禁言配置表
type RoomMuteConfig struct {
	BaseModel
	ConfigId      string `gorm:"not null;type:char(20);uniqueIndex:config_id_unique;comment:配置ID" json:"config_id"`
	Rid           string `gorm:"not null;type:char(20);uniqueIndex:rid_unique;comment:房间ID" json:"rid"`
	IsMuteAll     bool   `gorm:"not null;default:false;comment:是否全体禁言" json:"is_mute_all"`
	MuteAllBy     string `gorm:"not null;default:'';type:char(20);comment:全体禁言操作人" json:"mute_all_by"`
	MuteAllReason string `gorm:"not null;default:'';type:varchar(500);comment:全体禁言原因" json:"mute_all_reason"`
	RuleType      int8   `gorm:"not null;default:0;comment:规则类型位掩码: 1=时间段 2=频率 4=智能, 0=永久" json:"rule_type"`
	RuleConfig    string `gorm:"not null;default:'{}';type:jsonb;comment:规则配置" json:"rule_config"`
	AllowRoles    string `gorm:"not null;default:'[]';type:jsonb;comment:允许发言角色" json:"allow_roles"`
	ExceptUsers   string `gorm:"not null;default:'[]';type:jsonb;comment:例外用户" json:"except_users"`
	EffectiveAt   int64  `gorm:"not null;default:0;comment:生效时间" json:"effective_at"`
	ExpiresAt     int64  `gorm:"not null;default:0;comment:过期时间" json:"expires_at"`
	IsActive      bool   `gorm:"not null;default:false;comment:是否生效" json:"is_active"`
	Version       int    `gorm:"not null;default:1;comment:配置版本" json:"version"`
}

func (model *RoomMuteConfig) BeforeCreate(tx *gorm.DB) (err error) {
	model.BaseModel.setTimestamps()
	if model.ConfigId == "" {
		model.ConfigId = xid.New().String()
	}
	return
}
