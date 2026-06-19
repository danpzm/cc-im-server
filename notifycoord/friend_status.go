package notifycoord

import (
	"github.com/xd/quic-server/db/query"
)

// FriendStatusSyncRecipientUids 与 broadcastFriendUserStatusSync 一致的接收者集合：本人 + 在线好友
func FriendStatusSyncRecipientUids(subjectUid string) ([]string, error) {
	if subjectUid == "" {
		return nil, nil
	}
	out := make([]string, 0, 32)
	out = append(out, subjectUid)

	friendGroups, err := query.GetFriendGroupList(subjectUid)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{subjectUid: {}}
	for _, g := range friendGroups {
		if g == nil {
			continue
		}
		for _, f := range g.FriendList {
			if f == nil || f.Uid == subjectUid {
				continue
			}
			if f.Status == nil || !f.Status.IsOnline {
				continue
			}
			if _, ok := seen[f.Uid]; ok {
				continue
			}
			seen[f.Uid] = struct{}{}
			out = append(out, f.Uid)
		}
	}
	return out, nil
}
