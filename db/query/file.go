package query

import (
	"errors"

	"github.com/xd/quic-server/db"
	"github.com/xd/quic-server/db/entity"
	"gorm.io/gorm"
)

type MsgContentFile struct {
	Hash     string `json:"hash"`
	TypeMain string `json:"type_main"`
	Ext      string `json:"ext"`
	TypeSub  string `json:"type_sub"`
	Path     string `json:"path"`
	Width    uint32 `json:"width"`
	Height   uint32 `json:"height"`
	Thumb    string `json:"thumb"`
	FileSize int64  `json:"file_size"`
	Filename string `json:"filename"`
}

// 查找房间上传的文件，需要房间存在，用户存在，消息存在，消息内容存在，文件存在，用户上传文件存在，
// 并且用户加入了该房间，消息没有被删除和撤回，用户没有被删除，房间没有被删除,房间状态正常，消息内容没有被删除，
// 文件没有被删除，用户上传文件没有被删除，并且文件状态为上传完成
func GetMsgContentFile(ufId string, uid string) (*MsgContentFile, error) {
	var file MsgContentFile
	err := db.GetDB().
		Table("user_upload_file AS uuf").
		Joins("INNER JOIN upload_file AS uf ON uf.fid = uuf.fid").
		Joins("INNER JOIN room_message_content AS rmc ON rmc.type_id = uuf.uf_id AND rmc.type IN ('file','image', 'video','audio')").
		Joins("INNER JOIN room_message AS rm ON rm.mid = rmc.mid").
		Joins("INNER JOIN room_user AS ru ON ru.rid = rm.rid").
		Joins("INNER JOIN room AS r ON r.rid = ru.rid").
		Joins(`INNER JOIN "user" AS u ON u.uid = ru.uid`).
		Where("u.uid = ?", uid).
		Where("r.state = ?", 1).
		Where("rm.state = ?", 1).
		Where("rm.delete_time = 0").
		Where("u.delete_time = 0").
		Where("r.delete_time = 0").
		Where("rmc.delete_time = 0").
		Where("uf.delete_time = 0").
		Where("uuf.delete_time = 0").
		Where("uf.state = ?", 2).
		Where("uuf.uf_id = ?", ufId).
		Select("uf.ext", "uf.type_main", "uf.type_sub", "uf.path", "uf.width", "uf.height", "uf.total_size AS file_size", "uuf.filename", "uf.hash", "uf.thumb").
		Take(&file).Error
	if err != nil {
		return nil, err
	}
	return &file, nil
}

// UfFileRow 按 uf_id 查 UserUploadFile + UploadFile，用于公开场景按 uf_id 取文件
type UfFileRow struct {
	UfId     string
	Scene    string
	Hash     string
	TypeMain string
	TypeSub  string
	Ext      string
	Path     string
	Width    uint32
	Height   uint32
	Thumb    string
	FileSize int64
	Filename string
}

const ufFileSelect = "uuf.uf_id AS uf_id, uuf.scene AS scene, uuf.filename AS filename, " +
	"uf.hash AS hash, uf.type_main AS type_main, uf.type_sub AS type_sub, uf.ext AS ext, uf.path AS path, " +
	"uf.width AS width, uf.height AS height, uf.thumb AS thumb, uf.total_size AS file_size"

func ufRowToMsgContentFile(row *UfFileRow) *MsgContentFile {
	return &MsgContentFile{
		Hash:     row.Hash,
		TypeMain: row.TypeMain,
		TypeSub:  row.TypeSub,
		Ext:      row.Ext,
		Path:     row.Path,
		Width:    row.Width,
		Height:   row.Height,
		Thumb:    row.Thumb,
		FileSize: row.FileSize,
		Filename: row.Filename,
	}
}

// ResolveDownloadByUfId 解析可下载文件：公开场景头像/公告等无需消息附件链；其余走房间消息权限校验。
func ResolveDownloadByUfId(ufId, uid string) (*MsgContentFile, error) {
	row, err := GetUfFileByUfId(ufId)
	if err == nil && entity.IsPublicUploadScene(row.Scene) {
		return ufRowToMsgContentFile(row), nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return GetMsgContentFile(ufId, uid)
}

// GetUfFileByUfId 根据 uf_id 查询用户文件及底层文件信息；公开场景（user_avatar/room_avatar/room_announcement/room_info）可直接用此结果做 URL
func GetUfFileByUfId(ufId string) (*UfFileRow, error) {
	var row UfFileRow
	err := db.GetDB().
		Table("user_upload_file AS uuf").
		Joins("INNER JOIN upload_file AS uf ON uf.fid = uuf.fid").
		Where("uuf.uf_id = ?", ufId).
		Where("uuf.delete_time = 0").
		Where("uf.delete_time = 0").
		Where("uf.state = ?", 2).
		Select(ufFileSelect).
		Take(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// GetUfFileByUfIdForUser 仅当记录属于指定 uid 时返回（用于非公开场景校验）
func GetUfFileByUfIdForUser(ufId string, uid string) (*UfFileRow, error) {
	var row UfFileRow
	err := db.GetDB().
		Table("user_upload_file AS uuf").
		Joins("INNER JOIN upload_file AS uf ON uf.fid = uuf.fid").
		Where("uuf.uf_id = ?", ufId).
		Where("uuf.uid = ?", uid).
		Where("uuf.delete_time = 0").
		Where("uf.delete_time = 0").
		Where("uf.state = ?", 2).
		Select(ufFileSelect).
		Take(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// GetFavoriteImageFileByHash 用户已收藏的图片按 file_hash 取底层文件信息，不校验房间成员关系。
func GetFavoriteImageFileByHash(fileHash, uid string) (*MsgContentFile, error) {
	fav, err := GetUserEmojiFavorite(uid, entity.UserEmojiFavoriteKindImage, fileHash)
	if err != nil {
		return nil, err
	}
	if fav == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var file MsgContentFile
	err = db.GetDB().
		Table("upload_file AS uf").
		Where("uf.hash = ?", fileHash).
		Where("uf.delete_time = 0").
		Where("uf.state = ?", 2).
		Select("uf.ext", "uf.type_main", "uf.type_sub", "uf.path", "uf.width", "uf.height", "uf.total_size AS file_size", "'' AS filename", "uf.hash", "uf.thumb").
		Take(&file).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, err
	}
	return &file, nil
}
