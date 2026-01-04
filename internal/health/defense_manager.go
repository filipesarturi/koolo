package health

import (
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/data/state"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/pather"
	"github.com/hectorgimenez/koolo/internal/utils"
)

// DefenseManager monitors player state and takes defensive actions when in danger
type DefenseManager struct {
	data        *game.Data
	beltManager *BeltManager
	pathFinder  *pather.PathFinder
	logger      *slog.Logger

	// Position tracking
	lastPosition          data.Position
	lastPositionCheckTime time.Time
	stationaryStartTime   time.Time

	// HP tracking
	lastHP         int
	lastHPCheckTime time.Time
	damageStartTime time.Time

	// Attack tracking
	lastAttackTargetID          data.UnitID
	lastAttackTargetHP          int
	lastAttackTime              time.Time
	ineffectiveAttackStartTime  time.Time
}

// NewDefenseManager creates a new DefenseManager instance
func NewDefenseManager(bm *BeltManager, data *game.Data, pathFinder *pather.PathFinder, logger *slog.Logger) *DefenseManager {
	return &DefenseManager{
		beltManager: bm,
		data:        data,
		pathFinder:  pathFinder,
		logger:      logger,
	}
}

// CheckDefense monitors player state and takes defensive actions when needed
func (dm *DefenseManager) CheckDefense() error {
	cfg := dm.data.CharacterCfg.Defense

	// Early returns - check exit conditions first
	if !cfg.Enabled {
		return nil
	}

	if dm.data.PlayerUnit.Area.IsTown() {
		return nil
	}

	if dm.data.PlayerUnit.IsDead() {
		return nil
	}

	// Cache values used multiple times
	currentHP := dm.data.PlayerUnit.HPPercent()
	currentPos := dm.data.PlayerUnit.Position
	isPoisoned := dm.data.PlayerUnit.States.HasState(state.Poison)

	// Check if player is stationary and taking damage
	if dm.isStationaryAndTakingDamage(currentPos, currentHP, isPoisoned) {
		return dm.handleStationaryDamage(currentHP)
	}

	// Check if player is attacking ineffectively
	if dm.isAttackingIneffectively(currentHP) {
		return dm.handleIneffectiveAttack(currentHP)
	}

	// Reset tracking if conditions normalized
	dm.resetTrackingIfNormalized(currentPos, currentHP)

	return nil
}

// isStationaryAndTakingDamage checks if player is stationary and taking damage (excluding poison)
func (dm *DefenseManager) isStationaryAndTakingDamage(currentPos data.Position, currentHP int, isPoisoned bool) bool {
	cfg := dm.data.CharacterCfg.Defense
	const minMovementThreshold = 5.0 // units

	now := time.Now()
	stationaryThreshold := time.Duration(cfg.StationaryThresholdSeconds) * time.Second
	damageThreshold := time.Duration(cfg.DamageThresholdSeconds) * time.Second

	// Check if position changed significantly
	distance := utils.CalculateDistance(dm.lastPosition, currentPos)
	if distance > minMovementThreshold {
		// Player moved, reset stationary tracking
		dm.lastPosition = currentPos
		dm.lastPositionCheckTime = now
		dm.stationaryStartTime = time.Time{}
		return false
	}

	// Check if stationary for required time
	if dm.stationaryStartTime.IsZero() {
		if distance < minMovementThreshold {
			dm.stationaryStartTime = now
		}
		return false
	}

	if time.Since(dm.stationaryStartTime) < stationaryThreshold {
		return false
	}

	// Player is stationary, now check for damage
	if dm.lastHPCheckTime.IsZero() {
		dm.lastHP = currentHP
		dm.lastHPCheckTime = now
		return false
	}

	// Only check damage if enough time has passed
	if time.Since(dm.lastHPCheckTime) < 100*time.Millisecond {
		return false
	}

	// Check if HP decreased (taking damage)
	if currentHP < dm.lastHP && !isPoisoned {
		if dm.damageStartTime.IsZero() {
			dm.damageStartTime = now
		}
		
		if time.Since(dm.damageStartTime) >= damageThreshold {
			dm.lastHP = currentHP
			dm.lastHPCheckTime = now
			return true
		}
	} else {
		// HP not decreasing or is poison, reset damage tracking
		dm.damageStartTime = time.Time{}
	}

	dm.lastHP = currentHP
	dm.lastHPCheckTime = now
	return false
}

