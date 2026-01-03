package run

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/utils"
)

const (
	// Timeouts for critical operations
	portalInteractionTimeout = 10 * time.Second
	getLegTimeout            = 60 * time.Second
	preparePortalTimeout     = 30 * time.Second
	portalCheckTimeout       = 5 * time.Second
	areaLoadTimeout          = 15 * time.Second
)

type Cows struct {
	ctx *context.Status
}

func NewCows() *Cows {
	return &Cows{
		ctx: context.Get(),
	}
}

func (a Cows) Name() string {
	return string(config.CowsRun)
}

func (a Cows) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsQuestRun(parameters) {
		return SequencerError
	}
	if !a.ctx.Data.Quests[quest.Act5EveOfDestruction].Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (a Cows) Run(parameters *RunParameters) error {
	a.ctx.SetLastAction("CowsRun")
	a.ctx.Logger.Info("Starting Cows run")

	// Check for player death before starting
	if err := a.checkPlayerDeath(); err != nil {
		return err
	}

	// Step 1: Prepare portal (with timeout protection)
	portalReady, err := a.prepareCowPortal()
	if err != nil {
		return fmt.Errorf("failed to prepare cow portal: %w", err)
	}

	if !portalReady {
		a.ctx.Logger.Info("Cow portal not ready, skipping run")
		return nil
	}

	// Step 2: Enter Cow Level (with timeout and progress verification)
	if err := a.enterCowLevel(); err != nil {
		return fmt.Errorf("failed to enter cow level: %w", err)
	}

	// Step 3: Clear the level using optimized function
	return a.clearCowLevel()
}

// prepareCowPortal prepares the cow portal, returns true if portal is ready
func (a Cows) prepareCowPortal() (bool, error) {
	startTime := time.Now()

	// Check if we already have the items in cube so we can skip
	if a.hasWristAndBookInCube() {
		a.ctx.Logger.Info("Wrist Leg and Book found in cube, opening portal from stash")

		// Sell junk, refill potions, etc.
		if err := action.PreRun(false); err != nil {
			return false, fmt.Errorf("pre-run failed: %w", err)
		}

		// Check for player death
		if err := a.checkPlayerDeath(); err != nil {
			return false, err
		}

		// Move to town if needed
		if !a.ctx.Data.PlayerUnit.Area.IsTown() {
			if err := action.ReturnTown(); err != nil {
				return false, fmt.Errorf("failed to return to town: %w", err)
			}
		}

		// Find and interact with stash
		bank, found := a.ctx.Data.Objects.FindOne(object.Bank)
		if !found {
			return false, errors.New("stash not found")
		}

		if err := action.InteractObject(bank, func() bool {
			return a.ctx.Data.OpenMenus.Stash
		}); err != nil {
			return false, fmt.Errorf("failed to interact with stash: %w", err)
		}

		// Open cube and transmute Cow Level portal
		if err := action.CubeTransmute(); err != nil {
			return false, fmt.Errorf("failed to transmute portal: %w", err)
		}

		a.ctx.Logger.Info("Portal created from cube items", "elapsed", time.Since(startTime))
		return true, nil
	}

	// Need to get Wirt's Leg and prepare portal
	a.ctx.Logger.Info("Preparing cow portal from scratch")

	// Go to Rogue Encampment (Act 1) to check if portal already exists
	if err := action.WayPoint(area.RogueEncampment); err != nil {
		return false, fmt.Errorf("failed to waypoint to Rogue Encampment: %w", err)
	}

	// Check for Wirt's Leg on the ground in Act 1 before starting
	a.checkForLegOnGround()

	// Verify if cow portal already exists in town (with timeout)
	portalExists, err := a.checkCowPortalWithTimeout()
	if err != nil {
		a.ctx.Logger.Warn("Error checking for existing portal", "error", err)
	} else if portalExists {
		a.ctx.Logger.Info("Cow portal already exists, skipping leg collection")
		return true, nil
	}

	// Clean up any extra tomes if needed
	if err := a.cleanupExtraPortalTomes(); err != nil {
		a.ctx.Logger.Warn("Failed to cleanup extra portal tomes", "error", err)
	}

	// Get Wirt's Leg (with timeout)
	if err := a.getWirtsLegWithTimeout(); err != nil {
		// If failed to get leg, return to town and check if portal was already opened
		a.ctx.Logger.Warn("Failed to get Wirt's Leg, checking if portal already exists", "error", err)

		if err := action.WayPoint(area.RogueEncampment); err != nil {
			return false, fmt.Errorf("failed to return to Rogue Encampment: %w", err)
		}

		portalExists, checkErr := a.checkCowPortalWithTimeout()
		if checkErr != nil {
			return false, fmt.Errorf("failed to check portal after leg failure: %w (original error: %w)", checkErr, err)
		}

		if portalExists {
			a.ctx.Logger.Info("Cow portal already exists, continuing without leg")
			return true, nil
		}

		// No portal exists and couldn't get leg - this is a problem
		return false, fmt.Errorf("failed to get Wirt's Leg and no portal exists: %w", err)
	}

	// Sell junk, refill potions, etc.
	utils.Sleep(500)
	if err := action.PreRun(false); err != nil {
		return false, fmt.Errorf("pre-run failed: %w", err)
	}

	// Prepare portal (with timeout)
	utils.Sleep(500)
	if err := a.preparePortalWithTimeout(); err != nil {
		return false, fmt.Errorf("failed to prepare portal: %w", err)
	}

	a.ctx.Logger.Info("Portal preparation completed", "elapsed", time.Since(startTime))
	return true, nil
}

