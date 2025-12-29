package town

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/nip"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
	"github.com/lxn/win"
)

var questItems = []item.Name{
	"StaffOfKings",
	"HoradricStaff",
	"AmuletOfTheViper",
	"KhalimsFlail",
	"KhalimsWill",
	"HellforgeHammer",
}

func BuyConsumables(forceRefill bool) {
	ctx := context.Get()

	missingHealingPotionInBelt := ctx.BeltManager.GetMissingCount(data.HealingPotion)
	missingManaPotiontInBelt := ctx.BeltManager.GetMissingCount(data.ManaPotion)
	missingHealingPotionInInventory := ctx.Data.MissingPotionCountInInventory(data.HealingPotion)
	missingManaPotionInInventory := ctx.Data.MissingPotionCountInInventory(data.ManaPotion)

	// We traverse the items in reverse order because vendor has the best potions at the end
	healingPot, healingPotfound := findFirstMatch("superhealingpotion", "greaterhealingpotion", "healingpotion", "lighthealingpotion", "minorhealingpotion")
	manaPot, manaPotfound := findFirstMatch("supermanapotion", "greatermanapotion", "manapotion", "lightmanapotion", "minormanapotion")

	ctx.Logger.Debug(fmt.Sprintf("Buying: %d Healing potions and %d Mana potions for belt", missingHealingPotionInBelt, missingManaPotiontInBelt))

	if ShouldBuyTPs() || forceRefill {
		if _, found := ctx.Data.Inventory.Find(item.TomeOfTownPortal, item.LocationInventory); !found && ctx.Data.PlayerUnit.TotalPlayerGold() > 450 {
			ctx.Logger.Info("TP Tome not found, buying one...")
			if itm, itmFound := ctx.Data.Inventory.Find(item.TomeOfTownPortal, item.LocationVendor); itmFound {
				BuyItem(itm, 1)
			}
		}
	}

	// buy for belt first
	if healingPotfound && missingHealingPotionInBelt > 0 {
		BuyItem(healingPot, missingHealingPotionInBelt)
		missingHealingPotionInBelt = 0
	}

	if manaPotfound && missingManaPotiontInBelt > 0 {
		BuyItem(manaPot, missingManaPotiontInBelt)
		missingManaPotiontInBelt = 0
	}

	ctx.Logger.Debug(fmt.Sprintf("Buying: %d Healing potions and %d Mana potions for inventory", missingHealingPotionInInventory, missingManaPotionInInventory))

	// then buy for inventory
	if healingPotfound && missingHealingPotionInInventory > 0 {
		BuyItem(healingPot, missingHealingPotionInInventory)
		missingHealingPotionInInventory = 0
	}

	if manaPotfound && missingManaPotionInInventory > 0 {
		BuyItem(manaPot, missingManaPotionInInventory)
		missingManaPotionInInventory = 0
	}

	if ShouldBuyTPs() || forceRefill {
		ctx.Logger.Debug("Filling TP Tome...")
		if itm, found := ctx.Data.Inventory.Find(item.ScrollOfTownPortal, item.LocationVendor); found {
			if ctx.Data.PlayerUnit.TotalPlayerGold() > 6000 {
				buyFullStack(itm, -1) // -1 for irrelevant currentKeysInInventory
			} else {
				BuyItem(itm, 1)
			}
		}
	}

	if ShouldBuyIDs() || forceRefill {
		if _, found := ctx.Data.Inventory.Find(item.TomeOfIdentify, item.LocationInventory); !found && ctx.Data.PlayerUnit.TotalPlayerGold() > 360 {
			ctx.Logger.Info("ID Tome not found, buying one...")
			if itm, itmFound := ctx.Data.Inventory.Find(item.TomeOfIdentify, item.LocationVendor); itmFound {
				BuyItem(itm, 1)
			}
		}
		ctx.Logger.Debug("Filling IDs Tome...")
		if itm, found := ctx.Data.Inventory.Find(item.ScrollOfIdentify, item.LocationVendor); found {
			if ctx.Data.PlayerUnit.TotalPlayerGold() > 16000 {
				buyFullStack(itm, -1) // -1 for irrelevant currentKeysInInventory
			} else {
				BuyItem(itm, 1)
			}
		}
	}

	keyQuantity, shouldBuyKeys := ShouldBuyKeys() // keyQuantity is total keys in inventory
	if ctx.Data.PlayerUnit.Class != data.Assassin && (shouldBuyKeys || forceRefill) {
		if itm, found := ctx.Data.Inventory.Find(item.Key, item.LocationVendor); found {
			ctx.Logger.Debug("Vendor with keys detected, provisioning...")

			// Only buy if vendor has keys and we have less than configured KeyCount
			keyCount := getKeyCount()
			qtyVendor, _ := itm.FindStat(stat.Quantity, 0)
			if (qtyVendor.Value > 0) && keyCount > 0 && (keyQuantity < keyCount) {
				// Pass keyQuantity to buyFullStack so it knows how many keys we had initially
				buyFullStack(itm, keyQuantity)
			}
		}
	}
}

