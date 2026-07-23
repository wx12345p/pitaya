package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"richman/services"

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
	svType := flag.String("type", "connector", "服务器类型: connector 或 game")
	isFrontend := flag.Bool("frontend", true, "是否为前端服务器")
	flag.Parse()

	log.Printf("启动 %s 服务器 (frontend=%v, port=%d)", *svType, *isFrontend, *port)

	conf := config.NewDefaultPitayaConfig()
	conf.Heartbeat.Interval = 5 * time.Second

	builder := pitaya.NewDefaultBuilder(*isFrontend, *svType, pitaya.Cluster, map[string]string{}, *conf)
	// 全链路使用 protobuf 序列化
	builder.Serializer = protobuf.NewSerializer()
	builder.Groups = groups.NewMemoryGroupService(builder.Config.Groups.Memory)

	if *isFrontend {
		builder.AddAcceptor(acceptor.NewTCPAcceptor(fmt.Sprintf(":%d", *port)))
	}

	app = builder.Build()
	defer app.Shutdown()

	if *isFrontend {
		configureFrontend()
	} else {
		configureBackend()
	}

	app.Start()
}

// configureFrontend 配置前端 connector
func configureFrontend() {
	services.RegisterConnectorServices(app)

	// 将 game 类型的请求路由到后端 game 服务器
	err := app.AddRoute("game", func(
		ctx context.Context,
		r *route.Route,
		payload []byte,
		servers map[string]*cluster.Server,
	) (*cluster.Server, error) {
		for k := range servers {
			return servers[k], nil
		}
		return nil, fmt.Errorf("没有可用的 game 服务器")
	})
	if err != nil {
		log.Printf("添加路由失败: %s", err.Error())
	}
	log.Println("connector 前端服务配置完成")
}

// configureBackend 配置后端 game
func configureBackend() {
	services.RegisterGameServices(app)
	log.Println("game 后端服务配置完成")
}
