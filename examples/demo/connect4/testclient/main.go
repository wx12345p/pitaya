// Command testclient 是四子棋示例的自动化测试客户端。
//
// 用 -scenario 选择验收场景:
//
//	duel      两名真人玩家(分别连不同 connector)对战到出现四连或平局
//	ai        单个玩家匹配, 等待超时后由 AI 补位并走完整局
//	reconnect 双人对战, 其中一方中途断线并重连, 恢复棋盘继续走完
//	spectate  双人对战 + 第三个客户端按房间号观战
//	failover  双人对战(放慢节奏), 供手动 kill game 节点验证快照恢复
//	cancel    排队中取消匹配, 并验证重复取消被拒绝
//	timeout   一方全程不落子, 验证超时代打与累计 3 次超时判负
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	scenario := flag.String("scenario", "duel", "验收场景: duel / ai / reconnect / spectate / failover / cancel / timeout")
	addrList := flag.String("addrs", "localhost:4250,localhost:4260", "connector 地址列表(逗号分隔)")
	timeout := flag.Duration("timeout", 3*time.Minute, "场景等待上限")
	flag.Parse()
	log.SetFlags(log.Ltime)

	addrs := strings.Split(*addrList, ",")
	for i := range addrs {
		addrs[i] = strings.TrimSpace(addrs[i])
	}
	if len(addrs) == 0 || addrs[0] == "" {
		log.Fatal("请用 -addrs 指定至少一个 connector 地址")
	}

	log.Printf("=== 四子棋测试客户端 场景=%s connector=%v ===", *scenario, addrs)
	var clients []*Client
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	go handleSignals(&clients)

	switch *scenario {
	case "duel":
		clients = runDuel(addrs, *timeout)
	case "ai":
		clients = runAI(addrs, *timeout)
	case "reconnect":
		clients = runReconnect(addrs, *timeout)
	case "spectate":
		clients = runSpectate(addrs, *timeout)
	case "failover":
		clients = runFailover(addrs, *timeout)
	case "cancel":
		clients = runCancel(addrs)
	case "timeout":
		clients = runTimeout(addrs, *timeout)
	default:
		log.Fatalf("未知场景: %s", *scenario)
	}
	time.Sleep(500 * time.Millisecond)
}

// runDuel 场景1: 双人对局
func runDuel(addrs []string, timeout time.Duration) []*Client {
	a := mustStart("阿豆", pick(addrs, 0))
	time.Sleep(300 * time.Millisecond)
	b := mustStart("小圆", pick(addrs, 1))
	report(waitOver(timeout, a, b), a, b)
	return []*Client{a, b}
}

// runAI 场景2: 无对手时 AI 补位
func runAI(addrs []string, timeout time.Duration) []*Client {
	log.Printf("单人匹配, 预计 %v 后由 AI 补位", 10*time.Second)
	a := mustStart("阿豆", pick(addrs, 0))
	report(waitOver(timeout, a), a)
	return []*Client{a}
}

// runReconnect 场景3: 一方中途断线重连
func runReconnect(addrs []string, timeout time.Duration) []*Client {
	a := mustStart("阿豆", pick(addrs, 0))
	time.Sleep(300 * time.Millisecond)
	b := mustStart("小圆", pick(addrs, 1))

	go func() {
		time.Sleep(5 * time.Second)
		select {
		case <-b.Done():
			return
		default:
		}
		b.logf("===== 模拟网络中断 =====")
		b.Disconnect()
		time.Sleep(3 * time.Second)
		b.logf("===== 尝试重连 =====")
		if err := b.Reconnect(); err != nil {
			b.logf("重连失败: %v", err)
		}
	}()

	report(waitOver(timeout, a, b), a, b)
	return []*Client{a, b}
}

