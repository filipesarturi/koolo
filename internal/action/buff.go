package action

import (
	"fmt"
	"log/slog"
	"sync"
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

// Track weapon swap failures to prevent infinite rebuff loops
var (
	weaponSwapFailures   = make(map[string]int) // per-character failure count
	lastSwapFailureTime  = make(map[string]time.Time)
	weaponSwapFailuresMu sync.Mutex
	maxSwapFailures      = 3                // max failures before cooldown
	swapFailureCooldown  = 60 * time.Second // cooldown after max failures
)

// skillToState maps buff skills to their corresponding player states for verification
var skillToState = map[skill.ID]state.State{
	skill.EnergyShield:  state.Energyshield,
	skill.FrozenArmor:   state.Frozenarmor,
	skill.ShiverArmor:   state.Shiverarmor,
	skill.ChillingArmor: state.Chillingarmor,
	skill.HolyShield:    state.Holyshield,
	skill.CycloneArmor:  state.Cyclonearmor,
	skill.BattleOrders:  state.Battleorders,
	skill.BattleCommand: state.Battlecommand,
	skill.Shout:         state.Shout,
	skill.Fade:          state.Fade,
	skill.BurstOfSpeed:  state.Quickness, // Burst of Speed state
	skill.Hurricane:     state.Hurricane,
	skill.BoneArmor:     state.Bonearmor,
	skill.ThunderStorm:  state.Thunderstorm,
}

// castBuffWithVerify casts a buff skill and verifies it was applied by checking the player state.
// Returns true if the buff was successfully applied, false otherwise.
func castBuffWithVerify(ctx *context.Status, kb data.KeyBinding, buffSkill skill.ID, expectedState state.State, maxRetries int) bool {
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			ctx.Logger.Debug("Retrying buff cast",
				slog.String("skill", buffSkill.Desc().Name),
				slog.Int("attempt", attempt+1),
				slog.Int("maxRetries", maxRetries),
			)
			// Small delay before retry
			utils.Sleep(200)
		}

		// Cast the buff
		utils.Sleep(100)
		ctx.HID.PressKeyBinding(kb)
		utils.Sleep(220)
		ctx.HID.Click(game.RightButton, 640, 340)
		utils.Sleep(120)

		// Wait a bit for the state to be applied (network delay)
		utils.PingSleep(utils.Light, 250)

		// Verify buff was applied
		ctx.RefreshGameData()
		if ctx.Data.PlayerUnit.States.HasState(expectedState) {
			if attempt > 0 {
				ctx.Logger.Debug("Buff applied after retry",
					slog.String("skill", buffSkill.Desc().Name),
					slog.Int("attempt", attempt+1),
				)
			}
			return true
		}
	}

	// All retries exhausted
	ctx.Logger.Warn("Failed to apply buff after retries",
		slog.String("skill", buffSkill.Desc().Name),
		slog.Int("attempts", maxRetries),
	)
	return false
}

