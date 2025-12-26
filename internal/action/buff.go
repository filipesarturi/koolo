package action

import (
	"log/slog"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/data/state"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/utils"
)

// BuffIfRequired checks if rebuff is needed and moves to a safe position before buffing.
// - checks if rebuff is needed,
// - skips town,
// - if monsters are nearby and MoveToSafePositionForBuff is enabled, moves to a safe position first before buffing.
func BuffIfRequired() {
	ctx := context.Get()

	if !IsRebuffRequired() || ctx.Data.PlayerUnit.Area.IsTown() {
		return
	}

	const (
		safeDistanceForBuff = 20 // Minimum distance from monsters to safely buff
		maxSearchDistance   = 35 // Maximum distance to search for a safe position
	)

	// Check if MoveToSafePositionForBuff is enabled in config
	moveToSafePosition := ctx.CharacterCfg != nil && ctx.CharacterCfg.Character.MoveToSafePositionForBuff

	// Check if there are monsters close to the character
	closeMonsters := 0
	for _, m := range ctx.Data.Monsters {
		if ctx.PathFinder.DistanceFromMe(m.Position) < safeDistanceForBuff {
			closeMonsters++
		}
		if closeMonsters >= 1 {
			break
		}
	}

	// If monsters are nearby and feature is enabled, try to find and move to a safe position first
	if closeMonsters > 0 && moveToSafePosition {
		ctx.Logger.Debug("Monsters nearby, searching for safe position to buff...")

		safePos, found := FindSafePositionForBuff(safeDistanceForBuff, maxSearchDistance)
		if found && safePos != ctx.Data.PlayerUnit.Position {
			ctx.Logger.Debug("Moving to safe position for buffing",
				slog.Int("x", safePos.X),
				slog.Int("y", safePos.Y))

			// Move to the safe position before buffing
			err := MoveToCoords(safePos)
			if err != nil {
				ctx.Logger.Debug("Failed to move to safe buff position, will try to buff anyway",
					slog.String("error", err.Error()))
			}

			// Refresh data after moving
			ctx.RefreshGameData()
		} else if !found {
			ctx.Logger.Debug("No safe position found for buffing, skipping buff this time")
			return
		}
	} else if closeMonsters > 0 && !moveToSafePosition {
		// Feature disabled, use old behavior: don't buff if 2+ monsters nearby
		if closeMonsters >= 2 {
			return
		}
	}

	Buff()
}

// Buff keeps original timing / behavior:
// - no buff in town
// - no buff if done in last 30s
// - pre-CTA buffs
// - CTA (BO/BC) buffs
// - post-CTA class buffs
//
// The only extension is: if config.Character.UseSwapForBuffs is true,
// class buffs are cast from the weapon swap (offhand) instead of main hand.
func Buff() {
	ctx := context.Get()
	ctx.SetLastAction("Buff")

	if ctx.Data.PlayerUnit.Area.IsTown() || time.Since(ctx.LastBuffAt) < time.Second*30 {
		return
	}

	// Check if we're in loading screen
	if ctx.Data.OpenMenus.LoadingScreen {
		ctx.Logger.Debug("Loading screen detected. Waiting for game to load before buffing...")
		ctx.WaitForGameToLoad()
		utils.PingSleep(utils.Light, 400)
	}

	// --- Pre-CTA buffs (unchanged) ---
	preKeys := make([]data.KeyBinding, 0)
	for _, buff := range ctx.Char.PreCTABuffSkills() {
		kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(buff)
		if !found {
			ctx.Logger.Info("Key binding not found, skipping buff", slog.String("skill", buff.Desc().Name))
		} else {
			preKeys = append(preKeys, kb)
		}
	}

	if len(preKeys) > 0 {
		ctx.Logger.Debug("PRE CTA Buffing...")
		for _, kb := range preKeys {
			utils.Sleep(100)
			ctx.HID.PressKeyBinding(kb)
			utils.Sleep(180)
			ctx.HID.Click(game.RightButton, 640, 340)
			utils.Sleep(100)
		}
	}

	// --- CTA buffs (unchanged) ---
	buffCTA()

	// --- Post-CTA class buffs (with optional weapon swap) ---

	// Collect post-CTA buff keybindings as before.
	postKeys := make([]data.KeyBinding, 0)
	for _, buff := range ctx.Char.BuffSkills() {
		kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(buff)
		if !found {
			ctx.Logger.Info("Key binding not found, skipping buff", slog.String("skill", buff.Desc().Name))
		} else {
			postKeys = append(postKeys, kb)
		}
	}

	if len(postKeys) > 0 {
		// Read our new toggle from config:
		useSwapForBuffs := ctx.CharacterCfg != nil && ctx.CharacterCfg.Character.UseSwapForBuffs
		swappedForBuffs := false

		// Optionally swap to offhand (CTA / buff weapon) before class buffs.
		if useSwapForBuffs {
			ctx.Logger.Debug("Using weapon swap for class buff skills")
			if err := step.SwapToCTA(); err != nil {
				ctx.Logger.Warn("Failed to swap to offhand for buffs, using main weapon", "error", err)
			} else {
				swappedForBuffs = true
				utils.PingSleep(utils.Light, 400)
			}
		}

		ctx.Logger.Debug("Post CTA Buffing...")
		for _, kb := range postKeys {
			utils.Sleep(100)
			ctx.HID.PressKeyBinding(kb)
			utils.Sleep(180)
			ctx.HID.Click(game.RightButton, 640, 340)
			utils.Sleep(100)
		}

		// If we swapped, make sure we go back to main weapon.
		if swappedForBuffs {
			utils.PingSleep(utils.Light, 400)
			if err := step.SwapToMainWeapon(); err != nil {
				ctx.Logger.Warn("Failed to swap back to main weapon after buffs", "error", err)
			}
		}
	}

	utils.PingSleep(utils.Light, 200)
	buffsSuccessful := true
	if ctaFound(*ctx.Data) {
		if !ctx.Data.PlayerUnit.States.HasState(state.Battleorders) ||
			!ctx.Data.PlayerUnit.States.HasState(state.Battlecommand) {
			buffsSuccessful = false
			ctx.Logger.Warn("CTA buffs not detected after buffing, not updating LastBuffAt")
		}
	}

	if buffsSuccessful {
		ctx.LastBuffAt = time.Now()
	}
}

