package action

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/utils"
)

// Optimized constants for public games with high monster density
const (
	// Timeouts - more aggressive for public games
	maxRoomTime            = 15 * time.Second
	maxRoomTimeWithoutPath = 5 * time.Second
	maxActionTime          = 3 * time.Second
	stuckDetectionTime     = 3 * time.Second
	maxIterationTime       = 2 * time.Second

	// Circuit breaker thresholds
	maxConsecutiveFailures   = 3
	maxStagnantIterations    = 4
	maxIterationsWithoutKill = 6

	// Cache TTL
	pathCacheTTL    = 2 * time.Second
	monsterCacheTTL = 1 * time.Second

	// Other player detection
	otherPlayerCheckInterval     = 500 * time.Millisecond
	monsterCountChangeThreshold  = 3
	monsterCountChangeTimeWindow = 500 * time.Millisecond
	otherPlayerClearThreshold    = 0.33 // If <33% of initial monsters remain, others are clearing

	// Pickup and movement
	pickupRadius       = 10
	pickupEveryRooms   = 4
	moveClearRadius    = 20
	maxMonsterDistance = 30
)

// pathCacheEntry stores cached pathfinding results
type pathCacheEntry struct {
	path      bool
	timestamp time.Time
}

// monsterCacheEntry stores cached monster validation results
type monsterCacheEntry struct {
	accessible bool
	timestamp  time.Time
}

// monsterCountSnapshot tracks monster count over time for other player detection
type monsterCountSnapshot struct {
	count int
	time  time.Time
}

// optimizedRoomState tracks room clearing state with caching and optimization
type optimizedRoomState struct {
	// Basic state
	startTime            time.Time
	lastKillTime         time.Time
	lastSuccessfulAction time.Time
	lastProgressCheck    time.Time

	// Monster tracking
	lastMonsterCount     int
	lastMonsterCountTime time.Time
	initialMonsterCount  int
	monsterCountHistory  []monsterCountSnapshot
	maxHistorySize       int

	// Progress tracking
	iterationsWithoutKill     int
	iterationsWithoutProgress int
	consecutiveFailures       int
	stuckDetectionCount       int

	// Target tracking
	skippedMonsters map[data.UnitID]bool
	lastTargetID    data.UnitID
	stagnantCount   int

	// Path and movement
	noPathToCenter bool

	// Caches (thread-safe)
	pathCache    map[data.Position]pathCacheEntry
	monsterCache map[data.UnitID]monsterCacheEntry
	cacheMutex   sync.RWMutex

	// Iteration tracking
	iterationStartTime time.Time
	iterationCount     int
}

// newOptimizedRoomState creates a new optimized room state
func newOptimizedRoomState() *optimizedRoomState {
	return &optimizedRoomState{
		startTime:            time.Now(),
		lastKillTime:         time.Now(),
		lastSuccessfulAction: time.Now(),
		lastProgressCheck:    time.Now(),
		lastMonsterCount:     -1,
		lastMonsterCountTime: time.Now(),
		initialMonsterCount:  -1,
		monsterCountHistory:  make([]monsterCountSnapshot, 0, 10),
		maxHistorySize:       10,
		skippedMonsters:      make(map[data.UnitID]bool),
		pathCache:            make(map[data.Position]pathCacheEntry),
		monsterCache:         make(map[data.UnitID]monsterCacheEntry),
		iterationStartTime:   time.Now(),
	}
}

// ClearCurrentLevelCows clears the cow level optimized for public games with high monster density
func ClearCurrentLevelCows(openChests bool, filter data.MonsterFilter) error {
	ctx := context.Get()
	ctx.SetLastAction("ClearCurrentLevelCows")

	// Safety check: ensure game data is loaded
	if ctx.Data == nil || ctx.PathFinder == nil || ctx.Data.AreaData.Grid == nil {
		ctx.Logger.Warn("Cows: game data not ready, waiting...")
		utils.Sleep(500)
		ctx.RefreshGameData()
		if ctx.Data == nil || ctx.PathFinder == nil || ctx.Data.AreaData.Grid == nil {
			return fmt.Errorf("game data not available after wait")
		}
	}

	// Wait a bit for area to fully load after portal entry
	utils.Sleep(300)
	ctx.RefreshGameData()

	// Get optimized room order
	rooms := ctx.PathFinder.OptimizeRoomsTraverseOrder()

	for i, r := range rooms {
		if errDeath := checkPlayerDeath(ctx); errDeath != nil {
			return errDeath
		}

		// Clear room with optimized logic
		if err := clearRoomCowsOptimized(r, filter, moveClearRadius); err != nil {
			ctx.Logger.Warn("Failed to clear room (cows)", slog.Any("error", err))
		}

		// Periodic item pickup (not every room for performance)
		if (i%pickupEveryRooms == 0) || (i == len(rooms)-1) {
			if err := ItemPickup(pickupRadius); err != nil {
				ctx.Logger.Warn("Failed to pickup items (cows)", slog.Any("error", err))
			}
		}

		// Optional chest opening
		if openChests {
			openChestsInRoom(ctx, r)
		}
	}

	return nil
}