func findFirstMatch(itemNames ...string) (data.Item, bool) {
	ctx := context.Get()
	for _, name := range itemNames {
		if itm, found := ctx.Data.Inventory.Find(item.Name(name), item.LocationVendor); found {
			return itm, true
		}
	}

	return data.Item{}, false
}

func ShouldBuyTPs() bool {
	portalTome, found := context.Get().Data.Inventory.Find(item.TomeOfTownPortal, item.LocationInventory)
	if !found {
		return true
	}

	qty, found := portalTome.FindStat(stat.Quantity, 0)

	return qty.Value < 5 || !found
}

func ShouldBuyIDs() bool {
	ctx := context.Get()

	_, isLevelingChar := ctx.Char.(context.LevelingCharacter)

	// Respect end-game setting: completely disable ID tome purchasing
	if ctx.CharacterCfg.Game.DisableIdentifyTome && !isLevelingChar {
		// Do not buy Tome of Identify nor ID scrolls at all
		ctx.Logger.Debug("DisableIdentifyTome enabled – skipping ID tome/scroll purchases.")
		return false
	}

	// Original behaviour: keep at least 10 IDs in the tome
	idTome, found := ctx.Data.Inventory.Find(item.TomeOfIdentify, item.LocationInventory)
	if !found {
		return true
	}

	qty, found := idTome.FindStat(stat.Quantity, 0)
	return !found || qty.Value < 10
}

// getKeyCount returns the configured KeyCount, or 12 as default if not defined
// Returns 0 if explicitly disabled (KeyCount set to 0)
func getKeyCount() int {
	ctx := context.Get()
	if ctx.CharacterCfg.Inventory.KeyCount == nil {
		// Not defined, use default of 12
		return 12
	}
	// If explicitly set to 0, it's disabled
	return *ctx.CharacterCfg.Inventory.KeyCount
}

func ShouldBuyKeys() (int, bool) {
	// Re-calculating total keys each time ShouldBuyKeys is called for accuracy
	ctx := context.Get()
	totalKeys := 0
	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if itm.Name == item.Key {
			if qty, found := itm.FindStat(stat.Quantity, 0); found {
				totalKeys += qty.Value
			}
		}
	}

	keyCount := getKeyCount()
	if keyCount <= 0 {
		// If KeyCount is 0 (explicitly disabled), don't buy keys automatically
		return totalKeys, false
	}

	if totalKeys == 0 {
		return 0, true // No keys found, so we should buy
	}

	// We only need to buy if we have less than the configured KeyCount
	return totalKeys, totalKeys < keyCount
}

