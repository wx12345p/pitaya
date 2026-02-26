package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/topfreegames/pitaya/v3/examples/demo/landlord/game"
	"github.com/topfreegames/pitaya/v3/examples/demo/landlord/protos"
	"github.com/topfreegames/pitaya/v3/pkg/client"
	"github.com/topfreegames/pitaya/v3/pkg/conn/message"
)

const serverAddr = "localhost:3250"

// GamePlayer 测试客户端玩家
type GamePlayer struct {
	name      string
	uid       string
	seat      int32
	roomID    string
	cards     []int32
	client    *client.Client
	connected bool
	mu        sync.Mutex
}

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)
	log.Println("=== 斗地主测试客户端 ===")
	log.Println("连接服务器:", serverAddr)

	// 创建3个玩家
	players := []*GamePlayer{
		{name: "张三"},
		{name: "李四"},
		{name: "王五"},
	}

	var wg sync.WaitGroup
	for _, p := range players {
		wg.Add(1)
		go func(player *GamePlayer) {
			defer wg.Done()
			err := runPlayer(player)
			if err != nil {
				log.Printf("[%s] 错误: %v", player.name, err)
			}
		}(p)
		// 间隔连接，避免同时操作
		time.Sleep(500 * time.Millisecond)
	}

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("收到退出信号，断开所有连接...")
		for _, p := range players {
			if p.client != nil {
				p.client.Disconnect()
			}
		}
		os.Exit(0)
	}()

	wg.Wait()
	log.Println("=== 所有玩家已完成游戏 ===")

	// 等待一下让最后的消息处理完
	time.Sleep(2 * time.Second)
}

func runPlayer(player *GamePlayer) error {
	// 创建客户端
	c := client.New(logrus.WarnLevel, 10*time.Second)
	player.client = c

	// 连接服务器
	log.Printf("[%s] 正在连接服务器...", player.name)
	err := c.ConnectTo(serverAddr)
	if err != nil {
		return fmt.Errorf("连接失败: %v", err)
	}
	player.connected = true
	log.Printf("[%s] 连接成功!", player.name)

	// 启动消息处理循环
	go handleMessages(player)

	// 等待一下确保消息处理循环启动
	time.Sleep(200 * time.Millisecond)

	// 1. 登录
	log.Printf("[%s] 正在登录...", player.name)
	loginReq, _ := json.Marshal(map[string]string{"playerName": player.name})
	reqID, err := c.SendRequest("connector.login", loginReq)
	if err != nil {
		return fmt.Errorf("登录请求失败: %v", err)
	}
	log.Printf("[%s] 登录请求已发送 (reqID=%d)", player.name, reqID)

	// 等待登录响应
	time.Sleep(1 * time.Second)

	if player.uid == "" {
		return fmt.Errorf("登录超时")
	}

	// 2. 加入游戏匹配
	log.Printf("[%s] 正在加入匹配...", player.name)
	joinReq, _ := json.Marshal(&protos.JoinRequest{PlayerName: player.name})
	reqID, err = c.SendRequest("game.game.join", joinReq)
	if err != nil {
		return fmt.Errorf("加入匹配请求失败: %v", err)
	}
	log.Printf("[%s] 加入匹配请求已发送 (reqID=%d)", player.name, reqID)

	// 保持连接，等待游戏结束
	// 游戏逻辑由消息处理循环驱动
	for player.connected {
		time.Sleep(1 * time.Second)
	}

	return nil
}

func handleMessages(player *GamePlayer) {
	for msg := range player.client.MsgChannel() {
		switch msg.Type {
		case message.Response:
			handleResponse(player, msg)
		case message.Push:
			handlePush(player, msg)
		}
	}
	player.connected = false
	log.Printf("[%s] 连接已断开", player.name)
}

