package step

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/state"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/pather"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/hectorgimenez/koolo/internal/utils"
)

const DistanceToFinishMoving = 4
const stepMonsterCheckInterval = 100 * time.Millisecond

var (
	ErrMonstersInPath  = errors.New("monsters detected in movement path")
	ErrPlayerStuck     = errors.New("player is stuck")
	ErrPlayerRoundTrip = errors.New("player round trip")
	ErrNoPath          = errors.New("path couldn't be calculated")
)

type MoveOpts struct {
	distanceOverride      *int
	stationaryMinDistance *int
	stationaryMaxDistance *int
	ignoreShrines         bool
	ignoreMonsters        bool
	ignoreItems           bool
	monsterFilters        []data.MonsterFilter
	clearPathOverride     *int
}

type MoveOption func(*MoveOpts)

// WithDistanceToFinish overrides the default DistanceToFinishMoving
func WithDistanceToFinish(distance int) MoveOption {
	return func(opts *MoveOpts) {
		opts.distanceOverride = &distance
	}
}

// WithStationaryDistance configures MoveTo to stop when within a specific range of the destination.
func WithStationaryDistance(min, max int) MoveOption {
	return func(opts *MoveOpts) {
		opts.stationaryMinDistance = &min
		opts.stationaryMaxDistance = &max
	}
}

func WithIgnoreMonsters() MoveOption {
	return func(opts *MoveOpts) {
		opts.ignoreMonsters = true
	}
}

func WithIgnoreItems() MoveOption {
	return func(opts *MoveOpts) {
		opts.ignoreItems = true
	}
}

func IgnoreShrines() MoveOption {
	return func(opts *MoveOpts) {
		opts.ignoreShrines = true
	}
}

func WithMonsterFilter(filters ...data.MonsterFilter) MoveOption {
	return func(opts *MoveOpts) {
		opts.monsterFilters = append(opts.monsterFilters, filters...)
	}
}

func WithClearPathOverride(clearPathOverride int) MoveOption {
	return func(opts *MoveOpts) {
		opts.clearPathOverride = &clearPathOverride
	}
}

func (opts MoveOpts) DistanceToFinish() *int {
	return opts.distanceOverride
}

func (opts MoveOpts) IgnoreMonsters() bool {
	return opts.ignoreMonsters
}

func (opts MoveOpts) IgnoreItems() bool {
	return opts.ignoreItems
}

func (opts MoveOpts) MonsterFilters() []data.MonsterFilter {
	return opts.monsterFilters
}

func (opts MoveOpts) ClearPathOverride() *int {
	return opts.clearPathOverride
}

