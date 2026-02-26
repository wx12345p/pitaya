package game

import "sort"

// HandType 牌型枚举
type HandType int

const (
	HandTypeNone            HandType = iota // 不合法
	HandTypeSingle                          // 单牌
	HandTypePair                            // 对子
	HandTypeTriplet                         // 三条
	HandTypeTripletSingle                   // 三带一
	HandTypeTripletPair                     // 三带二
	HandTypeStraight                        // 顺子 (>=5 连续单牌)
	HandTypeStraightPair                    // 连对 (>=3 连续对子)
	HandTypePlane                           // 飞机不带 (>=2 连续三条)
	HandTypePlaneWithSingle                 // 飞机带单
	HandTypePlaneWithPair                   // 飞机带对
	HandTypeFourWithTwo                     // 四带二（单牌）
	HandTypeFourWithPairs                   // 四带二（对子）
	HandTypeBomb                            // 炸弹 (4张同点)
	HandTypeRocket                          // 火箭 (双王)
)

var handTypeNames = map[HandType]string{
	HandTypeNone:            "不合法",
	HandTypeSingle:          "单牌",
	HandTypePair:            "对子",
	HandTypeTriplet:         "三条",
	HandTypeTripletSingle:   "三带一",
	HandTypeTripletPair:     "三带二",
	HandTypeStraight:        "顺子",
	HandTypeStraightPair:    "连对",
	HandTypePlane:           "飞机",
	HandTypePlaneWithSingle: "飞机带单",
	HandTypePlaneWithPair:   "飞机带对",
	HandTypeFourWithTwo:     "四带二",
	HandTypeFourWithPairs:   "四带二对",
	HandTypeBomb:            "炸弹",
	HandTypeRocket:          "火箭",
}

func (h HandType) String() string {
	if name, ok := handTypeNames[h]; ok {
		return name
	}
	return "未知"
}

// HandInfo 牌型分析结果
type HandInfo struct {
	Type     HandType // 牌型
	MainRank Rank     // 主牌点数（用于比较大小）
	Length   int      // 顺子/连对/飞机的长度
}

