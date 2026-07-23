package services

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"richman/game"
	"richman/protos"

	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/component"
)

// GameHandler 游戏后端Handler
type GameHandler struct {
	component.Base
	app         pitaya.Pitaya
	roomManager *game.RoomManager
	playerRooms sync.Map // uid -> roomID
}

// NewGameHandler 创建游戏Handler
func NewGameHandler(app pitaya.Pitaya) *GameHandler {
	return &GameHandler{app: app, roomManager: game.NewRoomManager()}
}

// Init 初始化
func (h *GameHandler) Init() {
	log.Println("[GameHandler] 初始化完成")
}

// ==================== Handler: 客户端请求 ====================

// Join 进入/创建房间
func (h *GameHandler) Join(ctx context.Context, req *protos.JoinRoomRequest) (*protos.JoinRoomResponse, error) {
	s := h.app.GetSessionFromCtx(ctx)
	uid := s.UID()
	if uid == "" {
		return &protos.JoinRoomResponse{Code: protos.ResultCode_RESULT_NOT_LOGIN, Msg: "请先登录"}, nil
	}
	name := req.PlayerName
	if name == "" {
		name = "玩家" + uid[:6]
	}

	room, seat, full := h.roomManager.JoinOrCreate(uid, name)
	h.playerRooms.Store(uid, room.ID)
	if seat == 0 {
		h.setupRoomCallbacks(room)
	}

	log.Printf("[GameHandler] 玩家 %s(%s) 加入房间 %s 座位 %d (满员=%v)", name, uid, room.ID, seat, full)

	h.broadcastRoomUpdate(room)
	if full {
		go room.StartGame()
	}

	return &protos.JoinRoomResponse{Code: protos.ResultCode_RESULT_OK, Msg: "已加入房间", RoomId: room.ID, Seat: int32(seat)}, nil
}

// RollDice 掷骰
func (h *GameHandler) RollDice(ctx context.Context, req *protos.RollDiceRequest) (*protos.AckResponse, error) {
	seat, room := h.locate(ctx)
	if room == nil {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_NOT_IN_ROOM, Msg: "你不在任何房间"}, nil
	}
	if err := room.HandleRoll(seat); err != nil {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_BAD_TURN, Msg: err.Error()}, nil
	}
	return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "ok"}, nil
}

// Buy 购买地产
func (h *GameHandler) Buy(ctx context.Context, req *protos.BuyRequest) (*protos.AckResponse, error) {
	seat, room := h.locate(ctx)
	if room == nil {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_NOT_IN_ROOM, Msg: "你不在任何房间"}, nil
	}
	if err := room.HandleBuy(seat, req.Buy); err != nil {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_BAD_TURN, Msg: err.Error()}, nil
	}
	return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "ok"}, nil
}

// Upgrade 升级地产
func (h *GameHandler) Upgrade(ctx context.Context, req *protos.UpgradeRequest) (*protos.AckResponse, error) {
	seat, room := h.locate(ctx)
	if room == nil {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_NOT_IN_ROOM, Msg: "你不在任何房间"}, nil
	}
	if err := room.HandleUpgrade(seat, req.Upgrade); err != nil {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_BAD_TURN, Msg: err.Error()}, nil
	}
	return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "ok"}, nil
}

// EndTurn 主动结束回合(兼容协议)
func (h *GameHandler) EndTurn(ctx context.Context, req *protos.EndTurnRequest) (*protos.AckResponse, error) {
	seat, room := h.locate(ctx)
	if room == nil {
		return &protos.AckResponse{Code: protos.ResultCode_RESULT_NOT_IN_ROOM, Msg: "你不在任何房间"}, nil
	}
	_ = room.HandleEndTurn(seat)
	return &protos.AckResponse{Code: protos.ResultCode_RESULT_OK, Msg: "ok"}, nil
}

// GetRoomInfo 查询当前房间快照(状态同步/重连)
func (h *GameHandler) GetRoomInfo(ctx context.Context, req *protos.RoomInfoRequest) (*protos.RoomInfoResponse, error) {
	_, room := h.locate(ctx)
	if room == nil {
		return &protos.RoomInfoResponse{Code: protos.ResultCode_RESULT_NOT_IN_ROOM, Msg: "你不在任何房间"}, nil
	}
	state, round, currentSeat := room.Meta()
	return &protos.RoomInfoResponse{
		Code:        protos.ResultCode_RESULT_OK,
		Msg:         "ok",
		RoomId:      room.ID,
		State:       protos.RoomState(state),
		Round:       int32(round),
		MaxRound:    game.MaxRounds,
		CurrentSeat: int32(currentSeat),
		Players:     buildPlayerInfos(room),
		Tiles:       buildTileInfos(room),
	}, nil
}

