package action

import (
	"fmt"
	"slices"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
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
		ctx.HID.Click(game.LeftButton, 500, 500)
		WaitForCursorEmpty(2000)
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
		WaitForCondition(func() bool {
			ctx.RefreshGameData()
			return !ctx.Data.OpenMenus.IsMenuOpen()
		}, 1000, 50)
		closeAttempts++

		if closeAttempts >= 5 {
			return fmt.Errorf("failed to close open menu after 5 attempts")
		}
	}

	if i.Location.LocationType == item.LocationInventory {
		// Check if the inventory is open, if not open it
		if !ctx.Data.OpenMenus.Inventory {
			ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
			WaitForMenuOpen(MenuInventory, 1500)
		}

		screenPos := ui.GetScreenCoordsForItem(i)
		ctx.HID.MovePointer(screenPos.X, screenPos.Y)
		utils.Sleep(100)
		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)

		// Wait for item to be dropped (no longer in inventory)
		WaitForItemNotInLocation(i.UnitID, item.LocationInventory, 1500)

		// Close the inventory if its still open
		ctx.RefreshGameData()
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

// getLockedKeysCount returns the count of keys in locked inventory slots
func getLockedKeysCount() int {
	ctx := context.Get()
	lockedKeys := 0

	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if itm.Name == item.Key {
			if IsInLockedInventorySlot(itm) {
				if qty, found := itm.FindStat(stat.Quantity, 0); found {
					lockedKeys += qty.Value
				} else {
					lockedKeys++ // If no quantity stat, assume stack of 1
				}
			}
		}
	}
	return lockedKeys
}

