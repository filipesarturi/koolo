package step

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/utils"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/packet"
)

const attackCycleDuration = 120 * time.Millisecond
const repositionCooldown = 2 * time.Second // Constant for repositioning cooldown

var (
	statesMutex           sync.RWMutex
	monsterStates         = make(map[data.UnitID]*attackState)
	ErrMonsterUnreachable = errors.New("monster appears to be unreachable or unkillable")
	ErrMonsterDead        = errors.New("monster is dead")
)

// Contains all configuration for an attack sequence
type attackSettings struct {
	primaryAttack    bool          // Whether this is a primary (left click) attack
	skill            skill.ID      // Skill ID for secondary attacks
	followEnemy      bool          // Whether to follow the enemy while attacking
	minDistance      int           // Minimum attack range
	maxDistance      int           // Maximum attack range
	aura             skill.ID      // Aura to maintain during attack
	target           data.UnitID   // Specific target's unit ID (0 for AOE)
	shouldStandStill bool          // Whether to stand still while attacking
	numOfAttacks     int           // Number of attacks to perform
	timeout          time.Duration // Timeout for the attack sequence
	isBurstCastSkill bool          // Whether this is a channeled/burst skill like Nova
}

// AttackOption defines a function type for configuring attack settings
type AttackOption func(step *attackSettings)

type attackState struct {
	lastHealth             int
	lastHealthCheckTime    time.Time
	failedAttemptStartTime time.Time
	lastRepositionTime     time.Time
	repositionAttempts     int
	position               data.Position
}

// Distance configures attack to follow enemy within specified range
func Distance(minimum, maximum int) AttackOption {
	return func(step *attackSettings) {
		step.followEnemy = true
		step.minDistance = minimum
		step.maxDistance = maximum
	}
}

// RangedDistance configures attack for ranged combat without following
func RangedDistance(minimum, maximum int) AttackOption {
	return func(step *attackSettings) {
		step.followEnemy = false // Don't follow enemies for ranged attacks
		step.minDistance = minimum
		step.maxDistance = maximum
	}
}

// StationaryDistance configures attack to remain stationary (like FoH)
func StationaryDistance(minimum, maximum int) AttackOption {
	return func(step *attackSettings) {
		step.followEnemy = false
		step.minDistance = minimum
		step.maxDistance = maximum
		step.shouldStandStill = true
	}
}

// EnsureAura ensures specified aura is active during attack
func EnsureAura(aura skill.ID) AttackOption {
	return func(step *attackSettings) {
		step.aura = aura
	}
}

// PrimaryAttack initiates a primary (left-click) attack sequence
func PrimaryAttack(target data.UnitID, numOfAttacks int, standStill bool, opts ...AttackOption) error {
	ctx := context.Get()

	// Special handling for Berserker characters
	if berserker, ok := ctx.Char.(interface{ PerformBerserkAttack(data.UnitID) }); ok {
		for i := 0; i < numOfAttacks; i++ {
			berserker.PerformBerserkAttack(target)
		}
		return nil
	}

	settings := attackSettings{
		target:           target,
		numOfAttacks:     numOfAttacks,
		shouldStandStill: standStill,
		primaryAttack:    true,
	}
	for _, o := range opts {
		o(&settings)
	}

	return attack(settings)
}

// SecondaryAttack initiates a secondary (right-click) attack sequence with a specific skill
func SecondaryAttack(skill skill.ID, target data.UnitID, numOfAttacks int, opts ...AttackOption) error {
	settings := attackSettings{
		target:           target,
		numOfAttacks:     numOfAttacks,
		skill:            skill,
		primaryAttack:    false,
		isBurstCastSkill: skill == 48, // nova can define any other burst skill here
	}
	for _, o := range opts {
		o(&settings)
	}

	if settings.isBurstCastSkill {
		settings.timeout = 30 * time.Second
		return burstAttack(settings)
	}

	return attack(settings)
}

// Helper function to validate if a monster should be targetable
func isValidEnemy(monster data.Monster, ctx *context.Status) bool {
	// Skip dead monsters early (most common case)
	if monster.Stats[stat.Life] <= 0 {
		return false
	}

	// Skip pets, mercenaries, and friendly NPCs (allies' summons)
	if monster.IsPet() || monster.IsMerc() || monster.IsGoodNPC() || monster.IsSkip() {
		return false
	}

	// Special case: Always allow Vizier seal boss even if off grid
	isVizier := monster.Type == data.MonsterTypeSuperUnique && monster.Name == npc.StormCaster
	if isVizier {
		return true
	}

	// Skip monsters in invalid positions
	if !ctx.Data.AreaData.IsWalkable(monster.Position) {
		return false
	}

	return true
}

