package step

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/town"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
)

const (
	maxInteractionAttempts          = 5
	portalSyncDelay                 = 200
	maxPortalSyncAttempts           = 15
	telekinesisMaxInteractionRange  = 15 // Telekinesis effective range (conservative to ensure reliability)
	telekinesisInteractionAttempts  = 3
)

// InteractObject routes to packet or mouse implementation based on config
func InteractObject(obj data.Object, isCompletedFn func() bool) error {
	ctx := context.Get()

	// Check if Telekinesis can be used for this object
	if canUseTelekinesis(obj) {
		return InteractObjectTelekinesis(obj, isCompletedFn)
	}

	// For portals (blue/red), check if packet mode is enabled
	if (obj.IsPortal() || obj.IsRedPortal()) && ctx.CharacterCfg.PacketCasting.UseForTpInteraction {
		return InteractObjectPacket(obj, isCompletedFn)
	}

	// Default to mouse interaction
	return InteractObjectMouse(obj, isCompletedFn)
}

// canUseTelekinesis checks if Telekinesis can be used for the given object
func canUseTelekinesis(obj data.Object) bool {
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

	// Check if object is a valid Telekinesis target
	// Telekinesis works on: waypoints, chests, shrines, stashes, portals, seals
	if obj.IsWaypoint() || obj.IsChest() || obj.IsSuperChest() || obj.IsShrine() ||
		obj.IsPortal() || obj.IsRedPortal() || isStashObject(obj) {
		return true
	}

	// Check for Diablo seals (Chaos Sanctuary)
	if strings.Contains(obj.Desc().Name, "Seal") {
		return true
	}

	return false
}

// isStashObject checks if the object is a stash
func isStashObject(obj data.Object) bool {
	return obj.Name == object.Bank
}

// InteractObjectTelekinesis uses Telekinesis skill via HID to interact with objects from distance
// This method uses mouse simulation instead of packets for safety
func InteractObjectTelekinesis(obj data.Object, isCompletedFn func() bool) error {
	ctx := context.Get()
	ctx.SetLastStep("InteractObjectTelekinesis")

	startingArea := ctx.Data.PlayerUnit.Area
	interactionAttempts := 0

	// If there is no completion check, assume completed after successful interaction
	waitingForInteraction := false
	if isCompletedFn == nil {
		isCompletedFn = func() bool {
			return waitingForInteraction
		}
	}

	// For portals, determine expected area
	expectedArea := area.ID(0)
	if obj.IsRedPortal() {
		switch {
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.StonyField:
			expectedArea = area.Tristram
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.RogueEncampment:
			expectedArea = area.MooMooFarm
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.Harrogath:
			expectedArea = area.NihlathaksTemple
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.ArcaneSanctuary:
			expectedArea = area.CanyonOfTheMagi
		case obj.Name == object.BaalsPortal && ctx.Data.PlayerUnit.Area == area.ThroneOfDestruction:
			expectedArea = area.TheWorldstoneChamber
		case obj.Name == object.DurielsLairPortal && (ctx.Data.PlayerUnit.Area >= area.TalRashasTomb1 && ctx.Data.PlayerUnit.Area <= area.TalRashasTomb7):
			expectedArea = area.DurielsLair
		}
	} else if obj.IsPortal() {
		fromArea := ctx.Data.PlayerUnit.Area
		if !fromArea.IsTown() {
			expectedArea = town.GetTownByArea(fromArea).TownArea()
		} else {
			isCompletedFn = func() bool {
				return !ctx.Data.PlayerUnit.Area.IsTown() &&
					ctx.Data.AreaData.IsInside(ctx.Data.PlayerUnit.Position) &&
					len(ctx.Data.Objects) > 0
			}
		}
	}

	// Get Telekinesis keybinding
	tkKb, found := ctx.Data.KeyBindings.KeyBindingForSkill(skill.Telekinesis)
	if !found {
		ctx.Logger.Debug("Telekinesis keybinding not found, falling back to mouse interaction")
		return InteractObjectMouse(obj, isCompletedFn)
	}

	for !isCompletedFn() {
		ctx.PauseIfNotPriority()

		if interactionAttempts >= telekinesisInteractionAttempts {
			ctx.Logger.Debug("Telekinesis interaction failed, falling back to mouse interaction",
				slog.String("object", string(obj.Name)),
				slog.Int("attempts", interactionAttempts),
			)
			// Fallback to mouse interaction
			return InteractObjectMouse(obj, isCompletedFn)
		}

		ctx.RefreshGameData()

		// Check if we've transitioned to a new area (for portals)
		if ctx.Data.PlayerUnit.Area != startingArea {
			continue
		}

		// Find the object
		var o data.Object
		var found bool
		if obj.ID != 0 {
			o, found = ctx.Data.Objects.FindByID(obj.ID)
		} else {
			o, found = ctx.Data.Objects.FindOne(obj.Name)
		}

		if !found {
			return fmt.Errorf("object %v not found", obj)
		}

		// Check distance - Telekinesis has limited range
		distance := ctx.PathFinder.DistanceFromMe(o.Position)
		if distance > telekinesisMaxInteractionRange {
			ctx.Logger.Debug("Object too far for Telekinesis, falling back to mouse",
				slog.String("object", string(o.Name)),
				slog.Int("distance", distance),
			)
			return InteractObjectMouse(obj, isCompletedFn)
		}

		// For portals, check if it's ready
		if o.IsPortal() || o.IsRedPortal() {
			if o.Mode == mode.ObjectModeOperating {
				utils.Sleep(100)
				continue
			}
			if o.Mode != mode.ObjectModeOpened {
				utils.Sleep(100)
				continue
			}
		}

		// Select Telekinesis as right skill via HID
		ctx.HID.PressKeyBinding(tkKb)
		utils.Sleep(80)

		// Calculate screen position for the object
		objectX := o.Position.X - 2
		objectY := o.Position.Y - 2
		screenX, screenY := ui.GameCoordsToScreenCords(objectX, objectY)

		ctx.Logger.Debug("Using Telekinesis on object via HID",
			slog.String("object", string(o.Name)),
			slog.Int("distance", distance),
		)

		// Move mouse to object and right-click (Telekinesis)
		ctx.HID.MovePointer(screenX, screenY)
		utils.Sleep(50)
		ctx.HID.Click(game.RightButton, screenX, screenY)

		waitingForInteraction = true
		interactionAttempts++

		// Wait for interaction to complete
		utils.Sleep(350)

		// For portals with expected area, verify transition
		if expectedArea != 0 {
			utils.Sleep(500)
			for attempts := 0; attempts < maxPortalSyncAttempts; attempts++ {
				ctx.RefreshGameData()
				if ctx.Data.PlayerUnit.Area == expectedArea {
					if areaData, ok := ctx.Data.Areas[expectedArea]; ok {
						if areaData.IsInside(ctx.Data.PlayerUnit.Position) {
							if expectedArea.IsTown() {
								return nil
							}
							if len(ctx.Data.Objects) > 0 {
								return nil
							}
						}
					}
				}
				utils.Sleep(portalSyncDelay)
			}
			// Portal sync failed, retry
			ctx.Logger.Debug("Telekinesis portal sync failed, retrying",
				slog.String("expected_area", expectedArea.Area().Name),
				slog.String("current_area", ctx.Data.PlayerUnit.Area.Area().Name),
			)
			waitingForInteraction = false
		}
	}

	return nil
}

