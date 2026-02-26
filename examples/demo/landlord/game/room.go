package game

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RoomState 房间状态
type RoomState int

const (
	StateWaiting    RoomState = iota // 等待玩家加入
	StateBidding                     // 叫分阶段
	StatePlaying                     // 出牌阶段
	StateSettlement                  // 结算阶段
)

var stateNames = map[RoomState]string{
	StateWaiting:    "等待中",
	StateBidding:    "叫分中",
	StatePlaying:    "出牌中",
	StateSettlement: "结算中",
}

func (s RoomState) String() string {
	if name, ok := stateNames[s]; ok {
		return name
	}
	return "未知"
}

// Player 玩家信息
type Player struct {
	UID     string // 用户ID
	Name    string // 玩家名称
	Seat    int    // 座位号 (0, 1, 2)
	Cards   []Card // 手牌
	IsAI    bool   // 是否是AI玩家
	IsReady bool   // 是否准备好
}

// Room 游戏房间
type Room struct {
	mu sync.RWMutex

	ID      string     // 房间ID
	State   RoomState  // 房间状态
	Players [3]*Player // 3个座位

	// 发牌相关
	BottomCards []Card // 底牌

	// 叫分相关
	CurrentBidder int // 当前叫分的座位号
	FirstBidder   int // 首先叫分的座位号
	MaxBid        int // 当前最高叫分
	MaxBidSeat    int // 最高叫分的座位号
	BidRound      int // 叫分轮次
	BidCount      int // 叫过分的人数（包括不叫）

	// 出牌相关
	LandlordSeat  int      // 地主座位
	CurrentTurn   int      // 当前出牌的座位
	LastPlaySeat  int      // 上一次出牌的座位 (-1 表示无)
	LastPlayCards []Card   // 上一次出的牌
	LastHandInfo  HandInfo // 上一次出的牌型信息
	PassCount     int      // 连续不出的次数
	BombCount     int      // 本局炸弹数量（用于翻倍）

	// 出牌记录（判断春天用）
	LandlordPlayCount int // 地主出牌次数
	FarmerPlayCount   int // 农民出牌次数

	// 回调函数（由外层 service 设置）
	OnGameStart       func(room *Room)
	OnBidTurn         func(room *Room, seat int)
	OnBidResult       func(room *Room, seat int, score int)
	OnLandlordDecided func(room *Room)
	OnPlayTurn        func(room *Room, seat int)
	OnCardPlayed      func(room *Room, seat int, cards []Card, handInfo HandInfo)
	OnPass            func(room *Room, seat int)
	OnGameEnd         func(room *Room, winnerSeat int)

	// AI 策略
	AIStrategy AIStrategy
}

// AIStrategy AI策略接口
type AIStrategy interface {
	DecideBid(player *Player, maxBid int) int
	DecidePlay(player *Player, lastCards []Card, lastHandInfo HandInfo, isFirstPlay bool) []Card
}

// RoomManager 房间管理器
type RoomManager struct {
	mu       sync.RWMutex
	rooms    map[string]*Room
	waitRoom *Room // 当前等待匹配的房间
}

// NewRoomManager 创建房间管理器
func NewRoomManager() *RoomManager {
	return &RoomManager{
		rooms: make(map[string]*Room),
	}
}

// NewRoom 创建新房间
func NewRoom() *Room {
	return &Room{
		ID:           uuid.New().String()[:8],
		State:        StateWaiting,
		LastPlaySeat: -1,
		MaxBidSeat:   -1,
	}
}

// GetRoom 获取房间
func (rm *RoomManager) GetRoom(roomID string) *Room {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.rooms[roomID]
}

// JoinOrCreate 加入或创建房间，返回房间和座位号
func (rm *RoomManager) JoinOrCreate(uid, name string) (*Room, int) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// 检查是否已经在某个房间
	for _, room := range rm.rooms {
		for _, p := range room.Players {
			if p != nil && p.UID == uid {
				return room, p.Seat
			}
		}
	}

	// 尝试加入等待中的房间
	if rm.waitRoom != nil && rm.waitRoom.State == StateWaiting {
		seat := rm.waitRoom.FindEmptySeat()
		if seat >= 0 {
			rm.waitRoom.Players[seat] = &Player{
				UID:  uid,
				Name: name,
				Seat: seat,
			}
			room := rm.waitRoom
			// 检查是否满员
			if room.PlayerCount() >= 3 {
				rm.waitRoom = nil
			}
			return room, seat
		}
	}

	// 创建新房间
	room := NewRoom()
	room.Players[0] = &Player{
		UID:  uid,
		Name: name,
		Seat: 0,
	}
	rm.rooms[room.ID] = room
	rm.waitRoom = room
	return room, 0
}

