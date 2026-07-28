package services

import (
	"context"
	"errors"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"connect4/game"
	"connect4/protos"
	"connect4/routing"
	"connect4/store"

	"github.com/google/uuid"
	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/component"
	"github.com/topfreegames/pitaya/v2/session"
)

// AIFillWait 匹配等待多久没有真人对手就由 AI 补位
const AIFillWait = 10 * time.Second

// memWaiting 内存降级路径下的等待组(Redis 未启用时使用)
type memWaiting struct {
	roomID  string
	owner   string
	members []game.Member
	timer   *time.Timer
}

// Lobby 匹配服务。启用 Redis 时无本地状态, 可水平扩容;
// 未启用 Redis 时退化为单实例内存撮合。
type Lobby struct {
	component.Base
	app     pitaya.Pitaya
	store   *store.RedisStore
	mu      sync.Mutex
	waiting *memWaiting
}

// NewLobby 创建匹配服务
func NewLobby(app pitaya.Pitaya, st *store.RedisStore) *Lobby {
	return &Lobby{app: app, store: st}
}

// Init 初始化
func (l *Lobby) Init() {
	log.Printf("[Lobby] 初始化完成 (Redis=%v)", l.store != nil)
}

// Match 请求匹配对手(路由 lobby.lobby.match)
func (l *Lobby) Match(ctx context.Context, req *protos.MatchRequest) (*protos.MatchResponse, error) {
	s := l.app.GetSessionFromCtx(ctx)
	uid := s.UID()
	if uid == "" {
		return &protos.MatchResponse{Code: protos.ResultCode_RESULT_NOT_LOGIN, Msg: "请先登录"}, nil
	}
	name := playerName(s, uid)
	if l.store != nil {
		return l.matchDistributed(ctx, s, uid, name)
	}
	return l.matchInMemory(ctx, s, uid, name)
}

// matchDistributed 基于 Redis 共享队列的撮合(lobby 可多实例)
func (l *Lobby) matchDistributed(ctx context.Context, s session.Session, uid, name string) (*protos.MatchResponse, error) {
	// 已在对局中: 幂等返回原房间
	if roomID, err := l.store.GetPlayerRoom(ctx, uid); err == nil && roomID != "" {
		if snap, err := l.store.LoadSnapshot(ctx, roomID); err == nil && snap != nil && snap.State == game.StatePlaying {
			if owner, err := resolveOwner(ctx, l.app, l.store, roomID); err == nil {
				l.bindSession(ctx, s, roomID, owner, protos.PlayerRole_ROLE_PLAYER)
				return &protos.MatchResponse{
					Code: protos.ResultCode_RESULT_OK, Msg: "你已在对局中",
					RoomId: roomID, Seat: int32(seatInSnapshot(snap, uid)), Matched: true,
				}, nil
			}
		}
	}

	servers, err := l.app.GetServersByType("game")
	if err != nil || len(servers) == 0 {
		log.Printf("[Lobby] 无可用 game 节点: %v", err)
		return &protos.MatchResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "暂无可用对局服务器"}, nil
	}
	// 候选房间/owner: 仅当撮合脚本需要"开新房"时才被采用
	candRoomID := uuid.New().String()[:8]
	candOwner, ok := routing.OwnerServerID(candRoomID, servers)
	if !ok {
		return &protos.MatchResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "选取对局服务器失败"}, nil
	}

	roomID, seat, full, owner, err := l.store.MatchJoin(ctx, uid, name, game.MaxPlayers, candRoomID, candOwner)
	if err != nil {
		log.Printf("[Lobby] 撮合失败: %v", err)
		return &protos.MatchResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "匹配失败"}, nil
	}
	l.bindSession(ctx, s, roomID, owner, protos.PlayerRole_ROLE_PLAYER)

	if full {
		members, err := l.store.MatchMembers(ctx, roomID)
		if err != nil {
			log.Printf("[Lobby] 读取花名册失败 room=%s: %v", roomID, err)
		}
		l.createRoom(context.Background(), roomID, owner, members, false)
		if err := l.store.MatchCleanup(ctx, roomID); err != nil {
			log.Printf("[Lobby] 清理撮合中间态失败 room=%s: %v", roomID, err)
		}
		log.Printf("[Lobby] %s(%s) 匹配成功 room=%s 座位%d owner=%s", name, uid, roomID, seat, owner)
		return &protos.MatchResponse{
			Code: protos.ResultCode_RESULT_OK, Msg: "匹配成功",
			RoomId: roomID, Seat: int32(seat), Matched: true,
		}, nil
	}

	l.pushMatchUpdate([]string{uid}, protos.MatchState_MATCH_QUEUING, 0, "正在寻找对手")
	go l.aiFillAfterWait(roomID, owner)
	log.Printf("[Lobby] %s(%s) 入队等待 room=%s 座位%d owner=%s", name, uid, roomID, seat, owner)
	return &protos.MatchResponse{
		Code: protos.ResultCode_RESULT_OK, Msg: "已进入匹配队列",
		RoomId: roomID, Seat: int32(seat), Matched: false,
	}, nil
}

