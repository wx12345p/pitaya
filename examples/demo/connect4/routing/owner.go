// Package routing 提供房间到 game 节点的映射策略。
//
// 采用 rendezvous(最高随机权重)哈希: 对每个候选 game 节点计算
// score = fnv64(roomID + "/" + serverID), 取分值最大者作为该房间的 owner。
// 相比取模, 节点增减时需要重新分配的房间最少。
//
// 注意: 本函数只用于"建房(或故障重指派)时选一次 owner"; 选定后 owner 写入
// session 与 Redis 目录, 路由时直接读取, 不再现算, 避免不同 connector
// 视图不一致造成误路由。
package routing

import (
	"hash/fnv"

	"github.com/topfreegames/pitaya/v2/cluster"
)

// OwnerServerID 从存活的 game 节点集合中为 roomID 选定 owner 节点。
// 无候选节点时 ok 为 false。
func OwnerServerID(roomID string, servers map[string]*cluster.Server) (string, bool) {
	var bestID string
	var bestScore uint64
	found := false
	for id := range servers {
		s := score(roomID, id)
		if !found || s > bestScore || (s == bestScore && id < bestID) {
			bestID, bestScore, found = id, s, true
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
