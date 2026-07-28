// Package store 用 Redis 承载四子棋的"在线态数据库": 撮合仲裁、房间目录与
// 状态快照、重连与观战索引。所有键都带 TTL, 对局结束即清理。
//
// 键空间:
//
//	c4:uid:{uid}                    Hash   {name}         身份档案(支撑 last_uid 续接), TTL 1h
//	c4:mm:open                      String roomId         当前开放中的匹配房间(撮合仲裁点), TTL 30m
//	c4:mm:room:{roomId}:members     List   uid<0x1f>name  候选花名册
//	c4:mm:room:{roomId}:owner       String serverId       候选 owner 节点
//	c4:room:{roomId}:owner          String serverId       房间 owner 目录(恢复/重连定位), TTL 30m
//	c4:room:{roomId}:state          Bytes  GameSnapshot   每步落子后写入的状态快照, TTL 30m
//	c4:room:{roomId}:lock           String holder         恢复锁(SETNX), TTL 10s
//	c4:player:{uid}:room            String roomId         按玩家反查在局房间(重连用), TTL 30m
//	c4:rooms:live                   ZSET   roomId->ts     可观战房间列表
package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connect4/game"
	"connect4/protos"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

// 键前缀与分隔符
const (
	keyPrefix   = "c4:"
	mmOpenKey   = keyPrefix + "mm:open"
	liveKey     = keyPrefix + "rooms:live"
	memberSep   = "\x1f" // 花名册成员编码分隔符: uid<sep>name
	identityTTL = time.Hour
)

// RedisStore Redis 存储
type RedisStore struct {
	cli *redis.Client
	ttl time.Duration
}

// NewRedisStore 创建存储(不校验连通性, 需另行 Ping)
func NewRedisStore(addr string) *RedisStore {
	return &RedisStore{
		cli: redis.NewClient(&redis.Options{Addr: addr}),
		ttl: 30 * time.Minute,
	}
}

// Ping 校验连通性
func (s *RedisStore) Ping(ctx context.Context) error { return s.cli.Ping(ctx).Err() }

func identityKey(uid string) string     { return keyPrefix + "uid:" + uid }
func ownerKey(roomID string) string     { return keyPrefix + "room:" + roomID + ":owner" }
func stateKey(roomID string) string     { return keyPrefix + "room:" + roomID + ":state" }
func lockKey(roomID string) string      { return keyPrefix + "room:" + roomID + ":lock" }
func playerRoomKey(uid string) string   { return keyPrefix + "player:" + uid + ":room" }
func mmMembersKey(roomID string) string { return keyPrefix + "mm:room:" + roomID + ":members" }
func mmOwnerKey(roomID string) string   { return keyPrefix + "mm:room:" + roomID + ":owner" }

// ==================== 身份档案(支撑登录续接) ====================

// PutIdentity 写入/续期玩家身份
func (s *RedisStore) PutIdentity(ctx context.Context, uid, name string) error {
	if err := s.cli.HSet(ctx, identityKey(uid), "name", name).Err(); err != nil {
		return err
	}
	return s.cli.Expire(ctx, identityKey(uid), identityTTL).Err()
}

// GetIdentity 读取身份, 不存在返回 ok=false
func (s *RedisStore) GetIdentity(ctx context.Context, uid string) (name string, ok bool, err error) {
	name, err = s.cli.HGet(ctx, identityKey(uid), "name").Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	_ = s.cli.Expire(ctx, identityKey(uid), identityTTL).Err()
	return name, true, nil
}

// ==================== 撮合(共享队列 + Lua 原子操作) ====================