// isAttackingIneffectively checks if player is attacking but not dealing damage
func (dm *DefenseManager) isAttackingIneffectively(currentHP int) bool {
	cfgDefense := dm.data.CharacterCfg.Defense

	now := time.Now()
	threshold := time.Duration(cfgDefense.IneffectiveAttackThresholdSeconds) * time.Second

	// Find current attack target (monster being attacked)
	var currentTarget *data.Monster
	for _, monster := range dm.data.Monsters.Enemies() {
		if monster.Stats[stat.Life] <= 0 {
			continue
		}
		// Check if this monster is likely being attacked (close enough)
		distance := dm.pathFinder.DistanceFromMe(monster.Position)
		if distance <= 15 { // reasonable attack range
			if currentTarget == nil || distance < dm.pathFinder.DistanceFromMe(currentTarget.Position) {
				currentTarget = &monster
			}
		}
	}

	if currentTarget == nil {
		// No target, reset tracking
		dm.lastAttackTargetID = 0
		dm.ineffectiveAttackStartTime = time.Time{}
		return false
	}

	// Check if we're attacking the same target
	if dm.lastAttackTargetID != currentTarget.UnitID {
		// New target, reset tracking
		dm.lastAttackTargetID = currentTarget.UnitID
		dm.lastAttackTargetHP = currentTarget.Stats[stat.Life]
		dm.lastAttackTime = now
		dm.ineffectiveAttackStartTime = time.Time{}
		return false
	}

	// Same target, check if we're dealing damage
	currentTargetHP := currentTarget.Stats[stat.Life]
	
	// Only check if enough time has passed
	if time.Since(dm.lastAttackTime) < 200*time.Millisecond {
		return false
	}

	if currentTargetHP < dm.lastAttackTargetHP {
		// Dealing damage, reset tracking
		dm.lastAttackTargetHP = currentTargetHP
		dm.lastAttackTime = now
		dm.ineffectiveAttackStartTime = time.Time{}
		return false
	}

	// Not dealing damage, start/continue tracking
	if dm.ineffectiveAttackStartTime.IsZero() {
		dm.ineffectiveAttackStartTime = now
	}

	if time.Since(dm.ineffectiveAttackStartTime) >= threshold {
		dm.lastAttackTargetHP = currentTargetHP
		dm.lastAttackTime = now
		return true
	}

	return false
}

// handleStationaryDamage handles the case when player is stationary and taking damage
func (dm *DefenseManager) handleStationaryDamage(currentHP int) error {
	cfgDefense := dm.data.CharacterCfg.Defense

	dm.logger.Warn("Player stationary and taking damage, taking defensive action")

	// Always use aggressive actions when stationary and taking damage
	canTeleport := dm.data.CanTeleport()

	if canTeleport {
		// Try to teleport to a safe position
		if safePos, found := dm.findSafePositionForBuff(10, 20); found {
			dm.logger.Info("Teleporting to safe position")
			// Use pathFinder to move to safe position
			if path, _, found := dm.pathFinder.GetPathIgnoreMonsters(safePos); found && len(path) > 0 {
				dm.pathFinder.MoveThroughPath(path, 200*time.Millisecond)
			}
			return nil
		}
	}

	// If can't teleport or no safe position found, use escape movement
	dm.logger.Info("Using escape movement")
	dm.pathFinder.SmartEscapeMovement()

	// Use rejuvenation potion if HP is low
	if currentHP <= cfgDefense.LowHPThreshold {
		if dm.beltManager.DrinkPotion(data.RejuvenationPotion, false) {
			dm.logger.Info("Used rejuvenation potion")
		}
	}

	return nil
}