func SellJunk(lockConfig ...[][]int) {
	ctx := context.Get()
	ctx.Logger.Debug("--- SellJunk() function entered ---")
	ctx.Logger.Debug("Selling junk items and excess keys...")

	// --- OPTIMIZED LOGIC FOR SELLING EXCESS KEYS ---
	var allKeyStacks []data.Item
	totalKeys := 0

	// Iterate through ALL items in the inventory to find all key stacks
	// Make sure to re-fetch inventory data before this loop if it hasn't been refreshed recently
	ctx.RefreshGameData() // Crucial to have up-to-date inventory
	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if itm.Name == item.Key {
			if qty, found := itm.FindStat(stat.Quantity, 0); found {
				allKeyStacks = append(allKeyStacks, itm)
				totalKeys += qty.Value
			}
		}
	}

	ctx.Logger.Debug(fmt.Sprintf("Total keys found across all stacks in inventory: %d", totalKeys))

	if totalKeys > 12 {
		excessCount := totalKeys - 12
		ctx.Logger.Info(fmt.Sprintf("Found %d excess keys (total %d). Selling them.", excessCount, totalKeys))

		keysSold := 0

		// Sort key stacks by quantity in descending order to sell larger stacks first
		slices.SortFunc(allKeyStacks, func(a, b data.Item) int {
			qtyA, _ := a.FindStat(stat.Quantity, 0)
			qtyB, _ := b.FindStat(stat.Quantity, 0)
			return qtyB.Value - qtyA.Value // Descending order
		})

		// 1. Sell full stacks until we are close to the target
		stacksToProcess := make([]data.Item, len(allKeyStacks))
		copy(stacksToProcess, allKeyStacks)

		for _, keyStack := range stacksToProcess {
			if keysSold >= excessCount {
				break // We've sold enough
			}

			qtyInStack, found := keyStack.FindStat(stat.Quantity, 0)
			if !found {
				continue
			}

			// If selling this entire stack still leaves us with at least 12 keys
			// Or if this stack exactly equals the remaining excess to sell
			if (totalKeys-qtyInStack.Value >= 12) || (qtyInStack.Value == excessCount-keysSold) {
				ctx.Logger.Debug(fmt.Sprintf("Selling full stack of %d keys from %v", qtyInStack.Value, keyStack.Position))
				SellItemFullStack(keyStack)
				keysSold += qtyInStack.Value
				totalKeys -= qtyInStack.Value     // Update total keys count
				ctx.RefreshGameData()             // Refresh after selling a full stack
				utils.PingSleep(utils.Light, 200) // Light operation: Short delay for UI update
			}
		}

		// Re-evaluate total keys after selling full stacks
		ctx.RefreshGameData()
		totalKeys = 0
		allKeyStacks = []data.Item{} // Clear and re-populate allKeyStacks
		for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if itm.Name == item.Key {
				if qty, found := itm.FindStat(stat.Quantity, 0); found {
					allKeyStacks = append(allKeyStacks, itm)
					totalKeys += qty.Value
				}
			}
		}

		// 2. If there's still excess, sell individual keys from one of the remaining stacks
		if totalKeys > 12 {
			excessCount = totalKeys - 12 // Recalculate excess after full stack sales
			ctx.Logger.Info(fmt.Sprintf("Still have %d excess keys. Selling individually from a remaining stack.", excessCount))

			// Find *any* remaining key stack to sell from
			var remainingKeyStack data.Item
			for _, itm := range allKeyStacks {
				if itm.Name == item.Key {
					remainingKeyStack = itm
					break
				}
			}

			if remainingKeyStack.Name != "" { // Check if a stack was found
				for i := 0; i < excessCount; i++ {
					SellItem(remainingKeyStack)
					keysSold++
					ctx.RefreshGameData()
					utils.PingSleep(utils.Light, 100) // Light operation: Individual sell delay
				}
			} else {
				ctx.Logger.Warn("No remaining key stacks found to sell individual keys from, despite excess reported.")
			}
		}

		ctx.Logger.Info(fmt.Sprintf("Finished selling excess keys. Keys sold: %d. Estimated remaining: %d", keysSold, totalKeys-keysSold))
	} else {
		ctx.Logger.Debug("No excess keys to sell (12 or less).")
	}
	// --- END OPTIMIZED LOGIC ---

	// Check if we should drop items instead of selling
	currentGold := ctx.Data.PlayerUnit.TotalPlayerGold()
	minGoldToDrop := ctx.CharacterCfg.Vendor.MinGoldToDrop
	currentAct := ctx.Data.PlayerUnit.Area.Act()
	alwaysDropAct := ctx.CharacterCfg.Vendor.AlwaysDropAct

	// Determine if we should drop based on act or gold threshold
	shouldDrop := false
	dropNearStash := false
	dropReason := ""

	// If AlwaysDropAct is configured AND minGoldToDrop threshold is met, always drop in the configured act
	if alwaysDropAct > 0 && alwaysDropAct <= 5 && minGoldToDrop > 0 && currentGold >= minGoldToDrop {
		shouldDrop = true
		dropNearStash = true
		dropReason = fmt.Sprintf("Gold (%d) >= MinGoldToDrop (%d) AND AlwaysDropAct is set to Act %d - dropping items near stash in Act %d", currentGold, minGoldToDrop, alwaysDropAct, alwaysDropAct)
	} else if alwaysDropAct > 0 && alwaysDropAct <= 5 && currentAct == alwaysDropAct {
		// If AlwaysDropAct is configured and we're already in that act, drop near stash
		shouldDrop = true
		dropNearStash = true
		dropReason = fmt.Sprintf("AlwaysDropAct is set to Act %d - dropping items near stash", alwaysDropAct)
	} else if minGoldToDrop > 0 && currentGold >= minGoldToDrop {
		// If only minGoldToDrop threshold is met (and AlwaysDropAct is not configured or we're not in that act)
		shouldDrop = true
		dropReason = fmt.Sprintf("Gold (%d) >= MinGoldToDrop (%d) - dropping items", currentGold, minGoldToDrop)
	}

	if shouldDrop {
		// Ensure we're in town before dropping items
		if !ctx.Data.PlayerUnit.Area.IsTown() {
			ctx.Logger.Warn("Cannot drop items outside of town, selling instead")
			shouldDrop = false
		} else {
			ctx.Logger.Info(fmt.Sprintf("Processing items: %s", dropReason))
		}
	}

	// Process other junk items - drop or sell based on configuration
	itemsToProcess := ItemsToBeSold(lockConfig...)
	if shouldDrop {
		if dropNearStash {
			// Drop items near stash in the configured act
			dropItemsNearStash(itemsToProcess, alwaysDropAct)
		} else {
			// Drop all items at once, keeping inventory open
			dropItems(itemsToProcess)
		}
	} else {
		// Sell items normally
		for _, i := range itemsToProcess {
			SellItem(i)
		}
	}
}