// clearRoomCowsOptimized clears a room with optimized logic for public games
func clearRoomCowsOptimized(room data.Room, filter data.MonsterFilter, moveClearRadius int) error {
	ctx := context.Get()
	ctx.SetLastAction("clearRoomCowsOptimized")

	// Safety check: ensure we have valid game data
	if ctx.Data == nil || ctx.PathFinder == nil || ctx.Data.AreaData.Grid == nil {
		ctx.Logger.Warn("Cows: invalid game data, skipping room")
		return nil
	}

	state := newOptimizedRoomState()

	// Attempt to move to room center with timeout
	moveDeadline := time.Now().Add(5 * time.Second)
	if err := attemptMoveToRoomCenterOptimized(room, moveClearRadius, filter, state, moveDeadline); err != nil {
		ctx.Logger.Debug("Cows: failed moving to room center, clearing from current position",
			slog.Any("error", err))
	}

	// Main clearing loop with aggressive timeouts
	for {
		state.iterationStartTime = time.Now()
		state.iterationCount++

		ctx.PauseIfNotPriority()

		// Refresh game data (but not every iteration for performance)
		if time.Since(state.lastProgressCheck) >= otherPlayerCheckInterval {
			ctx.RefreshGameData()
			state.lastProgressCheck = time.Now()
		}

		// Check player death
		if err := checkPlayerDeath(ctx); err != nil {
			return err
		}

		// Check iteration timeout (prevent single iteration from blocking)
		if time.Since(state.iterationStartTime) > maxIterationTime {
			ctx.Logger.Debug("Cows: iteration timeout, advancing to next room")
			return nil
		}

		// Check room timeout
		if shouldAdvanceToNextRoomOptimized(state) {
			return nil
		}

		// Get valid monsters (with caching)
		monsters := getMonstersInRoomCowsOptimized(room, filter, state)
		if len(monsters) == 0 {
			return nil
		}

		// Update state and detect other players
		updateRoomStateOptimized(state, monsters)
		if shouldAdvanceDueToOtherPlayersOptimized(state, monsters) {
			return nil
		}

		// Check circuit breaker
		if state.consecutiveFailures >= maxConsecutiveFailures {
			ctx.Logger.Debug("Cows: circuit breaker triggered, advancing to next room")
			return nil
		}

		// Find best target (with caching and timeout)
		target := findBestTargetOptimized(ctx, monsters, state, filter)
		if target.UnitID == 0 {
			// No valid target - advance
			return nil
		}

		// Attack target with timeout
		// The high-priority bot loop will handle item pickup automatically
		actionDeadline := time.Now().Add(maxActionTime)
		killed := attackTargetOptimized(ctx, target, state, actionDeadline)

		if killed {
			state.lastKillTime = time.Now()
			state.lastSuccessfulAction = time.Now()
			state.iterationsWithoutKill = 0
			state.consecutiveFailures = 0
		} else {
			state.iterationsWithoutKill++
			// Only count as failure if we actually tried to attack
			if time.Since(state.iterationStartTime) > 100*time.Millisecond {
				state.consecutiveFailures++
			}
		}

		// Cleanup old cache entries periodically
		if state.iterationCount%10 == 0 {
			cleanupCache(state)
		}
	}
}

// attemptMoveToRoomCenterOptimized attempts to move to room center with timeout
func attemptMoveToRoomCenterOptimized(room data.Room, moveClearRadius int, filter data.MonsterFilter, state *optimizedRoomState, deadline time.Time) error {
	ctx := context.Get()

	if time.Now().After(deadline) {
		state.noPathToCenter = true
		return nil
	}

	// Safety check
	if ctx.PathFinder == nil || ctx.Data == nil || ctx.Data.AreaData.Grid == nil {
		state.noPathToCenter = true
		return nil
	}

	path, _, found := ctx.PathFinder.GetClosestWalkablePathIgnoreMonsters(room.GetCenter())
	if !found {
		state.noPathToCenter = true
		// Clear from current position if no path (synchronous, quick operation)
		_ = ClearAreaAroundPlayer(moveClearRadius, filter)
		return nil
	}

	to := data.Position{
		X: path.To().X + ctx.Data.AreaOrigin.X,
		Y: path.To().Y + ctx.Data.AreaOrigin.Y,
	}

	// Try to move to center, but with a timeout wrapper
	// Use a simple approach: call directly but limit the time spent
	startMove := time.Now()
	err := ClearThroughPathIgnoreMonsters(to, moveClearRadius, filter)

	// If it took too long or failed, mark as no path and continue
	if err != nil || time.Since(startMove) > 5*time.Second {
		state.noPathToCenter = true
		return nil
	}

	return err
}