// RemoveRoom 移除房间
func (rm *RoomManager) RemoveRoom(roomID string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.waitRoom != nil && rm.waitRoom.ID == roomID {
		rm.waitRoom = nil
	}
	delete(rm.rooms, roomID)
}

// PlayerLeave 玩家离开房间
func (rm *RoomManager) PlayerLeave(uid string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	for _, room := range rm.rooms {
		for i, p := range room.Players {
			if p != nil && p.UID == uid {
				room.Players[i] = nil
				// 如果房间空了就删除
				if room.PlayerCount() == 0 {
					if rm.waitRoom != nil && rm.waitRoom.ID == room.ID {
						rm.waitRoom = nil
					}
					delete(rm.rooms, room.ID)
				}
				return
			}
		}
	}
}

// FindRoomByUID 根据玩家UID查找房间
func (rm *RoomManager) FindRoomByUID(uid string) *Room {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	for _, room := range rm.rooms {
		for _, p := range room.Players {
			if p != nil && p.UID == uid {
				return room
			}
		}
	}
	return nil
}

// === Room 方法 ===

// FindEmptySeat 查找空座位
func (r *Room) FindEmptySeat() int {
	for i, p := range r.Players {
		if p == nil {
			return i
		}
	}
	return -1
}

// PlayerCount 当前玩家数
func (r *Room) PlayerCount() int {
	count := 0
	for _, p := range r.Players {
		if p != nil {
			count++
		}
	}
	return count
}

// GetPlayerBySeat 根据座位获取玩家
func (r *Room) GetPlayerBySeat(seat int) *Player {
	if seat < 0 || seat > 2 {
		return nil
	}
	return r.Players[seat]
}

// GetPlayerByUID 根据UID获取玩家
func (r *Room) GetPlayerByUID(uid string) *Player {
	for _, p := range r.Players {
		if p != nil && p.UID == uid {
			return p
		}
	}
	return nil
}

// FillAI 用AI填充空位
func (r *Room) FillAI() {
	for i, p := range r.Players {
		if p == nil {
			r.Players[i] = &Player{
				UID:  fmt.Sprintf("ai_%s_%d", r.ID, i),
				Name: fmt.Sprintf("机器人%d", i+1),
				Seat: i,
				IsAI: true,
			}
		}
	}
}

// StartGame 开始游戏
func (r *Room) StartGame() {
	r.mu.Lock()

	// 发牌
	result := Deal()
	r.Players[0].Cards = result.Player1
	r.Players[1].Cards = result.Player2
	r.Players[2].Cards = result.Player3
	r.BottomCards = result.Bottom

	// 随机选择首先叫分的人
	r.FirstBidder = int(time.Now().UnixNano() % 3)
	r.CurrentBidder = r.FirstBidder
	r.MaxBid = 0
	r.MaxBidSeat = -1
	r.BidRound = 0
	r.BidCount = 0
	r.BombCount = 0
	r.LandlordPlayCount = 0
	r.FarmerPlayCount = 0
	r.State = StateBidding

	r.mu.Unlock()

	log.Printf("[Room %s] 游戏开始，首先叫分: 座位%d (%s)", r.ID, r.FirstBidder, r.Players[r.FirstBidder].Name)

	// 通知游戏开始
	if r.OnGameStart != nil {
		r.OnGameStart(r)
	}

	// 通知首个叫分人
	if r.OnBidTurn != nil {
		r.OnBidTurn(r, r.CurrentBidder)
	}

	// 如果首个叫分人是AI，自动叫分
	r.tryAIBid()
}