// closeAllMenus closes all open menus by pressing ESC
func closeAllMenus() error {
	ctx := context.Get()
	attempts := 0
	for ctx.Data.OpenMenus.IsMenuOpen() {
		ctx.PauseIfNotPriority()
		ctx.RefreshGameData()
		if attempts > 10 {
			return errors.New("failed closing game menu")
		}
		ctx.HID.PressKey(win.VK_ESCAPE)
		utils.Sleep(200)
		attempts++
	}
	return nil
}

// dropItems handles dropping multiple items, keeping inventory open during the process
func dropItems(items []data.Item) {
	if len(items) == 0 {
		return
	}

	ctx := context.Get()
	ctx.SetLastAction("dropItems")

	// Close any open menus first
	_ = closeAllMenus()
	utils.PingSleep(utils.Medium, 170) // Medium operation: Wait for menus to close

	// Open inventory once
	ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
	utils.PingSleep(utils.Medium, 300) // Medium operation: Wait for inventory to open

	// Refresh to get updated item positions
	ctx.RefreshGameData()

	// Drop all items while keeping inventory open
	for _, i := range items {
		// Refresh item data to get current position (items may shift after previous drops)
		ctx.RefreshGameData()
		var currentItem data.Item
		var found bool
		for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if it.UnitID == i.UnitID {
				currentItem = it
				found = true
				break
			}
		}

		if !found {
			ctx.Logger.Debug(fmt.Sprintf("Item %s (UnitID: %d) not found in inventory, skipping", i.Name, i.UnitID))
			continue
		}

		screenPos := ui.GetScreenCoordsForItem(currentItem)
		ctx.HID.MovePointer(screenPos.X, screenPos.Y)
		utils.PingSleep(utils.Medium, 100) // Medium operation: Position pointer on item
		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
		utils.PingSleep(utils.Medium, 200) // Medium operation: Wait for item to drop

		// Verify item was dropped
		ctx.RefreshGameData()
		stillInInventory := false
		for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if it.UnitID == i.UnitID {
				stillInInventory = true
				ctx.Logger.Warn(fmt.Sprintf("Failed to drop item %s (UnitID: %d), still in inventory. Inventory might be full or area restricted.", i.Name, i.UnitID))
				break
			}
		}
		if !stillInInventory {
			ctx.Logger.Debug(fmt.Sprintf("Successfully dropped item %s (UnitID: %d).", i.Name, i.UnitID))
		}
	}

	// Close inventory after dropping all items
	_ = closeAllMenus()
	utils.PingSleep(utils.Medium, 170) // Medium operation: Clean up UI
}

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

