package game

import (
	"log"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RoomState 房间状态
type RoomState int

const (
	StateWaiting    RoomState = iota // 等待玩家
	StatePlaying                     // 游戏中
	StateSettlement                  // 结算
)

func (s RoomState) String() string {
	switch s {
	case StateWaiting:
		return "等待中"
	case StatePlaying:
		return "游戏中"
	case StateSettlement:
		return "结算中"
	}
	return "未知"
}

// turnPhase 回合内阶段
type turnPhase int

const (
	phaseIdle turnPhase = iota // 回合之间
	phaseWaitRoll              // 等待掷骰
	phaseWaitBuy               // 等待购买决策
	phaseWaitUpgrade           // 等待升级决策
)

// 各阶段等待超时(超时执行默认操作)
const actionTimeout = 15 * time.Second

// RankResult 结算排名项
type RankResult struct {
	UID        string
	Name       string
	TotalAsset int64
	Rank       int
	Bankrupt   bool
}

// Room 游戏房间(大富翁)
type Room struct {
	mu sync.Mutex

	ID      string
	State   RoomState
	Board   []*Tile
	Players []*Player // 座位索引 0..MaxPlayers-1

	CurrentSeat int
	Round       int
	turnsTaken  int

	phase       turnPhase
	pendingTile *Tile // 等待买/升级决策的地块

	actionSeq int64       // 用于校验超时定时器有效性
	timer     *time.Timer // 当前阶段超时定时器

	// 回调(由 service 设置, 均在未持锁时触发)
	OnRoomUpdate      func(r *Room)
	OnGameStart       func(r *Room)
	OnTurnStart       func(r *Room, seat, round int)
	OnDiceResult      func(r *Room, seat, dice, from, to int, passStart bool, salary int64)
	OnLandTile        func(r *Room, seat, pos int, canBuy, canUpgrade bool, price, money int64, level int)
	OnPropertyChanged func(r *Room, seat, pos int, ownerUID string, level int, action string, money int64)
	OnPayToll         func(r *Room, fromUID, toUID string, amount int64, pos int, fromMoney, toMoney int64)
	OnCardDrawn       func(r *Room, seat, cardType int, desc string, moneyDelta int64, moveTo int, money int64)
	OnPlayerBankrupt  func(r *Room, seat int)
	OnGameEnd         func(r *Room, rankings []RankResult, winnerUID, winnerName, reason string)
}

// NewRoom 创建房间
func NewRoom() *Room {
	return &Room{
		ID:          uuid.New().String()[:8],
		State:       StateWaiting,
		Board:       NewBoard(),
		Players:     make([]*Player, MaxPlayers),
		CurrentSeat: 0,
		phase:       phaseIdle,
	}
}

// ==================== 只读快照(线程安全) ====================

// PlayersSnapshot 返回玩家状态副本
func (r *Room) PlayersSnapshot() []Player {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := make([]Player, 0, MaxPlayers)
	for _, p := range r.Players {
		if p != nil {
			res = append(res, *p)
		}
	}
	return res
}

// TilesSnapshot 返回地图状态副本
func (r *Room) TilesSnapshot() []Tile {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := make([]Tile, len(r.Board))
	for i, t := range r.Board {
		res[i] = *t
	}
	return res
}

// Meta 返回房间元信息(状态/回合/当前行动座位), 线程安全
func (r *Room) Meta() (RoomState, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.State, r.Round, r.CurrentSeat
}

// PlayerCount 当前玩家数
func (r *Room) PlayerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.playerCountLocked()
}

func (r *Room) playerCountLocked() int {
	c := 0
	for _, p := range r.Players {
		if p != nil {
			c++
		}
	}
	return c
}

// GetSeatByUID 根据UID查座位, 不存在返回 -1
func (r *Room) GetSeatByUID(uid string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, p := range r.Players {
		if p != nil && p.UID == uid {
			return i
		}
	}
	return -1
}

