package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"richman/protos"

	"github.com/sirupsen/logrus"
	"github.com/topfreegames/pitaya/v2/client"
	"github.com/topfreegames/pitaya/v2/conn/message"
	"google.golang.org/protobuf/proto"
)

const serverAddr = "localhost:3250"

// GamePlayer 测试客户端玩家
type GamePlayer struct {
	name      string
	uid       string
	seat      int32
	roomID    string
	client    *client.Client
	connected bool
	mu        sync.Mutex

	// pitaya 的 Response 消息不携带 route, 需自行按请求ID关联
	reqRoutes map[uint]string
}

// send 发送请求并记录 reqID->route 映射(用于关联响应)
func (p *GamePlayer) send(route string, data []byte) {
	reqID, err := p.client.SendRequest(route, data)
	if err != nil {
		log.Printf("[%s] 发送 %s 失败: %v", p.name, route, err)
		return
	}
	p.mu.Lock()
	if p.reqRoutes == nil {
		p.reqRoutes = make(map[uint]string)
	}
	p.reqRoutes[reqID] = route
	p.mu.Unlock()
}

func main() {
	log.SetFlags(log.Ltime)
	log.Println("=== 大富翁10 测试客户端 (protobuf) ===")

	players := []*GamePlayer{
		{name: "阿福", reqRoutes: make(map[uint]string)},
		{name: "小美", reqRoutes: make(map[uint]string)},
		{name: "大熊", reqRoutes: make(map[uint]string)},
		{name: "阿强", reqRoutes: make(map[uint]string)},
	}

	var wg sync.WaitGroup
	for _, p := range players {
		wg.Add(1)
		go func(player *GamePlayer) {
			defer wg.Done()
			if err := runPlayer(player); err != nil {
				log.Printf("[%s] 错误: %v", player.name, err)
			}
		}(p)
		time.Sleep(400 * time.Millisecond)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		for _, p := range players {
			if p.client != nil {
				p.client.Disconnect()
			}
		}
		os.Exit(0)
	}()

	wg.Wait()
	log.Println("=== 所有玩家已结束 ===")
	time.Sleep(1 * time.Second)
}