// aiFillAfterWait 等待超时后抢占该房间, 用 AI 补位开局
func (l *Lobby) aiFillAfterWait(roomID, owner string) {
	time.Sleep(AIFillWait)
	ctx := context.Background()
	claimed, err := l.store.MatchClaimForAI(ctx, roomID, game.MaxPlayers)
	if err != nil {
		log.Printf("[Lobby] AI 补位抢占失败 room=%s: %v", roomID, err)
		return
	}
	if !claimed {
		return // 已匹配到真人对手, 或已被其他 lobby 处理
	}
	members, err := l.store.MatchMembers(ctx, roomID)
	if err != nil || len(members) == 0 {
		log.Printf("[Lobby] AI 补位读取花名册失败 room=%s: %v", roomID, err)
		return
	}
	l.createRoom(ctx, roomID, owner, members, true)
	if err := l.store.MatchCleanup(ctx, roomID); err != nil {
		log.Printf("[Lobby] 清理撮合中间态失败 room=%s: %v", roomID, err)
	}
	uids := make([]string, 0, len(members))
	for _, m := range members {
		uids = append(uids, m.UID)
	}
	l.pushMatchUpdate(uids, protos.MatchState_MATCH_AI_FILLED, int32(AIFillWait/time.Second), "未找到真人对手, 已匹配机器人")
	log.Printf("[Lobby] room=%s 等待超时, AI 补位开局", roomID)
}

// matchInMemory 单实例内存撮合(Redis 未启用时的降级路径)
func (l *Lobby) matchInMemory(ctx context.Context, s session.Session, uid, name string) (*protos.MatchResponse, error) {
	l.mu.Lock()
	if l.waiting == nil {
		servers, err := l.app.GetServersByType("game")
		if err != nil || len(servers) == 0 {
			l.mu.Unlock()
			return &protos.MatchResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "暂无可用对局服务器"}, nil
		}
		roomID := uuid.New().String()[:8]
		owner, ok := routing.OwnerServerID(roomID, servers)
		if !ok {
			l.mu.Unlock()
			return &protos.MatchResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "选取对局服务器失败"}, nil
		}
		l.waiting = &memWaiting{roomID: roomID, owner: owner}
		l.waiting.timer = time.AfterFunc(AIFillWait, func() { l.memAIFill(roomID) })
	}
	g := l.waiting
	seat := len(g.members)
	g.members = append(g.members, game.Member{UID: uid, Name: name, Seat: seat})
	roomID, owner := g.roomID, g.owner
	members := append([]game.Member(nil), g.members...)
	full := len(g.members) >= game.MaxPlayers
	if full {
		if g.timer != nil {
			g.timer.Stop()
		}
		l.waiting = nil
	}
	l.mu.Unlock()

	l.bindSession(ctx, s, roomID, owner, protos.PlayerRole_ROLE_PLAYER)
	if full {
		l.createRoom(context.Background(), roomID, owner, members, false)
		return &protos.MatchResponse{
			Code: protos.ResultCode_RESULT_OK, Msg: "匹配成功",
			RoomId: roomID, Seat: int32(seat), Matched: true,
		}, nil
	}
	l.pushMatchUpdate([]string{uid}, protos.MatchState_MATCH_QUEUING, 0, "正在寻找对手")
	return &protos.MatchResponse{
		Code: protos.ResultCode_RESULT_OK, Msg: "已进入匹配队列",
		RoomId: roomID, Seat: int32(seat), Matched: false,
	}, nil
}

