package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"connect4/game"
	"connect4/protos"

	"github.com/sirupsen/logrus"
	"github.com/topfreegames/pitaya/v2/client"
	"github.com/topfreegames/pitaya/v2/conn/message"
	pitayaprotos "github.com/topfreegames/pitaya/v2/protos"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/protoadapt"
)

// requestTimeout 单次请求等待响应的上限
const requestTimeout = 10 * time.Second

// moveDelay 落子前的停顿, 便于观察对局节奏(部分场景会调大)
var moveDelay = 300 * time.Millisecond

// reply 一次响应的原始内容
type reply struct {
	data  []byte
	isErr bool
}

// Client 自动化测试客户端: 维护本地棋盘镜像, 轮到自己时用与服务端相同的
// 搜索算法选点落子, 因此无需人工干预即可跑完整局。
type Client struct {
	name string
	addr string

	mu       sync.Mutex
	cli      *client.Client
	pending  map[uint]chan reply
	board    *game.Board
	uid      string
	seat     int
	roomID   string
	role     protos.PlayerRole
	lastDrop int  // 已为哪个 moveNo 落过子, 防重复
	passive  bool // true=从不主动落子(用于验证超时代打与超时判负)
	started  bool
	stopped  bool

	overOnce sync.Once
	over     chan struct{}
	result   string
}

// NewClient 创建客户端
func NewClient(name, addr string) *Client {
	return &Client{
		name:     name,
		addr:     addr,
		pending:  make(map[uint]chan reply),
		board:    game.NewBoard(),
		seat:     -1,
		lastDrop: -1,
		over:     make(chan struct{}),
	}
}

// Connect 建立连接并启动读循环
func (c *Client) Connect() error {
	cli := client.New(logrus.WarnLevel, requestTimeout)
	if err := cli.ConnectTo(c.addr); err != nil {
		return fmt.Errorf("连接 %s 失败: %w", c.addr, err)
	}
	c.mu.Lock()
	c.cli = cli
	c.stopped = false
	c.mu.Unlock()
	go c.readLoop(cli)
	c.logf("已连接 %s", c.addr)
	return nil
}

// Disconnect 断开传输层(模拟网络中断), 之后可用 Reconnect 恢复
func (c *Client) Disconnect() {
	c.mu.Lock()
	c.stopped = true
	cli := c.cli
	c.cli = nil
	c.mu.Unlock()
	if cli != nil {
		cli.Disconnect()
	}
}

// Close 关闭客户端
func (c *Client) Close() { c.Disconnect() }

// Done 对局结束信号
func (c *Client) Done() <-chan struct{} { return c.over }

// Result 结束语(用于断言/展示)
func (c *Client) Result() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}

// ==================== 请求 / 响应 ====================

func (c *Client) request(route string, req, resp proto.Message) error {
	data, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	c.mu.Lock()
	cli := c.cli
	c.mu.Unlock()
	if cli == nil {
		return fmt.Errorf("未连接")
	}

	ch := make(chan reply, 1)
	reqID, err := cli.SendRequest(route, data)
	if err != nil {
		return fmt.Errorf("发送 %s 失败: %w", route, err)
	}
	c.mu.Lock()
	c.pending[reqID] = ch
	c.mu.Unlock()

	select {
	case r := <-ch:
		if r.isErr {
			return fmt.Errorf("%s 返回错误: %s", route, decodeServerError(r.data))
		}
		return proto.Unmarshal(r.data, resp)
	case <-time.After(requestTimeout):
		c.mu.Lock()
		delete(c.pending, reqID)
		c.mu.Unlock()
		return fmt.Errorf("%s 响应超时", route)
	}
}

func (c *Client) readLoop(cli *client.Client) {
	for msg := range cli.MsgChannel() {
		switch msg.Type {
		case message.Response:
			c.mu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- reply{data: msg.Data, isErr: msg.Err}
			}
		case message.Push:
			c.handlePush(msg)
		}
	}
}

// ==================== 业务动作 ====================

// Login 登录; lastUID 非空时尝试身份续接
func (c *Client) Login(lastUID string) error {
	resp := &protos.LoginResponse{}
	if err := c.request("connector.login", &protos.LoginRequest{PlayerName: c.name, LastUid: lastUID}, resp); err != nil {
		return err
	}
	if resp.Code != protos.ResultCode_RESULT_OK {
		return fmt.Errorf("登录失败: %s", resp.Msg)
	}
	c.mu.Lock()
	c.uid = resp.Uid
	c.mu.Unlock()
	c.logf("登录成功 uid=%s 身份续接=%v", short(resp.Uid), resp.Resumed)
	return nil
}

