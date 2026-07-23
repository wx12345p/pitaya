package services

import (
	"context"
	"log"
	"strings"

	"richman/protos"

	"github.com/google/uuid"
	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/component"
)

// Connector 前端连接器(处理登录/会话绑定)
type Connector struct {
	component.Base
	app pitaya.Pitaya
}

// ConnectorRemote 连接器Remote(占位, 供后端RPC扩展)
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

// Login 登录并绑定会话
func (c *Connector) Login(ctx context.Context, req *protos.LoginRequest) (*protos.LoginResponse, error) {
	s := c.app.GetSessionFromCtx(ctx)

	if s.UID() != "" {
		return &protos.LoginResponse{Code: protos.ResultCode_RESULT_OK, Msg: "已登录", Uid: s.UID()}, nil
	}

	uid := uuid.New().String()
	if err := s.Bind(ctx, uid); err != nil {
		log.Printf("[Connector] 绑定会话失败: %v", err)
		return &protos.LoginResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "登录失败: " + err.Error()}, nil
	}
	if req.PlayerName != "" {
		s.Set("playerName", req.PlayerName)
	}

	log.Printf("[Connector] 玩家登录: %s (name=%s)", uid, req.PlayerName)
	s.OnClose(func() {
		log.Printf("[Connector] 玩家断开: %s", uid)
	})

	return &protos.LoginResponse{Code: protos.ResultCode_RESULT_OK, Msg: "登录成功", Uid: uid}, nil
}

// RegisterConnectorServices 注册前端服务
func RegisterConnectorServices(app pitaya.Pitaya) {
	app.Register(NewConnector(app),
		component.WithName("connector"),
		component.WithNameFunc(strings.ToLower),
	)
	app.RegisterRemote(NewConnectorRemote(app),
		component.WithName("connectorremote"),
		component.WithNameFunc(strings.ToLower),
	)
}
