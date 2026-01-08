package step

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/pather"
	"github.com/hectorgimenez/koolo/internal/utils"
)

const (
	clickDelay                   = 25 * time.Millisecond
	spiralDelayFallback          = 15 * time.Millisecond  // Fallback delay if zero delay doesn't work
	spiralDelayAdaptiveThreshold = 5                      // Number of attempts before switching to fallback delay (reduced from 8 based on logs)
	hoverCheckDelay              = 8 * time.Millisecond   // Small delay after moving mouse before checking hover
	pickupClickDelay             = 100 * time.Millisecond // Delay after clicking item to check if pickup succeeded
	pickupTimeout                = 3 * time.Second
	telekinesisPickupMaxAttempts = 3
)

// getTelekinesisPickupMaxRange returns the configured telekinesis range for item pickup, defaulting to 23 if not set
func getTelekinesisPickupMaxRange() int {
	ctx := context.Get()
	if ctx.CharacterCfg.Character.TelekinesisRange > 0 {
		return ctx.CharacterCfg.Character.TelekinesisRange
	}
	return 23 // Default: 23 tiles (~15.3 yards)
}

var (
	maxInteractions      = 24 // 25 attempts since we start at 0
	ErrItemTooFar        = errors.New("item is too far away")
	ErrNoLOSToItem       = errors.New("no line of sight to item")
	ErrMonsterAroundItem = errors.New("monsters detected around item")
	ErrCastingMoving     = errors.New("char casting or moving")
)

// Pre-calculated spiral offsets cache
var (
	spiralOffsetsCache24   []struct{ x, y int } // Cache for maxInteractions = 24
	spiralOffsetsCache44   []struct{ x, y int } // Cache for maxInteractions = 44
	spiralCacheInitialized bool
)

// initSpiralCache pre-calculates all spiral offsets once for reuse
func initSpiralCache() {
	if spiralCacheInitialized {
		return
	}

	// Pre-calculate for maxInteractions = 24
	spiralOffsetsCache24 = make([]struct{ x, y int }, 24)
	for i := 0; i < 24; i++ {
		offsetX, offsetY := utils.ItemSpiral(i)
		spiralOffsetsCache24[i] = struct{ x, y int }{x: offsetX, y: offsetY}
	}

	// Pre-calculate for maxInteractions = 44
	spiralOffsetsCache44 = make([]struct{ x, y int }, 44)
	for i := 0; i < 44; i++ {
		offsetX, offsetY := utils.ItemSpiral(i)
		spiralOffsetsCache44[i] = struct{ x, y int }{x: offsetX, y: offsetY}
	}

	spiralCacheInitialized = true
}

// getSpiralOffsets returns pre-calculated spiral offsets for the given maxInteractions
func getSpiralOffsets(maxInteractions int) []struct{ x, y int } {
	initSpiralCache()
	if maxInteractions == 44 {
		return spiralOffsetsCache44
	}
	return spiralOffsetsCache24
}

const (
	waitForCharacterTimeout = 2 * time.Second
	characterCheckInterval  = 25 * time.Millisecond
)

// waitForCharacterReady waits for the character to finish casting or moving
func waitForCharacterReady(timeout time.Duration) error {
	ctx := context.Get()
	waitingStartTime := time.Now()

	for ctx.Data.PlayerUnit.Mode == mode.CastingSkill ||
		ctx.Data.PlayerUnit.Mode == mode.Running ||
		ctx.Data.PlayerUnit.Mode == mode.Walking ||
		ctx.Data.PlayerUnit.Mode == mode.WalkingInTown {
		if time.Since(waitingStartTime) > timeout {
			ctx.Logger.Warn("Timeout waiting for character to stop moving or casting, proceeding anyway")
			break
		}
		time.Sleep(characterCheckInterval)
		ctx.RefreshGameData()
	}
	return nil
}

