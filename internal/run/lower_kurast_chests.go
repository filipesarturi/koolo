package run

import (
	"slices"
	"strings"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
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

		// Open all objects in batch (handles waiting for items internally)
		if len(objects) > 0 {
			run.ctx.Logger.Debug("Opening objects in batch near bonfire",
				"objectsCount", len(objects),
			)
			_ = action.OpenContainersInBatch(objects)
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

		// Collect all interactable objects in this room
		var objectsInRoom []data.Object
		for _, o := range run.ctx.Data.Objects {
			if !r.IsInside(o.Position) {
				continue
			}

			if !isInteractableObject(o) || !o.Selectable {
				continue
			}

			objectsInRoom = append(objectsInRoom, o)
		}

		// Open all objects in batch (handles waiting for items internally)
		if len(objectsInRoom) > 0 {
			run.ctx.Logger.Debug("Opening objects in batch in room",
				"objectsCount", len(objectsInRoom),
			)
			_ = action.OpenContainersInBatch(objectsInRoom)
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