// enterCowLevel enters the cow level with timeout and progress verification
func (a Cows) enterCowLevel() error {
	startTime := time.Now()
	a.ctx.Logger.Info("Entering Cow Level")

	// Check for player death
	if err := a.checkPlayerDeath(); err != nil {
		return err
	}

	// Make sure all menus are closed before interacting with cow portal
	if err := step.CloseAllMenus(); err != nil {
		return fmt.Errorf("failed to close menus: %w", err)
	}

	// Small delay to ensure everything is settled
	utils.Sleep(700)
	a.ctx.RefreshGameData()

	// Find portal with timeout
	portalFound := false
	var townPortal data.Object
	deadline := time.Now().Add(portalCheckTimeout)

	for time.Now().Before(deadline) {
		a.ctx.RefreshGameData()
		portal, found := a.ctx.Data.Objects.FindOne(object.PermanentTownPortal)
		if found && portal != (data.Object{}) {
			townPortal = portal
			portalFound = true
			break
		}
		utils.Sleep(200)
	}

	if !portalFound {
		return errors.New("cow portal not found after timeout")
	}

	// Interact with portal (with timeout)
	interactionStart := time.Now()
	err := action.InteractObject(townPortal, func() bool {
		// Check timeout
		if time.Since(interactionStart) > portalInteractionTimeout {
			return true // Force exit on timeout
		}

		// Check if we're in the cow level
		return a.ctx.Data.AreaData.Area == area.MooMooFarm && a.ctx.Data.AreaData.IsInside(a.ctx.Data.PlayerUnit.Position)
	})

	if err != nil {
		return fmt.Errorf("failed to interact with portal: %w", err)
	}

	// Verify we actually entered the cow level (with timeout)
	if err := a.verifyCowLevelEntry(); err != nil {
		return fmt.Errorf("failed to verify cow level entry: %w", err)
	}

	a.ctx.Logger.Info("Successfully entered Cow Level", "elapsed", time.Since(startTime))
	return nil
}

// verifyCowLevelEntry verifies that we actually entered the cow level
func (a Cows) verifyCowLevelEntry() error {
	deadline := time.Now().Add(areaLoadTimeout)
	attempts := 0
	maxAttempts := 30

	for attempts < maxAttempts && time.Now().Before(deadline) {
		a.ctx.RefreshGameData()

		// Check for player death
		if err := a.checkPlayerDeath(); err != nil {
			return err
		}

		// Check if we're in the cow level
		if a.ctx.Data.AreaData.Area == area.MooMooFarm {
			if a.ctx.Data.AreaData.IsInside(a.ctx.Data.PlayerUnit.Position) {
				// Wait a bit more to ensure area is fully loaded
				utils.Sleep(500)
				a.ctx.RefreshGameData()
				return nil
			}
		}

		attempts++
		utils.Sleep(500)
	}

	return fmt.Errorf("failed to verify cow level entry after %d attempts", attempts)
}

