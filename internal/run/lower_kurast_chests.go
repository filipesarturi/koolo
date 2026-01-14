package run

import (
	"slices"
	"sort"
	"strings"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/pather"
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
				run.ctx.Logger.Warn("Failed interacting with object",
					"name", run.ctx.Name,
					"object", closestObject.Name,
					"area", run.ctx.Data.PlayerUnit.Area.Area().Name,
					"error", err)
			} else {
				// Wait for items to drop from the opened container
				action.WaitForItemsAfterContainerOpen(closestObject.Position, closestObject)
			}

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
		pickupRadius = 20
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
				telekinesisRange := run.getTelekinesisRange()
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
			action.WaitForItemsAfterContainerOpen(o.Position, o)
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


// getTelekinesisRange returns the configured telekinesis range, defaulting to 23 if not set
func (run LowerKurastChests) getTelekinesisRange() int {
	ctx := run.ctx
	if ctx.CharacterCfg.Character.TelekinesisRange > 0 {
		return ctx.CharacterCfg.Character.TelekinesisRange
	}
	return 23 // Default: 23 tiles (~15.3 yards)
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
	
	// Object must be selectable to use Telekinesis
	if !obj.Selectable {
		return false
	}
	
	// Telekinesis works on all interactable objects: chests, super chests, shrines, breakables, etc.
	if obj.IsChest() || obj.IsSuperChest() || obj.IsShrine() {
		return true
	}
	
	// Include breakable objects (barrels, urns, caskets, logs, etc.)
	breakableObjects := []object.Name{
		object.Barrel, object.Urn2, object.Urn3, object.Casket,
		object.Casket5, object.Casket6, object.LargeUrn1, object.LargeUrn4,
		object.LargeUrn5, object.Crate, object.HollowLog, object.Sarcophagus,
	}
	for _, breakableName := range breakableObjects {
		if obj.Name == breakableName {
			return true
		}
	}
	
	// Include weapon racks and armor stands
	if obj.Name == object.ArmorStandRight || obj.Name == object.ArmorStandLeft ||
		obj.Name == object.WeaponRackRight || obj.Name == object.WeaponRackLeft {
		return true
	}
	
	// Include corpses, bodies, and other interactable containers
	if !obj.IsDoor() {
		desc := obj.Desc()
		if desc.Name != "" {
			name := strings.ToLower(desc.Name)
			if strings.Contains(name, "chest") || strings.Contains(name, "casket") || strings.Contains(name, "urn") ||
				strings.Contains(name, "barrel") || strings.Contains(name, "corpse") || strings.Contains(name, "body") ||
				strings.Contains(name, "sarcophagus") || strings.Contains(name, "log") || strings.Contains(name, "crate") ||
				strings.Contains(name, "wood") || strings.Contains(name, "rack") || strings.Contains(name, "stand") {
				return true
			}
		}
	}
	
	return false
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
