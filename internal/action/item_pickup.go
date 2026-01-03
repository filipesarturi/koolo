package action

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/event"
	"github.com/hectorgimenez/koolo/internal/utils"
)

// High runes (Vex and above) - highest pickup priority
var highRunes = map[item.Name]bool{
	"VexRune":  true,
	"OhmRune":  true,
	"LoRune":   true,
	"SurRune":  true,
	"BerRune":  true,
	"JahRune":  true,
	"ChamRune": true,
	"ZodRune":  true,
}

// Mid runes (Pul to Gul) - second priority
var midRunes = map[item.Name]bool{
	"PulRune": true,
	"UmRune":  true,
	"MalRune": true,
	"IstRune": true,
	"GulRune": true,
}

// getTelekinesisItemPickupRange returns the configured telekinesis range for item pickup, defaulting to 23 if not set
func getTelekinesisItemPickupRange() int {
	ctx := context.Get()
	if ctx.CharacterCfg.Character.TelekinesisRange > 0 {
		return ctx.CharacterCfg.Character.TelekinesisRange
	}
	return 23 // Default: 23 tiles (~15.3 yards)
}

func itemFitsInventory(i data.Item) bool {
	invMatrix := context.Get().Data.Inventory.Matrix()

	for y := 0; y <= len(invMatrix)-i.Desc().InventoryHeight; y++ {
		for x := 0; x <= len(invMatrix[0])-i.Desc().InventoryWidth; x++ {
			freeSpace := true
			for dy := 0; dy < i.Desc().InventoryHeight; dy++ {
				for dx := 0; dx < i.Desc().InventoryWidth; dx++ {
					if invMatrix[y+dy][x+dx] {
						freeSpace = false
						break
					}
				}
				if !freeSpace {
					break
				}
			}

			if freeSpace {
				return true
			}
		}
	}

	return false
}

func itemNeedsInventorySpace(i data.Item) bool {
	// Gold does not occupy grid slots.
	if i.Name == "Gold" {
		return false
	}
	// Potions can go to belt, and we don't want "no grid slot" to trigger town trips/blacklists for them.
	if i.IsPotion() {
		return false
	}
	return true
}

// HasTPsAvailable checks if the player has at least one Town Portal in their tome.
func HasTPsAvailable() bool {
	ctx := context.Get()

	// Check for Tome of Town Portal
	portalTome, found := ctx.Data.Inventory.Find(item.TomeOfTownPortal, item.LocationInventory)
	if !found {
		_, foundScroll := ctx.Data.Inventory.Find(item.ScrollOfTownPortal)
		if foundScroll {
			return true
		}
		return false // No portal tome found at all
	}

	qty, found := portalTome.FindStat(stat.Quantity, 0)
	// Return true only if the quantity stat was found and the value is greater than 0
	return found && qty.Value > 0
}