// clearCowLevel clears the cow level using optimized function
func (a Cows) clearCowLevel() error {
	a.ctx.Logger.Info("Starting to clear Cow Level")

	// Check for player death before starting
	if err := a.checkPlayerDeath(); err != nil {
		return err
	}

	// Check if there are cows nearby before buffing
	// Clear area around player if cows are detected
	closeMonsters := a.ctx.Data.Monsters.Enemies(data.MonsterAnyFilter())
	hasCloseMonsters := false
	for _, m := range closeMonsters {
		if m.Stats[stat.Life] > 0 {
			distance := a.ctx.PathFinder.DistanceFromMe(m.Position)
			if distance <= 30 {
				hasCloseMonsters = true
				break
			}
		}
	}

	if hasCloseMonsters {
		a.ctx.Logger.Info("Cows detected near portal entrance, clearing area before buffing")
		if err := action.ClearAreaAroundPlayer(30, data.MonsterAnyFilter()); err != nil {
			a.ctx.Logger.Warn("Failed to clear area before buffing", "error", err)
		}
	}

	// Apply buff if configured (BuffOnNewArea)
	if a.ctx.CharacterCfg.Character.BuffOnNewArea {
		a.ctx.Logger.Debug("Applying buffs (BuffOnNewArea enabled)")
		action.Buff()
	}

	// Use optimized clear function (already handles public games, timeouts, etc.)
	return action.ClearCurrentLevelCows(a.ctx.CharacterCfg.Game.Cows.OpenChests, data.MonsterAnyFilter())
}

// checkPlayerDeath checks if the player is dead
func (a Cows) checkPlayerDeath() error {
	if a.ctx.Data.PlayerUnit.Area.IsTown() {
		return nil
	}

	if a.ctx.Data.PlayerUnit.IsDead() {
		return errors.New("player is dead")
	}
	return nil
}

// checkCowPortalWithTimeout checks if cow portal exists with timeout
func (a Cows) checkCowPortalWithTimeout() (bool, error) {
	deadline := time.Now().Add(portalCheckTimeout)
	attempts := 0
	maxAttempts := 10

	for attempts < maxAttempts && time.Now().Before(deadline) {
		a.ctx.RefreshGameData()
		if a.hasCowPortal() {
			return true, nil
		}
		attempts++
		utils.Sleep(500)
	}

	return false, nil
}