// shouldAdvanceToNextRoomOptimized checks if we should advance based on timeouts
func shouldAdvanceToNextRoomOptimized(state *optimizedRoomState) bool {
	elapsed := time.Since(state.startTime)

	// General timeout
	if elapsed > maxRoomTime {
		return true
	}

	// Shorter timeout if no path to center
	if state.noPathToCenter && elapsed > maxRoomTimeWithoutPath {
		return true
	}

	// Stuck detection - no successful action for too long
	if time.Since(state.lastSuccessfulAction) > stuckDetectionTime {
		state.stuckDetectionCount++
		if state.stuckDetectionCount >= 2 {
			return true
		}
	} else {
		state.stuckDetectionCount = 0
	}

	return false
}

// updateRoomStateOptimized updates room state with optimized tracking
func updateRoomStateOptimized(state *optimizedRoomState, monsters []data.Monster) {
	currentCount := len(monsters)
	now := time.Now()

	// Initialize
	if state.initialMonsterCount == -1 {
		state.initialMonsterCount = currentCount
	}

	// Track progress
	if currentCount == state.lastMonsterCount {
		state.iterationsWithoutProgress++
	} else {
		state.iterationsWithoutProgress = 0
		state.lastMonsterCount = currentCount
		state.lastMonsterCountTime = now
	}

	// Add to history for other player detection
	if len(state.monsterCountHistory) >= state.maxHistorySize {
		state.monsterCountHistory = state.monsterCountHistory[1:]
	}
	state.monsterCountHistory = append(state.monsterCountHistory, monsterCountSnapshot{
		count: currentCount,
		time:  now,
	})
}

// shouldAdvanceDueToOtherPlayersOptimized detects if other players are clearing
func shouldAdvanceDueToOtherPlayersOptimized(state *optimizedRoomState, monsters []data.Monster) bool {
	currentCount := len(monsters)
	now := time.Now()

	// Check for rapid monster reduction (other players killing)
	if state.lastMonsterCount > 0 && currentCount < state.lastMonsterCount {
		reduction := state.lastMonsterCount - currentCount
		timeSinceLastCheck := now.Sub(state.lastMonsterCountTime)

		if reduction >= monsterCountChangeThreshold && timeSinceLastCheck < monsterCountChangeTimeWindow {
			return true
		}
	}

	// Check if most monsters are gone (likely cleared by others)
	if state.initialMonsterCount > 10 {
		remainingRatio := float64(currentCount) / float64(state.initialMonsterCount)
		if remainingRatio < otherPlayerClearThreshold {
			return true
		}
	}

	// Check history for rapid decline
	if len(state.monsterCountHistory) >= 3 {
		recent := state.monsterCountHistory[len(state.monsterCountHistory)-3:]
		oldest := recent[0]
		newest := recent[len(recent)-1]
		timeDiff := newest.time.Sub(oldest.time)
		countDiff := oldest.count - newest.count

		if timeDiff < monsterCountChangeTimeWindow*2 && countDiff >= monsterCountChangeThreshold*2 {
			return true
		}
	}

	// Check if no progress for too long
	if state.iterationsWithoutProgress >= maxStagnantIterations {
		return true
	}

	// Check if no kills for too long
	if time.Since(state.lastKillTime) > stuckDetectionTime {
		state.iterationsWithoutKill++
		if state.iterationsWithoutKill >= maxIterationsWithoutKill {
			return true
		}
	}

	return false
}