// matchScript 原子撮合: 无开放房则用候选 roomId/owner 开房; 追加成员并返回座位;
// 满员则删除开放房标记(下一次 match 会开新房)。
// KEYS[1]=openKey; ARGV: uid, name, maxPlayers, candRoomId, candOwner, ttlSeconds, prefix
// 返回: {roomId, seat, full(0/1), owner}
var matchScript = redis.NewScript(`
local openKey = KEYS[1]
local uid     = ARGV[1]
local name    = ARGV[2]
local maxP    = tonumber(ARGV[3])
local candRoom  = ARGV[4]
local candOwner = ARGV[5]
local ttl     = tonumber(ARGV[6])
local prefix  = ARGV[7]

local roomId = redis.call('GET', openKey)
if not roomId then
  roomId = candRoom
  redis.call('SET', openKey, roomId, 'EX', ttl)
  redis.call('SET', prefix..'mm:room:'..roomId..':owner', candOwner, 'EX', ttl)
end
local membersKey = prefix..'mm:room:'..roomId..':members'
local len = redis.call('RPUSH', membersKey, uid..'` + memberSep + `'..name)
redis.call('EXPIRE', membersKey, ttl)
local full = 0
if len >= maxP then
  full = 1
  redis.call('DEL', openKey)
else
  redis.call('EXPIRE', openKey, ttl)
end
local owner = redis.call('GET', prefix..'mm:room:'..roomId..':owner')
return {roomId, len - 1, full, owner}
`)

// claimAIScript AI 补位的 CAS: 仅当该房仍是开放房、且人数仍未满时, 关闭开放房并交给调用者建 AI 房。
// KEYS[1]=openKey; ARGV: roomId, maxPlayers, prefix
var claimAIScript = redis.NewScript(`
local openKey = KEYS[1]
local roomId  = ARGV[1]
local maxP    = tonumber(ARGV[2])
local prefix  = ARGV[3]
if redis.call('GET', openKey) ~= roomId then return 0 end
local len = redis.call('LLEN', prefix..'mm:room:'..roomId..':members')
if len < 1 or len >= maxP then return 0 end
redis.call('DEL', openKey)
return 1
`)

// leaveScript 取消匹配: 仅当仍在开放房内时移除该成员; 房间空了顺带清理。
// KEYS[1]=openKey; ARGV: roomId, member(uid<sep>name), prefix
var leaveScript = redis.NewScript(`
local openKey = KEYS[1]
local roomId  = ARGV[1]
local member  = ARGV[2]
local prefix  = ARGV[3]
if redis.call('GET', openKey) ~= roomId then return 0 end
local membersKey = prefix..'mm:room:'..roomId..':members'
if redis.call('LREM', membersKey, 1, member) == 0 then return 0 end
if redis.call('LLEN', membersKey) == 0 then
  redis.call('DEL', openKey)
  redis.call('DEL', membersKey)
  redis.call('DEL', prefix..'mm:room:'..roomId..':owner')
end
return 1
`)

