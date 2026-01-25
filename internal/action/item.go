package action

import (
	"fmt"
	"log/slog"
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
// Returns as soon as new items are detected, container is no longer selectable, or timeout is reached
// Different container types have different maximum wait times based on their animation duration
func WaitForItemsAfterContainerOpen(containerPos data.Position, obj data.Object) {
	ctx := context.Get()
	ctx.SetLastAction("WaitForItemsAfterContainerOpen")

	const (
		checkInterval   = 40 * time.Millisecond  // Check interval - small for quick detection
		itemCheckRadius = 5                      // Radius to check for items (tiles)
		initialDelay    = 30 * time.Millisecond  // Initial delay before first check
	)

	// Capture initial items BEFORE waiting - these existed before container was opened
	initialItems := getItemIDsNearPosition(containerPos, itemCheckRadius)

	// Determine maximum wait time based on container type
	// Balanced timeouts - fast for breakables, longer for chests with animations
	var maxWaitTime time.Duration
	isStash := obj.Name == object.Bank

	if isStash {
		// Stashes have longer animations
		maxWaitTime = 2000 * time.Millisecond
	} else if obj.IsSuperChest() {
		// Super chests have longer animations, need more time
		maxWaitTime = 1200 * time.Millisecond
	} else if obj.IsChest() {
		// Regular chests
		maxWaitTime = 600 * time.Millisecond
	} else {
		// Other containers (barrels, urns, corpses, etc.) - short timeout
		// Most breakables either drop immediately or don't drop at all
		maxWaitTime = 350 * time.Millisecond
	}

	// Small initial delay to allow animation to start
	time.Sleep(initialDelay)

	startTime := time.Now()

	// Check periodically for NEW items or container state change
	for time.Since(startTime) < maxWaitTime {
		ctx.RefreshGameData()

		// Quick exit: if container is no longer selectable, it was opened successfully
		// No need to wait for items - they either dropped or the container was empty
		if updatedObj, found := ctx.Data.Objects.FindByID(obj.ID); found && !updatedObj.Selectable {
			// Container opened - check for items one more time
			currentItems := getItemIDsNearPosition(containerPos, itemCheckRadius)
			if hasNewItems(initialItems, currentItems) {
				ctx.Logger.Debug("Items detected after container opened",
					"container", obj.Name,
					"newItemsCount", countNewItems(initialItems, currentItems),
					"waitTime", time.Since(startTime),
				)
			}
			return // Container opened, no need to wait more
		}

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
		return 2000 * time.Millisecond
	} else if obj.IsSuperChest() {
		// Super chests have longer animations, need more time
		return 1200 * time.Millisecond
	} else if obj.IsChest() {
		return 600 * time.Millisecond
	} else {
		// Breakables (barrels, urns, etc.) - short timeout
		return 350 * time.Millisecond
	}
}

// containerPosition represents a container and its position
type containerPosition struct {
	pos data.Position
	obj data.Object
}

// WaitForItemsAfterMultipleContainers waits for items to drop from multiple opened containers
// Each container has its own timeout based on type. Returns when ALL containers have either:
// - Container is no longer selectable (opened successfully), OR
// - Dropped items (detected new items nearby), OR
// - Reached their individual timeout
func WaitForItemsAfterMultipleContainers(containers []containerPosition) {
	ctx := context.Get()
	ctx.SetLastAction("WaitForItemsAfterMultipleContainers")

	if len(containers) == 0 {
		return
	}

	const (
		checkInterval   = 40 * time.Millisecond
		itemCheckRadius = 5
		initialDelay    = 30 * time.Millisecond
	)

	// Capture initial items and timeout for each container
	type containerState struct {
		initialItems map[data.UnitID]bool
		timeout      time.Duration
		completed    bool // true if items detected OR timeout reached OR container opened
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

			// Quick check: if container is no longer selectable, it's opened
			if updatedObj, found := ctx.Data.Objects.FindByID(c.obj.ID); found && !updatedObj.Selectable {
				states[i].completed = true
				// Check if items dropped
				currentItems := getItemIDsNearPosition(c.pos, itemCheckRadius)
				if hasNewItems(states[i].initialItems, currentItems) {
					states[i].droppedItems = true
				}
				continue
			}

			// Check if this container's timeout has been reached
			if elapsed >= states[i].timeout {
				states[i].completed = true
				continue
			}

			// Check for new items
			currentItems := getItemIDsNearPosition(c.pos, itemCheckRadius)
			if hasNewItems(states[i].initialItems, currentItems) {
				states[i].completed = true
				states[i].droppedItems = true
				continue
			}

			allCompleted = false
		}

		if allCompleted {
			return
		}

		time.Sleep(checkInterval)
	}
}