// handleIneffectiveAttack handles the case when player is attacking but not dealing damage
func (dm *DefenseManager) handleIneffectiveAttack(currentHP int) error {
	cfgDefense := dm.data.CharacterCfg.Defense

	isLowHP := currentHP < cfgDefense.LowHPThreshold

	if isLowHP {
		// HP is low, use aggressive actions
		dm.logger.Warn("Player attacking ineffectively with low HP, taking defensive action")

		canTeleport := dm.data.CanTeleport()
		if canTeleport {
			if safePos, found := dm.findSafePositionForBuff(10, 20); found {
				dm.logger.Info("Teleporting to safe position")
				// Use pathFinder to move to safe position
				if path, _, found := dm.pathFinder.GetPathIgnoreMonsters(safePos); found && len(path) > 0 {
					dm.pathFinder.MoveThroughPath(path, 200*time.Millisecond)
				}
				return nil
			}
		}

		dm.pathFinder.SmartEscapeMovement()

		// Use rejuvenation potion
		if dm.beltManager.DrinkPotion(data.RejuvenationPotion, false) {
			dm.logger.Info("Used rejuvenation potion")
		}
	} else {
		// HP is normal, just reposition
		dm.logger.Info("Player attacking ineffectively, repositioning")

		// Find closest enemy to reposition from
		hasEnemy, closestMonster := dm.isAnyEnemyAroundPlayer(15)
		if !hasEnemy {
			return nil
		}

		// Find safe position for repositioning
		safePos, found := dm.findSafePosition(closestMonster, 10, 15, 5, 20)
		if found {
			dm.logger.Info("Repositioning to new attack position")
			// Use pathFinder to move to safe position
			if path, _, found := dm.pathFinder.GetPathIgnoreMonsters(safePos); found && len(path) > 0 {
				dm.pathFinder.MoveThroughPath(path, 200*time.Millisecond)
			}
			return nil
		}
	}

	return nil
}

// resetTrackingIfNormalized resets tracking when conditions normalize
func (dm *DefenseManager) resetTrackingIfNormalized(currentPos data.Position, currentHP int) {
	const minMovementThreshold = 5.0

	// Reset stationary tracking if player moved
	distance := utils.CalculateDistance(dm.lastPosition, currentPos)
	if distance > minMovementThreshold {
		dm.stationaryStartTime = time.Time{}
		dm.damageStartTime = time.Time{}
	}

	// Update position tracking
	dm.lastPosition = currentPos
	dm.lastPositionCheckTime = time.Now()
}

// isAnyEnemyAroundPlayer checks if there are any enemies around the player
func (dm *DefenseManager) isAnyEnemyAroundPlayer(radius int) (bool, data.Monster) {
	playerPos := dm.data.PlayerUnit.Position
	for _, monster := range dm.data.Monsters.Enemies() {
		if monster.Stats[stat.Life] <= 0 {
			continue
		}
		distance := pather.DistanceFromPoint(playerPos, monster.Position)
		if distance <= radius {
			return true, monster
		}
	}
	return false, data.Monster{}
}

// getDistanceFromClosestEnemy returns the distance to the closest enemy from a position
func (dm *DefenseManager) getDistanceFromClosestEnemy(pos data.Position) float64 {
	minDistance := math.MaxFloat64
	for _, monster := range dm.data.Monsters.Enemies() {
		if monster.Stats[stat.Life] <= 0 {
			continue
		}
		distance := pather.DistanceFromPoint(pos, monster.Position)
		if float64(distance) < minDistance {
			minDistance = float64(distance)
		}
	}
	return minDistance
}