func ItemPickup(maxDistance int) error {
	ctx := context.Get()
	ctx.SetLastAction("ItemPickup")

	const maxRetries = 5                                        // Base retries for various issues
	const maxItemTooFarAttempts = 5                             // Additional retries specifically for "item too far"
	const totalMaxAttempts = maxRetries + maxItemTooFarAttempts // Combined total attempts
	const debugPickit = false
	const globalPickupTimeout = 60 * time.Second                // Global timeout to prevent infinite loops

	// If we're already picking items, skip it
	if ctx.CurrentGame.IsPickingItems {
		return nil
	}

	// Lock items pickup from other sources during the execution of the function
	ctx.SetPickingItems(true)
	defer func() {
		ctx.SetPickingItems(false)
	}()

	// Global timeout to prevent the function from running forever
	globalStartTime := time.Now()

	// Track how many times we tried to "clean inventory in town" for a specific ground UnitID
	// to avoid infinite town-loops when an item will never fit due to charm layout, etc.
	townCleanupByUnitID := map[data.UnitID]int{}
	consecutiveNoFitTownTrips := 0

outer:
	for {
		// Check global timeout to prevent infinite loops
		if time.Since(globalStartTime) > globalPickupTimeout {
			ctx.Logger.Warn("ItemPickup global timeout reached, aborting pickup cycle",
				"elapsed", time.Since(globalStartTime),
			)
			return fmt.Errorf("item pickup timeout after %v", globalPickupTimeout)
		}

		// Use timeout version to prevent infinite blocking in multi-bot scenarios
		if !ctx.PauseIfNotPriorityWithTimeout(5 * time.Second) {
			ctx.Logger.Debug("Priority wait timeout in ItemPickup, continuing...")
		}

		// Inventory state can drift while moving/clearing. Refresh before deciding what "fits".
		ctx.RefreshInventory()

		itemsToPickup := GetItemsToPickup(maxDistance)
		if len(itemsToPickup) == 0 {
			return nil
		}

		var itemToPickup data.Item
		for _, i := range itemsToPickup {
			// Prefer items that we can actually place.
			if !itemNeedsInventorySpace(i) || itemFitsInventory(i) {
				itemToPickup = i
				break
			}
		}

		if itemToPickup.UnitID == 0 {
			if debugPickit {
				ctx.Logger.Debug("No fitting items found for pickup after filtering.")
			}
			if HasTPsAvailable() {
				consecutiveNoFitTownTrips++
				if consecutiveNoFitTownTrips > 1 {
					// Prevent endless TP-town-TP loops when an item can never fit.
					ctx.Logger.Warn("No fitting items after a town cleanup; stopping pickup cycle to avoid loops.")
					return nil
				}

				if debugPickit {
					ctx.Logger.Debug("TPs available, returning to town to sell junk and stash items.")
				}
				if err := InRunReturnTownRoutine(); err != nil {
					ctx.Logger.Warn("Failed returning to town from ItemPickup", "error", err)
				}
				continue
			}

			ctx.Logger.Warn("Inventory is full and NO Town Portals found. Skipping return to town and continuing current run (no more item pickups this cycle).")
			return nil
		}

		consecutiveNoFitTownTrips = 0

		if debugPickit {
			ctx.Logger.Info(fmt.Sprintf(
				"Attempting to pickup item: %s [%d] at X:%d Y:%d",
				itemToPickup.Name,
				itemToPickup.Quality,
				itemToPickup.Position.X,
				itemToPickup.Position.Y,
			))
		}

		// Try to pick up the item with retries
		var lastError error
		attempt := 1
		itemTooFarRetryCount := 0     // Tracks retries specifically for "item too far"
		totalAttemptCounter := 0      // Overall attempts
		var consecutiveMoveErrors int // Track consecutive ErrCastingMoving errors
		pickedUp := false

		for totalAttemptCounter < totalMaxAttempts {
			totalAttemptCounter++
			if debugPickit {
				ctx.Logger.Debug(fmt.Sprintf("Item Pickup: Starting attempt %d (total: %d)", attempt, totalAttemptCounter))
			}

			// If inventory changed and item no longer fits, do NOT grind attempts and then blacklist.
			// Instead: go to town (stash/sell), come back and retry.
			if itemNeedsInventorySpace(itemToPickup) {
				ctx.RefreshInventory()
				if !itemFitsInventory(itemToPickup) {
					if HasTPsAvailable() {
						townCleanupByUnitID[itemToPickup.UnitID]++
						if townCleanupByUnitID[itemToPickup.UnitID] <= 1 {
							ctx.Logger.Debug("Item doesn't fit in inventory right now; returning to town to stash/sell and retry.",
								slog.String("itemName", string(itemToPickup.Desc().Name)),
								slog.Int("unitID", int(itemToPickup.UnitID)),
							)
							if err := InRunReturnTownRoutine(); err != nil {
								ctx.Logger.Warn("Failed returning to town from ItemPickup", "error", err)
							}
							continue outer
						}
						// Already tried town once and it still doesn't fit: blacklist this ground instance to stop thrashing.
						lastError = fmt.Errorf("item does not fit in inventory even after town cleanup")
						break
					}
					ctx.Logger.Warn("Inventory full and NO Town Portals found. Skipping further item pickups this cycle.")
					return nil
				}
			}

			pickupStartTime := time.Now()

			// Clear monsters on each attempt
			if debugPickit {
				ctx.Logger.Debug(fmt.Sprintf("Item Pickup: Clearing area around item. Attempt %d", attempt))
			}
			ClearAreaAroundPlayer(4, data.MonsterAnyFilter())
			ClearAreaAroundPosition(itemToPickup.Position, 4, data.MonsterAnyFilter())
			if debugPickit {
				ctx.Logger.Debug(fmt.Sprintf("Item Pickup: Area cleared in %v. Attempt %d", time.Since(pickupStartTime), attempt))
			}

			// Check if Telekinesis can be used for this item
			canUseTK := canUseTelekinesisForItemPickup(itemToPickup)
			distance := ctx.PathFinder.DistanceFromMe(itemToPickup.Position)
			telekinesisItemPickupRange := getTelekinesisItemPickupRange()

			// If Telekinesis is available and we're in range, skip movement
			if canUseTK && distance <= telekinesisItemPickupRange && attempt == 1 {
				if debugPickit {
					ctx.Logger.Debug(fmt.Sprintf("Item Pickup: Using Telekinesis from current position (distance: %d). Attempt %d", distance, attempt))
				}
				// Skip movement, go directly to pickup
			} else {
				// Calculate position to move to based on attempt number
				pickupPosition := itemToPickup.Position
				moveDistance := 3
				if attempt > 1 {
					switch attempt {
					case 2:
						pickupPosition = data.Position{X: itemToPickup.Position.X + moveDistance, Y: itemToPickup.Position.Y - 1}
					case 3:
						pickupPosition = data.Position{X: itemToPickup.Position.X - moveDistance, Y: itemToPickup.Position.Y + 1}
					case 4:
						pickupPosition = data.Position{X: itemToPickup.Position.X + moveDistance + 2, Y: itemToPickup.Position.Y - 3}
					case 5:
						MoveToCoords(ctx.PathFinder.BeyondPosition(ctx.Data.PlayerUnit.Position, itemToPickup.Position, 4), step.WithIgnoreItems())
					}
				}

				// For Telekinesis, only move if beyond TK range
				minDistanceForMove := 7
				if canUseTK {
					minDistanceForMove = telekinesisItemPickupRange
				}

				if distance >= minDistanceForMove || attempt > 1 {
					distanceToFinish := max(4-attempt, 2)
					// If using Telekinesis, stop at TK range instead of close
					if canUseTK && attempt == 1 {
						distanceToFinish = telekinesisItemPickupRange - 2
					}
					if debugPickit {
						ctx.Logger.Debug(fmt.Sprintf("Item Pickup: Moving to coordinates X:%d Y:%d (distance: %d, distToFinish: %d). Attempt %d", pickupPosition.X, pickupPosition.Y, distance, distanceToFinish, attempt))
					}
					if err := MoveToCoords(pickupPosition, step.WithDistanceToFinish(distanceToFinish), step.WithIgnoreItems()); err != nil {
						lastError = err
						continue
					}
					if debugPickit {
						ctx.Logger.Debug(fmt.Sprintf("Item Pickup: Move completed in %v. Attempt %d", time.Since(pickupStartTime), attempt))
					}
				}
			}

			// Try to pick up the item
			pickupActionStartTime := time.Now()
			if debugPickit {
				ctx.Logger.Debug(fmt.Sprintf("Item Pickup: Initiating PickupItem action. Attempt %d", attempt))
			}

			err := step.PickupItem(itemToPickup, attempt)
			if err == nil {
				pickedUp = true
				lastError = nil
				if debugPickit {
					ctx.Logger.Info(fmt.Sprintf("Successfully picked up item: %s [%d] in %v. Total attempts: %d", itemToPickup.Name, itemToPickup.Quality, time.Since(pickupActionStartTime), totalAttemptCounter))
				}
				break
			}

			lastError = err
			if debugPickit {
				ctx.Logger.Warn(fmt.Sprintf("Item Pickup: Pickup attempt %d failed: %v", attempt, err), slog.String("itemName", string(itemToPickup.Name)))
			}

			// If the pickup failed and the item doesn't fit *right now*, don't blacklist it.
			// This is the exact scenario where we should go stash/sell and retry.
			if itemNeedsInventorySpace(itemToPickup) {
				ctx.RefreshInventory()
				if !itemFitsInventory(itemToPickup) {
					if HasTPsAvailable() {
						townCleanupByUnitID[itemToPickup.UnitID]++
						if townCleanupByUnitID[itemToPickup.UnitID] <= 1 {
							ctx.Logger.Debug("Pickup failed and item no longer fits; returning to town to stash/sell and retry.",
								slog.String("itemName", string(itemToPickup.Desc().Name)),
								slog.Int("unitID", int(itemToPickup.UnitID)),
							)
							if errTown := InRunReturnTownRoutine(); errTown != nil {
								ctx.Logger.Warn("Failed returning to town from ItemPickup", "error", errTown)
							}
							continue outer
						}
						lastError = fmt.Errorf("item does not fit in inventory even after town cleanup: %w", err)
						break
					}
					ctx.Logger.Warn("Inventory full and NO Town Portals found. Skipping further item pickups this cycle.")
					return nil
				}
			}

			// Movement-state handling
			if errors.Is(err, step.ErrCastingMoving) {
				consecutiveMoveErrors++
				if consecutiveMoveErrors > 3 {
					lastError = fmt.Errorf("failed to pick up item after multiple attempts due to movement state: %w", err)
					break
				}
				time.Sleep(time.Millisecond * time.Duration(utils.PingMultiplier(utils.Light, 100)))
				continue
			}

			if errors.Is(err, step.ErrMonsterAroundItem) {
				continue
			}

			// Item too far retry logic
			if errors.Is(err, step.ErrItemTooFar) {
				itemTooFarRetryCount++
				if debugPickit {
					ctx.Logger.Debug(fmt.Sprintf("Item Pickup: Item too far detected. ItemTooFar specific retry %d/%d.", itemTooFarRetryCount, maxItemTooFarAttempts))
				}
				if itemTooFarRetryCount < maxItemTooFarAttempts {
					ctx.PathFinder.RandomMovement()
					continue
				}
			}

			if errors.Is(err, step.ErrNoLOSToItem) {
				if debugPickit {
					ctx.Logger.Debug("Item Pickup: No line of sight to item, moving closer",
						slog.String("item", string(itemToPickup.Desc().Name)))
				}
				beyondPos := ctx.PathFinder.BeyondPosition(ctx.Data.PlayerUnit.Position, itemToPickup.Position, 2+attempt)
				if mvErr := MoveToCoords(beyondPos, step.WithIgnoreItems()); mvErr == nil {
					err = step.PickupItem(itemToPickup, attempt)
					if err == nil {
						pickedUp = true
						lastError = nil
						if debugPickit {
							ctx.Logger.Info(fmt.Sprintf("Successfully picked up item after LOS correction: %s [%d] in %v. Total attempts: %d", itemToPickup.Name, itemToPickup.Quality, time.Since(pickupActionStartTime), totalAttemptCounter))
						}
						break
					}
					lastError = err
				} else {
					lastError = mvErr
				}
			}

			attempt++
		}

		if pickedUp {
			continue
		}

		// Final guard: if it doesn't fit at the end, prefer a town cleanup over blacklisting.
		if lastError != nil && itemNeedsInventorySpace(itemToPickup) {
			ctx.RefreshInventory()
			if !itemFitsInventory(itemToPickup) {
				if HasTPsAvailable() {
					townCleanupByUnitID[itemToPickup.UnitID]++
					if townCleanupByUnitID[itemToPickup.UnitID] <= 1 {
						if err := InRunReturnTownRoutine(); err != nil {
							ctx.Logger.Warn("Failed returning to town from ItemPickup", "error", err)
						}
						continue
					}
					// Still doesn't fit after town: fall through to blacklist this UnitID.
				} else {
					return nil
				}
			}
		}

		// If all attempts failed, blacklist *this specific ground instance* (UnitID), not the whole base item ID.
		if totalAttemptCounter >= totalMaxAttempts && lastError != nil {
			if !IsBlacklisted(itemToPickup) {
				ctx.CurrentGame.BlacklistedItems = append(ctx.CurrentGame.BlacklistedItems, itemToPickup)
			}

			// Screenshot with show items on
			ctx.HID.KeyDown(ctx.Data.KeyBindings.ShowItems)
			time.Sleep(time.Millisecond * time.Duration(utils.PingMultiplier(utils.Light, 200)))
			screenshot := ctx.GameReader.Screenshot()
			event.Send(event.ItemBlackListed(event.WithScreenshot(ctx.Name, fmt.Sprintf("Item %s [%s] BlackListed in Area:%s", itemToPickup.Name, itemToPickup.Quality.ToString(), ctx.Data.PlayerUnit.Area.Area().Name), screenshot), data.Drop{Item: itemToPickup}))
			ctx.HID.KeyUp(ctx.Data.KeyBindings.ShowItems)

			ctx.Logger.Warn(
				"Failed picking up item after all attempts, blacklisting it",
				slog.String("itemName", string(itemToPickup.Desc().Name)),
				slog.Int("unitID", int(itemToPickup.UnitID)),
				slog.String("lastError", lastError.Error()),
				slog.Int("totalAttempts", totalAttemptCounter),
			)
		}
	}
}