// locate 根据会话定位玩家所在房间与座位
func (h *GameHandler) locate(ctx context.Context) (int, *game.Room) {
	s := h.app.GetSessionFromCtx(ctx)
	uid := s.UID()
	if uid == "" {
		return -1, nil
	}
	room := h.roomManager.FindRoomByUID(uid)
	if room == nil {
		return -1, nil
	}
	return room.GetSeatByUID(uid), room
}

// ==================== 房间回调 -> 推送 ====================

func (h *GameHandler) setupRoomCallbacks(room *game.Room) {
	room.OnRoomUpdate = h.onRoomUpdate
	room.OnGameStart = h.onGameStart
	room.OnTurnStart = h.onTurnStart
	room.OnDiceResult = h.onDiceResult
	room.OnLandTile = h.onLandTile
	room.OnPropertyChanged = h.onPropertyChanged
	room.OnPayToll = h.onPayToll
	room.OnCardDrawn = h.onCardDrawn
	room.OnPlayerBankrupt = h.onPlayerBankrupt
	room.OnGameEnd = h.onGameEnd
}

func (h *GameHandler) onRoomUpdate(room *game.Room) {
	h.broadcastRoomUpdate(room)
}

func (h *GameHandler) broadcastRoomUpdate(room *game.Room) {
	players := buildPlayerInfos(room)
	need := int32(game.MaxPlayers - len(players))
	if need < 0 {
		need = 0
	}
	h.pushToRoom(room, "onRoomUpdate", &protos.RoomUpdatePush{
		RoomId:  room.ID,
		Players: players,
		Need:    need,
	})
}

func (h *GameHandler) onGameStart(room *game.Room) {
	tiles := buildTileInfos(room)
	players := buildPlayerInfos(room)
	order := make([]int32, 0, len(players))
	for _, p := range players {
		order = append(order, p.Seat)
	}
	h.pushToRoom(room, "onGameStart", &protos.GameStartPush{
		RoomId:    room.ID,
		Tiles:     tiles,
		Players:   players,
		Order:     order,
		InitMoney: game.InitMoney,
		MaxRound:  game.MaxRounds,
	})
}

func (h *GameHandler) onTurnStart(room *game.Room, seat, round int) {
	uid := uidBySeat(room, seat)
	h.pushToRoom(room, "onTurnStart", &protos.TurnStartPush{
		Uid:   uid,
		Seat:  int32(seat),
		Round: int32(round),
	})
}

func (h *GameHandler) onDiceResult(room *game.Room, seat, dice, from, to int, passStart bool, salary int64) {
	h.pushToRoom(room, "onDiceResult", &protos.DiceResultPush{
		Uid:       uidBySeat(room, seat),
		Seat:      int32(seat),
		Dice:      int32(dice),
		FromPos:   int32(from),
		ToPos:     int32(to),
		PassStart: passStart,
		Salary:    salary,
	})
}

func (h *GameHandler) onLandTile(room *game.Room, seat, pos int, canBuy, canUpgrade bool, price, money int64, level int) {
	tiles := room.TilesSnapshot()
	name := ""
	var ttype protos.TileType
	var owner string
	if pos >= 0 && pos < len(tiles) {
		name = tiles[pos].Name
		ttype = protos.TileType(tiles[pos].Type)
		owner = tiles[pos].OwnerUID
	}
	h.pushToRoom(room, "onLandTile", &protos.LandTilePush{
		Uid:        uidBySeat(room, seat),
		Pos:        int32(pos),
		TileType:   ttype,
		TileName:   name,
		CanBuy:     canBuy,
		CanUpgrade: canUpgrade,
		Price:      price,
		Money:      money,
		Level:      int32(level),
		OwnerUid:   owner,
	})
}

func (h *GameHandler) onPropertyChanged(room *game.Room, seat, pos int, ownerUID string, level int, action string, money int64) {
	act := protos.PropertyAction_PROP_BUY
	if action == "upgrade" {
		act = protos.PropertyAction_PROP_UPGRADE
	}
	h.pushToRoom(room, "onPropertyChanged", &protos.PropertyChangedPush{
		Uid:      uidBySeat(room, seat),
		Pos:      int32(pos),
		OwnerUid: ownerUID,
		Level:    int32(level),
		Action:   act,
		Money:    money,
	})
}

