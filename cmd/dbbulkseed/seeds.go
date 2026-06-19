package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seedOpts 单次灌数配置；RunPrefix 每进程随机，保证多次执行不与上次唯一键冲突。
type seedOpts struct {
	Rows        int64
	BatchLog    int64
	NowMs       int64
	RunPrefix   string
	JSONEmpty   []byte
	JSONArray   []byte
	InviteeJSON []byte
}

// Col 生成 char(20)：8 位随机前缀 + 2 字母槽位 + 10 位行号（最多 9_999_999_999 行）。
func (o seedOpts) Col(slot string, i int64) string {
	if len(slot) != 2 {
		panic("Col slot 必须为 2 个字符: " + slot)
	}
	return o.RunPrefix + slot + fmt.Sprintf("%010d", i)
}

// Hex64 生成 64 位十六进制字符串（用于 hash、device_id 等），依赖 RunPrefix 与 tag 区分用途。
func (o seedOpts) Hex64(tag string, i int64) string {
	sum := sha256.Sum256([]byte(o.RunPrefix + "|" + tag + "|" + strconv.FormatInt(i, 10)))
	return hex.EncodeToString(sum[:])
}

// JTI 生成 36 字符 UUID 形字符串（char(36)）。
func (o seedOpts) JTI(i int64) string {
	sum := sha256.Sum256([]byte(o.RunPrefix + "|jti|" + strconv.FormatInt(i, 10)))
	b := sum[:16]
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func newRunPrefix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	out := make([]byte, 8)
	for i := range out {
		out[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(out)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func allSeedJobs() []seedJob {
	return []seedJob{
		{"user", seedUser},
		{"user_remember_token", seedUserRememberToken},
		{"user_room_session", seedUserRoomSession},
		{"user_refresh_token", seedUserRefreshToken},
		{"user_friend_group", seedUserFriendGroup},
		{"user_friend", seedUserFriend},
		{"user_upload_file", seedUserUploadFile},
		{"upload_file", seedUploadFile},
		{"upload_file_chunk", seedUploadFileChunk},
		{"room", seedRoom},
		{"room_invite", seedRoomInvite},
		{"room_invite_join", seedRoomInviteJoin},
		{"room_category", seedRoomCategory},
		{"room_tag", seedRoomTag},
		{"room_tag_relation", seedRoomTagRelation},
		{"room_user", seedRoomUser},
		{"room_message", seedRoomMessage},
		{"room_message_content", seedRoomMessageContent},
		{"room_message_ack", seedRoomMessageAck},
		{"room_message_withdraw_ack", seedRoomMessageWithdrawAck},
		{"room_message_mention", seedRoomMessageMention},
		{"user_online_stat", seedUserOnlineStat},
		{"user_device_session", seedUserDeviceSession},
		{"user_session", seedUserSession},
		{"user_friend_request", seedUserFriendRequest},
		{"user_message_notification", seedUserMessageNotification},
		{"user_online_history", seedUserOnlineHistory},
		{"user_current_status", seedUserCurrentStatus},
		{"room_announcement", seedRoomAnnouncement},
		{"room_mute_config", seedRoomMuteConfig},
		{"room_admin_operation", seedRoomAdminOperation},
		{"user_operation", seedUserOperation},
		{"user_room_block", seedUserRoomBlock},
		{"room_user_block_user", seedRoomUserBlockUser},
		{"media_call_record", seedMediaCallRecord},
	}
}

type rowGen struct {
	n       int64
	max     int64
	opts    seedOpts
	label   string
	logStep int64
	gen     func(i int64, o seedOpts) ([]any, error)
}

func (r *rowGen) Next() bool { return r.n < r.max }

func (r *rowGen) Values() ([]any, error) {
	r.n++
	if r.logStep > 0 && r.n%r.logStep == 0 {
		log.Printf("  ... %s %d/%d", r.label, r.n, r.max)
	}
	return r.gen(r.n, r.opts)
}

func (r *rowGen) Err() error { return nil }

func copyFrom(
	ctx context.Context,
	pool *pgxpool.Pool,
	table pgx.Identifier,
	cols []string,
	opts seedOpts,
	label string,
	gen func(i int64, o seedOpts) ([]any, error),
) (int64, error) {
	src := &rowGen{
		max:     opts.Rows,
		opts:    opts,
		label:   label,
		logStep: opts.BatchLog,
		gen:     gen,
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	return conn.CopyFrom(ctx, table, cols, src)
}

const bcryptPlaceholder = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

func seedUser(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user"}, []string{
		"create_time", "update_time", "delete_time",
		"uid", "username", "nickname", "signature", "introduction", "email", "password",
		"avatar_uf_id", "allow_private_chat_from_non_friend", "db_key_seed", "db_key_iterations",
		"register_ip", "country", "city", "county",
	}, o, "user", func(i int64, o seedOpts) ([]any, error) {
		uid := o.Col("uu", i)
		return []any{
			o.NowMs, o.NowMs, int64(0),
			uid,
			fmt.Sprintf("n%s%010d", o.RunPrefix, i),
			"bulk",
			"", "",
			fmt.Sprintf("e%s%010d@b.local", o.RunPrefix, i),
			bcryptPlaceholder,
			"",
			true,
			o.Col("uk", i),
			int32(120000),
			"",
			"",
			"",
			"",
		}, nil
	})
}

func seedUserRememberToken(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_remember_token"}, []string{
		"create_time", "update_time", "delete_time",
		"uid", "token", "device_id", "expires_at",
	}, o, "user_remember_token", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("uu", i),
			o.Hex64("urtok", i),
			o.Hex64("urdev", i),
			o.NowMs + 86400000,
		}, nil
	})
}

