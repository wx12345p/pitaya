package game

import (
	"errors"
	"sync"
)

var errBadTurn = errors.New("不是你的操作回合或状态不允许")

// Member 建房成员(由 lobby 匹配后传入)
type Member struct {
	UID  string
	Name string
	Seat int
}

// RoomManager 房间管理器(单节点内存, 仅持有本节点 owner 的房间)
type RoomManager struct {
	mu    sync.RWMutex
	rooms map[string]*Room
}

// NewRoomManager 创建房间管理器
func NewRoomManager() *RoomManager {
	return &RoomManager{rooms: make(map[string]*Room)}
}

// GetRoom 获取房间
func (rm *RoomManager) GetRoom(id string) *Room {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.rooms[id]
}

// FindRoomByUID 根据玩家UID查找房间(本节点范围内)
func (rm *RoomManager) FindRoomByUID(uid string) *Room {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	for _, room := range rm.rooms {
		if room.GetSeatByUID(uid) >= 0 {
			return room
		}
	}
	return nil
}

// CreateRoom 按指定 roomID 与成员建房(幂等: 已存在则直接返回)
func (rm *RoomManager) CreateRoom(roomID string, members []Member) *Room {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if r, ok := rm.rooms[roomID]; ok {
		return r
	}
	r := NewRoomWithID(roomID, members)
	rm.rooms[roomID] = r
	return r
}

// RestoreRoom 由快照重建房间并登记(幂等: 已存在则直接返回)
func (rm *RoomManager) RestoreRoom(snap Snapshot) *Room {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if r, ok := rm.rooms[snap.RoomID]; ok {
		return r
	}
	r := roomFromSnapshot(snap)
	rm.rooms[snap.RoomID] = r
	return r
}

// RemoveRoom 删除房间
func (rm *RoomManager) RemoveRoom(id string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	delete(rm.rooms, id)
}

// Rooms 返回本节点当前所有房间(用于优雅关机快照)
func (rm *RoomManager) Rooms() []*Room {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	res := make([]*Room, 0, len(rm.rooms))
	for _, r := range rm.rooms {
		res = append(res, r)
	}
	return res
}
