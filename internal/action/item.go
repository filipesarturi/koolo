package action

import (
	"fmt"
	"slices"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/nip"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/pather"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
)

func doesExceedQuantity(rule nip.Rule) bool {
	ctx := context.Get()
	ctx.SetLastAction("doesExceedQuantity")

	stashItems := ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)

	maxQuantity := rule.MaxQuantity()
	if maxQuantity == 0 {
		return false
	}

	if maxQuantity == 0 {
		return false
	}

	matchedItemsInStash := 0

	for _, stashItem := range stashItems {
		res, _ := rule.Evaluate(stashItem)
		if res == nip.RuleResultFullMatch {
			matchedItemsInStash += 1
		}
	}

	return matchedItemsInStash >= maxQuantity
}

func DropMouseItem() {
	ctx := context.Get()
	ctx.SetLastAction("DropMouseItem")

	if len(ctx.Data.Inventory.ByLocation(item.LocationCursor)) > 0 {
		utils.Sleep(1000)
		ctx.HID.Click(game.LeftButton, 500, 500)
		utils.Sleep(1000)
	}
}

func DropInventoryItem(i data.Item) error {
	ctx := context.Get()
	ctx.SetLastAction("DropInventoryItem")

	// Never drop HoradricCube
	if i.Name == "HoradricCube" {
		ctx.Logger.Debug(fmt.Sprintf("Skipping drop for protected item: %s", i.Name))
		return nil
	}

	// Protect TomeOfTownPortal and TomeOfIdentify only if Cows run is active
	if i.Name == item.TomeOfTownPortal || i.Name == item.TomeOfIdentify {
		if ctx.CharacterCfg != nil && slices.Contains(ctx.CharacterCfg.Game.Runs, config.CowsRun) {
			ctx.Logger.Debug(fmt.Sprintf("Skipping drop for %s (Cows run active): %s", i.Name, i.Name))
			return nil
		}
	}

	// Protect Wirt's Leg only if Cows run is active
	if i.Name == "WirtsLeg" {
		if ctx.CharacterCfg != nil && slices.Contains(ctx.CharacterCfg.Game.Runs, config.CowsRun) {
			ctx.Logger.Debug(fmt.Sprintf("Skipping drop for Wirt's Leg (Cows run active): %s", i.Name))
			return nil
		}
	}

	closeAttempts := 0

	// Check if any other menu is open, except the inventory
	for ctx.Data.OpenMenus.IsMenuOpen() {

		// Press escape to close it
		ctx.HID.PressKey(0x1B) // ESC
		utils.Sleep(500)
		closeAttempts++

		if closeAttempts >= 5 {
			return fmt.Errorf("failed to close open menu after 5 attempts")
		}
	}

	if i.Location.LocationType == item.LocationInventory {

		// Check if the inventory is open, if not open it
		if !ctx.Data.OpenMenus.Inventory {
			ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
		}

		// Wait a second
		utils.Sleep(1000)

		screenPos := ui.GetScreenCoordsForItem(i)
		ctx.HID.MovePointer(screenPos.X, screenPos.Y)
		utils.Sleep(250)
		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
		utils.Sleep(500)

		// Close the inventory if its still open, which should be at this point
		if ctx.Data.OpenMenus.Inventory {
			ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
		}
	}

	return nil
}
func IsInLockedInventorySlot(itm data.Item) bool {
	// Check if item is in inventory
	if itm.Location.LocationType != item.LocationInventory {
		return false
	}

	// Get the lock configuration from character config
	ctx := context.Get()
	lockConfig := ctx.CharacterCfg.Inventory.InventoryLock
	if len(lockConfig) == 0 {
		return false
	}

	// Calculate row and column in inventory
	row := itm.Position.Y
	col := itm.Position.X

	// Check if position is within bounds
	if row >= len(lockConfig) || col >= len(lockConfig[0]) {
		return false
	}

	// 0 means locked, 1 means unlocked
	return lockConfig[row][col] == 0
}

func DrinkAllPotionsInInventory() {
	ctx := context.Get()
	ctx.SetLastStep("DrinkPotionsInInventory")

	step.OpenInventory()

	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if i.IsPotion() {
			if ctx.CharacterCfg.Inventory.InventoryLock[i.Position.Y][i.Position.X] == 0 {
				continue
			}

			screenPos := ui.GetScreenCoordsForItem(i)
			utils.Sleep(100)
			ctx.HID.Click(game.RightButton, screenPos.X, screenPos.Y)
			utils.Sleep(200)
		}
	}

	step.CloseAllMenus()
}

// hasKeysInInventory checks if there are any keys in the inventory
// Returns true immediately when the first key is found, without iterating through the entire inventory
func hasKeysInInventory() bool {
	ctx := context.Get()
	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if itm.Name == item.Key {
			if qty, found := itm.FindStat(stat.Quantity, 0); found {
				if qty.Value > 0 {
					return true // Return immediately when keys are found
				}
			} else {
				// If no quantity stat, assume it's a key (stack of 1)
				return true
			}
		}
	}
	return false
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

// WaitForItemsAfterContainerOpen waits for items to drop from opened containers
// It checks periodically if items appeared on the ground near the container position
// Returns as soon as items are detected or timeout is reached
// Different container types have different maximum wait times based on their animation duration
func WaitForItemsAfterContainerOpen(containerPos data.Position, obj data.Object) {
	ctx := context.Get()
	ctx.SetLastAction("WaitForItemsAfterContainerOpen")

	const (
		checkInterval   = 50 * time.Millisecond  // Check interval - small for quick detection
		itemCheckRadius = 3                      // Radius to check for items (tiles)
		initialDelay    = 50 * time.Millisecond  // Initial delay before first check
	)

	// Determine maximum wait time based on container type
	var maxWaitTime time.Duration
	isStash := obj.Name == object.Bank
	
	if isStash {
		// Stashes have longer animations
		maxWaitTime = 3000 * time.Millisecond
	} else if obj.IsSuperChest() {
		// Super chests may have longer animations
		maxWaitTime = 2000 * time.Millisecond
	} else if obj.IsChest() {
		// Regular chests
		maxWaitTime = 1500 * time.Millisecond
	} else {
		// Other containers (barrels, urns, corpses, etc.)
		maxWaitTime = 2000 * time.Millisecond
	}

	// Small initial delay to allow animation to start
	time.Sleep(initialDelay)

	startTime := time.Now()

	// Check periodically for items
	for time.Since(startTime) < maxWaitTime {
		ctx.RefreshGameData()

		// Get items on the ground near the container position
		itemsNearby := getItemsNearPosition(containerPos, itemCheckRadius)

		if len(itemsNearby) > 0 {
			// Items detected, we can return immediately
			ctx.Logger.Debug("Items detected after container open",
				"container", obj.Name,
				"itemsCount", len(itemsNearby),
				"waitTime", time.Since(startTime),
			)
			return
		}

		// Wait before next check
		time.Sleep(checkInterval)
	}

	// Timeout reached - log if in debug mode
	ctx.Logger.Debug("Timeout reached waiting for items after container open",
		"container", obj.Name,
		"maxWaitTime", maxWaitTime,
	)
}

// getItemsNearPosition returns items on the ground near a position within the specified radius
func getItemsNearPosition(pos data.Position, radius int) []data.Item {
	ctx := context.Get()
	var items []data.Item

	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationGround) {
		distance := pather.DistanceFromPoint(itm.Position, pos)
		if distance <= radius {
			items = append(items, itm)
		}
	}

	return items
}