func seedUserRoomSession(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_room_session"}, []string{
		"create_time", "update_time", "delete_time",
		"uid", "rid", "rsid", "disturb_type", "enable_push", "show_unread",
		"work_start_time", "work_end_time", "disturb_config", "last_seq_id", "last_mention_seq_id", "is_top", "state",
		"last_room_message_create_time",
	}, o, "user_room_session", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("uu", i),
			o.Col("rm", i),
			o.Col("rs", i),
			int16(0),
			true,
			true,
			"",
			"",
			"{}",
			int64(0),
			int64(0),
			false,
			int16(1),
			int64(0),
		}, nil
	})
}

func seedUserRefreshToken(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_refresh_token"}, []string{
		"create_time", "update_time", "delete_time",
		"uid", "jti", "expires_at", "revoked", "user_agent", "ip",
	}, o, "user_refresh_token", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("uu", i),
			o.JTI(i),
			o.NowMs + 86400000,
			false,
			"bulk",
			"127.0.0.1",
		}, nil
	})
}

func seedUserFriendGroup(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_friend_group"}, []string{
		"create_time", "update_time", "delete_time",
		"uid", "gid", "name", "is_default", "sort", "description",
	}, o, "user_friend_group", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("uu", i),
			o.Col("fg", i),
			"bulk",
			false,
			0,
			"",
		}, nil
	})
}

func seedUserFriend(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_friend"}, []string{
		"create_time", "update_time", "delete_time",
		"uid", "friend_uid", "gid", "remark", "friend_delete_time",
	}, o, "user_friend", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("uu", i),
			o.Col("fd", i),
			o.Col("fg", i),
			"",
			int64(0),
		}, nil
	})
}

func seedUserUploadFile(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_upload_file"}, []string{
		"create_time", "update_time", "delete_time",
		"filename", "uf_id", "uid", "fid", "scene", "client_type", "ip",
	}, o, "user_upload_file", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			"f.bin",
			o.Col("uf", i),
			o.Col("uu", i),
			o.Col("fi", i),
			"",
			int16(0),
			"127.0.0.1",
		}, nil
	})
}

func seedUploadFile(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"upload_file"}, []string{
		"create_time", "update_time", "delete_time",
		"fid", "hash", "ext", "type_main", "type_sub", "path", "total_size", "chunk_size", "chunk_count",
		"width", "height", "thumb", "duration", "state",
	}, o, "upload_file", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("fi", i),
			o.Hex64("fihash", i),
			"",
			"", "",
			"",
			uint64(1),
			uint32(1024),
			uint32(1),
			uint32(0),
			uint32(0),
			"",
			uint64(0),
			uint8(2),
		}, nil
	})
}

func seedUploadFileChunk(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"upload_file_chunk"}, []string{
		"create_time", "update_time", "delete_time",
		"hash", "size", "file_hash", "fid", "chunk_idx",
	}, o, "upload_file_chunk", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Hex64("uchnk", i),
			int64(1024),
			o.Hex64("ufihash", i),
			o.Col("fi", i),
			uint32(0),
		}, nil
	})
}