func runPlayer(player *GamePlayer) error {
	c := client.New(logrus.WarnLevel, 10*time.Second)
	player.client = c

	if err := c.ConnectTo(serverAddr); err != nil {
		return fmt.Errorf("连接失败: %v", err)
	}
	player.connected = true
	log.Printf("[%s] 已连接", player.name)

	go handleMessages(player)
	time.Sleep(200 * time.Millisecond)

	// 登录
	loginReq, _ := proto.Marshal(&protos.LoginRequest{PlayerName: player.name})
	player.send("connector.login", loginReq)
	time.Sleep(1 * time.Second)
	if player.uid == "" {
		return fmt.Errorf("登录超时")
	}

	// 进房
	joinReq, _ := proto.Marshal(&protos.JoinRoomRequest{PlayerName: player.name})
	player.send("game.game.join", joinReq)

	for player.connected {
		time.Sleep(500 * time.Millisecond)
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
}

func handleResponse(player *GamePlayer, msg *message.Message) {
	player.mu.Lock()
	route := player.reqRoutes[msg.ID]
	delete(player.reqRoutes, msg.ID)
	player.mu.Unlock()

	switch route {
	case "connector.login":
		var resp protos.LoginResponse
		proto.Unmarshal(msg.Data, &resp)
		if resp.Code == protos.ResultCode_RESULT_OK {
			player.mu.Lock()
			player.uid = resp.Uid
			player.mu.Unlock()
			log.Printf("[%s] 登录成功 uid=%s", player.name, resp.Uid[:8])
		} else {
			log.Printf("[%s] 登录失败: %s", player.name, resp.Msg)
		}
	case "game.game.join":
		var resp protos.JoinRoomResponse
		proto.Unmarshal(msg.Data, &resp)
		if resp.Code == protos.ResultCode_RESULT_OK {
			player.mu.Lock()
			player.roomID = resp.RoomId
			player.seat = resp.Seat
			player.mu.Unlock()
			log.Printf("[%s] 进入房间 %s 座位 %d", player.name, resp.RoomId, resp.Seat)
		} else {
			log.Printf("[%s] 进房失败: %s", player.name, resp.Msg)
		}
	}
}

func handlePush(player *GamePlayer, msg *message.Message) {
	switch msg.Route {
	case "onRoomUpdate":
		var push protos.RoomUpdatePush
		proto.Unmarshal(msg.Data, &push)
		log.Printf("[%s] 房间人数 %d/4 (还需%d人)", player.name, len(push.Players), push.Need)

	case "onGameStart":
		var push protos.GameStartPush
		proto.Unmarshal(msg.Data, &push)
		log.Printf("[%s] ===== 游戏开始 (初始资金%d, 上限%d回合) =====", player.name, push.InitMoney, push.MaxRound)
		for _, p := range push.Players {
			log.Printf("[%s]   座位%d %s 资金%d", player.name, p.Seat, p.Name, p.Money)
		}

	case "onTurnStart":
		var push protos.TurnStartPush
		proto.Unmarshal(msg.Data, &push)
		if push.Uid == player.uid {
			log.Printf("[%s] >> 第%d回合 轮到我, 掷骰", player.name, push.Round)
			go func() {
				time.Sleep(500 * time.Millisecond)
				req, _ := proto.Marshal(&protos.RollDiceRequest{})
				player.send("game.game.rolldice", req)
			}()
		}

	case "onDiceResult":
		var push protos.DiceResultPush
		proto.Unmarshal(msg.Data, &push)
		if push.Uid == player.uid {
			extra := ""
			if push.PassStart {
				extra = fmt.Sprintf(" (过起点+%d)", push.Salary)
			}
			log.Printf("[%s]    掷出%d, 移动到格%d%s", player.name, push.Dice, push.ToPos, extra)
		}

	case "onLandTile":
		var push protos.LandTilePush
		proto.Unmarshal(msg.Data, &push)
		if push.Uid != player.uid {
			return
		}
		switch {
		case push.CanBuy:
			log.Printf("[%s]    落在【%s】可购买(价%d, 余%d), 购买", player.name, push.TileName, push.Price, push.Money)
			go func() {
				time.Sleep(400 * time.Millisecond)
				req, _ := proto.Marshal(&protos.BuyRequest{Buy: true})
				player.send("game.game.buy", req)
			}()
		case push.CanUpgrade:
			upgrade := push.Money > push.Price*2 // 保留一些现金
			log.Printf("[%s]    落在自己的【%s】可升级(费%d, 余%d), 升级=%v", player.name, push.TileName, push.Price, push.Money, upgrade)
			go func() {
				time.Sleep(400 * time.Millisecond)
				req, _ := proto.Marshal(&protos.UpgradeRequest{Upgrade: upgrade})
				player.send("game.game.upgrade", req)
			}()
		default:
			log.Printf("[%s]    落在【%s】(格%d)", player.name, push.TileName, push.Pos)
		}

	case "onPropertyChanged":
		var push protos.PropertyChangedPush
		proto.Unmarshal(msg.Data, &push)
		if push.Uid == player.uid {
			act := "购买"
			if push.Action == protos.PropertyAction_PROP_UPGRADE {
				act = "升级"
			}
			log.Printf("[%s]    %s成功, 地块%d -> %d级, 余额%d", player.name, act, push.Pos, push.Level, push.Money)
		}

	case "onPayToll":
		var push protos.PayTollPush
		proto.Unmarshal(msg.Data, &push)
		if push.FromUid == player.uid {
			if push.ToUid == "" {
				log.Printf("[%s]    缴税%d, 余额%d", player.name, push.Amount, push.FromMoney)
			} else {
				log.Printf("[%s]    支付过路费%d, 余额%d", player.name, push.Amount, push.FromMoney)
			}
		} else if push.ToUid == player.uid {
			log.Printf("[%s]    收取过路费%d, 余额%d", player.name, push.Amount, push.ToMoney)
		}

	case "onCardDrawn":
		var push protos.CardDrawnPush
		proto.Unmarshal(msg.Data, &push)
		if push.Uid == player.uid {
			log.Printf("[%s]    抽卡: %s, 余额%d", player.name, push.Desc, push.Money)
		}

	case "onPlayerBankrupt":
		var push protos.PlayerBankruptPush
		proto.Unmarshal(msg.Data, &push)
		if push.Uid == player.uid {
			log.Printf("[%s]    !!! 我破产淘汰了 !!!", player.name)
		} else {
			log.Printf("[%s]    座位%d 破产淘汰", player.name, push.Seat)
		}

	case "onGameEnd":
		var push protos.GameEndPush
		proto.Unmarshal(msg.Data, &push)
		log.Printf("[%s] ===== 对局结束 (%s) 胜者: %s =====", player.name, push.Reason.String(), push.WinnerName)
		for _, r := range push.Rankings {
			tag := ""
			if r.Bankrupt {
				tag = " [破产]"
			}
			log.Printf("[%s]   第%d名 %s 总资产%d%s", player.name, r.Rank, r.Name, r.TotalAsset, tag)
		}
		time.Sleep(1 * time.Second)
		player.client.Disconnect()
		player.connected = false
	}
}
