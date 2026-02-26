package game

import (
	"sort"
)

// SimpleAI 简单规则型AI
type SimpleAI struct{}

// NewSimpleAI 创建简单AI
func NewSimpleAI() *SimpleAI {
	return &SimpleAI{}
}

// DecideBid AI叫分策略
// 根据手牌质量决定叫分:
// - 有火箭直接叫3
// - 2个以上炸弹叫3
// - 1个炸弹叫2
// - 有大小王之一 + 多张2叫1
// - 其他不叫
func (ai *SimpleAI) DecideBid(player *Player, maxBid int) int {
	cards := player.Cards
	rankCount := GetRankCount(cards)

	// 统计炸弹数量
	bombCount := 0
	for _, count := range rankCount {
		if count == 4 {
			bombCount++
		}
	}

	// 检查是否有大小王
	hasSmallJoker := rankCount[RankSmallJoker] > 0
	hasBigJoker := rankCount[RankBigJoker] > 0
	hasRocket := hasSmallJoker && hasBigJoker

	// 统计2的数量
	twoCount := rankCount[Rank2]

	// 统计A的数量
	aceCount := rankCount[RankA]

	// 计算叫分
	score := 0

	if hasRocket {
		score = 3
	} else if bombCount >= 2 {
		score = 3
	} else if bombCount == 1 && (hasSmallJoker || hasBigJoker) {
		score = 3
	} else if bombCount == 1 {
		score = 2
	} else if (hasSmallJoker || hasBigJoker) && twoCount >= 2 {
		score = 2
	} else if (hasSmallJoker || hasBigJoker) && twoCount >= 1 && aceCount >= 1 {
		score = 1
	} else if twoCount >= 2 && aceCount >= 2 {
		score = 1
	}

	// 叫分必须大于当前最高分
	if score <= maxBid {
		return 0
	}
	return score
}

// DecidePlay AI出牌策略
func (ai *SimpleAI) DecidePlay(player *Player, lastCards []Card, lastHandInfo HandInfo, isFirstPlay bool) []Card {
	if isFirstPlay {
		return ai.playFirst(player)
	}
	return ai.playFollow(player, lastCards, lastHandInfo)
}

// playFirst 主动出牌：从最小的牌开始出
func (ai *SimpleAI) playFirst(player *Player) []Card {
	cards := make([]Card, len(player.Cards))
	copy(cards, player.Cards)
	SortCards(cards)

	rankCount := GetRankCount(cards)

	// 优先出单牌（从小到大）
	// 先尝试出顺子
	if straight := findStraight(cards, rankCount); straight != nil {
		return straight
	}

	// 尝试出连对
	if straightPair := findStraightPair(cards, rankCount); straightPair != nil {
		return straightPair
	}

	// 出三带一
	for rank := Rank3; rank <= Rank2; rank++ {
		if rankCount[rank] == 3 {
			triplet := getCardsByRank(cards, rank, 3)
			// 找一张最小的单牌带出去
			kicker := findSmallestKicker(cards, rank, 1, rankCount)
			if kicker != nil {
				return append(triplet, kicker...)
			}
			return triplet
		}
	}

	// 出对子
	for rank := Rank3; rank <= Rank2; rank++ {
		if rankCount[rank] == 2 {
			return getCardsByRank(cards, rank, 2)
		}
	}

	// 出单牌（不出王和2）
	for rank := Rank3; rank < Rank2; rank++ {
		if rankCount[rank] == 1 {
			return getCardsByRank(cards, rank, 1)
		}
	}

	// 出任何单牌
	for rank := Rank3; rank <= RankBigJoker; rank++ {
		if rankCount[rank] >= 1 {
			return getCardsByRank(cards, rank, 1)
		}
	}

	// 不应该到这里
	return cards[:1]
}

