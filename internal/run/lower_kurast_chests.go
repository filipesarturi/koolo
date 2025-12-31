package run

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/pather"
	"github.com/hectorgimenez/koolo/internal/utils"
)

var minChestDistanceFromBonfire = 25
var maxChestDistanceFromBonfire = 45

type LowerKurastChests struct {
	ctx *context.Status
}

func NewLowerKurastChest() *LowerKurastChests {
	return &LowerKurastChests{
		ctx: context.Get(),
	}
}

func (run LowerKurastChests) Name() string {
	return string(config.LowerKurastChestRun)
}

func (run LowerKurastChests) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	if !run.ctx.Data.Quests[quest.Act2TheSevenTombs].Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (run LowerKurastChests) Run(parameters *RunParameters) error {
	run.ctx.Logger.Debug("Running a Lower Kurast Chest run")

	// Use Waypoint to Lower Kurast
	err := action.WayPoint(area.LowerKurast)
	if err != nil {
		return err
	}

	// Get bonfires from cached map data
	var bonFirePositions []data.Position
	if areaData, ok := run.ctx.GameReader.GetData().Areas[area.LowerKurast]; ok {
		for _, obj := range areaData.Objects {
			if obj.Name == object.Name(160) { // SmallFire
				run.ctx.Logger.Debug("Found bonfire at:", "position", obj.Position)
				bonFirePositions = append(bonFirePositions, obj.Position)
			}
		}
	}

	run.ctx.Logger.Debug("Total bonfires found", "count", len(bonFirePositions))

	// Define objects to interact with : chests + weapon racks/armor stands (if enabled)
	interactableObjects := []object.Name{object.JungleMediumChestLeft, object.JungleChest}

	if run.ctx.CharacterCfg.Game.LowerKurastChest.OpenRacks {
		interactableObjects = append(interactableObjects,
			object.ArmorStandRight,
			object.ArmorStandLeft,
			object.WeaponRackRight,
			object.WeaponRackLeft,
		)
	}

	// If OpenAllChests is enabled, clear the entire map
	if run.ctx.CharacterCfg.Game.LowerKurastChest.OpenAllChests {
		return run.clearAllInteractableObjects()
	}

	// Otherwise, use the bonfire-based approach for superchests
	// Move to each of the bonfires one by one
	for _, bonfirePos := range bonFirePositions {
		// Move to the bonfire
		err = action.MoveToCoords(bonfirePos)
		if err != nil {
			return err
		}

		// Find the interactable objects
		var objects []data.Object
		for _, o := range run.ctx.Data.Objects {
			// Check if object is within bonfire range
			if !isChestWithinBonfireRange(o, bonfirePos) {
				continue
			}

			// Only include specific interactable objects
			if slices.Contains(interactableObjects, o.Name) {
				objects = append(objects, o)
			}
		}

		// Interact with objects in the order of shortest travel
		for len(objects) > 0 {

			playerPos := run.ctx.Data.PlayerUnit.Position

			sort.Slice(objects, func(i, j int) bool {
				return pather.DistanceFromPoint(objects[i].Position, playerPos) <
					pather.DistanceFromPoint(objects[j].Position, playerPos)
			})

			// Interact with the closest object
			closestObject := objects[0]
			err = action.InteractObject(closestObject, func() bool {
				object, _ := run.ctx.Data.Objects.FindByID(closestObject.ID)
				return !object.Selectable
			})
			if err != nil {
				run.ctx.Logger.Warn(fmt.Sprintf("[%s] failed interacting with object [%v] in Area: [%s]", run.ctx.Name, closestObject.Name, run.ctx.Data.PlayerUnit.Area.Area().Name), err)
			}
			utils.Sleep(500) // Add small delay to allow the game to open the object and drop the content

			// Remove the interacted container from the list
			objects = objects[1:]
		}
	}

	// Return to town
	if err = action.ReturnTown(); err != nil {
		return err
	}

	_, isLevelingChar := run.ctx.Char.(context.LevelingCharacter)

	if !isLevelingChar {

		// Move to A4 if possible to shorten the run time
		err = action.WayPoint(area.ThePandemoniumFortress)
		if err != nil {
			return err
		}

	}

	// Done
	return nil
}