// HandleBid 处理叫分
func (r *Room) HandleBid(seat int, score int) error {
	r.mu.Lock()

	if r.State != StateBidding {
		r.mu.Unlock()
		return fmt.Errorf("当前不在叫分阶段")
	}
	if seat != r.CurrentBidder {
		r.mu.Unlock()
		return fmt.Errorf("不是你的叫分回合")
	}
	if score < 0 || score > 3 {
		r.mu.Unlock()
		return fmt.Errorf("叫分无效，应为0-3")
	}
	if score != 0 && score <= r.MaxBid {
		r.mu.Unlock()
		return fmt.Errorf("叫分必须大于当前最高分 %d", r.MaxBid)
	}

	// 记录叫分
	if score > r.MaxBid {
		r.MaxBid = score
		r.MaxBidSeat = seat
	}
	r.BidCount++

	log.Printf("[Room %s] 座位%d (%s) 叫分: %d", r.ID, seat, r.Players[seat].Name, score)

	r.mu.Unlock()

	// 通知叫分结果
	if r.OnBidResult != nil {
		r.OnBidResult(r, seat, score)
	}

	r.mu.Lock()

	// 叫了3分直接成为地主
	if score == 3 {
		r.mu.Unlock()
		r.decideLandlord()
		return nil
	}

	// 所有人都叫过了
	if r.BidCount >= 3 {
		r.mu.Unlock()
		// 没人叫分，重新发牌
		if r.MaxBid == 0 {
			log.Printf("[Room %s] 没人叫分，重新开始", r.ID)
			r.StartGame()
			return nil
		}
		r.decideLandlord()
		return nil
	}

	// 下一个人叫分
	r.CurrentBidder = (r.CurrentBidder + 1) % 3
	r.mu.Unlock()

	if r.OnBidTurn != nil {
		r.OnBidTurn(r, r.CurrentBidder)
	}

	// AI 自动叫分
	r.tryAIBid()

	return nil
}

// decideLandlord 确定地主
func (r *Room) decideLandlord() {
	r.mu.Lock()

	r.LandlordSeat = r.MaxBidSeat
	landlord := r.Players[r.LandlordSeat]

	// 地主拿底牌
	landlord.Cards = append(landlord.Cards, r.BottomCards...)
	SortCards(landlord.Cards)

	// 进入出牌阶段
	r.State = StatePlaying
	r.CurrentTurn = r.LandlordSeat
	r.LastPlaySeat = -1
	r.LastPlayCards = nil
	r.LastHandInfo = HandInfo{Type: HandTypeNone}
	r.PassCount = 0

	log.Printf("[Room %s] 地主确定: 座位%d (%s), 叫分%d, 底牌: %s",
		r.ID, r.LandlordSeat, landlord.Name, r.MaxBid, CardsString(r.BottomCards))

	r.mu.Unlock()

	// 通知地主确定
	if r.OnLandlordDecided != nil {
		r.OnLandlordDecided(r)
	}

	// 通知地主出牌
	if r.OnPlayTurn != nil {
		r.OnPlayTurn(r, r.CurrentTurn)
	}

	// AI 自动出牌
	r.tryAIPlay()
}

// HandlePlay 处理出牌
func (r *Room) HandlePlay(seat int, cards []Card) error {
	r.mu.Lock()

	if r.State != StatePlaying {
		r.mu.Unlock()
		return fmt.Errorf("当前不在出牌阶段")
	}
	if seat != r.CurrentTurn {
		r.mu.Unlock()
		return fmt.Errorf("不是你的出牌回合")
	}

	player := r.Players[seat]

	// 验证牌是否在手中
	for _, c := range cards {
		if !ContainsCard(player.Cards, c) {
			r.mu.Unlock()
			return fmt.Errorf("你没有这张牌: %s", c.String())
		}
	}

	// 识别牌型
	handInfo := DetectHandType(cards)
	if handInfo.Type == HandTypeNone {
		r.mu.Unlock()
		return fmt.Errorf("不合法的牌型")
	}

	// 检查是否能打过上家
	isFirstPlay := r.LastPlaySeat == -1 || r.LastPlaySeat == seat
	if !isFirstPlay {
		if !CanBeat(handInfo, r.LastHandInfo) {
			r.mu.Unlock()
			return fmt.Errorf("出的牌打不过上家")
		}
	}

	// 从手牌中移除
	player.Cards = RemoveCards(player.Cards, cards)

	// 更新出牌信息
	r.LastPlaySeat = seat
	r.LastPlayCards = cards
	r.LastHandInfo = handInfo
	r.PassCount = 0

	// 统计炸弹
	if handInfo.Type == HandTypeBomb || handInfo.Type == HandTypeRocket {
		r.BombCount++
	}

	// 统计出牌次数
	if seat == r.LandlordSeat {
		r.LandlordPlayCount++
	} else {
		r.FarmerPlayCount++
	}

	log.Printf("[Room %s] 座位%d (%s) 出牌: %s [%s], 剩余%d张",
		r.ID, seat, player.Name, CardsString(cards), handInfo.Type.String(), len(player.Cards))

	r.mu.Unlock()

	// 通知出牌
	if r.OnCardPlayed != nil {
		r.OnCardPlayed(r, seat, cards, handInfo)
	}

	// 检查是否赢了
	if len(player.Cards) == 0 {
		r.handleGameEnd(seat)
		return nil
	}

	// 下一个人出牌
	r.mu.Lock()
	r.CurrentTurn = (r.CurrentTurn + 1) % 3
	r.mu.Unlock()

	if r.OnPlayTurn != nil {
		r.OnPlayTurn(r, r.CurrentTurn)
	}

	// AI 自动出牌
	r.tryAIPlay()

	return nil
}

