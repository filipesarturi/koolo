package step

import (
	"errors"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
)

var ErrPlayerDied = errors.New("player is dead")

func OpenPortal() error {
	ctx := context.Get()
	ctx.SetLastStep("OpenPortal")

	// Portal cooldown: Prevent rapid portal creation during lag
	// Check last portal time to avoid spam during network delays
	if !ctx.LastPortalTick.IsZero() {
		timeSinceLastPortal := time.Since(ctx.LastPortalTick)
		minPortalCooldown := time.Duration(utils.PingMultiplier(utils.Critical, 1000)) * time.Millisecond
		if timeSinceLastPortal < minPortalCooldown {
			remainingCooldown := minPortalCooldown - timeSinceLastPortal
			ctx.Logger.Debug("Portal cooldown active, waiting",
				"cooldownRemaining", remainingCooldown)
			time.Sleep(remainingCooldown)
		}
	}

	lastRun := time.Time{}
	for {
		// IMPORTANT: Check for player death at the beginning of each loop iteration
		if ctx.Data.PlayerUnit.IsDead() && !ctx.Data.PlayerUnit.Area.IsTown() {
			return ErrPlayerDied // Player is dead, stop trying to open portal
		}

		// Pause the execution if the priority is not the same as the execution priority
		ctx.PauseIfNotPriority()

		_, found := ctx.Data.Objects.FindOne(object.TownPortal)
		if found {
			ctx.LastPortalTick = time.Now() // Update portal timestamp on success
			return nil                      // Portal found, success!
		}

		// Give some time to portal to popup before retrying...
		if time.Since(lastRun) < time.Millisecond*1000 {
			continue
		}

		usedKB := false
		tpItemFound := false
		var tpItem data.Item

		// Check if we should use scroll TP from belt
		if ctx.CharacterCfg.Inventory.UseScrollTPInBelt {
			_, found := ctx.BeltManager.GetFirstScrollTP()
			if found {
				// Get the TP scroll column from beltColumns or TPScrollBeltColumn
				tpScrollColumn := -1
				for i, col := range ctx.CharacterCfg.Inventory.BeltColumns {
					if strings.EqualFold(col, "tp") {
						tpScrollColumn = i
						break
					}
				}
				if tpScrollColumn < 0 {
					tpScrollColumn = ctx.CharacterCfg.Inventory.TPScrollBeltColumn
				}
				if tpScrollColumn >= 0 && tpScrollColumn <= 3 {
					binding := ctx.Data.KeyBindings.UseBelt[tpScrollColumn]
					ctx.HID.PressKeyBinding(binding)
					utils.PingSleep(utils.Medium, 250) // Medium operation: Wait for scroll activation
					ctx.HID.Click(game.RightButton, 300, 300)
					usedKB = true
					tpItemFound = true
					ctx.Logger.Debug("Using TP scroll from belt", "column", tpScrollColumn+1)
				}
			}
		}

		// If not using belt scroll, try tome (unless disabled)
		if !usedKB && !ctx.CharacterCfg.Inventory.DisableTomePortal {
			tpItem, tpItemFound = ctx.Data.Inventory.Find(item.TomeOfTownPortal, item.LocationInventory)
			if tpItemFound {
				if _, bindingFound := ctx.Data.KeyBindings.KeyBindingForSkill(skill.TomeOfTownPortal); bindingFound {
					ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.MustKBForSkill(skill.TomeOfTownPortal))
					utils.PingSleep(utils.Medium, 250) // Medium operation: Wait for tome activation
					ctx.HID.Click(game.RightButton, 300, 300)
					usedKB = true
				}
			}
		}

		// If still not used, try scroll from inventory
		if !usedKB && !tpItemFound {
			tpItem, tpItemFound = ctx.Data.Inventory.Find(item.ScrollOfTownPortal, item.LocationInventory)
		}

		// Try to tp through inventory using tome or scroll
		if !usedKB && tpItemFound {
			ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.Inventory)
			screenPos := ui.GetScreenCoordsForItem(tpItem)
			ctx.HID.Click(game.RightButton, screenPos.X, screenPos.Y)
			CloseAllMenus()
		}

		if !tpItemFound && !usedKB {
			return errors.New("no tp item, can't open portal")
		}
		lastRun = time.Now()
	}
}