// ==================== 游戏流程 ====================

// StartGame 开始游戏(满员后调用)
func (r *Room) StartGame() {
	r.mu.Lock()
	if r.State != StateWaiting {
		r.mu.Unlock()
		return
	}
	for _, p := range r.Players {
		if p == nil {
			r.mu.Unlock()
			log.Printf("[Room %s] 人数不足, 无法开始", r.ID)
			return
		}
		p.Money = InitMoney
		p.Pos = 0
		p.Bankrupt = false
	}
	r.State = StatePlaying
	r.turnsTaken = 0
	r.Round = 1
	r.CurrentSeat = 0
	r.phase = phaseIdle
	r.mu.Unlock()

	log.Printf("[Room %s] 游戏开始", r.ID)
	if r.OnGameStart != nil {
		r.OnGameStart(r)
	}
	r.beginTurn()
}

// beginTurn 开启当前玩家的回合
func (r *Room) beginTurn() {
	r.mu.Lock()
	if r.State != StatePlaying {
		r.mu.Unlock()
		return
	}
	r.Round = r.turnsTaken/MaxPlayers + 1
	r.phase = phaseWaitRoll
	seat := r.CurrentSeat
	round := r.Round
	player := r.Players[seat]
	isAI := player != nil && player.IsAI
	r.armTimerLocked(phaseWaitRoll, seat)
	r.mu.Unlock()

	log.Printf("[Room %s] 第%d回合 轮到座位%d(%s)", r.ID, round, seat, player.Name)
	if r.OnTurnStart != nil {
		r.OnTurnStart(r, seat, round)
	}
	if isAI {
		go func() {
			time.Sleep(600 * time.Millisecond)
			r.HandleRoll(seat)
		}()
	}
}

// HandleRoll 处理掷骰
func (r *Room) HandleRoll(seat int) error {
	r.mu.Lock()
	if r.State != StatePlaying || r.phase != phaseWaitRoll || seat != r.CurrentSeat {
		r.mu.Unlock()
		return errBadTurn
	}
	r.invalidateTimerLocked()
	r.phase = phaseIdle
	player := r.Players[seat]
	dice := RollDice()
	from := player.Pos
	sum := from + dice
	to := sum % BoardSize
	passStart := sum >= BoardSize
	var salary int64
	if passStart {
		salary = StartSalary
		player.Money += salary
	}
	player.Pos = to
	r.mu.Unlock()

	log.Printf("[Room %s] 座位%d 掷出%d: %d->%d 过起点=%v", r.ID, seat, dice, from, to, passStart)
	if r.OnDiceResult != nil {
		r.OnDiceResult(r, seat, dice, from, to, passStart, salary)
	}
	r.resolveLanding(seat, to)
	return nil
}