func seedRoom(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room"}, []string{
		"create_time", "update_time", "delete_time",
		"rid", "name", "create_uid", "description", "state", "sort", "password",
		"avatar_uf_id", "type", "member_count", "allow_non_friend_chat", "from_room_rid", "category_id",
	}, o, "room", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("rm", i),
			"bulk-room",
			o.Col("uu", 1),
			"",
			int16(1),
			0,
			"",
			"",
			int16(1),
			0,
			true,
			"",
			"",
		}, nil
	})
}

func seedRoomInvite(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_invite"}, []string{
		"create_time", "update_time", "delete_time",
		"invite_id", "token", "rid", "inviter_uid", "bypass_password", "expires_at",
		"join_success_count", "last_join_uid", "last_join_at",
	}, o, "room_invite", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("vi", i),
			o.Hex64("invtk", i),
			o.Col("rm", i),
			o.Col("uu", 1),
			false,
			o.NowMs + 86400000,
			int64(0),
			"",
			int64(0),
		}, nil
	})
}

func seedRoomInviteJoin(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_invite_join"}, []string{
		"create_time", "update_time", "delete_time",
		"invite_id", "token", "rid", "inviter_uid", "join_uid", "join_at",
	}, o, "room_invite_join", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("vi", i),
			o.Hex64("invtk", i),
			o.Col("rm", i),
			o.Col("uu", 1),
			o.Col("uu", 2),
			o.NowMs,
		}, nil
	})
}

func seedRoomCategory(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_category"}, []string{
		"create_time", "update_time", "delete_time",
		"cid", "name", "sort",
	}, o, "room_category", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("ca", i),
			"cat",
			0,
		}, nil
	})
}

func seedRoomTag(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_tag"}, []string{
		"create_time", "update_time", "delete_time",
		"tid", "name", "sort",
	}, o, "room_tag", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("tg", i),
			"tag",
			0,
		}, nil
	})
}

func seedRoomTagRelation(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_tag_relation"}, []string{
		"create_time", "update_time", "delete_time",
		"rid", "tag_id",
	}, o, "room_tag_relation", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("rm", i),
			o.Col("tg", i),
		}, nil
	})
}

func seedRoomUser(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_user"}, []string{
		"create_time", "update_time", "delete_time",
		"rid", "uid", "role", "mute_until", "room_nickname", "room_remark",
		"join_room_time", "last_speak_time", "last_speak_ip",
	}, o, "room_user", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("rm", i),
			o.Col("uu", i),
			int16(0),
			int64(0),
			"",
			"",
			o.NowMs,
			int64(0),
			"",
		}, nil
	})
}

func seedRoomMessage(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	rid := o.Col("rm", 1)
	sender := o.Col("uu", 1)
	return copyFrom(ctx, pool, pgx.Identifier{"room_message"}, []string{
		"create_time", "update_time", "delete_time",
		"client_create_time", "rid", "seq_id", "mid", "client_mid", "sender_uid", "state", "ip", "withdraw_time",
	}, o, "room_message", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.NowMs,
			rid,
			i,
			o.Col("mm", i),
			o.Col("mb", i),
			sender,
			int16(1),
			"127.0.0.1",
			int64(0),
		}, nil
	})
}

func seedRoomMessageContent(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_message_content"}, []string{
		"create_time", "update_time", "delete_time",
		"client_create_time", "type", "type_id", "content", "mid", "client_cid", "cid",
	}, o, "room_message_content", func(i int64, o seedOpts) ([]any, error) {
		mid := o.Col("mm", i)
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.NowMs,
			"text",
			"",
			o.JSONEmpty,
			mid,
			o.Col("cc", i),
			o.Col("cn", i),
		}, nil
	})
}

func seedRoomMessageAck(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	sender := o.Col("uu", 1)
	return copyFrom(ctx, pool, pgx.Identifier{"room_message_ack"}, []string{
		"create_time", "update_time", "delete_time",
		"state", "confirmed_time", "last_try_time", "retry_count", "rid", "seq_id", "uid", "mid", "is_offline",
	}, o, "room_message_ack", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			int16(0),
			int64(0),
			int64(0),
			int32(0),
			o.Col("rm", 1),
			i,
			sender,
			o.Col("mm", i),
			false,
		}, nil
	})
}

func seedRoomMessageWithdrawAck(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	sender := o.Col("uu", 1)
	return copyFrom(ctx, pool, pgx.Identifier{"room_message_withdraw_ack"}, []string{
		"create_time", "update_time", "delete_time",
		"state", "confirmed_time", "last_try_time", "retry_count", "rid", "seq_id", "uid", "mid",
	}, o, "room_message_withdraw_ack", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			int16(0),
			int64(0),
			int64(0),
			int32(0),
			o.Col("rm", 1),
			i,
			sender,
			o.Col("mm", i),
		}, nil
	})
}

