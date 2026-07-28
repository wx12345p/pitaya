package services

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"connect4/game"
	"connect4/protos"
	"connect4/store"

	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/component"
)

// roomLingerAfterOver 对局结束后房间在内存中保留多久(便于客户端补拉终局状态)
const roomLingerAfterOver = 5 * time.Second

// GameHandler 对局服务: 只处理本节点作为 owner 的房间
type GameHandler struct {
	component.Base
	app   pitaya.Pitaya
	rooms *game.RoomManager
	store *store.RedisStore // 可为 nil(未启用 Redis, 无快照/无恢复)
}

// NewGameHandler 创建对局服务
func NewGameHandler(app pitaya.Pitaya, st *store.RedisStore) *GameHandler {
	return &GameHandler{app: app, rooms: game.NewRoomManager(), store: st}
}

// Init 初始化
func (h *GameHandler) Init() {
	log.Printf("[Game] 初始化完成 (Redis=%v)", h.store != nil)
}

// FlushAll 优雅关机时把本节点所有房间快照落盘
func (h *GameHandler) FlushAll() {
	if h.store == nil {
		return
	}
	rooms := h.rooms.Rooms()
	for _, room := range rooms {
		if err := h.store.SaveSnapshot(context.Background(), room.ToSnapshot()); err != nil {
			log.Printf("[Game] 关机快照失败 room=%s: %v", room.ID, err)
		}
	}
	log.Printf("[Game] 关机 flush 完成, 房间数 %d", len(rooms))
}

// ==================== Handler: 客户端请求 ====================

// Drop 落子(路由 game.game.drop)
func (h *GameHandler) Drop(ctx context.Context, req *protos.DropRequest) (*protos.AckResponse, error) {
	room, uid := h.locate(ctx)
	if room == nil {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_NOT_IN_ROOM, Msg: "你不在任何对局中"}, nil
	}
	if err := room.HandleDrop(uid, int(req.Col)); err != nil {
		code := protos.ResultCode_RESULT_BAD_TURN
		switch {
		case errors.Is(err, game.ErrBadCol):
			code = protos.ResultCode_RESULT_BAD_COL
		case errors.Is(err, game.ErrColFull):
			code = protos.ResultCode_RESULT_COL_FULL
		}
		return &protos.AckResponse{Code: code, Msg: err.Error()}, nil
	}
	return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "ok"}, nil
}

// Watch 注册观战并返回全量状态(路由 game.game.watch)
func (h *GameHandler) Watch(ctx context.Context, req *protos.WatchRequest) (*protos.GameStateResponse, error) {
	room, uid := h.locate(ctx)
	if room == nil {
		return &protos.GameStateResponse{Code: protos.ResultCode_RESULT_ROOM_NOT_FOUND, Msg: "房间不存在"}, nil
	}
	room.AddSpectator(uid)
	return h.buildGameState(room, uid), nil
}

// GetGameState 拉取全量对局状态(路由 game.game.getgamestate)
func (h *GameHandler) GetGameState(ctx context.Context, req *protos.GameStateRequest) (*protos.GameStateResponse, error) {
	room, uid := h.locate(ctx)
	if room == nil {
		return &protos.GameStateResponse{Code: protos.ResultCode_RESULT_NOT_IN_ROOM, Msg: "你不在任何对局中"}, nil
	}
	return h.buildGameState(room, uid), nil
}

// locate 依据会话中的 roomId 定位本节点房间
func (h *GameHandler) locate(ctx context.Context) (*game.Room, string) {
	s := h.app.GetSessionFromCtx(ctx)
	if s == nil || s.UID() == "" {
		return nil, ""
	}
	roomID, _ := s.Get("roomId").(string)
	if roomID == "" {
		return nil, s.UID()
	}
	return h.rooms.GetRoom(roomID), s.UID()
}

// ==================== GameRemote: 服务器间 RPC ====================

// GameRemote 对局服务的内部 RPC(非客户端可见)
type GameRemote struct {
	component.Base
	h *GameHandler
}

// CreateRoom lobby -> game: 建房并开局
// 路由: game.gameremote.createroom
func (gr *GameRemote) CreateRoom(ctx context.Context, req *protos.CreateRoomRequest) (*protos.AckResponse, error) {
	members := make([]game.Member, 0, len(req.Members))
	for _, m := range req.Members {
		members = append(members, game.Member{UID: m.Uid, Name: m.Name, Seat: int(m.Seat)})
	}
	room, created := gr.h.rooms.CreateRoom(req.RoomId, members, req.WithAi, int(req.FirstSeat))
	if !created {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "房间已存在"}, nil
	}
	gr.h.setupRoom(room)
	log.Printf("[Game] 建房 %s 成员%d AI补位=%v 先手座位%d", req.RoomId, len(members), req.WithAi, req.FirstSeat)
	go room.StartGame()
	return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "ok"}, nil
}

