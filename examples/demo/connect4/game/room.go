package game

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// 对局节奏参数
const (
	TurnTimeout  = 15 * time.Second // 单步思考时限
	MaxTimeouts  = 3                // 同一玩家累计超时次数上限, 超过判负
	OfflineGrace = 60 * time.Second // 掉线宽限期, 超过判负
	aiThinkDelay = 500 * time.Millisecond
)

// 落子失败原因
var (
	ErrBadTurn = errors.New("不是你的回合或对局状态不允许")
	ErrBadCol  = errors.New("列号非法")
	ErrColFull = errors.New("该列已满")
)

// RoomState 房间状态(与 protos.RoomState 同值)
type RoomState int

// 房间状态取值
const (
	StateWaiting RoomState = iota // 等待开局
	StatePlaying                  // 对局中
	StateOver                     // 已结束
)

// EndReason 对局结束原因(与 protos.EndReason 同值)
type EndReason int

// 结束原因取值
const (
	ReasonUnknown    EndReason = iota
	ReasonConnect4             // 连成四子
	ReasonDrawFull             // 满盘平局
	ReasonTimeout              // 超时次数超限
	ReasonDisconnect           // 掉线超过宽限期
)

// Member 建房成员(由 lobby 撮合后传入)
type Member struct {
	UID  string
	Name string
	Seat int
}

// Player 对局玩家
type Player struct {
	UID          string
	Name         string
	Seat         int
	IsAI         bool
	Online       bool
	TimeoutCount int
}

// Info 房间只读快照(供 service 层构造协议消息)
type Info struct {
	ID          string
	State       RoomState
	CurrentSeat int
	MoveNo      int
	FirstSeat   int
	DeadlineMs  int64
	StartedAt   int64
	Cells       []byte
	Players     []Player
	Spectators  int
	WinnerUID   string
	WinnerSeat  int
	Reason      EndReason
	WinLine     []Cell
	Moves       []int
}

// Room 一局四子棋。所有状态变更在互斥锁内完成, 回调统一在解锁后触发。
type Room struct {
	mu sync.Mutex

	ID        string
	FirstSeat int
	StartedAt int64

	state       RoomState
	board       *Board
	players     [MaxPlayers]*Player
	currentSeat int
	moves       []int
	spectators  map[string]bool

	winnerUID  string
	winnerSeat int
	reason     EndReason
	winLine    []Cell

	turnSeq    int64
	turnTimer  *time.Timer
	deadlineMs int64

	offlineSeq    [MaxPlayers]int64
	offlineTimers [MaxPlayers]*time.Timer

	// 回调: 由 service 层设置, 均在未持锁时调用
	OnGameStart       func(r *Room, resync bool)
	OnTurnStart       func(r *Room, seat int, uid string, moveNo int, deadlineMs int64)
	OnPieceDropped    func(r *Room, seat, col, row, moveNo int, byTimeout bool)
	OnGameOver        func(r *Room)
	OnPlayerStatus    func(r *Room, seat int, uid string, online bool, graceSeconds int)
	OnSpectatorUpdate func(r *Room, count int)
	OnSnapshot        func(r *Room)
}

// NewRoom 创建房间。withAI=true 时, members 未占满的座位由 AI 补位。
func NewRoom(id string, members []Member, withAI bool, firstSeat int) *Room {
	r := &Room{
		ID:         id,
		FirstSeat:  firstSeat,
		state:      StateWaiting,
		board:      NewBoard(),
		spectators: make(map[string]bool),
		winnerSeat: -1,
	}
	for _, m := range members {
		if m.Seat >= 0 && m.Seat < MaxPlayers {
			r.players[m.Seat] = &Player{UID: m.UID, Name: m.Name, Seat: m.Seat, Online: true}
		}
	}
	if withAI {
		for seat := 0; seat < MaxPlayers; seat++ {
			if r.players[seat] == nil {
				r.players[seat] = &Player{
					UID:    fmt.Sprintf("ai-%s-%d", id, seat),
					Name:   "机器人",
					Seat:   seat,
					IsAI:   true,
					Online: true,
				}
			}
		}
	}
	return r
}

// ==================== 只读访问 ====================

// Info 返回房间当前状态的副本
func (r *Room) Info() Info {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.infoLocked()
}