// WaitForItemsAfterContainerOpen waits for items to drop from opened containers
// It checks periodically if NEW items appeared on the ground near the container position
// Returns as soon as new items are detected or timeout is reached
// Different container types have different maximum wait times based on their animation duration
func WaitForItemsAfterContainerOpen(containerPos data.Position, obj data.Object) {
	ctx := context.Get()
	ctx.SetLastAction("WaitForItemsAfterContainerOpen")

	const (
		checkInterval   = 50 * time.Millisecond  // Check interval - small for quick detection
		itemCheckRadius = 5                      // Radius to check for items (tiles) - increased from 3
		initialDelay    = 50 * time.Millisecond  // Initial delay before first check
	)

	// Capture initial items BEFORE waiting - these existed before container was opened
	initialItems := getItemIDsNearPosition(containerPos, itemCheckRadius)

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

	// Check periodically for NEW items (items that weren't there before)
	for time.Since(startTime) < maxWaitTime {
		ctx.RefreshGameData()

		// Get current items and check if any are NEW
		currentItems := getItemIDsNearPosition(containerPos, itemCheckRadius)

		if hasNewItems(initialItems, currentItems) {
			newCount := countNewItems(initialItems, currentItems)
			ctx.Logger.Debug("New items detected after container open",
				"container", obj.Name,
				"newItemsCount", newCount,
				"totalItemsNearby", len(currentItems),
				"waitTime", time.Since(startTime),
			)
			return
		}

		// Wait before next check
		time.Sleep(checkInterval)
	}

	// Timeout reached - log if in debug mode
	currentItems := getItemIDsNearPosition(containerPos, itemCheckRadius)
	newCount := countNewItems(initialItems, currentItems)
	ctx.Logger.Debug("Timeout reached waiting for items after container open",
		"container", obj.Name,
		"maxWaitTime", maxWaitTime,
		"newItemsFound", newCount,
		"totalItemsNearby", len(currentItems),
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

// getItemIDsNearPosition returns a map of item IDs on the ground near a position
// Used for tracking which items existed before opening a container
func getItemIDsNearPosition(pos data.Position, radius int) map[data.UnitID]bool {
	ctx := context.Get()
	itemIDs := make(map[data.UnitID]bool)

	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationGround) {
		distance := pather.DistanceFromPoint(itm.Position, pos)
		if distance <= radius {
			itemIDs[itm.UnitID] = true
		}
	}

	return itemIDs
}

// hasNewItems checks if there are any new items in current that weren't in initial
func hasNewItems(initial, current map[data.UnitID]bool) bool {
	for id := range current {
		if !initial[id] {
			return true // New item found
		}
	}
	return false
}

// countNewItems returns the number of new items in current that weren't in initial
func countNewItems(initial, current map[data.UnitID]bool) int {
	count := 0
	for id := range current {
		if !initial[id] {
			count++
		}
	}
	return count
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
// Each container has its own timeout based on type. Returns when ALL containers have either:
// - Dropped items (detected new items nearby), OR
// - Reached their individual timeout
func WaitForItemsAfterMultipleContainers(containers []containerPosition) {
	ctx := context.Get()
	ctx.SetLastAction("WaitForItemsAfterMultipleContainers")

	if len(containers) == 0 {
		return
	}

	const (
		checkInterval   = 50 * time.Millisecond
		itemCheckRadius = 5
		initialDelay    = 50 * time.Millisecond
	)

	// Capture initial items and timeout for each container
	type containerState struct {
		initialItems map[data.UnitID]bool
		timeout      time.Duration
		completed    bool // true if items detected OR timeout reached
		droppedItems bool // true if items were detected
	}

	states := make([]containerState, len(containers))
	for i, c := range containers {
		states[i] = containerState{
			initialItems: getItemIDsNearPosition(c.pos, itemCheckRadius),
			timeout:      getMaxWaitTimeForContainer(c.obj),
			completed:    false,
			droppedItems: false,
		}
	}

	// Small initial delay to allow animations to start
	time.Sleep(initialDelay)

	startTime := time.Now()

	// Check periodically until all containers are completed
	for {
		ctx.RefreshGameData()
		elapsed := time.Since(startTime)

		allCompleted := true
		for i, c := range containers {
			if states[i].completed {
				continue
			}

			// Check if this container's timeout has been reached
			if elapsed >= states[i].timeout {
				states[i].completed = true
				ctx.Logger.Debug("Container timeout reached without items",
					"container", c.obj.Name,
					"timeout", states[i].timeout,
				)
				continue
			}

			// Check for new items
			currentItems := getItemIDsNearPosition(c.pos, itemCheckRadius)
			if hasNewItems(states[i].initialItems, currentItems) {
				states[i].completed = true
				states[i].droppedItems = true
				newCount := countNewItems(states[i].initialItems, currentItems)
				ctx.Logger.Debug("New items detected after batch container open",
					"container", c.obj.Name,
					"newItemsCount", newCount,
					"waitTime", elapsed,
				)
				continue
			}

			allCompleted = false
		}

		if allCompleted {
			// Count results
			droppedCount := 0
			timeoutCount := 0
			for _, s := range states {
				if s.droppedItems {
					droppedCount++
				} else {
					timeoutCount++
				}
			}
			ctx.Logger.Debug("All containers completed",
				"total", len(containers),
				"droppedItems", droppedCount,
				"timedOut", timeoutCount,
				"totalWaitTime", time.Since(startTime),
			)
			return
		}

		time.Sleep(checkInterval)
	}
}

// OpenContainersInBatch opens multiple containers in batch, works with or without Telekinesis
// Opens all containers rapidly without waiting between each, then waits once for items from all
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

	// Filter containers to only process those in range (TK or close enough to click)
	// Containers outside range will be processed naturally when bot approaches during normal movement
	var containersInRange []data.Object

	for _, obj := range containers {
		distance := pather.DistanceFromPoint(playerPos, obj.Position)
		canUseTK := canUseTelekinesisForObject(obj)

		// In range if: can use TK and within TK range, OR close enough to click (15 tiles)
		if (canUseTK && distance <= telekinesisRange) || distance <= 15 {
			containersInRange = append(containersInRange, obj)
		}
	}

	// Log containers that will be skipped (outside range) for debugging
	containersOutsideRange := len(containers) - len(containersInRange)
	if containersOutsideRange > 0 {
		ctx.Logger.Debug("Skipping containers outside range - will process when bot approaches naturally",
			"skippedCount", containersOutsideRange,
			"totalContainers", len(containers),
		)
	}

	// Open all containers in range rapidly using fast interaction
	// No delays between openings - all opened as fast as possible
	openStartTime := time.Now()
	successCount := 0
	failCount := 0
	ctx.Logger.Debug("Starting rapid batch opening",
		"containersInRange", len(containersInRange),
		"containersSkipped", containersOutsideRange,
	)

	// Pre-select Telekinesis once if any container can use it (optimization)
	// This avoids selecting TK for each container individually
	tkSelected := false
	tkKb, tkFound := ctx.Data.KeyBindings.KeyBindingForSkill(skill.Telekinesis)
	if tkFound {
		for _, obj := range containersInRange {
			if canUseTelekinesisForObject(obj) {
				ctx.HID.PressKeyBinding(tkKb)
				utils.Sleep(20) // Small delay to ensure TK is selected
				tkSelected = true
				break
			}
		}
	}

	// Open all containers rapidly - no delays between each
	for i, obj := range containersInRange {
		ctx.PauseIfNotPriority()

		itemOpenStart := time.Now()
		if step.InteractObjectFastInBatch(obj, tkSelected) {
			successCount++
			openedContainers = append(openedContainers, containerPosition{
				pos: obj.Position,
				obj: obj,
			})
			ctx.Logger.Debug("Container opened in batch",
				"container", obj.Name,
				"index", i+1,
				"total", len(containersInRange),
				"duration", time.Since(itemOpenStart),
			)
		} else {
			failCount++
			ctx.Logger.Debug("Failed to open container in batch",
				"container", obj.Name,
				"index", i+1,
				"total", len(containersInRange),
			)
		}
	}

	if len(containersInRange) > 0 {
		ctx.Logger.Debug("Completed rapid batch opening",
			"total", len(containersInRange),
			"success", successCount,
			"failed", failCount,
			"totalDuration", time.Since(openStartTime),
			"avgDurationPerContainer", time.Since(openStartTime)/time.Duration(len(containersInRange)),
		)
	}

	// If any containers were opened, wait for items from all of them
	// This is the ONLY wait - after ALL containers have been opened rapidly
	if len(openedContainers) > 0 {
		waitStartTime := time.Now()
		ctx.Logger.Debug("All containers opened rapidly, now waiting for items from all",
			"containersCount", len(openedContainers),
			"timeSinceFirstOpen", time.Since(openStartTime),
		)
		WaitForItemsAfterMultipleContainers(openedContainers)
		ctx.Logger.Debug("Finished waiting for items from batch",
			"containersCount", len(openedContainers),
			"waitDuration", time.Since(waitStartTime),
			"totalBatchDuration", time.Since(openStartTime),
		)
	} else {
		ctx.Logger.Debug("No containers were opened in batch",
			"totalContainers", len(containers),
			"containersInRange", len(containersInRange),
		)
	}

	// Return list of successfully opened container objects
	result := make([]data.Object, len(openedContainers))
	for i, c := range openedContainers {
		result[i] = c.obj
	}
	return result
}

// openContainerIndividuallyFast moves to a container if needed and opens it quickly
func openContainerIndividuallyFast(obj data.Object, openedContainers *[]containerPosition) {
	ctx := context.Get()

	// Move to container
	chestDistance := ctx.PathFinder.DistanceFromMe(obj.Position)
	canUseTK := canUseTelekinesisForObject(obj)
	telekinesisRange := getTelekinesisRange()

	if !canUseTK || chestDistance > telekinesisRange {
		if err := MoveToCoords(obj.Position); err != nil {
			ctx.Logger.Debug("Failed moving to container",
				"container", obj.Name,
				"error", err,
			)
			return
		}
	}

	// Open the container quickly
	if step.InteractObjectFast(obj) {
		*openedContainers = append(*openedContainers, containerPosition{
			pos: obj.Position,
			obj: obj,
		})
	} else {
		ctx.Logger.Debug("Failed to open container individually",
			"container", obj.Name,
		)
	}
}

// openContainerIndividually opens a single container and adds it to the opened list (legacy, waits for completion)
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
