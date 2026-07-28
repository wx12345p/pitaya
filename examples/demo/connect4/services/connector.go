package services

import (
	"context"
	"log"
	"strings"

	"connect4/protos"
	"connect4/store"

	"github.com/google/uuid"
	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/component"
	"github.com/topfreegames/pitaya/v2/session"
)

// Connector 前端连接器: 登录、会话绑定、断线通报
type Connector struct {
	component.Base
	app   pitaya.Pitaya
	store *store.RedisStore // 可为 nil(未启用 Redis)
}

// NewConnector 创建连接器
func NewConnector(app pitaya.Pitaya, st *store.RedisStore) *Connector {
	return &Connector{app: app, store: st}
}

// Login 登录并绑定会话。
// req.LastUid 非空且身份仍有效时复用同一 uid, 从而让断线重连能定位到原房间。
func (c *Connector) Login(ctx context.Context, req *protos.LoginRequest) (*protos.LoginResponse, error) {
	s := c.app.GetSessionFromCtx(ctx)
	if s.UID() != "" {
		name, _ := s.Get("playerName").(string)
		return &protos.LoginResponse{Code: protos.ResultCode_RESULT_OK, Msg: "已登录", Uid: s.UID(), Name: name}, nil
	}

	uid, name, resumed := "", req.PlayerName, false
	if req.LastUid != "" && c.store != nil {
		if oldName, ok, err := c.store.GetIdentity(ctx, req.LastUid); err != nil {
			log.Printf("[Connector] 读取身份失败 uid=%s: %v", req.LastUid, err)
		} else if ok {
			uid, resumed = req.LastUid, true
			if name == "" {
				name = oldName
			}
		}
	}
	if uid == "" {
		uid = uuid.New().String()
	}
	if name == "" {
		name = "玩家" + uid[:6]
	}

	if err := s.Bind(ctx, uid); err != nil {
		log.Printf("[Connector] 绑定会话失败: %v", err)
		return &protos.LoginResponse{Code: protos.ResultCode_RESULT_ERROR, Msg: "登录失败: " + err.Error()}, nil
	}
	_ = s.Set("playerName", name)
	if c.store != nil {
		if err := c.store.PutIdentity(ctx, uid, name); err != nil {
			log.Printf("[Connector] 写入身份失败 uid=%s: %v", uid, err)
		}
	}
	c.watchDisconnect(s, uid)

	log.Printf("[Connector] 登录: %s (uid=%s, 续接=%v)", name, uid, resumed)
	return &protos.LoginResponse{
		Code:    protos.ResultCode_RESULT_OK,
		Msg:     "登录成功",
		Uid:     uid,
		Name:    name,
		Resumed: resumed,
	}, nil
}

// watchDisconnect 会话关闭时通报 owner game 节点(开启掉线宽限 / 移除观战者)
func (c *Connector) watchDisconnect(s session.Session, uid string) {
	s.OnClose(func() {
		roomID, _ := s.Get("roomId").(string)
		owner, _ := s.Get("gameServer").(string)
		log.Printf("[Connector] 断开: uid=%s room=%s", uid, roomID)
		if roomID == "" || owner == "" {
			return
		}
		reply := &protos.PlayerConnResponse{}
		err := c.app.RPCTo(context.Background(), owner, "game.gameremote.playerconn", reply,
			&protos.PlayerConnRequest{RoomId: roomID, Uid: uid, Online: false})
		if err != nil {
			log.Printf("[Connector] 通报掉线失败 room=%s owner=%s: %v", roomID, owner, err)
		}
	})
}

// RegisterConnectorServices 注册前端服务
func RegisterConnectorServices(app pitaya.Pitaya, st *store.RedisStore) {
	app.Register(NewConnector(app, st),
		component.WithName("connector"),
		component.WithNameFunc(strings.ToLower),
	)
}
