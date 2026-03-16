package services

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/topfreegames/pitaya/v3/examples/demo/landlord/game"
	"github.com/topfreegames/pitaya/v3/examples/demo/landlord/protos"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
)

// GameHandler 游戏后端Handler
type GameHandler struct {
	component.Base
	app         pitaya.Pitaya
	roomManager *game.RoomManager
	aiStrategy  *game.SimpleAI

	// 玩家UID -> 房间ID映射（方便查找）
	playerRooms sync.Map
}

// GameRemote 游戏后端Remote（接收来自Connector的RPC）
type GameRemote struct {
	component.Base
	app     pitaya.Pitaya
	handler *GameHandler
}

// NewGameHandler 创建游戏Handler
func NewGameHandler(app pitaya.Pitaya) *GameHandler {
	return &GameHandler{
		app:         app,
		roomManager: game.NewRoomManager(),
		aiStrategy:  game.NewSimpleAI(),
	}
}

// NewGameRemote 创建游戏Remote
func NewGameRemote(app pitaya.Pitaya, handler *GameHandler) *GameRemote {
	return &GameRemote{
		app:     app,
		handler: handler,
	}
}

// Init 初始化
func (h *GameHandler) Init() {
	log.Println("[GameHandler] 初始化完成")
}

// ==================== Handler 方法（处理客户端请求） ====================

// Join 加入匹配
func (h *GameHandler) Join(ctx context.Context, req *protos.JoinRequest) (*protos.JoinResponse, error) {
	s := h.app.GetSessionFromCtx(ctx)
	uid := s.UID()
	if uid == "" {
		return &protos.JoinResponse{Code: -1, Message: "请先登录"}, nil
	}

	name := req.PlayerName
	if name == "" {
		name = "玩家" + uid[:6]
	}

	log.Printf("[GameHandler] 玩家 %s(%s) 请求加入匹配", name, uid)

	room, seat := h.roomManager.JoinOrCreate(uid, name)
	h.playerRooms.Store(uid, room.ID)

	// 仅在新创建房间（座位0）时设置回调和AI策略，避免重复设置
	if seat == 0 {
		h.setupRoomCallbacks(room)
		room.AIStrategy = h.aiStrategy
	}

	log.Printf("[GameHandler] 玩家 %s 加入房间 %s 座位 %d (当前%d人)",
		name, room.ID, seat, room.PlayerCount())

	// 如果已有足够人，用AI填充并开始
	if room.PlayerCount() >= 3 {
		go room.StartGame()
	} else {
		// 等一会如果人不够就用AI填充
		go h.tryFillAIAndStart(room)
	}

	return &protos.JoinResponse{
		Code:    0,
		Message: "已加入匹配",
		RoomId:  room.ID,
		Seat:    int32(seat),
	}, nil
}

// tryFillAIAndStart 等待后用AI填充并开始
func (h *GameHandler) tryFillAIAndStart(room *game.Room) {
	// 等待5秒，看是否有新玩家加入
	// 如果还不够人就用AI填充
	time.Sleep(5 * time.Second)

	if room.State != game.StateWaiting {
		return
	}
	if room.PlayerCount() < 3 {
		room.FillAI()
		log.Printf("[GameHandler] 房间 %s 用AI填充到3人", room.ID)
	}

	if room.PlayerCount() >= 3 && room.State == game.StateWaiting {
		room.StartGame()
	}
}

// Bid 叫分
func (h *GameHandler) Bid(ctx context.Context, req *protos.BidRequest) (*protos.BidResponse, error) {
	s := h.app.GetSessionFromCtx(ctx)
	uid := s.UID()

	room := h.roomManager.FindRoomByUID(uid)
	if room == nil {
		return &protos.BidResponse{Code: -1, Message: "你不在任何房间中"}, nil
	}

	player := room.GetPlayerByUID(uid)
	if player == nil {
		return &protos.BidResponse{Code: -1, Message: "玩家未找到"}, nil
	}

	err := room.HandleBid(player.Seat, int(req.Score))
	if err != nil {
		return &protos.BidResponse{Code: -1, Message: err.Error()}, nil
	}

	return &protos.BidResponse{Code: 0, Message: "叫分成功"}, nil
}