// getWirtsLegWithTimeout gets Wirt's Leg with timeout protection
func (a Cows) getWirtsLegWithTimeout() error {
	startTime := time.Now()
	a.ctx.Logger.Info("Getting Wirt's Leg")

	// Check if we already have it
	if a.hasWirtsLeg() {
		a.ctx.Logger.Info("Wirt's Leg found from previous game, skipping")
		return nil
	}

	// Check timeout at start
	if time.Since(startTime) > getLegTimeout {
		return fmt.Errorf("getWirtsLeg timeout before starting")
	}

	// Waypoint to Stony Field
	if err := action.WayPoint(area.StonyField); err != nil {
		return fmt.Errorf("failed to waypoint to Stony Field: %w", err)
	}

	// Check for player death
	if err := a.checkPlayerDeath(); err != nil {
		return err
	}

	// Find Cain Stone
	cainStone, found := a.ctx.Data.Objects.FindOne(object.CairnStoneAlpha)
	if !found {
		return errors.New("cain stones not found")
	}

	// Move to Cain Stone
	if err := action.MoveToCoords(cainStone.Position); err != nil {
		return fmt.Errorf("failed to move to Cain Stone: %w", err)
	}

	// Clear area around player
	action.ClearAreaAroundPlayer(10, data.MonsterAnyFilter())

	// Find Tristram portal
	portal, found := a.ctx.Data.Objects.FindOne(object.PermanentTownPortal)
	if !found {
		return errors.New("tristram portal not found")
	}

	// Interact with portal (with timeout check)
	interactionStart := time.Now()
	if err := action.InteractObject(portal, func() bool {
		// Check timeout
		if time.Since(startTime) > getLegTimeout || time.Since(interactionStart) > 10*time.Second {
			return true
		}
		return a.ctx.Data.AreaData.Area == area.Tristram && a.ctx.Data.AreaData.IsInside(a.ctx.Data.PlayerUnit.Position)
	}); err != nil {
		return fmt.Errorf("failed to interact with Tristram portal: %w", err)
	}

	// Verify we're in Tristram
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.ctx.RefreshGameData()
		if a.ctx.Data.AreaData.Area == area.Tristram {
			break
		}
		utils.Sleep(200)
	}

	if a.ctx.Data.AreaData.Area != area.Tristram {
		return errors.New("failed to enter Tristram")
	}

	// Clear Tristram before getting the leg if option is enabled
	if a.ctx.CharacterCfg.Game.Cows.ClearTristram {
		a.ctx.Logger.Info("Clearing Tristram before getting Wirt's Leg")
		if err := a.clearTristram(); err != nil {
			a.ctx.Logger.Warn("Failed to clear Tristram", "error", err)
			// Don't fail the whole operation if clearing fails
		}
	}

	// Check timeout before continuing
	if time.Since(startTime) > getLegTimeout {
		return fmt.Errorf("getWirtsLeg timeout before reaching corpse")
	}

	// Find Wirt's corpse
	wirtCorpse, found := a.ctx.Data.Objects.FindOne(object.WirtCorpse)
	if !found {
		return errors.New("wirt corpse not found")
	}

	// Move to corpse
	if err := action.MoveToCoords(wirtCorpse.Position); err != nil {
		return fmt.Errorf("failed to move to Wirt's corpse: %w", err)
	}

	// Interact with corpse (with timeout)
	corpseInteractionStart := time.Now()
	if err := action.InteractObject(wirtCorpse, func() bool {
		// Check timeout
		if time.Since(startTime) > getLegTimeout || time.Since(corpseInteractionStart) > 10*time.Second {
			return true
		}
		return a.hasWirtsLeg()
	}); err != nil {
		return fmt.Errorf("failed to interact with Wirt's corpse: %w", err)
	}

	// Return to town
	if err := action.ReturnTown(); err != nil {
		return fmt.Errorf("failed to return to town: %w", err)
	}

	// After returning from Tristram, check if we got the leg
	if !a.hasWirtsLeg() {
		// If we didn't get the leg, check for it on the ground in town
		a.ctx.Logger.Info("Wirt's Leg not found after interacting with corpse, checking ground in town")
		a.checkForLegOnGround()
	}

	// Final verification
	if !a.hasWirtsLeg() {
		return errors.New("failed to obtain Wirt's Leg after all attempts")
	}

	a.ctx.Logger.Info("Successfully obtained Wirt's Leg", "elapsed", time.Since(startTime))
	return nil
}

// getWirtsLeg is kept for backward compatibility but now uses the timeout version
func (a Cows) getWirtsLeg() error {
	return a.getWirtsLegWithTimeout()
}

