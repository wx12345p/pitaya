package game

import "sync"

// RoomManager 本节点房间表(仅持有本节点作为 owner 的房间)
type RoomManager struct {
	mu    sync.RWMutex
	rooms map[string]*Room
}

// NewRoomManager 创建房间管理器
func NewRoomManager() *RoomManager {
	return &RoomManager{rooms: make(map[string]*Room)}
}

// GetRoom 按 roomID 获取房间, 不存在返回 nil
func (rm *RoomManager) GetRoom(id string) *Room {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.rooms[id]
}

// CreateRoom 建房(幂等: 已存在则返回已有房间与 false)
func (rm *RoomManager) CreateRoom(id string, members []Member, withAI bool, firstSeat int) (*Room, bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if r, ok := rm.rooms[id]; ok {
		return r, false
	}
	r := NewRoom(id, members, withAI, firstSeat)
	rm.rooms[id] = r
	return r, true
}

// RestoreRoom 由快照重建房间并登记(幂等: 已存在则返回已有房间与 false)
func (rm *RoomManager) RestoreRoom(snap Snapshot) (*Room, bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if r, ok := rm.rooms[snap.RoomID]; ok {
		return r, false
	}
	r := RoomFromSnapshot(snap)
	rm.rooms[snap.RoomID] = r
	return r, true
}

// RemoveRoom 移除房间并停止其定时器
func (rm *RoomManager) RemoveRoom(id string) {
	rm.mu.Lock()
	r := rm.rooms[id]
	delete(rm.rooms, id)
	rm.mu.Unlock()
	if r != nil {
		r.Stop()
	}
}

// Rooms 返回本节点当前所有房间(优雅关机 flush 快照用)
func (rm *RoomManager) Rooms() []*Room {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	res := make([]*Room, 0, len(rm.rooms))
	for _, r := range rm.rooms {
		res = append(res, r)
	}
	return res
}

// FindRoomByUID 在本节点范围内按玩家 uid 查找房间
func (rm *RoomManager) FindRoomByUID(uid string) *Room {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	for _, r := range rm.rooms {
		if r.SeatOf(uid) >= 0 {
			return r
		}
	}
	return nil
}