// validatePickupPreconditions validates common preconditions for item pickup
// Returns an error if any precondition fails
func validatePickupPreconditions(it data.Item, maxDistance int, checkMonsters bool) error {
	ctx := context.Get()

	// Check for monsters if requested
	if checkMonsters && hasHostileMonstersNearby(it.Position) {
		return ErrMonsterAroundItem
	}

	// Validate line of sight
	if !ctx.PathFinder.LineOfSight(ctx.Data.PlayerUnit.Position, it.Position) {
		return ErrNoLOSToItem
	}

	// Check distance
	distance := ctx.PathFinder.DistanceFromMe(it.Position)
	if distance >= maxDistance {
		return fmt.Errorf("%w (%d): %s", ErrItemTooFar, distance, it.Desc().Name)
	}

	return nil
}

func PickupItem(it data.Item, itemPickupAttempt int) error {
	ctx := context.Get()
	ctx.SetLastStep("PickupItem")

	distance := ctx.PathFinder.DistanceFromMe(it.Position)
	hasLoS := ctx.PathFinder.LineOfSight(ctx.Data.PlayerUnit.Position, it.Position)

	// Check if Telekinesis can be used for this item (potions and gold)
	if canUseTelekinesisForItem(it) {
		ctx.Logger.Debug("Attempting item pickup via Telekinesis",
			slog.String("itemName", string(it.Desc().Name)),
			slog.String("itemQuality", it.Quality.ToString()),
			slog.Int("unitID", int(it.UnitID)),
			slog.Int("distance", distance),
			slog.Bool("hasLoS", hasLoS),
			slog.Int("attempt", itemPickupAttempt),
		)
		return PickupItemTelekinesis(it, itemPickupAttempt)
	}

	// Check if packet casting is enabled for item pickup
	if ctx.CharacterCfg.PacketCasting.UseForItemPickup {
		ctx.Logger.Debug("Attempting item pickup via packet method",
			slog.String("itemName", string(it.Desc().Name)),
			slog.Int("unitID", int(it.UnitID)),
			slog.Int("distance", distance),
			slog.Int("attempt", itemPickupAttempt),
		)
		return PickupItemPacket(it, itemPickupAttempt)
	}

	// Use mouse-based pickup (original implementation)
	ctx.Logger.Debug("Attempting item pickup via mouse method",
		slog.String("itemName", string(it.Desc().Name)),
		slog.Int("unitID", int(it.UnitID)),
		slog.Int("distance", distance),
		slog.Bool("hasLoS", hasLoS),
		slog.Int("attempt", itemPickupAttempt),
	)
	return PickupItemMouse(it, itemPickupAttempt)
}

