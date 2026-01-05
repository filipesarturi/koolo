package action

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/data/state"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
)

// Track weapon swap failures to prevent infinite rebuff loops
var (
	weaponSwapFailures   = make(map[string]int) // per-character failure count
	lastSwapFailureTime  = make(map[string]time.Time)
	weaponSwapFailuresMu sync.Mutex
	maxSwapFailures      = 3                     // max failures before cooldown
	swapFailureCooldown  = 60 * time.Second      // cooldown after max failures
	memoryBuffApplied    = make(map[string]bool) // track if Memory buff was applied in first run
	memoryBuffInProgress = make(map[string]bool) // track if Memory buff is currently being applied
	memoryBuffAppliedMu  sync.Mutex
)

// ResetMemoryBuffFlag resets the Memory buff flag for a character when starting a new game
func ResetMemoryBuffFlag(characterName string) {
	memoryBuffAppliedMu.Lock()
	defer memoryBuffAppliedMu.Unlock()
	delete(memoryBuffApplied, characterName)
	delete(memoryBuffInProgress, characterName)
}

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
	// Get skill name for logging
	skillName := buffSkill.Desc().Name
	if skillName == "" {
		skillName = fmt.Sprintf("SkillID(%d)", buffSkill)
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			ctx.Logger.Debug("Retrying buff cast",
				slog.String("skill", skillName),
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
					slog.String("skill", skillName),
					slog.Int("attempt", attempt+1),
				)
			}
			return true
		}
	}

	// All retries exhausted
	ctx.Logger.Warn("Failed to apply buff after retries",
		slog.String("skill", skillName),
		slog.Int("skillID", int(buffSkill)),
		slog.String("expectedState", string(expectedState)),
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
// - if in town, tries to use Memory staff for Energy Shield and Armor buffs
func BuffIfRequired() {
	ctx := context.Get()

	// Don't buff if we're currently picking items to avoid interference
	if ctx.CurrentGame.IsPickingItems {
		ctx.Logger.Debug("Skipping buff check - item pickup in progress")
		return
	}

	// If in town, try to use Memory staff for buffs (only on first run)
	if ctx.Data.PlayerUnit.Area.IsTown() {
		// Check if Memory buff is enabled in config
		useMemoryBuff := ctx.CharacterCfg != nil && ctx.CharacterCfg.Character.UseMemoryBuff
		if useMemoryBuff {
			// Check if there's a corpse to recover first - priority over buffing
			if ctx.Data.Corpse.Found {
				ctx.Logger.Debug("Corpse found, skipping Memory buff - will be applied after corpse recovery")
				return
			}

			// Check if Memory buff was already applied or is in progress
			memoryBuffAppliedMu.Lock()
			alreadyApplied := memoryBuffApplied[ctx.Name]
			inProgress := memoryBuffInProgress[ctx.Name]
			memoryBuffAppliedMu.Unlock()

			// Skip if already applied or currently in progress
			if alreadyApplied || inProgress {
				if inProgress {
					ctx.Logger.Debug("Memory buff already in progress, skipping")
				} else if alreadyApplied {
					ctx.Logger.Debug("Memory buff already applied, skipping")
				}
				return
			}

			// Only apply Memory buff on first run
			// Check if Energy Shield or Armor buffs are needed
			needsMemoryBuffs := false
			buffSkills := ctx.Char.BuffSkills()
			for _, buffSkill := range buffSkills {
				if buffSkill == skill.EnergyShield {
					if !ctx.Data.PlayerUnit.States.HasState(state.Energyshield) {
						needsMemoryBuffs = true
						break
					}
				}
				if buffSkill == skill.FrozenArmor || buffSkill == skill.ShiverArmor || buffSkill == skill.ChillingArmor {
					if !ctx.Data.PlayerUnit.States.HasState(state.Frozenarmor) &&
						!ctx.Data.PlayerUnit.States.HasState(state.Shiverarmor) &&
						!ctx.Data.PlayerUnit.States.HasState(state.Chillingarmor) {
						needsMemoryBuffs = true
						break
					}
				}
			}

			if needsMemoryBuffs {
				// Mark as in progress before calling buffWithMemory to prevent concurrent calls
				memoryBuffAppliedMu.Lock()
				memoryBuffInProgress[ctx.Name] = true
				memoryBuffAppliedMu.Unlock()

				if err := buffWithMemory(); err != nil {
					ctx.Logger.Debug("Failed to use Memory staff for buffs", "error", err)
					// Reset in progress flag on failure so it can be retried
					memoryBuffAppliedMu.Lock()
					memoryBuffInProgress[ctx.Name] = false
					memoryBuffAppliedMu.Unlock()
				} else {
					// Mark Memory buff as applied and clear in progress flag
					memoryBuffAppliedMu.Lock()
					memoryBuffApplied[ctx.Name] = true
					memoryBuffInProgress[ctx.Name] = false
					memoryBuffAppliedMu.Unlock()
				}
			}
			return
		}
		// If Memory is disabled, continue with normal buff flow (don't return here)
	}

	if !IsRebuffRequired() {
		return
	}

	const (
		safeDistanceForBuff = 35 // Minimum distance from monsters to safely buff
		maxSearchDistance   = 55 // Maximum distance to search for a safe position
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
	// Skip movement if bot is stuck to avoid infinite loops
	if closeMonsters > 0 && moveToSafePosition && !ctx.CurrentGame.IsStuck {
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

	// Don't buff if we're currently picking items to avoid interference
	if ctx.CurrentGame.IsPickingItems {
		ctx.Logger.Debug("Skipping buff - item pickup in progress")
		return
	}

	// Allow buffing in town if Memory is disabled (Memory handles buffing in town when enabled)
	allowTownBuffing := ctx.CharacterCfg != nil && !ctx.CharacterCfg.Character.UseMemoryBuff
	if (!allowTownBuffing && ctx.Data.PlayerUnit.Area.IsTown()) || time.Since(ctx.LastBuffAt) < time.Second*30 {
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

	armorSkillAdded := false
	armorSkills := []skill.ID{skill.ChillingArmor, skill.ShiverArmor, skill.FrozenArmor}

	for _, buff := range ctx.Char.BuffSkills() {
		// Check if this is an armor skill
		isArmorSkill := buff == skill.FrozenArmor || buff == skill.ShiverArmor || buff == skill.ChillingArmor

		// Skip Energy Shield if it's already active (was applied with Memory and still active)
		if buff == skill.EnergyShield {
			if ctx.Data.PlayerUnit.States.HasState(state.Energyshield) {
				ctx.Logger.Debug("Energy Shield already active (from Memory), skipping from rebuff list")
				continue
			}
		}

		// Check if skill exists on character (has level > 0) with current weapon
		skillData, skillExists := ctx.Data.PlayerUnit.Skills[buff]
		hasSkill := skillExists && skillData.Level > 0

		kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(buff)
		if !found || !hasSkill {
			// If it's an armor skill and not available, try fallback
			if isArmorSkill && !armorSkillAdded {
				// Try first available armor skill as fallback (in order: ChillingArmor > ShiverArmor > FrozenArmor)
				fallbackFound := false
				for _, armorSkill := range armorSkills {
					if armorSkill == buff {
						continue // Skip the one that's not available
					}
					// Check if fallback skill exists on character with current weapon
					armorSkillData, armorSkillExists := ctx.Data.PlayerUnit.Skills[armorSkill]
					armorHasSkill := armorSkillExists && armorSkillData.Level > 0

					if armorKb, armorFound := ctx.Data.KeyBindings.KeyBindingForSkill(armorSkill); armorFound && armorHasSkill {
						ctx.Logger.Info("Armor skill not available, using fallback",
							slog.String("preferred", buff.Desc().Name),
							slog.String("fallback", armorSkill.Desc().Name))
						postBuffs = append(postBuffs, buffEntry{skill: armorSkill, kb: armorKb})
						armorSkillAdded = true
						fallbackFound = true
						break
					}
				}
				if !fallbackFound {
					ctx.Logger.Warn("No armor skill available as fallback", slog.String("preferred", buff.Desc().Name))
				}
			} else {
				if !hasSkill {
					ctx.Logger.Debug("Skill not learned on character, skipping buff", slog.String("skill", buff.Desc().Name))
				} else {
					ctx.Logger.Info("Key binding not found, skipping buff", slog.String("skill", buff.Desc().Name))
				}
			}
		} else {
			if isArmorSkill {
				armorSkillAdded = true
			}
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

		armorSkills := []skill.ID{skill.ChillingArmor, skill.ShiverArmor, skill.FrozenArmor}
		armorApplied := false

		for i, entry := range postBuffs {
			// Check timeout to prevent hanging
			if time.Now().After(buffTimeout) {
				ctx.Logger.Warn("Post CTA buffing timeout reached, skipping remaining buffs",
					slog.Int("completed", i),
					slog.Int("total", len(postBuffs)),
				)
				break
			}

			// Skip Energy Shield if it's already active (was applied with Memory on first run)
			if entry.skill == skill.EnergyShield {
				if ctx.Data.PlayerUnit.States.HasState(state.Energyshield) {
					ctx.Logger.Debug("Energy Shield already active, skipping cast")
					continue
				}
			}

			// Check if this is an armor skill
			isArmorSkill := entry.skill == skill.FrozenArmor || entry.skill == skill.ShiverArmor || entry.skill == skill.ChillingArmor

			// Skip armor skill if any armor buff is already active (was applied with Memory on first run)
			if isArmorSkill {
				if ctx.Data.PlayerUnit.States.HasState(state.Frozenarmor) ||
					ctx.Data.PlayerUnit.States.HasState(state.Shiverarmor) ||
					ctx.Data.PlayerUnit.States.HasState(state.Chillingarmor) {
					ctx.Logger.Debug("Armor buff already active, skipping cast")
					armorApplied = true // Mark as applied so we don't try fallback
					continue
				}
			}

			// Get skill name for logging
			skillName := entry.skill.Desc().Name
			if skillName == "" {
				skillName = fmt.Sprintf("SkillID(%d)", entry.skill)
			}

			ctx.Logger.Debug("Casting buff",
				slog.String("skill", skillName),
				slog.Int("skillID", int(entry.skill)))

			// Check if this skill has a verifiable state for retry logic
			buffSuccess := false
			if expectedState, canVerify := skillToState[entry.skill]; canVerify {
				// Use verification with retry for skills with known states
				buffSuccess = castBuffWithVerify(ctx, entry.kb, entry.skill, expectedState, maxRetries)
				if !buffSuccess {
					ctx.Logger.Warn("Buff verification failed",
						slog.String("skill", skillName),
						slog.Int("skillID", int(entry.skill)),
						slog.String("expectedState", string(expectedState)))
				}
			} else {
				// Use simple cast for skills without verifiable states (summons, etc.)
				castBuff(ctx, entry.kb)
				buffSuccess = true // Assume success for non-verifiable skills
				ctx.Logger.Debug("Buff cast (no verification)",
					slog.String("skill", skillName),
					slog.Int("skillID", int(entry.skill)))
			}

			// If armor skill failed and we haven't applied armor yet, try fallback
			if isArmorSkill && !buffSuccess && !armorApplied {
				ctx.Logger.Info("Armor skill failed, trying fallback",
					slog.String("failed", skillName))

				// Try first available armor skill as fallback
				fallbackFound := false
				// Refresh game data before trying fallback to ensure we have current weapon/skills state
				ctx.RefreshGameData()

				for _, armorSkill := range armorSkills {
					if armorSkill == entry.skill {
						continue // Skip the one that failed
					}

					// Check if fallback skill exists on character (refresh ensures current weapon state)
					armorSkillData, armorSkillExists := ctx.Data.PlayerUnit.Skills[armorSkill]
					armorHasSkill := armorSkillExists && armorSkillData.Level > 0

					if !armorHasSkill {
						continue // Skip if skill not learned
					}

					if armorKb, armorFound := ctx.Data.KeyBindings.KeyBindingForSkill(armorSkill); armorFound {
						armorName := armorSkill.Desc().Name
						if armorName == "" {
							armorName = fmt.Sprintf("SkillID(%d)", armorSkill)
						}
						ctx.Logger.Info("Trying armor skill fallback",
							slog.String("fallback", armorName))

						if expectedState, canVerify := skillToState[armorSkill]; canVerify {
							if castBuffWithVerify(ctx, armorKb, armorSkill, expectedState, maxRetries) {
								armorApplied = true
								fallbackFound = true
								ctx.Logger.Info("Armor skill fallback succeeded",
									slog.String("skill", armorName))
								break
							}
						} else {
							castBuff(ctx, armorKb)
							armorApplied = true
							fallbackFound = true
							ctx.Logger.Info("Armor skill fallback cast (no verification)",
								slog.String("skill", armorName))
							break
						}
					}
				}
				if !fallbackFound {
					ctx.Logger.Warn("No armor skill available as fallback")
				}
			} else if isArmorSkill && buffSuccess {
				armorApplied = true
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

	// Don't buff if we're currently picking items to avoid interference
	if ctx.CurrentGame.IsPickingItems {
		ctx.Logger.Debug("Skipping CTA buff - item pickup in progress")
		return false
	}

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

// buffWithMemory uses Memory staff from stash to cast Energy Shield and Armor buffs
// It swaps to weapon slot 1 (tab 2), equips Memory, casts buffs, then swaps back
func buffWithMemory() error {
	ctx := context.Get()
	ctx.SetLastAction("buffWithMemory")

	// Check if we're in town - need to be in town to access stash
	if !ctx.Data.PlayerUnit.Area.IsTown() {
		ctx.Logger.Debug("Not in town, skipping Memory buff")
		return fmt.Errorf("not in town")
	}

	// Check if Memory is already equipped (recovery scenario)
	ctx.RefreshGameData()
	memoryAlreadyEquipped := false
	for _, eqItem := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
		if eqItem.IsRuneword && eqItem.RunewordName == item.RunewordMemory {
			memoryAlreadyEquipped = true
			ctx.Logger.Warn("Memory staff already equipped, will try to restore original weapon")
			break
		}
	}

	// Find Memory staff in stash
	var memoryStaff data.Item
	var memoryFound bool
	var memoryTab int

	// Search all stash tabs for Memory
	for tab := 1; tab <= 4; tab++ {
		ctx.RefreshGameData()
		items := ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
		for _, itm := range items {
			// Check if item is on the current tab
			itemTab := 1
			if itm.Location.LocationType == item.LocationSharedStash {
				itemTab = itm.Location.Page + 1
			}
			if itemTab != tab {
				continue
			}

			// Check if it's a Memory runeword
			if itm.IsRuneword && itm.RunewordName == item.RunewordMemory {
				memoryStaff = itm
				memoryFound = true
				memoryTab = tab
				break
			}
		}
		if memoryFound {
			break
		}
	}

	// If Memory is already equipped, skip equipping and go straight to buffs
	if memoryAlreadyEquipped {
		ctx.Logger.Info("Memory staff already equipped, applying buffs directly")
		// Apply buffs and then restore weapon
		return buffWithMemoryAlreadyEquipped()
	}

	if !memoryFound {
		ctx.Logger.Debug("Memory staff not found in stash")
		return fmt.Errorf("memory staff not found")
	}

	ctx.Logger.Info("Found Memory staff in stash, starting buff sequence")

	// Step 1: Go to stash and open it
	bank, found := ctx.Data.Objects.FindOne(object.Bank)
	if !found {
		return fmt.Errorf("stash not found")
	}

	// Move to stash if needed
	if ctx.PathFinder.DistanceFromMe(bank.Position) > 10 {
		if err := MoveToCoords(bank.Position); err != nil {
			return fmt.Errorf("failed to move to stash: %w", err)
		}
	}

	// Open stash
	if err := OpenStash(); err != nil {
		return fmt.Errorf("failed to open stash: %w", err)
	}
	utils.PingSleep(utils.Medium, 500)

	// Step 2: Swap to weapon slot 1 (tab 2) to see what's currently equipped
	ctx.Logger.Debug("Swapping to weapon slot 1 (tab 2)")
	if err := step.SwapToSlot(1); err != nil {
		step.CloseAllMenus()
		return fmt.Errorf("failed to swap to weapon slot 1: %w", err)
	}
	utils.PingSleep(utils.Light, 300)

	// Refresh to get current equipped items
	ctx.RefreshGameData()

	// Step 3: Save the currently equipped weapon(s) in slot 1 before replacing with Memory
	originalLeftArm := GetEquippedItem(ctx.Data.Inventory, item.LocLeftArm)
	originalRightArm := GetEquippedItem(ctx.Data.Inventory, item.LocRightArm)

	ctx.Logger.Debug("Saving original weapon from slot 1",
		slog.String("leftArm", originalLeftArm.IdentifiedName),
		slog.String("rightArm", originalRightArm.IdentifiedName))

	// Step 4: Switch to the correct stash tab
	SwitchStashTab(memoryTab)
	utils.PingSleep(utils.Medium, 500)

	// Refresh to get updated item position
	ctx.RefreshGameData()

	// Find Memory again after tab switch
	memoryFound = false
	items := ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
	for _, itm := range items {
		if itm.IsRuneword && itm.RunewordName == item.RunewordMemory && itm.UnitID == memoryStaff.UnitID {
			memoryStaff = itm
			memoryFound = true
			break
		}
	}

	if !memoryFound {
		step.CloseAllMenus()
		return fmt.Errorf("memory staff not found after tab switch")
	}

	// Step 5: Equip Memory using SHIFT + Left Click
	ctx.Logger.Info("Equipping Memory staff with SHIFT + Click")
	screenPos := ui.GetScreenCoordsForItem(memoryStaff)
	ctx.HID.MovePointer(screenPos.X, screenPos.Y)
	utils.PingSleep(utils.Medium, 200)
	ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.ShiftKey)
	utils.PingSleep(utils.Medium, 800)

	// Verify Memory is equipped with retry
	ctx.RefreshGameData()
	memoryEquipped := false
	maxEquipRetries := 3
	for retry := 0; retry < maxEquipRetries; retry++ {
		for _, eqItem := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
			if eqItem.IsRuneword && eqItem.RunewordName == item.RunewordMemory {
				memoryEquipped = true
				break
			}
		}
		if memoryEquipped {
			break
		}
		if retry < maxEquipRetries-1 {
			ctx.Logger.Debug("Memory not equipped yet, retrying", slog.Int("retry", retry+1))
			// Try clicking again - refresh Memory position first
			ctx.RefreshGameData()
			items = ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
			for _, itm := range items {
				if itm.IsRuneword && itm.RunewordName == item.RunewordMemory && itm.UnitID == memoryStaff.UnitID {
					memoryStaff = itm
					break
				}
			}
			retryScreenPos := ui.GetScreenCoordsForItem(memoryStaff)
			ctx.HID.MovePointer(retryScreenPos.X, retryScreenPos.Y)
			utils.PingSleep(utils.Medium, 200)
			ctx.HID.ClickWithModifier(game.LeftButton, retryScreenPos.X, retryScreenPos.Y, game.ShiftKey)
			utils.PingSleep(utils.Medium, 800)
			ctx.RefreshGameData()
		}
	}

	if !memoryEquipped {
		step.CloseAllMenus()
		return fmt.Errorf("failed to equip Memory staff after %d attempts", maxEquipRetries)
	}
	ctx.Logger.Info("Memory staff successfully equipped")

	// Step 6: Close stash
	step.CloseAllMenus()
	utils.PingSleep(utils.Medium, 300)

	// Step 7: Apply Energy Shield and Armor skills
	ctx.Logger.Info("Applying Energy Shield and Armor buffs")

	// Get buff skills from character
	buffSkills := ctx.Char.BuffSkills()
	energyShieldApplied := false
	armorApplied := false

	// Determine preferred armor skill from config
	var preferredArmorSkill skill.ID
	preferredArmorSkillStr := ""
	if ctx.CharacterCfg != nil {
		preferredArmorSkillStr = ctx.CharacterCfg.Character.PreferredArmorSkill
	}

	switch preferredArmorSkillStr {
	case "frozen":
		preferredArmorSkill = skill.FrozenArmor
	case "shiver":
		preferredArmorSkill = skill.ShiverArmor
	case "chilling":
		preferredArmorSkill = skill.ChillingArmor
	default:
		// Auto: use first available in order ChillingArmor > ShiverArmor > FrozenArmor
		preferredArmorSkill = 0
	}

	// Try to apply Energy Shield first
	for _, buffSkill := range buffSkills {
		if buffSkill == skill.EnergyShield {
			kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(buffSkill)
			if !found {
				ctx.Logger.Debug("Key binding not found for Energy Shield")
				break
			}

			if expectedState, canVerify := skillToState[buffSkill]; canVerify {
				if castBuffWithVerify(ctx, kb, buffSkill, expectedState, 3) {
					energyShieldApplied = true
				}
			} else {
				castBuff(ctx, kb)
				energyShieldApplied = true
			}
			break
		}
	}

	// Apply armor skill (preferred or first available)
	armorSkills := []skill.ID{skill.ChillingArmor, skill.ShiverArmor, skill.FrozenArmor}

	// If preferred skill is set, try it first
	if preferredArmorSkill != 0 {
		// Check if preferred skill has keybinding available
		kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(preferredArmorSkill)
		if found {
			ctx.Logger.Info("Using preferred armor skill", slog.String("skill", preferredArmorSkill.Desc().Name))
			if expectedState, canVerify := skillToState[preferredArmorSkill]; canVerify {
				if castBuffWithVerify(ctx, kb, preferredArmorSkill, expectedState, 3) {
					armorApplied = true
				}
			} else {
				castBuff(ctx, kb)
				armorApplied = true
			}
		} else {
			ctx.Logger.Debug("Preferred armor skill not available, will try first available", slog.String("skill", preferredArmorSkill.Desc().Name))
		}
	}

	// If preferred skill wasn't applied, try first available
	if !armorApplied {
		for _, armorSkill := range armorSkills {
			// Skip if this was the preferred skill we already tried
			if preferredArmorSkill != 0 && armorSkill == preferredArmorSkill {
				continue
			}

			// Check if this armor skill has keybinding
			kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(armorSkill)
			if !found {
				continue
			}

			ctx.Logger.Info("Using available armor skill", slog.String("skill", armorSkill.Desc().Name))
			if expectedState, canVerify := skillToState[armorSkill]; canVerify {
				if castBuffWithVerify(ctx, kb, armorSkill, expectedState, 3) {
					armorApplied = true
					break
				}
			} else {
				castBuff(ctx, kb)
				armorApplied = true
				break
			}
		}
	}

	ctx.Logger.Info("Buffs applied",
		slog.Bool("energyShield", energyShieldApplied),
		slog.Bool("armor", armorApplied))

	// Step 8: Open stash again to restore original weapon
	utils.PingSleep(utils.Medium, 300)

	// Verify Memory is still equipped before proceeding
	ctx.RefreshGameData()
	memoryStillEquipped := false
	for _, eqItem := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
		if eqItem.IsRuneword && eqItem.RunewordName == item.RunewordMemory {
			memoryStillEquipped = true
			break
		}
	}

	if !memoryStillEquipped {
		ctx.Logger.Warn("Memory staff no longer equipped, may have been lost. Attempting recovery...")
		// Try to find Memory in inventory
		var memoryInInventory data.Item
		foundInInv := false
		for _, invItem := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if invItem.IsRuneword && invItem.RunewordName == item.RunewordMemory {
				memoryInInventory = invItem
				foundInInv = true
				break
			}
		}
		if foundInInv {
			ctx.Logger.Info("Found Memory in inventory, will put it back in stash")
			if err := OpenStash(); err == nil {
				SwitchStashTab(memoryTab)
				utils.PingSleep(utils.Medium, 300)
				ctx.RefreshGameData()
				recoveryScreenPos := ui.GetScreenCoordsForItem(memoryInInventory)
				ctx.HID.MovePointer(recoveryScreenPos.X, recoveryScreenPos.Y)
				utils.PingSleep(utils.Medium, 200)
				ctx.HID.ClickWithModifier(game.LeftButton, recoveryScreenPos.X, recoveryScreenPos.Y, game.CtrlKey)
				utils.PingSleep(utils.Medium, 800)
			}
		}
		step.CloseAllMenus()
		return fmt.Errorf("memory staff was lost during buff sequence")
	}

	if err := OpenStash(); err != nil {
		ctx.Logger.Warn("Failed to reopen stash, cannot restore original weapon", "error", err)
		// Try to close any open menus and retry
		step.CloseAllMenus()
		utils.PingSleep(utils.Medium, 500)
		if err := OpenStash(); err != nil {
			return fmt.Errorf("failed to reopen stash after retry: %w", err)
		}
	}
	utils.PingSleep(utils.Medium, 500)

	// Step 9: Restore original weapon(s) if they exist
	// SHIFT + Click on the original weapon in stash will automatically replace Memory
	if originalLeftArm.UnitID != 0 {
		ctx.Logger.Info("Restoring original weapon to slot 1", slog.String("weapon", originalLeftArm.IdentifiedName))

		// Find the original weapon in stash
		var originalWeapon data.Item
		foundOriginal := false
		var originalWeaponTab int

		// Search all stash tabs for the original weapon
		for tab := 1; tab <= 4; tab++ {
			SwitchStashTab(tab)
			utils.PingSleep(utils.Medium, 300)
			ctx.RefreshGameData()

			stashItems := ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
			for _, stashItem := range stashItems {
				// Check if item is on the current tab
				itemTab := 1
				if stashItem.Location.LocationType == item.LocationSharedStash {
					itemTab = stashItem.Location.Page + 1
				}
				if itemTab != tab {
					continue
				}

				if stashItem.UnitID == originalLeftArm.UnitID {
					originalWeapon = stashItem
					foundOriginal = true
					originalWeaponTab = tab
					break
				}
			}
			if foundOriginal {
				break
			}
		}

		if foundOriginal {
			// Make sure we're on the correct tab
			SwitchStashTab(originalWeaponTab)
			utils.PingSleep(utils.Medium, 300)
			ctx.RefreshGameData()

			// Find the weapon again after tab switch
			stashItems := ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
			for _, stashItem := range stashItems {
				if stashItem.UnitID == originalLeftArm.UnitID {
					originalWeapon = stashItem
					break
				}
			}

			// Equip original weapon using SHIFT + Click (this will automatically replace Memory)
			ctx.Logger.Info("Equipping original weapon with SHIFT + Click (will replace Memory)")
			screenPos = ui.GetScreenCoordsForItem(originalWeapon)
			ctx.HID.MovePointer(screenPos.X, screenPos.Y)
			utils.PingSleep(utils.Medium, 200)
			ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.ShiftKey)
			utils.PingSleep(utils.Medium, 800)

			// Verify Memory is back in stash and original weapon is equipped
			ctx.RefreshGameData()
			memoryInStash := false
			originalEquipped := false

			stashItems = ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
			for _, itm := range stashItems {
				if itm.IsRuneword && itm.RunewordName == item.RunewordMemory {
					memoryInStash = true
					break
				}
			}

			for _, eqItem := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
				if eqItem.UnitID == originalLeftArm.UnitID {
					originalEquipped = true
					break
				}
			}

			if memoryInStash && originalEquipped {
				ctx.Logger.Info("Original weapon restored and Memory returned to stash")
			} else {
				ctx.Logger.Warn("Verification failed, attempting recovery",
					slog.Bool("memoryInStash", memoryInStash),
					slog.Bool("originalEquipped", originalEquipped))

				// Recovery: if Memory is still equipped, try to find CTA in stash
				ctx.RefreshGameData()
				memoryStillEquippedCheck := false
				for _, eqItem := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
					if eqItem.IsRuneword && eqItem.RunewordName == item.RunewordMemory {
						memoryStillEquippedCheck = true
						break
					}
				}

				if memoryStillEquippedCheck {
					ctx.Logger.Info("Memory still equipped, searching for CTA in stash as fallback")
					if err := restoreWeaponFromCTA(); err != nil {
						ctx.Logger.Error("Failed to restore weapon from CTA", "error", err)
						step.CloseAllMenus()
						return fmt.Errorf("failed to restore weapon: memory still equipped and CTA not found")
					}
				}
			}
		} else {
			ctx.Logger.Warn("Original weapon not found in stash, searching for CTA as fallback")
			// If we don't know the original weapon, try to use CTA from stash
			if err := restoreWeaponFromCTA(); err != nil {
				ctx.Logger.Error("Failed to restore weapon from CTA", "error", err)
				step.CloseAllMenus()
				return fmt.Errorf("failed to restore weapon: original not found and CTA not available")
			}
		}
	}

	// If there was a right arm item (shield), restore it too
	if originalRightArm.UnitID != 0 && originalRightArm.UnitID != originalLeftArm.UnitID {
		ctx.Logger.Info("Restoring original right arm item", slog.String("item", originalRightArm.IdentifiedName))

		// Find the original right arm item in inventory or stash
		var originalRightItem data.Item
		foundRight := false
		var rightItemTab int

		// First check inventory
		for _, invItem := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
			if invItem.UnitID == originalRightArm.UnitID {
				originalRightItem = invItem
				foundRight = true
				break
			}
		}

		// If not in inventory, check stash
		if !foundRight {
			for tab := 1; tab <= 4; tab++ {
				SwitchStashTab(tab)
				utils.PingSleep(utils.Medium, 300)
				ctx.RefreshGameData()

				stashItems := ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
				for _, stashItem := range stashItems {
					itemTab := 1
					if stashItem.Location.LocationType == item.LocationSharedStash {
						itemTab = stashItem.Location.Page + 1
					}
					if itemTab != tab {
						continue
					}

					if stashItem.UnitID == originalRightArm.UnitID {
						originalRightItem = stashItem
						foundRight = true
						rightItemTab = tab
						break
					}
				}
				if foundRight {
					break
				}
			}

			if foundRight {
				SwitchStashTab(rightItemTab)
				utils.PingSleep(utils.Medium, 300)
				ctx.RefreshGameData()

				stashItems := ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
				for _, stashItem := range stashItems {
					if stashItem.UnitID == originalRightArm.UnitID {
						originalRightItem = stashItem
						break
					}
				}
			}
		}

		if foundRight {
			screenPos = ui.GetScreenCoordsForItem(originalRightItem)
			ctx.HID.MovePointer(screenPos.X, screenPos.Y)
			utils.PingSleep(utils.Medium, 200)
			ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.ShiftKey)
			utils.PingSleep(utils.Medium, 800)
			ctx.Logger.Info("Original right arm item restored")
		}
	}

	// Final verification: ensure Memory is back in stash
	ctx.RefreshGameData()
	memoryStillEquippedFinal := false
	for _, eqItem := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
		if eqItem.IsRuneword && eqItem.RunewordName == item.RunewordMemory {
			memoryStillEquippedFinal = true
			break
		}
	}

	if memoryStillEquippedFinal {
		ctx.Logger.Error("CRITICAL: Memory staff still equipped after restore attempt")
		// Last attempt: try to put Memory back in stash manually
		if ctx.Data.OpenMenus.Stash {
			// Get coordinates for equipped slot
			var slotCoords data.Position
			if ctx.Data.LegacyGraphics {
				slotCoords = data.Position{X: ui.EquipLArmClassicX, Y: ui.EquipLArmClassicY}
			} else {
				slotCoords = data.Position{X: ui.EquipLArmX, Y: ui.EquipLArmY}
			}
			ctx.HID.MovePointer(slotCoords.X, slotCoords.Y)
			utils.PingSleep(utils.Medium, 200)
			ctx.HID.ClickWithModifier(game.LeftButton, slotCoords.X, slotCoords.Y, game.CtrlKey)
			utils.PingSleep(utils.Medium, 1000)
			ctx.RefreshGameData()

			// Check again
			memoryStillEquippedFinal = false
			for _, eqItem := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
				if eqItem.IsRuneword && eqItem.RunewordName == item.RunewordMemory {
					memoryStillEquippedFinal = true
					break
				}
			}
		}

		if memoryStillEquippedFinal {
			step.CloseAllMenus()
			return fmt.Errorf("CRITICAL: failed to restore Memory to stash - Memory still equipped")
		}
	}

	// Close stash
	step.CloseAllMenus()

	ctx.Logger.Info("Memory buff sequence completed successfully")
	return nil
}