// playFollow 跟牌：尝试找到能打过上家的最小牌
func (ai *SimpleAI) playFollow(player *Player, lastCards []Card, lastHandInfo HandInfo) []Card {
	cards := make([]Card, len(player.Cards))
	copy(cards, player.Cards)
	SortCards(cards)

	rankCount := GetRankCount(cards)

	switch lastHandInfo.Type {
	case HandTypeSingle:
		return ai.followSingle(cards, rankCount, lastHandInfo.MainRank)
	case HandTypePair:
		return ai.followPair(cards, rankCount, lastHandInfo.MainRank)
	case HandTypeTriplet:
		return ai.followTriplet(cards, rankCount, lastHandInfo.MainRank, 0)
	case HandTypeTripletSingle:
		return ai.followTripletSingle(cards, rankCount, lastHandInfo.MainRank)
	case HandTypeTripletPair:
		return ai.followTripletPair(cards, rankCount, lastHandInfo.MainRank)
	case HandTypeStraight:
		return ai.followStraight(cards, rankCount, lastHandInfo.MainRank, lastHandInfo.Length)
	case HandTypeStraightPair:
		return ai.followStraightPair(cards, rankCount, lastHandInfo.MainRank, lastHandInfo.Length)
	case HandTypeBomb:
		return ai.followBomb(cards, rankCount, lastHandInfo.MainRank)
	}

	// 对于其他复杂牌型，尝试用炸弹或火箭
	return ai.tryBombOrRocket(cards, rankCount)
}

// followSingle 跟单牌
func (ai *SimpleAI) followSingle(cards []Card, rankCount map[Rank]int, minRank Rank) []Card {
	// 找比 minRank 大的最小单牌（优先出单张的）
	for rank := minRank + 1; rank <= RankBigJoker; rank++ {
		if rankCount[rank] == 1 {
			return getCardsByRank(cards, rank, 1)
		}
	}
	// 从对子中拆
	for rank := minRank + 1; rank <= Rank2; rank++ {
		if rankCount[rank] == 2 {
			return getCardsByRank(cards, rank, 1)
		}
	}
	// 如果只剩少量牌，考虑用大牌
	if len(cards) <= 5 {
		for rank := minRank + 1; rank <= RankBigJoker; rank++ {
			if rankCount[rank] >= 1 {
				return getCardsByRank(cards, rank, 1)
			}
		}
	}
	// 不出
	return nil
}

// followPair 跟对子
func (ai *SimpleAI) followPair(cards []Card, rankCount map[Rank]int, minRank Rank) []Card {
	for rank := minRank + 1; rank <= Rank2; rank++ {
		if rankCount[rank] >= 2 {
			return getCardsByRank(cards, rank, 2)
		}
	}
	return ai.tryBombOrRocket(cards, rankCount)
}

// followTriplet 跟三条
func (ai *SimpleAI) followTriplet(cards []Card, rankCount map[Rank]int, minRank Rank, kickerCount int) []Card {
	for rank := minRank + 1; rank <= Rank2; rank++ {
		if rankCount[rank] >= 3 {
			result := getCardsByRank(cards, rank, 3)
			if kickerCount > 0 {
				kicker := findSmallestKicker(cards, rank, kickerCount, rankCount)
				if kicker != nil {
					return append(result, kicker...)
				}
			}
			return result
		}
	}
	return ai.tryBombOrRocket(cards, rankCount)
}

// followTripletSingle 跟三带一
func (ai *SimpleAI) followTripletSingle(cards []Card, rankCount map[Rank]int, minRank Rank) []Card {
	for rank := minRank + 1; rank <= Rank2; rank++ {
		if rankCount[rank] >= 3 {
			result := getCardsByRank(cards, rank, 3)
			kicker := findSmallestKicker(cards, rank, 1, rankCount)
			if kicker != nil {
				return append(result, kicker...)
			}
			// 如果没有单牌可带，只出三条
			return nil
		}
	}
	return ai.tryBombOrRocket(cards, rankCount)
}

// followTripletPair 跟三带对
func (ai *SimpleAI) followTripletPair(cards []Card, rankCount map[Rank]int, minRank Rank) []Card {
	for rank := minRank + 1; rank <= Rank2; rank++ {
		if rankCount[rank] >= 3 {
			// 找一对做翅膀
			for kr := Rank3; kr <= Rank2; kr++ {
				if kr != rank && rankCount[kr] >= 2 {
					result := getCardsByRank(cards, rank, 3)
					pair := getCardsByRank(cards, kr, 2)
					return append(result, pair...)
				}
			}
		}
	}
	return ai.tryBombOrRocket(cards, rankCount)
}

// followStraight 跟顺子
func (ai *SimpleAI) followStraight(cards []Card, rankCount map[Rank]int, minRank Rank, length int) []Card {
	// 从 minRank+1 开始找连续的单牌
	for start := minRank + 1; int(start)+length-1 < int(Rank2); start++ {
		found := true
		for i := 0; i < length; i++ {
			r := Rank(int(start) + i)
			if rankCount[r] < 1 {
				found = false
				break
			}
		}
		if found {
			var result []Card
			for i := 0; i < length; i++ {
				r := Rank(int(start) + i)
				result = append(result, getCardsByRank(cards, r, 1)...)
			}
			return result
		}
	}
	return ai.tryBombOrRocket(cards, rankCount)
}