// refreshAndValidateMonster refreshes game data and validates if the monster is still alive and valid.
// Returns the updated monster, whether it was found, and an error if the monster is dead.
// This function helps avoid duplicate code and ensures consistent validation.
func refreshAndValidateMonster(ctx *context.Status, monsterID data.UnitID) (data.Monster, bool, error) {
	ctx.RefreshGameData()
	monster, found := ctx.Data.Monsters.FindByID(monsterID)
	if !found {
		return data.Monster{}, false, nil
	}

	// Early return if monster is dead
	if monster.Stats[stat.Life] <= 0 {
		// Clean up state efficiently
		statesMutex.Lock()
		delete(monsterStates, monsterID)
		statesMutex.Unlock()
		return data.Monster{}, false, ErrMonsterDead
	}

	// Validate enemy using existing helper
	if !isValidEnemy(monster, ctx) {
		return data.Monster{}, false, nil
	}

	return monster, true, nil
}

// Cleanup function to ensure proper state on exit
func keyCleanup(ctx *context.Status) {
	ctx.HID.KeyUp(ctx.Data.KeyBindings.StandStill)
}

func attack(settings attackSettings) error {
	ctx := context.Get()
	ctx.SetLastStep("Attack")
	defer keyCleanup(ctx) // cleanup possible pressed keys/buttons

	numOfAttacksRemaining := settings.numOfAttacks
	lastRunAt := time.Time{}
	lastRefreshTime := time.Now()
	const refreshInterval = 500 * time.Millisecond // Refresh game data periodically, not every iteration

	attackStartTime := time.Now()
	lastLogTime := time.Time{}
	const logThrottleInterval = 2 * time.Second // Throttle debug logs to avoid spam

	for {
		ctx.PauseIfNotPriority()

		if numOfAttacksRemaining <= 0 {
			ctx.Logger.Debug("Attack sequence completed",
				slog.Int("attacksPerformed", settings.numOfAttacks),
				slog.Duration("totalDuration", time.Since(attackStartTime)),
			)
			return nil
		}

		// Refresh game data periodically to catch monster death
		if time.Since(lastRefreshTime) > refreshInterval {
			ctx.RefreshGameData()
			lastRefreshTime = time.Now()
		}

		monster, found := ctx.Data.Monsters.FindByID(settings.target)
		// Early return if monster not found or dead
		if !found || !isValidEnemy(monster, ctx) {
			ctx.Logger.Debug("Target monster not found or invalid",
				slog.Int("monsterID", int(settings.target)),
				slog.Bool("found", found),
			)
			return nil // Target is not valid, we don't have anything to attack
		}

		// Early return if monster is dead before movement calculations
		if monster.Stats[stat.Life] <= 0 {
			ctx.Logger.Debug("Monster died during attack sequence",
				slog.Int("monsterID", int(monster.UnitID)),
				slog.String("monsterName", string(monster.Name)),
			)
			statesMutex.Lock()
			delete(monsterStates, settings.target)
			statesMutex.Unlock()
			return nil
		}

		distance := ctx.PathFinder.DistanceFromMe(monster.Position)
		hasLoS := ctx.PathFinder.LineOfSight(ctx.Data.PlayerUnit.Position, monster.Position)
		hpCurrent := monster.Stats[stat.Life]
		hpMax := monster.Stats[stat.MaxLife]

		// Throttled debug log with monster state
		if time.Since(lastLogTime) > logThrottleInterval {
			ctx.Logger.Debug("Attack state",
				slog.Int("monsterID", int(monster.UnitID)),
				slog.String("monsterName", string(monster.Name)),
				slog.Int("hpCurrent", hpCurrent),
				slog.Int("hpMax", hpMax),
				slog.Int("hpPercent", int(float64(hpCurrent)/float64(hpMax)*100)),
				slog.Int("distance", distance),
				slog.Bool("hasLoS", hasLoS),
				slog.Int("minDistance", settings.minDistance),
				slog.Int("maxDistance", settings.maxDistance),
				slog.Bool("followEnemy", settings.followEnemy),
				slog.Int("attacksRemaining", numOfAttacksRemaining),
			)
			lastLogTime = time.Now()
		}

		if !lastRunAt.IsZero() && !settings.followEnemy && distance > settings.maxDistance {
			ctx.Logger.Debug("Enemy out of range, stopping attack",
				slog.Int("distance", distance),
				slog.Int("maxDistance", settings.maxDistance),
				slog.Bool("followEnemy", settings.followEnemy),
			)
			return nil // Enemy is out of range and followEnemy is disabled, we cannot attack
		}

		// Check if we need to reposition if we aren't doing any damage (prevent attacking through doors etc.)
		_, state := checkMonsterDamage(monster) // Get the state
		needsRepositioning := !state.failedAttemptStartTime.IsZero() &&
			time.Since(state.failedAttemptStartTime) > 3*time.Second

		if needsRepositioning {
			ctx.Logger.Debug("Repositioning needed - no damage detected",
				slog.Int("monsterID", int(monster.UnitID)),
				slog.Duration("noDamageTime", time.Since(state.failedAttemptStartTime)),
				slog.Int("repositionAttempts", state.repositionAttempts),
			)
		}

		// Be sure we stay in range of the enemy. ensureEnemyIsInRange will handle reposition attempts.
		err := ensureEnemyIsInRange(monster, state, settings.maxDistance, settings.minDistance, needsRepositioning)
		if err != nil {
			if errors.Is(err, ErrMonsterUnreachable) {
				ctx.Logger.Info("Giving up on monster due to unreachability/unkillability",
					slog.Int("monsterID", int(monster.UnitID)),
					slog.String("monsterName", string(monster.Name)),
					slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
					slog.Int("repositionAttempts", state.repositionAttempts),
				)
				statesMutex.Lock()
				delete(monsterStates, settings.target) // Clean up state for this monster
				statesMutex.Unlock()
				return nil // Return nil, allowing the higher-level action to find a new monster or finish.
			}
			if errors.Is(err, ErrMonsterDead) {
				ctx.Logger.Debug("Monster died during range check")
				return nil // Monster died, allow higher-level action to find new target
			}
			return err // Propagate other errors from ensureEnemyIsInRange
		}

		// Handle aura activation
		if settings.aura != 0 && lastRunAt.IsZero() {
			ctx.Logger.Debug("Activating aura for attack",
				slog.Int("auraSkillID", int(settings.aura)),
			)
			ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.MustKBForSkill(settings.aura))
		}

		// Attack timing check
		castDuration := ctx.Data.PlayerCastDuration()
		timeSinceLastAttack := time.Since(lastRunAt)
		if !lastRunAt.IsZero() && timeSinceLastAttack <= castDuration-attackCycleDuration {
			continue
		}

		attackMethod := "mouse"
		if settings.skill == skill.Blizzard && ctx.CharacterCfg.Character.BlizzardSorceress.UseBlizzardPackets {
			attackMethod = "packet_blizzard"
		} else if ctx.CharacterCfg.PacketCasting.UseForEntitySkills && ctx.PacketSender != nil && settings.target != 0 {
			attackMethod = "packet_entity"
		}

		ctx.Logger.Debug("Performing attack",
			slog.Int("monsterID", int(monster.UnitID)),
			slog.Int("skillID", int(settings.skill)),
			slog.Bool("primaryAttack", settings.primaryAttack),
			slog.String("attackMethod", attackMethod),
			slog.Duration("castDuration", castDuration),
			slog.Duration("timeSinceLastAttack", timeSinceLastAttack),
		)

		performAttack(ctx, settings, monster.UnitID, monster.Position.X, monster.Position.Y)

		lastRunAt = time.Now()
		numOfAttacksRemaining--
	}
}