func MoveTo(dest data.Position, options ...MoveOption) error {
	// Initialize options
	opts := &MoveOpts{}

	// Apply any provided options
	for _, o := range options {
		o(opts)
	}

	minDistanceToFinishMoving := DistanceToFinishMoving
	if opts.distanceOverride != nil {
		minDistanceToFinishMoving = *opts.distanceOverride
	}

	ctx := context.Get()
	isDragondin := strings.EqualFold(ctx.CharacterCfg.Character.Class, "dragondin")
	ctx.SetLastStep(fmt.Sprintf("MoveTo_%d_%d", dest.X, dest.Y))

	startPos := ctx.Data.PlayerUnit.Position
	movementStartTime := time.Now()
	canTeleport := ctx.Data.CanTeleport()
	movementMethod := "walk"
	if canTeleport {
		movementMethod = "teleport"
	}

	ctx.Logger.Debug("Starting movement",
		slog.Int("fromX", startPos.X),
		slog.Int("fromY", startPos.Y),
		slog.Int("toX", dest.X),
		slog.Int("toY", dest.Y),
		slog.String("movementMethod", movementMethod),
		slog.Int("minDistanceToFinish", minDistanceToFinishMoving),
		slog.Bool("ignoreMonsters", opts.ignoreMonsters),
		slog.Bool("ignoreItems", opts.ignoreItems),
		slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
	)

	opts.ignoreShrines = !ctx.CharacterCfg.Game.InteractWithShrines
	stepLastMonsterCheck := time.Time{}

	blockThreshold := 200 * time.Millisecond
	stuckThreshold := 1500 * time.Millisecond // Base threshold for walking
	if canTeleport {
		// Dynamic threshold based on cast duration + network latency
		// Teleport needs: cast time + server response + safety margin
		castDuration := ctx.Data.PlayerCastDuration()
		pingBuffer := time.Duration(ctx.Data.Game.Ping*2) * time.Millisecond // Round trip latency
		safetyMargin := 200 * time.Millisecond
		stuckThreshold = castDuration*3 + pingBuffer + safetyMargin // ~3 failed teleport attempts before stuck
		// Clamp to reasonable bounds
		if stuckThreshold < 1000*time.Millisecond {
			stuckThreshold = 1000 * time.Millisecond
		} else if stuckThreshold > 3000*time.Millisecond {
			stuckThreshold = 3000 * time.Millisecond
		}
	}
	maxStuckDuration := 15 * time.Second             // Maximum time to be stuck before aborting
	const absoluteMovementTimeout = 30 * time.Second // Absolute timeout for any movement - NEVER reset
	stuckCheckStartTime := time.Now()
	escapeAttempts := 0
	const maxEscapeAttempts = 3

	roundTripReferencePosition := ctx.Data.PlayerUnit.Position
	roundTripCheckStartTime := time.Now()
	const roundTripThreshold = 5 * time.Second // Reduced from 10s for faster detection
	const roundTripMaxRadius = 8
	var previousDistanceToDest float64 = -1 // Track previous distance to destination for progress check

	// Adaptive movement refresh intervals based on ping
	// Adjust polling frequency based on network latency
	var walkDuration time.Duration
	if !ctx.Data.AreaData.Area.IsTown() {
		// In dungeons: faster refresh for combat
		baseMin, baseMax := 300, 350
		pingAdjustment := int(float64(ctx.Data.Game.Ping) * 0.5) // Add half ping to base
		walkDuration = utils.RandomDurationMs(baseMin+pingAdjustment, baseMax+pingAdjustment)
	} else {
		// In town: slower refresh is acceptable
		baseMin, baseMax := 500, 800
		pingAdjustment := int(float64(ctx.Data.Game.Ping) * 0.5)
		walkDuration = utils.RandomDurationMs(baseMin+pingAdjustment, baseMax+pingAdjustment)
	}

	lastRun := time.Time{}
	previousPosition := data.Position{}
	clearPathDist := ctx.CharacterCfg.Character.ClearPathDist
	overrideClearPathDist := false
	blocked := false
	if opts.ClearPathOverride() != nil {
		clearPathDist = *opts.ClearPathOverride()
		overrideClearPathDist = true
	}

	startArea := ctx.Data.PlayerUnit.Area
	lastLogTime := time.Time{}
	const logThrottleInterval = 3 * time.Second

	for {
		// Check absolute timeout FIRST - before any pause or blocking operations
		// This ensures we detect timeout even if the bot is paused for a long time
		elapsed := time.Since(movementStartTime)
		if elapsed > absoluteMovementTimeout {
			ctx.Logger.Error("Movement absolute timeout (30s) exceeded",
				slog.Duration("elapsed", elapsed),
				slog.Int("startX", startPos.X),
				slog.Int("startY", startPos.Y),
				slog.Int("destX", dest.X),
				slog.Int("destY", dest.Y),
				slog.Int("currentX", ctx.Data.PlayerUnit.Position.X),
				slog.Int("currentY", ctx.Data.PlayerUnit.Position.Y),
				slog.Int("escapeAttempts", escapeAttempts),
				slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
			)
			return ErrPlayerStuck
		}

		// Update last step periodically with current movement state
		currentDist := ctx.PathFinder.DistanceFromMe(dest)
		ctx.SetLastStep(fmt.Sprintf("MoveTo_dist%d", currentDist))

		// Log if we're about to pause (priority mismatch)
		if ctx.Priority != ctx.ExecutionPriority {
			ctx.Logger.Debug("Movement paused - priority mismatch",
				slog.Int("priority", int(ctx.Priority)),
				slog.Int("executionPriority", int(ctx.ExecutionPriority)),
				slog.Duration("elapsed", elapsed),
			)
		}

		// Pause if not priority - the timeout check above ensures we don't block indefinitely
		ctx.PauseIfNotPriority()

		// Check if a Drop request is pending and interrupt
		// the current movement early so the Drop flow can take over

		if err := interruptDropIfRequested(); err != nil {
			return err
		}
		ctx.RefreshGameData()

		// If area changed during movement, the destination is no longer valid
		// This happens during portal interactions - area transition means objective achieved
		if ctx.Data.PlayerUnit.Area != startArea {
			ctx.Logger.Debug("Area transition detected during movement",
				slog.String("fromArea", startArea.Area().Name),
				slog.String("toArea", ctx.Data.PlayerUnit.Area.Area().Name),
			)
			// Wait for collision data to be loaded for the new area before returning
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if ctx.Data.AreaData.Grid != nil &&
					ctx.Data.AreaData.Grid.CollisionGrid != nil &&
					len(ctx.Data.AreaData.Grid.CollisionGrid) > 0 {
					// Area transitioned and collision data loaded - movement objective achieved
					ctx.Logger.Debug("Movement completed via area transition",
						slog.Duration("duration", time.Since(movementStartTime)),
					)
					return nil
				}
				utils.Sleep(100)
				ctx.RefreshGameData()
			}
			// If we timeout waiting for collision data, return error
			ctx.Logger.Warn("Area transition detected but collision data failed to load",
				slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
			)
			return fmt.Errorf("area transition detected but collision data failed to load for area %s", ctx.Data.PlayerUnit.Area.Area().Name)
		}

		currentDest := dest

		//Compute distance to destination
		currentDistanceToDest := ctx.PathFinder.DistanceFromMe(currentDest)

		//We've reached the destination, stop movement
		if currentDistanceToDest <= minDistanceToFinishMoving {
			moveDuration := time.Since(movementStartTime)
			// Calculate expected time based on method and distance
			initialDistance := pather.DistanceFromPoint(startPos, dest)
			expectedSeconds := float64(initialDistance) / 10.0 // ~10 tiles/second for teleport
			if !canTeleport {
				expectedSeconds = float64(initialDistance) / 5.0 // ~5 tiles/second for walk
			}
			expectedDuration := time.Duration(expectedSeconds * float64(time.Second))

			// Log slow movements (>10s OR >3x expected time) for performance analysis
			if moveDuration > 10*time.Second || (expectedDuration > 0 && moveDuration > expectedDuration*3) {
				ctx.Logger.Info("Slow movement completed",
					slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
					slog.Int("startX", startPos.X),
					slog.Int("startY", startPos.Y),
					slog.Int("destX", dest.X),
					slog.Int("destY", dest.Y),
					slog.Int("distance", initialDistance),
					slog.Duration("duration", moveDuration),
					slog.Duration("expected", expectedDuration),
					slog.String("method", movementMethod),
					slog.Int("escapeAttempts", escapeAttempts),
					slog.Bool("wasBlocked", blocked),
				)
			} else {
				ctx.Logger.Debug("Movement completed - reached destination",
					slog.Int("finalX", ctx.Data.PlayerUnit.Position.X),
					slog.Int("finalY", ctx.Data.PlayerUnit.Position.Y),
					slog.Int("destX", dest.X),
					slog.Int("destY", dest.Y),
					slog.Int("distance", currentDistanceToDest),
					slog.Duration("duration", moveDuration),
				)
			}
			return nil
		} else if blocked {
			//Add tolerance to reach destination if blocked
			if currentDistanceToDest <= minDistanceToFinishMoving*2 {
				ctx.Logger.Debug("Movement completed - reached destination (blocked tolerance)",
					slog.Int("finalX", ctx.Data.PlayerUnit.Position.X),
					slog.Int("finalY", ctx.Data.PlayerUnit.Position.Y),
					slog.Int("distance", currentDistanceToDest),
					slog.Duration("duration", time.Since(movementStartTime)),
				)
				return nil
			}
		}

		//Check for Doors on path & open them
		if !ctx.Data.CanTeleport() {
			if doorFound, doorObj := ctx.PathFinder.HasDoorBetween(ctx.Data.PlayerUnit.Position, currentDest); doorFound {
				doorToOpen := *doorObj
				ctx.Logger.Debug("Door detected on path, attempting to open",
					slog.Int("doorID", int(doorToOpen.ID)),
					slog.Int("doorX", doorToOpen.Position.X),
					slog.Int("doorY", doorToOpen.Position.Y),
				)
				interactErr := error(nil)
				//Retry a few times (maggot lair slime door fix)
				for attempt := 0; attempt < 5; attempt++ {
					if interactErr = InteractObject(doorToOpen, func() bool {
						door, found := ctx.Data.Objects.FindByID(doorToOpen.ID)
						return found && !door.Selectable
					}); interactErr == nil {
						ctx.Logger.Debug("Door opened successfully",
							slog.Int("doorID", int(doorToOpen.ID)),
							slog.Int("attempts", attempt+1),
						)
						break
					}
					ctx.PathFinder.RandomMovement()
					utils.Sleep(250)
				}
				if interactErr != nil {
					ctx.Logger.Warn("Failed to open door after retries",
						slog.Int("doorID", int(doorToOpen.ID)),
						slog.String("error", interactErr.Error()),
					)
					return interactErr
				}
			}
		}

		//Handle stationary distance (not sure what it refers to...)
		if opts.stationaryMinDistance != nil && opts.stationaryMaxDistance != nil {
			if currentDistanceToDest >= *opts.stationaryMinDistance && currentDistanceToDest <= *opts.stationaryMaxDistance {
				ctx.Logger.Debug("Movement completed - reached stationary distance",
					slog.Int("minDistance", *opts.stationaryMinDistance),
					slog.Int("maxDistance", *opts.stationaryMaxDistance),
					slog.Int("currentDistance", currentDistanceToDest),
					slog.Duration("duration", time.Since(movementStartTime)),
				)
				return nil
			}
		}

		//If teleporting, sleep for the cast duration
		if ctx.Data.CanTeleport() {
			castDuration := ctx.Data.PlayerCastDuration()
			timeSinceLastRun := time.Since(lastRun)

			// Only sleep if we're within cast duration AND lastRun was actually set
			// If lastRun is zero (first iteration) or player is stuck, don't wait
			if !lastRun.IsZero() && timeSinceLastRun < castDuration {
				// Add maximum sleep cap to prevent excessive delays when stuck
				maxSleep := 500 * time.Millisecond
				sleepDuration := castDuration - timeSinceLastRun
				if sleepDuration > maxSleep {
					sleepDuration = maxSleep
				}
				time.Sleep(sleepDuration)
				continue
			}
		}

		//Handle monsters if needed
		if !opts.ignoreMonsters && !ctx.Data.AreaData.Area.IsTown() && (!ctx.Data.CanTeleport() || overrideClearPathDist) && clearPathDist > 0 && time.Since(stepLastMonsterCheck) > stepMonsterCheckInterval {
			stepLastMonsterCheck = time.Now()
			monsterFound := false
			var blockingMonster data.Monster

			for _, m := range ctx.Data.Monsters.Enemies(opts.monsterFilters...) {
				if ctx.Char.ShouldIgnoreMonster(m) {
					continue
				}
				//Check distance first as it is cheaper
				distanceToMonster := ctx.PathFinder.DistanceFromMe(m.Position)
				if distanceToMonster <= clearPathDist {
					//Line of sight second
					if ctx.PathFinder.LineOfSight(ctx.Data.PlayerUnit.Position, m.Position) {
						//Finally door check as it computes path
						if hasDoorBetween, _ := ctx.PathFinder.HasDoorBetween(ctx.Data.PlayerUnit.Position, m.Position); !hasDoorBetween {
							monsterFound = true
							blockingMonster = m
							break
						}
					}
				}
			}

			if monsterFound {
				ctx.Logger.Debug("Monster detected in movement path, aborting",
					slog.Int("monsterID", int(blockingMonster.UnitID)),
					slog.String("monsterName", string(blockingMonster.Name)),
					slog.Int("distance", ctx.PathFinder.DistanceFromMe(blockingMonster.Position)),
					slog.Int("clearPathDist", clearPathDist),
				)
				return ErrMonstersInPath
			}
		}

		currentPosition := ctx.Data.PlayerUnit.Position
		blocked = false

		// Check if we're making progress towards destination (using currentDistanceToDest calculated above)
		isMakingProgress := false
		currentDistanceToDestFloat := float64(currentDistanceToDest)
		if previousDistanceToDest >= 0 {
			// Consider progress if we're getting closer to destination OR staying at same distance
			// (teleport can sometimes land at same distance due to obstacles)
			isMakingProgress = currentDistanceToDestFloat <= previousDistanceToDest
		} else {
			// First iteration, assume progress
			isMakingProgress = true
		}
		previousDistanceToDest = currentDistanceToDestFloat

		//Detect if player is doing round trips around a position for too long and return error if it's the case
		roundTripDistance := utils.CalculateDistance(currentPosition, roundTripReferencePosition)
		if roundTripDistance <= float64(roundTripMaxRadius) {
			timeInRoundtrip := time.Since(roundTripCheckStartTime)

			// If not making progress and stuck in round trip, abort faster
			if !isMakingProgress && timeInRoundtrip > roundTripThreshold {
				ctx.Logger.Warn("Player is doing round trips, aborting movement",
					slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
					slog.Int("destX", currentDest.X),
					slog.Int("destY", currentDest.Y),
					slog.Duration("roundTripTime", timeInRoundtrip),
					slog.Int("roundTripRadius", roundTripMaxRadius),
					slog.Float64("currentDistance", roundTripDistance),
					slog.Float64("distanceToDest", currentDistanceToDestFloat),
					slog.Bool("isMakingProgress", isMakingProgress),
				)
				return ErrPlayerRoundTrip
			} else if timeInRoundtrip > roundTripThreshold/2.0 && !isMakingProgress {
				// Only consider blocked if we're NOT making progress towards destination
				blocked = true
				if time.Since(lastLogTime) > logThrottleInterval {
					ctx.Logger.Debug("Round trip detected (warning phase)",
						slog.Duration("roundTripTime", timeInRoundtrip),
						slog.Int("roundTripRadius", roundTripMaxRadius),
						slog.Float64("currentDistance", roundTripDistance),
						slog.Float64("distanceToDest", currentDistanceToDestFloat),
						slog.Bool("isMakingProgress", isMakingProgress),
					)
					lastLogTime = time.Now()
				}
			}
		} else {
			//Player moved significantly, reset Round Trip detection
			roundTripReferencePosition = currentPosition
			roundTripCheckStartTime = time.Now()
			// Also reset progress tracking when moving significantly
			previousDistanceToDest = -1
		}

		if currentPosition == previousPosition && !ctx.Data.PlayerUnit.States.HasState(state.Stunned) {
			stuckTime := time.Since(stuckCheckStartTime)
			totalStuckTime := time.Since(movementStartTime)

			// Reset lastRun when stuck to prevent sleep loop
			if stuckTime > blockThreshold {
				lastRun = time.Time{} // Reset to allow movement attempts
			}

			// If stuck for too long (15+ seconds), abort immediately
			if totalStuckTime > maxStuckDuration {
				ctx.Logger.Error("Movement stuck for too long, aborting",
					slog.Duration("totalStuckTime", totalStuckTime),
					slog.Duration("currentStuckTime", stuckTime),
					slog.Int("posX", currentPosition.X),
					slog.Int("posY", currentPosition.Y),
					slog.Int("destX", currentDest.X),
					slog.Int("destY", currentDest.Y),
					slog.Int("escapeAttempts", escapeAttempts),
				)
				return ErrPlayerStuck
			}

			if stuckTime > stuckThreshold {
				// Try escape before giving up
				if escapeAttempts < maxEscapeAttempts {
					escapeAttempts++
					ctx.Logger.Debug("Player stuck, attempting escape",
						slog.Int("escapeAttempt", escapeAttempts),
						slog.Int("maxEscapeAttempts", maxEscapeAttempts),
						slog.Duration("stuckTime", stuckTime),
						slog.Duration("totalStuckTime", totalStuckTime),
						slog.Int("posX", currentPosition.X),
						slog.Int("posY", currentPosition.Y),
					)
					ctx.PathFinder.SmartEscapeMovement()
					stuckCheckStartTime = time.Now()
					continue
				}
				// If stuck for too long after multiple escape attempts, abort movement
				ctx.Logger.Warn("Player stuck after escape attempts, aborting movement",
					slog.Int("escapeAttempts", escapeAttempts),
					slog.Duration("stuckTime", stuckTime),
					slog.Duration("totalStuckTime", totalStuckTime),
					slog.Int("posX", currentPosition.X),
					slog.Int("posY", currentPosition.Y),
					slog.Int("destX", currentDest.X),
					slog.Int("destY", currentDest.Y),
				)
				return ErrPlayerStuck
			} else if stuckTime > blockThreshold {
				// Detect blocked after short threshold
				blocked = true
				if time.Since(lastLogTime) > logThrottleInterval {
					ctx.Logger.Debug("Player blocked",
						slog.Duration("blockedTime", stuckTime),
						slog.Int("posX", currentPosition.X),
						slog.Int("posY", currentPosition.Y),
					)
					lastLogTime = time.Now()
				}
			}
		} else {
			// Player moved, reset stuck detection timer and escape attempts
			if escapeAttempts > 0 {
				ctx.Logger.Debug("Player movement resumed, resetting stuck detection",
					slog.Int("previousEscapeAttempts", escapeAttempts),
				)
			}
			stuckCheckStartTime = time.Now()
			escapeAttempts = 0
		}

		if blocked {
			// First check if there's a destructible nearby
			if obj, found := ctx.PathFinder.GetClosestDestructible(ctx.Data.PlayerUnit.Position); found {
				if !obj.Selectable {
					// Already destroyed, move on
					continue
				}
				ctx.Logger.Debug("Destructible obstacle detected, attempting to destroy",
					slog.Int("objectID", int(obj.ID)),
					slog.String("objectName", string(obj.Name)),
					slog.Int("objX", obj.Position.X),
					slog.Int("objY", obj.Position.Y),
				)
				x, y := ui.GameCoordsToScreenCords(obj.Position.X, obj.Position.Y)
				ctx.HID.Click(game.LeftButton, x, y)

				// Adaptive delay for obstacle interaction based on ping
				time.Sleep(time.Millisecond * time.Duration(utils.PingMultiplier(utils.Light, 100)))
			} else if door, found := ctx.PathFinder.GetClosestDoor(ctx.Data.PlayerUnit.Position); found {
				// There's a door really close, try to open it
				doorToOpen := *door
				ctx.Logger.Debug("Door detected nearby, attempting to open",
					slog.Int("doorID", int(doorToOpen.ID)),
					slog.Int("doorX", doorToOpen.Position.X),
					slog.Int("doorY", doorToOpen.Position.Y),
				)
				InteractObject(doorToOpen, func() bool {
					door, found := ctx.Data.Objects.FindByID(door.ID)
					return found && !door.Selectable
				})
			}
			// Note: SmartEscapeMovement is only called when stuckThreshold is reached,
			// not during normal blocked detection to avoid interfering with combat
		}

		//Handle skills for navigation
		if ctx.Data.CanTeleport() {
			if ctx.Data.PlayerUnit.RightSkill != skill.Teleport {
				ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.MustKBForSkill(skill.Teleport))
			}
		} else if isDragondin {
			// Dragondin: keep Conviction active while moving (instead of Vigor).
			// Fallback to Vigor if Conviction isn't bound.
			if kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(skill.Conviction); found {
				if ctx.Data.PlayerUnit.RightSkill != skill.Conviction {
					ctx.HID.PressKeyBinding(kb)
				}
			} else if kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(skill.Vigor); found {
				if ctx.Data.PlayerUnit.RightSkill != skill.Vigor {
					ctx.HID.PressKeyBinding(kb)
				}
			}
		} else if kb, found := ctx.Data.KeyBindings.KeyBindingForSkill(skill.Vigor); found {
			if ctx.Data.PlayerUnit.RightSkill != skill.Vigor {
				ctx.HID.PressKeyBinding(kb)
			}
		}

		//Compute path to reach destination
		path, pathDistance, found := ctx.PathFinder.GetPath(currentDest)
		if !found {
			//Couldn't find path, abort movement
			ctx.Logger.Warn("Path could not be calculated",
				slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
				slog.Int("fromX", ctx.Data.PlayerUnit.Position.X),
				slog.Int("fromY", ctx.Data.PlayerUnit.Position.Y),
				slog.Int("toX", currentDest.X),
				slog.Int("toY", currentDest.Y),
				slog.Int("distance", currentDistanceToDest),
			)
			return ErrNoPath
		} else if len(path) == 0 {
			//Path found but it's empty, consider movement done
			//Not sure if it can happen
			ctx.Logger.Warn("Path found but it's empty",
				slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
				slog.Int("toX", currentDest.X),
				slog.Int("toY", currentDest.Y),
			)
			return nil
		}

		// Throttled debug log with pathfinding info
		if time.Since(lastLogTime) > logThrottleInterval {
			elapsedTime := time.Since(movementStartTime)
			// Use INFO level if movement is taking too long (>10s)
			if elapsedTime > 10*time.Second {
				ctx.Logger.Info("Movement taking long time",
					slog.Int("pathLength", len(path)),
					slog.Int("pathDistance", pathDistance),
					slog.Int("currentDistance", currentDistanceToDest),
					slog.Int("currentX", ctx.Data.PlayerUnit.Position.X),
					slog.Int("currentY", ctx.Data.PlayerUnit.Position.Y),
					slog.Int("destX", currentDest.X),
					slog.Int("destY", currentDest.Y),
					slog.String("movementMethod", movementMethod),
					slog.Bool("blocked", blocked),
					slog.Duration("elapsed", elapsedTime),
					slog.Int("escapeAttempts", escapeAttempts),
					slog.String("playerMode", string(ctx.Data.PlayerUnit.Mode)),
				)
			} else {
				ctx.Logger.Debug("Pathfinding update",
					slog.Int("pathLength", len(path)),
					slog.Int("pathDistance", pathDistance),
					slog.Int("currentDistance", currentDistanceToDest),
					slog.Int("currentX", ctx.Data.PlayerUnit.Position.X),
					slog.Int("currentY", ctx.Data.PlayerUnit.Position.Y),
					slog.Int("destX", currentDest.X),
					slog.Int("destY", currentDest.Y),
					slog.String("movementMethod", movementMethod),
					slog.Bool("blocked", blocked),
					slog.Duration("elapsed", elapsedTime),
				)
			}
			lastLogTime = time.Now()
		}

		//Update values
		lastRun = time.Now()
		previousPosition = ctx.Data.PlayerUnit.Position

		//Perform the movement
		ctx.PathFinder.MoveThroughPath(path, walkDuration)
	}
}
