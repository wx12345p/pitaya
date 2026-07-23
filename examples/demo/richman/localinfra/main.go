// Command localinfra 启动本地测试用的 etcd(:2379) 与 NATS(:4222)。
// 适用于没有 docker 的环境, 便于本地联调 richman 示例。
package main

import (
	"log"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"go.etcd.io/etcd/server/v3/embed"
)

func main() {
	// 启动 NATS
	nopts := &natsserver.Options{Host: "127.0.0.1", Port: 4222}
	ns, err := natsserver.NewServer(nopts)
	if err != nil {
		log.Fatalf("创建 NATS 失败: %v", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		log.Fatal("NATS 启动超时")
	}
	log.Println("NATS 已启动: nats://127.0.0.1:4222")

	// 启动嵌入式 etcd
	dir, err := os.MkdirTemp("", "richman-etcd-")
	if err != nil {
		log.Fatalf("创建 etcd 数据目录失败: %v", err)
	}
	cfg := embed.NewConfig()
	cfg.Dir = dir
	cfg.LogLevel = "error"
	lc, _ := url.Parse("http://127.0.0.1:2379")
	lp, _ := url.Parse("http://127.0.0.1:2380")
	cfg.ListenClientUrls = []url.URL{*lc}
	cfg.AdvertiseClientUrls = []url.URL{*lc}
	cfg.ListenPeerUrls = []url.URL{*lp}
	cfg.AdvertisePeerUrls = []url.URL{*lp}
	cfg.InitialCluster = "default=http://127.0.0.1:2380"

	e, err := embed.StartEtcd(cfg)
	if err != nil {
		log.Fatalf("启动 etcd 失败: %v", err)
	}
	defer e.Close()
	select {
	case <-e.Server.ReadyNotify():
		log.Println("etcd 已启动: http://127.0.0.1:2379")
	case <-time.After(30 * time.Second):
		e.Server.Stop()
		log.Fatal("etcd 启动超时")
	}

	log.Println("本地基础设施就绪, 按 Ctrl+C 退出")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ns.Shutdown()
	log.Println("已关闭")
}
