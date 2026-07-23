package services

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"richman/game"
	"richman/protos"
	"richman/routing"
	"richman/store"

	"github.com/google/uuid"
	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/component"
	"github.com/topfreegames/pitaya/v2/session"
)

// waitGroup 一个正在匹配中的等待组(尚未满员)
type waitGroup struct {
	roomID  string
	owner   string // 建组时用 rendezvous 哈希选定的 owner game 节点
	members []game.Member
}

// Lobby 匹配服务(单实例): 负责全局匹配、分配 roomId+座位, 并把 owner 写入 session
type Lobby struct {
	component.Base
	app   pitaya.Pitaya
	store *store.RedisStore // 可为 nil
	mu    sync.Mutex
	cur   *waitGroup // 当前开放的等待组; 满员后置空轮换
}

// NewLobby 创建匹配服务
func NewLobby(app pitaya.Pitaya, st *store.RedisStore) *Lobby {
	return &Lobby{app: app, store: st}
}

// Init 初始化
func (l *Lobby) Init() {
	log.Println("[Lobby] 初始化完成")
}

// Join 匹配进房(路由 lobby.lobby.join)
// 启用 Redis 时走共享队列(lobby 可水平扩容); 否则退化为单实例内存匹配。
func (l *Lobby) Join(ctx context.Context, req *protos.JoinRoomRequest) (*protos.JoinRoomResponse, error) {
	s := l.app.GetSessionFromCtx(ctx)
	uid := s.UID()
	if uid == "" {
		return &protos.JoinRoomResponse{Code: protos.ResultCode_RESULT_NOT_LOGIN, Msg: "请先登录"}, nil
	}
	name := req.PlayerName
	if name == "" {
		name = "玩家" + uid[:6]
	}
	if l.store != nil {
		return l.joinDistributed(ctx, s, uid, name)
	}
	return l.joinInMemory(ctx, s, uid, name)
}

// joinDistributed 基于 Redis 共享队列的分布式匹配(无本地状态, lobby 可多实例)
func (l *Lobby) joinDistributed(ctx context.Context, s session.Session, uid, name string) (*protos.JoinRoomResponse, error) {
	servers, err := l.app.GetServersByType("game")
	if err != nil || len(servers) == 0 {
		log.Printf("[Lobby] 无可用 game 节点: %v", err)
		return &protos.JoinRoomResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "暂无可用游戏服务器"}, nil
	}
	// 候选 roomId/owner: 仅当脚本需要"开新房"时被采用
	candidateRoomID := uuid.New().String()[:8]
	candidateOwner, ok := routing.OwnerServerID(candidateRoomID, servers)
	if !ok {
		return &protos.JoinRoomResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "选取游戏服务器失败"}, nil
	}

	roomID, seat, full, owner, err := l.store.MatchJoin(ctx, uid, name, game.MaxPlayers, candidateRoomID, candidateOwner)
	if err != nil {
		log.Printf("[Lobby] 分布式匹配失败: %v", err)
		return &protos.JoinRoomResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "匹配失败"}, nil
	}

	// 写入本玩家 session(每次 join 处理本玩家会话, 天然分布式)
	_ = s.Set("roomId", roomID)
	_ = s.Set("gameServer", owner)
	if err := s.PushToFront(ctx); err != nil {
		log.Printf("[Lobby] PushToFront 失败(uid=%s): %v", uid, err)
	}

	// 读取当前花名册, 推送人数更新; 满员则建房
	members, err := l.store.MatchMembers(ctx, roomID)
	if err != nil {
		log.Printf("[Lobby] 读取花名册失败 room=%s: %v", roomID, err)
	}
	l.pushRoomUpdate(roomID, members)
	if full {
		l.createRoom(ctx, roomID, owner, members)
		if err := l.store.MatchCleanup(ctx, roomID); err != nil {
			log.Printf("[Lobby] 清理匹配态失败 room=%s: %v", roomID, err)
		}
	}

	log.Printf("[Lobby] %s(%s) 匹配进房 %s 座位%d owner=%s 满员=%v (分布式)", name, uid, roomID, seat, owner, full)
	return &protos.JoinRoomResponse{Code: protos.ResultCode_RESULT_OK, Msg: "已匹配", RoomId: roomID, Seat: int32(seat)}, nil
}

