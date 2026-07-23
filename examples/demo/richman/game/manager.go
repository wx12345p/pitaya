package game

import (
	"errors"
	"sync"
)

var errBadTurn = errors.New("不是你的操作回合或状态不允许")

// RoomManager 房间管理器(内存)
type RoomManager struct {
	mu       sync.RWMutex
	rooms    map[string]*Room
	waitRoom *Room // 当前等待匹配的房间
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

// FindRoomByUID 根据玩家UID查找房间
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

// JoinOrCreate 加入或创建房间, 返回房间/座位/是否满员
func (rm *RoomManager) JoinOrCreate(uid, name string) (*Room, int, bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// 已在房间则直接返回
	for _, room := range rm.rooms {
		if seat := room.GetSeatByUID(uid); seat >= 0 {
			return room, seat, room.PlayerCount() >= MaxPlayers
		}
	}

	// 加入等待房间
	if rm.waitRoom != nil && rm.waitRoom.State == StateWaiting {
		room := rm.waitRoom
		seat := room.addPlayer(uid, name)
		if seat >= 0 {
			full := room.PlayerCount() >= MaxPlayers
			if full {
				rm.waitRoom = nil
			}
			return room, seat, full
		}
	}

	// 创建新房间
	room := NewRoom()
	seat := room.addPlayer(uid, name)
	rm.rooms[room.ID] = room
	rm.waitRoom = room
	return room, seat, room.PlayerCount() >= MaxPlayers
}

// RemoveRoom 删除房间
func (rm *RoomManager) RemoveRoom(id string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.waitRoom != nil && rm.waitRoom.ID == id {
		rm.waitRoom = nil
	}
	delete(rm.rooms, id)
}

// addPlayer 在空座位放入玩家, 返回座位号(-1=无空位)
func (r *Room) addPlayer(uid, name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, p := range r.Players {
		if p == nil {
			r.Players[i] = &Player{UID: uid, Name: name, Seat: i, Money: InitMoney}
			return i
		}
	}
	return -1
}