// DetectHandType 识别一组牌的牌型
func DetectHandType(cards []Card) HandInfo {
	n := len(cards)
	if n == 0 {
		return HandInfo{Type: HandTypeNone}
	}

	sorted := make([]Card, n)
	copy(sorted, cards)
	SortCards(sorted)

	rankCount := GetRankCount(sorted)

	// 按出现次数分组
	var singles, pairs, triplets, quads []Rank
	for rank, count := range rankCount {
		switch count {
		case 1:
			singles = append(singles, rank)
		case 2:
			pairs = append(pairs, rank)
		case 3:
			triplets = append(triplets, rank)
		case 4:
			quads = append(quads, rank)
		}
	}
	sortRanks(singles)
	sortRanks(pairs)
	sortRanks(triplets)
	sortRanks(quads)

	// 火箭：小王+大王
	if n == 2 && len(singles) == 2 {
		if containsRank(singles, RankSmallJoker) && containsRank(singles, RankBigJoker) {
			return HandInfo{Type: HandTypeRocket, MainRank: RankBigJoker}
		}
	}

	// 单牌
	if n == 1 {
		return HandInfo{Type: HandTypeSingle, MainRank: sorted[0].Rank}
	}

	// 对子
	if n == 2 && len(pairs) == 1 {
		return HandInfo{Type: HandTypePair, MainRank: pairs[0]}
	}

	// 三条
	if n == 3 && len(triplets) == 1 {
		return HandInfo{Type: HandTypeTriplet, MainRank: triplets[0]}
	}

	// 炸弹
	if n == 4 && len(quads) == 1 {
		return HandInfo{Type: HandTypeBomb, MainRank: quads[0]}
	}

	// 三带一
	if n == 4 && len(triplets) == 1 && len(singles) == 1 {
		return HandInfo{Type: HandTypeTripletSingle, MainRank: triplets[0]}
	}

	// 三带二
	if n == 5 && len(triplets) == 1 && len(pairs) == 1 {
		return HandInfo{Type: HandTypeTripletPair, MainRank: triplets[0]}
	}

	// 顺子：>=5 连续单牌，不含2和王
	if n >= 5 && len(singles) == n && isConsecutive(singles) && allBelow2(singles) {
		return HandInfo{Type: HandTypeStraight, MainRank: singles[0], Length: n}
	}

	// 连对：>=3 连续对子，不含2和王
	if n >= 6 && n%2 == 0 && len(pairs) == n/2 && isConsecutive(pairs) && allBelow2(pairs) {
		return HandInfo{Type: HandTypeStraightPair, MainRank: pairs[0], Length: len(pairs)}
	}

	// 飞机（不带）：>=2 连续三条，不含2和王
	if n >= 6 && n%3 == 0 && len(triplets) == n/3 && isConsecutive(triplets) && allBelow2(triplets) {
		return HandInfo{Type: HandTypePlane, MainRank: triplets[0], Length: len(triplets)}
	}

	// 飞机带单：连续三条 + 等量单牌
	if len(triplets) >= 2 && isConsecutive(triplets) && allBelow2(triplets) {
		planeLen := len(triplets)
		kickers := n - planeLen*3
		if kickers == planeLen {
			return HandInfo{Type: HandTypePlaneWithSingle, MainRank: triplets[0], Length: planeLen}
		}
	}

	// 飞机带对：连续三条 + 等量对子
	if len(triplets) >= 2 && isConsecutive(triplets) && allBelow2(triplets) {
		planeLen := len(triplets)
		kickers := n - planeLen*3
		if kickers == planeLen*2 && len(pairs) >= planeLen {
			return HandInfo{Type: HandTypePlaneWithPair, MainRank: triplets[0], Length: planeLen}
		}
	}

	// 四带二（单牌）
	if n == 6 && len(quads) == 1 && len(singles) == 2 {
		return HandInfo{Type: HandTypeFourWithTwo, MainRank: quads[0]}
	}

	// 四带二（对子）
	if n == 8 && len(quads) == 1 && len(pairs) == 2 {
		return HandInfo{Type: HandTypeFourWithPairs, MainRank: quads[0]}
	}

	return HandInfo{Type: HandTypeNone}
}

// CanBeat 判断 current 是否能打过 previous
// previous 为 nil 表示主动出牌（第一手或前两家都不出）
func CanBeat(current, previous HandInfo) bool {
	// 不合法的牌型不能出
	if current.Type == HandTypeNone {
		return false
	}

	// 没有上家牌，只要合法就行
	if previous.Type == HandTypeNone {
		return true
	}

	// 火箭最大，可以打任何牌
	if current.Type == HandTypeRocket {
		return true
	}

	// 炸弹可以打非火箭非炸弹的任何牌
	if current.Type == HandTypeBomb && previous.Type != HandTypeBomb && previous.Type != HandTypeRocket {
		return true
	}

	// 同类型比较
	if current.Type == previous.Type {
		// 顺子/连对/飞机需要长度相同
		switch current.Type {
		case HandTypeStraight, HandTypeStraightPair, HandTypePlane, HandTypePlaneWithSingle, HandTypePlaneWithPair:
			if current.Length != previous.Length {
				return false
			}
		}
		return current.MainRank > previous.MainRank
	}

	return false
}

// === 辅助函数 ===

func sortRanks(ranks []Rank) {
	sort.Slice(ranks, func(i, j int) bool {
		return ranks[i] < ranks[j]
	})
}

func containsRank(ranks []Rank, r Rank) bool {
	for _, rank := range ranks {
		if rank == r {
			return true
		}
	}
	return false
}

// isConsecutive 检查 ranks 是否连续
func isConsecutive(ranks []Rank) bool {
	if len(ranks) < 2 {
		return len(ranks) == 1 || len(ranks) == 0
	}
	for i := 1; i < len(ranks); i++ {
		if ranks[i] != ranks[i-1]+1 {
			return false
		}
	}
	return true
}

// allBelow2 检查所有 rank 都 < Rank2（顺子、连对、飞机不能包含2和王）
func allBelow2(ranks []Rank) bool {
	for _, r := range ranks {
		if r >= Rank2 {
			return false
		}
	}
	return true
}