// dropItemsNearStash drops items on the ground near the stash in the specified act
func dropItemsNearStash(items []data.Item, targetAct int) {
	if len(items) == 0 {
		return
	}

	ctx := context.Get()
	ctx.SetLastAction("dropItemsNearStash")

	currentArea := ctx.Data.PlayerUnit.Area

	// Verify we're in the target act's town
	// Note: VendorRefill should have moved us here already, but double-check
	if !currentArea.IsTown() || currentArea.Act() != targetAct {
		ctx.Logger.Warn(fmt.Sprintf("Not in Act %d town (current: %s), dropping items at current position. VendorRefill should have moved us here.", targetAct, currentArea.Area().Name))
		// Continue to drop at current position as fallback
	}

	// Find stash position - try to find Bank object first
	ctx.RefreshGameData()
	var stashPos data.Position
	bank, found := ctx.Data.Objects.FindOne(object.Bank)
	if found {
		stashPos = bank.Position
		ctx.Logger.Info(fmt.Sprintf("Found stash at position X:%d Y:%d in Act %d", stashPos.X, stashPos.Y, targetAct))
		
		// Move near stash using pathfinder - similar to action.MoveToCoords but without import cycle
		if ctx.PathFinder != nil {
			// Move to stash position in a loop until we're close enough (distance <= 6, like in drop.go)
			maxAttempts := 10
			targetDistance := 6
			
			for attempt := 0; attempt < maxAttempts; attempt++ {
				ctx.RefreshGameData()
				currentDistance := ctx.PathFinder.DistanceFromMe(stashPos)
				
				if currentDistance <= targetDistance {
					ctx.Logger.Debug(fmt.Sprintf("Close enough to stash (distance: %d)", currentDistance))
					break
				}
				
				ctx.Logger.Debug(fmt.Sprintf("Moving to stash (attempt %d/%d, current distance: %d)", attempt+1, maxAttempts, currentDistance))
				
				// Get path to stash position
				path, pathDistance, pathFound := ctx.PathFinder.GetPath(stashPos)
				if !pathFound || pathDistance == 0 {
					ctx.Logger.Warn("Could not find path to stash, trying direct click")
					// Fallback: try direct click
					screenX, screenY := ctx.PathFinder.GameCoordsToScreenCords(stashPos.X, stashPos.Y)
					ctx.HID.Click(game.LeftButton, screenX, screenY)
					utils.PingSleep(utils.Medium, 500)
					break
				}
				
				// Move through the path - use a reasonable walk duration
				walkDuration := 2 * time.Second
				if ctx.Data.CanTeleport() {
					walkDuration = 500 * time.Millisecond
				}
				ctx.PathFinder.MoveThroughPath(path, walkDuration)
				utils.PingSleep(utils.Medium, 300)
			}
			
			// Final check
			ctx.RefreshGameData()
			finalDistance := ctx.PathFinder.DistanceFromMe(stashPos)
			if finalDistance > targetDistance {
				ctx.Logger.Warn(fmt.Sprintf("Still far from stash after movement (distance: %d), continuing anyway", finalDistance))
			} else {
				ctx.Logger.Debug(fmt.Sprintf("Successfully moved near stash (final distance: %d)", finalDistance))
			}
		}
	} else {
		ctx.Logger.Debug(fmt.Sprintf("Stash object not found in Act %d, dropping items at current position", targetAct))
	}

	// Close any open menus
	_ = closeAllMenus()
	utils.PingSleep(utils.Medium, 170)

	// Open inventory once
	ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
	utils.PingSleep(utils.Medium, 300)
	ctx.RefreshGameData()

	// Drop all items while keeping inventory open
	for _, i := range items {
		// Refresh item data to get current position (items may shift after previous drops)
		ctx.RefreshGameData()
		var currentItem data.Item
		var found bool
		for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if it.UnitID == i.UnitID {
				currentItem = it
				found = true
				break
			}
		}

		if !found {
			ctx.Logger.Debug(fmt.Sprintf("Item %s (UnitID: %d) not found in inventory, skipping", i.Name, i.UnitID))
			continue
		}

		screenPos := ui.GetScreenCoordsForItem(currentItem)
		ctx.HID.MovePointer(screenPos.X, screenPos.Y)
		utils.PingSleep(utils.Medium, 100)
		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
		utils.PingSleep(utils.Medium, 200)

		// Verify item was dropped
		ctx.RefreshGameData()
		stillInInventory := false
		for _, it := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if it.UnitID == i.UnitID {
				stillInInventory = true
				ctx.Logger.Warn(fmt.Sprintf("Failed to drop item %s (UnitID: %d), still in inventory", i.Name, i.UnitID))
				break
			}
		}
		if !stillInInventory {
			ctx.Logger.Debug(fmt.Sprintf("Successfully dropped item %s (UnitID: %d) near stash in Act %d", i.Name, i.UnitID, targetAct))
		}
	}

	// Close inventory after dropping all items
	_ = closeAllMenus()
	utils.PingSleep(utils.Medium, 170)
	
	// Note: Return to original area is handled in VendorRefill after SellJunk completes
}

