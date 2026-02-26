package game

import (
	"math/rand"
)

// DealResult 发牌结果
type DealResult struct {
	Player1 []Card // 玩家1的手牌 (17张)
	Player2 []Card // 玩家2的手牌 (17张)
	Player3 []Card // 玩家3的手牌 (17张)
	Bottom  []Card // 底牌 (3张)
}

// NewDeck 创建一副新牌（54张）
func NewDeck() []Card {
	cards := make([]Card, 0, 54)

	// 4种花色 x 13个点数 = 52张
	suits := []Suit{SuitSpade, SuitHeart, SuitClub, SuitDiamond}
	for _, suit := range suits {
		for rank := Rank3; rank <= Rank2; rank++ {
			cards = append(cards, Card{Suit: suit, Rank: rank})
		}
	}

	// 小王和大王
	cards = append(cards, Card{Suit: SuitJoker, Rank: RankSmallJoker})
	cards = append(cards, Card{Suit: SuitJoker, Rank: RankBigJoker})

	return cards
}

// Shuffle 洗牌
func Shuffle(cards []Card) {
	rand.Shuffle(len(cards), func(i, j int) {
		cards[i], cards[j] = cards[j], cards[i]
	})
}

// Deal 发牌：3个玩家各17张，剩余3张底牌
func Deal() *DealResult {
	deck := NewDeck()
	Shuffle(deck)

	result := &DealResult{
		Player1: make([]Card, 17),
		Player2: make([]Card, 17),
		Player3: make([]Card, 17),
		Bottom:  make([]Card, 3),
	}

	copy(result.Player1, deck[0:17])
	copy(result.Player2, deck[17:34])
	copy(result.Player3, deck[34:51])
	copy(result.Bottom, deck[51:54])

	// 排序
	SortCards(result.Player1)
	SortCards(result.Player2)
	SortCards(result.Player3)
	SortCards(result.Bottom)

	return result
}