func handleResponse(player *GamePlayer, msg *message.Message) {
	route := msg.Route

	switch route {
	case "connector.login":
		var resp struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			UID     string `json:"uid"`
		}
		json.Unmarshal(msg.Data, &resp)
		if resp.Code == 0 {
			player.mu.Lock()
			player.uid = resp.UID
			player.mu.Unlock()
			log.Printf("[%s] 登录成功, UID=%s", player.name, resp.UID)
		} else {
			log.Printf("[%s] 登录失败: %s", player.name, resp.Message)
		}

	case "game.game.join":
		var resp protos.JoinResponse
		json.Unmarshal(msg.Data, &resp)
		if resp.Code == 0 {
			player.mu.Lock()
			player.roomID = resp.RoomId
			player.seat = resp.Seat
			player.mu.Unlock()
			log.Printf("[%s] 加入房间 %s 座位 %d", player.name, resp.RoomId, resp.Seat)
		} else {
			log.Printf("[%s] 加入匹配失败: %s", player.name, resp.Message)
		}

	case "game.game.bid":
		var resp protos.BidResponse
		json.Unmarshal(msg.Data, &resp)
		log.Printf("[%s] 叫分响应: %s", player.name, resp.Message)

	case "game.game.play":
		var resp protos.PlayResponse
		json.Unmarshal(msg.Data, &resp)
		if resp.Code != 0 {
			log.Printf("[%s] 出牌失败: %s", player.name, resp.Message)
		}

	case "game.game.pass":
		var resp protos.PassResponse
		json.Unmarshal(msg.Data, &resp)

	default:
		log.Printf("[%s] 收到响应: route=%s data=%s", player.name, route, string(msg.Data))
	}
}

func handlePush(player *GamePlayer, msg *message.Message) {
	route := msg.Route

	switch route {
	case "onGameStart":
		var push protos.GameStartPush
		json.Unmarshal(msg.Data, &push)
		player.mu.Lock()
		player.cards = push.YourCards
		player.mu.Unlock()

		cards := game.CardsFromIDs32(push.YourCards)
		game.SortCards(cards)
		log.Printf("[%s] ===== 游戏开始 =====", player.name)
		log.Printf("[%s] 你的手牌 (%d张): %s", player.name, len(cards), game.CardsString(cards))
		log.Printf("[%s] 玩家列表:", player.name)
		for _, p := range push.Players {
			if p == nil {
				continue
			}
			aiTag := ""
			if p.IsAi {
				aiTag = " [AI]"
			}
			log.Printf("[%s]   座位%d: %s%s", player.name, p.Seat, p.Name, aiTag)
		}
		log.Printf("[%s] 首先叫分: 座位%d", player.name, push.FirstBidder)

	case "onBidTurn":
		var push protos.BidTurnPush
		json.Unmarshal(msg.Data, &push)
		log.Printf("[%s] 轮到座位%d叫分 (当前最高%d分)", player.name, push.Seat, push.MaxBid)

		// 如果轮到自己叫分
		if push.Seat == player.seat {
			go func() {
				time.Sleep(500 * time.Millisecond)
				decideBid(player, int(push.MaxBid))
			}()
		}

	case "onBidResult":
		var push protos.BidResultPush
		json.Unmarshal(msg.Data, &push)
		if push.Score == 0 {
			log.Printf("[%s] %s (座位%d) 不叫", player.name, push.Name, push.Seat)
		} else {
			log.Printf("[%s] %s (座位%d) 叫 %d 分", player.name, push.Name, push.Seat, push.Score)
		}

	case "onLandlordDecided":
		var push protos.LandlordDecidedPush
		json.Unmarshal(msg.Data, &push)

		player.mu.Lock()
		player.cards = push.YourCards
		player.mu.Unlock()

		bottomCards := game.CardsFromIDs32(push.BottomCards)
		log.Printf("[%s] ===== 地主确定 =====", player.name)
		log.Printf("[%s] 地主: %s (座位%d), 叫%d分", player.name, push.LandlordName, push.LandlordSeat, push.BidScore)
		log.Printf("[%s] 底牌: %s", player.name, game.CardsString(bottomCards))

		if push.LandlordSeat == player.seat {
			cards := game.CardsFromIDs32(push.YourCards)
			game.SortCards(cards)
			log.Printf("[%s] 你是地主! 当前手牌 (%d张): %s", player.name, len(cards), game.CardsString(cards))
		}

	case "onPlayTurn":
		var push protos.PlayTurnPush
		json.Unmarshal(msg.Data, &push)

		if push.LastSeat >= 0 && len(push.LastCards) > 0 {
			lastCards := game.CardsFromIDs32(push.LastCards)
			log.Printf("[%s] 轮到座位%d出牌 (上家座位%d出了: %s)", player.name, push.Seat, push.LastSeat, game.CardsString(lastCards))
		} else {
			log.Printf("[%s] 轮到座位%d出牌 (自由出牌)", player.name, push.Seat)
		}

		// 如果轮到自己出牌
		if push.Seat == player.seat {
			go func() {
				time.Sleep(800 * time.Millisecond)
				decidePlay(player, &push)
			}()
		}

	case "onCardPlayed":
		var push protos.CardPlayedPush
		json.Unmarshal(msg.Data, &push)
		cards := game.CardsFromIDs32(push.Cards)
		log.Printf("[%s] %s (座位%d) 出牌: %s [%s] 剩余%d张",
			player.name, push.Name, push.Seat, game.CardsString(cards), push.HandType, push.CardCount)

	case "onPass":
		var push protos.PassPush
		json.Unmarshal(msg.Data, &push)
		log.Printf("[%s] %s (座位%d) 不出", player.name, push.Name, push.Seat)

	case "onGameEnd":
		var push protos.GameEndPush
		json.Unmarshal(msg.Data, &push)
		log.Printf("[%s] ===== 游戏结束 =====", player.name)
		log.Printf("[%s] 赢家: %s (座位%d)", player.name, push.WinnerName, push.WinnerSeat)
		if push.LandlordWin {
			log.Printf("[%s] 地主胜利!", player.name)
		} else {
			log.Printf("[%s] 农民胜利!", player.name)
		}
		log.Printf("[%s] 叫分:%d 炸弹:%d 春天:%v", player.name, push.BidScore, push.BombCount, push.Spring)

		// 游戏结束，断开连接
		time.Sleep(2 * time.Second)
		player.client.Disconnect()
		player.connected = false

	default:
		log.Printf("[%s] 收到推送: route=%s data=%s", player.name, route, string(msg.Data))
	}
}

