package health

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/event"
	"github.com/hectorgimenez/koolo/internal/game"
)

type BeltManager struct {
	data       *game.Data
	hid        *game.HID
	logger     *slog.Logger
	supervisor string
}

func NewBeltManager(data *game.Data, hid *game.HID, logger *slog.Logger, supervisor string) *BeltManager {
	return &BeltManager{
		data:       data,
		hid:        hid,
		logger:     logger,
		supervisor: supervisor,
	}
}

func (bm BeltManager) DrinkPotion(potionType data.PotionType, merc bool) bool {
	p, found := bm.data.Inventory.Belt.GetFirstPotion(potionType)
	if found {
		binding := bm.data.KeyBindings.UseBelt[p.X]
		if merc {
			bm.hid.PressKeyWithModifier(binding.Key1[0], game.ShiftKey)
			bm.logger.Debug(fmt.Sprintf("Using %s potion on Mercenary [Column: %d]. HP: %d", potionType, p.X+1, bm.data.MercHPPercent()))
			event.Send(event.UsedPotion(event.Text(bm.supervisor, ""), potionType, true))
			return true
		}
		bm.hid.PressKeyBinding(binding)
		bm.logger.Debug(fmt.Sprintf("Using %s potion [Column: %d]. HP: %d MP: %d", potionType, p.X+1, bm.data.PlayerUnit.HPPercent(), bm.data.PlayerUnit.MPPercent()))
		event.Send(event.UsedPotion(event.Text(bm.supervisor, ""), potionType, false))
		return true
	}

	return false
}

// ShouldBuyPotions will return true if more than 25% of belt is empty (ignoring rejuv)
func (bm BeltManager) ShouldBuyPotions() bool {
	targetHealingAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.HealingPotion) * bm.data.Inventory.Belt.Rows()
	targetManaAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.ManaPotion) * bm.data.Inventory.Belt.Rows()
	targetRejuvAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.RejuvenationPotion) * bm.data.Inventory.Belt.Rows()

	currentHealing, currentMana, currentRejuv := bm.getCurrentPotions()

	bm.logger.Debug(fmt.Sprintf(
		"Belt Stats Health: %d/%d healing, %d/%d mana, %d/%d rejuv.",
		currentHealing,
		targetHealingAmount,
		currentMana,
		targetManaAmount,
		currentRejuv,
		targetRejuvAmount,
	))

	if currentHealing < int(float32(targetHealingAmount)*0.75) || currentMana < int(float32(targetManaAmount)*0.75) {
		bm.logger.Debug("Need more pots, let's buy them.")
		return true
	}

	return false
}

func (bm BeltManager) getCurrentPotions() (int, int, int) {
	currentHealing := 0
	currentMana := 0
	currentRejuv := 0
	for _, i := range bm.data.Inventory.Belt.Items {
		if strings.Contains(string(i.Name), string(data.HealingPotion)) {
			currentHealing++
			continue
		}
		if strings.Contains(string(i.Name), string(data.ManaPotion)) {
			currentMana++
			continue
		}
		if strings.Contains(string(i.Name), string(data.RejuvenationPotion)) {
			currentRejuv++
		}
	}

	return currentHealing, currentMana, currentRejuv
}

func (bm BeltManager) GetMissingCount(potionType data.PotionType) int {
	currentHealing, currentMana, currentRejuv := bm.getCurrentPotions()

	switch potionType {
	case data.HealingPotion:
		targetAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.HealingPotion) * bm.data.Inventory.Belt.Rows()
		missingPots := targetAmount - currentHealing
		if missingPots < 0 {
			return 0
		}
		return missingPots
	case data.ManaPotion:
		targetAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.ManaPotion) * bm.data.Inventory.Belt.Rows()
		missingPots := targetAmount - currentMana
		if missingPots < 0 {
			return 0
		}
		return missingPots
	case data.RejuvenationPotion:
		targetAmount := bm.data.CharacterCfg.Inventory.BeltColumns.Total(data.RejuvenationPotion) * bm.data.Inventory.Belt.Rows()
		missingPots := targetAmount - currentRejuv
		if missingPots < 0 {
			return 0
		}
		return missingPots
	}

	return 0
}

// getTPScrollColumn finds which belt column is configured for TP scrolls
func (bm BeltManager) getTPScrollColumn() (int, bool) {
	// First check if "tp" is in any belt column
	for i, col := range bm.data.CharacterCfg.Inventory.BeltColumns {
		if strings.EqualFold(col, "tp") {
			return i, true
		}
	}
	// Fallback to TPScrollBeltColumn if no "tp" found in beltColumns
	tpScrollColumn := bm.data.CharacterCfg.Inventory.TPScrollBeltColumn
	if tpScrollColumn >= 0 && tpScrollColumn <= 3 {
		return tpScrollColumn, true
	}
	return -1, false
}

// GetFirstScrollTP finds the first Scroll of Town Portal in the belt
func (bm BeltManager) GetFirstScrollTP() (data.Item, bool) {
	if !bm.data.CharacterCfg.Inventory.UseScrollTPInBelt {
		return data.Item{}, false
	}

	tpScrollColumn, found := bm.getTPScrollColumn()
	if !found {
		return data.Item{}, false
	}

	rows := bm.data.Inventory.Belt.Rows()
	for row := 0; row < rows; row++ {
		beltIndex := row*4 + tpScrollColumn
		for _, beltItem := range bm.data.Inventory.Belt.Items {
			if beltItem.Position.X == beltIndex && beltItem.Name == item.ScrollOfTownPortal {
				return beltItem, true
			}
		}
	}

	return data.Item{}, false
}

// GetMissingScrollTPCount returns how many TP scrolls are missing in the belt
func (bm BeltManager) GetMissingScrollTPCount() int {
	if !bm.data.CharacterCfg.Inventory.UseScrollTPInBelt {
		return 0
	}

	tpScrollColumn, found := bm.getTPScrollColumn()
	if !found {
		return 0
	}

	rows := bm.data.Inventory.Belt.Rows()
	targetAmount := rows
	currentCount := 0

	for _, beltItem := range bm.data.Inventory.Belt.Items {
		if beltItem.Name == item.ScrollOfTownPortal {
			// Check if it's in the correct column
			if beltItem.Position.X%4 == tpScrollColumn {
				currentCount++
			}
		}
	}

	missing := targetAmount - currentCount
	if missing < 0 {
		return 0
	}
	return missing
}