// Match 请求匹配
func (c *Client) Match() error {
	resp := &protos.MatchResponse{}
	if err := c.request("lobby.lobby.match", &protos.MatchRequest{}, resp); err != nil {
		return err
	}
	if resp.Code != protos.ResultCode_RESULT_OK {
		return fmt.Errorf("匹配失败: %s", resp.Msg)
	}
	c.mu.Lock()
	c.roomID, c.seat, c.role = resp.RoomId, int(resp.Seat), protos.PlayerRole_ROLE_PLAYER
	c.mu.Unlock()
	if resp.Matched {
		c.logf("匹配成功 房间=%s 座位=%d", resp.RoomId, resp.Seat)
	} else {
		c.logf("已进入匹配队列 房间=%s 座位=%d (等待对手)", resp.RoomId, resp.Seat)
	}
	return nil
}

// CancelMatch 取消匹配
func (c *Client) CancelMatch() error {
	ack := &protos.AckResponse{}
	if err := c.request("lobby.lobby.cancelmatch", &protos.CancelMatchRequest{}, ack); err != nil {
		return err
	}
	if ack.Code != protos.ResultCode_RESULT_OK {
		return fmt.Errorf("取消失败: %s", ack.Msg)
	}
	c.logf("已取消匹配")
	return nil
}