// memAIFill 内存路径的 AI 补位
func (l *Lobby) memAIFill(roomID string) {
	l.mu.Lock()
	g := l.waiting
	if g == nil || g.roomID != roomID || len(g.members) == 0 {
		l.mu.Unlock()
		return
	}
	members := append([]game.Member(nil), g.members...)
	owner := g.owner
	l.waiting = nil
	l.mu.Unlock()

	l.createRoom(context.Background(), roomID, owner, members, true)
	uids := make([]string, 0, len(members))
	for _, m := range members {
		uids = append(uids, m.UID)
	}
	l.pushMatchUpdate(uids, protos.MatchState_MATCH_AI_FILLED, int32(AIFillWait/time.Second), "未找到真人对手, 已匹配机器人")
}

// CancelMatch 取消匹配(路由 lobby.lobby.cancelmatch)
func (l *Lobby) CancelMatch(ctx context.Context, req *protos.CancelMatchRequest) (*protos.AckResponse, error) {
	s := l.app.GetSessionFromCtx(ctx)
	uid := s.UID()
	if uid == "" {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_NOT_LOGIN, Msg: "请先登录"}, nil
	}
	roomID, _ := s.Get("roomId").(string)
	if roomID == "" {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "当前不在匹配中"}, nil
	}
	name := playerName(s, uid)

	left := false
	if l.store != nil {
		var err error
		if left, err = l.store.MatchLeave(ctx, roomID, uid, name); err != nil {
			log.Printf("[Lobby] 取消匹配失败 room=%s: %v", roomID, err)
		}
	} else {
		l.mu.Lock()
		if g := l.waiting; g != nil && g.roomID == roomID {
			for i, m := range g.members {
				if m.UID == uid {
					g.members = append(g.members[:i], g.members[i+1:]...)
					left = true
					break
				}
			}
			if len(g.members) == 0 {
				if g.timer != nil {
					g.timer.Stop()
				}
				l.waiting = nil
			}
		}
		l.mu.Unlock()
	}
	if !left {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "对局已开始, 无法取消"}, nil
	}
	l.clearSession(ctx, s)
	log.Printf("[Lobby] %s(%s) 取消匹配 room=%s", name, uid, roomID)
	return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "已取消匹配"}, nil
}

// Reconnect 断线重连: 按 uid 反查在局房间, 重建粘性路由并通报 game 节点
// (路由 lobby.lobby.reconnect)
func (l *Lobby) Reconnect(ctx context.Context, req *protos.ReconnectRequest) (*protos.ReconnectResponse, error) {
	s := l.app.GetSessionFromCtx(ctx)
	uid := s.UID()
	if uid == "" {
		return &protos.ReconnectResponse{Code: protos.ResultCode_RESULT_NOT_LOGIN, Msg: "请先登录"}, nil
	}
	if l.store == nil {
		return &protos.ReconnectResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "未启用 Redis, 不支持重连"}, nil
	}
	roomID, err := l.store.GetPlayerRoom(ctx, uid)
	if err != nil || roomID == "" {
		return &protos.ReconnectResponse{Code: protos.ResultCode_RESULT_NOT_IN_ROOM, Msg: "没有进行中的对局"}, nil
	}
	owner, err := resolveOwner(ctx, l.app, l.store, roomID)
	if err != nil {
		log.Printf("[Lobby] 重连定位 owner 失败 room=%s: %v", roomID, err)
		return &protos.ReconnectResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: err.Error()}, nil
	}

	reply := &protos.PlayerConnResponse{}
	if err := l.app.RPCTo(ctx, owner, "game.gameremote.playerconn", reply,
		&protos.PlayerConnRequest{RoomId: roomID, Uid: uid, Online: true}); err != nil {
		log.Printf("[Lobby] 通报重连失败 room=%s owner=%s: %v", roomID, owner, err)
		return &protos.ReconnectResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "重连失败"}, nil
	}
	if reply.Code != protos.ResultCode_RESULT_OK {
		return &protos.ReconnectResponse{Code: reply.Code, Msg: reply.Msg}, nil
	}
	l.bindSession(ctx, s, roomID, owner, reply.Role)

	log.Printf("[Lobby] %s 重连回房间 %s 座位%d", uid, roomID, reply.Seat)
	return &protos.ReconnectResponse{
		Code: protos.ResultCode_RESULT_OK, Msg: "重连成功",
		RoomId: roomID, Seat: reply.Seat, Role: reply.Role,
	}, nil
}

