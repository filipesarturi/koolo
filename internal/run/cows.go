package run

import (
	"errors"
	"strings"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/utils"
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

	// Check if we already have the items in cube so we can skip.
	if a.hasWristAndBookInCube() {

		// Sell junk, refill potions, etc. (basically ensure space for getting the TP tome)
		action.PreRun(false)

		a.ctx.Logger.Info("Wrist Leg and Book found in cube")
		// Move to town if needed
		if !a.ctx.Data.PlayerUnit.Area.IsTown() {
			if err := action.ReturnTown(); err != nil {
				return err
			}
		}

		// Find and interact with stash
		bank, found := a.ctx.Data.Objects.FindOne(object.Bank)
		if !found {
			return errors.New("stash not found")
		}
		err := action.InteractObject(bank, func() bool {
			return a.ctx.Data.OpenMenus.Stash
		})
		if err != nil {
			return err
		}

		// Open cube and transmute Cow Level portal
		if err := action.CubeTransmute(); err != nil {
			return err
		}
		// If we dont have Wirstleg and Book in cube
	} else {
		// First, go to Rogue Encampment (Act 1) to check if portal already exists
		if err := action.WayPoint(area.RogueEncampment); err != nil {
			return err
		}

		// Check for Wirt's Leg on the ground in Act 1 before starting
		a.checkForLegOnGround()

		// Verify if cow portal already exists in town
		if a.hasCowPortal() {
			a.ctx.Logger.Info("Cow portal already exists, skipping leg collection")
		} else {
			// First clean up any extra tomes if needed
			err := a.cleanupExtraPortalTomes()
			if err != nil {
				return err
			}

			// Get Wrist leg
			err = a.getWirtsLeg()

			if err != nil {
				// If failed to get leg, return to town and check if portal was already opened
				a.ctx.Logger.Warn("Failed to get Wirt's Leg, checking if portal already exists")
				if err := action.WayPoint(area.RogueEncampment); err != nil {
					return err
				}

				if a.hasCowPortal() {
					a.ctx.Logger.Info("Cow portal already exists, continuing without leg")
				} else {
					return err
				}

				utils.Sleep(500)
				
				// Sell junk, refill potions, etc. (basically ensure space for getting the TP tome)
				action.PreRun(false)
			} else {
				utils.Sleep(500)
				// Sell junk, refill potions, etc. (basically ensure space for getting the TP tome)
				action.PreRun(false)

				utils.Sleep(500)
				err = a.preparePortal()
				if err != nil {
					return err
				}
			}
		}
	}
	// Make sure all menus are closed before interacting with cow portal
	if err := step.CloseAllMenus(); err != nil {
		return err
	}

	// Add a small delay to ensure everything is settled
	utils.Sleep(700)

	townPortal, found := a.ctx.Data.Objects.FindOne(object.PermanentTownPortal)
	if !found {
		return errors.New("cow portal not found")
	}

	err := action.InteractObject(townPortal, func() bool {
		return a.ctx.Data.AreaData.Area == area.MooMooFarm && a.ctx.Data.AreaData.IsInside(a.ctx.Data.PlayerUnit.Position)
	})
	if err != nil {
		return err
	}

	return action.ClearCurrentLevelCows(a.ctx.CharacterCfg.Game.Cows.OpenChests, data.MonsterAnyFilter())
}

