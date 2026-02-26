package services

import (
	"context"
	"log"
	"strings"

	"github.com/google/uuid"
	pitaya "github.com/topfreegames/pitaya/v3/pkg"
	"github.com/topfreegames/pitaya/v3/pkg/component"
)

// Connector 前端连接器Handler
type Connector struct {
	component.Base
	app pitaya.Pitaya
}

// ConnectorRemote 连接器Remote（供后端RPC调用）
type ConnectorRemote struct {
	component.Base
	app pitaya.Pitaya
}

// NewConnector 创建连接器
func NewConnector(app pitaya.Pitaya) *Connector {
	return &Connector{app: app}
}

// NewConnectorRemote 创建连接器Remote
func NewConnectorRemote(app pitaya.Pitaya) *ConnectorRemote {
	return &ConnectorRemote{app: app}
}

// LoginRequest 登录请求
type LoginRequest struct {
	PlayerName string `json:"playerName"`
}

// LoginResponse 登录响应
type LoginResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	UID     string `json:"uid"`
}

// Login 登录并绑定session
func (c *Connector) Login(ctx context.Context, req *LoginRequest) (*LoginResponse, error) {
	s := c.app.GetSessionFromCtx(ctx)

	// 如果已经绑定过UID，直接返回
	if s.UID() != "" {
		return &LoginResponse{
			Code:    0,
			Message: "已登录",
			UID:     s.UID(),
		}, nil
	}

	// 生成唯一UID
	uid := uuid.New().String()
	err := s.Bind(ctx, uid)
	if err != nil {
		log.Printf("[Connector] 绑定session失败: %v", err)
		return &LoginResponse{
			Code:    -1,
			Message: "登录失败: " + err.Error(),
		}, nil
	}

	// 存储玩家名称到session
	if req.PlayerName != "" {
		s.Set("playerName", req.PlayerName)
	}

	log.Printf("[Connector] 玩家登录成功: %s (name=%s)", uid, req.PlayerName)

	// 当session关闭时清理
	s.OnClose(func() {
		log.Printf("[Connector] 玩家断开连接: %s", uid)
	})

	return &LoginResponse{
		Code:    0,
		Message: "登录成功",
		UID:     uid,
	}, nil
}

// RegisterConnectorServices 注册连接器服务到Pitaya app
func RegisterConnectorServices(app pitaya.Pitaya) {
	connector := NewConnector(app)
	connectorRemote := NewConnectorRemote(app)

	app.Register(connector,
		component.WithName("connector"),
		component.WithNameFunc(strings.ToLower),
	)

	app.RegisterRemote(connectorRemote,
		component.WithName("connectorremote"),
		component.WithNameFunc(strings.ToLower),
	)
}
