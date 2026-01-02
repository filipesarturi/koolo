package action

import (
	"log/slog"
	"slices"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/utils"
)

// Cow-only tuned clear: optimized for public games with high monster density
func ClearCurrentLevelCows(openChests bool, filter data.MonsterFilter) error {
	ctx := context.Get()
	ctx.SetLastAction("ClearCurrentLevelCows")

	const (
		pickupRadius     = 10 // smaller for cows
		pickupEveryRooms = 4  // pick up every N rooms + last room
		moveClearRadius  = 20 // used by ClearThroughPath
	)

	// Get optimized room order (sequential, avoids unnecessary teleports)
	rooms := ctx.PathFinder.OptimizeRoomsTraverseOrder()

	for i, r := range rooms {
		if errDeath := checkPlayerDeath(ctx); errDeath != nil {
			return errDeath
		}

		// Aggressive "fight-through" movement to room center (no monster filter path-avoidance)
		if err := clearRoomCows(r, filter, moveClearRadius); err != nil {
			ctx.Logger.Warn("Failed to clear room (cows)", slog.Any("error", err))
		}

		// Don't loot-vacuum after every room
		if (i%pickupEveryRooms == 0) || (i == len(rooms)-1) {
			if err := ItemPickup(pickupRadius); err != nil {
				ctx.Logger.Warn("Failed to pickup items (cows)", slog.Any("error", err))
			}
		}

		// Optional chest opening (usually false for speed)
		if openChests {
			for _, o := range ctx.Data.Objects {
				if r.IsInside(o.Position) && o.IsChest() && o.Selectable {
					// Check if we can use Telekinesis from current position
					chestDistance := ctx.PathFinder.DistanceFromMe(o.Position)
					canUseTK := canUseTelekinesisForObject(o)

					// Only move if not within Telekinesis range (or TK not available)
					if !canUseTK || chestDistance > telekinesisRange {
						if err := MoveToCoords(o.Position); err != nil {
							continue
						}
					}

					_ = InteractObject(o, func() bool {
						chest, _ := ctx.Data.Objects.FindByID(o.ID)
						return !chest.Selectable
					})
					utils.Sleep(250)
				}
			}
		}
	}

	return nil
}

// roomState tracks the state of room clearing for optimization
type roomState struct {
	startTime              time.Time
	lastKillTime           time.Time
	lastMonsterCount       int
	lastMonsterCountTime   time.Time
	initialMonsterCount    int
	iterationsWithoutKill  int
	iterationsWithoutProgress int
	skippedMonsters        map[data.UnitID]bool
	lastTargetID           data.UnitID
	stagnantCount          int
	noPathToCenter         bool
}

const (
	maxRoomTimeGeneral        = 20 * time.Second
	maxRoomTimeWithoutPath    = 8 * time.Second
	maxIterationsWithoutKill  = 8
	maxIterationsWithoutProgress = 5
	maxStagnantIterations     = 5
	fastMonsterReductionThreshold = 5
	monsterReductionTimeWindow = 2 * time.Second
	noKillTimeout            = 5 * time.Second
)