// RoomList 可观战房间列表(路由 lobby.lobby.roomlist)
func (l *Lobby) RoomList(ctx context.Context, req *protos.RoomListRequest) (*protos.RoomListResponse, error) {
	if l.store == nil {
		return &protos.RoomListResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "未启用 Redis, 不支持观战列表"}, nil
	}
	ids, err := l.store.ListLiveRooms(ctx, int(req.Limit))
	if err != nil {
		return &protos.RoomListResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "查询失败"}, nil
	}
	resp := &protos.RoomListResponse{Code: protos.ResultCode_RESULT_OK, Msg: "ok"}
	for _, id := range ids {
		snap, err := l.store.LoadSnapshot(ctx, id)
		if err != nil || snap == nil || snap.State != game.StatePlaying {
			continue
		}
		brief := &protos.RoomBrief{RoomId: id, MoveNo: int32(snap.MoveNo), StartedAt: snap.StartedAt}
		for _, p := range snap.Players {
			brief.Players = append(brief.Players, &protos.PlayerInfo{
				Uid: p.UID, Name: p.Name, Seat: int32(p.Seat),
				Piece: protos.Piece(game.PieceOfSeat(p.Seat)), IsAi: p.IsAI, Online: p.Online,
			})
		}
		resp.Rooms = append(resp.Rooms, brief)
	}
	return resp, nil
}

// Spectate 选择观战房间: 写入 session 粘性路由, 之后客户端发 game.game.watch 注册
// (路由 lobby.lobby.spectate)
func (l *Lobby) Spectate(ctx context.Context, req *protos.SpectateRequest) (*protos.AckResponse, error) {
	s := l.app.GetSessionFromCtx(ctx)
	if s.UID() == "" {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_NOT_LOGIN, Msg: "请先登录"}, nil
	}
	if req.RoomId == "" {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_ROOM_NOT_FOUND, Msg: "房间号为空"}, nil
	}
	owner, err := resolveOwner(ctx, l.app, l.store, req.RoomId)
	if err != nil {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_ROOM_NOT_FOUND, Msg: err.Error()}, nil
	}
	l.bindSession(ctx, s, req.RoomId, owner, protos.PlayerRole_ROLE_SPECTATOR)
	log.Printf("[Lobby] %s 观战房间 %s (owner=%s)", s.UID(), req.RoomId, owner)
	return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "ok"}, nil
}

// ==================== 内部辅助 ====================

// createRoom 请求 owner game 节点建房开局, 并写入 Redis 目录与玩家索引
func (l *Lobby) createRoom(ctx context.Context, roomID, owner string, members []game.Member, withAI bool) {
	if l.store != nil {
		if err := l.store.PutOwner(ctx, roomID, owner); err != nil {
			log.Printf("[Lobby] 写 owner 目录失败 room=%s: %v", roomID, err)
		}
		for _, m := range members {
			if err := l.store.BindPlayerRoom(ctx, m.UID, roomID); err != nil {
				log.Printf("[Lobby] 写玩家索引失败 uid=%s: %v", m.UID, err)
			}
		}
	}
	pm := make([]*protos.RoomMember, 0, len(members))
	for _, m := range members {
		pm = append(pm, &protos.RoomMember{Uid: m.UID, Name: m.Name, Seat: int32(m.Seat)})
	}
	reply := &protos.AckResponse{}
	err := l.app.RPCTo(ctx, owner, "game.gameremote.createroom", reply, &protos.CreateRoomRequest{
		RoomId:    roomID,
		Members:   pm,
		WithAi:    withAI,
		FirstSeat: int32(rand.Intn(game.MaxPlayers)),
	})
	if err != nil {
		log.Printf("[Lobby] RPCTo(%s) 建房失败 room=%s: %v", owner, roomID, err)
	}
}

// bindSession 写入房间粘性路由信息并同步到前端
func (l *Lobby) bindSession(ctx context.Context, s session.Session, roomID, owner string, role protos.PlayerRole) {
	_ = s.Set("roomId", roomID)
	_ = s.Set("gameServer", owner)
	_ = s.Set("role", int(role))
	if err := s.PushToFront(ctx); err != nil {
		log.Printf("[Lobby] PushToFront 失败 uid=%s: %v", s.UID(), err)
	}
}