// MatchJoin 原子加入匹配队列, 返回房间/座位/是否满员/owner
func (s *RedisStore) MatchJoin(ctx context.Context, uid, name string, maxPlayers int, candRoomID, candOwner string) (roomID string, seat int, full bool, owner string, err error) {
	ttl := int64(s.ttl / time.Second)
	res, err := matchScript.Run(ctx, s.cli, []string{mmOpenKey}, uid, name, maxPlayers, candRoomID, candOwner, ttl, keyPrefix).Result()
	if err != nil {
		return "", 0, false, "", err
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 4 {
		return "", 0, false, "", fmt.Errorf("撮合脚本返回异常: %v", res)
	}
	roomID, _ = arr[0].(string)
	owner, _ = arr[3].(string)
	return roomID, toInt(arr[1]), toInt(arr[2]) == 1, owner, nil
}

// MatchClaimForAI 抢占等待中的房间以 AI 补位, 返回是否抢占成功
func (s *RedisStore) MatchClaimForAI(ctx context.Context, roomID string, maxPlayers int) (bool, error) {
	res, err := claimAIScript.Run(ctx, s.cli, []string{mmOpenKey}, roomID, maxPlayers, keyPrefix).Result()
	if err != nil {
		return false, err
	}
	return toInt(res) == 1, nil
}

// MatchLeave 取消匹配, 返回是否成功从队列移除(false=已开局或不在队列)
func (s *RedisStore) MatchLeave(ctx context.Context, roomID, uid, name string) (bool, error) {
	res, err := leaveScript.Run(ctx, s.cli, []string{mmOpenKey}, roomID, encodeMember(uid, name), keyPrefix).Result()
	if err != nil {
		return false, err
	}
	return toInt(res) == 1, nil
}

// MatchMembers 读取候选花名册(按入队顺序即座位号)
func (s *RedisStore) MatchMembers(ctx context.Context, roomID string) ([]game.Member, error) {
	vals, err := s.cli.LRange(ctx, mmMembersKey(roomID), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	members := make([]game.Member, 0, len(vals))
	for i, v := range vals {
		uid, name := decodeMember(v)
		members = append(members, game.Member{UID: uid, Name: name, Seat: i})
	}
	return members, nil
}

// MatchCleanup 清理撮合中间态(建房后调用)
func (s *RedisStore) MatchCleanup(ctx context.Context, roomID string) error {
	return s.cli.Del(ctx, mmMembersKey(roomID), mmOwnerKey(roomID)).Err()
}

func encodeMember(uid, name string) string { return uid + memberSep + name }

func decodeMember(v string) (uid, name string) {
	if i := strings.Index(v, memberSep); i >= 0 {
		return v[:i], v[i+len(memberSep):]
	}
	return v, ""
}

// ==================== 房间目录 / 快照 / 索引 ====================

// PutOwner 记录房间 owner 节点
func (s *RedisStore) PutOwner(ctx context.Context, roomID, serverID string) error {
	return s.cli.Set(ctx, ownerKey(roomID), serverID, s.ttl).Err()
}

// GetOwner 读取房间 owner, 不存在返回空串
func (s *RedisStore) GetOwner(ctx context.Context, roomID string) (string, error) {
	v, err := s.cli.Get(ctx, ownerKey(roomID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	return v, err
}

// SaveSnapshot 写入房间状态快照
func (s *RedisStore) SaveSnapshot(ctx context.Context, snap game.Snapshot) error {
	b, err := proto.Marshal(SnapshotToProto(snap))
	if err != nil {
		return err
	}
	return s.cli.Set(ctx, stateKey(snap.RoomID), b, s.ttl).Err()
}

// LoadSnapshot 读取房间状态快照, 不存在返回 (nil, nil)
func (s *RedisStore) LoadSnapshot(ctx context.Context, roomID string) (*game.Snapshot, error) {
	b, err := s.cli.Get(ctx, stateKey(roomID)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m protos.GameSnapshot
	if err := proto.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	snap := SnapshotFromProto(&m)
	return &snap, nil
}

// BindPlayerRoom 建立 uid -> roomId 反查索引(重连定位用)
func (s *RedisStore) BindPlayerRoom(ctx context.Context, uid, roomID string) error {
	return s.cli.Set(ctx, playerRoomKey(uid), roomID, s.ttl).Err()
}

// GetPlayerRoom 读取玩家所在房间, 不存在返回空串
func (s *RedisStore) GetPlayerRoom(ctx context.Context, uid string) (string, error) {
	v, err := s.cli.Get(ctx, playerRoomKey(uid)).Result()
	if err == redis.Nil {
		return "", nil
	}
	return v, err
}

// UnbindPlayerRoom 删除玩家的房间索引
func (s *RedisStore) UnbindPlayerRoom(ctx context.Context, uid string) error {
	return s.cli.Del(ctx, playerRoomKey(uid)).Err()
}

// AddLiveRoom 把房间登记到可观战列表
func (s *RedisStore) AddLiveRoom(ctx context.Context, roomID string, startedAt int64) error {
	return s.cli.ZAdd(ctx, liveKey, redis.Z{Score: float64(startedAt), Member: roomID}).Err()
}

// ListLiveRooms 列出最近开局的房间(倒序), limit<=0 时取 20 条
func (s *RedisStore) ListLiveRooms(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 20
	}
	return s.cli.ZRevRange(ctx, liveKey, 0, int64(limit-1)).Result()
}

// DeleteRoom 清理房间的目录/快照/锁/观战登记与玩家索引(对局结束时调用)
func (s *RedisStore) DeleteRoom(ctx context.Context, roomID string, playerUIDs []string) error {
	pipe := s.cli.Pipeline()
	pipe.Del(ctx, ownerKey(roomID), stateKey(roomID), lockKey(roomID))
	pipe.ZRem(ctx, liveKey, roomID)
	for _, uid := range playerUIDs {
		pipe.Del(ctx, playerRoomKey(uid))
	}
	_, err := pipe.Exec(ctx)
	return err
}

// AcquireRecoveryLock 抢恢复锁(SETNX + TTL), 防并发双恢复
func (s *RedisStore) AcquireRecoveryLock(ctx context.Context, roomID, holder string, ttl time.Duration) (bool, error) {
	return s.cli.SetNX(ctx, lockKey(roomID), holder, ttl).Result()
}

// ReleaseRecoveryLock 释放恢复锁
func (s *RedisStore) ReleaseRecoveryLock(ctx context.Context, roomID string) error {
	return s.cli.Del(ctx, lockKey(roomID)).Err()
}

// ==================== 快照 <-> proto 转换 ====================

// SnapshotToProto 快照转 protobuf
func SnapshotToProto(snap game.Snapshot) *protos.GameSnapshot {
	m := &protos.GameSnapshot{
		RoomId:      snap.RoomID,
		State:       protos.RoomState(snap.State),
		Cols:        game.Cols,
		Rows:        game.Rows,
		Cells:       snap.Cells,
		CurrentSeat: int32(snap.CurrentSeat),
		MoveNo:      int32(snap.MoveNo),
		FirstSeat:   int32(snap.FirstSeat),
		WinnerUid:   snap.WinnerUID,
		WinnerSeat:  int32(snap.WinnerSeat),
		Reason:      protos.EndReason(snap.Reason),
		StartedAt:   snap.StartedAt,
	}
	for _, p := range snap.Players {
		m.Players = append(m.Players, &protos.PlayerState{
			Uid:          p.UID,
			Name:         p.Name,
			Seat:         int32(p.Seat),
			IsAi:         p.IsAI,
			TimeoutCount: int32(p.TimeoutCount),
			Online:       p.Online,
		})
	}
	for _, col := range snap.Moves {
		m.Moves = append(m.Moves, int32(col))
	}
	return m
}

// SnapshotFromProto protobuf 转快照
func SnapshotFromProto(m *protos.GameSnapshot) game.Snapshot {
	snap := game.Snapshot{
		RoomID:      m.RoomId,
		State:       game.RoomState(m.State),
		Cells:       m.Cells,
		CurrentSeat: int(m.CurrentSeat),
		MoveNo:      int(m.MoveNo),
		FirstSeat:   int(m.FirstSeat),
		WinnerUID:   m.WinnerUid,
		WinnerSeat:  int(m.WinnerSeat),
		Reason:      game.EndReason(m.Reason),
		StartedAt:   m.StartedAt,
	}
	for _, p := range m.Players {
		snap.Players = append(snap.Players, game.PlayerState{
			UID:          p.Uid,
			Name:         p.Name,
			Seat:         int(p.Seat),
			IsAI:         p.IsAi,
			TimeoutCount: int(p.TimeoutCount),
			Online:       p.Online,
		})
	}
	for _, col := range m.Moves {
		snap.Moves = append(snap.Moves, int(col))
	}
	return snap
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
