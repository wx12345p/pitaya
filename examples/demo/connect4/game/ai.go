package game

import (
	"math"
	"math/rand"
)

// AI 搜索深度: 正常出手用 AIDepth, 超时代打求快用 AIFastDepth
const (
	AIDepth     = 6
	AIFastDepth = 2
)

// winScore 必胜局面的基准分, 远大于任何启发式评分
const winScore = 1 << 20

// searchOrder 列的搜索顺序: 中心优先, 让 alpha-beta 更早剪枝
var searchOrder = [Cols]int{3, 2, 4, 1, 5, 0, 6}

// windowScore 一个 4 连窗口中自己占 n 子(且无对手子)时的价值
var windowScore = [WinLen + 1]int{0, 1, 12, 60, winScore}

// BestMove 为 me 选择最佳落子列; 棋盘已满返回 -1。
// 实现为 negamax + alpha-beta 剪枝, 终局(连四/满盘)直接返回终局分,
// 达到深度上限时用窗口启发式评分。同分列之间随机取一个, 避免每局棋路完全相同。
func BestMove(b *Board, me Piece, depth int) int {
	work := b.Clone()
	best := -1
	bestScore := math.MinInt32
	ties := 0
	alpha := math.MinInt32
	beta := math.MaxInt32

	for _, col := range searchOrder {
		if work.ColFull(col) {
			continue
		}
		row, _ := work.Drop(col, me)
		var sc int
		switch {
		case work.WinningLine(col, row) != nil:
			sc = winScore + depth
		case work.Full():
			sc = 0
		default:
			sc = -search(work, depth-1, -beta, -alpha, Opponent(me))
		}
		work.Undo(col)

		switch {
		case sc > bestScore:
			bestScore, best, ties = sc, col, 1
		case sc == bestScore:
			ties++
			if rand.Intn(ties) == 0 { // 蓄水池抽样: 同分列等概率选取
				best = col
			}
		}
		if bestScore > alpha {
			alpha = bestScore
		}
	}
	return best
}

// search 返回当前轮到 cur 行动时该局面对 cur 的分值
func search(b *Board, depth int, alpha, beta int, cur Piece) int {
	if b.Full() {
		return 0
	}
	if depth <= 0 {
		return evaluate(b, cur)
	}
	best := math.MinInt32
	for _, col := range searchOrder {
		if b.ColFull(col) {
			continue
		}
		row, _ := b.Drop(col, cur)
		var sc int
		switch {
		case b.WinningLine(col, row) != nil:
			sc = winScore + depth // 同为必胜时, 剩余深度越大表示越快取胜
		case b.Full():
			sc = 0
		default:
			sc = -search(b, depth-1, -beta, -alpha, Opponent(cur))
		}
		b.Undo(col)

		if sc > best {
			best = sc
		}
		if best > alpha {
			alpha = best
		}
		if alpha >= beta {
			break // beta 剪枝
		}
	}
	return best
}

// evaluate 静态评估: 双方所有"未被对手封死的 4 连窗口"价值之差
func evaluate(b *Board, cur Piece) int {
	return scoreFor(b, cur) - scoreFor(b, Opponent(cur))
}

func scoreFor(b *Board, p Piece) int {
	opp := Opponent(p)
	total := 0
	for _, d := range winDirs {
		for col := 0; col < Cols; col++ {
			for row := 0; row < Rows; row++ {
				endC, endR := col+d[0]*(WinLen-1), row+d[1]*(WinLen-1)
				if endC < 0 || endC >= Cols || endR < 0 || endR >= Rows {
					continue
				}
				mine, blocked := 0, false
				for i := 0; i < WinLen; i++ {
					switch b.At(col+d[0]*i, row+d[1]*i) {
					case p:
						mine++
					case opp:
						blocked = true
					}
				}
				if blocked || mine == 0 {
					continue
				}
				total += windowScore[mine]
			}
		}
	}
	// 中心列的子参与更多连线, 额外加权
	for row := 0; row < Rows; row++ {
		if b.At(Cols/2, row) == p {
			total += 3
		}
	}
	return total
}