// Play 出牌
func (h *GameHandler) Play(ctx context.Context, req *protos.PlayRequest) (*protos.PlayResponse, error) {
	s := h.app.GetSessionFromCtx(ctx)
	uid := s.UID()

	room := h.roomManager.FindRoomByUID(uid)
	if room == nil {
		return &protos.PlayResponse{Code: -1, Message: "你不在任何房间中"}, nil
	}

	player := room.GetPlayerByUID(uid)
	if player == nil {
		return &protos.PlayResponse{Code: -1, Message: "玩家未找到"}, nil
	}

	cards := game.CardsFromIDs32(req.CardIds)
	err := room.HandlePlay(player.Seat, cards)
	if err != nil {
		return &protos.PlayResponse{Code: -1, Message: err.Error()}, nil
	}

	return &protos.PlayResponse{Code: 0, Message: "出牌成功"}, nil
}

// Pass 不出
func (h *GameHandler) Pass(ctx context.Context, req *protos.PassRequest) (*protos.PassResponse, error) {
	s := h.app.GetSessionFromCtx(ctx)
	uid := s.UID()

	room := h.roomManager.FindRoomByUID(uid)
	if room == nil {
		return &protos.PassResponse{Code: -1, Message: "你不在任何房间中"}, nil
	}

	player := room.GetPlayerByUID(uid)
	if player == nil {
		return &protos.PassResponse{Code: -1, Message: "玩家未找到"}, nil
	}

	err := room.HandlePass(player.Seat)
	if err != nil {
		return &protos.PassResponse{Code: -1, Message: err.Error()}, nil
	}

	return &protos.PassResponse{Code: 0, Message: "操作成功"}, nil
}

// ==================== 房间回调（向客户端推送消息） ====================

func (h *GameHandler) setupRoomCallbacks(room *game.Room) {
	room.OnGameStart = h.onGameStart
	room.OnBidTurn = h.onBidTurn
	room.OnBidResult = h.onBidResult
	room.OnLandlordDecided = h.onLandlordDecided
	room.OnPlayTurn = h.onPlayTurn
	room.OnCardPlayed = h.onCardPlayed
	room.OnPass = h.onPass
	room.OnGameEnd = h.onGameEnd
}

func (h *GameHandler) onGameStart(room *game.Room) {
	for _, p := range room.Players {
		if p == nil || p.IsAI {
			continue
		}

		playerInfos := make([]*protos.PlayerInfo, 3)
		for i, pp := range room.Players {
			if pp != nil {
				playerInfos[i] = &protos.PlayerInfo{
					Uid:       pp.UID,
					Name:      pp.Name,
					Seat:      int32(pp.Seat),
					IsAi:      pp.IsAI,
					CardCount: int32(len(pp.Cards)),
				}
			}
		}

		push := &protos.GameStartPush{
			RoomId:      room.ID,
			Players:     playerInfos,
			YourCards:   game.CardsToIDs32(p.Cards),
			FirstBidder: int32(room.FirstBidder),
		}

		h.pushToPlayer(p.UID, "onGameStart", push)
	}
}

func (h *GameHandler) onBidTurn(room *game.Room, seat int) {
	push := &protos.BidTurnPush{
		Seat:   int32(seat),
		MaxBid: int32(room.MaxBid),
	}
	h.pushToRoom(room, "onBidTurn", push)
}

func (h *GameHandler) onBidResult(room *game.Room, seat int, score int) {
	player := room.GetPlayerBySeat(seat)
	name := ""
	if player != nil {
		name = player.Name
	}
	push := &protos.BidResultPush{
		Seat:  int32(seat),
		Score: int32(score),
		Name:  name,
	}
	h.pushToRoom(room, "onBidResult", push)
}

func (h *GameHandler) onLandlordDecided(room *game.Room) {
	for _, p := range room.Players {
		if p == nil || p.IsAI {
			continue
		}
		landlord := room.GetPlayerBySeat(room.LandlordSeat)
		push := &protos.LandlordDecidedPush{
			LandlordSeat: int32(room.LandlordSeat),
			LandlordName: landlord.Name,
			BottomCards:   game.CardsToIDs32(room.BottomCards),
			YourCards:     game.CardsToIDs32(p.Cards),
			BidScore:      int32(room.MaxBid),
		}
		h.pushToPlayer(p.UID, "onLandlordDecided", push)
	}
}