// resolveLanding 结算落地格
func (r *Room) resolveLanding(seat, pos int) {
	r.mu.Lock()
	tile := r.Board[pos]
	player := r.Players[seat]

	switch tile.Type {
	case TileProperty:
		if tile.OwnerUID == "" {
			if player.Money >= tile.Price {
				r.phase = phaseWaitBuy
				r.pendingTile = tile
				price := tile.Price
				money := player.Money
				isAI := player.IsAI
				r.armTimerLocked(phaseWaitBuy, seat)
				r.mu.Unlock()
				if r.OnLandTile != nil {
					r.OnLandTile(r, seat, pos, true, false, price, money, 0)
				}
				if isAI {
					go func() { time.Sleep(600 * time.Millisecond); r.HandleBuy(seat, true) }()
				}
				return
			}
			money := player.Money
			r.mu.Unlock()
			if r.OnLandTile != nil {
				r.OnLandTile(r, seat, pos, false, false, tile.Price, money, 0)
			}
			r.endTurn()
			return
		}
		if tile.OwnerUID == player.UID {
			if tile.CanUpgrade() && player.Money >= tile.UpgradeCost() {
				r.phase = phaseWaitUpgrade
				r.pendingTile = tile
				cost := tile.UpgradeCost()
				money := player.Money
				level := tile.Level
				isAI := player.IsAI
				r.armTimerLocked(phaseWaitUpgrade, seat)
				r.mu.Unlock()
				if r.OnLandTile != nil {
					r.OnLandTile(r, seat, pos, false, true, cost, money, level)
				}
				if isAI {
					go func() { time.Sleep(600 * time.Millisecond); r.HandleUpgrade(seat, false) }()
				}
				return
			}
			money := player.Money
			level := tile.Level
			r.mu.Unlock()
			if r.OnLandTile != nil {
				r.OnLandTile(r, seat, pos, false, false, 0, money, level)
			}
			r.endTurn()
			return
		}
		// 他人地产 -> 付过路费
		ownerSeat := r.seatByUIDLocked(tile.OwnerUID)
		toll := tile.Toll()
		r.mu.Unlock()
		r.payMoney(seat, ownerSeat, toll, pos)
		r.endTurn()
		return

	case TileTax:
		tax := tile.Tax
		r.mu.Unlock()
		r.payMoney(seat, -1, tax, pos)
		r.endTurn()
		return

	case TileChance:
		eff := DrawChance()
		r.mu.Unlock()
		r.applyCard(seat, int(TileChance), eff)
		r.endTurn()
		return

	case TileFate:
		eff := DrawFate()
		r.mu.Unlock()
		r.applyCard(seat, int(TileFate), eff)
		r.endTurn()
		return

	default: // 起点/停留, 无额外结算(工资已在掷骰阶段处理)
		money := player.Money
		r.mu.Unlock()
		if r.OnLandTile != nil {
			r.OnLandTile(r, seat, pos, false, false, 0, money, 0)
		}
		r.endTurn()
		return
	}
}

// HandleBuy 处理购买决策
func (r *Room) HandleBuy(seat int, buy bool) error {
	r.mu.Lock()
	if r.State != StatePlaying || r.phase != phaseWaitBuy || seat != r.CurrentSeat {
		r.mu.Unlock()
		return errBadTurn
	}
	r.invalidateTimerLocked()
	r.phase = phaseIdle
	tile := r.pendingTile
	r.pendingTile = nil
	player := r.Players[seat]

	bought := false
	if buy && tile != nil && tile.OwnerUID == "" && player.Money >= tile.Price {
		player.Money -= tile.Price
		tile.OwnerUID = player.UID
		tile.Level = 1
		bought = true
	}
	var pos, level int
	var owner string
	var money int64
	if tile != nil {
		pos = tile.Index
		level = tile.Level
		owner = tile.OwnerUID
	}
	money = player.Money
	r.mu.Unlock()

	if bought {
		log.Printf("[Room %s] 座位%d 购买地块%d", r.ID, seat, pos)
		if r.OnPropertyChanged != nil {
			r.OnPropertyChanged(r, seat, pos, owner, level, "buy", money)
		}
	}
	r.endTurn()
	return nil
}

// HandleUpgrade 处理升级决策
func (r *Room) HandleUpgrade(seat int, upgrade bool) error {
	r.mu.Lock()
	if r.State != StatePlaying || r.phase != phaseWaitUpgrade || seat != r.CurrentSeat {
		r.mu.Unlock()
		return errBadTurn
	}
	r.invalidateTimerLocked()
	r.phase = phaseIdle
	tile := r.pendingTile
	r.pendingTile = nil
	player := r.Players[seat]

	upgraded := false
	if upgrade && tile != nil && tile.OwnerUID == player.UID && tile.CanUpgrade() && player.Money >= tile.UpgradeCost() {
		player.Money -= tile.UpgradeCost()
		tile.Level++
		upgraded = true
	}
	var pos, level int
	var owner string
	if tile != nil {
		pos = tile.Index
		level = tile.Level
		owner = tile.OwnerUID
	}
	money := player.Money
	r.mu.Unlock()

	if upgraded {
		log.Printf("[Room %s] 座位%d 升级地块%d 至%d级", r.ID, seat, pos, level)
		if r.OnPropertyChanged != nil {
			r.OnPropertyChanged(r, seat, pos, owner, level, "upgrade", money)
		}
	}
	r.endTurn()
	return nil
}