func clearRoomCows(room data.Room, filter data.MonsterFilter, moveClearRadius int) error {
	ctx := context.Get()
	ctx.SetLastAction("clearRoomCows")

	state := &roomState{
		startTime:            time.Now(),
		lastKillTime:         time.Now(),
		lastMonsterCount:     -1,
		lastMonsterCountTime: time.Now(),
		initialMonsterCount:  -1,
		skippedMonsters:      make(map[data.UnitID]bool),
	}

	// Try to move to room center first
	if err := attemptMoveToRoomCenter(room, moveClearRadius, filter, state); err != nil {
		ctx.Logger.Debug("Cows: failed moving to room center, clearing from current position",
			slog.Any("error", err))
	}

	pickupOnKill := ctx.CharacterCfg.Character.PickupOnKill

	// Main clearing loop - optimized for public games
	for {
		ctx.PauseIfNotPriority()
		ctx.RefreshGameData()

		if err := checkPlayerDeath(ctx); err != nil {
			return err
		}

		// Check timeouts - advance to next room if exceeded
		if shouldAdvanceToNextRoom(state) {
			return nil
		}

		// Get valid monsters in room
		monsters := getMonstersInRoomCows(room, filter)
		if len(monsters) == 0 {
			return nil
		}

		// Update state tracking
		updateRoomState(state, monsters)

		// Check if should advance due to other players clearing
		if shouldAdvanceDueToOtherPlayers(state, monsters) {
			return nil
		}

		// Find and attack target
		target := findBestTarget(ctx, monsters, state, filter)
		if target.UnitID == 0 {
			// No valid target - advance to next room
			return nil
		}

		// Attack target
		if killed := attackTarget(ctx, target, state, pickupOnKill); killed {
			state.lastKillTime = time.Now()
			state.iterationsWithoutKill = 0
		}
	}
}

// attemptMoveToRoomCenter tries to move to room center, returns error if path not found
func attemptMoveToRoomCenter(room data.Room, moveClearRadius int, filter data.MonsterFilter, state *roomState) error {
	ctx := context.Get()
	
	path, _, found := ctx.PathFinder.GetClosestWalkablePathIgnoreMonsters(room.GetCenter())
	if !found {
		state.noPathToCenter = true
		// Clear from current position if no path
		ClearAreaAroundPlayer(moveClearRadius, filter)
		return nil
	}

	to := data.Position{
		X: path.To().X + ctx.Data.AreaOrigin.X,
		Y: path.To().Y + ctx.Data.AreaOrigin.Y,
	}

	return ClearThroughPathIgnoreMonsters(to, moveClearRadius, filter)
}

// shouldAdvanceToNextRoom checks if we should advance to next room based on timeouts
func shouldAdvanceToNextRoom(state *roomState) bool {
	elapsed := time.Since(state.startTime)

	// General timeout for any room
	if elapsed > maxRoomTimeGeneral {
		return true
	}

	// Shorter timeout if no path to center
	if state.noPathToCenter && elapsed > maxRoomTimeWithoutPath {
		return true
	}

	return false
}

// updateRoomState updates room state tracking
func updateRoomState(state *roomState, monsters []data.Monster) {
	currentCount := len(monsters)

	// Initialize tracking
	if state.initialMonsterCount == -1 {
		state.initialMonsterCount = currentCount
	}

	// Track progress
	if currentCount == state.lastMonsterCount {
		state.iterationsWithoutProgress++
	} else {
		state.iterationsWithoutProgress = 0
		state.lastMonsterCount = currentCount
	}
}

// shouldAdvanceDueToOtherPlayers checks if other players are clearing and we should advance
func shouldAdvanceDueToOtherPlayers(state *roomState, monsters []data.Monster) bool {
	currentCount := len(monsters)
	now := time.Now()

	// Check for rapid monster reduction (other players killing)
	if state.lastMonsterCount > 0 && currentCount < state.lastMonsterCount {
		reduction := state.lastMonsterCount - currentCount
		timeSinceLastCheck := now.Sub(state.lastMonsterCountTime)
		
		if reduction >= fastMonsterReductionThreshold && timeSinceLastCheck < monsterReductionTimeWindow {
			return true
		}
	}
	state.lastMonsterCountTime = now

	// Check if most monsters are gone (likely cleared by others)
	if state.initialMonsterCount > 10 && currentCount < state.initialMonsterCount/3 {
		return true
	}

	// Check if no progress for too long
	if state.iterationsWithoutProgress >= maxIterationsWithoutProgress {
		return true
	}

	// Check if no kills for too long
	if time.Since(state.lastKillTime) > noKillTimeout {
		state.iterationsWithoutKill++
		if state.iterationsWithoutKill >= maxIterationsWithoutKill {
			return true
		}
	}

	return false
}