func (r *Room) infoLocked() Info {
	info := Info{
		ID:          r.ID,
		State:       r.state,
		CurrentSeat: r.currentSeat,
		MoveNo:      r.board.MoveCount(),
		FirstSeat:   r.FirstSeat,
		DeadlineMs:  r.deadlineMs,
		StartedAt:   r.StartedAt,
		Cells:       r.board.Encode(),
		Spectators:  len(r.spectators),
		WinnerUID:   r.winnerUID,
		WinnerSeat:  r.winnerSeat,
		Reason:      r.reason,
		WinLine:     append([]Cell(nil), r.winLine...),
		Moves:       append([]int(nil), r.moves...),
	}
	for _, p := range r.players {
		if p != nil {
			info.Players = append(info.Players, *p)
		}
	}
	return info
}

// SeatOf 返回 uid 的座位, 非本局玩家返回 -1
func (r *Room) SeatOf(uid string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seatOfLocked(uid)
}

func (r *Room) seatOfLocked(uid string) int {
	for i, p := range r.players {
		if p != nil && p.UID == uid {
			return i
		}
	}
	return -1
}

// PlayerUIDs 返回真人玩家的 uid(AI 不需要推送)
func (r *Room) PlayerUIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.playerUIDsLocked()
}

func (r *Room) playerUIDsLocked() []string {
	uids := make([]string, 0, MaxPlayers)
	for _, p := range r.players {
		if p != nil && !p.IsAI {
			uids = append(uids, p.UID)
		}
	}
	return uids
}

// AudienceUIDs 返回需要接收对局推送的所有 uid(真人玩家 + 观战者)
func (r *Room) AudienceUIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	uids := r.playerUIDsLocked()
	for uid := range r.spectators {
		uids = append(uids, uid)
	}
	return uids
}

// ==================== 观战 ====================

// AddSpectator 加入观战(玩家自身不计入); 返回当前观战人数
func (r *Room) AddSpectator(uid string) int {
	r.mu.Lock()
	if r.seatOfLocked(uid) < 0 {
		r.spectators[uid] = true
	}
	count := len(r.spectators)
	r.mu.Unlock()
	if r.OnSpectatorUpdate != nil {
		r.OnSpectatorUpdate(r, count)
	}
	return count
}

// RemoveSpectator 退出观战
func (r *Room) RemoveSpectator(uid string) {
	r.mu.Lock()
	if !r.spectators[uid] {
		r.mu.Unlock()
		return
	}
	delete(r.spectators, uid)
	count := len(r.spectators)
	r.mu.Unlock()
	if r.OnSpectatorUpdate != nil {
		r.OnSpectatorUpdate(r, count)
	}
}

// ==================== 对局流程 ====================

// StartGame 开局(由 lobby 撮合满员后触发)
func (r *Room) StartGame() {
	r.mu.Lock()
	if r.state != StateWaiting {
		r.mu.Unlock()
		return
	}
	for seat, p := range r.players {
		if p == nil {
			r.mu.Unlock()
			log.Printf("[Room %s] 座位%d 缺席, 无法开局", r.ID, seat)
			return
		}
	}
	r.state = StatePlaying
	r.currentSeat = r.FirstSeat
	r.StartedAt = time.Now().Unix()
	r.mu.Unlock()

	log.Printf("[Room %s] 开局, 先手座位%d", r.ID, r.FirstSeat)
	if r.OnGameStart != nil {
		r.OnGameStart(r, false)
	}
	if r.OnSnapshot != nil {
		r.OnSnapshot(r)
	}
	r.beginTurn()
}

// Resume 故障恢复后续跑当前回合
func (r *Room) Resume() {
	r.mu.Lock()
	playing := r.state == StatePlaying
	r.mu.Unlock()
	if playing {
		r.beginTurn()
	}
}

// HandleDrop 处理玩家落子
func (r *Room) HandleDrop(uid string, col int) error {
	r.mu.Lock()
	seat := r.seatOfLocked(uid)
	if r.state != StatePlaying || seat < 0 || seat != r.currentSeat {
		r.mu.Unlock()
		return ErrBadTurn
	}
	ev, err := r.dropLocked(seat, col, false)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	r.emitDrop(ev)
	return nil
}

// dropEvent 一次落子产生的事件(解锁后广播)
type dropEvent struct {
	seat      int
	col       int
	row       int
	moveNo    int
	byTimeout bool
	over      bool
}