// clearAllInteractableObjects clears all interactable objects from the entire map
// Optimized version based on ClearCurrentLevel but without monster clearing for maximum speed
func (run LowerKurastChests) clearAllInteractableObjects() error {
	run.ctx.Logger.Debug("Clearing all interactable objects from the entire map (optimized)")

	const (
		pickupRadius    = 20
		telekinesisRange = 15
	)

	// Use optimized room traversal
	rooms := run.ctx.PathFinder.OptimizeRoomsTraverseOrder()
	
	for _, r := range rooms {
		run.ctx.PauseIfNotPriority()

		// Move to room center quickly (no monster clearing for speed)
		path, _, found := run.ctx.PathFinder.GetClosestWalkablePath(r.GetCenter())
		if !found {
			continue
		}

		to := data.Position{
			X: path.To().X + run.ctx.Data.AreaOrigin.X,
			Y: path.To().Y + run.ctx.Data.AreaOrigin.Y,
		}
		
		// Quick movement without monster filter for speed
		err := action.MoveToCoords(to)
		if err != nil {
			continue
		}

		// Refresh game data
		run.ctx.RefreshGameData()

		// Find and interact with all interactable objects in this room
		for _, o := range run.ctx.Data.Objects {
			if !r.IsInside(o.Position) {
				continue
			}

			if !isInteractableObject(o) || !o.Selectable {
				continue
			}

			// Check if we can use Telekinesis from current position
			objDistance := run.ctx.PathFinder.DistanceFromMe(o.Position)
			canUseTK := run.canUseTelekinesisForObject(o)
			forceTK := run.ctx.CharacterCfg.Game.LowerKurastChest.ForceTelekinesis

			// If ForceTelekinesis is enabled and TK is available, let InteractObject handle movement
			// InteractObject will move to TK range if needed, or use TK directly if in range
			if forceTK && canUseTK {
				// Don't pre-move - let InteractObject handle it optimally
				// InteractObject will check distance and move to TK range if needed
			} else {
				// Normal mode: move if not within Telekinesis range (or TK not available)
				if !canUseTK || objDistance > telekinesisRange {
					err = action.MoveToCoords(o.Position)
					if err != nil {
						continue
					}
				}
			}

			// Interact with the object
			// If ForceTelekinesis is enabled, use step.InteractObject directly to bypass global UseTelekinesis check
			if forceTK && canUseTK {
				// Force TK usage by calling step.InteractObject directly
				// This bypasses the global UseTelekinesis check in action.InteractObject
				err = run.interactObjectWithForcedTK(o, func() bool {
					run.ctx.RefreshGameData()
					obj, found := run.ctx.Data.Objects.FindByID(o.ID)
					return !found || !obj.Selectable
				})
			} else {
				// Normal interaction (InteractObject will use TK if available and in range)
				err = action.InteractObject(o, func() bool {
					run.ctx.RefreshGameData()
					obj, found := run.ctx.Data.Objects.FindByID(o.ID)
					return !found || !obj.Selectable
				})
			}
			if err != nil {
				run.ctx.Logger.Debug("Failed interacting with object", "object", o.Name, "error", err)
				continue
			}

			// Wait for items to drop from chest/stash (some have delays, stashes have longer animations)
			run.waitForItemsToDrop(o.Position, o)
		}

		// Pick up items after clearing room (less frequent for speed)
		err = action.ItemPickup(pickupRadius)
		if err != nil {
			run.ctx.Logger.Debug("Failed to pickup items", "error", err)
		}
	}

	// Return to town
	if err := action.ReturnTown(); err != nil {
		return err
	}

	_, isLevelingChar := run.ctx.Char.(context.LevelingCharacter)

	if !isLevelingChar {
		// Move to A4 if possible to shorten the run time
		err := action.WayPoint(area.ThePandemoniumFortress)
		if err != nil {
			return err
		}
	}

	return nil
}

// waitForItemsToDrop waits for items to drop from opened chests/stashes
// Some containers have delays before items appear on the ground
// Stashes have longer animations and need more wait time
func (run LowerKurastChests) waitForItemsToDrop(containerPos data.Position, obj data.Object) {
	// Stashes have longer animations, need more wait time
	isStash := obj.Name == object.Bank
	
	var (
		initialDelay    int
		maxWaitTime     int
		checkInterval   = 100  // Check interval in ms
		itemCheckRadius = 2    // Radius to check for items (small to avoid detecting items from nearby containers)
	)

	if isStash {
		// Stashes have longer animations, wait more
		initialDelay = 800  // Initial delay for stashes in ms
		maxWaitTime = 3000  // Maximum total wait time for stashes in ms
	} else {
		// Regular chests and containers
		initialDelay = 300  // Initial delay in ms
		maxWaitTime = 1500  // Maximum total wait time in ms
	}

	utils.Sleep(initialDelay)

	// Check if items appeared on ground near the container
	run.ctx.RefreshGameData()
	itemsNearby := run.getItemsNearPosition(containerPos, itemCheckRadius)

	// If items already appeared, we're done
	if len(itemsNearby) > 0 {
		return
	}

	// Wait up to maxWaitTime for items to appear
	elapsed := initialDelay
	for elapsed < maxWaitTime {
		utils.Sleep(checkInterval)
		elapsed += checkInterval

		run.ctx.RefreshGameData()
		itemsNearby = run.getItemsNearPosition(containerPos, itemCheckRadius)
		if len(itemsNearby) > 0 {
			// Items appeared, we can continue
			return
		}
	}
}