// RestoreRoom lobby -> game: 从 Redis 快照恢复房间并续跑
// 路由: game.gameremote.restoreroom
func (gr *GameRemote) RestoreRoom(ctx context.Context, req *protos.RestoreRoomRequest) (*protos.AckResponse, error) {
	h := gr.h
	if h.store == nil {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "未启用快照"}, nil
	}
	if room := h.rooms.GetRoom(req.RoomId); room != nil {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "房间已存在"}, nil
	}
	snap, err := h.store.LoadSnapshot(ctx, req.RoomId)
	if err != nil || snap == nil {
		log.Printf("[Game] 恢复失败 room=%s: 快照缺失(%v)", req.RoomId, err)
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "快照缺失"}, nil
	}
	room, created := h.rooms.RestoreRoom(*snap)
	if !created {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "房间已存在"}, nil
	}
	h.setupRoom(room)
	log.Printf("[Game] 恢复房间 %s (第%d手, 当前座位%d), 续跑", req.RoomId, snap.MoveNo, snap.CurrentSeat)
	h.onGameStart(room, true) // 先全量重同步, 再续跑当前回合
	go room.Resume()
	return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "ok"}, nil
}

// PlayerConn connector/lobby -> game: 通报玩家连接状态变化
// 路由: game.gameremote.playerconn
func (gr *GameRemote) PlayerConn(ctx context.Context, req *protos.PlayerConnRequest) (*protos.PlayerConnResponse, error) {
	room := gr.h.rooms.GetRoom(req.RoomId)
	if room == nil {
		return &protos.PlayerConnResponse{Code: protos.ResultCode_RESULT_ROOM_NOT_FOUND, Msg: "房间不存在", Seat: -1}, nil
	}
	if seat := room.SeatOf(req.Uid); seat >= 0 {
		room.SetOnline(req.Uid, req.Online)
		return &protos.PlayerConnResponse{
			Code: protos.ResultCode_RESULT_OK, Msg: "ok",
			Seat: int32(seat), Role: protos.PlayerRole_ROLE_PLAYER,
		}, nil
	}
	if !req.Online {
		room.RemoveSpectator(req.Uid)
	}
	return &protos.PlayerConnResponse{
		Code: protos.ResultCode_RESULT_OK, Msg: "ok",
		Seat: -1, Role: protos.PlayerRole_ROLE_SPECTATOR,
	}, nil
}

// ==================== 房间回调 -> 推送 ====================

func (h *GameHandler) setupRoom(room *game.Room) {
	room.OnGameStart = h.onGameStart
	room.OnTurnStart = h.onTurnStart
	room.OnPieceDropped = h.onPieceDropped
	room.OnGameOver = h.onGameOver
	room.OnPlayerStatus = h.onPlayerStatus
	room.OnSpectatorUpdate = h.onSpectatorUpdate
	room.OnSnapshot = h.onSnapshot
}

func (h *GameHandler) onGameStart(room *game.Room, resync bool) {
	info := room.Info()
	h.pushToRoom(room, "onGameStart", &protos.GameStartPush{
		RoomId:         room.ID,
		Cols:           game.Cols,
		Rows:           game.Rows,
		WinLen:         game.WinLen,
		Players:        buildPlayerInfos(info),
		FirstSeat:      int32(info.FirstSeat),
		TurnTimeoutSec: int32(game.TurnTimeout / time.Second),
		Board:          buildBoard(info),
		CurrentSeat:    int32(info.CurrentSeat),
		MoveNo:         int32(info.MoveNo),
		Resync:         resync,
	})
	if h.store != nil && !resync {
		if err := h.store.AddLiveRoom(context.Background(), room.ID, info.StartedAt); err != nil {
			log.Printf("[Game] 登记观战列表失败 room=%s: %v", room.ID, err)
		}
	}
}

func (h *GameHandler) onTurnStart(room *game.Room, seat int, uid string, moveNo int, deadlineMs int64) {
	h.pushToRoom(room, "onTurnStart", &protos.TurnStartPush{
		Uid:            uid,
		Seat:           int32(seat),
		MoveNo:         int32(moveNo),
		DeadlineUnixMs: deadlineMs,
	})
}

func (h *GameHandler) onPieceDropped(room *game.Room, seat, col, row, moveNo int, byTimeout bool) {
	h.pushToRoom(room, "onPieceDropped", &protos.PieceDroppedPush{
		Uid:       uidBySeat(room, seat),
		Seat:      int32(seat),
		Col:       int32(col),
		Row:       int32(row),
		Piece:     protos.Piece(game.PieceOfSeat(seat)),
		MoveNo:    int32(moveNo),
		ByTimeout: byTimeout,
	})
}

func (h *GameHandler) onGameOver(room *game.Room) {
	info := room.Info()
	winnerName := ""
	for _, p := range info.Players {
		if p.Seat == info.WinnerSeat {
			winnerName = p.Name
		}
	}
	h.pushToRoom(room, "onGameOver", &protos.GameOverPush{
		WinnerUid:   info.WinnerUID,
		WinnerSeat:  int32(info.WinnerSeat),
		WinnerName:  winnerName,
		Reason:      protos.EndReason(info.Reason),
		WinningLine: buildCells(info.WinLine),
		Board:       buildBoard(info),
	})

	if h.store != nil {
		uids := make([]string, 0, len(info.Players))
		for _, p := range info.Players {
			uids = append(uids, p.UID)
		}
		if err := h.store.DeleteRoom(context.Background(), room.ID, uids); err != nil {
			log.Printf("[Game] 清理 Redis 失败 room=%s: %v", room.ID, err)
		}
	}
	go func() {
		time.Sleep(roomLingerAfterOver)
		h.rooms.RemoveRoom(room.ID)
	}()
}