// HasItemsToPickup checks if there are items on the ground that should be picked up
// This is useful for continuous monitoring, especially in public games where other players may kill monsters
func HasItemsToPickup(maxDistance int) bool {
	items := GetItemsToPickup(maxDistance)
	return len(items) > 0
}

func GetItemsToPickup(maxDistance int) []data.Item {
	ctx := context.Get()
	ctx.SetLastAction("GetItemsToPickup")

	missingHealingPotions := ctx.BeltManager.GetMissingCount(data.HealingPotion) + ctx.Data.MissingPotionCountInInventory(data.HealingPotion)
	missingManaPotions := ctx.BeltManager.GetMissingCount(data.ManaPotion) + ctx.Data.MissingPotionCountInInventory(data.ManaPotion)
	missingRejuvenationPotions := ctx.BeltManager.GetMissingCount(data.RejuvenationPotion) + ctx.Data.MissingPotionCountInInventory(data.RejuvenationPotion)

	var itemsToPickup []data.Item
	_, isLevelingChar := ctx.Char.(context.LevelingCharacter)

	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationGround) {
		// Skip itempickup on party leveling Maggot Lair, is too narrow and causes characters to get stuck
		if isLevelingChar && itm.Name != "StaffOfKings" && (ctx.Data.PlayerUnit.Area == area.MaggotLairLevel1 ||
			ctx.Data.PlayerUnit.Area == area.MaggotLairLevel2 ||
			ctx.Data.PlayerUnit.Area == area.MaggotLairLevel3 ||
			ctx.Data.PlayerUnit.Area == area.ArcaneSanctuary) {
			continue
		}

		// Skip potion pickup for Berserker Barb in Travincal if configured
		if ctx.CharacterCfg.Character.Class == "berserker" &&
			ctx.CharacterCfg.Character.BerserkerBarb.SkipPotionPickupInTravincal &&
			ctx.Data.PlayerUnit.Area == area.Travincal &&
			itm.IsPotion() {
			continue
		}

		// Skip potion pickup for Warcry Barb in Travincal if configured
		if ctx.CharacterCfg.Character.Class == "warcry_barb" &&
			ctx.CharacterCfg.Character.WarcryBarb.SkipPotionPickupInTravincal &&
			ctx.Data.PlayerUnit.Area == area.Travincal &&
			itm.IsPotion() {
			continue
		}

		// Skip items that are outside pickup radius, this is useful when clearing big areas to prevent
		// character going back to pickup potions all the time after using them
		itemDistance := ctx.PathFinder.DistanceFromMe(itm.Position)
		if maxDistance > 0 && itemDistance > maxDistance && itm.IsPotion() {
			continue
		}

		if itm.IsPotion() {
			if (itm.IsHealingPotion() && missingHealingPotions > 0) ||
				(itm.IsManaPotion() && missingManaPotions > 0) ||
				(itm.IsRejuvPotion() && missingRejuvenationPotions > 0) {
				if shouldBePickedUp(itm) {
					itemsToPickup = append(itemsToPickup, itm)
					switch {
					case itm.IsHealingPotion():
						missingHealingPotions--
					case itm.IsManaPotion():
						missingManaPotions--
					case itm.IsRejuvPotion():
						missingRejuvenationPotions--
					}
				}
			}
		} else if shouldBePickedUp(itm) {
			itemsToPickup = append(itemsToPickup, itm)
		}
	}

	// Remove blacklisted items from the list, we don't want to pick them up
	filteredItems := make([]data.Item, 0, len(itemsToPickup))
	for _, itm := range itemsToPickup {
		isBlacklisted := IsBlacklisted(itm)
		if !isBlacklisted {
			filteredItems = append(filteredItems, itm)
		}
	}

	// Sort items by priority: high runes > mid runes > unique/set > other > potions/scrolls/gold
	sortItemsByPriority(filteredItems)

	return filteredItems
}

