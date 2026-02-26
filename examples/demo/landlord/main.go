package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/topfreegames/pitaya/v3/examples/demo/landlord/services"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/acceptor"
	"github.com/topfreegames/pitaya/v3/pkg/cluster"
	"github.com/topfreegames/pitaya/v3/pkg/config"
	"github.com/topfreegames/pitaya/v3/pkg/groups"
	"github.com/topfreegames/pitaya/v3/pkg/route"
)

var app pitaya.Pitaya

func main() {
	port := flag.Int("port", 3250, "连接器监听端口")
	svType := flag.String("type", "connector", "服务器类型: connector 或 game")
	isFrontend := flag.Bool("frontend", true, "是否为前端服务器")
	flag.Parse()

	log.Printf("启动 %s 服务器 (frontend=%v, port=%d)", *svType, *isFrontend, *port)

	conf := config.NewDefaultPitayaConfig()
	conf.Heartbeat.Interval = time.Duration(5 * time.Second)
	builder := pitaya.NewDefaultBuilder(*isFrontend, *svType, pitaya.Cluster, map[string]string{}, *conf)

	if *isFrontend {
		tcp := acceptor.NewTCPAcceptor(fmt.Sprintf(":%d", *port))
		builder.AddAcceptor(tcp)
	}

	builder.Groups = groups.NewMemoryGroupService(builder.Config.Groups.Memory)
	app = builder.Build()

	defer app.Shutdown()

	if *isFrontend {
		configureFrontend()
	} else {
		configureBackend()
	}

	app.Start()
}

// configureFrontend 配置前端 Connector 服务器
func configureFrontend() {
	log.Println("配置 Connector 前端服务...")

	// 注册连接器服务
	services.RegisterConnectorServices(app)

	// 添加路由：将 game 类型的消息路由到 game 服务器
	err := app.AddRoute("game", func(
		ctx context.Context,
		r *route.Route,
		payload []byte,
		servers map[string]*cluster.Server,
	) (*cluster.Server, error) {
		// 简单策略：返回第一个可用的 game 服务器
		for k := range servers {
			return servers[k], nil
		}
		return nil, fmt.Errorf("没有可用的 game 服务器")
	})

	if err != nil {
		log.Printf("添加路由失败: %s", err.Error())
	}

	log.Println("Connector 前端服务配置完成")
}

// configureBackend 配置后端 Game 服务器
func configureBackend() {
	log.Println("配置 Game 后端服务...")

	// 注册游戏服务
	services.RegisterGameServices(app)

	log.Println("Game 后端服务配置完成")
}