// clearSession 清除房间信息(取消匹配时)
func (l *Lobby) clearSession(ctx context.Context, s session.Session) {
	_ = s.Set("roomId", "")
	_ = s.Set("gameServer", "")
	if err := s.PushToFront(ctx); err != nil {
		log.Printf("[Lobby] PushToFront 失败 uid=%s: %v", s.UID(), err)
	}
}

func (l *Lobby) pushMatchUpdate(uids []string, state protos.MatchState, waiting int32, msg string) {
	if len(uids) == 0 {
		return
	}
	push := &protos.MatchUpdatePush{State: state, WaitingSeconds: waiting, Msg: msg}
	if _, err := l.app.SendPushToUsers("onMatchUpdate", push, uids, "connector"); err != nil {
		log.Printf("[Lobby] onMatchUpdate 推送失败: %v", err)
	}
}

func playerName(s session.Session, uid string) string {
	if name, _ := s.Get("playerName").(string); name != "" {
		return name
	}
	return "玩家" + uid[:6]
}

func seatInSnapshot(snap *game.Snapshot, uid string) int {
	for _, p := range snap.Players {
		if p.UID == uid {
			return p.Seat
		}
	}
	return -1
}

// resolveOwner 定位房间 owner; owner 已失联时抢锁并在存活节点上从快照恢复。
func resolveOwner(ctx context.Context, app pitaya.Pitaya, st *store.RedisStore, roomID string) (string, error) {
	if st == nil {
		return "", errors.New("未启用 Redis, 无法定位房间")
	}
	servers, err := app.GetServersByType("game")
	if err != nil || len(servers) == 0 {
		return "", errors.New("无可用对局服务器")
	}
	owner, _ := st.GetOwner(ctx, roomID)
	if owner == "" {
		return "", errors.New("房间不存在或已结束")
	}
	if _, alive := servers[owner]; alive {
		return owner, nil
	}

	got, _ := st.AcquireRecoveryLock(ctx, roomID, "lobby", 10*time.Second)
	if !got {
		// 已有其他恢复在进行, 轮询等待目录更新
		for i := 0; i < 20; i++ {
			time.Sleep(150 * time.Millisecond)
			if o, _ := st.GetOwner(ctx, roomID); o != "" {
				if _, alive := servers[o]; alive {
					return o, nil
				}
			}
		}
		return "", errors.New("房间恢复中, 请稍后重试")
	}
	defer func() {
		if err := st.ReleaseRecoveryLock(ctx, roomID); err != nil {
			log.Printf("[Lobby] 释放恢复锁失败 room=%s: %v", roomID, err)
		}
	}()
	// 双检: 抢锁期间可能已被别人恢复
	if o, _ := st.GetOwner(ctx, roomID); o != "" {
		if _, alive := servers[o]; alive {
			return o, nil
		}
	}

	newOwner, ok := routing.OwnerServerID(roomID, servers)
	if !ok {
		return "", errors.New("选取对局服务器失败")
	}
	reply := &protos.AckResponse{}
	if err := app.RPCTo(ctx, newOwner, "game.gameremote.restoreroom", reply, &protos.RestoreRoomRequest{RoomId: roomID}); err != nil {
		return "", err
	}
	if reply.Code != protos.ResultCode_RESULT_OK {
		return "", errors.New(reply.Msg)
	}
	if err := st.PutOwner(ctx, roomID, newOwner); err != nil {
		log.Printf("[Lobby] 更新 owner 目录失败 room=%s: %v", roomID, err)
	}
	log.Printf("[Lobby] room=%s 重指派 owner -> %s", roomID, newOwner)
	return newOwner, nil
}

// LobbyRemote 供 connector 在 owner 失联时请求重指派
type LobbyRemote struct {
	component.Base
	app   pitaya.Pitaya
	store *store.RedisStore
}

// ResolveOwner 查询(必要时重指派并恢复)房间 owner
// 路由: lobby.lobbyremote.resolveowner
func (lr *LobbyRemote) ResolveOwner(ctx context.Context, req *protos.ResolveOwnerRequest) (*protos.ResolveOwnerResponse, error) {
	owner, err := resolveOwner(ctx, lr.app, lr.store, req.RoomId)
	if err != nil {
		return &protos.ResolveOwnerResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: err.Error()}, nil
	}
	return &protos.ResolveOwnerResponse{Code: protos.ResultCode_RESULT_OK, Owner: owner}, nil
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