// getMonstersInRoomCowsOptimized returns valid monsters with caching
func getMonstersInRoomCowsOptimized(room data.Room, filter data.MonsterFilter, state *optimizedRoomState) []data.Monster {
	ctx := context.Get()

	// Pre-allocate with estimated capacity
	out := make([]data.Monster, 0, 50)

	for _, m := range ctx.Data.Monsters.Enemies(filter) {
		// Fast checks first (cheapest)
		if m.Stats[stat.Life] <= 0 {
			continue
		}

		// Skip pets, mercenaries, and friendly NPCs (allies' summons)
		if m.IsPet() || m.IsMerc() || m.IsGoodNPC() || m.IsSkip() {
			continue
		}

		// Check cache first
		state.cacheMutex.RLock()
		cached, cachedExists := state.monsterCache[m.UnitID]
		state.cacheMutex.RUnlock()

		if cachedExists && time.Since(cached.timestamp) < monsterCacheTTL {
			if !cached.accessible {
				continue
			}
		} else {
			// Validate monster
			// Skip monsters outside room and far from player
			distance := ctx.PathFinder.DistanceFromMe(m.Position)
			if !room.IsInside(m.Position) && distance >= maxMonsterDistance {
				// Cache negative result
				state.cacheMutex.Lock()
				state.monsterCache[m.UnitID] = monsterCacheEntry{
					accessible: false,
					timestamp:  time.Now(),
				}
				state.cacheMutex.Unlock()
				continue
			}

			// Skip monsters on non-walkable positions (ghost monsters)
			if !ctx.Data.AreaData.IsWalkable(m.Position) {
				state.cacheMutex.Lock()
				state.monsterCache[m.UnitID] = monsterCacheEntry{
					accessible: false,
					timestamp:  time.Now(),
				}
				state.cacheMutex.Unlock()
				continue
			}

			// Cache positive result
			state.cacheMutex.Lock()
			state.monsterCache[m.UnitID] = monsterCacheEntry{
				accessible: true,
				timestamp:  time.Now(),
			}
			state.cacheMutex.Unlock()
		}

		out = append(out, m)
	}

	return out
}

// findBestTargetOptimized finds best target with caching and early exit
func findBestTargetOptimized(ctx *context.Status, monsters []data.Monster, state *optimizedRoomState, filter data.MonsterFilter) data.Monster {
	// Check if all monsters are blacklisted
	hasValidMonster := false
	for _, m := range monsters {
		if !state.skippedMonsters[m.UnitID] {
			hasValidMonster = true
			break
		}
	}
	if !hasValidMonster {
		return data.Monster{}
	}

	// Sort by priority
	SortEnemiesByPriority(&monsters)

	// Helper to check accessibility with caching
	isAccessible := func(m data.Monster) bool {
		if state.skippedMonsters[m.UnitID] {
			return false
		}

		if ctx.Char.ShouldIgnoreMonster(m) {
			state.skippedMonsters[m.UnitID] = true
			return false
		}

		// Check path cache
		state.cacheMutex.RLock()
		cached, cachedExists := state.pathCache[m.Position]
		state.cacheMutex.RUnlock()

		var pathFound bool
		if cachedExists && time.Since(cached.timestamp) < pathCacheTTL {
			pathFound = cached.path
		} else {
			// Calculate path
			_, _, found := ctx.PathFinder.GetPathIgnoreMonsters(m.Position)
			pathFound = found

			// Cache result
			state.cacheMutex.Lock()
			state.pathCache[m.Position] = pathCacheEntry{
				path:      found,
				timestamp: time.Now(),
			}
			state.cacheMutex.Unlock()
		}

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

// attackTargetOptimized attacks target with timeout protection
func attackTargetOptimized(ctx *context.Status, target data.Monster, state *optimizedRoomState, deadline time.Time) bool {
	// Check if deadline already passed
	if time.Now().After(deadline) {
		return false
	}

	// Verify monster still exists and is alive
	monster, found := ctx.Data.Monsters.FindByID(target.UnitID)
	if !found || monster.Stats[stat.Life] <= 0 {
		return true // Already dead (possibly killed by other player)
	}

	monsterHPBefore := monster.Stats[stat.Life]

	// Attack sequence - call directly (KillMonsterSequence should handle its own timeouts)
	// Check deadline before starting
	if time.Now().After(deadline) {
		return false
	}

	// Call KillMonsterSequence directly - it should be fast enough
	// If it blocks, the room timeout will catch it
	ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		// Check deadline during sequence
		if time.Now().After(deadline) {
			return 0, false
		}
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
		// The high-priority bot loop will handle item pickup automatically
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

// cleanupCache removes old cache entries
func cleanupCache(state *optimizedRoomState) {
	now := time.Now()
	state.cacheMutex.Lock()
	defer state.cacheMutex.Unlock()

	// Clean path cache
	for pos, entry := range state.pathCache {
		if now.Sub(entry.timestamp) > pathCacheTTL*2 {
			delete(state.pathCache, pos)
		}
	}

	// Clean monster cache
	for id, entry := range state.monsterCache {
		if now.Sub(entry.timestamp) > monsterCacheTTL*2 {
			delete(state.monsterCache, id)
		}
	}
}

// openChestsInRoom opens chests in the room
func openChestsInRoom(ctx *context.Status, room data.Room) {
	for _, o := range ctx.Data.Objects {
		if !room.IsInside(o.Position) || !o.IsChest() || !o.Selectable {
			continue
		}

		// Check if we can use Telekinesis from current position
		chestDistance := ctx.PathFinder.DistanceFromMe(o.Position)
		canUseTK := canUseTelekinesisForObject(o)

		// Only move if not within Telekinesis range (or TK not available)
		telekinesisRange := getTelekinesisRange()
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
