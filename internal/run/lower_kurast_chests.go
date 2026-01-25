package run

import (
	"slices"
	"strings"
	"time"

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
	run.ctx.Logger.Info("Starting Lower Kurast Chest run",
		"openAllChests", run.ctx.CharacterCfg.Game.LowerKurastChest.OpenAllChests,
		"openRacks", run.ctx.CharacterCfg.Game.LowerKurastChest.OpenRacks,
	)

	// Use Waypoint to Lower Kurast
	err := action.WayPoint(area.LowerKurast)
	if err != nil {
		return err
	}

	// If OpenAllChests is enabled, clear the entire map using room-by-room approach
	if run.ctx.CharacterCfg.Game.LowerKurastChest.OpenAllChests {
		run.ctx.Logger.Info("Using OpenAllChests mode (room-by-room)")
		return run.clearAllInteractableObjects()
	}

	// Use bonfire-based approach for super chests (faster, targeted)
	run.ctx.Logger.Info("Using bonfire-based mode for super chests")

	// Get bonfires from cached map data
	var bonFirePositions []data.Position
	if areaData, ok := run.ctx.GameReader.GetData().Areas[area.LowerKurast]; ok {
		for _, obj := range areaData.Objects {
			if obj.Name == object.Name(160) { // SmallFire
				bonFirePositions = append(bonFirePositions, obj.Position)
			}
		}
	}

	run.ctx.Logger.Info("Bonfires found in Lower Kurast", "count", len(bonFirePositions))

	if len(bonFirePositions) == 0 {
		run.ctx.Logger.Warn("No bonfires found, falling back to OpenAllChests mode")
		return run.clearAllInteractableObjects()
	}

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

	// Otherwise, use the bonfire-based approach for superchests
	// Move to each of the bonfires one by one
	for bonfireIdx, bonfirePos := range bonFirePositions {
		// Move to the bonfire
		err = action.MoveToCoords(bonfirePos)
		if err != nil {
			return err
		}

		// Refresh game data to get updated object states
		run.ctx.RefreshGameData()

		// Find the interactable objects
		var objects []data.Object
		var totalObjectsNearBonfire int
		for _, o := range run.ctx.Data.Objects {
			// Check if object is within bonfire range
			if !isChestWithinBonfireRange(o, bonfirePos) {
				continue
			}
			totalObjectsNearBonfire++

			// Only include specific interactable objects that are still selectable
			if slices.Contains(interactableObjects, o.Name) && o.Selectable {
				objects = append(objects, o)
			}
		}

		// Open all objects in batch (handles waiting for items internally)
		if len(objects) > 0 {
			run.ctx.Logger.Info("Opening super chests near bonfire",
				"bonfireIndex", bonfireIdx+1,
				"totalBonfires", len(bonFirePositions),
				"chestsFound", len(objects),
				"totalObjectsNearBonfire", totalObjectsNearBonfire,
			)
			_ = action.OpenContainersInBatch(objects)
		} else {
			run.ctx.Logger.Info("No chests found near bonfire",
				"bonfireIndex", bonfireIdx+1,
				"totalBonfires", len(bonFirePositions),
				"totalObjectsNearBonfire", totalObjectsNearBonfire,
			)
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
// Optimized room traversal: collects ALL visible containers at each position and processes them in batches
func (run LowerKurastChests) clearAllInteractableObjects() error {
	const (
		pickupRadius  = 20
		nearbyRadius  = 40 // Increased radius to collect more containers per stop
	)

	startTime := time.Now()
	totalContainersOpened := 0
	processedContainerIDs := make(map[uint]bool) // Track already opened containers

	// Use optimized room traversal
	rooms := run.ctx.PathFinder.OptimizeRoomsTraverseOrder()

	run.ctx.Logger.Info("Starting OpenAllChests traversal",
		"totalRooms", len(rooms),
	)

	for roomIdx, r := range rooms {
		run.ctx.PauseIfNotPriority()

		// Move to room center
		path, _, found := run.ctx.PathFinder.GetClosestWalkablePath(r.GetCenter())
		if !found {
			continue
		}

		to := data.Position{
			X: path.To().X + run.ctx.Data.AreaOrigin.X,
			Y: path.To().Y + run.ctx.Data.AreaOrigin.Y,
		}

		err := action.MoveToCoords(to)
		if err != nil {
			continue
		}

		// Refresh to see containers in this area
		run.ctx.RefreshGameData()
		playerPos := run.ctx.Data.PlayerUnit.Position

		// Collect ALL visible containers within nearbyRadius that haven't been processed
		var containersToOpen []data.Object
		for _, o := range run.ctx.Data.Objects {
			if processedContainerIDs[uint(o.ID)] {
				continue // Already processed
			}

			if !isInteractableObject(o) || !o.Selectable {
				continue
			}

			// Include any container within nearbyRadius of player
			dist := pather.DistanceFromPoint(playerPos, o.Position)
			if dist <= nearbyRadius {
				containersToOpen = append(containersToOpen, o)
				processedContainerIDs[uint(o.ID)] = true // Mark as will be processed
			}
		}

		// Process all found containers in batch
		if len(containersToOpen) > 0 {
			run.ctx.Logger.Debug("Processing containers in area",
				"roomIndex", roomIdx+1,
				"containersFound", len(containersToOpen),
			)

			opened := action.OpenContainersInBatch(containersToOpen)
			totalContainersOpened += len(opened)

			// Pick up items after opening containers
			err = action.ItemPickup(pickupRadius)
			if err != nil {
				run.ctx.Logger.Debug("Failed to pickup items", "error", err)
			}
		}
	}

	run.ctx.Logger.Info("OpenAllChests mode completed",
		"totalContainersOpened", totalContainersOpened,
		"roomsTraversed", len(rooms),
		"duration", time.Since(startTime),
	)

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