// canUseTelekinesisForItem checks if Telekinesis can be used to pick up this item
func canUseTelekinesisForItem(it data.Item) bool {
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

// PickupItemTelekinesis uses Telekinesis skill via HID to pick up items from distance
// This method uses mouse simulation instead of packets for safety
func PickupItemTelekinesis(it data.Item, itemPickupAttempt int) error {
	ctx := context.Get()
	ctx.SetLastStep("PickupItemTelekinesis")

	const telekinesisTimeout = 10 * time.Second // Maximum time for this function
	startTime := time.Now()

	// Wait for the character to finish casting or moving
	if err := waitForCharacterReady(waitForCharacterTimeout); err != nil {
		return err
	}
	if time.Since(startTime) > telekinesisTimeout {
		return fmt.Errorf("telekinesis pickup timeout after %v", telekinesisTimeout)
	}

	// Check distance - Telekinesis has limited range
	telekinesisPickupMaxRange := getTelekinesisPickupMaxRange()
	distance := ctx.PathFinder.DistanceFromMe(it.Position)
	if distance > telekinesisPickupMaxRange {
		ctx.Logger.Debug("Item too far for Telekinesis pickup, falling back to normal pickup",
			slog.String("itemName", string(it.Desc().Name)),
			slog.Int("distance", distance),
			slog.Int("telekinesisMaxRange", telekinesisPickupMaxRange),
			slog.Int("unitID", int(it.UnitID)),
		)
		if ctx.CharacterCfg.PacketCasting.UseForItemPickup {
			return PickupItemPacket(it, itemPickupAttempt)
		}
		return PickupItemMouse(it, itemPickupAttempt)
	}

	// Validate line of sight (distance already checked above)
	hasLoS := ctx.PathFinder.LineOfSight(ctx.Data.PlayerUnit.Position, it.Position)
	if !hasLoS {
		ctx.Logger.Debug("No line of sight to item for Telekinesis",
			slog.String("itemName", string(it.Desc().Name)),
			slog.Int("unitID", int(it.UnitID)),
			slog.Int("distance", distance),
		)
		return ErrNoLOSToItem
	}

	// Get Telekinesis keybinding
	tkKb, found := ctx.Data.KeyBindings.KeyBindingForSkill(skill.Telekinesis)
	if !found {
		ctx.Logger.Debug("Telekinesis keybinding not found, falling back to normal pickup")
		return PickupItemMouse(it, itemPickupAttempt)
	}

	// Select Telekinesis as right skill via HID
	ctx.HID.PressKeyBinding(tkKb)
	utils.Sleep(80)

	targetItem := it

	for attempt := 0; attempt < telekinesisPickupMaxAttempts; attempt++ {
		// Check timeout
		if time.Since(startTime) > telekinesisTimeout {
			return fmt.Errorf("telekinesis pickup timeout after %v", telekinesisTimeout)
		}

		// Use timeout version to prevent infinite blocking
		if !ctx.PauseIfNotPriorityWithTimeout(2 * time.Second) {
			ctx.Logger.Debug("Priority wait timeout in telekinesis pickup, continuing...")
		}
		ctx.RefreshGameData()

		// Check if item still exists
		_, exists := findItemOnGround(targetItem.UnitID)
		if !exists {
			ctx.Logger.Info("Picked up item via Telekinesis",
				slog.String("itemName", string(targetItem.Desc().Name)),
				slog.String("itemQuality", targetItem.Quality.ToString()),
				slog.Int("unitID", int(targetItem.UnitID)),
				slog.Int("attempt", attempt+1),
				slog.Duration("duration", time.Since(startTime)),
			)
			ctx.CurrentGame.PickedUpItems[int(targetItem.UnitID)] = int(ctx.Data.PlayerUnit.Area.Area().ID)
			return nil
		}

		// Calculate screen position for the item
		screenX, screenY := ctx.PathFinder.GameCoordsToScreenCords(targetItem.Position.X, targetItem.Position.Y)
		currentDistance := ctx.PathFinder.DistanceFromMe(targetItem.Position)

		ctx.Logger.Debug("Using Telekinesis to pick up item via HID",
			slog.String("itemName", string(targetItem.Desc().Name)),
			slog.Int("unitID", int(targetItem.UnitID)),
			slog.Int("distance", currentDistance),
			slog.Int("attempt", attempt+1),
			slog.Int("screenX", screenX),
			slog.Int("screenY", screenY),
		)

		// Move mouse to item and right-click (Telekinesis)
		ctx.HID.MovePointer(screenX, screenY)
		utils.Sleep(50)
		ctx.HID.Click(game.RightButton, screenX, screenY)

		// Wait for pickup to complete
		utils.Sleep(350)

		// Check if item was picked up
		ctx.RefreshGameData()
		_, stillExists := findItemOnGround(targetItem.UnitID)
		if !stillExists {
			ctx.Logger.Info("Picked up item via Telekinesis",
				slog.String("itemName", string(targetItem.Desc().Name)),
				slog.String("itemQuality", targetItem.Quality.ToString()),
				slog.Int("unitID", int(targetItem.UnitID)),
				slog.Int("attempt", attempt+1),
				slog.Duration("duration", time.Since(startTime)),
			)
			ctx.CurrentGame.PickedUpItems[int(targetItem.UnitID)] = int(ctx.Data.PlayerUnit.Area.Area().ID)
			return nil
		}
	}

	// Telekinesis failed, fallback to normal pickup
	ctx.Logger.Debug("Telekinesis pickup failed after max attempts, falling back to normal pickup",
		slog.String("itemName", string(it.Desc().Name)),
		slog.Int("unitID", int(it.UnitID)),
		slog.Int("maxAttempts", telekinesisPickupMaxAttempts),
	)
	if ctx.CharacterCfg.PacketCasting.UseForItemPickup {
		return PickupItemPacket(it, itemPickupAttempt)
	}
	return PickupItemMouse(it, itemPickupAttempt)
}

func PickupItemMouse(it data.Item, itemPickupAttempt int) error {
	ctx := context.Get()
	ctx.SetLastStep("PickupItemMouse")

	const mousePickupTimeout = 15 * time.Second // Maximum time for this function
	funcStartTime := time.Now()

	// Wait for the character to finish casting or moving before proceeding
	if err := waitForCharacterReady(waitForCharacterTimeout); err != nil {
		return err
	}
	if time.Since(funcStartTime) > mousePickupTimeout {
		return fmt.Errorf("mouse pickup timeout after %v", mousePickupTimeout)
	}

	// Calculate base screen position for item
	baseX := it.Position.X - 1
	baseY := it.Position.Y - 1
	switch itemPickupAttempt {
	case 3:
		baseX = baseX + 1
	case 4:
		maxInteractions = 44
		baseY = baseY + 1
	case 5:
		maxInteractions = 44
		baseX = baseX - 1
		baseY = baseY - 1
	default:
		maxInteractions = 24
	}
	baseScreenX, baseScreenY := ctx.PathFinder.GameCoordsToScreenCords(baseX, baseY)

	// Calculate exact item position for first attempt (0, 0 offset)
	exactScreenX, exactScreenY := ctx.PathFinder.GameCoordsToScreenCords(it.Position.X, it.Position.Y)

	// Get pre-calculated spiral offsets (calculated once, reused for all pickups)
	spiralOffsets := getSpiralOffsets(maxInteractions)

	// Validate preconditions (monsters, LOS, distance)
	if err := validatePickupPreconditions(it, 7, true); err != nil {
		return err
	}

	ctx.Logger.Debug(fmt.Sprintf("Picking up: %s [%s]", it.Desc().Name, it.Quality.ToString()))

	// Track interaction state
	waitingForInteraction := time.Time{}
	spiralAttempt := 0
	targetItem := it
	lastMonsterCheck := time.Now()
	const monsterCheckInterval = 150 * time.Millisecond
	currentSpiralDelay := time.Duration(0) // Start with zero delay (adaptive)

	startTime := time.Now()

	for {
		// Check global function timeout
		if time.Since(funcStartTime) > mousePickupTimeout {
			return fmt.Errorf("mouse pickup timeout after %v", mousePickupTimeout)
		}

		// Use timeout version to prevent infinite blocking (reduced timeout for faster pickup)
		if !ctx.PauseIfNotPriorityWithTimeout(500 * time.Millisecond) {
			ctx.Logger.Debug("Priority wait timeout in mouse pickup, continuing...")
		}

		// Adaptive delay: switch to fallback delay after threshold attempts
		if spiralAttempt >= spiralDelayAdaptiveThreshold && currentSpiralDelay == 0 {
			currentSpiralDelay = spiralDelayFallback
			ctx.Logger.Debug("Switching to fallback spiral delay after threshold attempts",
				slog.Int("attempt", spiralAttempt),
				slog.Duration("delay", currentSpiralDelay),
			)
		}

		// Refresh game data after clicking to check if item was picked up
		if !waitingForInteraction.IsZero() {
			ctx.RefreshGameData()
		}

		// Periodic monster check
		if time.Since(lastMonsterCheck) > monsterCheckInterval {
			if hasHostileMonstersNearby(it.Position) {
				return ErrMonsterAroundItem
			}
			lastMonsterCheck = time.Now()
		}

		// Check if item still exists
		currentItem, exists := findItemOnGround(targetItem.UnitID)
		if !exists {
			ctx.Logger.Info("Picked up item via mouse",
				slog.String("itemName", string(targetItem.Desc().Name)),
				slog.String("itemQuality", targetItem.Quality.ToString()),
				slog.Int("unitID", int(targetItem.UnitID)),
				slog.Int("itemPickupAttempt", itemPickupAttempt),
				slog.Int("spiralAttempt", spiralAttempt),
				slog.Duration("duration", time.Since(startTime)),
			)

			ctx.CurrentGame.PickedUpItems[int(targetItem.UnitID)] = int(ctx.Data.PlayerUnit.Area.Area().ID)

			return nil // Success!
		}

		// Check timeout conditions
		if spiralAttempt > maxInteractions ||
			(!waitingForInteraction.IsZero() && time.Since(waitingForInteraction) > pickupTimeout) ||
			time.Since(startTime) > pickupTimeout {
			ctx.Logger.Debug("Mouse pickup timeout",
				slog.String("itemName", string(it.Desc().Name)),
				slog.Int("unitID", int(it.UnitID)),
				slog.Int("spiralAttempt", spiralAttempt),
				slog.Int("maxInteractions", maxInteractions),
				slog.Duration("elapsed", time.Since(startTime)),
				slog.Duration("waitingForInteraction", time.Since(waitingForInteraction)),
			)
			return fmt.Errorf("failed to pick up %s after %d attempts", it.Desc().Name, spiralAttempt)
		}

		// Calculate cursor position using pre-calculated spiral offsets
		var cursorX, cursorY int
		if spiralAttempt == 0 {
			// First attempt: use exact item position
			cursorX = exactScreenX
			cursorY = exactScreenY
		} else if spiralAttempt-1 < len(spiralOffsets) {
			// Use pre-calculated offset
			offset := spiralOffsets[spiralAttempt-1]
			cursorX = baseScreenX + offset.x
			cursorY = baseScreenY + offset.y
		} else {
			// Fallback: calculate on the fly (shouldn't happen in normal operation)
			offsetX, offsetY := utils.ItemSpiral(spiralAttempt - 1)
			cursorX = baseScreenX + offsetX
			cursorY = baseScreenY + offsetY
		}

		// Move cursor first, then refresh to get updated HoverData
		ctx.HID.MovePointer(cursorX, cursorY)

		// Small delay to allow game to update hover state
		if currentSpiralDelay > 0 {
			time.Sleep(currentSpiralDelay)
		} else {
			// Even with zero delay, give a tiny delay for hover detection
			time.Sleep(hoverCheckDelay)
		}

		// Refresh after moving mouse to get accurate HoverData
		ctx.RefreshGameData()

		// Click on item if mouse is hovering over (use cached HoverData)
		if currentItem.UnitID == ctx.Data.HoverData.UnitID {
			ctx.HID.Click(game.LeftButton, cursorX, cursorY)
			utils.PingSleep(utils.Light, int(pickupClickDelay.Milliseconds()))

			if waitingForInteraction.IsZero() {
				waitingForInteraction = time.Now()
			}
			continue
		}

		// Sometimes we got stuck because mouse is hovering a chest and item is in behind, it usually happens a lot
		// on Andariel, so we open it
		if isChestorShrineHovered() {
			ctx.HID.Click(game.LeftButton, cursorX, cursorY)
			time.Sleep(50 * time.Millisecond)
		}

		spiralAttempt++
	}
}

func isChestorShrineHovered() bool {
	ctx := context.Get()
	hoverData := ctx.Data.HoverData
	return hoverData.IsHovered && (hoverData.UnitType == 2 || hoverData.UnitType == 5)
}

func hasHostileMonstersNearby(pos data.Position) bool {
	ctx := context.Get()

	for _, monster := range ctx.Data.Monsters.Enemies() {
		if monster.Stats[stat.Life] <= 0 {
			continue
		}

		// Skip pets, mercenaries, and friendly NPCs (allies' summons)
		if monster.IsPet() || monster.IsMerc() || monster.IsGoodNPC() || monster.IsSkip() {
			continue
		}

		if pather.DistanceFromPoint(pos, monster.Position) <= 4 {
			return true
		}
	}
	return false
}

func findItemOnGround(targetID data.UnitID) (data.Item, bool) {
	ctx := context.Get()

	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationGround) {
		if i.UnitID == targetID {
			return i, true
		}
	}
	return data.Item{}, false
}