func (h *GameHandler) onPlayTurn(room *game.Room, seat int) {
	canPass := room.LastPlaySeat != -1 && room.LastPlaySeat != seat
	push := &protos.PlayTurnPush{
		Seat:      int32(seat),
		CanPass:   canPass,
		LastCards: game.CardsToIDs32(room.LastPlayCards),
		LastSeat:  int32(room.LastPlaySeat),
	}
	h.pushToRoom(room, "onPlayTurn", push)
}

func (h *GameHandler) onCardPlayed(room *game.Room, seat int, cards []game.Card, handInfo game.HandInfo) {
	player := room.GetPlayerBySeat(seat)
	push := &protos.CardPlayedPush{
		Seat:      int32(seat),
		Cards:     game.CardsToIDs32(cards),
		HandType:  handInfo.Type.String(),
		CardCount: int32(len(player.Cards)),
		Name:      player.Name,
	}
	h.pushToRoom(room, "onCardPlayed", push)
}

func (h *GameHandler) onPass(room *game.Room, seat int) {
	player := room.GetPlayerBySeat(seat)
	push := &protos.PassPush{
		Seat: int32(seat),
		Name: player.Name,
	}
	h.pushToRoom(room, "onPass", push)
}

func (h *GameHandler) onGameEnd(room *game.Room, winnerSeat int) {
	winner := room.GetPlayerBySeat(winnerSeat)
	isLandlord := winnerSeat == room.LandlordSeat
	landlordWin := isLandlord

	push := &protos.GameEndPush{
		WinnerSeat:  int32(winnerSeat),
		WinnerName:  winner.Name,
		IsLandlord:  isLandlord,
		LandlordWin: landlordWin,
		BidScore:    int32(room.MaxBid),
		BombCount:   int32(room.BombCount),
		Spring:      room.IsSpring(),
	}
	h.pushToRoom(room, "onGameEnd", push)

	// 清理玩家->房间映射
	for _, p := range room.Players {
		if p != nil {
			h.playerRooms.Delete(p.UID)
		}
	}

	// 清理房间
	h.roomManager.RemoveRoom(room.ID)
}

// ==================== 推送辅助方法 ====================

func (h *GameHandler) pushToPlayer(uid string, route string, v interface{}) {
	_, err := h.app.SendPushToUsers(route, v, []string{uid}, "connector")
	if err != nil {
		log.Printf("[Push Error] 推送 %s 给玩家 %s 失败: %v", route, uid, err)
	}
}

func (h *GameHandler) pushToRoom(room *game.Room, route string, v interface{}) {
	var uids []string
	for _, p := range room.Players {
		if p != nil && !p.IsAI {
			uids = append(uids, p.UID)
		}
	}
	if len(uids) == 0 {
		return
	}
	_, err := h.app.SendPushToUsers(route, v, uids, "connector")
	if err != nil {
		log.Printf("[Push Error] 推送 %s 给房间 %s 失败: %v", route, room.ID, err)
	}
}

// ==================== Remote 方法 ====================

// GetRoomInfo 获取房间信息（供 Connector RPC 调用）
func (r *GameRemote) GetRoomInfo(ctx context.Context, req *protos.RoomInfoRequest) (*protos.RoomInfoResponse, error) {
	room := r.handler.roomManager.GetRoom(req.RoomId)
	if room == nil {
		return nil, fmt.Errorf("房间不存在: %s", req.RoomId)
	}

	players := make([]*protos.PlayerInfo, 0, 3)
	for _, p := range room.Players {
		if p != nil {
			players = append(players, &protos.PlayerInfo{
				Uid:       p.UID,
				Name:      p.Name,
				Seat:      int32(p.Seat),
				IsAi:      p.IsAI,
				CardCount: int32(len(p.Cards)),
			})
		}
	}

	return &protos.RoomInfoResponse{
		RoomId:  room.ID,
		State:   room.State.String(),
		Players: players,
	}, nil
}

// RegisterGameServices 注册游戏服务到Pitaya app
func RegisterGameServices(app pitaya.Pitaya) {
	handler := NewGameHandler(app)
	remote := NewGameRemote(app, handler)

	app.Register(handler,
		component.WithName("game"),
		component.WithNameFunc(strings.ToLower),
	)

	app.RegisterRemote(remote,
		component.WithName("gameremote"),
		component.WithNameFunc(strings.ToLower),
	)
}