// getItemsNearPosition returns items on the ground near a position
func (run LowerKurastChests) getItemsNearPosition(pos data.Position, radius int) []data.Item {
	var items []data.Item
	for _, itm := range run.ctx.Data.Inventory.ByLocation(item.LocationGround) {
		distance := pather.DistanceFromPoint(itm.Position, pos)
		if distance <= radius {
			items = append(items, itm)
		}
	}
	return items
}

// canUseTelekinesisForObject checks if Telekinesis can be used for the given object
// If ForceTelekinesis is enabled, ignores the global UseTelekinesis setting
func (run LowerKurastChests) canUseTelekinesisForObject(obj data.Object) bool {
	ctx := run.ctx
	forceTK := ctx.CharacterCfg.Game.LowerKurastChest.ForceTelekinesis
	
	// If ForceTelekinesis is enabled, ignore global UseTelekinesis setting
	// Otherwise, check global setting
	if !forceTK && !ctx.CharacterCfg.Character.UseTelekinesis {
		return false
	}
	
	if ctx.Data.PlayerUnit.Skills[skill.Telekinesis].Level == 0 {
		return false
	}
	if _, found := ctx.Data.KeyBindings.KeyBindingForSkill(skill.Telekinesis); !found {
		return false
	}
	// Telekinesis works on chests, super chests, and shrines
	return obj.IsChest() || obj.IsSuperChest() || obj.IsShrine()
}

// interactObjectWithForcedTK interacts with an object forcing Telekinesis usage
// This temporarily enables UseTelekinesis to bypass the global setting
func (run LowerKurastChests) interactObjectWithForcedTK(obj data.Object, isCompletedFn func() bool) error {
	ctx := run.ctx
	
	// Temporarily enable UseTelekinesis to force TK usage
	originalUseTK := ctx.CharacterCfg.Character.UseTelekinesis
	ctx.CharacterCfg.Character.UseTelekinesis = true
	defer func() {
		// Restore original setting
		ctx.CharacterCfg.Character.UseTelekinesis = originalUseTK
	}()
	
	// Now InteractObject will use Telekinesis
	return action.InteractObject(obj, isCompletedFn)
}

func isChestWithinBonfireRange(chest data.Object, bonfirePosition data.Position) bool {
	distance := pather.DistanceFromPoint(chest.Position, bonfirePosition)
	return distance >= minChestDistanceFromBonfire && distance <= maxChestDistanceFromBonfire
}

// isInteractableObject checks if an object can be opened/interacted with
// Includes: chests, super chests, breakable objects (barrels, urns, caskets, etc.), and other selectable containers
func isInteractableObject(o data.Object) bool {
	if !o.Selectable {
		return false
	}

	// Exclude special objects that shouldn't be opened
	if o.IsWaypoint() || o.IsPortal() || o.IsRedPortal() || o.IsShrine() || o.Name == object.Bank {
		return false
	}

	// Include chests and super chests
	if o.IsChest() || o.IsSuperChest() {
		return true
	}

	// Include breakable objects (barrels, urns, caskets, etc.)
	breakableObjects := []object.Name{
		object.Barrel, object.Urn2, object.Urn3, object.Casket,
		object.Casket5, object.Casket6, object.LargeUrn1, object.LargeUrn4,
		object.LargeUrn5, object.Crate, object.HollowLog, object.Sarcophagus,
	}
	if slices.Contains(breakableObjects, o.Name) {
		return true
	}

	// Include weapon racks and armor stands
	if o.Name == object.ArmorStandRight || o.Name == object.ArmorStandLeft ||
		o.Name == object.WeaponRackRight || o.Name == object.WeaponRackLeft {
		return true
	}

	// Include any other selectable object that might be a container
	// This catches corpses and other interactable objects
	// We check if it's not a door (doors are handled separately)
	if !o.IsDoor() {
		// Check if object description suggests it's a container
		desc := o.Desc()
		if desc.Name != "" {
			name := strings.ToLower(desc.Name)
			// Include objects with names suggesting containers
			if strings.Contains(name, "chest") || strings.Contains(name, "casket") || strings.Contains(name, "urn") ||
				strings.Contains(name, "barrel") || strings.Contains(name, "corpse") || strings.Contains(name, "body") ||
				strings.Contains(name, "sarcophagus") || strings.Contains(name, "log") || strings.Contains(name, "crate") {
				return true
			}
		}
	}

	return false
}