func burstAttack(settings attackSettings) error {
	ctx := context.Get()
	ctx.SetLastStep("BurstAttack")
	defer keyCleanup(ctx) // cleanup possible pressed keys/buttons

	monster, found := ctx.Data.Monsters.FindByID(settings.target)
	if !found || !isValidEnemy(monster, ctx) {
		ctx.Logger.Debug("Burst attack: initial target not found or invalid",
			slog.Int("monsterID", int(settings.target)),
			slog.Bool("found", found),
		)
		return nil // Target is not valid, we don't have anything to attack
	}

	ctx.Logger.Debug("Starting burst attack",
		slog.Int("skillID", int(settings.skill)),
		slog.Int("initialMonsterID", int(monster.UnitID)),
		slog.String("initialMonsterName", string(monster.Name)),
		slog.Duration("timeout", settings.timeout),
	)

	// Initially we try to move to the enemy, later we will check for closer enemies to keep attacking
	_, state := checkMonsterDamage(monster)                                                        // Get the state for the initial monster
	err := ensureEnemyIsInRange(monster, state, settings.maxDistance, settings.minDistance, false) // No initial repositioning check for burst
	if err != nil {
		if errors.Is(err, ErrMonsterUnreachable) {
			ctx.Logger.Info("Giving up on initial monster due to unreachability/unkillability during burst",
				slog.Int("monsterID", int(monster.UnitID)),
				slog.String("monsterName", string(monster.Name)),
				slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
			)
			statesMutex.Lock()
			delete(monsterStates, monster.UnitID) // Clean up state for this monster
			statesMutex.Unlock()
			return nil // Exit burst attack, caller will find next target.
		}
		return err // Propagate error from initial range check
	}

	startedAt := time.Now()
	lastRefreshTime := time.Now()
	const refreshInterval = 500 * time.Millisecond // Refresh game data periodically, not every iteration
	lastTargetSwitch := time.Time{}
	targetSwitchCount := 0
	lastLogTime := time.Time{}
	const logThrottleInterval = 2 * time.Second

	for {
		ctx.PauseIfNotPriority()

		if !startedAt.IsZero() && time.Since(startedAt) > settings.timeout {
			ctx.Logger.Debug("Burst attack timeout reached",
				slog.Duration("duration", time.Since(startedAt)),
				slog.Int("targetSwitches", targetSwitchCount),
			)
			return nil // Timeout reached, finish attack sequence
		}

		// Refresh game data periodically to catch monster deaths
		if time.Since(lastRefreshTime) > refreshInterval {
			ctx.RefreshGameData()
			lastRefreshTime = time.Now()
		}

		// Optimized loop: check life before calculating distance (early continue)
		target := data.Monster{}
		enemiesChecked := 0
		for _, m := range ctx.Data.Monsters.Enemies() {
			enemiesChecked++
			// Check validity before distance calculation
			if !isValidEnemy(m, ctx) {
				continue
			}

			distance := ctx.PathFinder.DistanceFromMe(m.Position)
			if distance <= settings.maxDistance {
				target = m
				break // Found valid target, stop iterating
			}
		}

		if target.UnitID == 0 {
			ctx.Logger.Debug("Burst attack: no valid targets in range",
				slog.Int("enemiesChecked", enemiesChecked),
				slog.Int("maxDistance", settings.maxDistance),
			)
			return nil // We have no valid targets in range, finish attack sequence
		}

		// Track target switches
		if lastTargetSwitch.IsZero() || target.UnitID != settings.target {
			if !lastTargetSwitch.IsZero() {
				targetSwitchCount++
				ctx.Logger.Debug("Burst attack: target switched",
					slog.Int("newMonsterID", int(target.UnitID)),
					slog.String("newMonsterName", string(target.Name)),
					slog.Int("totalSwitches", targetSwitchCount),
				)
			}
			lastTargetSwitch = time.Now()
		}

		// Verify target is still alive after selection (race condition protection)
		if target.Stats[stat.Life] <= 0 {
			ctx.Logger.Debug("Burst attack: target died, finding new target")
			continue // Target died, find new one immediately
		}

		// Check if we need to reposition if we aren't doing any damage
		didDamage, state := checkMonsterDamage(target) // Get the state for the current target

		needsRepositioning := !state.failedAttemptStartTime.IsZero() &&
			time.Since(state.failedAttemptStartTime) > 3*time.Second

		distance := ctx.PathFinder.DistanceFromMe(target.Position)
		hasLoS := ctx.PathFinder.LineOfSight(ctx.Data.PlayerUnit.Position, target.Position)

		// Throttled debug log
		if time.Since(lastLogTime) > logThrottleInterval {
			ctx.Logger.Debug("Burst attack state",
				slog.Int("targetMonsterID", int(target.UnitID)),
				slog.String("targetMonsterName", string(target.Name)),
				slog.Int("hpCurrent", target.Stats[stat.Life]),
				slog.Int("hpMax", target.Stats[stat.MaxLife]),
				slog.Int("distance", distance),
				slog.Bool("hasLoS", hasLoS),
				slog.Bool("didDamage", didDamage),
				slog.Bool("needsRepositioning", needsRepositioning),
				slog.Duration("elapsed", time.Since(startedAt)),
			)
			lastLogTime = time.Now()
		}

		// If we don't have LoS we will need to interrupt and move :(
		if !hasLoS || needsRepositioning {
			if !hasLoS {
				ctx.Logger.Debug("Burst attack: no line of sight, repositioning",
					slog.Int("targetMonsterID", int(target.UnitID)),
				)
			}
			// ensureEnemyIsInRange will handle reposition attempts and return nil if it skips
			err = ensureEnemyIsInRange(target, state, settings.maxDistance, settings.minDistance, needsRepositioning)
			if err != nil {
				if errors.Is(err, ErrMonsterUnreachable) {
					ctx.Logger.Info("Giving up on monster due to unreachability/unkillability during burst",
						slog.Int("monsterID", int(target.UnitID)),
						slog.String("monsterName", string(target.Name)),
						slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
					)
					statesMutex.Lock()
					delete(monsterStates, target.UnitID) // Clean up state for this monster
					statesMutex.Unlock()
					continue // Continue loop to find next target instead of returning
				}
				if errors.Is(err, ErrMonsterDead) {
					ctx.Logger.Debug("Burst attack: target died during range check")
					continue // Monster died, find new target immediately
				}
				return err // Propagate general errors from ensureEnemyIsInRange
			}
			continue // Continue loop to re-evaluate conditions after a potential move
		}

		performAttack(ctx, settings, target.UnitID, target.Position.X, target.Position.Y)
	}
}

