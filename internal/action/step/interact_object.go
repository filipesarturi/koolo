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
	maxInteractionAttempts         = 5
	portalSyncDelay                = 200
	maxPortalSyncAttempts          = 15
	telekinesisInteractionAttempts = 3
)

// getTelekinesisMaxInteractionRange returns the configured telekinesis range for object interaction, defaulting to 23 if not set
func getTelekinesisMaxInteractionRange() int {
	ctx := context.Get()
	if ctx.CharacterCfg.Character.TelekinesisRange > 0 {
		return ctx.CharacterCfg.Character.TelekinesisRange
	}
	return 18 // Default: 18 tiles (~12 yards)
}

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

	// Object must be selectable to use Telekinesis
	if !obj.Selectable {
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
	mouseOverAttempts := 0
	currentMouseCoords := data.Position{}

	// If there is no completion check, assume completed after successful interaction
	waitingForInteraction := false
	if isCompletedFn == nil {
		isCompletedFn = func() bool {
			return waitingForInteraction
		}
	}

	// For shrines, override completion check to return immediately after clicking
	// Shrines activate instantly, so no need to wait for selectable state change
	if obj.IsShrine() {
		originalIsCompletedFn := isCompletedFn
		isCompletedFn = func() bool {
			// If we've already clicked, consider it completed immediately
			if waitingForInteraction {
				return true
			}
			// Otherwise, use original completion check
			return originalIsCompletedFn()
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

	const maxMouseOverAttempts = 20

	for !isCompletedFn() {
		// For batch opening, skip priority pause to open containers rapidly
		// Only pause if we haven't clicked yet (still trying to hover)
		if interactionAttempts == 0 {
			ctx.PauseIfNotPriority()
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
		telekinesisMaxInteractionRange := getTelekinesisMaxInteractionRange()

		// If object is too far, fall back to mouse interaction
		if distance > telekinesisMaxInteractionRange {
			ctx.Logger.Debug("Object too far for Telekinesis, falling back to mouse",
				slog.String("object", string(o.Name)),
				slog.Int("distance", distance),
				slog.Int("tkRange", telekinesisMaxInteractionRange),
				slog.Int("spiralAttempts", mouseOverAttempts),
			)
			return InteractObjectMouse(obj, isCompletedFn)
		}

		// If object is very close to the range limit (within 2 tiles), reduce max attempts
		// to avoid wasting time on spiral when hover detection is unreliable at range edge
		effectiveMaxMouseOverAttempts := maxMouseOverAttempts
		if distance >= telekinesisMaxInteractionRange-2 {
			// Reduce attempts when at range edge - hover detection is less reliable
			effectiveMaxMouseOverAttempts = 8
			if mouseOverAttempts == 0 {
				ctx.Logger.Debug("Object near Telekinesis range limit, using reduced spiral attempts",
					slog.String("object", string(o.Name)),
					slog.Int("distance", distance),
					slog.Int("tkRange", telekinesisMaxInteractionRange),
					slog.Int("maxAttempts", effectiveMaxMouseOverAttempts),
				)
			}
		}

		if interactionAttempts >= telekinesisInteractionAttempts || mouseOverAttempts >= effectiveMaxMouseOverAttempts {
			ctx.Logger.Debug("Telekinesis interaction failed, falling back to mouse interaction",
				slog.String("object", string(obj.Name)),
				slog.Int("interactionAttempts", interactionAttempts),
				slog.Int("mouseOverAttempts", mouseOverAttempts),
				slog.Int("distance", distance),
				slog.Int("tkRange", telekinesisMaxInteractionRange),
				slog.Bool("isHovered", o.IsHovered),
			)
			// Fallback to mouse interaction
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

		// Check if object is hovered before clicking (similar to mouse interaction)
		if o.IsHovered && !utils.IsZeroPosition(currentMouseCoords) {
			if mouseOverAttempts > 0 {
				ctx.Logger.Debug("Telekinesis object hovered after spiral attempts",
					"object", o.Name,
					"spiralAttempts", mouseOverAttempts,
					"distance", distance,
					"tkRange", telekinesisMaxInteractionRange,
				)
			}
			// Select Telekinesis as right skill via HID
			ctx.HID.PressKeyBinding(tkKb)
			utils.Sleep(80)

			// Click on hovered object with Telekinesis
			ctx.HID.Click(game.RightButton, currentMouseCoords.X, currentMouseCoords.Y)

			waitingForInteraction = true
			interactionAttempts++

			// For containers (not portals/waypoints), return immediately after clicking
			// to enable rapid batch opening. The batch opening will wait for items after all containers are opened.
			// For portals and waypoints, we need to verify the interaction completed.
			if !obj.IsPortal() && !obj.IsRedPortal() && !obj.IsWaypoint() {
				// Special handling for stash: verify it opened using state polling instead of fixed delay
				if isStashObject(obj) {
					// Wait for stash to open with timeout and polling
					const stashOpenTimeout = 2000 // 2 seconds
					deadline := time.Now().Add(time.Duration(stashOpenTimeout) * time.Millisecond)
					ticker := time.NewTicker(50 * time.Millisecond)
					defer ticker.Stop()

					for time.Now().Before(deadline) {
						<-ticker.C
						ctx.RefreshGameData()
						if ctx.Data.OpenMenus.Stash {
							return nil
						}
					}
					// Stash did not open within timeout, but continue to allow retry in outer loop
					ctx.Logger.Debug("Stash did not open within timeout after TK click",
						"object", obj.Name,
						"timeout", stashOpenTimeout,
					)
					// Don't return error here, let the outer retry loop handle it
					continue
				}

				// For shrines and other containers, minimal delay to allow click to register
				// Shrines activate immediately, so no need to wait for selectable state change
				utils.Sleep(50)
				// Return immediately - no need to wait for object state change
				return nil
			}

			// For portals and waypoints, wait longer and verify interaction completed
			// Wait for interaction to complete (animation starts)
			utils.Sleep(350)

			// Refresh to check if object interaction completed
			ctx.RefreshGameData()

			// Re-find object to check if interaction completed
			var updatedObj data.Object
			var found bool
			if obj.ID != 0 {
				updatedObj, found = ctx.Data.Objects.FindByID(obj.ID)
			} else {
				updatedObj, found = ctx.Data.Objects.FindOne(obj.Name)
			}

			if found && !updatedObj.Selectable {
				return nil
			}

			if isCompletedFn() {
				return nil
			}
		} else {
			// Calculate screen position for the object using spiral pattern
			objectX := o.Position.X - 2
			objectY := o.Position.Y - 2
			mX, mY := ui.GameCoordsToScreenCords(objectX, objectY)

			// Use spiral pattern like mouse interaction for better accuracy
			x, y := utils.Spiral(mouseOverAttempts)
			currentMouseCoords = data.Position{X: mX + x, Y: mY + y}
			ctx.HID.MovePointer(mX+x, mY+y)
			mouseOverAttempts++

			if mouseOverAttempts > 1 {
				ctx.Logger.Debug("Telekinesis using spiral pattern",
					"object", o.Name,
					"spiralAttempt", mouseOverAttempts,
					"offset", fmt.Sprintf("(%d, %d)", x, y),
					"distance", distance,
					"tkRange", telekinesisMaxInteractionRange,
					"isHovered", o.IsHovered,
				)
			}

			// Small delay to allow hover detection
			utils.Sleep(50)

			// Refresh to get updated hover state
			ctx.RefreshGameData()

			// Re-find object to check if now hovered
			var updatedObj data.Object
			var found bool
			if obj.ID != 0 {
				updatedObj, found = ctx.Data.Objects.FindByID(obj.ID)
			} else {
				updatedObj, found = ctx.Data.Objects.FindOne(obj.Name)
			}

			if !found {
				return fmt.Errorf("object %v not found", obj)
			}

			// Update o with latest data
			o = updatedObj

			// Continue loop to check if hovered on next iteration
			continue
		}

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

// InteractObjectFast clicks on an object and returns immediately without waiting for completion.
// Used for batch container opening where we want to open multiple containers rapidly.
// Does NOT wait for hover - clicks directly on object position for speed.
// Returns true if the click was sent, false if object not found.
func InteractObjectFast(obj data.Object) bool {
	return InteractObjectFastInBatch(obj, false)
}

// InteractObjectFastInBatch clicks on an object rapidly for batch opening.
// tkAlreadySelected indicates if Telekinesis is already selected (optimization to avoid reselecting for each container).
// Returns true if the click was sent, false if object not found.
func InteractObjectFastInBatch(obj data.Object, tkAlreadySelected bool) bool {
	ctx := context.Get()
	ctx.SetLastStep("InteractObjectFastInBatch")

	// Try to use the object directly if it has valid ID and is selectable (optimization for batch mode)
	// Only refresh if we need to find the object or verify its state
	var o data.Object
	var found bool

	if obj.ID != 0 && obj.Selectable {
		// Try to use object directly without refresh for speed
		o = obj
		found = true
	} else {
		// Need to refresh and find object
		ctx.RefreshGameData()
		if obj.ID != 0 {
			o, found = ctx.Data.Objects.FindByID(obj.ID)
		} else {
			o, found = ctx.Data.Objects.FindOne(obj.Name)
		}
	}

	if !found {
		ctx.Logger.Debug("InteractObjectFastInBatch: object not found", "objName", obj.Name, "objID", obj.ID)
		return false
	}

	if !o.Selectable {
		ctx.Logger.Debug("InteractObjectFastInBatch: object not selectable", "objName", obj.Name, "objID", obj.ID)
		return false
	}

	// Calculate screen position
	objectX := o.Position.X - 2
	objectY := o.Position.Y - 2
	screenX, screenY := ui.GameCoordsToScreenCords(objectX, objectY)

	// Check if Telekinesis can be used
	useTK := canUseTelekinesis(obj)

	if useTK {
		// Only select TK if not already selected (optimization for batch mode)
		if !tkAlreadySelected {
			tkKb, tkFound := ctx.Data.KeyBindings.KeyBindingForSkill(skill.Telekinesis)
			if tkFound {
				ctx.HID.PressKeyBinding(tkKb)
				utils.Sleep(20) // Small delay to ensure TK is selected
			}
		}
		// Click immediately - TK is already selected
		ctx.HID.Click(game.RightButton, screenX, screenY)
		utils.Sleep(10) // Minimal delay - just enough for click to register
		return true
	}

	// Fallback to left click
	ctx.HID.Click(game.LeftButton, screenX, screenY)
	utils.Sleep(10) // Minimal delay - just enough for click to register
	return true
}
