package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"richman/protos"
	"richman/services"
	"richman/store"

	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/acceptor"
	"github.com/topfreegames/pitaya/v2/cluster"
	"github.com/topfreegames/pitaya/v2/config"
	"github.com/topfreegames/pitaya/v2/groups"
	"github.com/topfreegames/pitaya/v2/route"
	"github.com/topfreegames/pitaya/v2/serialize/protobuf"
)

var app pitaya.Pitaya

func main() {
	port := flag.Int("port", 3250, "前端监听端口")
	svType := flag.String("type", "connector", "服务器类型: connector / lobby / game")
	isFrontend := flag.Bool("frontend", true, "是否为前端服务器")
	redisAddr := flag.String("redis", "127.0.0.1:6379", "Redis 地址(空=禁用快照/恢复, 退化为 Phase 1)")
	flag.Parse()

	log.Printf("启动 %s 服务器 (frontend=%v, port=%d)", *svType, *isFrontend, *port)

	conf := config.NewDefaultPitayaConfig()
	conf.Heartbeat.Interval = 5 * time.Second
	// 缩短服务发现心跳/同步周期, 让节点宕机能较快被感知(demo 用, 生产按需)
	conf.Cluster.SD.Etcd.Heartbeat.TTL = 5 * time.Second
	conf.Cluster.SD.Etcd.SyncServers.Interval = 5 * time.Second

	// 初始化 Redis(可选): Ping 失败则禁用, 退化为 Phase 1 行为
	var st *store.RedisStore
	if *redisAddr != "" {
		st = store.NewRedisStore(*redisAddr)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := st.Ping(ctx); err != nil {
			log.Printf("Redis(%s) 不可用: %v, 禁用快照/恢复", *redisAddr, err)
			st = nil
		} else {
			log.Printf("Redis 已连接: %s", *redisAddr)
		}
		cancel()
	}

	builder := pitaya.NewDefaultBuilder(*isFrontend, *svType, pitaya.Cluster, map[string]string{}, *conf)
	// 全链路使用 protobuf 序列化
	builder.Serializer = protobuf.NewSerializer()
	builder.Groups = groups.NewMemoryGroupService(builder.Config.Groups.Memory)

	if *isFrontend {
		builder.AddAcceptor(acceptor.NewTCPAcceptor(fmt.Sprintf(":%d", *port)))
	}

	app = builder.Build()
	defer app.Shutdown()

	var gameHandler *services.GameHandler
	if *isFrontend {
		configureFrontend()
	} else {
		gameHandler = configureBackend(*svType, st)
	}

	app.Start() // 阻塞至收到信号, 内部完成优雅关机后返回

	// 优雅关机后: 将本节点剩余房间快照落盘, 尽量减少数据丢失
	if gameHandler != nil {
		gameHandler.FlushAll()
	}
}

// configureFrontend 配置前端 connector
func configureFrontend() {
	services.RegisterConnectorServices(app)

	// game 请求的粘性路由: owner 已在匹配阶段(lobby)写入 session.gameServer,
	// 直接读取返回对应节点(不在路由时现算哈希, 避免多 connector 视图不一致)。
	// 若 owner 已失联(宕机): 惰性恢复——询问 lobby 重指派并从 Redis 快照恢复。
	err := app.AddRoute("game", func(
		ctx context.Context,
		r *route.Route,
		payload []byte,
		servers map[string]*cluster.Server,
	) (*cluster.Server, error) {
		s := app.GetSessionFromCtx(ctx)
		if s == nil {
			return nil, fmt.Errorf("无会话, 无法路由 game 请求")
		}
		id, _ := s.Get("gameServer").(string)
		if id == "" {
			return nil, fmt.Errorf("尚未匹配房间(session 无 gameServer), 请先调用 lobby.join")
		}
		if sv, ok := servers[id]; ok {
			return sv, nil
		}

		// owner 失联 -> 惰性恢复: 询问 lobby 重指派
		roomID, _ := s.Get("roomId").(string)
		if roomID == "" {
			return nil, fmt.Errorf("owner %s 失联且 session 无 roomId, 无法恢复", id)
		}
		resp := &protos.ResolveOwnerResponse{}
		if err := app.RPC(ctx, "lobby.lobbyremote.resolveowner", resp, &protos.ResolveOwnerRequest{RoomId: roomID}); err != nil {
			return nil, fmt.Errorf("请求恢复 owner 失败: %v", err)
		}
		if resp.Code != protos.ResultCode_RESULT_OK || resp.Owner == "" {
			return nil, fmt.Errorf("恢复 owner 失败: %s", resp.Msg)
		}
		_ = s.Set("gameServer", resp.Owner) // 前端本地生效, 后续请求直达新 owner
		log.Printf("[connector] room=%s owner 失联(%s) -> 重指派 %s", roomID, id, resp.Owner)
		if sv, ok := servers[resp.Owner]; ok {
			return sv, nil
		}
		return nil, fmt.Errorf("新 owner %s 尚未出现在路由表, 请重试", resp.Owner)
	})
	if err != nil {
		log.Printf("添加路由失败: %s", err.Error())
	}
	log.Println("connector 前端服务配置完成")
}

// configureBackend 配置后端服务(game 或 lobby); game 返回 handler 供关机 flush
func configureBackend(svType string, st *store.RedisStore) *services.GameHandler {
	switch svType {
	case "lobby":
		services.RegisterLobbyServices(app, st)
		log.Println("lobby 匹配服务配置完成")
		return nil
	default:
		h := services.RegisterGameServices(app, st)
		log.Println("game 后端服务配置完成")
		return h
	}
}