// OpenContainersInBatch opens multiple containers in batch, works with or without Telekinesis
// Opens all containers rapidly without waiting between each, then waits once for items from all
// Containers out of range will be approached and opened individually
func OpenContainersInBatch(containers []data.Object) []data.Object {
	ctx := context.Get()
	ctx.SetLastAction(fmt.Sprintf("OpenContainers_batch%d", len(containers)))
	batchStartTime := time.Now()

	if len(containers) == 0 {
		return nil
	}

	telekinesisRange := getTelekinesisRange()
	playerPos := ctx.Data.PlayerUnit.Position
	openedContainers := make([]containerPosition, 0)

	ctx.Logger.Info("Batch container opening started",
		slog.Int("totalContainers", len(containers)),
		slog.Int("tkRange", telekinesisRange),
		slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
	)

	// Separate containers into in-range and out-of-range
	var containersInRange []data.Object
	var containersOutOfRange []data.Object

	for _, obj := range containers {
		distance := pather.DistanceFromPoint(playerPos, obj.Position)
		canUseTK := canUseTelekinesisForObject(obj)

		// In range if: can use TK and within TK range, OR close enough to click (15 tiles)
		if (canUseTK && distance <= telekinesisRange) || distance <= 15 {
			containersInRange = append(containersInRange, obj)
		} else {
			containersOutOfRange = append(containersOutOfRange, obj)
		}
	}

	// Open all containers in range rapidly using fast interaction
	successCount := 0
	failCount := 0

	// Pre-select Telekinesis once if any container can use it (optimization)
	tkSelected := false
	tkKb, tkFound := ctx.Data.KeyBindings.KeyBindingForSkill(skill.Telekinesis)
	if tkFound && len(containersInRange) > 0 {
		for _, obj := range containersInRange {
			if canUseTelekinesisForObject(obj) {
				ctx.HID.PressKeyBinding(tkKb)
				utils.Sleep(15)
				tkSelected = true
				break
			}
		}
	}

	// Open all in-range containers rapidly - no delays between each
	for _, obj := range containersInRange {
		ctx.PauseIfNotPriority()

		if step.InteractObjectFastInBatch(obj, tkSelected) {
			successCount++
			openedContainers = append(openedContainers, containerPosition{
				pos: obj.Position,
				obj: obj,
			})
		} else {
			failCount++
		}
	}

	// Wait for items from in-range containers before moving to out-of-range ones
	if len(openedContainers) > 0 {
		WaitForItemsAfterMultipleContainers(openedContainers)
	}

	// Now process out-of-range containers by moving to each one
	// Use reliable interaction (with verification) instead of fast batch for better success rate
	if len(containersOutOfRange) > 0 {
		ctx.Logger.Info("Processing containers outside initial range",
			slog.Int("outOfRangeCount", len(containersOutOfRange)),
		)

		processedIDs := make(map[data.UnitID]bool)

		for _, obj := range containersOutOfRange {
			if processedIDs[obj.ID] {
				continue // Already opened in a previous sub-batch
			}

			ctx.PauseIfNotPriority()
			ctx.RefreshGameData()

			// Check if container still exists and is selectable
			currentObj, found := ctx.Data.Objects.FindByID(obj.ID)
			if !found {
				ctx.Logger.Debug("Container not found, skipping",
					slog.Int("objID", int(obj.ID)),
				)
				processedIDs[obj.ID] = true
				continue
			}
			if !currentObj.Selectable {
				ctx.Logger.Debug("Container not selectable (already opened?)",
					slog.String("container", string(obj.Name)),
					slog.Int("objID", int(obj.ID)),
				)
				processedIDs[obj.ID] = true
				continue
			}

			// Move closer to container
			moveErr := MoveToCoords(currentObj.Position)
			if moveErr != nil {
				ctx.Logger.Info("Failed to move to container",
					slog.String("container", string(obj.Name)),
					slog.Int("objID", int(obj.ID)),
					slog.Any("error", moveErr),
				)
				processedIDs[obj.ID] = true
				continue
			}

			// After moving, try to open the TARGET container first (we just moved to it)
			ctx.RefreshGameData()
			var subBatchContainers []containerPosition

			// Check distance after moving
			newPlayerPos := ctx.Data.PlayerUnit.Position
			distAfterMove := pather.DistanceFromPoint(newPlayerPos, currentObj.Position)

			// Try to open the target container we moved to
			targetObj, targetFound := ctx.Data.Objects.FindByID(obj.ID)
			if targetFound && targetObj.Selectable {
				ctx.Logger.Debug("Attempting to interact with container",
					slog.String("container", string(targetObj.Name)),
					slog.Int("distanceAfterMove", distAfterMove),
				)

				err := step.InteractObject(targetObj, func() bool {
					o, f := ctx.Data.Objects.FindByID(targetObj.ID)
					return f && !o.Selectable
				})

				if err == nil {
					successCount++
					subBatchContainers = append(subBatchContainers, containerPosition{
						pos: targetObj.Position,
						obj: targetObj,
					})
					ctx.Logger.Debug("Successfully opened container",
						slog.String("container", string(targetObj.Name)),
					)
				} else {
					failCount++
					ctx.Logger.Info("Failed to open container after moving to it",
						slog.String("container", string(targetObj.Name)),
						slog.Int("distanceAfterMove", distAfterMove),
						slog.Any("error", err),
					)
				}
			} else if !targetFound {
				ctx.Logger.Debug("Target container disappeared after move",
					slog.Int("objID", int(obj.ID)),
				)
			} else {
				ctx.Logger.Debug("Target container no longer selectable after move",
					slog.String("container", string(targetObj.Name)),
				)
			}
			processedIDs[obj.ID] = true

			// Also try to open any OTHER nearby containers we haven't processed yet
			ctx.RefreshGameData()
			newPlayerPos = ctx.Data.PlayerUnit.Position

			for _, outObj := range containersOutOfRange {
				if processedIDs[outObj.ID] {
					continue
				}

				updatedObj, objFound := ctx.Data.Objects.FindByID(outObj.ID)
				if !objFound || !updatedObj.Selectable {
					processedIDs[outObj.ID] = true
					continue
				}

				dist := pather.DistanceFromPoint(newPlayerPos, updatedObj.Position)

				// Only try containers within reasonable range (TK range or close click range)
				if dist <= telekinesisRange+5 || dist <= 15 {
					err := step.InteractObject(updatedObj, func() bool {
						o, f := ctx.Data.Objects.FindByID(updatedObj.ID)
						return f && !o.Selectable
					})

					if err == nil {
						successCount++
						subBatchContainers = append(subBatchContainers, containerPosition{
							pos: updatedObj.Position,
							obj: updatedObj,
						})
					}
					processedIDs[outObj.ID] = true
				}
			}

			// Wait for items from all containers opened in this sub-batch
			if len(subBatchContainers) > 0 {
				openedContainers = append(openedContainers, subBatchContainers...)
				WaitForItemsAfterMultipleContainers(subBatchContainers)
			}
		}
	}

	totalBatchDuration := time.Since(batchStartTime)

	// Log final results
	if len(openedContainers) > 0 {
		avgDuration := totalBatchDuration / time.Duration(len(openedContainers))
		ctx.Logger.Info("Batch container opening finished",
			slog.Int("containersOpened", len(openedContainers)),
			slog.Int("failed", failCount),
			slog.Int("inRangeProcessed", len(containersInRange)),
			slog.Int("outOfRangeProcessed", len(containersOutOfRange)),
			slog.Duration("totalDuration", totalBatchDuration),
			slog.Duration("avgPerContainer", avgDuration),
		)

		// Log warning if batch opening is slow (>1s avg per container when multiple)
		if len(openedContainers) > 1 && avgDuration > 1*time.Second {
			ctx.Logger.Warn("Slow batch container opening",
				slog.Duration("avgPerContainer", avgDuration),
				slog.Int("containersCount", len(openedContainers)),
			)
		}
	} else {
		ctx.Logger.Info("No containers opened in batch",
			slog.Int("totalContainers", len(containers)),
			slog.Int("inRange", len(containersInRange)),
			slog.Int("outOfRange", len(containersOutOfRange)),
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
