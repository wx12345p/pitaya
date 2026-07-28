package game

import "time"

// 房间快照(纯 Go 结构, 与 protobuf 解耦; 由 store 层负责与 proto 互转)。
// 四子棋整局状态很小(42 字节棋盘 + 少量元信息), 因此每步落子后都写一次,
// 宕机最多丢失"最后一步之后的操作"。

// PlayerState 玩家状态快照
type PlayerState struct {
	UID          string
	Name         string
	Seat         int
	IsAI         bool
	TimeoutCount int
	Online       bool
}

// Snapshot 房间状态快照
type Snapshot struct {
	RoomID      string
	State       RoomState
	Cells       []byte
	CurrentSeat int
	MoveNo      int
	FirstSeat   int
	Players     []PlayerState
	WinnerUID   string
	WinnerSeat  int
	Reason      EndReason
	StartedAt   int64
	Moves       []int
}

// ToSnapshot 生成当前房间快照(线程安全)
func (r *Room) ToSnapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := Snapshot{
		RoomID:      r.ID,
		State:       r.state,
		Cells:       r.board.Encode(),
		CurrentSeat: r.currentSeat,
		MoveNo:      r.board.MoveCount(),
		FirstSeat:   r.FirstSeat,
		WinnerUID:   r.winnerUID,
		WinnerSeat:  r.winnerSeat,
		Reason:      r.reason,
		StartedAt:   r.StartedAt,
		Moves:       append([]int(nil), r.moves...),
	}
	for _, p := range r.players {
		if p == nil {
			continue
		}
		snap.Players = append(snap.Players, PlayerState{
			UID: p.UID, Name: p.Name, Seat: p.Seat,
			IsAI: p.IsAI, TimeoutCount: p.TimeoutCount, Online: p.Online,
		})
	}
	return snap
}

// RoomFromSnapshot 由快照重建房间(尚未开启回合, 需调用 Resume 续跑)
func RoomFromSnapshot(snap Snapshot) *Room {
	r := &Room{
		ID:          snap.RoomID,
		FirstSeat:   snap.FirstSeat,
		StartedAt:   snap.StartedAt,
		state:       snap.State,
		board:       DecodeBoard(snap.Cells),
		currentSeat: snap.CurrentSeat,
		moves:       append([]int(nil), snap.Moves...),
		spectators:  make(map[string]bool),
		winnerUID:   snap.WinnerUID,
		winnerSeat:  snap.WinnerSeat,
		reason:      snap.Reason,
	}
	if r.StartedAt == 0 {
		r.StartedAt = time.Now().Unix()
	}
	for _, ps := range snap.Players {
		if ps.Seat < 0 || ps.Seat >= MaxPlayers {
			continue
		}
		r.players[ps.Seat] = &Player{
			UID: ps.UID, Name: ps.Name, Seat: ps.Seat,
			IsAI: ps.IsAI, TimeoutCount: ps.TimeoutCount, Online: ps.Online,
		}
	}
	return r
}
