package action

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/difficulty"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/nip"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/town"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
	"github.com/lxn/win"
)

func IdentifyAll(skipIdentify bool) error {
	ctx := context.Get()
	ctx.SetLastAction("IdentifyAll")

	items := itemsToIdentify()

	ctx.Logger.Debug("Checking for items to identify...")
	if len(items) == 0 || skipIdentify {
		ctx.Logger.Debug("No items to identify...")
		return nil
	}

	shouldUseCain := ctx.CharacterCfg.Game.UseCainIdentify

	// Check conditions to force "skip Cain" even if UseCainIdentify is true
	_, isLevelingChar := ctx.Char.(context.LevelingCharacter)
	currentAct := ctx.Data.PlayerUnit.Area.Act()
	currentDifficulty := ctx.CharacterCfg.Game.Difficulty

	if isLevelingChar && currentAct == 4 && (currentDifficulty == difficulty.Nightmare || currentDifficulty == difficulty.Normal) {
		if shouldUseCain { // Only log this if Cain *would* have been used
			ctx.Logger.Debug("Forcing skip of Cain Identify: Leveling character in Act 4 Nightmare.")
		}
		shouldUseCain = false // Force Cain to be skipped
	}

	if shouldUseCain {
		ctx.Logger.Debug("Identifying all items with Cain...")

		const maxRetries = 3
		for attempt := 1; attempt <= maxRetries; attempt++ {
			// Close any open menus first
			step.CloseAllMenus()
			utils.PingSleep(utils.Light, 200)

			err := CainIdentify()
			if err != nil {
				ctx.Logger.Debug("Cain identification attempt failed", "attempt", attempt, "err", err)
				if attempt < maxRetries {
					utils.PingSleep(utils.Medium, 300)
					continue
				}
				ctx.Logger.Warn("Cain identification failed after all retries, protecting unidentified items")
				return nil
			}

			// Check if any items still need identification
			ctx.RefreshGameData()
			remaining := itemsToIdentify()
			if len(remaining) == 0 {
				ctx.Logger.Debug("All items successfully identified with Cain")
				return nil
			}

			ctx.Logger.Debug("Items remaining after Cain attempt", "attempt", attempt, "remaining", len(remaining))
			if attempt < maxRetries {
				utils.PingSleep(utils.Medium, 300)
			}
		}

		ctx.Logger.Debug("Some items not identified by Cain after retries, will use tome")
	}

	// --- Tome Identification Starts Here ---
	idTome, found := ctx.Data.Inventory.Find(item.TomeOfIdentify, item.LocationInventory)
	if !found {
		ctx.Logger.Warn("ID Tome not found, not identifying items")
		return nil
	}

	if st, statFound := idTome.FindStat(stat.Quantity, 0); !statFound || st.Value < len(items) {
		ctx.Logger.Info("Not enough ID scrolls, refilling...")
		VendorRefill(true, false)
	}

	ctx.Logger.Info(fmt.Sprintf("Identifying %d items...", len(items)))

	// Close all menus to prevent issues
	step.CloseAllMenus()
	for !ctx.Data.OpenMenus.Inventory {
		ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
		utils.PingSleep(utils.Critical, 1000) // Critical operation: Wait for inventory to open
	}

	for _, i := range items {
		identifyItem(idTome, i)
	}
	step.CloseAllMenus()

	return nil
}

func CainIdentify() error {
	ctx := context.Get()
	ctx.SetLastAction("CainIdentify")

	// Capture unidentified items before interaction
	unidentifiedIDs := make(map[data.UnitID]struct{})
	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if !i.Identified && i.Quality != item.QualityNormal && i.Quality != item.QualitySuperior {
			unidentifiedIDs[i.UnitID] = struct{}{}
		}
	}

	if len(unidentifiedIDs) == 0 {
		return nil
	}

	stayAwhileAndListen := town.GetTownByArea(ctx.Data.PlayerUnit.Area).IdentifyNPC()

	// Close any open menus first
	step.CloseAllMenus()
	utils.PingSleep(utils.Light, 200) // Light operation: Close menus before NPC interaction

	err := InteractNPC(stayAwhileAndListen)
	if err != nil {
		return fmt.Errorf("error interacting with Cain: %w", err)
	}

	// Verify menu opened
	menuWait := time.Now().Add(2 * time.Second)
	for time.Now().Before(menuWait) {
		ctx.PauseIfNotPriority()
		ctx.RefreshGameData()
		if ctx.Data.OpenMenus.NPCInteract {
			break
		}
		utils.PingSleep(utils.Light, 100) // Light operation: Polling for menu state
	}

	if !ctx.Data.OpenMenus.NPCInteract {
		return fmt.Errorf("NPC menu did not open")
	}

	// Select identify option
	ctx.HID.KeySequence(win.VK_HOME, win.VK_DOWN, win.VK_RETURN)

	// Wait for items to be identified (intelligent detection)
	identifyTimeout := time.Now().Add(5 * time.Second)
	for time.Now().Before(identifyTimeout) {
		ctx.PauseIfNotPriority()
		utils.PingSleep(utils.Light, 100) // Light operation: Polling for item state
		ctx.RefreshGameData()

		// Check if all previously unidentified items are now identified
		allIdentified := true
		for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if _, wasUnidentified := unidentifiedIDs[i.UnitID]; wasUnidentified {
				if !i.Identified {
					allIdentified = false
					break
				}
			}
		}

		if allIdentified {
			ctx.Logger.Debug("All items identified by Cain")
			break
		}
	}

	// Close menu if still open
	if ctx.Data.OpenMenus.NPCInteract {
		step.CloseAllMenus()
		utils.PingSleep(utils.Light, 200) // Wait for menu to close
	}

	return nil
}

func itemsToIdentify() (items []data.Item) {
	ctx := context.Get()
	ctx.SetLastAction("itemsToIdentify")

	for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
		if i.Identified || i.Quality == item.QualityNormal || i.Quality == item.QualitySuperior {
			continue
		}

		// Skip identifying items that fully match a rule when unid and we're not leveling
		_, isLevelingChar := ctx.Char.(context.LevelingCharacter)

		if !isLevelingChar {

			if _, result := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(i); result == nip.RuleResultFullMatch {
				continue
			}
		}

		items = append(items, i)
	}

	return
}

func HaveItemsToStashUnidentified() bool {
	ctx := context.Get()
	ctx.SetLastAction("HaveItemsToStashUnidentified")

	// Do not stash unid items when leveling
	_, isLevelingChar := ctx.Char.(context.LevelingCharacter)

	if !isLevelingChar {
		items := ctx.Data.Inventory.ByLocation(item.LocationInventory)
		for _, i := range items {

			if !i.Identified {
				if _, result := ctx.CharacterCfg.Runtime.Rules.EvaluateAll(i); result == nip.RuleResultFullMatch {
					return true
				}
			}
		}
	}

	return false
}

func identifyItem(idTome data.Item, i data.Item) {
	ctx := context.Get()
	screenPos := ui.GetScreenCoordsForItem(idTome)

	utils.PingSleep(utils.Medium, 500) // Medium operation: Prepare for right-click on tome
	ctx.HID.Click(game.RightButton, screenPos.X, screenPos.Y)
	utils.PingSleep(utils.Critical, 1000) // Critical operation: Wait for tome activation

	screenPos = ui.GetScreenCoordsForItem(i)

	ctx.HID.Click(game.LeftButton, screenPos.X, screenPos.Y)
	utils.PingSleep(utils.Critical, 350) // Critical operation: Wait for item identification
}
