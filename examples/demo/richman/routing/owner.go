// Package routing 提供房间到 game 节点的映射策略。
//
// 采用 rendezvous(最高随机权重)哈希: 对每个候选 game 节点计算
// score = fnv64(roomID + "/" + serverID), 取分值最大的节点作为该房间的 owner。
// 相比取模, 成员增减时重分配的房间数最少。
//
// 注意: 本函数仅用于"建房时选定 owner"这一次性放置; 之后 owner 会写入 session,
// 路由时直接读取, 不再现算, 以避免不同 connector 视图不一致导致误路由。
package routing

import (
	"hash/fnv"

	"github.com/topfreegames/pitaya/v2/cluster"
)

// OwnerServerID 从存活的 game 节点集合中为 roomID 选定 owner 节点。
// servers 为 app.GetServersByType("game") 或路由函数收到的同类型节点集合。
// 返回选中的 serverID; 若没有候选节点, ok 为 false。
func OwnerServerID(roomID string, servers map[string]*cluster.Server) (string, bool) {
	var bestID string
	var bestScore uint64
	found := false
	for id := range servers {
		score := score(roomID, id)
		if !found || score > bestScore || (score == bestScore && id < bestID) {
			bestID = id
			bestScore = score
			found = true
		}
	}
	return bestID, found
}

func score(roomID, serverID string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(roomID))
	_, _ = h.Write([]byte{'/'})
	_, _ = h.Write([]byte(serverID))
	return h.Sum64()
}