// SellItem sells a single item by Control-Clicking it.
func SellItem(i data.Item) {
	ctx := context.Get()
	screenPos := ui.GetScreenCoordsForItem(i)

	ctx.Logger.Debug(fmt.Sprintf("Attempting to sell single item %s at screen coords X:%d Y:%d", i.Desc().Name, screenPos.X, screenPos.Y))

	utils.PingSleep(utils.Light, 200) // Light operation: Pre-click delay
	ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
	utils.PingSleep(utils.Light, 200) // Light operation: Post-click delay
	ctx.Logger.Debug(fmt.Sprintf("Item %s [%s] sold", i.Desc().Name, i.Quality.ToString()))
}

// SellItemFullStack sells an entire stack of items by Ctrl-Clicking it.
func SellItemFullStack(i data.Item) {
	ctx := context.Get()
	screenPos := ui.GetScreenCoordsForItem(i)

	ctx.Logger.Debug(fmt.Sprintf("Attempting to sell full stack of item %s at screen coords X:%d Y:%d", i.Desc().Name, screenPos.X, screenPos.Y))

	utils.PingSleep(utils.Light, 200) // Light operation: Pre-click delay for stack sell
	ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
	utils.PingSleep(utils.Medium, 500) // Medium operation: Post-click delay for stack sell (longer for confirmation)
	ctx.Logger.Debug(fmt.Sprintf("Full stack of %s [%s] sold", i.Desc().Name, i.Quality.ToString()))
}