// buffWithMemoryAlreadyEquipped handles the case where Memory is already equipped (recovery scenario)
func buffWithMemoryAlreadyEquipped() error {
	ctx := context.Get()
	ctx.SetLastAction("buffWithMemoryAlreadyEquipped")

	ctx.Logger.Info("Memory already equipped, applying buffs and restoring weapon")

	// Apply buffs first
	// (Same buff logic as in buffWithMemory)
	buffSkills := ctx.Char.BuffSkills()
	energyShieldApplied := false
	armorApplied := false

	// Determine preferred armor skill from config
	var preferredArmorSkill skill.ID
	preferredArmorSkillStr := ""
	if ctx.CharacterCfg != nil {
		preferredArmorSkillStr = ctx.CharacterCfg.Character.PreferredArmorSkill
	}

	switch preferredArmorSkillStr {
	case "frozen":
		preferredArmorSkill = skill.FrozenArmor
	case "shiver":
		preferredArmorSkill = skill.ShiverArmor
	case "chilling":
		preferredArmorSkill = skill.ChillingArmor
	default:
		preferredArmorSkill = 0
	}

	// Apply Energy Shield
	for _, buffSkill := range buffSkills {
		if buffSkill == skill.EnergyShield {
			kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(buffSkill)
			if found {
				if expectedState, canVerify := skillToState[buffSkill]; canVerify {
					if castBuffWithVerify(ctx, kb, buffSkill, expectedState, 3) {
						energyShieldApplied = true
					}
				} else {
					castBuff(ctx, kb)
					energyShieldApplied = true
				}
			}
			break
		}
	}

	// Apply armor skill
	armorSkills := []skill.ID{skill.ChillingArmor, skill.ShiverArmor, skill.FrozenArmor}
	if preferredArmorSkill != 0 {
		kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(preferredArmorSkill)
		if found {
			if expectedState, canVerify := skillToState[preferredArmorSkill]; canVerify {
				if castBuffWithVerify(ctx, kb, preferredArmorSkill, expectedState, 3) {
					armorApplied = true
				}
			} else {
				castBuff(ctx, kb)
				armorApplied = true
			}
		}
	}

	if !armorApplied {
		for _, armorSkill := range armorSkills {
			if preferredArmorSkill != 0 && armorSkill == preferredArmorSkill {
				continue
			}
			kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(armorSkill)
			if found {
				if expectedState, canVerify := skillToState[armorSkill]; canVerify {
					if castBuffWithVerify(ctx, kb, armorSkill, expectedState, 3) {
						armorApplied = true
						break
					}
				} else {
					castBuff(ctx, kb)
					armorApplied = true
					break
				}
			}
		}
	}

	ctx.Logger.Info("Buffs applied",
		slog.Bool("energyShield", energyShieldApplied),
		slog.Bool("armor", armorApplied))

	// Now restore weapon - try CTA first since we don't know the original
	return restoreWeaponFromCTA()
}