func performAttack(ctx *context.Status, settings attackSettings, targetID data.UnitID, x, y int) {
	monsterPos := data.Position{X: x, Y: y}
	hasLoS := ctx.PathFinder.LineOfSight(ctx.Data.PlayerUnit.Position, monsterPos)
	if !hasLoS && !ctx.ForceAttack {
		ctx.Logger.Debug("Skipping attack - no line of sight",
			slog.Int("targetID", int(targetID)),
			slog.Bool("forceAttack", ctx.ForceAttack),
		)
		return // Skip attack if no line of sight
	}

	// Check if we should use packet casting for Blizzard (location-based)
	useBlizzardPacket := false
	if settings.skill == skill.Blizzard {
		switch ctx.CharacterCfg.Character.Class {
		case "sorceress":
			useBlizzardPacket = ctx.CharacterCfg.Character.BlizzardSorceress.UseBlizzardPackets
		case "sorceress_leveling":
			useBlizzardPacket = ctx.CharacterCfg.Character.SorceressLeveling.UseBlizzardPackets
		}
	}

	// If using packet casting for Blizzard (location-based skill)
	if useBlizzardPacket {
		// Ensure we have Blizzard selected on right-click
		if ctx.Data.PlayerUnit.RightSkill != skill.Blizzard {
			ctx.Logger.Debug("Selecting Blizzard skill for packet casting")
			SelectRightSkill(skill.Blizzard)
			time.Sleep(time.Millisecond * 10)
		}

		// Send packet to cast Blizzard at location
		if err := ctx.PacketSender.CastSkillAtLocation(monsterPos); err != nil {
			ctx.Logger.Warn("Failed to cast Blizzard via packet, falling back to mouse",
				slog.String("error", err.Error()),
				slog.Int("targetX", x),
				slog.Int("targetY", y),
			)
			// Fall back to regular mouse casting
			performMouseAttack(ctx, settings, x, y)
		} else {
			ctx.Logger.Debug("Blizzard cast via packet",
				slog.Int("targetX", x),
				slog.Int("targetY", y),
			)
		}
		return
	}

	// Check if we should use entity-targeted packet casting
	if ctx.CharacterCfg.PacketCasting.UseForEntitySkills && ctx.PacketSender != nil && targetID != 0 {
		// Ensure we have the skill selected
		if settings.primaryAttack {
			if settings.skill != 0 && ctx.Data.PlayerUnit.LeftSkill != settings.skill {
				ctx.Logger.Debug("Selecting left skill for packet casting",
					slog.Int("skillID", int(settings.skill)),
				)
				SelectLeftSkill(settings.skill)
				time.Sleep(time.Millisecond * 10)
			}
			// Send left-click entity skill packet
			castPacket := packet.NewCastSkillEntityLeft(targetID)
			if err := ctx.PacketSender.SendPacket(castPacket.GetPayload()); err != nil {
				ctx.Logger.Warn("Failed to cast entity skill via packet (left), falling back to mouse",
					slog.String("error", err.Error()),
					slog.Int("targetID", int(targetID)),
					slog.Int("skillID", int(settings.skill)),
				)
				performMouseAttack(ctx, settings, x, y)
			} else {
				ctx.Logger.Debug("Entity skill cast via packet (left)",
					slog.Int("targetID", int(targetID)),
					slog.Int("skillID", int(settings.skill)),
				)
				// Respect cast duration to avoid spamming server
				time.Sleep(ctx.Data.PlayerCastDuration())
			}
		} else {
			if settings.skill != 0 && ctx.Data.PlayerUnit.RightSkill != settings.skill {
				ctx.Logger.Debug("Selecting right skill for packet casting",
					slog.Int("skillID", int(settings.skill)),
				)
				SelectRightSkill(settings.skill)
				time.Sleep(time.Millisecond * 10)
			}
			// Send right-click entity skill packet
			castPacket := packet.NewCastSkillEntityRight(targetID)
			if err := ctx.PacketSender.SendPacket(castPacket.GetPayload()); err != nil {
				ctx.Logger.Warn("Failed to cast entity skill via packet (right), falling back to mouse",
					slog.String("error", err.Error()),
					slog.Int("targetID", int(targetID)),
					slog.Int("skillID", int(settings.skill)),
				)
				performMouseAttack(ctx, settings, x, y)
			} else {
				ctx.Logger.Debug("Entity skill cast via packet (right)",
					slog.Int("targetID", int(targetID)),
					slog.Int("skillID", int(settings.skill)),
				)
				// Respect cast duration to avoid spamming server
				time.Sleep(ctx.Data.PlayerCastDuration())
			}
		}
		return
	}

	// Regular mouse-based attack
	ctx.Logger.Debug("Using mouse-based attack",
		slog.Int("targetID", int(targetID)),
		slog.Int("skillID", int(settings.skill)),
		slog.Bool("primaryAttack", settings.primaryAttack),
	)
	performMouseAttack(ctx, settings, x, y)
}