func BuyItem(i data.Item, quantity int) {
	ctx := context.Get()
	screenPos := ui.GetScreenCoordsForItem(i)

	utils.PingSleep(utils.Medium, 250) // Medium operation: Pre-buy delay
	for k := 0; k < quantity; k++ {
		ctx.HID.Click(game.RightButton, screenPos.X, screenPos.Y)
		utils.PingSleep(utils.Medium, 600) // Medium operation: Wait for purchase to process
		ctx.Logger.Debug(fmt.Sprintf("Purchased %s [X:%d Y:%d]", i.Desc().Name, i.Position.X, i.Position.Y))
	}
}

// buyFullStack is for buying full stacks of items from a vendor (e.g., potions, scrolls, keys)
// For keys, currentKeysInInventory determines if a special double-click behavior is needed.
func buyFullStack(i data.Item, currentKeysInInventory int) {
	ctx := context.Get()
	screenPos := ui.GetScreenCoordsForItem(i)

	ctx.Logger.Debug(fmt.Sprintf("Attempting to buy full stack of %s from vendor at screen coords X:%d Y:%d", i.Desc().Name, screenPos.X, screenPos.Y))

	// First click: Standard Shift + Right Click for buying a stack from a vendor.
	// As per user's observation:
	// - If 0 keys: this buys 1 key.
	// - If >0 keys: this fills the current stack.
	ctx.HID.ClickWithModifier(game.RightButton, screenPos.X, screenPos.Y, game.ShiftKey)
	utils.PingSleep(utils.Light, 200) // Light operation: Wait for first purchase

	// Special handling for keys: only perform a second click if starting from 0 keys.
	if i.Name == item.Key {
		if currentKeysInInventory == 0 {
			// As per user: if 0 keys, first click buys 1, second click fills the stack.
			ctx.Logger.Debug("Initial keys were 0. Performing second Shift+Right Click to fill key stack.")
			ctx.HID.ClickWithModifier(game.RightButton, screenPos.X, screenPos.Y, game.ShiftKey)
			utils.PingSleep(utils.Light, 200) // Light operation: Wait for second purchase
		} else {
			// As per user: if > 0 keys, the first click should have already filled the stack.
			// No second click is needed to avoid buying an unnecessary extra key/stack.
			ctx.Logger.Debug("Initial keys were > 0. Single Shift+Right Click should have filled stack. No second click needed.")
		}
	}

	ctx.Logger.Debug(fmt.Sprintf("Finished full stack purchase attempt for %s", i.Desc().Name))
}