// HandleEndTurn 客户端主动结束回合(本实现自动推进, 此处为兼容协议的空操作)
func (r *Room) HandleEndTurn(seat int) error {
	return nil
}

// payMoney 支付金额: toSeat<0 表示缴税给系统; 现金不足则破产淘汰
func (r *Room) payMoney(fromSeat, toSeat int, amount int64, pos int) {
	if amount <= 0 {
		return
	}
	r.mu.Lock()
	from := r.Players[fromSeat]
	var to *Player
	if toSeat >= 0 {
		to = r.Players[toSeat]
	}
	actual := amount
	bankrupt := false
	if from.Money >= amount {
		from.Money -= amount
	} else {
		actual = from.Money
		from.Money = 0
		bankrupt = true
	}
	if to != nil {
		to.Money += actual
	}
	fromUID := from.UID
	fromMoney := from.Money
	toUID := ""
	var toMoney int64
	if to != nil {
		toUID = to.UID
		toMoney = to.Money
	}
	r.mu.Unlock()

	log.Printf("[Room %s] 座位%d 支付%d(格%d) -> %s", r.ID, fromSeat, actual, pos, toUID)
	if r.OnPayToll != nil {
		r.OnPayToll(r, fromUID, toUID, actual, pos, fromMoney, toMoney)
	}
	if bankrupt {
		r.setBankrupt(fromSeat)
	}
}

// applyCard 应用机会/命运卡效果
func (r *Room) applyCard(seat, cardType int, eff CardEffect) {
	r.mu.Lock()
	player := r.Players[seat]

	if eff.MoveTo >= 0 {
		player.Pos = eff.MoveTo
		if eff.MoveTo == 0 {
			player.Money += StartSalary
		}
	}

	selfBankrupt := false
	if eff.MoneyDelta < 0 {
		if player.Money < -eff.MoneyDelta {
			player.Money = 0
			selfBankrupt = true
		} else {
			player.Money += eff.MoneyDelta
		}
	} else if eff.MoneyDelta > 0 {
		player.Money += eff.MoneyDelta
	}

	var dividendBankrupt []int
	if eff.AllPay > 0 && !selfBankrupt {
		for i, p := range r.Players {
			if p == nil || i == seat || p.Bankrupt {
				continue
			}
			pay := eff.AllPay
			if p.Money < pay {
				pay = p.Money
				p.Money = 0
				dividendBankrupt = append(dividendBankrupt, i)
			} else {
				p.Money -= pay
			}
			player.Money += pay
		}
	}
	money := player.Money
	r.mu.Unlock()

	log.Printf("[Room %s] 座位%d 抽卡: %s", r.ID, seat, eff.Desc)
	if r.OnCardDrawn != nil {
		r.OnCardDrawn(r, seat, cardType, eff.Desc, eff.MoneyDelta, eff.MoveTo, money)
	}
	if selfBankrupt {
		r.setBankrupt(seat)
	}
	for _, bs := range dividendBankrupt {
		r.setBankrupt(bs)
	}
}

// setBankrupt 玩家破产: 名下地产收归无主
func (r *Room) setBankrupt(seat int) {
	r.mu.Lock()
	player := r.Players[seat]
	if player == nil || player.Bankrupt {
		r.mu.Unlock()
		return
	}
	player.Bankrupt = true
	player.Money = 0
	uid := player.UID
	for _, t := range r.Board {
		if t.OwnerUID == uid {
			t.Reset()
		}
	}
	r.mu.Unlock()

	log.Printf("[Room %s] 座位%d(%s) 破产淘汰", r.ID, seat, player.Name)
	if r.OnPlayerBankrupt != nil {
		r.OnPlayerBankrupt(r, seat)
	}
}