func performMouseAttack(ctx *context.Status, settings attackSettings, x, y int) {
	// Ensure we have the skill selected
	if settings.skill != 0 && ctx.Data.PlayerUnit.RightSkill != settings.skill {
		SelectRightSkill(settings.skill)
		time.Sleep(time.Millisecond * 10)
	}

	if settings.shouldStandStill {
		ctx.HID.KeyDown(ctx.Data.KeyBindings.StandStill)
	}

	x, y = ctx.PathFinder.GameCoordsToScreenCords(x, y)
	if settings.primaryAttack {
		ctx.HID.Click(game.LeftButton, x, y)
	} else {
		ctx.HID.Click(game.RightButton, x, y)
	}

	if settings.shouldStandStill {
		ctx.HID.KeyUp(ctx.Data.KeyBindings.StandStill)
	}
}

// Modified: Added 'state' parameter to manage lastRepositionTime and repositionAttempts
func ensureEnemyIsInRange(monster data.Monster, state *attackState, maxDistance, minDistance int, needsRepositioning bool) error {
	ctx := context.Get()
	ctx.SetLastStep("ensureEnemyIsInRange")

	// Early return if monster is dead - avoid unnecessary path calculations
	if monster.Stats[stat.Life] <= 0 {
		statesMutex.Lock()
		delete(monsterStates, monster.UnitID)
		statesMutex.Unlock()
		return ErrMonsterDead
	}

	currentPos := ctx.Data.PlayerUnit.Position
	distanceToMonster := ctx.PathFinder.DistanceFromMe(monster.Position)
	hasLoS := ctx.PathFinder.LineOfSight(currentPos, monster.Position)

	// If we are already in range, have LoS, and don't need repositioning, we are good.
	// Reset repositionAttempts for future needs.
	if hasLoS && distanceToMonster <= maxDistance && !needsRepositioning {
		state.repositionAttempts = 0 // Reset attempts if we're in a good state
		return nil
	}

	// Handle repositioning if needed (due to no damage, or no LoS for burst attacks)
	if needsRepositioning {
		// If we've already tried repositioning once for this "stuck" phase
		if state.repositionAttempts >= 1 { // This is the problematic part. User wants to allow 1 attempt.
			ctx.Logger.Info("Already attempted repositioning, considering monster unkillable",
				slog.Int("monsterID", int(monster.UnitID)),
				slog.String("monsterName", string(monster.Name)),
				slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
				slog.Int("repositionAttempts", state.repositionAttempts),
			)
			return ErrMonsterUnreachable // <-- CHANGE: Return specific error
		}

		// Check if enough time has passed since the last reposition attempt (cooldown)
		cooldownRemaining := repositionCooldown - time.Since(state.lastRepositionTime)
		if cooldownRemaining > 0 {
			ctx.Logger.Debug("Repositioning on cooldown",
				slog.Int("monsterID", int(monster.UnitID)),
				slog.Duration("cooldownRemaining", cooldownRemaining),
			)
			return nil // Still on cooldown, do not reposition yet. Return nil to continue attacking.
		}

		ctx.Logger.Info("No damage detected, attempting reposition",
			slog.Int("monsterID", int(monster.UnitID)),
			slog.String("monsterName", string(monster.Name)),
			slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
			slog.Int("repositionAttempt", state.repositionAttempts+1),
			slog.Duration("noDamageTime", time.Since(state.failedAttemptStartTime)),
			slog.Int("playerX", currentPos.X),
			slog.Int("playerY", currentPos.Y),
			slog.Int("monsterX", monster.Position.X),
			slog.Int("monsterY", monster.Position.Y),
			slog.Int("distance", distanceToMonster),
		)

		dest := ctx.PathFinder.BeyondPosition(currentPos, monster.Position, 4)
		err := MoveTo(dest, WithIgnoreMonsters())
		state.repositionAttempts++ // Increment attempt count after trying to move
		if err != nil {
			ctx.Logger.Error("MoveTo failed during reposition attempt",
				slog.Int("monsterID", int(monster.UnitID)),
				slog.String("monsterName", string(monster.Name)),
				slog.String("error", err.Error()),
				slog.Int("destX", dest.X),
				slog.Int("destY", dest.Y),
			)
			// Do NOT update lastRepositionTime here if MoveTo completely failed, so it can try again sooner if the path clears.
			// However, since we're only allowing ONE attempt, the increment of repositionAttempts handles the "give up" logic.
			return nil // Continue attacking, but the next loop iteration will hit repositionAttempts >= 1 and return ErrMonsterUnreachable
		}
		state.lastRepositionTime = time.Now() // Update the last reposition time only if MoveTo was initiated without error
		ctx.Logger.Debug("Repositioning initiated",
			slog.Int("monsterID", int(monster.UnitID)),
			slog.Int("destX", dest.X),
			slog.Int("destY", dest.Y),
		)
		return nil // Successfully initiated the move, continue attacking next loop iteration
	}

	// Any close-range combat (mosaic,barb...) should move directly to target
	// This is general movement, not triggered by needsRepositioning (no damage), so don't touch repositionAttempts.
	if maxDistance <= 3 {
		ctx.Logger.Debug("Close-range combat: moving directly to target",
			slog.Int("monsterID", int(monster.UnitID)),
			slog.Int("maxDistance", maxDistance),
			slog.Int("distance", distanceToMonster),
		)
		return MoveTo(monster.Position, WithIgnoreMonsters(), WithDistanceToFinish(max(2, maxDistance)))
	}

	// Get path to monster
	path, pathDistance, found := ctx.PathFinder.GetPath(monster.Position)
	// We cannot reach the enemy, let's skip the attack sequence by returning an error
	if !found {
		ctx.Logger.Debug("Path could not be calculated to reach monster",
			slog.Int("monsterID", int(monster.UnitID)),
			slog.Int("playerX", currentPos.X),
			slog.Int("playerY", currentPos.Y),
			slog.Int("monsterX", monster.Position.X),
			slog.Int("monsterY", monster.Position.Y),
			slog.Int("distance", distanceToMonster),
		)
		return errors.New("path could not be calculated to reach monster") // This is a fundamental pathing error, propagate it.
	}

	ctx.Logger.Debug("Path found to monster",
		slog.Int("monsterID", int(monster.UnitID)),
		slog.Int("pathLength", len(path)),
		slog.Int("pathDistance", pathDistance),
		slog.Int("distance", distanceToMonster),
		slog.Bool("hasLoS", hasLoS),
	)

	// Look for suitable position along path
	for _, pos := range path {
		monsterDistance := utils.DistanceFromPoint(ctx.Data.AreaData.RelativePosition(monster.Position), pos)
		if monsterDistance > maxDistance || monsterDistance < minDistance {
			continue
		}

		dest := data.Position{
			X: pos.X + ctx.Data.AreaData.OffsetX,
			Y: pos.Y + ctx.Data.AreaData.OffsetY,
		}

		// Handle overshooting for short distances (Nova distances)
		distanceToMove := ctx.PathFinder.DistanceFromMe(dest)
		if distanceToMove <= DistanceToFinishMoving {
			dest = ctx.PathFinder.BeyondPosition(currentPos, dest, 9)
		}

		destHasLoS := ctx.PathFinder.LineOfSight(dest, monster.Position)
		if destHasLoS && !ctx.ForceAttack {
			ctx.Logger.Debug("Moving to suitable attack position",
				slog.Int("monsterID", int(monster.UnitID)),
				slog.Int("destX", dest.X),
				slog.Int("destY", dest.Y),
				slog.Int("monsterDistance", monsterDistance),
				slog.Int("distanceToMove", distanceToMove),
			)
			// This is also general movement to get into attack range, not a "repositioning attempt" for being stuck.
			return MoveTo(dest, WithIgnoreMonsters())
		}
	}

	ctx.Logger.Debug("No suitable position found along path, continuing attack",
		slog.Int("monsterID", int(monster.UnitID)),
		slog.Int("distance", distanceToMonster),
		slog.Bool("hasLoS", hasLoS),
	)
	return nil // No suitable position found along path, continue attacking
}