// preparePortalWithTimeout prepares the portal with timeout protection
func (a Cows) preparePortalWithTimeout() error {
	startTime := time.Now()
	a.ctx.Logger.Info("Preparing cow portal")

	// Check timeout at start
	if time.Since(startTime) > preparePortalTimeout {
		return fmt.Errorf("preparePortal timeout before starting")
	}

	// Waypoint to Rogue Encampment
	if err := action.WayPoint(area.RogueEncampment); err != nil {
		return fmt.Errorf("failed to waypoint to Rogue Encampment: %w", err)
	}

	// Check for player death
	if err := a.checkPlayerDeath(); err != nil {
		return err
	}

	// Refresh inventory to get latest data
	a.ctx.RefreshInventory()

	// Find Wirt's Leg
	leg, found := a.ctx.Data.Inventory.Find("WirtsLeg",
		item.LocationStash,
		item.LocationInventory,
		item.LocationCube)
	if !found {
		return errors.New("Wirt's Leg could not be found, portal cannot be opened")
	}

	// Track if we found a usable spare tome
	var spareTome data.Item
	tomeCount := 0

	// Look for an existing spare tome (not in locked inventory slots)
	for _, itm := range a.ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if strings.EqualFold(string(itm.Name), item.TomeOfTownPortal) {
			tomeCount++
			if !action.IsInLockedInventorySlot(itm) {
				spareTome = itm
			}
		}
	}

	// Only 1 tome in inventory, buy one
	if tomeCount <= 1 {
		spareTome = data.Item{}
	}

	// If no spare tome found, buy a new one
	if spareTome.UnitID == 0 {
		a.ctx.Logger.Debug("No spare tome found, buying one from Akara")

		// Check timeout before buying
		if time.Since(startTime) > preparePortalTimeout {
			return fmt.Errorf("preparePortal timeout before buying tome")
		}

		if err := action.BuyAtVendor(npc.Akara, action.VendorItemRequest{
			Item:     item.TomeOfTownPortal,
			Quantity: 1,
			Tab:      4,
		}); err != nil {
			return fmt.Errorf("failed to buy TomeOfTownPortal: %w", err)
		}

		// Refresh inventory after buying
		a.ctx.RefreshInventory()

		// Find the newly bought tome (not in locked slots)
		for _, itm := range a.ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if strings.EqualFold(string(itm.Name), item.TomeOfTownPortal) && !action.IsInLockedInventorySlot(itm) {
				spareTome = itm
				break
			}
		}
	}

	if spareTome.UnitID == 0 {
		return errors.New("failed to obtain spare TomeOfTownPortal for cow portal")
	}

	// Check timeout before adding items to cube
	if time.Since(startTime) > preparePortalTimeout {
		return fmt.Errorf("preparePortal timeout before adding items to cube")
	}

	// Add items to cube
	if err := action.CubeAddItems(leg, spareTome); err != nil {
		return fmt.Errorf("failed to add items to cube: %w", err)
	}

	// Transmute
	if err := action.CubeTransmute(); err != nil {
		return fmt.Errorf("failed to transmute portal: %w", err)
	}

	a.ctx.Logger.Info("Successfully prepared cow portal", "elapsed", time.Since(startTime))
	return nil
}

// preparePortal is kept for backward compatibility but now uses the timeout version
func (a Cows) preparePortal() error {
	return a.preparePortalWithTimeout()
}
// cleanupExtraPortalTomes cleans up extra portal tomes to make space
func (a Cows) cleanupExtraPortalTomes() error {
	// Refresh inventory to get latest data
	a.ctx.RefreshInventory()

	// Only attempt cleanup if we don't have Wirt's Leg
	if _, hasLeg := a.ctx.Data.Inventory.Find("WirtsLeg", item.LocationStash, item.LocationInventory, item.LocationCube); !hasLeg {
		// Find all portal tomes, keeping track of which are in locked slots
		var protectedTomes []data.Item
		var unprotectedTomes []data.Item

		for _, itm := range a.ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if strings.EqualFold(string(itm.Name), item.TomeOfTownPortal) {
				if action.IsInLockedInventorySlot(itm) {
					protectedTomes = append(protectedTomes, itm)
				} else {
					unprotectedTomes = append(unprotectedTomes, itm)
				}
			}
		}

		// Do not drop any tome if only 1 in inventory
		if len(protectedTomes)+len(unprotectedTomes) > 1 {
			// Only drop extra unprotected tomes if we have any
			if len(unprotectedTomes) > 0 {
				a.ctx.Logger.Info("Extra TomeOfTownPortal found - dropping it", slog.Int("count", len(unprotectedTomes)))
				for i := 0; i < len(unprotectedTomes); i++ {
					if err := action.DropInventoryItem(unprotectedTomes[i]); err != nil {
						a.ctx.Logger.Warn("Failed to drop extra tome", "error", err)
						// Continue with other tomes even if one fails
					}
				}
				// Refresh inventory after dropping
				utils.Sleep(300)
				a.ctx.RefreshInventory()
			}
		}
	}
	return nil
}
// hasWristAndBookInCube checks if we have both Wirt's Leg and Tome of Town Portal in cube
func (a Cows) hasWristAndBookInCube() bool {
	a.ctx.RefreshInventory()
	cubeItems := a.ctx.Data.Inventory.ByLocation(item.LocationCube)

	var hasLeg, hasTome bool
	for _, item := range cubeItems {
		if strings.EqualFold(string(item.Name), "WirtsLeg") {
			hasLeg = true
		}
		if strings.EqualFold(string(item.Name), "TomeOfTownPortal") {
			hasTome = true
		}
		// Early exit if both found
		if hasLeg && hasTome {
			return true
		}
	}

	return hasLeg && hasTome
}

