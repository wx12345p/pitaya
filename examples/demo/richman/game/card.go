package game

import "math/rand"

// CardEffect 机会/命运卡效果
type CardEffect struct {
	Desc       string // 描述
	MoneyDelta int64  // 直接增减抽卡者现金(正=获得,负=支出)
	MoveTo     int    // 传送到指定格子, -1=不移动
	AllPay     int64  // 其余每位在局玩家向抽卡者支付的金额(分红), 0=无
}

// chanceCards 机会卡池
var chanceCards = []CardEffect{
	{Desc: "幸运奖金, 获得 1000", MoneyDelta: 1000, MoveTo: -1},
	{Desc: "违章罚款, 支出 800", MoneyDelta: -800, MoveTo: -1},
	{Desc: "时来运转, 传送回起点并领取工资", MoneyDelta: 0, MoveTo: 0},
}

// fateCards 命运卡池
var fateCards = []CardEffect{
	{Desc: "天降横财, 获得 1500", MoneyDelta: 1500, MoveTo: -1},
	{Desc: "医疗支出, 支出 1000", MoneyDelta: -1000, MoveTo: -1},
	{Desc: "全体分红, 其余玩家各支付你 200", MoveTo: -1, AllPay: 200},
}

// DrawChance 抽一张机会卡
func DrawChance() CardEffect {
	return chanceCards[rand.Intn(len(chanceCards))]
}

// DrawFate 抽一张命运卡
func DrawFate() CardEffect {
	return fateCards[rand.Intn(len(fateCards))]
}