// IsRebuffRequired is left as original: 30s cooldown, CTA priority, and
// simple state-based checks for known buff skills.
func IsRebuffRequired() bool {
	ctx := context.Get()
	ctx.SetLastAction("IsRebuffRequired")

	// Don't buff if we are in town, or we did it recently
	// (prevents double buffing because of network lag).
	if ctx.Data.PlayerUnit.Area.IsTown() || time.Since(ctx.LastBuffAt) < time.Second*30 {
		return false
	}

	if ctaFound(*ctx.Data) &&
		(!ctx.Data.PlayerUnit.States.HasState(state.Battleorders) ||
			!ctx.Data.PlayerUnit.States.HasState(state.Battlecommand)) {
		return true
	}

	// TODO: Find a better way to convert skill to state
	buffs := ctx.Char.BuffSkills()
	for _, buff := range buffs {
		if _, found := ctx.Data.KeyBindings.KeyBindingForSkill(buff); found {
			if buff == skill.HolyShield && !ctx.Data.PlayerUnit.States.HasState(state.Holyshield) {
				return true
			}
			if buff == skill.FrozenArmor &&
				(!ctx.Data.PlayerUnit.States.HasState(state.Frozenarmor) &&
					!ctx.Data.PlayerUnit.States.HasState(state.Shiverarmor) &&
					!ctx.Data.PlayerUnit.States.HasState(state.Chillingarmor)) {
				return true
			}
			if buff == skill.EnergyShield && !ctx.Data.PlayerUnit.States.HasState(state.Energyshield) {
				return true
			}
			if buff == skill.CycloneArmor && !ctx.Data.PlayerUnit.States.HasState(state.Cyclonearmor) {
				return true
			}
		}
	}

	return false
}

// buffCTA handles the CTA weapon set: swap, cast BC/BO, swap back.
// This is kept exactly as in the original implementation.
func buffCTA() {
	ctx := context.Get()
	ctx.SetLastAction("buffCTA")

	if ctaFound(*ctx.Data) {
		ctx.Logger.Debug("CTA found: swapping weapon and casting Battle Command / Battle Orders")

		// Swap weapon only in case we don't have the CTA already equipped
		// (for example chicken previous game during buff stage).
		if _, found := ctx.Data.PlayerUnit.Skills[skill.BattleCommand]; !found {
			if err := step.SwapToCTA(); err != nil {
				ctx.Logger.Warn("Failed to swap to CTA, skipping CTA buffs", "error", err)
				return
			}
			utils.PingSleep(utils.Light, 150)
		}

		// Refresh data after swap to ensure we have current keybindings
		ctx.RefreshGameData()

		// Cast Battle Command
		if kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(skill.BattleCommand); found {
			ctx.HID.PressKeyBinding(kb)
			utils.Sleep(180)
			ctx.HID.Click(game.RightButton, 300, 300)
			utils.Sleep(100)
		} else {
			ctx.Logger.Warn("BattleCommand keybinding not found on CTA")
		}

		// Cast Battle Orders
		if kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(skill.BattleOrders); found {
			ctx.HID.PressKeyBinding(kb)
			utils.Sleep(180)
			ctx.HID.Click(game.RightButton, 300, 300)
			utils.Sleep(100)
		} else {
			ctx.Logger.Warn("BattleOrders keybinding not found on CTA")
		}

		utils.PingSleep(utils.Light, 400)

		// Always try to swap back to main weapon
		if err := step.SwapToMainWeapon(); err != nil {
			ctx.Logger.Warn("Failed to swap back to main weapon", "error", err)
		}
	}
}

// ctaFound checks if the player has a CTA-like item equipped (providing both BO and BC as NonClassSkill).
func ctaFound(d game.Data) bool {
	for _, itm := range d.Inventory.ByLocation(item.LocationEquipped) {
		_, boFound := itm.FindStat(stat.NonClassSkill, int(skill.BattleOrders))
		_, bcFound := itm.FindStat(stat.NonClassSkill, int(skill.BattleCommand))

		if boFound && bcFound {
			return true
		}
	}

	return false
}