// endTurn 结束当前回合并推进(或结算)
func (r *Room) endTurn() {
	r.mu.Lock()
	if r.State != StatePlaying {
		r.mu.Unlock()
		return
	}
	r.phase = phaseIdle
	r.pendingTile = nil
	r.turnsTaken++

	active := 0
	for _, p := range r.Players {
		if p != nil && !p.Bankrupt {
			active++
		}
	}
	if active <= 1 {
		r.mu.Unlock()
		r.settle("last_standing")
		return
	}
	if r.turnsTaken >= MaxRounds*MaxPlayers {
		r.mu.Unlock()
		r.settle("round_limit")
		return
	}

	next := r.CurrentSeat
	for i := 0; i < MaxPlayers; i++ {
		next = (next + 1) % MaxPlayers
		if r.Players[next] != nil && !r.Players[next].Bankrupt {
			break
		}
	}
	r.CurrentSeat = next
	r.mu.Unlock()

	r.beginTurn()
}

// settle 结算
func (r *Room) settle(reason string) {
	r.mu.Lock()
	if r.State == StateSettlement {
		r.mu.Unlock()
		return
	}
	r.State = StateSettlement
	r.invalidateTimerLocked()

	ranks := make([]RankResult, 0, MaxPlayers)
	for _, p := range r.Players {
		if p == nil {
			continue
		}
		asset := p.Money
		for _, t := range r.Board {
			if t.OwnerUID == p.UID {
				asset += t.Value()
			}
		}
		ranks = append(ranks, RankResult{
			UID:        p.UID,
			Name:       p.Name,
			TotalAsset: asset,
			Bankrupt:   p.Bankrupt,
		})
	}
	r.mu.Unlock()

	sort.Slice(ranks, func(i, j int) bool {
		if ranks[i].Bankrupt != ranks[j].Bankrupt {
			return !ranks[i].Bankrupt // 未破产者排前
		}
		return ranks[i].TotalAsset > ranks[j].TotalAsset
	})
	for i := range ranks {
		ranks[i].Rank = i + 1
	}
	winnerUID, winnerName := "", ""
	if len(ranks) > 0 {
		winnerUID = ranks[0].UID
		winnerName = ranks[0].Name
	}

	log.Printf("[Room %s] 对局结束(%s), 胜者: %s", r.ID, reason, winnerName)
	if r.OnGameEnd != nil {
		r.OnGameEnd(r, ranks, winnerUID, winnerName, reason)
	}
}

// ==================== 定时器/超时 ====================

// armTimerLocked 在持锁状态下装配当前阶段的超时定时器
func (r *Room) armTimerLocked(phase turnPhase, seat int) {
	r.actionSeq++
	seq := r.actionSeq
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(actionTimeout, func() {
		r.onTimeout(seq, phase, seat)
	})
}

// invalidateTimerLocked 使当前定时器失效
func (r *Room) invalidateTimerLocked() {
	r.actionSeq++
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

// onTimeout 超时执行默认操作
func (r *Room) onTimeout(seq int64, phase turnPhase, seat int) {
	r.mu.Lock()
	if r.actionSeq != seq || r.State != StatePlaying || r.phase != phase || r.CurrentSeat != seat {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	log.Printf("[Room %s] 座位%d 操作超时, 执行默认操作(phase=%d)", r.ID, seat, phase)
	switch phase {
	case phaseWaitRoll:
		r.HandleRoll(seat)
	case phaseWaitBuy:
		r.HandleBuy(seat, false)
	case phaseWaitUpgrade:
		r.HandleUpgrade(seat, false)
	}
}

// ==================== 内部工具 ====================

func (r *Room) seatByUIDLocked(uid string) int {
	for i, p := range r.Players {
		if p != nil && p.UID == uid {
			return i
		}
	}
	return -1
}
