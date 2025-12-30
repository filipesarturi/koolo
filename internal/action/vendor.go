package action

import (
	"fmt"
	"log/slog"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
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

// getTownAreaByAct returns the town area ID for a given act number
func getTownAreaByAct(act int) area.ID {
	switch act {
	case 1:
		return area.RogueEncampment
	case 2:
		return area.LutGholein
	case 3:
		return area.KurastDocks
	case 4:
		return area.ThePandemoniumFortress
	case 5:
		return area.Harrogath
	default:
		return area.RogueEncampment // Default to Act 1
	}
}

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
	var originalAreaForReturn area.ID
	needsToMoveToAct := false
	
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
		currentAct := ctx.Data.PlayerUnit.Area.Act()
		alwaysDropAct := ctx.CharacterCfg.Vendor.AlwaysDropAct

		// Check if should drop based on act or gold threshold
		// Priority: If AlwaysDropAct is configured AND minGoldToDrop threshold is met, always drop in configured act
		if alwaysDropAct > 0 && alwaysDropAct <= 5 && minGoldToDrop > 0 && currentGold >= minGoldToDrop {
			// Need to drop in the configured act - move there if not already there
			shouldDrop = true
			if !ctx.Data.PlayerUnit.Area.IsTown() || currentAct != alwaysDropAct {
				needsToMoveToAct = true
				originalAreaForReturn = ctx.Data.PlayerUnit.Area
				targetTownArea := getTownAreaByAct(alwaysDropAct)
				ctx.Logger.Info(fmt.Sprintf("Moving to Act %d town to drop items near stash (gold threshold reached, current act: %d)", alwaysDropAct, currentAct))
				if err := WayPoint(targetTownArea); err != nil {
					ctx.Logger.Warn(fmt.Sprintf("Failed to move to Act %d town, dropping items at current location: %v", alwaysDropAct, err))
					shouldDrop = false // Can't drop in configured act, so don't drop
					needsToMoveToAct = false
					originalAreaForReturn = area.ID(0) // Reset since we're not moving
				} else {
					ctx.RefreshGameData()
				}
			}
		} else if alwaysDropAct > 0 && alwaysDropAct <= 5 && currentAct == alwaysDropAct {
			// Already in the configured act, drop near stash
			shouldDrop = ctx.Data.PlayerUnit.Area.IsTown()
		} else if minGoldToDrop > 0 && currentGold >= minGoldToDrop {
			// Only minGoldToDrop threshold is met (AlwaysDropAct not configured or not in that act)
			shouldDrop = ctx.Data.PlayerUnit.Area.IsTown()
		}
		
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
		ctx.Logger.Info("Dropping items instead of visiting vendor (gold threshold or AlwaysDropAct reached, no keys to sell)")
		// First, drop excess keys and other junk items
		if len(lockConfig) > 0 {
			town.SellJunk(lockConfig)
		} else {
			town.SellJunk()
		}
		// Refresh after dropping to get accurate key count
		ctx.RefreshGameData()
		// Return to original area if we moved to a different act
		if needsToMoveToAct && originalAreaForReturn != area.ID(0) {
			if originalAreaForReturn != ctx.Data.PlayerUnit.Area {
				ctx.Logger.Info(fmt.Sprintf("Returning to original area: %s", originalAreaForReturn.Area().Name))
				if err := WayPoint(originalAreaForReturn); err != nil {
					ctx.Logger.Warn(fmt.Sprintf("Failed to return to original area: %v", err))
				} else {
					ctx.RefreshGameData()
				}
			}
		}
		// Check if we need to buy consumables (forceRefill or missing keys) AFTER dropping excess
		_, needsBuyKeys := town.ShouldBuyKeys()
		if forceRefill || needsBuyKeys {
			ctx.Logger.Info("Visiting vendor for consumables...", slog.Bool("forceRefill", forceRefill), slog.Bool("needsBuyKeys", needsBuyKeys))
			vendorNPC := town.GetTownByArea(ctx.Data.PlayerUnit.Area).RefillNPC()
			if vendorNPC == npc.Drognan {
				if needsBuyKeys && ctx.Data.PlayerUnit.Class != data.Assassin {
					vendorNPC = npc.Lysander
				}
			}
			if vendorNPC == npc.Ormus {
				if needsBuyKeys && ctx.Data.PlayerUnit.Class != data.Assassin {
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

	// First, drop excess keys if we're selling junk (this happens before checking if we need to buy)
	if sellJunk {
		if len(lockConfig) > 0 {
			town.SellJunk(lockConfig)
		} else {
			town.SellJunk()
		}
		// Refresh after dropping excess keys to get accurate key count
		ctx.RefreshGameData()
	}

	// Now check if we need to buy keys (after dropping excess)
	vendorNPC := town.GetTownByArea(ctx.Data.PlayerUnit.Area).RefillNPC()
	_, needsBuyKeys := town.ShouldBuyKeys()
	if vendorNPC == npc.Drognan {
		if needsBuyKeys && ctx.Data.PlayerUnit.Class != data.Assassin {
			vendorNPC = npc.Lysander
		}
	}
	if vendorNPC == npc.Ormus {
		if needsBuyKeys && ctx.Data.PlayerUnit.Class != data.Assassin {
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

	// If we didn't sell junk yet (because sellJunk was false), do it now
	// But this should be rare since we usually want to sell junk
	if !sellJunk {
		// Even if sellJunk is false, we might still want to drop excess keys
		// But only if we're not visiting vendor just for consumables
		if forceRefill {
			// Just refresh and proceed to buy consumables
			ctx.RefreshGameData()
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

	_, needsBuyKeys := town.ShouldBuyKeys()
	if ctx.BeltManager.ShouldBuyPotions() || town.ShouldBuyTPs() || town.ShouldBuyIDs() || needsBuyKeys {
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