// joinInMemory 单实例内存匹配(Redis 未启用时的降级路径)
func (l *Lobby) joinInMemory(ctx context.Context, s session.Session, uid, name string) (*protos.JoinRoomResponse, error) {
	l.mu.Lock()
	if l.cur == nil || len(l.cur.members) >= game.MaxPlayers {
		servers, err := l.app.GetServersByType("game")
		if err != nil || len(servers) == 0 {
			l.mu.Unlock()
			log.Printf("[Lobby] 无可用 game 节点: %v", err)
			return &protos.JoinRoomResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "暂无可用游戏服务器"}, nil
		}
		roomID := uuid.New().String()[:8]
		owner, ok := routing.OwnerServerID(roomID, servers)
		if !ok {
			l.mu.Unlock()
			return &protos.JoinRoomResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "选取游戏服务器失败"}, nil
		}
		l.cur = &waitGroup{roomID: roomID, owner: owner}
	}
	g := l.cur
	seat := len(g.members)
	g.members = append(g.members, game.Member{UID: uid, Name: name, Seat: seat})
	roomID := g.roomID
	owner := g.owner
	members := append([]game.Member(nil), g.members...)
	full := len(g.members) >= game.MaxPlayers
	if full {
		l.cur = nil
	}
	l.mu.Unlock()

	// 写入本玩家 session: roomId + owner(gameServer), 同步到 connector 供后续 game.* 粘性路由
	_ = s.Set("roomId", roomID)
	_ = s.Set("gameServer", owner)
	if err := s.PushToFront(ctx); err != nil {
		log.Printf("[Lobby] PushToFront 失败(uid=%s): %v", uid, err)
	}

	// 通知已匹配玩家当前人数
	l.pushRoomUpdate(roomID, members)

	// 满员 -> 请求 owner game 节点建房并开局
	if full {
		l.createRoom(ctx, roomID, owner, members)
	}

	log.Printf("[Lobby] %s(%s) 匹配进房 %s 座位%d owner=%s 满员=%v", name, uid, roomID, seat, owner, full)
	return &protos.JoinRoomResponse{Code: protos.ResultCode_RESULT_OK, Msg: "已匹配", RoomId: roomID, Seat: int32(seat)}, nil
}

// pushRoomUpdate 向等待组成员推送房间人数更新
func (l *Lobby) pushRoomUpdate(roomID string, members []game.Member) {
	players := make([]*protos.PlayerInfo, 0, len(members))
	uids := make([]string, 0, len(members))
	for _, m := range members {
		players = append(players, &protos.PlayerInfo{
			Uid:  m.UID,
			Name: m.Name,
			Seat: int32(m.Seat),
			Money: game.InitMoney,
		})
		uids = append(uids, m.UID)
	}
	need := int32(game.MaxPlayers - len(members))
	if need < 0 {
		need = 0
	}
	if _, err := l.app.SendPushToUsers("onRoomUpdate", &protos.RoomUpdatePush{
		RoomId:  roomID,
		Players: players,
		Need:    need,
	}, uids, "connector"); err != nil {
		log.Printf("[Lobby] onRoomUpdate 推送失败: %v", err)
	}
}

// createRoom 通过内部 RPC 请求 owner game 节点建房; 并把 owner 写入 Redis 目录
func (l *Lobby) createRoom(ctx context.Context, roomID, owner string, members []game.Member) {
	if l.store != nil {
		if err := l.store.PutOwner(ctx, roomID, owner); err != nil {
			log.Printf("[Lobby] 写 owner 目录失败 room=%s: %v", roomID, err)
		}
	}
	pm := make([]*protos.RoomMember, 0, len(members))
	for _, m := range members {
		pm = append(pm, &protos.RoomMember{Uid: m.UID, Name: m.Name, Seat: int32(m.Seat)})
	}
	reply := &protos.AckResponse{}
	if err := l.app.RPCTo(ctx, owner, "game.gameremote.createroom", reply, &protos.CreateRoomRequest{
		RoomId:  roomID,
		Members: pm,
	}); err != nil {
		log.Printf("[Lobby] RPCTo(%s) 建房失败: %v", owner, err)
	}
}