func (h *GameHandler) onPayToll(room *game.Room, fromUID, toUID string, amount int64, pos int, fromMoney, toMoney int64) {
	h.pushToRoom(room, "onPayToll", &protos.PayTollPush{
		FromUid:   fromUID,
		ToUid:     toUID,
		Amount:    amount,
		Pos:       int32(pos),
		FromMoney: fromMoney,
		ToMoney:   toMoney,
		IsTax:     toUID == "",
	})
}

func (h *GameHandler) onCardDrawn(room *game.Room, seat, cardType int, desc string, moneyDelta int64, moveTo int, money int64) {
	ct := protos.CardType_CARD_CHANCE
	if cardType == int(game.TileFate) {
		ct = protos.CardType_CARD_FATE
	}
	h.pushToRoom(room, "onCardDrawn", &protos.CardDrawnPush{
		Uid:        uidBySeat(room, seat),
		CardType:   ct,
		Desc:       desc,
		MoneyDelta: moneyDelta,
		MoveTo:     int32(moveTo),
		Money:      money,
	})
}

func (h *GameHandler) onPlayerBankrupt(room *game.Room, seat int) {
	h.pushToRoom(room, "onPlayerBankrupt", &protos.PlayerBankruptPush{
		Uid:  uidBySeat(room, seat),
		Seat: int32(seat),
	})
}

func (h *GameHandler) onGameEnd(room *game.Room, rankings []game.RankResult, winnerUID, winnerName, reason string) {
	ranks := make([]*protos.RankItem, 0, len(rankings))
	for _, r := range rankings {
		ranks = append(ranks, &protos.RankItem{
			Uid:        r.UID,
			Name:       r.Name,
			TotalAsset: r.TotalAsset,
			Rank:       int32(r.Rank),
			Bankrupt:   r.Bankrupt,
		})
	}
	endReason := protos.EndReason_END_UNKNOWN
	switch reason {
	case "last_standing":
		endReason = protos.EndReason_END_LAST_STANDING
	case "round_limit":
		endReason = protos.EndReason_END_ROUND_LIMIT
	}
	h.pushToRoom(room, "onGameEnd", &protos.GameEndPush{
		Rankings:   ranks,
		WinnerUid:  winnerUID,
		WinnerName: winnerName,
		Reason:     endReason,
	})

	// 清理
	for _, p := range room.PlayersSnapshot() {
		h.playerRooms.Delete(p.UID)
	}
	go func() {
		time.Sleep(3 * time.Second)
		h.roomManager.RemoveRoom(room.ID)
	}()
}

// ==================== 推送辅助 ====================

func (h *GameHandler) pushToRoom(room *game.Room, route string, v interface{}) {
	var uids []string
	for _, p := range room.PlayersSnapshot() {
		if !p.IsAI {
			uids = append(uids, p.UID)
		}
	}
	if len(uids) == 0 {
		return
	}
	if _, err := h.app.SendPushToUsers(route, v, uids, "connector"); err != nil {
		log.Printf("[Push Error] 推送 %s 给房间 %s 失败: %v", route, room.ID, err)
	}
}

func buildPlayerInfos(room *game.Room) []*protos.PlayerInfo {
	snap := room.PlayersSnapshot()
	res := make([]*protos.PlayerInfo, 0, len(snap))
	for _, p := range snap {
		res = append(res, &protos.PlayerInfo{
			Uid:      p.UID,
			Name:     p.Name,
			Seat:     int32(p.Seat),
			Money:    p.Money,
			Pos:      int32(p.Pos),
			Bankrupt: p.Bankrupt,
			IsAi:     p.IsAI,
		})
	}
	return res
}

func buildTileInfos(room *game.Room) []*protos.TileInfo {
	snap := room.TilesSnapshot()
	res := make([]*protos.TileInfo, 0, len(snap))
	for _, t := range snap {
		res = append(res, &protos.TileInfo{
			Index:    int32(t.Index),
			Type:     protos.TileType(t.Type),
			Name:     t.Name,
			Price:    t.Price,
			BaseToll: t.BaseToll,
			OwnerUid: t.OwnerUID,
			Level:    int32(t.Level),
			Tax:      t.Tax,
		})
	}
	return res
}

func uidBySeat(room *game.Room, seat int) string {
	for _, p := range room.PlayersSnapshot() {
		if p.Seat == seat {
			return p.UID
		}
	}
	return ""
}

// RegisterGameServices 注册后端服务
func RegisterGameServices(app pitaya.Pitaya) {
	handler := NewGameHandler(app)
	app.Register(handler,
		component.WithName("game"),
		component.WithNameFunc(strings.ToLower),
	)
}