// hasWirtsLeg checks if we have Wirt's Leg in inventory, stash, or cube
func (a Cows) hasWirtsLeg() bool {
	a.ctx.RefreshInventory()
	_, found := a.ctx.Data.Inventory.Find("WirtsLeg",
		item.LocationStash,
		item.LocationInventory,
		item.LocationCube)
	return found
}

// hasCowPortal checks if cow portal already exists in current area
func (a Cows) hasCowPortal() bool {
	a.ctx.RefreshGameData()
	portal, found := a.ctx.Data.Objects.FindOne(object.PermanentTownPortal)
	if !found {
		return false
	}
	// Verify it's a valid portal object
	return portal != (data.Object{}) && portal.Selectable
}

// clearTristram clears Tristram with timeout protection
func (a Cows) clearTristram() error {
	a.ctx.Logger.Info("Clearing Tristram")

	// Check for player death before starting
	if err := a.checkPlayerDeath(); err != nil {
		return err
	}

	// Use optimized clear function with timeout (ClearCurrentLevel has built-in timeouts)
	if err := action.ClearCurrentLevel(false, data.MonsterAnyFilter()); err != nil {
		return fmt.Errorf("failed to clear Tristram: %w", err)
	}

	// Check for player death after clearing
	if err := a.checkPlayerDeath(); err != nil {
		return err
	}

	return nil
}

// checkForLegOnGround checks for Wirt's Leg on the ground in Act 1 town (Rogue Encampment) and picks it up if found
func (a Cows) checkForLegOnGround() {
	// Only check in Rogue Encampment (Act 1 town)
	currentArea := a.ctx.Data.PlayerUnit.Area
	if currentArea != area.RogueEncampment {
		return
	}

	// Skip if we already have the leg
	if a.hasWirtsLeg() {
		return
	}

	// Refresh game data to get latest items
	a.ctx.RefreshGameData()

	// Check for Wirt's Leg on the ground
	legFound := false
	var legItem data.Item
	for _, itm := range a.ctx.Data.Inventory.ByLocation(item.LocationGround) {
		if strings.EqualFold(string(itm.Name), "WirtsLeg") {
			legFound = true
			legItem = itm
			break
		}
	}

	if !legFound {
		return
	}

	a.ctx.Logger.Info("Found Wirt's Leg on the ground, attempting to pick it up",
		slog.String("area", currentArea.Area().Name),
		slog.Int("x", legItem.Position.X),
		slog.Int("y", legItem.Position.Y))

	// Move close to the item if needed
	distance := a.ctx.PathFinder.DistanceFromMe(legItem.Position)
	if distance > 5 {
		if err := action.MoveToCoords(legItem.Position); err != nil {
			a.ctx.Logger.Warn("Failed to move to Wirt's Leg on ground", "error", err)
			return
		}
		utils.Sleep(500)
		a.ctx.RefreshGameData()

		// Re-check if item still exists after moving
		legStillExists := false
		for _, itm := range a.ctx.Data.Inventory.ByLocation(item.LocationGround) {
			if itm.UnitID == legItem.UnitID {
				legStillExists = true
				legItem = itm
				break
			}
		}
		if !legStillExists {
			// Item might have been picked up or disappeared
			if a.hasWirtsLeg() {
				a.ctx.Logger.Info("Wirt's Leg was picked up during movement")
				return
			}
			return
		}
	}

	// Try to pick up the item using step.PickupItem for more direct control
	if err := step.PickupItem(legItem, 1); err != nil {
		a.ctx.Logger.Warn("Failed to pickup Wirt's Leg from ground", "error", err)
		// Fallback to ItemPickup if step.PickupItem fails
		if err := action.ItemPickup(10); err != nil {
			a.ctx.Logger.Warn("Fallback ItemPickup also failed", "error", err)
		}
	}

	// Verify we got it
	utils.Sleep(500)
	a.ctx.RefreshGameData()
	if a.hasWirtsLeg() {
		a.ctx.Logger.Info("Successfully picked up Wirt's Leg from the ground")
	}
}