// dropLocked 在持锁状态下落子并推进状态机
func (r *Room) dropLocked(seat, col int, byTimeout bool) (*dropEvent, error) {
	if !ColValid(col) {
		return nil, ErrBadCol
	}
	if r.board.ColFull(col) {
		return nil, ErrColFull
	}
	row, ok := r.board.Drop(col, PieceOfSeat(seat))
	if !ok {
		return nil, ErrColFull
	}
	r.moves = append(r.moves, col)
	r.invalidateTurnTimerLocked()

	ev := &dropEvent{seat: seat, col: col, row: row, moveNo: r.board.MoveCount(), byTimeout: byTimeout}
	switch line := r.board.WinningLine(col, row); {
	case line != nil:
		r.finishLocked(ReasonConnect4, seat, line)
		ev.over = true
	case r.board.Full():
		r.finishLocked(ReasonDrawFull, -1, nil)
		ev.over = true
	default:
		r.currentSeat = 1 - seat
	}
	return ev, nil
}

// emitDrop 广播落子事件并推进到下一回合或结束
func (r *Room) emitDrop(ev *dropEvent) {
	log.Printf("[Room %s] 座位%d 落子 列%d 行%d (第%d手, 超时代打=%v)", r.ID, ev.seat, ev.col, ev.row, ev.moveNo, ev.byTimeout)
	if r.OnPieceDropped != nil {
		r.OnPieceDropped(r, ev.seat, ev.col, ev.row, ev.moveNo, ev.byTimeout)
	}
	if r.OnSnapshot != nil {
		r.OnSnapshot(r) // 每步落子后落快照, 把宕机损失压到一步以内
	}
	if ev.over {
		if r.OnGameOver != nil {
			r.OnGameOver(r)
		}
		return
	}
	r.beginTurn()
}

// beginTurn 开启当前座位的回合
func (r *Room) beginTurn() {
	r.mu.Lock()
	if r.state != StatePlaying {
		r.mu.Unlock()
		return
	}
	seat := r.currentSeat
	p := r.players[seat]
	if p == nil {
		r.mu.Unlock()
		return
	}
	seq := r.armTurnTimerLocked(seat)
	uid, isAI, deadline, moveNo := p.UID, p.IsAI, r.deadlineMs, r.board.MoveCount()
	r.mu.Unlock()

	if r.OnTurnStart != nil {
		r.OnTurnStart(r, seat, uid, moveNo, deadline)
	}
	if isAI {
		go r.aiMove(seat, seq, AIDepth, false)
	}
}

// aiMove AI 出手(亦用于超时代打)
func (r *Room) aiMove(seat int, seq int64, depth int, byTimeout bool) {
	if !byTimeout {
		time.Sleep(aiThinkDelay) // 正常出手稍作停顿, 便于观察对局过程
	}
	r.mu.Lock()
	if !r.turnValidLocked(seq, seat) {
		r.mu.Unlock()
		return
	}
	board := r.board.Clone()
	r.mu.Unlock()

	col := BestMove(board, PieceOfSeat(seat), depth)
	if col < 0 {
		return
	}

	r.mu.Lock()
	if !r.turnValidLocked(seq, seat) {
		r.mu.Unlock()
		return
	}
	ev, err := r.dropLocked(seat, col, byTimeout)
	r.mu.Unlock()
	if err != nil {
		log.Printf("[Room %s] AI 落子失败 座位%d 列%d: %v", r.ID, seat, col, err)
		return
	}
	r.emitDrop(ev)
}

func (r *Room) turnValidLocked(seq int64, seat int) bool {
	return r.state == StatePlaying && r.turnSeq == seq && r.currentSeat == seat
}

// SetOnline 更新玩家在线状态: 掉线开启宽限计时, 重连则取消
func (r *Room) SetOnline(uid string, online bool) {
	r.mu.Lock()
	seat := r.seatOfLocked(uid)
	if seat < 0 || r.state != StatePlaying {
		r.mu.Unlock()
		return
	}
	p := r.players[seat]
	if p.Online == online {
		r.mu.Unlock()
		return
	}
	p.Online = online
	grace := 0
	if online {
		r.cancelOfflineTimerLocked(seat)
	} else {
		r.armOfflineTimerLocked(seat)
		grace = int(OfflineGrace / time.Second)
	}
	r.mu.Unlock()

	log.Printf("[Room %s] 座位%d(%s) 在线状态 -> %v", r.ID, seat, uid, online)
	if r.OnPlayerStatus != nil {
		r.OnPlayerStatus(r, seat, uid, online, grace)
	}
}

