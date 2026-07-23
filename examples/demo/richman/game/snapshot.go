package game

// 房间快照(纯 Go 结构, 与 protobuf 解耦; 由 store 层做与 proto 的转换)。
// 仅在回合边界(phaseIdle, 一个回合开始时)拍摄, 故无需持久化 phase/pendingTile:
// 恢复时按 CurrentSeat 重新开启该回合即可(RPO = 一个回合)。

// PlayerState 玩家状态快照
type PlayerState struct {
	UID      string
	Name     string
	Seat     int
	Money    int64
	Pos      int
	Bankrupt bool
	IsAI     bool
}

// TileState 地块动态状态快照(仅归属/等级)
type TileState struct {
	Index    int
	OwnerUID string
	Level    int
}

// Snapshot 房间状态快照
type Snapshot struct {
	RoomID      string
	State       int
	Round       int
	TurnsTaken  int
	CurrentSeat int
	Players     []PlayerState
	Tiles       []TileState
}

// ToSnapshot 生成当前房间的快照(线程安全)
func (r *Room) ToSnapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := Snapshot{
		RoomID:      r.ID,
		State:       int(r.State),
		Round:       r.Round,
		TurnsTaken:  r.turnsTaken,
		CurrentSeat: r.CurrentSeat,
	}
	for _, p := range r.Players {
		if p == nil {
			continue
		}
		snap.Players = append(snap.Players, PlayerState{
			UID: p.UID, Name: p.Name, Seat: p.Seat, Money: p.Money,
			Pos: p.Pos, Bankrupt: p.Bankrupt, IsAI: p.IsAI,
		})
	}
	for _, t := range r.Board {
		if t.OwnerUID != "" {
			snap.Tiles = append(snap.Tiles, TileState{Index: t.Index, OwnerUID: t.OwnerUID, Level: t.Level})
		}
	}
	return snap
}

// roomFromSnapshot 由快照重建房间(未开启回合)
func roomFromSnapshot(snap Snapshot) *Room {
	r := &Room{
		ID:          snap.RoomID,
		State:       RoomState(snap.State),
		Board:       NewBoard(),
		Players:     make([]*Player, MaxPlayers),
		CurrentSeat: snap.CurrentSeat,
		Round:       snap.Round,
		turnsTaken:  snap.TurnsTaken,
		phase:       phaseIdle,
	}
	for _, ps := range snap.Players {
		if ps.Seat < 0 || ps.Seat >= MaxPlayers {
			continue
		}
		r.Players[ps.Seat] = &Player{
			UID: ps.UID, Name: ps.Name, Seat: ps.Seat, Money: ps.Money,
			Pos: ps.Pos, Bankrupt: ps.Bankrupt, IsAI: ps.IsAI,
		}
	}
	byIndex := make(map[int]*Tile, len(r.Board))
	for _, t := range r.Board {
		byIndex[t.Index] = t
	}
	for _, ts := range snap.Tiles {
		if t, ok := byIndex[ts.Index]; ok {
			t.OwnerUID = ts.OwnerUID
			t.Level = ts.Level
		}
	}
	return r
}

// Resume 恢复后重新开启当前座位的回合(若仍在对局中)
func (r *Room) Resume() {
	r.mu.Lock()
	playing := r.State == StatePlaying
	r.mu.Unlock()
	if playing {
		r.beginTurn()
	}
}
