package game

// 地图与经济参数
const (
	BoardSize   = 20    // 环形地图格子数
	StartSalary = 2000  // 经过/停留起点工资
	InitMoney   = 10000 // 初始现金
	MaxRounds   = 50    // 回合上限
	MaxPlayers  = 4     // 房间人数
)

// NewBoard 创建固定 20 格环形地图
// 索引 0 为起点; 索引 10 为普通停留格(占位, 不发工资)
func NewBoard() []*Tile {
	return []*Tile{
		{Index: 0, Type: TileStart, Name: "起点", Salary: StartSalary},
		{Index: 1, Type: TileProperty, Name: "城中村", Price: 1000, BaseToll: 100},
		{Index: 2, Type: TileChance, Name: "机会"},
		{Index: 3, Type: TileProperty, Name: "老街", Price: 1200, BaseToll: 120},
		{Index: 4, Type: TileProperty, Name: "市场", Price: 1400, BaseToll: 140},
		{Index: 5, Type: TileTax, Name: "税务局", Tax: 500},
		{Index: 6, Type: TileProperty, Name: "车站", Price: 1600, BaseToll: 160},
		{Index: 7, Type: TileFate, Name: "命运"},
		{Index: 8, Type: TileProperty, Name: "商业街", Price: 1800, BaseToll: 180},
		{Index: 9, Type: TileProperty, Name: "公园", Price: 2000, BaseToll: 200},
		{Index: 10, Type: TileStop, Name: "中转站"},
		{Index: 11, Type: TileProperty, Name: "学区房", Price: 2200, BaseToll: 220},
		{Index: 12, Type: TileChance, Name: "机会"},
		{Index: 13, Type: TileProperty, Name: "写字楼", Price: 2400, BaseToll: 240},
		{Index: 14, Type: TileProperty, Name: "酒店", Price: 2600, BaseToll: 260},
		{Index: 15, Type: TileTax, Name: "税务局", Tax: 800},
		{Index: 16, Type: TileProperty, Name: "商场", Price: 2800, BaseToll: 280},
		{Index: 17, Type: TileFate, Name: "命运"},
		{Index: 18, Type: TileProperty, Name: "CBD", Price: 3000, BaseToll: 300},
		{Index: 19, Type: TileProperty, Name: "地标大厦", Price: 3500, BaseToll: 350},
	}
}