// RoomList 查询可观战房间
func (c *Client) RoomList(limit int32) (*protos.RoomListResponse, error) {
	resp := &protos.RoomListResponse{}
	if err := c.request("lobby.lobby.roomlist", &protos.RoomListRequest{Limit: limit}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Spectate 观战指定房间并拉取全量状态
func (c *Client) Spectate(roomID string) error {
	ack := &protos.AckResponse{}
	if err := c.request("lobby.lobby.spectate", &protos.SpectateRequest{RoomId: roomID}, ack); err != nil {
		return err
	}
	if ack.Code != protos.ResultCode_RESULT_OK {
		return fmt.Errorf("观战失败: %s", ack.Msg)
	}
	state := &protos.GameStateResponse{}
	if err := c.request("game.game.watch", &protos.WatchRequest{}, state); err != nil {
		return err
	}
	if state.Code != protos.ResultCode_RESULT_OK {
		return fmt.Errorf("拉取状态失败: %s", state.Msg)
	}
	c.mu.Lock()
	c.roomID, c.role, c.seat = roomID, protos.PlayerRole_ROLE_SPECTATOR, -1
	c.mu.Unlock()
	c.applyState(state)
	c.logf("开始观战 房间=%s 当前第%d手 观战人数=%d", roomID, state.MoveNo, state.Spectators)
	return nil
}

// Reconnect 断线重连: 重新建连 + 身份续接 + 定位房间 + 全量同步
func (c *Client) Reconnect() error {
	c.Disconnect()
	c.mu.Lock()
	uid := c.uid
	c.mu.Unlock()
	if err := c.Connect(); err != nil {
		return err
	}
	if err := c.Login(uid); err != nil {
		return err
	}
	resp := &protos.ReconnectResponse{}
	if err := c.request("lobby.lobby.reconnect", &protos.ReconnectRequest{}, resp); err != nil {
		return err
	}
	if resp.Code != protos.ResultCode_RESULT_OK {
		return fmt.Errorf("重连失败: %s", resp.Msg)
	}
	c.mu.Lock()
	c.roomID, c.seat, c.role = resp.RoomId, int(resp.Seat), resp.Role
	c.mu.Unlock()
	c.logf("重连成功 房间=%s 座位=%d", resp.RoomId, resp.Seat)
	return c.SyncState()
}

// SyncState 拉取全量状态并在轮到自己时补上落子(亦用于驱动 owner 宕机后的惰性恢复)
func (c *Client) SyncState() error {
	state := &protos.GameStateResponse{}
	if err := c.request("game.game.getgamestate", &protos.GameStateRequest{}, state); err != nil {
		return err
	}
	if state.Code != protos.ResultCode_RESULT_OK {
		return fmt.Errorf("同步失败: %s", state.Msg)
	}
	c.applyState(state)
	c.maybeMove(int(state.CurrentSeat), int(state.MoveNo), state.State)
	return nil
}

// StartHeartbeat 周期性同步状态: 正常时校验一致性, owner 宕机时驱动惰性恢复
func (c *Client) StartHeartbeat(interval time.Duration) {
	go func() {
		for {
			select {
			case <-c.over:
				return
			case <-time.After(interval):
			}
			c.mu.Lock()
			skip := c.stopped || !c.started || c.roomID == ""
			c.mu.Unlock()
			if skip { // 还在匹配队列中(或已断开), 无需同步
				continue
			}
			if err := c.SyncState(); err != nil {
				c.logf("状态同步失败(可能正在故障恢复): %v", err)
			}
		}
	}()
}

// drop 落子(失败时重试, 覆盖 owner 重指派的瞬时失败)
func (c *Client) drop(col int) {
	for attempt := 1; attempt <= 3; attempt++ {
		ack := &protos.AckResponse{}
		err := c.request("game.game.drop", &protos.DropRequest{Col: int32(col)}, ack)
		switch {
		case err != nil:
			c.logf("落子请求失败(第%d次): %v", attempt, err)
		case ack.Code == protos.ResultCode_RESULT_OK:
			return
		case ack.Code == protos.ResultCode_RESULT_BAD_TURN:
			return // 已经不是本方回合(如超时被代打), 放弃
		default:
			c.logf("落子被拒: %s", ack.Msg)
			return
		}
		time.Sleep(time.Second)
	}
}

// ==================== 推送处理 ====================

func (c *Client) handlePush(msg *message.Message) {
	switch msg.Route {
	case "onMatchUpdate":
		var push protos.MatchUpdatePush
		if err := proto.Unmarshal(msg.Data, &push); err == nil {
			c.logf("匹配状态: %s (%s)", push.State, push.Msg)
		}

	case "onGameStart":
		var push protos.GameStartPush
		if err := proto.Unmarshal(msg.Data, &push); err != nil {
			return
		}
		c.mu.Lock()
		c.roomID = push.RoomId
		c.board = game.DecodeBoard(push.Board.GetCells())
		c.lastDrop = -1
		c.started = true
		for _, p := range push.Players {
			if p.Uid == c.uid {
				c.seat, c.role = int(p.Seat), protos.PlayerRole_ROLE_PLAYER
			}
		}
		seat := c.seat
		c.mu.Unlock()
		tag := "开局"
		if push.Resync {
			tag = "故障恢复后重同步"
		}
		c.logf("===== %s ===== 房间=%s 我的座位=%d 先手=%d 对手: %s",
			tag, push.RoomId, seat, push.FirstSeat, describeOpponents(push.Players, c.uid))
		c.maybeMove(int(push.CurrentSeat), int(push.MoveNo), protos.RoomState_ROOM_PLAYING)

	case "onTurnStart":
		var push protos.TurnStartPush
		if err := proto.Unmarshal(msg.Data, &push); err != nil {
			return
		}
		c.maybeMove(int(push.Seat), int(push.MoveNo), protos.RoomState_ROOM_PLAYING)

	case "onPieceDropped":
		var push protos.PieceDroppedPush
		if err := proto.Unmarshal(msg.Data, &push); err != nil {
			return
		}
		c.mu.Lock()
		c.board.Drop(int(push.Col), game.Piece(push.Piece))
		mySeat, spectating := c.seat, c.role == protos.PlayerRole_ROLE_SPECTATOR
		c.mu.Unlock()
		who := "对手"
		switch {
		case spectating:
			who = "玩家"
		case int(push.Seat) == mySeat:
			who = "我"
		}
		extra := ""
		if push.ByTimeout {
			extra = " [超时代打]"
		}
		c.logf("第%d手: %s(座位%d) 落在 列%d 行%d%s", push.MoveNo, who, push.Seat, push.Col, push.Row, extra)

	case "onOpponentOffline":
		var push protos.OpponentStatusPush
		if err := proto.Unmarshal(msg.Data, &push); err == nil {
			c.logf("对手(座位%d)掉线, 宽限 %d 秒", push.Seat, push.GraceSeconds)
		}

	case "onOpponentOnline":
		var push protos.OpponentStatusPush
		if err := proto.Unmarshal(msg.Data, &push); err == nil {
			c.logf("对手(座位%d)已重连", push.Seat)
		}

	case "onSpectatorUpdate":
		var push protos.SpectatorUpdatePush
		if err := proto.Unmarshal(msg.Data, &push); err == nil {
			c.logf("观战人数: %d", push.Count)
		}

	case "onGameOver":
		var push protos.GameOverPush
		if err := proto.Unmarshal(msg.Data, &push); err != nil {
			return
		}
		c.mu.Lock()
		c.board = game.DecodeBoard(push.Board.GetCells())
		mySeat := c.seat
		board := c.board
		c.mu.Unlock()

		outcome := "平局"
		switch {
		case push.WinnerSeat < 0:
			outcome = "平局"
		case int(push.WinnerSeat) == mySeat:
			outcome = "我赢了"
		default:
			outcome = "我输了"
		}
		res := fmt.Sprintf("%s (胜方=%s 原因=%s)", outcome, push.WinnerName, push.Reason)
		c.logf("===== 对局结束: %s =====\n%s", res, renderBoard(board, push.WinningLine))
		c.mu.Lock()
		c.result = res
		c.mu.Unlock()
		c.overOnce.Do(func() { close(c.over) })

	case "onError":
		var push protos.ErrorPush
		if err := proto.Unmarshal(msg.Data, &push); err == nil {
			c.logf("服务端错误: %s (%s)", push.Msg, push.Code)
		}
	}
}

// maybeMove 若轮到自己且该手尚未落子, 则计算并发送落子
func (c *Client) maybeMove(currentSeat, moveNo int, state protos.RoomState) {
	if state != protos.RoomState_ROOM_PLAYING {
		return
	}
	c.mu.Lock()
	if c.stopped || c.passive || c.role != protos.PlayerRole_ROLE_PLAYER || c.seat < 0 ||
		currentSeat != c.seat || c.lastDrop == moveNo {
		c.mu.Unlock()
		return
	}
	c.lastDrop = moveNo
	board := c.board.Clone()
	piece := game.PieceOfSeat(c.seat)
	c.mu.Unlock()

	col := game.BestMove(board, piece, 4)
	if col < 0 {
		return
	}
	go func() {
		time.Sleep(moveDelay)
		c.drop(col)
	}()
}

// applyState 用服务端全量状态覆盖本地镜像
func (c *Client) applyState(state *protos.GameStateResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.board = game.DecodeBoard(state.Board.GetCells())
	c.started = true
	for _, p := range state.Players {
		if p.Uid == c.uid {
			c.seat = int(p.Seat)
		}
	}
}

func (c *Client) logf(format string, args ...interface{}) {
	log.Printf("[%s] "+format, append([]interface{}{c.name}, args...)...)
}

// ==================== 展示辅助 ====================

// renderBoard 以文本形式画出棋盘, 获胜连线用大写标记
func renderBoard(b *game.Board, line []*protos.Cell) string {
	win := make(map[int]bool, len(line))
	for _, c := range line {
		win[int(c.Row)*game.Cols+int(c.Col)] = true
	}
	var sb strings.Builder
	for row := game.Rows - 1; row >= 0; row-- {
		sb.WriteString("    |")
		for col := 0; col < game.Cols; col++ {
			ch := "."
			switch b.At(col, row) {
			case game.Red:
				ch = "o"
				if win[row*game.Cols+col] {
					ch = "O"
				}
			case game.Yellow:
				ch = "x"
				if win[row*game.Cols+col] {
					ch = "X"
				}
			}
			sb.WriteString(ch + "|")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("     0 1 2 3 4 5 6   (o/O=红先手, x/X=黄后手)")
	return sb.String()
}

func describeOpponents(players []*protos.PlayerInfo, myUID string) string {
	var parts []string
	for _, p := range players {
		if p.Uid == myUID {
			continue
		}
		tag := ""
		if p.IsAi {
			tag = "[AI]"
		}
		parts = append(parts, fmt.Sprintf("%s%s(座位%d)", p.Name, tag, p.Seat))
	}
	return strings.Join(parts, ", ")
}

// decodeServerError 解析 pitaya 的错误应答体。
// pitaya 的 protos.Error 是旧版 protoc-gen-go 产物, 需经 protoadapt 适配到 v2 API。
func decodeServerError(data []byte) string {
	var e pitayaprotos.Error
	if err := proto.Unmarshal(data, protoadapt.MessageV2Of(&e)); err == nil && (e.Code != "" || e.Msg != "") {
		return fmt.Sprintf("%s %s", e.Code, e.Msg)
	}
	return string(data)
}

func short(uid string) string {
	if len(uid) > 8 {
		return uid[:8]
	}
	return uid
}