// LobbyRemote 匹配服务的服务器间 RPC(供 connector 路由在 owner 失联时调用)
type LobbyRemote struct {
	component.Base
	app   pitaya.Pitaya
	store *store.RedisStore
}

// ResolveOwner connector -> lobby: 查询 roomId 的 owner; 若 owner 已失联则从快照重指派恢复
// 路由: lobby.lobbyremote.resolveowner
func (lr *LobbyRemote) ResolveOwner(ctx context.Context, req *protos.ResolveOwnerRequest) (*protos.ResolveOwnerResponse, error) {
	roomID := req.RoomId
	if lr.store == nil {
		return &protos.ResolveOwnerResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "未启用恢复"}, nil
	}
	servers, err := lr.app.GetServersByType("game")
	if err != nil || len(servers) == 0 {
		return &protos.ResolveOwnerResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "无可用 game 节点"}, nil
	}

	// 1) 目录中的 owner 仍存活 -> 直接返回(可能只是 connector 视图陈旧)
	if owner, _ := lr.store.GetOwner(ctx, roomID); owner != "" {
		if _, alive := servers[owner]; alive {
			return &protos.ResolveOwnerResponse{Code: protos.ResultCode_RESULT_OK, Owner: owner}, nil
		}
	}

	// 2) owner 失联 -> 抢恢复锁进行重指派
	got, _ := lr.store.AcquireRecoveryLock(ctx, roomID, "lobby", 10*time.Second)
	if !got {
		// 另一恢复进行中, 轮询等待 owner 更新
		for i := 0; i < 20; i++ {
			time.Sleep(150 * time.Millisecond)
			if owner, _ := lr.store.GetOwner(ctx, roomID); owner != "" {
				if _, alive := servers[owner]; alive {
					return &protos.ResolveOwnerResponse{Code: protos.ResultCode_RESULT_OK, Owner: owner}, nil
				}
			}
		}
		return &protos.ResolveOwnerResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "恢复中, 请重试"}, nil
	}
	defer lr.store.ReleaseRecoveryLock(ctx, roomID)

	// 双检: 抢锁期间可能已被恢复
	if owner, _ := lr.store.GetOwner(ctx, roomID); owner != "" {
		if _, alive := servers[owner]; alive {
			return &protos.ResolveOwnerResponse{Code: protos.ResultCode_RESULT_OK, Owner: owner}, nil
		}
	}

	newOwner, ok := routing.OwnerServerID(roomID, servers)
	if !ok {
		return &protos.ResolveOwnerResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "选取节点失败"}, nil
	}
	reply := &protos.AckResponse{}
	if err := lr.app.RPCTo(ctx, newOwner, "game.gameremote.restoreroom", reply, &protos.RestoreRoomRequest{RoomId: roomID}); err != nil {
		log.Printf("[LobbyRemote] 触发恢复失败 room=%s owner=%s: %v", roomID, newOwner, err)
		return &protos.ResolveOwnerResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "恢复失败"}, nil
	}
	if reply.Code != protos.ResultCode_RESULT_OK {
		return &protos.ResolveOwnerResponse{Code: reply.Code, Msg: reply.Msg}, nil
	}
	if err := lr.store.PutOwner(ctx, roomID, newOwner); err != nil {
		log.Printf("[LobbyRemote] 更新 owner 目录失败 room=%s: %v", roomID, err)
	}
	log.Printf("[LobbyRemote] room=%s 重指派 owner -> %s", roomID, newOwner)
	return &protos.ResolveOwnerResponse{Code: protos.ResultCode_RESULT_OK, Owner: newOwner}, nil
}

// RegisterLobbyServices 注册匹配服务(客户端 handler + 服务器间 remote)
func RegisterLobbyServices(app pitaya.Pitaya, st *store.RedisStore) {
	app.Register(NewLobby(app, st),
		component.WithName("lobby"),
		component.WithNameFunc(strings.ToLower),
	)
	app.RegisterRemote(&LobbyRemote{app: app, store: st},
		component.WithName("lobbyremote"),
		component.WithNameFunc(strings.ToLower),
	)
}
