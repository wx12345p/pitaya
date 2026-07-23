// Package store 提供基于 Redis 的房间目录与状态快照(Phase 2 抗宕机)。
//
//   - richman:room:{roomId}:owner  房间 owner game 节点(目录, 用于恢复/重连定位)
//   - richman:room:{roomId}:state  房间状态快照(protobuf 序列化, 带 TTL)
//   - richman:room:{roomId}:lock   恢复锁(SETNX, 防并发双恢复)
package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"richman/game"
	"richman/protos"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

// RedisStore 房间目录 + 快照存储
type RedisStore struct {
	cli *redis.Client
	ttl time.Duration
}

// NewRedisStore 创建 Redis 存储(不校验连通性, 需另行 Ping)
func NewRedisStore(addr string) *RedisStore {
	return &RedisStore{
		cli: redis.NewClient(&redis.Options{Addr: addr}),
		ttl: 30 * time.Minute,
	}
}

// Ping 校验连通性
func (s *RedisStore) Ping(ctx context.Context) error {
	return s.cli.Ping(ctx).Err()
}

func ownerKey(roomID string) string { return "richman:room:" + roomID + ":owner" }
func stateKey(roomID string) string { return "richman:room:" + roomID + ":state" }
func lockKey(roomID string) string  { return "richman:room:" + roomID + ":lock" }

// PutOwner 记录房间 owner
func (s *RedisStore) PutOwner(ctx context.Context, roomID, serverID string) error {
	return s.cli.Set(ctx, ownerKey(roomID), serverID, s.ttl).Err()
}