func checkMonsterDamage(monster data.Monster) (bool, *attackState) {
	ctx := context.Get()
	statesMutex.Lock()
	defer statesMutex.Unlock()

	state, exists := monsterStates[monster.UnitID]
	if !exists {
		state = &attackState{
			lastHealth:          monster.Stats[stat.Life],
			lastHealthCheckTime: time.Now(),
			position:            monster.Position,
			repositionAttempts:  0, // Initialize counter to 0 for new states
		}
		monsterStates[monster.UnitID] = state
		ctx.Logger.Debug("New attack state created for monster",
			slog.Int("monsterID", int(monster.UnitID)),
			slog.String("monsterName", string(monster.Name)),
			slog.Int("initialHP", state.lastHealth),
		)
	}

	didDamage := false
	currentHealth := monster.Stats[stat.Life]
	hpChange := state.lastHealth - currentHealth

	// Only update health check if some time has passed
	if time.Since(state.lastHealthCheckTime) > 100*time.Millisecond {
		if currentHealth < state.lastHealth {
			didDamage = true
			state.failedAttemptStartTime = time.Time{}
			state.repositionAttempts = 0 // Reset attempts when damage is successfully dealt
			ctx.Logger.Debug("Damage detected on monster",
				slog.Int("monsterID", int(monster.UnitID)),
				slog.Int("hpChange", hpChange),
				slog.Int("hpBefore", state.lastHealth),
				slog.Int("hpAfter", currentHealth),
			)
		} else if state.failedAttemptStartTime.IsZero() &&
			monster.Position == state.position { // only start failing if monster hasn't moved
			state.failedAttemptStartTime = time.Now()
			state.repositionAttempts = 0 // Reset attempts when starting a new failed phase
			ctx.Logger.Debug("No damage detected, starting failure timer",
				slog.Int("monsterID", int(monster.UnitID)),
				slog.Int("currentHP", currentHealth),
			)
		}

		state.lastHealth = currentHealth
		state.lastHealthCheckTime = time.Now()
		state.position = monster.Position

		// Clean up old entries periodically
		if len(monsterStates) > 100 {
			now := time.Now()
			cleaned := 0
			for id, s := range monsterStates {
				if now.Sub(s.lastHealthCheckTime) > 5*time.Minute {
					delete(monsterStates, id)
					cleaned++
				}
			}
			if cleaned > 0 {
				ctx.Logger.Debug("Cleaned up old attack states",
					slog.Int("cleaned", cleaned),
					slog.Int("remaining", len(monsterStates)),
				)
			}
		}
	}

	return didDamage, state
}