// findSafePositionForBuff finds a safe position away from all monsters
func (dm *DefenseManager) findSafePositionForBuff(minSafeDistance int, maxSearchDistance int) (data.Position, bool) {
	playerPos := dm.data.PlayerUnit.Position

	// If there are no monsters nearby, current position is safe
	closestMonsterDist := dm.getDistanceFromClosestEnemy(playerPos)
	if closestMonsterDist >= float64(minSafeDistance) {
		return playerPos, true
	}

	// Generate candidate positions in a circle around the player
	candidatePositions := []data.Position{}

	// Find direction away from the closest monster
	var closestMonster data.Monster
	minDist := math.MaxFloat64
	for _, monster := range dm.data.Monsters.Enemies() {
		if monster.Stats[stat.Life] <= 0 {
			continue
		}
		dist := float64(pather.DistanceFromPoint(playerPos, monster.Position))
		if dist < minDist {
			minDist = dist
			closestMonster = monster
		}
	}

	// Try positions in the opposite direction from the closest monster first
	if minDist < math.MaxFloat64 {
		vectorX := playerPos.X - closestMonster.Position.X
		vectorY := playerPos.Y - closestMonster.Position.Y

		length := math.Sqrt(float64(vectorX*vectorX + vectorY*vectorY))
		if length > 0 {
			for distance := minSafeDistance; distance <= maxSearchDistance; distance += 3 {
				normalizedX := int(float64(vectorX) / length * float64(distance))
				normalizedY := int(float64(vectorY) / length * float64(distance))

				for offsetX := -2; offsetX <= 2; offsetX++ {
					for offsetY := -2; offsetY <= 2; offsetY++ {
						candidatePos := data.Position{
							X: playerPos.X + normalizedX + offsetX,
							Y: playerPos.Y + normalizedY + offsetY,
						}

						if dm.data.AreaData.IsWalkable(candidatePos) {
							candidatePositions = append(candidatePositions, candidatePos)
						}
					}
				}
			}
		}
	}

	// Generate positions in a full circle for more options
	for angle := 0; angle < 360; angle += 15 {
		radians := float64(angle) * math.Pi / 180

		for distance := minSafeDistance; distance <= maxSearchDistance; distance += 3 {
			dx := int(math.Cos(radians) * float64(distance))
			dy := int(math.Sin(radians) * float64(distance))

			candidatePos := data.Position{
				X: playerPos.X + dx,
				Y: playerPos.Y + dy,
			}

			if dm.data.AreaData.IsWalkable(candidatePos) {
				candidatePositions = append(candidatePositions, candidatePos)
			}
		}
	}

	if len(candidatePositions) == 0 {
		return data.Position{}, false
	}

	// Evaluate candidate positions - find the one with maximum distance from monsters
	type scoredPosition struct {
		pos   data.Position
		score float64
	}

	scoredPositions := []scoredPosition{}

	for _, pos := range candidatePositions {
		// Check if we can path to this position
		_, _, pathFound := dm.pathFinder.GetPathIgnoreMonsters(pos)
		if !pathFound {
			continue
		}

		// Calculate minimum distance to any monster from this position
		minMonsterDist := dm.getDistanceFromClosestEnemy(pos)

		// Skip positions that are too close to monsters
		if minMonsterDist < float64(minSafeDistance) {
			continue
		}

		// Distance from player (prefer closer positions to minimize travel time)
		distanceFromPlayer := pather.DistanceFromPoint(pos, playerPos)

		// Score: prioritize safety (distance from monsters) but also consider travel time
		score := minMonsterDist*2.0 - float64(distanceFromPlayer)*0.5

		scoredPositions = append(scoredPositions, scoredPosition{
			pos:   pos,
			score: score,
		})
	}

	// Sort by score (highest first)
	sort.Slice(scoredPositions, func(i, j int) bool {
		return scoredPositions[i].score > scoredPositions[j].score
	})

	if len(scoredPositions) > 0 {
		return scoredPositions[0].pos, true
	}

	return data.Position{}, false
}

