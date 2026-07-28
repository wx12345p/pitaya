// Command connect4 是基于 Pitaya v2 集群的四子棋示例服务器。
//
// 三类节点(以 -type 区分):
//
//	connector: 前端接入(TCP), 登录 + game.* 粘性路由
//	lobby    : 匹配撮合 + 重连/观战定位 + owner 重指派
//	game     : 对局状态机 + AI + 推送 + 状态快照
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"connect4/protos"
	"connect4/services"
	"connect4/store"

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
	port := flag.Int("port", 4250, "前端监听端口")
	svType := flag.String("type", "connector", "服务器类型: connector / lobby / game")
	isFrontend := flag.Bool("frontend", true, "是否为前端服务器")
	redisAddr := flag.String("redis", "127.0.0.1:6379", "Redis 地址(置空则禁用快照/重连/观战列表)")
	flag.Parse()

	log.Printf("启动 %s 节点 (frontend=%v, port=%d)", *svType, *isFrontend, *port)

	conf := config.NewDefaultPitayaConfig()
	conf.Heartbeat.Interval = 5 * time.Second
	// 缩短服务发现心跳/同步周期, 让节点宕机能较快被感知(demo 用, 生产按需调整)
	conf.Cluster.SD.Etcd.Heartbeat.TTL = 5 * time.Second
	conf.Cluster.SD.Etcd.SyncServers.Interval = 5 * time.Second

	st := initStore(*redisAddr)

	builder := pitaya.NewDefaultBuilder(*isFrontend, *svType, pitaya.Cluster, map[string]string{}, *conf)
	builder.Serializer = protobuf.NewSerializer() // 全链路 protobuf
	builder.Groups = groups.NewMemoryGroupService(builder.Config.Groups.Memory)
	if *isFrontend {
		builder.AddAcceptor(acceptor.NewTCPAcceptor(fmt.Sprintf(":%d", *port)))
	}

	app = builder.Build()
	defer app.Shutdown()

	var gameHandler *services.GameHandler
	if *isFrontend {
		configureFrontend(st)
	} else {
		gameHandler = configureBackend(*svType, st)
	}

	app.Start() // 阻塞至收到信号, 内部完成优雅关机后返回

	// 关机后把本节点剩余房间快照落盘, 便于其他节点接管
	if gameHandler != nil {
		gameHandler.FlushAll()
	}
}

// initStore 初始化 Redis; 不可用时返回 nil, 服务退化为无快照/无重连模式
func initStore(addr string) *store.RedisStore {
	if addr == "" {
		log.Println("未配置 Redis, 退化为内存撮合(无快照/无重连/无观战列表)")
		return nil
	}
	st := store.NewRedisStore(addr)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := st.Ping(ctx); err != nil {
		log.Printf("Redis(%s) 不可用: %v, 退化为内存撮合", addr, err)
		return nil
	}
	log.Printf("Redis 已连接: %s", addr)
	return st
}

// configureFrontend 配置前端 connector: 登录服务 + game.* 粘性路由
func configureFrontend(st *store.RedisStore) {
	services.RegisterConnectorServices(app, st)

	// game.* 的粘性路由: owner 已由 lobby 在匹配/重连/观战时写入 session.gameServer,
	// 这里直接读取(不现算哈希, 避免多 connector 视图不一致)。
	// owner 失联时走惰性恢复: 请求 lobby 重指派并从 Redis 快照恢复。
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
			return nil, fmt.Errorf("尚未进入房间, 请先调用 lobby.lobby.match / spectate / reconnect")
		}
		if sv, ok := servers[id]; ok {
			return sv, nil
		}

		roomID, _ := s.Get("roomId").(string)
		if roomID == "" {
			return nil, fmt.Errorf("owner %s 失联且 session 无 roomId, 无法恢复", id)
		}
		resp := &protos.ResolveOwnerResponse{}
		if err := app.RPC(ctx, "lobby.lobbyremote.resolveowner", resp, &protos.ResolveOwnerRequest{RoomId: roomID}); err != nil {
			return nil, fmt.Errorf("请求重指派 owner 失败: %v", err)
		}
		if resp.Code != protos.ResultCode_RESULT_OK || resp.Owner == "" {
			return nil, fmt.Errorf("重指派 owner 失败: %s", resp.Msg)
		}
		_ = s.Set("gameServer", resp.Owner) // 前端本地生效, 后续请求直达新 owner
		log.Printf("[connector] room=%s owner 失联(%s) -> 重指派 %s", roomID, id, resp.Owner)
		if sv, ok := servers[resp.Owner]; ok {
			return sv, nil
		}
		return nil, fmt.Errorf("新 owner %s 尚未出现在路由表, 请重试", resp.Owner)
	})
	if err != nil {
		log.Printf("添加路由失败: %v", err)
	}
	log.Println("connector 前端服务配置完成")
}

// configureBackend 配置后端节点; game 返回 handler 供关机 flush 快照
func configureBackend(svType string, st *store.RedisStore) *services.GameHandler {
	switch svType {
	case "lobby":
		services.RegisterLobbyServices(app, st)
		log.Println("lobby 匹配服务配置完成")
		return nil
	default:
		h := services.RegisterGameServices(app, st)
		log.Println("game 对局服务配置完成")
		return h
	}
}
