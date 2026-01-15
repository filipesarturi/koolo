package action

import (
	"fmt"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/event"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/pather"
	"github.com/hectorgimenez/koolo/internal/utils"
)

// getTelekinesisRange returns the configured telekinesis range, defaulting to 18 if not set
func getTelekinesisRange() int {
	ctx := context.Get()
	if ctx.CharacterCfg.Character.TelekinesisRange > 0 {
		return ctx.CharacterCfg.Character.TelekinesisRange
	}
	return 18 // Default: 18 tiles (~12 yards)
}

func InteractNPC(npc npc.ID) error {
	ctx := context.Get()
	ctx.SetLastAction("InteractNPC")

	pos, found := getNPCPosition(npc, ctx.Data)
	if !found {
		return fmt.Errorf("npc with ID %d not found", npc)
	}

	var err error
	for range 5 {
		err = MoveToCoords(pos)
		if err != nil {
			continue
		}

		err = step.InteractNPC(npc)
		if err != nil {
			continue
		}
		break
	}
	if err != nil {
		return err
	}

	event.Send(event.InteractedTo(event.Text(ctx.Name, ""), int(npc), event.InteractionTypeNPC))

	return nil
}

func InteractObject(o data.Object, isCompletedFn func() bool) error {
	ctx := context.Get()
	ctx.SetLastAction("InteractObject")

	startingArea := ctx.Data.PlayerUnit.Area

	// Check if Telekinesis can be used for this object
	canUseTK := canUseTelekinesisForObject(o)
	currentDistance := pather.DistanceFromPoint(ctx.Data.PlayerUnit.Position, o.Position)

	// If Telekinesis is available and we're already in range, skip movement
	telekinesisRange := getTelekinesisRange()
	if canUseTK && currentDistance <= telekinesisRange {
		ctx.Logger.Debug("Using Telekinesis from current position",
			"object", o.Name,
			"distance", currentDistance,
		)
		// Directly interact without moving
		var err error
		for range 5 {
			err = step.InteractObject(o, isCompletedFn)
			if err != nil {
				continue
			}
			break
		}
		if err != nil {
			ctx.Logger.Debug("InteractObject with Telekinesis failed",
				"object", o.Name,
				"error", err)
			return err
		}
	} else {
		// Normal movement-based interaction
		pos := o.Position
		distFinish := step.DistanceToFinishMoving

		// If Telekinesis is available, only move close enough for TK range
		if canUseTK {
			telekinesisRange := getTelekinesisRange()
			distFinish = telekinesisRange - 2 // Stop a bit before max range for safety
		}

		if ctx.Data.PlayerUnit.Area == area.RiverOfFlame && o.IsWaypoint() {
			pos = data.Position{X: 7800, Y: 5919}
			o.ID = 0
			// Special case for seals: we cant teleport directly to center. Interaction range is bigger then DistanceToFinishMoving so we modify it
		} else if strings.Contains(o.Desc().Name, "Seal") {
			distFinish = 10
		}

		var err error
		for range 5 {
			if o.IsWaypoint() && !ctx.Data.AreaData.Area.IsTown() && !canUseTK {
				err = MoveToCoords(pos)
				if err != nil {
					continue
				}
			} else {
				err = step.MoveTo(pos, step.WithDistanceToFinish(distFinish), step.WithIgnoreMonsters())
				if err != nil {
					continue
				}
			}

			err = step.InteractObject(o, isCompletedFn)
			if err != nil {
				continue
			}
			break
		}

		if err != nil {
			ctx.Logger.Debug("InteractObject step.InteractObject returned error",
				"object", o.Name,
				"error", err)
			return err
		}
	}

	// Refresh game data to get the final area state after interaction
	ctx.RefreshGameData()

	// If we transitioned to a new area (portal interaction), ensure collision data is loaded
	if ctx.Data.PlayerUnit.Area != startingArea {

		// Initial delay to allow server to fully sync area data
		utils.Sleep(500)
		ctx.RefreshGameData()

		// Wait up to 3 seconds for collision grid to load and be valid
		deadline := time.Now().Add(3 * time.Second)
		gridLoaded := false
		for time.Now().Before(deadline) {
			ctx.RefreshGameData()

			// Verify collision grid exists, is not nil, and has valid dimensions
			if ctx.Data.AreaData.Grid != nil &&
				ctx.Data.AreaData.Grid.CollisionGrid != nil &&
				len(ctx.Data.AreaData.Grid.CollisionGrid) > 0 {
				gridLoaded = true
				break
			}
			utils.Sleep(100)
		}

		if !gridLoaded {
			ctx.Logger.Warn("Collision grid did not load within timeout",
				"area", ctx.Data.PlayerUnit.Area,
				"timeout", "3s")
		}
	}

	return nil
}

// canUseTelekinesisForObject checks if Telekinesis can be used for the given object
func canUseTelekinesisForObject(obj data.Object) bool {
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

	// Object must be selectable to use Telekinesis
	if !obj.Selectable {
		return false
	}

	// Check if object is a valid Telekinesis target
	// Includes: waypoints, chests, shrines, stash, portals, and Diablo seals
	if obj.IsWaypoint() || obj.IsChest() || obj.IsSuperChest() || obj.IsShrine() ||
		obj.IsPortal() || obj.IsRedPortal() || obj.Name == object.Bank {
		return true
	}

	// Check for Diablo seals (Chaos Sanctuary)
	if strings.Contains(obj.Desc().Name, "Seal") {
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
	// Check if it's not a door (doors are handled separately)
	if !obj.IsDoor() {
		desc := obj.Desc()
		if desc.Name != "" {
			name := strings.ToLower(desc.Name)
			// Include objects with names suggesting containers or interactable objects
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

func InteractObjectByID(id data.UnitID, isCompletedFn func() bool) error {
	ctx := context.Get()
	ctx.SetLastAction("InteractObjectByID")

	o, found := ctx.Data.Objects.FindByID(id)
	if !found {
		return fmt.Errorf("object with ID %d not found", id)
	}

	return InteractObject(o, isCompletedFn)
}

func getNPCPosition(npc npc.ID, d *game.Data) (data.Position, bool) {
	monster, found := d.Monsters.FindOne(npc, data.MonsterTypeNone)
	if found {
		return monster.Position, true
	}

	n, found := d.NPCs.FindOne(npc)
	if !found {
		return data.Position{}, false
	}

	return data.Position{X: n.Positions[0].X, Y: n.Positions[0].Y}, true
}
