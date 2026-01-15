package action

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
)

func RefillBeltFromInventory() error {
	defer step.CloseAllMenus()

	ctx := context.Get()
	ctx.Logger.Info("Refilling belt from inventory")

	healingPotions := ctx.Data.PotionsInInventory(data.HealingPotion)
	manaPotions := ctx.Data.PotionsInInventory(data.ManaPotion)
	rejuvPotions := ctx.Data.PotionsInInventory(data.RejuvenationPotion)

	missingHealingPotionCount := ctx.BeltManager.GetMissingCount(data.HealingPotion)
	missingManaPotionCount := ctx.BeltManager.GetMissingCount(data.ManaPotion)
	missingRejuvPotionCount := ctx.BeltManager.GetMissingCount(data.RejuvenationPotion)

	// Check for TP scrolls if using belt for TP
	missingTPScrollCount := 0
	var tpScrolls []data.Item
	if ctx.CharacterCfg.Inventory.UseScrollTPInBelt {
		missingTPScrollCount = ctx.BeltManager.GetMissingScrollTPCount()
		// Find TP scrolls in inventory
		for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if itm.Name == item.ScrollOfTownPortal {
				tpScrolls = append(tpScrolls, itm)
			}
		}
	}

	if !((missingHealingPotionCount > 0 && len(healingPotions) > 0) || (missingManaPotionCount > 0 && len(manaPotions) > 0) || (missingRejuvPotionCount > 0 && len(rejuvPotions) > 0) || (missingTPScrollCount > 0 && len(tpScrolls) > 0)) {
		ctx.Logger.Debug("No need to refill belt from inventory")
		return nil
	}

	// Add slight delay before opening inventory
	utils.Sleep(200)

	if err := step.OpenInventory(); err != nil {
		return err
	}

	// Refill healing potions
	for i := 0; i < missingHealingPotionCount && i < len(healingPotions); i++ {
		putPotionInBelt(ctx, healingPotions[i])
	}

	// Refill mana potions
	for i := 0; i < missingManaPotionCount && i < len(manaPotions); i++ {
		putPotionInBelt(ctx, manaPotions[i])
	}

	// Refill rejuvenation potions
	for i := 0; i < missingRejuvPotionCount && i < len(rejuvPotions); i++ {
		putPotionInBelt(ctx, rejuvPotions[i])
	}

	// Refill TP scrolls
	for i := 0; i < missingTPScrollCount && i < len(tpScrolls); i++ {
		putScrollTPInBelt(ctx, tpScrolls[i])
	}

	ctx.Logger.Info("Belt refilled from inventory")
	err := step.CloseAllMenus()
	if err != nil {
		return err
	}

	// Add slight delay after closing inventory
	utils.Sleep(200)
	return nil

}

func putPotionInBelt(ctx *context.Status, potion data.Item) {
	screenPos := ui.GetScreenCoordsForItem(potion)
	ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.ShiftKey)
	WaitForItemInBelt(potion.UnitID, 1000)
}

func putScrollTPInBelt(ctx *context.Status, scroll data.Item) {
	screenPos := ui.GetScreenCoordsForItem(scroll)
	ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.ShiftKey)
	WaitForItemInBelt(scroll.UnitID, 1000)
}