func (a Cows) getWirtsLeg() error {
	if a.hasWirtsLeg() {
		a.ctx.Logger.Info("WirtsLeg found from previous game, we can skip")
		return nil
	}

	err := action.WayPoint(area.StonyField)
	if err != nil {
		return err
	}

	cainStone, found := a.ctx.Data.Objects.FindOne(object.CairnStoneAlpha)
	if !found {
		return errors.New("cain stones not found")
	}
	err = action.MoveToCoords(cainStone.Position)
	if err != nil {
		return err
	}
	action.ClearAreaAroundPlayer(10, data.MonsterAnyFilter())

	portal, found := a.ctx.Data.Objects.FindOne(object.PermanentTownPortal)
	if !found {
		return errors.New("tristram not found")
	}
	err = action.InteractObject(portal, func() bool {
		return a.ctx.Data.AreaData.Area == area.Tristram && a.ctx.Data.AreaData.IsInside(a.ctx.Data.PlayerUnit.Position)
	})
	if err != nil {
		return err
	}

	// Clear Tristram before getting the leg if option is enabled
	if a.ctx.CharacterCfg.Game.Cows.ClearTristram {
		a.ctx.Logger.Info("Clearing Tristram before getting Wirt's Leg")
		if err := a.clearTristram(); err != nil {
			return err
		}
	}

	wirtCorpse, found := a.ctx.Data.Objects.FindOne(object.WirtCorpse)
	if !found {
		return errors.New("wirt corpse not found")
	}

	if err := action.MoveToCoords(wirtCorpse.Position); err != nil {
		return err
	}

	err = action.InteractObject(wirtCorpse, func() bool {
		return a.hasWirtsLeg()
	})

	if err != nil {
		return err
	}

	// Return to town first
	if err := action.ReturnTown(); err != nil {
		return err
	}

	// After returning from Tristram, check if we got the leg
	if !a.hasWirtsLeg() {
		// If we didn't get the leg, check for it on the ground in town
		a.ctx.Logger.Info("Wirt's Leg not found after interacting with corpse, checking ground in town")
		a.checkForLegOnGround()
	}

	return nil
}

func (a Cows) preparePortal() error {
	err := action.WayPoint(area.RogueEncampment)
	if err != nil {
		return err
	}

	leg, found := a.ctx.Data.Inventory.Find("WirtsLeg",
		item.LocationStash,
		item.LocationInventory,
		item.LocationCube)
	if !found {
		return errors.New("WirtsLeg could not be found, portal cannot be opened")
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

	//Only 1 tome in inventory, buy one
	if tomeCount <= 1 {
		spareTome = data.Item{}
	}

	// If no spare tome found, buy a new one
	if spareTome.UnitID == 0 {
		err = action.BuyAtVendor(npc.Akara, action.VendorItemRequest{
			Item:     item.TomeOfTownPortal,
			Quantity: 1,
			Tab:      4,
		})
		if err != nil {
			return err
		}

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

	err = action.CubeAddItems(leg, spareTome)
	if err != nil {
		return err
	}

	return action.CubeTransmute()
}
func (a Cows) cleanupExtraPortalTomes() error {
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

		//Do not drop any tome if only 1 in inventory
		if len(protectedTomes)+len(unprotectedTomes) > 1 {
			// Only drop extra unprotected tomes if we have any
			if len(unprotectedTomes) > 0 {
				a.ctx.Logger.Info("Extra TomeOfTownPortal found - dropping it")
				for i := 0; i < len(unprotectedTomes); i++ {
					err := action.DropInventoryItem(unprotectedTomes[i])
					if err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
func (a Cows) hasWristAndBookInCube() bool {
	cubeItems := a.ctx.Data.Inventory.ByLocation(item.LocationCube)

	var hasLeg, hasTome bool
	for _, item := range cubeItems {
		if strings.EqualFold(string(item.Name), "WirtsLeg") {
			hasLeg = true
		}
		if strings.EqualFold(string(item.Name), "TomeOfTownPortal") {
			hasTome = true
		}
	}

	return hasLeg && hasTome
}

func (a Cows) hasWirtsLeg() bool {
	_, found := a.ctx.Data.Inventory.Find("WirtsLeg",
		item.LocationStash,
		item.LocationInventory,
		item.LocationCube)
	return found
}

func (a Cows) hasCowPortal() bool {
	// Check if cow portal already exists in Rogue Encampment
	portal, found := a.ctx.Data.Objects.FindOne(object.PermanentTownPortal)
	return found && portal != (data.Object{})
}

func (a Cows) clearTristram() error {
	a.ctx.Logger.Info("Clearing Tristram")
	return action.ClearCurrentLevel(false, data.MonsterAnyFilter())
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
		"area", currentArea.Area().Name,
		"x", legItem.Position.X,
		"y", legItem.Position.Y)

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