// getItemPickupPriority returns a priority value for sorting (lower = higher priority)
func getItemPickupPriority(itm data.Item) int {
	// Priority 1: High Runes (Vex+) - most valuable, pick up first
	if highRunes[itm.Name] {
		return 1
	}

	// Priority 2: Mid Runes (Pul-Gul)
	if midRunes[itm.Name] {
		return 2
	}

	// Priority 3: Unique and Set items
	if itm.Quality == item.QualityUnique || itm.Quality == item.QualitySet {
		return 3
	}

	// Priority 4: Other runes (low runes) and valuable items
	if itm.Desc().Type == item.TypeRune {
		return 4
	}

	// Priority 4: Rare items
	if itm.Quality == item.QualityRare {
		return 4
	}

	// Priority 5: Magic items and other equipment
	if itm.Quality == item.QualityMagic {
		return 5
	}

	// Priority 6: Potions, Gold - pick up last
	if itm.IsPotion() || itm.Name == "Gold" {
		return 6
	}

	// Priority 7: Keys - low priority, only pick up if not at capacity
	if itm.Name == item.Key {
		return 7
	}

	// Priority 8: Scrolls - lowest priority, only pick up if tome exists and not at capacity
	if strings.Contains(string(itm.Name), "Scroll") {
		return 8
	}

	// Default priority for anything else
	return 5
}