// finishLocked 结束对局(持锁)。winnerSeat<0 表示平局。
func (r *Room) finishLocked(reason EndReason, winnerSeat int, line []Cell) {
	if r.state == StateOver {
		return
	}
	r.state = StateOver
	r.reason = reason
	r.winnerSeat = winnerSeat
	r.winLine = line
	r.winnerUID = ""
	if winnerSeat >= 0 && r.players[winnerSeat] != nil {
		r.winnerUID = r.players[winnerSeat].UID
	}
	r.invalidateTurnTimerLocked()
	for seat := range r.offlineTimers {
		r.cancelOfflineTimerLocked(seat)
	}
	log.Printf("[Room %s] 对局结束 原因=%d 胜方座位=%d", r.ID, reason, winnerSeat)
}

// Stop 停止房间内所有定时器(房间销毁时调用)
func (r *Room) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidateTurnTimerLocked()
	for seat := range r.offlineTimers {
		r.cancelOfflineTimerLocked(seat)
	}
}

// ==================== 定时器 ====================

// armTurnTimerLocked 装配本回合的超时定时器, 返回本回合序号
func (r *Room) armTurnTimerLocked(seat int) int64 {
	r.turnSeq++
	seq := r.turnSeq
	if r.turnTimer != nil {
		r.turnTimer.Stop()
	}
	r.deadlineMs = time.Now().Add(TurnTimeout).UnixMilli()
	r.turnTimer = time.AfterFunc(TurnTimeout, func() { r.onTurnTimeout(seq, seat) })
	return seq
}

func (r *Room) invalidateTurnTimerLocked() {
	r.turnSeq++
	if r.turnTimer != nil {
		r.turnTimer.Stop()
		r.turnTimer = nil
	}
	r.deadlineMs = 0
}

// onTurnTimeout 思考超时: 累计次数超限判负, 否则由 AI 代落一子
func (r *Room) onTurnTimeout(seq int64, seat int) {
	r.mu.Lock()
	if !r.turnValidLocked(seq, seat) {
		r.mu.Unlock()
		return
	}
	p := r.players[seat]
	if p == nil {
		r.mu.Unlock()
		return
	}
	p.TimeoutCount++
	count := p.TimeoutCount
	if count >= MaxTimeouts {
		r.finishLocked(ReasonTimeout, 1-seat, nil)
		r.mu.Unlock()
		log.Printf("[Room %s] 座位%d 超时%d次, 判负", r.ID, seat, count)
		if r.OnGameOver != nil {
			r.OnGameOver(r)
		}
		return
	}
	r.mu.Unlock()

	log.Printf("[Room %s] 座位%d 第%d次超时, 由服务端代落", r.ID, seat, count)
	r.aiMove(seat, seq, AIFastDepth, true)
}

// armOfflineTimerLocked 开启掉线宽限计时
func (r *Room) armOfflineTimerLocked(seat int) {
	r.offlineSeq[seat]++
	seq := r.offlineSeq[seat]
	if r.offlineTimers[seat] != nil {
		r.offlineTimers[seat].Stop()
	}
	r.offlineTimers[seat] = time.AfterFunc(OfflineGrace, func() { r.onOfflineTimeout(seq, seat) })
}

func (r *Room) cancelOfflineTimerLocked(seat int) {
	r.offlineSeq[seat]++
	if r.offlineTimers[seat] != nil {
		r.offlineTimers[seat].Stop()
		r.offlineTimers[seat] = nil
	}
}

// onOfflineTimeout 掉线超过宽限期判负
func (r *Room) onOfflineTimeout(seq int64, seat int) {
	r.mu.Lock()
	if r.state != StatePlaying || r.offlineSeq[seat] != seq {
		r.mu.Unlock()
		return
	}
	r.finishLocked(ReasonDisconnect, 1-seat, nil)
	r.mu.Unlock()

	log.Printf("[Room %s] 座位%d 掉线超过%v, 判负", r.ID, seat, OfflineGrace)
	if r.OnGameOver != nil {
		r.OnGameOver(r)
	}
}
