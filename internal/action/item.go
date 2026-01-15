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
	itemsNearby := getItemsNearPosition(containerPos, itemCheckRadius)
	ctx.Logger.Debug("Timeout reached waiting for items after container open",
		"container", obj.Name,
		"maxWaitTime", maxWaitTime,
		"itemsFound", len(itemsNearby),
		"containerPos", fmt.Sprintf("(%d, %d)", containerPos.X, containerPos.Y),
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

// getMaxWaitTimeForContainer returns the maximum wait time for a container type
func getMaxWaitTimeForContainer(obj data.Object) time.Duration {
	isStash := obj.Name == object.Bank
	
	if isStash {
		return 3000 * time.Millisecond
	} else if obj.IsSuperChest() {
		return 2000 * time.Millisecond
	} else if obj.IsChest() {
		return 1500 * time.Millisecond
	} else {
		return 2000 * time.Millisecond
	}
}

// containerPosition represents a container and its position
type containerPosition struct {
	pos data.Position
	obj data.Object
}

// WaitForItemsAfterMultipleContainers waits for items to drop from multiple opened containers
// It checks periodically if items appeared near any of the container positions
// Returns as soon as items are detected in all positions or timeout is reached
func WaitForItemsAfterMultipleContainers(containers []containerPosition) {
	ctx := context.Get()
	ctx.SetLastAction("WaitForItemsAfterMultipleContainers")

	if len(containers) == 0 {
		return
	}

	const (
		checkInterval   = 50 * time.Millisecond
		itemCheckRadius = 3
		initialDelay    = 50 * time.Millisecond
	)

	// Calculate maximum wait time based on the slowest container type
	var maxWaitTime time.Duration
	for _, c := range containers {
		waitTime := getMaxWaitTimeForContainer(c.obj)
		if waitTime > maxWaitTime {
			maxWaitTime = waitTime
		}
	}

	// Small initial delay to allow animations to start
	time.Sleep(initialDelay)

	startTime := time.Now()
	itemsDetected := make(map[int]bool) // Track which containers have items detected

	// Check periodically for items
	for time.Since(startTime) < maxWaitTime {
		ctx.RefreshGameData()

		allDetected := true
		for i, c := range containers {
			if itemsDetected[i] {
				continue // Already detected items for this container
			}

			itemsNearby := getItemsNearPosition(c.pos, itemCheckRadius)
			if len(itemsNearby) > 0 {
				itemsDetected[i] = true
				ctx.Logger.Debug("Items detected after batch container open",
					"container", c.obj.Name,
					"itemsCount", len(itemsNearby),
					"waitTime", time.Since(startTime),
				)
			} else {
				allDetected = false
			}
		}

		// If items detected for all containers, we can return early
		if allDetected && len(itemsDetected) == len(containers) {
			ctx.Logger.Debug("All containers in batch have items detected",
				"containersCount", len(containers),
				"waitTime", time.Since(startTime),
			)
			return
		}

		// Wait before next check
		time.Sleep(checkInterval)
	}

	// Log which containers didn't get items (if any)
	undetected := len(containers) - len(itemsDetected)
	if undetected > 0 {
		ctx.Logger.Debug("Timeout reached waiting for items after batch container open",
			"containersCount", len(containers),
			"undetectedCount", undetected,
			"maxWaitTime", maxWaitTime,
		)
	}
}

// OpenContainersInBatch opens multiple containers in batch, works with or without Telekinesis
// Groups containers by proximity and opens them rapidly without waiting between each
// Then waits once for items from all containers
func OpenContainersInBatch(containers []data.Object) []data.Object {
	ctx := context.Get()
	ctx.SetLastAction("OpenContainersInBatch")

	if len(containers) == 0 {
		return nil
	}

	telekinesisRange := getTelekinesisRange()
	playerPos := ctx.Data.PlayerUnit.Position
	openedContainers := make([]containerPosition, 0)

	ctx.Logger.Debug("Starting batch container opening",
		"totalContainers", len(containers),
		"tkRange", telekinesisRange,
	)

	// Separate containers into two groups: within TK range and outside TK range
	var containersInTKRange []data.Object
	var containersOutsideTKRange []data.Object

	for _, obj := range containers {
		distance := pather.DistanceFromPoint(playerPos, obj.Position)
		canUseTK := canUseTelekinesisForObject(obj)

		if canUseTK && distance <= telekinesisRange {
			containersInTKRange = append(containersInTKRange, obj)
		} else {
			containersOutsideTKRange = append(containersOutsideTKRange, obj)
		}
	}

	// Open containers within TK range rapidly
	tkOpenStartTime := time.Now()
	tkSuccessCount := 0
	tkFailCount := 0
	ctx.Logger.Debug("Starting batch opening with Telekinesis",
		"containersInTKRange", len(containersInTKRange),
		"containersOutsideTKRange", len(containersOutsideTKRange),
		"tkRange", telekinesisRange,
	)
	for _, obj := range containersInTKRange {
		err := InteractObject(obj, func() bool {
			openedObj, found := ctx.Data.Objects.FindByID(obj.ID)
			return found && !openedObj.Selectable
		})

		if err != nil {
			tkFailCount++
			distance := pather.DistanceFromPoint(ctx.Data.PlayerUnit.Position, obj.Position)
			ctx.Logger.Debug("Failed to open container in batch (TK)",
				"container", obj.Name,
				"error", err,
				"distance", distance,
				"tkRange", telekinesisRange,
			)
			continue
		}

		tkSuccessCount++
		openedContainers = append(openedContainers, containerPosition{
			pos: obj.Position,
			obj: obj,
		})
	}
	if len(containersInTKRange) > 0 {
		ctx.Logger.Debug("Opened containers with Telekinesis in batch",
			"total", len(containersInTKRange),
			"success", tkSuccessCount,
			"failed", tkFailCount,
			"duration", time.Since(tkOpenStartTime),
		)
	}

	// Group containers outside TK range by proximity (within 10 tiles of each other)
	// and open them in batches
	const proximityRadius = 10
	processed := make(map[data.UnitID]bool)
	groupCount := 0
	nonTKOpenStartTime := time.Now()
	nonTKSuccessCount := 0
	nonTKFailCount := 0

	for _, obj := range containersOutsideTKRange {
		if processed[obj.ID] {
			continue
		}

		// Find all containers near this one
		var nearbyGroup []data.Object
		for _, other := range containersOutsideTKRange {
			if processed[other.ID] {
				continue
			}
			distance := pather.DistanceFromPoint(obj.Position, other.Position)
			if distance <= proximityRadius {
				nearbyGroup = append(nearbyGroup, other)
				processed[other.ID] = true
			}
		}

		// If multiple containers nearby, move to center and open all
		if len(nearbyGroup) > 1 {
			groupCount++
			ctx.Logger.Debug("Grouping containers for batch opening",
				"groupSize", len(nearbyGroup),
				"groupIndex", groupCount,
			)
			// Calculate center position
			centerX, centerY := 0, 0
			for _, c := range nearbyGroup {
				centerX += c.Position.X
				centerY += c.Position.Y
			}
			centerX /= len(nearbyGroup)
			centerY /= len(nearbyGroup)
			centerPos := data.Position{X: centerX, Y: centerY}

			// Move to center position
			moveStartTime := time.Now()
			if err := MoveToCoords(centerPos); err != nil {
				ctx.Logger.Debug("Failed to move to container group center",
					"error", err,
					"groupSize", len(nearbyGroup),
				)
				// Fall back to individual opening
				for _, c := range nearbyGroup {
					openContainerIndividually(c, &openedContainers)
				}
				continue
			}
			ctx.Logger.Debug("Moved to container group center",
				"groupSize", len(nearbyGroup),
				"moveDuration", time.Since(moveStartTime),
			)

			// Refresh to get updated positions
			ctx.RefreshGameData()

			// Open all containers in the group from center position
			groupTKCount := 0
			groupNonTKCount := 0
			for _, c := range nearbyGroup {
				// Re-find container to get updated data
				updatedObj, found := ctx.Data.Objects.FindByID(c.ID)
				if !found || !updatedObj.Selectable {
					continue
				}

				// Check if now within TK range from center
				newDistance := pather.DistanceFromPoint(centerPos, updatedObj.Position)
				canUseTK := canUseTelekinesisForObject(updatedObj)

				if canUseTK && newDistance <= telekinesisRange {
					// Can use TK from center
					err := InteractObject(updatedObj, func() bool {
						openedObj, found := ctx.Data.Objects.FindByID(updatedObj.ID)
						return found && !openedObj.Selectable
					})

					if err != nil {
						nonTKFailCount++
						ctx.Logger.Debug("Failed to open container in batch after moving to center",
							"container", updatedObj.Name,
							"error", err,
							"distance", newDistance,
						)
					} else {
						groupTKCount++
						nonTKSuccessCount++
						openedContainers = append(openedContainers, containerPosition{
							pos: updatedObj.Position,
							obj: updatedObj,
						})
					}
				} else {
					// Still need to move closer
					groupNonTKCount++
					openContainerIndividually(updatedObj, &openedContainers)
				}
			}
			ctx.Logger.Debug("Opened container group from center",
				"groupSize", len(nearbyGroup),
				"openedWithTK", groupTKCount,
				"openedWithoutTK", groupNonTKCount,
			)
		} else {
			// Single container, open individually
			openContainerIndividually(obj, &openedContainers)
		}
	}
	if len(containersOutsideTKRange) > 0 {
		ctx.Logger.Debug("Opened containers outside TK range",
			"total", len(containersOutsideTKRange),
			"groups", groupCount,
			"success", nonTKSuccessCount,
			"failed", nonTKFailCount,
			"duration", time.Since(nonTKOpenStartTime),
		)
	}

	// If any containers were opened, wait for items from all of them
	if len(openedContainers) > 0 {
		waitStartTime := time.Now()
		ctx.Logger.Debug("Opened containers in batch, waiting for items",
			"containersCount", len(openedContainers),
		)
		WaitForItemsAfterMultipleContainers(openedContainers)
		ctx.Logger.Debug("Finished waiting for items from batch",
			"containersCount", len(openedContainers),
			"waitDuration", time.Since(waitStartTime),
		)
	}

	// Return list of successfully opened container objects
	result := make([]data.Object, len(openedContainers))
	for i, c := range openedContainers {
		result[i] = c.obj
	}
	return result
}

// openContainerIndividually opens a single container and adds it to the opened list
func openContainerIndividually(obj data.Object, openedContainers *[]containerPosition) {
	ctx := context.Get()

	// Move to container if needed
	chestDistance := ctx.PathFinder.DistanceFromMe(obj.Position)
	canUseTK := canUseTelekinesisForObject(obj)
	telekinesisRange := getTelekinesisRange()

	if !canUseTK || chestDistance > telekinesisRange {
		moveStartTime := time.Now()
		if err := MoveToCoords(obj.Position); err != nil {
			ctx.Logger.Debug("Failed moving to container",
				"container", obj.Name,
				"error", err,
				"distance", chestDistance,
			)
			return
		}
		ctx.Logger.Debug("Moved to container for individual opening",
			"container", obj.Name,
			"distance", chestDistance,
			"moveDuration", time.Since(moveStartTime),
		)
	}

	// Open the container
	err := InteractObject(obj, func() bool {
		openedObj, found := ctx.Data.Objects.FindByID(obj.ID)
		return found && !openedObj.Selectable
	})

	if err != nil {
		ctx.Logger.Debug("Failed to open container individually",
			"container", obj.Name,
			"error", err,
			"distance", chestDistance,
			"canUseTK", canUseTK,
		)
		return
	}

	// Successfully opened
	*openedContainers = append(*openedContainers, containerPosition{
		pos: obj.Position,
		obj: obj,
	})
}

// OpenContainersInBatchWithTelekinesis opens multiple containers within telekinesis range in batch
// This is a convenience wrapper that calls OpenContainersInBatch
// Kept for backward compatibility
func OpenContainersInBatchWithTelekinesis(containers []data.Object) []data.Object {
	return OpenContainersInBatch(containers)
}
