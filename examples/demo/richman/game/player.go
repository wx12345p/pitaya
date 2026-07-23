package game

// Player 玩家运行时状态
type Player struct {
	UID      string
	Name     string
	Seat     int
	Money    int64
	Pos      int
	Bankrupt bool
	IsAI     bool
}
