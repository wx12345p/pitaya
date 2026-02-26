package game

import (
	"fmt"
	"sort"
	"strings"
)

// Suit 花色
type Suit int

const (
	SuitSpade   Suit = iota // 黑桃 ♠
	SuitHeart               // 红桃 ♥
	SuitClub                // 梅花 ♣
	SuitDiamond             // 方块 ♦
	SuitJoker               // 王牌（特殊）
)

var suitNames = map[Suit]string{
	SuitSpade:   "♠",
	SuitHeart:   "♥",
	SuitClub:    "♣",
	SuitDiamond: "♦",
	SuitJoker:   "🃏",
}

// Rank 点数
type Rank int

const (
	Rank3          Rank = iota + 3 // 3
	Rank4                          // 4
	Rank5                          // 5
	Rank6                          // 6
	Rank7                          // 7
	Rank8                          // 8
	Rank9                          // 9
	Rank10                         // 10
	RankJ                          // J (11)
	RankQ                          // Q (12)
	RankK                          // K (13)
	RankA                          // A (14)
	Rank2                          // 2 (15)
	RankSmallJoker                 // 小王 (16)
	RankBigJoker                   // 大王 (17)
)

var rankNames = map[Rank]string{
	Rank3:          "3",
	Rank4:          "4",
	Rank5:          "5",
	Rank6:          "6",
	Rank7:          "7",
	Rank8:          "8",
	Rank9:          "9",
	Rank10:         "10",
	RankJ:          "J",
	RankQ:          "Q",
	RankK:          "K",
	RankA:          "A",
	Rank2:          "2",
	RankSmallJoker: "小王",
	RankBigJoker:   "大王",
}

// Card 一张扑克牌
type Card struct {
	Suit Suit `json:"suit"`
	Rank Rank `json:"rank"`
}

// Weight 返回牌的权重，用于排序和比较
func (c Card) Weight() int {
	return int(c.Rank)
}

// String 返回牌的可读字符串
func (c Card) String() string {
	if c.Rank == RankSmallJoker {
		return "小王"
	}
	if c.Rank == RankBigJoker {
		return "大王"
	}
	return fmt.Sprintf("%s%s", suitNames[c.Suit], rankNames[c.Rank])
}

// ID 返回牌的唯一标识，用于网络传输
func (c Card) ID() int {
	return int(c.Suit)*100 + int(c.Rank)
}

// CardFromID 从 ID 还原一张牌
func CardFromID(id int) Card {
	return Card{
		Suit: Suit(id / 100),
		Rank: Rank(id % 100),
	}
}

// CardsFromIDs 批量从 ID 还原牌
func CardsFromIDs(ids []int) []Card {
	cards := make([]Card, len(ids))
	for i, id := range ids {
		cards[i] = CardFromID(id)
	}
	return cards
}

// CardsToIDs 批量转换牌为 ID
func CardsToIDs(cards []Card) []int {
	ids := make([]int, len(cards))
	for i, c := range cards {
		ids[i] = c.ID()
	}
	return ids
}

// CardsToIDs32 批量转换牌为 int32 ID（供 protobuf 消息使用）
func CardsToIDs32(cards []Card) []int32 {
	ids := make([]int32, len(cards))
	for i, c := range cards {
		ids[i] = int32(c.ID())
	}
	return ids
}

// CardsFromIDs32 从 int32 ID 批量还原牌（供 protobuf 消息使用）
func CardsFromIDs32(ids []int32) []Card {
	cards := make([]Card, len(ids))
	for i, id := range ids {
		cards[i] = CardFromID(int(id))
	}
	return cards
}

// IDs32ToIDs 将 []int32 转换为 []int
func IDs32ToIDs(ids []int32) []int {
	result := make([]int, len(ids))
	for i, id := range ids {
		result[i] = int(id)
	}
	return result
}

// IDsToIDs32 将 []int 转换为 []int32
func IDsToIDs32(ids []int) []int32 {
	result := make([]int32, len(ids))
	for i, id := range ids {
		result[i] = int32(id)
	}
	return result
}

// SortCards 按权重从小到大排序
func SortCards(cards []Card) {
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].Rank == cards[j].Rank {
			return cards[i].Suit < cards[j].Suit
		}
		return cards[i].Rank < cards[j].Rank
	})
}

// SortCardsDesc 按权重从大到小排序
func SortCardsDesc(cards []Card) {
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].Rank == cards[j].Rank {
			return cards[i].Suit > cards[j].Suit
		}
		return cards[i].Rank > cards[j].Rank
	})
}

// CardsString 返回一组牌的可读字符串
func CardsString(cards []Card) string {
	strs := make([]string, len(cards))
	for i, c := range cards {
		strs[i] = c.String()
	}
	return strings.Join(strs, " ")
}

// GetRankCount 统计每个点数出现的次数
func GetRankCount(cards []Card) map[Rank]int {
	counts := make(map[Rank]int)
	for _, c := range cards {
		counts[c.Rank]++
	}
	return counts
}

// ContainsCard 检查牌列表是否包含某张牌
func ContainsCard(cards []Card, card Card) bool {
	for _, c := range cards {
		if c.Suit == card.Suit && c.Rank == card.Rank {
			return true
		}
	}
	return false
}

// RemoveCards 从手牌中移除指定的牌
func RemoveCards(hand []Card, toRemove []Card) []Card {
	removed := make(map[int]bool)
	for _, c := range toRemove {
		removed[c.ID()] = true
	}
	result := make([]Card, 0, len(hand)-len(toRemove))
	for _, c := range hand {
		if removed[c.ID()] {
			// 只移除一次
			delete(removed, c.ID())
			continue
		}
		result = append(result, c)
	}
	return result
}