// sortItemsByPriority sorts items by pickup priority (higher priority items first)
func sortItemsByPriority(items []data.Item) {
	ctx := context.Get()

	sort.SliceStable(items, func(i, j int) bool {
		priorityI := getItemPickupPriority(items[i])
		priorityJ := getItemPickupPriority(items[j])

		// If same priority, prefer closer items
		if priorityI == priorityJ {
			distI := ctx.PathFinder.DistanceFromMe(items[i].Position)
			distJ := ctx.PathFinder.DistanceFromMe(items[j].Position)
			return distI < distJ
		}

		return priorityI < priorityJ
	})
}

func shouldBePickedUp(i data.Item) bool {
	ctx := context.Get()
	ctx.SetLastAction("shouldBePickedUp")

	// Always pick up runewords and Wirt's Leg.
	if i.IsRuneword || i.Name == "WirtsLeg" {
		return true
	}

	// Pick up quest items if in a leveling or questing run.
	specialRuns := slices.Contains(ctx.CharacterCfg.Game.Runs, "quests") || slices.Contains(ctx.CharacterCfg.Game.Runs, "leveling") || slices.Contains(ctx.CharacterCfg.Game.Runs, "leveling_sequence")
	if specialRuns {
		switch i.Name {
		case "Scroll of Inifuss", "ScrollOfInifuss", "LamEsensTome", "HoradricCube", "HoradricMalus",
			"AmuletoftheViper", "StaffofKings", "HoradricStaff",
			"AJadeFigurine", "KhalimsEye", "KhalimsBrain", "KhalimsHeart", "KhalimsFlail", "HellforgeHammer", "TheGidbinn":
			return true
		}
	}
	// Specific ID checks (e.g. Book of Skill and Scroll of Inifuss).
	if i.ID == 552 || i.ID == 524 {
		return true
	}

	// Skip picking up gold if inventory is full of gold.
	gold, _ := ctx.Data.PlayerUnit.FindStat(stat.Gold, 0)
	if gold.Value >= ctx.Data.PlayerUnit.MaxGold() && i.Name == "Gold" {
		ctx.Logger.Debug("Skipping gold pickup, inventory full")
		return false
	}

	// In leveling runs, pick up any non‑gold item if very low on gold.
	_, isLevelingChar := ctx.Char.(context.LevelingCharacter)
	if isLevelingChar && IsLowGold() && i.Name != "Gold" {
		return true
	}

	// Pick up stamina potions only when needed in leveling runs.
	if isLevelingChar && i.Name == "StaminaPotion" {
		if ctx.HealthManager.ShouldPickStaminaPot() {
			return true
		}
	}

	// Pick up keys if we have less than the configured KeyCount (low priority pickup)
	if i.Name == item.Key {
		keyCount := getKeyCount()
		if keyCount <= 0 {
			// If KeyCount is 0 or disabled, don't pick up keys
			return false
		}

		// Count current keys in inventory
		totalKeys := 0
		for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if itm.Name == item.Key {
				if qty, found := itm.FindStat(stat.Quantity, 0); found {
					totalKeys += qty.Value
				} else {
					totalKeys++ // If no quantity stat, assume stack of 1
				}
			}
		}

		// Only pick up keys if we have less than the configured amount
		if totalKeys < keyCount {
			return true
		}
		return false
	}

	// Pick up scrolls if we have the corresponding tome and it's not full (low priority pickup)
	const maxScrollsInTome = 20 // Maximum scrolls a tome can hold
	if i.Name == item.ScrollOfTownPortal {
		portalTome, found := ctx.Data.Inventory.Find(item.TomeOfTownPortal, item.LocationInventory)
		if !found {
			return false // Don't pick up scrolls if we don't have the tome
		}

		qty, found := portalTome.FindStat(stat.Quantity, 0)
		if !found {
			// If no quantity stat, assume tome is empty
			return true
		}

		// Only pick up if tome has less than maximum capacity
		return qty.Value < maxScrollsInTome
	}

	if i.Name == item.ScrollOfIdentify {
		// Respect end-game setting: completely disable ID tome for non-leveling characters
		_, isLevelingChar := ctx.Char.(context.LevelingCharacter)
		if ctx.CharacterCfg.Game.DisableIdentifyTome && !isLevelingChar {
			return false
		}

		idTome, found := ctx.Data.Inventory.Find(item.TomeOfIdentify, item.LocationInventory)
		if !found {
			return false // Don't pick up scrolls if we don't have the tome
		}

		qty, found := idTome.FindStat(stat.Quantity, 0)
		if !found {
			// If no quantity stat, assume tome is empty
			return true
		}

		// Only pick up if tome has less than maximum capacity
		return qty.Value < maxScrollsInTome
	}

	// If total gold is below the minimum threshold, pick up magic and better items for selling.
	minGoldPickupThreshold := ctx.CharacterCfg.Game.MinGoldPickupThreshold
	if ctx.Data.PlayerUnit.TotalPlayerGold() < minGoldPickupThreshold && i.Quality >= item.QualityMagic {
		return true
	}

	// After all heuristics, defer to strict pickit/tier evaluation.
	// This function encapsulates the final rule logic (tiers and NIP) and
	// handles quantity blacklisting without re‑implementing it here.
	return shouldMatchRulesOnly(i)
}

func IsBlacklisted(itm data.Item) bool {
	for _, blacklisted := range context.Get().CurrentGame.BlacklistedItems {
		// Blacklist is per-game. UnitID is the safest key: it targets only the problematic ground instance.
		if itm.UnitID == blacklisted.UnitID {
			return true
		}
	}
	return false
}

// canUseTelekinesisForItemPickup checks if Telekinesis can be used to pick up this item
func canUseTelekinesisForItemPickup(it data.Item) bool {
	ctx := context.Get()

	// Check if Telekinesis is enabled in config
	if !ctx.CharacterCfg.Character.UseTelekinesis {
		return false
	}

	// Check if character has Telekinesis skill
	if ctx.Data.PlayerUnit.Skills[skill.Telekinesis].Level == 0 {
		return false
	}

	// Check if Telekinesis has a keybinding (required for HID interaction)
	if _, found := ctx.Data.KeyBindings.KeyBindingForSkill(skill.Telekinesis); !found {
		return false
	}

	// Telekinesis works on: potions, gold, and scrolls (TP/ID)
	if it.IsPotion() || it.Name == "Gold" ||
		it.Name == item.ScrollOfTownPortal || it.Name == item.ScrollOfIdentify {
		return true
	}

	return false
}
