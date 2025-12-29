package action

import (
	"log/slog"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	botCtx "github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/town"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
	"github.com/lxn/win"
)

func VendorRefill(forceRefill bool, sellJunk bool, tempLock ...[][]int) (err error) {
	ctx := botCtx.Get()
	ctx.SetLastAction("VendorRefill")

	// Check if we should drop items instead of selling
	var lockConfig [][]int
	if len(tempLock) > 0 {
		lockConfig = tempLock[0]
	}
	shouldDrop := false
	hasKeysToSell := false
	if sellJunk {
		// Check for excess keys that need to be sold
		ctx.RefreshGameData()
		totalKeys := 0
		for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if itm.Name == item.Key {
				if qty, found := itm.FindStat(stat.Quantity, 0); found {
					totalKeys += qty.Value
				}
			}
		}
		hasKeysToSell = totalKeys > 12

		currentGold := ctx.Data.PlayerUnit.TotalPlayerGold()
		minGoldToDrop := ctx.CharacterCfg.Vendor.MinGoldToDrop
		shouldDrop = minGoldToDrop > 0 && currentGold >= minGoldToDrop && ctx.Data.PlayerUnit.Area.IsTown()
		
		// Check if there are items to process
		if shouldDrop {
			var itemsToCheck []data.Item
			if len(lockConfig) > 0 {
				itemsToCheck = town.ItemsToBeSold(lockConfig)
			} else {
				itemsToCheck = town.ItemsToBeSold()
			}
			if len(itemsToCheck) == 0 {
				shouldDrop = false
			}
		}
	}

	// If dropping items and no keys to sell, process them without visiting vendor
	if shouldDrop && !hasKeysToSell {
		ctx.Logger.Info("Dropping items instead of visiting vendor (gold threshold reached, no keys to sell)")
		if len(lockConfig) > 0 {
			town.SellJunk(lockConfig)
		} else {
			town.SellJunk()
		}
		// Still need to buy consumables if forceRefill is true
		if forceRefill {
			ctx.Logger.Info("Visiting vendor for consumables...", slog.Bool("forceRefill", forceRefill))
			vendorNPC := town.GetTownByArea(ctx.Data.PlayerUnit.Area).RefillNPC()
			if vendorNPC == npc.Drognan {
				_, needsBuy := town.ShouldBuyKeys()
				if needsBuy && ctx.Data.PlayerUnit.Class != data.Assassin {
					vendorNPC = npc.Lysander
				}
			}
			if vendorNPC == npc.Ormus {
				_, needsBuy := town.ShouldBuyKeys()
				if needsBuy && ctx.Data.PlayerUnit.Class != data.Assassin {
					if err := FindHratliEverywhere(); err != nil {
						return err
					}
					vendorNPC = npc.Hratli
				}
			}
			err = InteractNPC(vendorNPC)
			if err != nil {
				return err
			}
			if vendorNPC == npc.Jamella {
				ctx.HID.KeySequence(win.VK_HOME, win.VK_RETURN)
			} else {
				ctx.HID.KeySequence(win.VK_HOME, win.VK_DOWN, win.VK_RETURN)
			}
			SwitchVendorTab(4)
			ctx.RefreshGameData()
			town.BuyConsumables(forceRefill)
			return step.CloseAllMenus()
		}
		return nil
	}

	// This is a special case, we want to sell junk, but we don't have enough space to unequip items
	if !forceRefill && !shouldVisitVendor() && len(tempLock) == 0 {
		return nil
	}

	ctx.Logger.Info("Visiting vendor...", slog.Bool("forceRefill", forceRefill))

	vendorNPC := town.GetTownByArea(ctx.Data.PlayerUnit.Area).RefillNPC()
	if vendorNPC == npc.Drognan {
		_, needsBuy := town.ShouldBuyKeys()
		if needsBuy && ctx.Data.PlayerUnit.Class != data.Assassin {
			vendorNPC = npc.Lysander
		}
	}
	if vendorNPC == npc.Ormus {
		_, needsBuy := town.ShouldBuyKeys()
		if needsBuy && ctx.Data.PlayerUnit.Class != data.Assassin {
			if err := FindHratliEverywhere(); err != nil {
				// If moveToHratli returns an error, it means a forced game quit is required.
				return err
			}
			vendorNPC = npc.Hratli
		}
	}

	err = InteractNPC(vendorNPC)
	if err != nil {
		return err
	}

	// Jamella trade button is the first one
	if vendorNPC == npc.Jamella {
		ctx.HID.KeySequence(win.VK_HOME, win.VK_RETURN)
	} else {
		ctx.HID.KeySequence(win.VK_HOME, win.VK_DOWN, win.VK_RETURN)
	}

	if sellJunk {
		if len(lockConfig) > 0 {
			town.SellJunk(lockConfig)
		} else {
			town.SellJunk()
		}
	}
	SwitchVendorTab(4)
	ctx.RefreshGameData()
	town.BuyConsumables(forceRefill)

	return step.CloseAllMenus()
}

func BuyAtVendor(vendor npc.ID, items ...VendorItemRequest) error {
	ctx := botCtx.Get()
	ctx.SetLastAction("BuyAtVendor")

	err := InteractNPC(vendor)
	if err != nil {
		return err
	}

	// Jamella trade button is the first one
	if vendor == npc.Jamella {
		ctx.HID.KeySequence(win.VK_HOME, win.VK_DOWN, win.VK_RETURN)
	} else {
		ctx.HID.KeySequence(win.VK_HOME, win.VK_DOWN, win.VK_RETURN)
	}

	for _, i := range items {
		SwitchVendorTab(i.Tab)
		itm, found := ctx.Data.Inventory.Find(i.Item, item.LocationVendor)
		if found {
			town.BuyItem(itm, i.Quantity)
		} else {
			ctx.Logger.Warn("Item not found in vendor", slog.String("Item", string(i.Item)))
		}
	}

	return step.CloseAllMenus()
}

type VendorItemRequest struct {
	Item     item.Name
	Quantity int
	Tab      int
}

func shouldVisitVendor() bool {
	ctx := botCtx.Get()
	ctx.SetLastStep("shouldVisitVendor")

	if len(town.ItemsToBeSold()) > 0 {
		return true
	}

	if ctx.Data.PlayerUnit.TotalPlayerGold() < 1000 {
		return false
	}

	if ctx.BeltManager.ShouldBuyPotions() || town.ShouldBuyTPs() || town.ShouldBuyIDs() {
		return true
	}

	return false
}

func SwitchVendorTab(tab int) {
	// Ensure any chat messages that could prevent clicking on the tab are cleared
	ClearMessages()
	utils.Sleep(200)

	ctx := context.Get()
	ctx.SetLastStep("switchVendorTab")

	if ctx.GameReader.LegacyGraphics() {
		x := ui.SwitchVendorTabBtnXClassic
		y := ui.SwitchVendorTabBtnYClassic

		tabSize := ui.SwitchVendorTabBtnTabSizeClassic
		x = x + tabSize*tab - tabSize/2
		ctx.HID.Click(game.LeftButton, x, y)
		utils.PingSleep(utils.Medium, 500) // Medium operation: Wait for tab switch
	} else {
		x := ui.SwitchVendorTabBtnX
		y := ui.SwitchVendorTabBtnY

		tabSize := ui.SwitchVendorTabBtnTabSize
		x = x + tabSize*tab - tabSize/2
		ctx.HID.Click(game.LeftButton, x, y)
		utils.PingSleep(utils.Medium, 500) // Medium operation: Wait for tab switch
	}
}