// followStraightPair 跟连对
func (ai *SimpleAI) followStraightPair(cards []Card, rankCount map[Rank]int, minRank Rank, length int) []Card {
	for start := minRank + 1; int(start)+length-1 < int(Rank2); start++ {
		found := true
		for i := 0; i < length; i++ {
			r := Rank(int(start) + i)
			if rankCount[r] < 2 {
				found = false
				break
			}
		}
		if found {
			var result []Card
			for i := 0; i < length; i++ {
				r := Rank(int(start) + i)
				result = append(result, getCardsByRank(cards, r, 2)...)
			}
			return result
		}
	}
	return ai.tryBombOrRocket(cards, rankCount)
}

// followBomb 跟炸弹
func (ai *SimpleAI) followBomb(cards []Card, rankCount map[Rank]int, minRank Rank) []Card {
	// 找更大的炸弹
	for rank := minRank + 1; rank <= Rank2; rank++ {
		if rankCount[rank] == 4 {
			return getCardsByRank(cards, rank, 4)
		}
	}
	// 火箭
	if rankCount[RankSmallJoker] > 0 && rankCount[RankBigJoker] > 0 {
		sj := getCardsByRank(cards, RankSmallJoker, 1)
		bj := getCardsByRank(cards, RankBigJoker, 1)
		return append(sj, bj...)
	}
	return nil
}

// tryBombOrRocket 尝试出炸弹或火箭
func (ai *SimpleAI) tryBombOrRocket(cards []Card, rankCount map[Rank]int) []Card {
	// 火箭
	if rankCount[RankSmallJoker] > 0 && rankCount[RankBigJoker] > 0 {
		sj := getCardsByRank(cards, RankSmallJoker, 1)
		bj := getCardsByRank(cards, RankBigJoker, 1)
		return append(sj, bj...)
	}
	// 炸弹（只在手牌较少时使用）
	if len(cards) <= 8 {
		for rank := Rank3; rank <= Rank2; rank++ {
			if rankCount[rank] == 4 {
				return getCardsByRank(cards, rank, 4)
			}
		}
	}
	return nil
}

// === 辅助函数 ===

// getCardsByRank 获取指定点数的前 count 张牌
func getCardsByRank(cards []Card, rank Rank, count int) []Card {
	var result []Card
	for _, c := range cards {
		if c.Rank == rank {
			result = append(result, c)
			if len(result) >= count {
				break
			}
		}
	}
	return result
}

// findSmallestKicker 找最小的踢牌（不包含指定 rank 的牌）
func findSmallestKicker(cards []Card, excludeRank Rank, count int, rankCount map[Rank]int) []Card {
	// 收集可用的 ranks 并排序
	var availableRanks []Rank
	for rank := range rankCount {
		if rank != excludeRank {
			availableRanks = append(availableRanks, rank)
		}
	}
	sort.Slice(availableRanks, func(i, j int) bool {
		return availableRanks[i] < availableRanks[j]
	})

	var result []Card
	for _, rank := range availableRanks {
		need := count - len(result)
		if need <= 0 {
			break
		}
		available := getCardsByRank(cards, rank, need)
		result = append(result, available...)
	}

	if len(result) >= count {
		return result[:count]
	}
	return nil
}

// findStraight 查找一个顺子（至少5张连续牌）
func findStraight(cards []Card, rankCount map[Rank]int) []Card {
	// 从最小的rank开始找
	for start := Rank3; start <= RankA-4; start++ {
		length := 0
		for r := start; r < Rank2; r++ {
			if rankCount[r] >= 1 {
				length++
			} else {
				break
			}
		}
		if length >= 5 {
			var result []Card
			for i := 0; i < length; i++ {
				r := Rank(int(start) + i)
				result = append(result, getCardsByRank(cards, r, 1)...)
			}
			return result
		}
	}
	return nil
}

// findStraightPair 查找连对
func findStraightPair(cards []Card, rankCount map[Rank]int) []Card {
	for start := Rank3; start <= RankA-2; start++ {
		length := 0
		for r := start; r < Rank2; r++ {
			if rankCount[r] >= 2 {
				length++
			} else {
				break
			}
		}
		if length >= 3 {
			var result []Card
			for i := 0; i < length; i++ {
				r := Rank(int(start) + i)
				result = append(result, getCardsByRank(cards, r, 2)...)
			}
			return result
		}
	}
	return nil
}