// GetOwner 读取房间 owner(不存在返回 "")
func (s *RedisStore) GetOwner(ctx context.Context, roomID string) (string, error) {
	v, err := s.cli.Get(ctx, ownerKey(roomID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	return v, err
}

// DeleteRoom 删除房间的目录与快照(对局结束时清理)
func (s *RedisStore) DeleteRoom(ctx context.Context, roomID string) error {
	return s.cli.Del(ctx, ownerKey(roomID), stateKey(roomID), lockKey(roomID)).Err()
}

// SaveSnapshot 写入房间快照
func (s *RedisStore) SaveSnapshot(ctx context.Context, snap game.Snapshot) error {
	b, err := proto.Marshal(toProto(snap))
	if err != nil {
		return err
	}
	return s.cli.Set(ctx, stateKey(snap.RoomID), b, s.ttl).Err()
}

// LoadSnapshot 读取房间快照(不存在返回 nil, nil)
func (s *RedisStore) LoadSnapshot(ctx context.Context, roomID string) (*game.Snapshot, error) {
	b, err := s.cli.Get(ctx, stateKey(roomID)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m protos.RoomSnapshot
	if err := proto.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	snap := fromProto(&m)
	return &snap, nil
}

// ==================== 分布式匹配(Redis 共享队列, 支持 lobby 水平扩容) ====================

const (
	mmOpenKey  = "richman:mm:openroom" // 当前开放中的房间(String, 单个)
	mmMemberSp = "\x1f"                // 成员编码分隔符(uid<sep>name)
)

func mmMembersKey(roomID string) string { return "richman:mm:room:" + roomID + ":members" }
func mmOwnerKey(roomID string) string   { return "richman:mm:room:" + roomID + ":owner" }

// matchScript 原子撮合: 无开放房间则用候选 roomId/owner 建房; 追加成员并返回座位;
// 满员则关闭开放房间(下一次 join 开新房)。KEYS[1]=openKey。
// ARGV: uid,name,maxPlayers,candidateRoomId,candidateOwner,ttlSeconds
// 返回: {roomId, seat, full(0/1), owner}
var matchScript = redis.NewScript(`
local openKey = KEYS[1]
local uid  = ARGV[1]
local name = ARGV[2]
local maxP = tonumber(ARGV[3])
local candRoom  = ARGV[4]
local candOwner = ARGV[5]
local ttl  = tonumber(ARGV[6])

local roomId = redis.call('GET', openKey)
if not roomId then
  roomId = candRoom
  redis.call('SET', openKey, roomId, 'EX', ttl)
  redis.call('SET', 'richman:mm:room:'..roomId..':owner', candOwner, 'EX', ttl)
end
local membersKey = 'richman:mm:room:'..roomId..':members'
local len = redis.call('RPUSH', membersKey, uid..'` + mmMemberSp + `'..name)
redis.call('EXPIRE', membersKey, ttl)
local full = 0
if len >= maxP then
  full = 1
  redis.call('DEL', openKey)
else
  redis.call('EXPIRE', openKey, ttl)
end
local owner = redis.call('GET', 'richman:mm:room:'..roomId..':owner')
return {roomId, len - 1, full, owner}
`)

// MatchJoin 原子加入匹配, 返回房间/座位/是否满员/owner
func (s *RedisStore) MatchJoin(ctx context.Context, uid, name string, maxPlayers int, candidateRoomID, candidateOwner string) (roomID string, seat int, full bool, owner string, err error) {
	ttl := int64(s.ttl / time.Second)
	res, e := matchScript.Run(ctx, s.cli, []string{mmOpenKey}, uid, name, maxPlayers, candidateRoomID, candidateOwner, ttl).Result()
	if e != nil {
		return "", 0, false, "", e
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 4 {
		return "", 0, false, "", fmt.Errorf("匹配脚本返回异常: %v", res)
	}
	roomID, _ = arr[0].(string)
	seat = toInt(arr[1])
	full = toInt(arr[2]) == 1
	owner, _ = arr[3].(string)
	return roomID, seat, full, owner, nil
}

// MatchMembers 读取房间当前成员(按座位顺序)
func (s *RedisStore) MatchMembers(ctx context.Context, roomID string) ([]game.Member, error) {
	vals, err := s.cli.LRange(ctx, mmMembersKey(roomID), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	members := make([]game.Member, 0, len(vals))
	for i, v := range vals {
		uid, name := v, ""
		if idx := strings.Index(v, mmMemberSp); idx >= 0 {
			uid, name = v[:idx], v[idx+len(mmMemberSp):]
		}
		members = append(members, game.Member{UID: uid, Name: name, Seat: i})
	}
	return members, nil
}

// MatchCleanup 清理匹配中间态(满员建房后调用; openKey 已在脚本内删除)
func (s *RedisStore) MatchCleanup(ctx context.Context, roomID string) error {
	return s.cli.Del(ctx, mmMembersKey(roomID), mmOwnerKey(roomID)).Err()
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

// AcquireRecoveryLock 尝试获取恢复锁(SETNX + TTL), 返回是否成功
func (s *RedisStore) AcquireRecoveryLock(ctx context.Context, roomID, holder string, ttl time.Duration) (bool, error) {
	return s.cli.SetNX(ctx, lockKey(roomID), holder, ttl).Result()
}

// ReleaseRecoveryLock 释放恢复锁
func (s *RedisStore) ReleaseRecoveryLock(ctx context.Context, roomID string) error {
	return s.cli.Del(ctx, lockKey(roomID)).Err()
}

// ==================== 快照 <-> proto 转换 ====================

func toProto(snap game.Snapshot) *protos.RoomSnapshot {
	m := &protos.RoomSnapshot{
		RoomId:      snap.RoomID,
		State:       int32(snap.State),
		Round:       int32(snap.Round),
		TurnsTaken:  int32(snap.TurnsTaken),
		CurrentSeat: int32(snap.CurrentSeat),
	}
	for _, p := range snap.Players {
		m.Players = append(m.Players, &protos.PlayerState{
			Uid: p.UID, Name: p.Name, Seat: int32(p.Seat), Money: p.Money,
			Pos: int32(p.Pos), Bankrupt: p.Bankrupt, IsAi: p.IsAI,
		})
	}
	for _, t := range snap.Tiles {
		m.Tiles = append(m.Tiles, &protos.TileState{
			Index: int32(t.Index), OwnerUid: t.OwnerUID, Level: int32(t.Level),
		})
	}
	return m
}

func fromProto(m *protos.RoomSnapshot) game.Snapshot {
	snap := game.Snapshot{
		RoomID:      m.RoomId,
		State:       int(m.State),
		Round:       int(m.Round),
		TurnsTaken:  int(m.TurnsTaken),
		CurrentSeat: int(m.CurrentSeat),
	}
	for _, p := range m.Players {
		snap.Players = append(snap.Players, game.PlayerState{
			UID: p.Uid, Name: p.Name, Seat: int(p.Seat), Money: p.Money,
			Pos: int(p.Pos), Bankrupt: p.Bankrupt, IsAI: p.IsAi,
		})
	}
	for _, t := range m.Tiles {
		snap.Tiles = append(snap.Tiles, game.TileState{
			Index: int(t.Index), OwnerUID: t.OwnerUid, Level: int(t.Level),
		})
	}
	return snap
}