// castBuff casts a buff skill without verification (for skills without verifiable states)
func castBuff(ctx *context.Status, kb data.KeyBinding) {
	utils.Sleep(100)
	ctx.HID.PressKeyBinding(kb)
	utils.Sleep(180)
	ctx.HID.Click(game.RightButton, 640, 340)
	utils.Sleep(100)
}

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

	// --- CTA buffs ---
	// Check if we need to use swap for class buffs
	useSwapForBuffs := ctx.CharacterCfg != nil && ctx.CharacterCfg.Character.UseSwapForBuffs
	// If useSwapForBuffs is active, don't swap back after CTA, we'll use CTA for class buffs
	ctaBuffsApplied := buffCTA(!useSwapForBuffs)

	// --- Post-CTA class buffs (with optional weapon swap) ---

	// Collect post-CTA buff skills and their keybindings
	type buffEntry struct {
		skill skill.ID
		kb    data.KeyBinding
	}
	postBuffs := make([]buffEntry, 0)
	for _, buff := range ctx.Char.BuffSkills() {
		kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(buff)
		if !found {
			ctx.Logger.Info("Key binding not found, skipping buff", slog.String("skill", buff.Desc().Name))
		} else {
			postBuffs = append(postBuffs, buffEntry{skill: buff, kb: kb})
		}
	}

	if len(postBuffs) > 0 {
		swappedForBuffs := false

		// Optionally swap to offhand (CTA / buff weapon) before class buffs.
		// Only swap if useSwapForBuffs is active AND we're not already on CTA
		if useSwapForBuffs {
			// Check if we're already on CTA (buffCTA might have left us there)
			ctx.RefreshGameData()
			_, alreadyOnCTA := ctx.Data.PlayerUnit.Skills[skill.BattleOrders]
			if !alreadyOnCTA {
				ctx.Logger.Debug("Using weapon swap for class buff skills")
				if err := step.SwapToCTA(); err != nil {
					ctx.Logger.Warn("Failed to swap to offhand for buffs, using main weapon", "error", err)
				} else {
					swappedForBuffs = true
					utils.PingSleep(utils.Light, 400)
				}
			} else {
				// We're already on CTA from buffCTA, no need to swap
				swappedForBuffs = true
				ctx.Logger.Debug("Already on CTA from buffCTA, skipping swap for class buffs")
			}
		}

		ctx.Logger.Debug("Post CTA Buffing...", slog.Int("buffCount", len(postBuffs)))
		buffTimeout := time.Now().Add(20 * time.Second)
		const maxRetries = 3

		for i, entry := range postBuffs {
			// Check timeout to prevent hanging
			if time.Now().After(buffTimeout) {
				ctx.Logger.Warn("Post CTA buffing timeout reached, skipping remaining buffs",
					slog.Int("completed", i),
					slog.Int("total", len(postBuffs)),
				)
				break
			}

			// Get skill name for logging
			skillName := entry.skill.Desc().Name
			if skillName == "" {
				skillName = fmt.Sprintf("SkillID(%d)", entry.skill)
			}

			ctx.Logger.Debug("Casting buff", slog.String("skill", skillName))

			// Check if this skill has a verifiable state for retry logic
			if expectedState, canVerify := skillToState[entry.skill]; canVerify {
				// Use verification with retry for skills with known states
				castBuffWithVerify(ctx, entry.kb, entry.skill, expectedState, maxRetries)
			} else {
				// Use simple cast for skills without verifiable states (summons, etc.)
				castBuff(ctx, entry.kb)
			}
		}
		ctx.Logger.Debug("Post CTA Buffing completed")

		// If we swapped, make sure we go back to main weapon.
		if swappedForBuffs {
			utils.PingSleep(utils.Light, 400)
			if err := step.SwapToMainWeapon(); err != nil {
				ctx.Logger.Warn("Failed to swap back to main weapon after buffs", "error", err)
			}
		}
	}

	utils.PingSleep(utils.Light, 200)

	// Check if CTA buffs were successfully applied
	ctaBuffsDetected := true
	if ctaFound(*ctx.Data) {
		if !ctx.Data.PlayerUnit.States.HasState(state.Battleorders) ||
			!ctx.Data.PlayerUnit.States.HasState(state.Battlecommand) {
			ctaBuffsDetected = false
			ctx.Logger.Warn("CTA buffs not detected after buffing")
		}
	}

	// Always update LastBuffAt to prevent infinite rebuff loops
	// Even if buffs failed, we wait before trying again
	ctx.LastBuffAt = time.Now()

	if !ctaBuffsApplied || !ctaBuffsDetected {
		ctx.Logger.Debug("Buff cycle completed with issues",
			"ctaBuffsApplied", ctaBuffsApplied,
			"ctaBuffsDetected", ctaBuffsDetected,
		)
	} else {
		ctx.Logger.Debug("Buff cycle completed successfully")
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

// buffCTA handles the CTA weapon set: swap, cast BC/BO, optionally swap back.
// If shouldSwapBack is false, leaves the character on CTA weapon set.
// Returns true if CTA buffs were successfully applied, false otherwise.
func buffCTA(shouldSwapBack bool) bool {
	ctx := context.Get()
	ctx.SetLastAction("buffCTA")

	if !ctaFound(*ctx.Data) {
		return true // No CTA, nothing to do
	}

	// Check if we're in cooldown due to repeated swap failures
	weaponSwapFailuresMu.Lock()
	failures := weaponSwapFailures[ctx.Name]
	lastFailure := lastSwapFailureTime[ctx.Name]
	weaponSwapFailuresMu.Unlock()

	if failures >= maxSwapFailures && time.Since(lastFailure) < swapFailureCooldown {
		ctx.Logger.Debug("Skipping CTA buffs due to repeated weapon swap failures",
			"failures", failures,
			"cooldownRemaining", swapFailureCooldown-time.Since(lastFailure),
		)
		return false
	}

	// Reset failure count if cooldown has passed
	if failures >= maxSwapFailures && time.Since(lastFailure) >= swapFailureCooldown {
		weaponSwapFailuresMu.Lock()
		weaponSwapFailures[ctx.Name] = 0
		weaponSwapFailuresMu.Unlock()
	}

	ctx.Logger.Debug("CTA found: swapping weapon and casting Battle Command / Battle Orders")

	// Swap weapon only in case we don't have the CTA already equipped
	// (for example chicken previous game during buff stage).
	if _, found := ctx.Data.PlayerUnit.Skills[skill.BattleCommand]; !found {
		if err := step.SwapToCTA(); err != nil {
			ctx.Logger.Warn("Failed to swap to CTA, skipping CTA buffs", "error", err)
			recordSwapFailure(ctx.Name)
			return false
		}
		utils.PingSleep(utils.Light, 150)
	}

	// Refresh data after swap to ensure we have current keybindings
	ctx.RefreshGameData()

	const maxCTARetries = 3

	// Cast Battle Command with verification and retry
	if kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(skill.BattleCommand); found {
		if !castBuffWithVerify(ctx, kb, skill.BattleCommand, state.Battlecommand, maxCTARetries) {
			ctx.Logger.Warn("Failed to apply Battle Command after retries")
		}
	} else {
		ctx.Logger.Warn("BattleCommand keybinding not found on CTA")
	}

	// Cast Battle Orders with verification and retry
	if kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(skill.BattleOrders); found {
		if !castBuffWithVerify(ctx, kb, skill.BattleOrders, state.Battleorders, maxCTARetries) {
			ctx.Logger.Warn("Failed to apply Battle Orders after retries")
		}
	} else {
		ctx.Logger.Warn("BattleOrders keybinding not found on CTA")
	}

	utils.PingSleep(utils.Light, 200)

	// Only swap back to main weapon if requested
	if shouldSwapBack {
		if err := step.SwapToMainWeapon(); err != nil {
			ctx.Logger.Warn("Failed to swap back to main weapon", "error", err)
			recordSwapFailure(ctx.Name)
			return false
		}
	}

	// Clear failure count on success
	weaponSwapFailuresMu.Lock()
	weaponSwapFailures[ctx.Name] = 0
	weaponSwapFailuresMu.Unlock()

	return true
}

// recordSwapFailure records a weapon swap failure for cooldown tracking
func recordSwapFailure(name string) {
	weaponSwapFailuresMu.Lock()
	defer weaponSwapFailuresMu.Unlock()
	weaponSwapFailures[name]++
	lastSwapFailureTime[name] = time.Now()
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
