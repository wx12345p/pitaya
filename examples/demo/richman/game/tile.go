package game

// TileType 地块类型
type TileType int32

const (
	TileStart    TileType = 0 // 起点(经过/停留发工资)
	TileProperty TileType = 1 // 可购买地产
	TileChance   TileType = 2 // 机会
	TileFate     TileType = 3 // 命运
	TileTax      TileType = 4 // 税收
	TileStop     TileType = 5 // 普通停留(占位)
)

// 地产过路费按等级的倍数系数: 1级x1, 2级x2, 3级x4
var tollMultiplier = map[int]int64{1: 1, 2: 2, 3: 4}

// MaxLevel 地产最高等级
const MaxLevel = 3

// Tile 地块(含运行时归属状态)
type Tile struct {
	Index    int
	Type     TileType
	Name     string
	Price    int64 // 地产基础价
	BaseToll int64 // 地产基础过路费
	Tax      int64 // 税收金额
	Salary   int64 // 起点工资

	OwnerUID string // 归属玩家UID(空=无主)
	Level    int    // 地产等级(0=空地,1..3)
}

// UpgradeCost 升级到下一级的费用(基础价的一半)
func (t *Tile) UpgradeCost() int64 {
	return t.Price / 2
}

// Toll 当前应收过路费
func (t *Tile) Toll() int64 {
	m, ok := tollMultiplier[t.Level]
	if !ok {
		return 0
	}
	return t.BaseToll * m
}

// Value 地产估值(用于结算/清算): 基础价 + 已投入升级费
func (t *Tile) Value() int64 {
	if t.OwnerUID == "" || t.Level < 1 {
		return 0
	}
	// level1 => 仅基础价; 每多一级加一次升级费
	return t.Price + int64(t.Level-1)*t.UpgradeCost()
}

// CanUpgrade 是否还能升级
func (t *Tile) CanUpgrade() bool {
	return t.Type == TileProperty && t.OwnerUID != "" && t.Level < MaxLevel
}

// Reset 清空归属(玩家破产时收归无主)
func (t *Tile) Reset() {
	t.OwnerUID = ""
	t.Level = 0
}