func seedRoomMessageMention(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_message_mention"}, []string{
		"create_time", "update_time", "delete_time",
		"rid", "mid", "seq_id", "uid", "sender_uid", "is_at_all", "is_read", "read_at",
	}, o, "room_message_mention", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("rm", 1),
			o.Col("mm", i),
			i,
			o.Col("uu", 2),
			o.Col("uu", 1),
			false,
			false,
			int64(0),
		}, nil
	})
}

func seedUserOnlineStat(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_online_stat"}, []string{
		"create_time", "update_time", "delete_time",
		"stat_id", "uid", "stat_date", "stat_type", "login_count", "total_seconds", "avg_duration",
		"max_duration", "first_login", "last_login", "peak_hour",
	}, o, "user_online_stat", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("st", i),
			o.Col("uu", i),
			int64(20260101),
			"day",
			0,
			0,
			float64(0),
			0,
			int64(0),
			int64(0),
			0,
		}, nil
	})
}

func seedUserDeviceSession(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	meta := o.JSONEmpty
	return copyFrom(ctx, pool, pgx.Identifier{"user_device_session"}, []string{
		"create_time", "update_time", "delete_time",
		"uid", "device_id", "device_finger", "current_sid", "platform", "device_name",
		"last_login", "last_logout", "total_sessions", "is_trusted", "is_blocked",
		"first_seen", "login_count", "total_online", "avg_duration",
		"last_location_ip", "last_location_country", "last_location_city", "meta_info",
	}, o, "user_device_session", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("uu", i),
			o.Hex64("udsdi", i),
			o.Hex64("udsdf", i) + o.Hex64("udsdg", i),
			o.Hex64("udsds", i),
			"web",
			"bulk",
			int64(0),
			int64(0),
			0,
			false,
			false,
			int64(0),
			0,
			0,
			float64(0),
			"",
			"",
			"",
			meta,
		}, nil
	})
}

func seedUserSession(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_session"}, []string{
		"create_time", "update_time", "delete_time",
		"sid", "uid", "device_id", "device_finger", "platform", "login_time", "logout_time", "last_activity",
		"is_active", "is_expired", "login_ip", "user_agent", "client_version", "notification", "session_data", "expires_at", "reason",
	}, o, "user_session", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("ss", i),
			o.Col("uu", i),
			o.Hex64("ussdi", i),
			o.Hex64("usf1", i) + o.Hex64("usf2", i),
			"web",
			o.NowMs,
			int64(0),
			o.NowMs,
			true,
			false,
			"127.0.0.1",
			"",
			"",
			true,
			"{}",
			int64(0),
			"",
		}, nil
	})
}

func seedUserFriendRequest(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_friend_request"}, []string{
		"create_time", "update_time", "delete_time",
		"fr_id", "receiver_uid", "sender_uid", "gid", "remark", "message", "state", "expires_at", "processed_at",
	}, o, "user_friend_request", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("fr", i),
			o.Col("fq", i),
			o.Col("uu", i),
			o.Col("fg", i),
			"",
			"",
			int16(0),
			o.NowMs + 86400000*7,
			int64(0),
		}, nil
	})
}

func seedUserMessageNotification(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_message_notification"}, []string{
		"create_time", "update_time", "delete_time",
		"nid", "uid", "type", "related_id", "content", "state", "status", "read_at",
	}, o, "user_message_notification", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("nn", i),
			o.Col("uu", i),
			int16(10),
			o.Col("uu", 1),
			"{}",
			int16(0),
			int16(1),
			int64(0),
		}, nil
	})
}

func seedUserOnlineHistory(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	empty := o.JSONEmpty
	return copyFrom(ctx, pool, pgx.Identifier{"user_online_history"}, []string{
		"create_time", "update_time", "delete_time",
		"hid", "sid", "uid", "event_type", "event_subtype", "status_before", "status_after", "online_sec", "reason", "event_time",
		"platform", "device_type", "device_model", "os_version",
		"ip", "country", "country_en", "region", "region_en", "city", "city_en", "latitude", "longitude", "timezone",
		"network_type", "isp", "network_signal", "network_latency",
		"is_foreground", "battery_level", "is_charging",
		"device_info", "network_info", "location_info", "app_state",
	}, o, "user_online_history", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("ht", i),
			o.Col("ss", i),
			o.Col("uu", i),
			"login",
			"",
			"",
			"online",
			0,
			"",
			o.NowMs,
			"web",
			"",
			"",
			"",
			"127.0.0.1",
			"", "",
			"", "",
			"", "",
			0.0, 0.0,
			"",
			"wifi",
			"",
			0,
			0,
			false,
			0.0,
			false,
			empty,
			empty,
			empty,
			empty,
		}, nil
	})
}