// findBestTarget finds the best target to attack, considering accessibility and priority
func findBestTarget(ctx *context.Status, monsters []data.Monster, state *roomState, filter data.MonsterFilter) data.Monster {
	// Check if all monsters are blacklisted
	if !slices.ContainsFunc(monsters, func(m data.Monster) bool {
		return !state.skippedMonsters[m.UnitID]
	}) {
		return data.Monster{}
	}

	SortEnemiesByPriority(&monsters)

	// Helper function to check if monster is accessible
	isAccessible := func(m data.Monster) bool {
		if state.skippedMonsters[m.UnitID] {
			return false
		}
		if ctx.Char.ShouldIgnoreMonster(m) {
			state.skippedMonsters[m.UnitID] = true
			return false
		}
		_, _, pathFound := ctx.PathFinder.GetPathIgnoreMonsters(m.Position)
		if !pathFound && !ctx.Data.CanTeleport() {
			state.skippedMonsters[m.UnitID] = true
			return false
		}
		return true
	}

	// First, try to find a raiser (priority target)
	target, found := findFirst(monsters, func(m data.Monster) bool {
		return isAccessible(m) && m.IsMonsterRaiser()
	})

	// If no raiser found, get first accessible target
	if !found {
		target, found = findFirst(monsters, isAccessible)
	}

	// If no accessible monsters and can't teleport, advance
	if !found && !ctx.Data.CanTeleport() {
		return data.Monster{}
	}

	// Check for stagnation on same target
	if target.UnitID == state.lastTargetID {
		state.stagnantCount++
		if state.stagnantCount >= maxStagnantIterations {
			// Blacklist and return empty to find new target
			state.skippedMonsters[target.UnitID] = true
			state.stagnantCount = 0
			return data.Monster{}
		}
	} else {
		state.stagnantCount = 0
		state.lastTargetID = target.UnitID
	}

	return target
}

// attackTarget attacks the target and returns true if killed
func attackTarget(ctx *context.Status, target data.Monster, state *roomState, pickupOnKill bool) bool {
	monsterHPBefore := target.Stats[stat.Life]

	// Attack sequence
	ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		m, ok := d.Monsters.FindByID(target.UnitID)
		if ok && m.Stats[stat.Life] > 0 {
			return target.UnitID, true
		}
		return 0, false
	}, nil)

	// Check result
	ctx.RefreshGameData()
	m, stillExists := ctx.Data.Monsters.FindByID(target.UnitID)
	
	if !stillExists || m.Stats[stat.Life] <= 0 {
		// Monster killed
		if pickupOnKill {
			_ = ItemPickup(pickupOnKillRadius)
		}
		return true
	}

	// Check if unkillable (HP didn't decrease)
	if m.Stats[stat.Life] >= monsterHPBefore {
		ctx.Logger.Debug("Cows: monster HP unchanged after attack, blacklisting",
			slog.Any("targetID", target.UnitID))
		state.skippedMonsters[target.UnitID] = true
	}

	return false
}

// getMonstersInRoomCows returns alive monsters in room or near player
func getMonstersInRoomCows(room data.Room, filter data.MonsterFilter) []data.Monster {
	ctx := context.Get()

	out := make([]data.Monster, 0)
	for _, m := range ctx.Data.Monsters.Enemies(filter) {
		// Skip dead monsters
		if m.Stats[stat.Life] <= 0 {
			continue
		}

		// Skip monsters outside room and far from player
		if !room.IsInside(m.Position) && ctx.PathFinder.DistanceFromMe(m.Position) >= 30 {
			continue
		}

		// Skip monsters on non-walkable positions (ghost monsters)
		if !ctx.Data.AreaData.IsWalkable(m.Position) {
			continue
		}

		out = append(out, m)
	}
	return out
}

// findFirst finds the first element in slice matching the predicate
func findFirst[T any](slice []T, predicate func(T) bool) (T, bool) {
	for _, item := range slice {
		if predicate(item) {
			return item, true
		}
	}
	var zero T
	return zero, false
}