// HandlePass 处理不出
func (r *Room) HandlePass(seat int) error {
	r.mu.Lock()

	if r.State != StatePlaying {
		r.mu.Unlock()
		return fmt.Errorf("当前不在出牌阶段")
	}
	if seat != r.CurrentTurn {
		r.mu.Unlock()
		return fmt.Errorf("不是你的出牌回合")
	}

	// 第一次出牌或上一手是自己的不能不出
	if r.LastPlaySeat == -1 || r.LastPlaySeat == seat {
		r.mu.Unlock()
		return fmt.Errorf("你必须出牌")
	}

	r.PassCount++
	player := r.Players[seat]

	log.Printf("[Room %s] 座位%d (%s) 不出", r.ID, seat, player.Name)

	// 如果连续2人不出，下一个人自由出牌
	if r.PassCount >= 2 {
		r.LastPlaySeat = r.CurrentTurn // 这会使下一手变成自由出牌
		r.LastPlayCards = nil
		r.LastHandInfo = HandInfo{Type: HandTypeNone}
		r.PassCount = 0
	}

	r.CurrentTurn = (r.CurrentTurn + 1) % 3
	r.mu.Unlock()

	// 通知不出
	if r.OnPass != nil {
		r.OnPass(r, seat)
	}

	if r.OnPlayTurn != nil {
		r.OnPlayTurn(r, r.CurrentTurn)
	}

	// AI 自动出牌
	r.tryAIPlay()

	return nil
}

// handleGameEnd 处理游戏结束
func (r *Room) handleGameEnd(winnerSeat int) {
	r.mu.Lock()
	r.State = StateSettlement
	log.Printf("[Room %s] 游戏结束! 赢家: 座位%d (%s)", r.ID, winnerSeat, r.Players[winnerSeat].Name)
	r.mu.Unlock()

	if r.OnGameEnd != nil {
		r.OnGameEnd(r, winnerSeat)
	}
}

// IsSpring 是否春天（地主赢且农民没出过牌，或农民赢且地主只出过一次牌）
func (r *Room) IsSpring() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.FarmerPlayCount == 0 || r.LandlordPlayCount == 1
}

// tryAIBid AI 自动叫分
func (r *Room) tryAIBid() {
	r.mu.RLock()
	if r.State != StateBidding {
		r.mu.RUnlock()
		return
	}
	player := r.Players[r.CurrentBidder]
	if player == nil || !player.IsAI || r.AIStrategy == nil {
		r.mu.RUnlock()
		return
	}

	seat := r.CurrentBidder
	maxBid := r.MaxBid
	r.mu.RUnlock()

	// AI 思考延迟
	go func() {
		time.Sleep(time.Duration(500+time.Now().UnixNano()%1000) * time.Millisecond)
		score := r.AIStrategy.DecideBid(player, maxBid)
		r.HandleBid(seat, score)
	}()
}

// tryAIPlay AI 自动出牌
func (r *Room) tryAIPlay() {
	r.mu.RLock()
	if r.State != StatePlaying {
		r.mu.RUnlock()
		return
	}
	player := r.Players[r.CurrentTurn]
	if player == nil || !player.IsAI || r.AIStrategy == nil {
		r.mu.RUnlock()
		return
	}

	seat := r.CurrentTurn
	isFirstPlay := r.LastPlaySeat == -1 || r.LastPlaySeat == seat
	lastCards := r.LastPlayCards
	lastHandInfo := r.LastHandInfo
	r.mu.RUnlock()

	// AI 思考延迟
	go func() {
		time.Sleep(time.Duration(500+time.Now().UnixNano()%1000) * time.Millisecond)
		cards := r.AIStrategy.DecidePlay(player, lastCards, lastHandInfo, isFirstPlay)
		if cards == nil {
			r.HandlePass(seat)
		} else {
			err := r.HandlePlay(seat, cards)
			if err != nil {
				log.Printf("[AI Error] 座位%d 出牌失败: %v, 尝试不出", seat, err)
				r.HandlePass(seat)
			}
		}
	}()
}
