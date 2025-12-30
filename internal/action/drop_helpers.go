package action

import (
	"fmt"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/stat"

	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/town"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
	"github.com/lxn/win"
)

// IsDropProtected determines which items must NOT be dropped
func IsDropProtected(i data.Item) bool {
	ctx := context.Get()
	selected := false
	DropperOnly := false
	filtersEnabled := false

	if ctx != nil && ctx.Context != nil {
		if ctx.Context.Drop != nil {
			filtersEnabled = ctx.Context.Drop.DropFiltersEnabled()
			if filtersEnabled {
				selected = ctx.Context.Drop.ShouldDropperItem(string(i.Name), i.Quality, i.Type().Code)
				DropperOnly = ctx.Context.Drop.DropperOnlySelected()
			}
		}
	}

	// Always keep the cube so the bot can continue farming afterward.
	if i.Name == "HoradricCube" {
		return true
	}

	// Protect keys based on KeyCount configuration
	// Allow dropping excess keys, but keep at least KeyCount
	if i.Name == item.Key {
		keyCount := getKeyCount()
		if keyCount <= 0 {
			// If KeyCount is 0 or disabled, never drop keys
			return true
		}

		// Count current keys in inventory
		totalKeys := 0
		if ctx != nil {
			for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
				if itm.Name == item.Key {
					if qty, found := itm.FindStat(stat.Quantity, 0); found {
						totalKeys += qty.Value
					} else {
						totalKeys++ // If no quantity stat, assume stack of 1
					}
				}
			}
		}

		// If we have at or below the configured amount, protect all keys
		if totalKeys <= keyCount {
			return true // Protect keys if we have at or below the configured amount
		}

		// We have excess keys, check if dropping this specific key would leave us below KeyCount
		keysInThisStack := 1
		if qty, found := i.FindStat(stat.Quantity, 0); found {
			keysInThisStack = qty.Value
		}

		// If dropping this key would leave us with less than KeyCount, protect it
		if totalKeys-keysInThisStack < keyCount {
			return true // Protect this key to maintain at least KeyCount
		}

		// This key is excess, allow dropping it
		return false
	}

	if selected {
		if ctx != nil && ctx.Context != nil && ctx.Context.Drop != nil && !ctx.Context.Drop.HasRemainingDropQuota(string(i.Name)) {
			return true
		}
		return false
	}

	// Keep recipe materials configured in cube settings.
	if shouldKeepRecipeItem(i) {
		return true
	}

	if i.Name == "GrandCharm" && ctx != nil && HasGrandCharmRerollCandidate(ctx) {
		return true
	}

	if !filtersEnabled {
		return false
	}

	if DropperOnly {
		return true
	}

	// Everything else should be dropped for Drop to ensure the stash empties fully.
	return false
}

func RunDropCleanup() error {
	ctx := context.Get()

	ctx.RefreshGameData()

	if !ctx.Data.PlayerUnit.Area.IsTown() {
		if err := ReturnTown(); err != nil {
			return fmt.Errorf("failed to return to town for Drop cleanup: %w", err)
		}
		// Update town/NPC data after the town portal sequence.
		ctx.RefreshGameData()
	}
	RecoverCorpse()

	IdentifyAll(false)
	ctx.PauseIfNotPriority()
	Stash(false)
	ctx.PauseIfNotPriority()
	DropVendorRefill(false, true)
	ctx.PauseIfNotPriority() // Check after VendorRefill
	Stash(false)
	ctx.PauseIfNotPriority() // Check after Stash

	ctx.RefreshGameData()
	if ctx.Data.OpenMenus.IsMenuOpen() {
		step.CloseAllMenus()
	}
	return nil
}

// HasGrandCharmRerollCandidate indicates whether a reroll-able GrandCharm + perfect gems exist in stash.
func HasGrandCharmRerollCandidate(ctx *context.Status) bool {
	ctx.RefreshGameData()
	items := ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
	_, ok := hasItemsForGrandCharmReroll(ctx, items)
	return ok
}

// DropVendorRefill is a Drop-specific vendor helper.
// - Always interacts with Akara as the vendor.
// - Sells junk items (and excess keys) via town.SellJunk, respecting optional lockConfig.
// - Does not buy potions, TP or ID scrolls, but will buy keys if needed.
func DropVendorRefill(forceRefill bool, sellJunk bool, tempLock ...[][]int) error {
	ctx := context.Get()
	ctx.SetLastAction("DropVendorRefill")

	ctx.RefreshGameData()

	// Determine if there is anything to sell before visiting the vendor.
	var lockConfig [][]int
	if len(tempLock) > 0 {
		lockConfig = tempLock[0]
	}

	hasJunkToSell := false
	if sellJunk {
		if len(lockConfig) > 0 {
			if len(town.ItemsToBeSold(lockConfig)) > 0 {
				hasJunkToSell = true
			}
		} else if len(town.ItemsToBeSold()) > 0 {
			hasJunkToSell = true
		}
	}

	// Check if we need to buy keys
	_, needsBuyKeys := town.ShouldBuyKeys()

	// If we are not selling anything and don't need to buy keys, skip vendor entirely.
	if !hasJunkToSell && !needsBuyKeys {
		return nil
	}

	ctx.Logger.Info("Drop: Visiting Akara...", "forceRefill", forceRefill, "hasJunkToSell", hasJunkToSell, "needsBuyKeys", needsBuyKeys)

	if err := InteractNPC(npc.Akara); err != nil {
		return err
	}

	// Akara trade menu: HOME -> DOWN -> ENTER
	ctx.HID.KeySequence(win.VK_HOME, win.VK_DOWN, win.VK_RETURN)

	if sellJunk {
		if len(lockConfig) > 0 {
			town.SellJunk(lockConfig)
		} else {
			town.SellJunk()
		}
	}

	// Switch to tab 4 (keys are usually in tab 4) and buy keys if needed
	SwitchVendorTab(4)
	ctx.RefreshGameData()

	if needsBuyKeys && ctx.Data.PlayerUnit.Class != data.Assassin {
		// Buy keys using the same logic as BuyConsumables
		keyQuantity, _ := town.ShouldBuyKeys()
		if itm, found := ctx.Data.Inventory.Find(item.Key, item.LocationVendor); found {
			ctx.Logger.Debug("Drop: Vendor with keys detected, provisioning...")
			keyCount := getKeyCount()
			qtyVendor, _ := itm.FindStat(stat.Quantity, 0)
			if (qtyVendor.Value > 0) && keyCount > 0 && (keyQuantity < keyCount) {
				// Use buyFullStack logic: for keys, Shift+Right Click fills the stack
				// If 0 keys, need two clicks; if >0 keys, one click fills
				screenPos := ui.GetScreenCoordsForItem(itm)
				ctx.HID.ClickWithModifier(game.RightButton, screenPos.X, screenPos.Y, game.ShiftKey)
				utils.PingSleep(utils.Light, 200)
				if keyQuantity == 0 {
					// If 0 keys, first click buys 1, second click fills the stack
					ctx.HID.ClickWithModifier(game.RightButton, screenPos.X, screenPos.Y, game.ShiftKey)
					utils.PingSleep(utils.Light, 200)
				}
			}
		}
	}

	return step.CloseAllMenus()
}