func (h *GameHandler) onPlayerStatus(room *game.Room, seat int, uid string, online bool, graceSeconds int) {
	route := "onOpponentOffline"
	if online {
		route = "onOpponentOnline"
	}
	targets := make([]string, 0, 2)
	for _, u := range room.AudienceUIDs() {
		if u != uid {
			targets = append(targets, u)
		}
	}
	h.pushToUIDs(targets, route, &protos.OpponentStatusPush{
		Uid:          uid,
		Seat:         int32(seat),
		Online:       online,
		GraceSeconds: int32(graceSeconds),
	})
}

func (h *GameHandler) onSpectatorUpdate(room *game.Room, count int) {
	h.pushToRoom(room, "onSpectatorUpdate", &protos.SpectatorUpdatePush{
		RoomId: room.ID,
		Count:  int32(count),
	})
}

// onSnapshot 每步落子后异步写快照, 不阻塞对局
func (h *GameHandler) onSnapshot(room *game.Room) {
	if h.store == nil {
		return
	}
	snap := room.ToSnapshot()
	go func() {
		if err := h.store.SaveSnapshot(context.Background(), snap); err != nil {
			log.Printf("[Game] 快照写入失败 room=%s: %v", snap.RoomID, err)
		}
	}()
}

// ==================== 构造与推送辅助 ====================

func (h *GameHandler) buildGameState(room *game.Room, uid string) *protos.GameStateResponse {
	info := room.Info()
	role := protos.PlayerRole_ROLE_SPECTATOR
	for _, p := range info.Players {
		if p.UID == uid {
			role = protos.PlayerRole_ROLE_PLAYER
		}
	}
	return &protos.GameStateResponse{
		Code:           protos.ResultCode_RESULT_OK,
		Msg:            "ok",
		RoomId:         room.ID,
		State:          protos.RoomState(info.State),
		WinLen:         game.WinLen,
		Players:        buildPlayerInfos(info),
		Board:          buildBoard(info),
		CurrentSeat:    int32(info.CurrentSeat),
		MoveNo:         int32(info.MoveNo),
		Role:           role,
		Spectators:     int32(info.Spectators),
		TurnTimeoutSec: int32(game.TurnTimeout / time.Second),
		DeadlineUnixMs: info.DeadlineMs,
		WinnerUid:      info.WinnerUID,
		WinnerSeat:     int32(info.WinnerSeat),
		Reason:         protos.EndReason(info.Reason),
		WinningLine:    buildCells(info.WinLine),
	}
}

func (h *GameHandler) pushToRoom(room *game.Room, route string, msg interface{}) {
	h.pushToUIDs(room.AudienceUIDs(), route, msg)
}

func (h *GameHandler) pushToUIDs(uids []string, route string, msg interface{}) {
	if len(uids) == 0 {
		return
	}
	if _, err := h.app.SendPushToUsers(route, msg, uids, "connector"); err != nil {
		log.Printf("[Game] 推送 %s 失败: %v", route, err)
	}
}

func buildPlayerInfos(info game.Info) []*protos.PlayerInfo {
	res := make([]*protos.PlayerInfo, 0, len(info.Players))
	for _, p := range info.Players {
		res = append(res, &protos.PlayerInfo{
			Uid:          p.UID,
			Name:         p.Name,
			Seat:         int32(p.Seat),
			Piece:        protos.Piece(game.PieceOfSeat(p.Seat)),
			IsAi:         p.IsAI,
			Online:       p.Online,
			TimeoutCount: int32(p.TimeoutCount),
		})
	}
	return res
}

func buildBoard(info game.Info) *protos.BoardState {
	return &protos.BoardState{Cols: game.Cols, Rows: game.Rows, Cells: info.Cells}
}

func buildCells(line []game.Cell) []*protos.Cell {
	if len(line) == 0 {
		return nil
	}
	res := make([]*protos.Cell, 0, len(line))
	for _, c := range line {
		res = append(res, &protos.Cell{Col: int32(c.Col), Row: int32(c.Row)})
	}
	return res
}

func uidBySeat(room *game.Room, seat int) string {
	for _, p := range room.Info().Players {
		if p.Seat == seat {
			return p.UID
		}
	}
	return ""
}

// RegisterGameServices 注册对局服务(客户端 handler + 服务器间 remote)
// 返回 handler 以便 main 在优雅关机时 flush 快照。
func RegisterGameServices(app pitaya.Pitaya, st *store.RedisStore) *GameHandler {
	handler := NewGameHandler(app, st)
	app.Register(handler,
		component.WithName("game"),
		component.WithNameFunc(strings.ToLower),
	)
	app.RegisterRemote(&GameRemote{h: handler},
		component.WithName("gameremote"),
		component.WithNameFunc(strings.ToLower),
	)
	return handler
}