// findSafePosition finds a safe position for attacking a target monster
func (dm *DefenseManager) findSafePosition(targetMonster data.Monster, dangerDistance int, safeDistance int, minAttackDistance int, maxAttackDistance int) (data.Position, bool) {
	playerPos := dm.data.PlayerUnit.Position

	// Define a stricter minimum safe distance from monsters
	minSafeMonsterDistance := int(math.Floor((float64(safeDistance) + float64(dangerDistance)) / 2))

	// Generate candidate positions in a circle around the player
	candidatePositions := []data.Position{}

	// First try positions in the opposite direction from the dangerous monster
	vectorX := playerPos.X - targetMonster.Position.X
	vectorY := playerPos.Y - targetMonster.Position.Y

	// Normalize the vector
	length := math.Sqrt(float64(vectorX*vectorX + vectorY*vectorY))
	if length > 0 {
		normalizedX := int(float64(vectorX) / length * float64(safeDistance))
		normalizedY := int(float64(vectorY) / length * float64(safeDistance))

		// Add positions in the opposite direction with some variation
		for offsetX := -3; offsetX <= 3; offsetX++ {
			for offsetY := -3; offsetY <= 3; offsetY++ {
				candidatePos := data.Position{
					X: playerPos.X + normalizedX + offsetX,
					Y: playerPos.Y + normalizedY + offsetY,
				}

				if dm.data.AreaData.IsWalkable(candidatePos) {
					candidatePositions = append(candidatePositions, candidatePos)
				}
			}
		}
	}

	// Generate positions in a circle with smaller angle increments
	for angle := 0; angle < 360; angle += 5 {
		radians := float64(angle) * math.Pi / 180

		for distance := safeDistance; distance <= safeDistance+10; distance += 3 {
			dx := int(math.Cos(radians) * float64(distance))
			dy := int(math.Sin(radians) * float64(distance))

			candidatePos := data.Position{
				X: playerPos.X + dx,
				Y: playerPos.Y + dy,
			}

			if dm.data.AreaData.IsWalkable(candidatePos) {
				candidatePositions = append(candidatePositions, candidatePos)
			}
		}
	}

	if len(candidatePositions) == 0 {
		return data.Position{}, false
	}

	// Evaluate and score positions
	type scoredPosition struct {
		pos   data.Position
		score float64
	}

	scoredPositions := []scoredPosition{}

	for _, pos := range candidatePositions {
		// Check if this position has line of sight to target
		if !dm.pathFinder.LineOfSight(pos, targetMonster.Position) {
			continue
		}

		// Calculate minimum distance to any monster
		minMonsterDist := dm.getDistanceFromClosestEnemy(pos)

		// Strictly skip positions that are too close to monsters
		if minMonsterDist < float64(minSafeMonsterDistance) {
			continue
		}

		// Calculate distance to target monster
		targetDistance := pather.DistanceFromPoint(pos, targetMonster.Position)
		distanceFromPlayer := pather.DistanceFromPoint(pos, playerPos)

		// Calculate attack range score
		attackRangeScore := 0.0
		if targetDistance >= minAttackDistance && targetDistance <= maxAttackDistance {
			attackRangeScore = 10.0
		} else {
			attackRangeScore = -math.Abs(float64(targetDistance) - float64(minAttackDistance+maxAttackDistance)/2.0)
		}

		// Final score calculation
		score := minMonsterDist*3.0 + attackRangeScore*2.0 - float64(distanceFromPlayer)*0.5

		// Extra bonus for positions that are very safe
		if minMonsterDist > float64(dangerDistance) {
			score += 5.0
		}

		scoredPositions = append(scoredPositions, scoredPosition{
			pos:   pos,
			score: score,
		})
	}

	// Sort positions by score (highest first)
	sort.Slice(scoredPositions, func(i, j int) bool {
		return scoredPositions[i].score > scoredPositions[j].score
	})

	// Return the best position if we found any
	if len(scoredPositions) > 0 {
		return scoredPositions[0].pos, true
	}

	return data.Position{}, false
}