// InteractObjectMouse is the original mouse-based object interaction
func InteractObjectMouse(obj data.Object, isCompletedFn func() bool) error {
	interactionAttempts := 0
	mouseOverAttempts := 0
	waitingForInteraction := false
	currentMouseCoords := data.Position{}
	lastRun := time.Time{}

	ctx := context.Get()
	ctx.SetLastStep("InteractObjectMouse")

	startingArea := ctx.Data.PlayerUnit.Area

	// If there is no completion check, just assume the interaction is completed after clicking
	if isCompletedFn == nil {
		isCompletedFn = func() bool {
			return waitingForInteraction
		}
	}

	// For portals, we need to ensure proper area sync
	expectedArea := area.ID(0)
	if obj.IsRedPortal() {
		// For red portals, we need to determine the expected destination
		switch {
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.StonyField:
			expectedArea = area.Tristram
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.RogueEncampment:
			expectedArea = area.MooMooFarm
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.Harrogath:
			expectedArea = area.NihlathaksTemple
		case obj.Name == object.PermanentTownPortal && ctx.Data.PlayerUnit.Area == area.ArcaneSanctuary:
			expectedArea = area.CanyonOfTheMagi
		case obj.Name == object.BaalsPortal && ctx.Data.PlayerUnit.Area == area.ThroneOfDestruction:
			expectedArea = area.TheWorldstoneChamber
		case obj.Name == object.DurielsLairPortal && (ctx.Data.PlayerUnit.Area >= area.TalRashasTomb1 && ctx.Data.PlayerUnit.Area <= area.TalRashasTomb7):
			expectedArea = area.DurielsLair
		}
	} else if obj.IsPortal() {
		// For blue town portals, determine the town area based on current area
		fromArea := ctx.Data.PlayerUnit.Area
		if !fromArea.IsTown() {
			expectedArea = town.GetTownByArea(fromArea).TownArea()
		} else {
			// When using portal from town, we need to wait for any non-town area
			isCompletedFn = func() bool {
				return !ctx.Data.PlayerUnit.Area.IsTown() &&
					ctx.Data.AreaData.IsInside(ctx.Data.PlayerUnit.Position) &&
					len(ctx.Data.Objects) > 0
			}
		}
	}

	for !isCompletedFn() {
		ctx.PauseIfNotPriority()

		if interactionAttempts >= maxInteractionAttempts || mouseOverAttempts >= 20 {
			return fmt.Errorf("[%s] failed interacting with object [%v] in Area: [%s]", ctx.Name, obj.Name, ctx.Data.PlayerUnit.Area.Area().Name)
		}

		ctx.RefreshGameData()

		if ctx.Data.PlayerUnit.Area != startingArea {
			continue
		}

		interactionCooldown := utils.PingMultiplier(utils.Light, 200)
		if ctx.Data.PlayerUnit.Area.IsTown() {
			interactionCooldown = utils.PingMultiplier(utils.Medium, 400)
		}

		// Give some time before retrying the interaction
		if waitingForInteraction && time.Since(lastRun) < time.Duration(interactionCooldown)*time.Millisecond {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		var o data.Object
		var found bool
		if obj.ID != 0 {
			o, found = ctx.Data.Objects.FindByID(obj.ID)
			if !found {
				return fmt.Errorf("object %v not found", obj)
			}
		} else {
			o, found = ctx.Data.Objects.FindOne(obj.Name)
			if !found {
				return fmt.Errorf("object %v not found", obj)
			}
		}

		lastRun = time.Now()

		// Check portal states
		if o.IsPortal() || o.IsRedPortal() {
			// If portal is still being created, wait with escalating delay
			if o.Mode == mode.ObjectModeOperating {
				// Use retry escalation for portal opening waits
				utils.RetrySleep(interactionAttempts, float64(ctx.Data.Game.Ping), 100)
				continue
			}

			// Only interact when portal is fully opened
			if o.Mode != mode.ObjectModeOpened {
				utils.RetrySleep(interactionAttempts, float64(ctx.Data.Game.Ping), 100)
				continue
			}
		}

		if o.IsHovered && !utils.IsZeroPosition(currentMouseCoords) {
			ctx.HID.Click(game.LeftButton, currentMouseCoords.X, currentMouseCoords.Y)

			waitingForInteraction = true
			interactionAttempts++

			// For portals with expected area, we need to wait for proper area sync
			if expectedArea != 0 {
				utils.PingSleep(utils.Medium, 500)

				maxQuickChecks := 5
				for attempts := 0; attempts < maxQuickChecks; attempts++ {
					ctx.RefreshGameData()
					if ctx.Data.PlayerUnit.Area == expectedArea {
						if areaData, ok := ctx.Data.Areas[expectedArea]; ok {
							if areaData.IsInside(ctx.Data.PlayerUnit.Position) {
								if expectedArea.IsTown() {
									return nil // For town areas, we can return immediately
								}
								// For special areas, ensure we have proper object data loaded
								if len(ctx.Data.Objects) > 0 {
									return nil
								}
							}
						}
					}

					utils.PingSleep(utils.Light, 100)
				}

				// Area transition didn't happen yet - reset hover state to retry portal click
				ctx.Logger.Debug("Portal click may have failed - will retry",
					slog.String("expected_area", expectedArea.Area().Name),
					slog.String("current_area", ctx.Data.PlayerUnit.Area.Area().Name),
					slog.Int("interaction_attempt", interactionAttempts),
				)
				waitingForInteraction = false
				mouseOverAttempts = 0 // Reset to find portal again
			}
			continue
		} else {
			objectX := o.Position.X - 2
			objectY := o.Position.Y - 2
			distance := ctx.PathFinder.DistanceFromMe(o.Position)
			if distance > 15 {
				return fmt.Errorf("object is too far away: %d. Current distance: %d", o.Name, distance)
			}

			mX, mY := ui.GameCoordsToScreenCords(objectX, objectY)
			// In order to avoid the spiral (super slow and shitty) let's try to point the mouse to the top of the portal directly
			if mouseOverAttempts == 2 && o.IsPortal() {
				mX, mY = ui.GameCoordsToScreenCords(objectX-4, objectY-4)
			}

			x, y := utils.Spiral(mouseOverAttempts)
			currentMouseCoords = data.Position{X: mX + x, Y: mY + y}
			ctx.HID.MovePointer(mX+x, mY+y)
			mouseOverAttempts++
		}
	}

	return nil
}