func seedUserCurrentStatus(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	di := mustJSON(map[string]any{"bulk": true})
	return copyFrom(ctx, pool, pgx.Identifier{"user_current_status"}, []string{
		"create_time", "update_time", "delete_time",
		"uid", "is_online", "current_status", "last_online", "last_login", "last_logout", "last_heartbeat",
		"custom_state", "current_session_id", "websocket_id",
		"platform", "device_type", "device_model", "os_version", "app_version",
		"device_info", "concurrent_devices", "total_online_today", "ip",
	}, o, "user_current_status", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("uu", i),
			false,
			"offline",
			int64(0),
			int64(0),
			int64(0),
			int64(0),
			"",
			o.Hex64("ucssi", i),
			(o.Hex64("ucsws", i) + o.Hex64("ucsw2", i))[:100],
			"web",
			"",
			"",
			"",
			"",
			di,
			0,
			0,
			"127.0.0.1",
		}, nil
	})
}

func seedRoomAnnouncement(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_announcement"}, []string{
		"create_time", "update_time", "delete_time",
		"rid", "content", "updated_by", "pinned",
	}, o, "room_announcement", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("rm", i),
			o.JSONArray,
			o.Col("uu", 1),
			false,
		}, nil
	})
}

func seedRoomMuteConfig(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_mute_config"}, []string{
		"create_time", "update_time", "delete_time",
		"config_id", "rid", "is_mute_all", "mute_all_by", "mute_all_reason", "rule_type", "rule_config", "allow_roles", "except_users",
		"effective_at", "expires_at", "is_active", "version",
	}, o, "room_mute_config", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("mq", i),
			o.Col("rm", i),
			false,
			"",
			"",
			int16(0),
			"{}",
			"[]",
			"[]",
			int64(0),
			int64(0),
			false,
			1,
		}, nil
	})
}

func seedRoomAdminOperation(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_admin_operation"}, []string{
		"create_time", "update_time", "delete_time",
		"rid", "op_type", "operator_uid", "sid", "related_id", "before_data", "after_data",
	}, o, "room_admin_operation", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("rm", i),
			"room_name_update",
			o.Col("uu", 1),
			o.Col("ss", i),
			"",
			"{}",
			"{}",
		}, nil
	})
}

func seedUserOperation(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_operation"}, []string{
		"create_time", "update_time", "delete_time",
		"uid", "op_type", "sid", "related_id", "before_data", "after_data",
	}, o, "user_operation", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("uu", i),
			"nickname",
			o.Col("ss", i),
			"",
			"{}",
			"{}",
		}, nil
	})
}

func seedUserRoomBlock(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"user_room_block"}, []string{
		"create_time", "update_time", "delete_time",
		"uid", "rid",
	}, o, "user_room_block", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("uu", i),
			o.Col("rm", i),
		}, nil
	})
}

func seedRoomUserBlockUser(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"room_user_block_user"}, []string{
		"create_time", "update_time", "delete_time",
		"uid", "rid", "target_uid",
	}, o, "room_user_block_user", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.Col("uu", i),
			o.Col("rm", i),
			o.Col("fd", i),
		}, nil
	})
}

func seedMediaCallRecord(ctx context.Context, pool *pgxpool.Pool, o seedOpts) (int64, error) {
	return copyFrom(ctx, pool, pgx.Identifier{"media_call_record"}, []string{
		"create_time", "update_time", "delete_time",
		"call_id", "rid", "call_type", "call_scene", "inviter_uid", "invitee_uids", "started_at", "ended_at", "duration_sec", "end_reason", "operator_uid",
	}, o, "media_call_record", func(i int64, o seedOpts) ([]any, error) {
		return []any{
			o.NowMs, o.NowMs, int64(0),
			o.JTI(i + 3_000_000_000),
			o.Col("rm", i),
			"audio",
			"room",
			o.Col("uu", 1),
			o.InviteeJSON,
			o.NowMs,
			int64(0),
			int64(0),
			"hangup",
			"",
		}, nil
	})
}