// runSpectate 场景4: 第三方观战
func runSpectate(addrs []string, timeout time.Duration) []*Client {
	moveDelay = time.Second // 放慢节奏, 让观战者能看到过程中的落子
	a := mustStart("阿豆", pick(addrs, 0))
	time.Sleep(300 * time.Millisecond)
	b := mustStart("小圆", pick(addrs, 1))

	s := NewClient("老王", pick(addrs, 2))
	if err := s.Connect(); err != nil {
		log.Fatalf("观战者连接失败: %v", err)
	}
	if err := s.Login(""); err != nil {
		log.Fatalf("观战者登录失败: %v", err)
	}
	roomID := ""
	for i := 0; i < 20 && roomID == ""; i++ {
		if resp, err := s.RoomList(10); err == nil && len(resp.Rooms) > 0 {
			roomID = resp.Rooms[0].RoomId
			log.Printf("[老王] 可观战房间 %d 个, 选择 %s (已下%d手)", len(resp.Rooms), roomID, resp.Rooms[0].MoveNo)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if roomID == "" {
		log.Println("[老王] 未查询到可观战房间(检查 Redis 是否启用)")
	} else if err := s.Spectate(roomID); err != nil {
		log.Printf("[老王] 观战失败: %v", err)
	}

	report(waitOver(timeout, a, b, s), a, b, s)
	return []*Client{a, b, s}
}

// runFailover 场景5: owner 宕机后从快照恢复
func runFailover(addrs []string, timeout time.Duration) []*Client {
	moveDelay = 3 * time.Second
	a := mustStart("阿豆", pick(addrs, 0))
	time.Sleep(300 * time.Millisecond)
	b := mustStart("小圆", pick(addrs, 1))
	log.Println("提示: 现在可以 kill 掉打印了\"建房\"日志的那个 game 节点;")
	log.Println("      客户端心跳会触发 connector -> lobby 重指派, 新节点从 Redis 快照恢复并续跑。")
	report(waitOver(timeout, a, b), a, b)
	return []*Client{a, b}
}

// runCancel 场景6: 排队中取消匹配
func runCancel(addrs []string) []*Client {
	c := NewClient("阿豆", pick(addrs, 0))
	if err := c.Connect(); err != nil {
		log.Fatalf("[阿豆] %v", err)
	}
	if err := c.Login(""); err != nil {
		log.Fatalf("[阿豆] %v", err)
	}
	if err := c.Match(); err != nil {
		log.Fatalf("[阿豆] %v", err)
	}
	time.Sleep(time.Second)
	if err := c.CancelMatch(); err != nil {
		log.Printf("场景未通过: %v", err)
		return []*Client{c}
	}
	// 再次取消应当失败(已不在队列中)
	if err := c.CancelMatch(); err == nil {
		log.Println("场景未通过: 重复取消竟然成功了")
		return []*Client{c}
	}
	log.Println("重复取消被正确拒绝")
	log.Println("场景通过")
	return []*Client{c}
}

// runTimeout 场景7: 一方始终不落子, 验证超时代打与 3 次超时判负
func runTimeout(addrs []string, timeout time.Duration) []*Client {
	log.Println("小圆将全程不落子: 预期每 15 秒被 AI 代打一次, 累计 3 次后判负")
	a := mustStart("阿豆", pick(addrs, 0))
	time.Sleep(300 * time.Millisecond)
	b := NewClient("小圆", pick(addrs, 1))
	b.passive = true
	if err := b.Connect(); err != nil {
		log.Fatalf("[小圆] %v", err)
	}
	if err := b.Login(""); err != nil {
		log.Fatalf("[小圆] %v", err)
	}
	if err := b.Match(); err != nil {
		log.Fatalf("[小圆] %v", err)
	}
	report(waitOver(timeout, a, b), a, b)
	return []*Client{a, b}
}

// ==================== 辅助 ====================

func mustStart(name, addr string) *Client {
	c := NewClient(name, addr)
	if err := c.Connect(); err != nil {
		log.Fatalf("[%s] %v", name, err)
	}
	if err := c.Login(""); err != nil {
		log.Fatalf("[%s] %v", name, err)
	}
	if err := c.Match(); err != nil {
		log.Fatalf("[%s] %v", name, err)
	}
	c.StartHeartbeat(3 * time.Second)
	return c
}

func pick(addrs []string, i int) string { return addrs[i%len(addrs)] }

func waitOver(timeout time.Duration, clients ...*Client) bool {
	deadline := time.After(timeout)
	for _, c := range clients {
		select {
		case <-c.Done():
		case <-deadline:
			log.Printf("[%s] 等待超时, 未收到对局结束推送", c.name)
			return false
		}
	}
	return true
}

func report(ok bool, clients ...*Client) {
	log.Println("================ 场景结果 ================")
	for _, c := range clients {
		res := c.Result()
		if res == "" {
			res = "未收到结束推送"
		}
		log.Printf("  %s: %s", c.name, res)
	}
	if ok {
		log.Println("场景通过")
	} else {
		log.Println("场景未在预期时间内完成")
	}
}

func handleSignals(clients *[]*Client) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	for _, c := range *clients {
		c.Close()
	}
	os.Exit(0)
}