func ItemsToBeSold(lockConfig ...[][]int) (items []data.Item) {
	ctx := context.Get()
	_, portalTomeFound := ctx.Data.Inventory.Find(item.TomeOfTownPortal, item.LocationInventory)
	healingPotionCountToKeep := ctx.Data.ConfiguredInventoryPotionCount(data.HealingPotion)
	manaPotionCountToKeep := ctx.Data.ConfiguredInventoryPotionCount(data.ManaPotion)
	rejuvPotionCountToKeep := ctx.Data.ConfiguredInventoryPotionCount(data.RejuvenationPotion)

	var currentLockConfig [][]int
	if len(lockConfig) > 0 {
		currentLockConfig = lockConfig[0]
	} else {
		currentLockConfig = ctx.CharacterCfg.Inventory.InventoryLock
	}

	// Count ALL non-NIP jewels (stash + inventory) to determine how many we can keep
	totalNonNIPJewels := 0

	// Count in stash
	for _, stashed := range ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash) {
		if string(stashed.Name) == "Jewel" {
			if _, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(stashed); res != nip.RuleResultFullMatch {
				totalNonNIPJewels++
			}
		}
	}

	// Count in inventory
	for _, invItem := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if string(invItem.Name) == "Jewel" {
			if _, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(invItem); res != nip.RuleResultFullMatch {
				totalNonNIPJewels++
			}
		}
	}

	ctx.Logger.Debug(fmt.Sprintf("Total non-NIP jewels (stash + inventory): %d, Configured limit: %d",
		totalNonNIPJewels, ctx.CharacterCfg.CubeRecipes.JewelsToKeep))

	// Determine whether any jewel-using recipes are enabled
	maxJewelsToKeep := ctx.CharacterCfg.CubeRecipes.JewelsToKeep
	craftingEnabled := false
	for _, r := range ctx.CharacterCfg.CubeRecipes.EnabledRecipes {
		if strings.HasPrefix(r, "Caster ") ||
			strings.HasPrefix(r, "Blood ") ||
			strings.HasPrefix(r, "Safety ") ||
			strings.HasPrefix(r, "Hitpower ") {
			craftingEnabled = true
			break
		}
	}

	// Track how many jewels we've decided to keep so far (starting with those in stash)
	jewelsKeptCount := totalNonNIPJewels
	// Now subtract inventory jewels as we'll re-evaluate them below
	for _, invItem := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if string(invItem.Name) == "Jewel" {
			if _, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(invItem); res != nip.RuleResultFullMatch {
				jewelsKeptCount-- // We'll re-count them as we process
			}
		}
	}

	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		// Check if the item is in a locked slot, and if so, skip it.
		if len(currentLockConfig) > itm.Position.Y && len(currentLockConfig[itm.Position.Y]) > itm.Position.X {
			if currentLockConfig[itm.Position.Y][itm.Position.X] == 0 {
				continue
			}
		}

		isQuestItem := slices.Contains(questItems, itm.Name)
		if itm.IsFromQuest() || isQuestItem {
			continue
		}

		if itm.Name == item.TomeOfTownPortal || itm.Name == item.TomeOfIdentify || itm.Name == item.Key || itm.Name == "WirtsLeg" {
			continue
		}

		//Don't sell scroll of town portal if tome isn't found
		if !portalTomeFound && itm.Name == item.ScrollOfTownPortal {
			continue
		}

		if itm.IsRuneword {
			continue
		}

		if _, result := ctx.CharacterCfg.Runtime.Rules.EvaluateAllIgnoreTiers(itm); result == nip.RuleResultFullMatch && !itm.IsPotion() {
			continue
		}

		// Handle jewels: keep up to the configured limit of non-NIP jewels
		if craftingEnabled && string(itm.Name) == "Jewel" {
			// Only consider jewels that are not covered by a NIP rule
			if _, res := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(itm); res != nip.RuleResultFullMatch {
				if jewelsKeptCount < maxJewelsToKeep {
					jewelsKeptCount++ // Keep this jewel
					ctx.Logger.Debug(fmt.Sprintf("Keeping jewel #%d (under limit of %d)", jewelsKeptCount, maxJewelsToKeep))
					continue
				} else {
					ctx.Logger.Debug(fmt.Sprintf("Selling jewel - already at limit (%d/%d)", jewelsKeptCount, maxJewelsToKeep))
					// This jewel exceeds the limit, so it will be added to items to sell below
				}
			}
		}

		if itm.IsHealingPotion() {
			if healingPotionCountToKeep > 0 {
				healingPotionCountToKeep--
				continue
			}
		}

		if itm.IsManaPotion() {
			if manaPotionCountToKeep > 0 {
				manaPotionCountToKeep--
				continue
			}
		}

		if itm.IsRejuvPotion() {
			if rejuvPotionCountToKeep > 0 {
				rejuvPotionCountToKeep--
				continue
			}
		}

		if itm.Name == "StaminaPotion" && ctx.HealthManager.ShouldKeepStaminaPot() {
			continue
		}

		items = append(items, itm)
	}

	return
}