// restoreWeaponFromCTA finds CTA in stash and equips it to replace Memory
func restoreWeaponFromCTA() error {
	ctx := context.Get()
	ctx.SetLastAction("restoreWeaponFromCTA")

	// Check if we're in town
	if !ctx.Data.PlayerUnit.Area.IsTown() {
		return fmt.Errorf("not in town, cannot access stash")
	}

	// Open stash if not already open
	if !ctx.Data.OpenMenus.Stash {
		if err := OpenStash(); err != nil {
			return fmt.Errorf("failed to open stash: %w", err)
		}
		utils.PingSleep(utils.Medium, 500)
	}

	// Find CTA in stash
	var ctaWeapon data.Item
	var ctaFound bool
	var ctaTab int

	for tab := 1; tab <= 4; tab++ {
		SwitchStashTab(tab)
		utils.PingSleep(utils.Medium, 300)
		ctx.RefreshGameData()

		stashItems := ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
		for _, stashItem := range stashItems {
			itemTab := 1
			if stashItem.Location.LocationType == item.LocationSharedStash {
				itemTab = stashItem.Location.Page + 1
			}
			if itemTab != tab {
				continue
			}

			if stashItem.IsRuneword && stashItem.RunewordName == item.RunewordCallToArms {
				ctaWeapon = stashItem
				ctaFound = true
				ctaTab = tab
				break
			}
		}
		if ctaFound {
			break
		}
	}

	if !ctaFound {
		return fmt.Errorf("CTA not found in stash")
	}

	ctx.Logger.Info("Found CTA in stash, equipping to replace Memory", slog.String("cta", ctaWeapon.IdentifiedName))

	// Make sure we're on the correct tab
	SwitchStashTab(ctaTab)
	utils.PingSleep(utils.Medium, 300)
	ctx.RefreshGameData()

	// Find CTA again after tab switch
	stashItems := ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
	for _, stashItem := range stashItems {
		if stashItem.IsRuneword && stashItem.RunewordName == item.RunewordCallToArms && stashItem.UnitID == ctaWeapon.UnitID {
			ctaWeapon = stashItem
			break
		}
	}

	// Equip CTA using SHIFT + Click (will replace Memory)
	screenPos := ui.GetScreenCoordsForItem(ctaWeapon)
	ctx.HID.MovePointer(screenPos.X, screenPos.Y)
	utils.PingSleep(utils.Medium, 200)
	ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.ShiftKey)
	utils.PingSleep(utils.Medium, 1000)

	// Verify CTA is equipped and Memory is back in stash
	ctx.RefreshGameData()
	ctaEquipped := false
	memoryInStash := false

	for _, eqItem := range ctx.Data.Inventory.ByLocation(item.LocationEquipped) {
		if eqItem.IsRuneword && eqItem.RunewordName == item.RunewordCallToArms {
			ctaEquipped = true
			break
		}
	}

	stashItems = ctx.Data.Inventory.ByLocation(item.LocationStash, item.LocationSharedStash)
	for _, itm := range stashItems {
		if itm.IsRuneword && itm.RunewordName == item.RunewordMemory {
			memoryInStash = true
			break
		}
	}

	if ctaEquipped && memoryInStash {
		ctx.Logger.Info("CTA equipped and Memory returned to stash")
		return nil
	}

	ctx.Logger.Warn("Verification failed after CTA equip",
		slog.Bool("ctaEquipped", ctaEquipped),
		slog.Bool("memoryInStash", memoryInStash))
	return fmt.Errorf("failed to verify CTA equip and Memory return")
}