// decideBid 玩家叫分（简单策略：根据手牌大致决定）
func decideBid(player *GamePlayer, maxBid int) {
	player.mu.Lock()
	cards := game.CardsFromIDs32(player.cards)
	player.mu.Unlock()

	// 简单策略
	rankCount := game.GetRankCount(cards)
	bombCount := 0
	for _, count := range rankCount {
		if count == 4 {
			bombCount++
		}
	}
	hasSmallJoker := rankCount[game.RankSmallJoker] > 0
	hasBigJoker := rankCount[game.RankBigJoker] > 0

	score := 0
	if hasSmallJoker && hasBigJoker {
		score = 3
	} else if bombCount >= 1 {
		score = 2
	} else if hasSmallJoker || hasBigJoker {
		score = 1
	}

	if score <= maxBid {
		score = 0
	}

	log.Printf("[%s] 决定叫分: %d", player.name, score)

	bidReq, _ := json.Marshal(&protos.BidRequest{Score: int32(score)})
	_, err := player.client.SendRequest("game.game.bid", bidReq)
	if err != nil {
		log.Printf("[%s] 叫分请求失败: %v", player.name, err)
	}
}

// decidePlay 玩家出牌（使用简单AI策略）
func decidePlay(player *GamePlayer, turnInfo *protos.PlayTurnPush) {
	player.mu.Lock()
	cards := game.CardsFromIDs32(player.cards)
	player.mu.Unlock()

	game.SortCards(cards)

	isFirstPlay := turnInfo.LastSeat < 0 || turnInfo.LastSeat == player.seat
	var lastCards []game.Card
	var lastHandInfo game.HandInfo

	if !isFirstPlay && len(turnInfo.LastCards) > 0 {
		lastCards = game.CardsFromIDs32(turnInfo.LastCards)
		lastHandInfo = game.DetectHandType(lastCards)
	}

	// 使用 AI 来决定出牌
	ai := game.NewSimpleAI()
	p := &game.Player{Cards: cards}
	playCards := ai.DecidePlay(p, lastCards, lastHandInfo, isFirstPlay)

	if playCards == nil {
		// 不出
		log.Printf("[%s] 决定不出", player.name)
		passReq, _ := json.Marshal(&protos.PassRequest{})
		_, err := player.client.SendRequest("game.game.pass", passReq)
		if err != nil {
			log.Printf("[%s] 不出请求失败: %v", player.name, err)
		}
	} else {
		log.Printf("[%s] 决定出牌: %s", player.name, game.CardsString(playCards))
		cardIDs := game.CardsToIDs32(playCards)

		// 更新本地手牌
		player.mu.Lock()
		remainCards := game.RemoveCards(cards, playCards)
		player.cards = game.CardsToIDs32(remainCards)
		player.mu.Unlock()

		playReq, _ := json.Marshal(&protos.PlayRequest{CardIds: cardIDs})
		_, err := player.client.SendRequest("game.game.play", playReq)
		if err != nil {
			log.Printf("[%s] 出牌请求失败: %v", player.name, err)
		}
	}
}
