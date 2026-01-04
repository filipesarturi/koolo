package action

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/pather"
)

// Static maps for O(1) lookups
var (
	priorityMonstersMap = map[npc.ID]bool{
		npc.FallenShaman:    true,
		npc.CarverShaman:    true,
		npc.DevilkinShaman:  true,
		npc.DarkShaman:      true,
		npc.WarpedShaman:    true,
		npc.MummyGenerator:  true,
		npc.BaalSubjectMummy: true,
		npc.FetishShaman:    true,
		npc.BlackSoul:       true,
		npc.BlackSoul2:      true,
		npc.BurningSoul:     true,
		npc.BurningSoul2:    true,
	}

	soulNPCsMap = map[npc.ID]bool{
		npc.BlackSoul:    true,
		npc.BlackSoul2:   true,
		npc.BurningSoul:  true,
		npc.BurningSoul2: true,
	}

	dangerousNPCsMap = map[npc.ID]bool{
		npc.UndeadStygianDoll:  true,
		npc.UndeadStygianDoll2: true,
		npc.UndeadSoulKiller:   true,
		npc.UndeadSoulKiller2:  true,
		npc.BlackSoul:          true,
		npc.BlackSoul2:         true,
		npc.BurningSoul:        true,
		npc.BurningSoul2:       true,
	}
)

func ClearAreaAroundPlayer(radius int, filter data.MonsterFilter) error {
	return ClearAreaAroundPosition(context.Get().Data.PlayerUnit.Position, radius, filter)
}

func IsPriorityMonster(m data.Monster) bool {
	return priorityMonstersMap[m.Name]
}

func SortEnemiesByPriority(enemies *[]data.Monster) {
	ctx := context.Get()
	
	// Pre-calculate priority and distance for all enemies to avoid redundant calculations
	type enemyData struct {
		monster   data.Monster
		isPriority bool
		distance   int
	}
	
	enemyDataList := make([]enemyData, len(*enemies))
	for i := range *enemies {
		enemyDataList[i] = enemyData{
			monster:    (*enemies)[i],
			isPriority: priorityMonstersMap[(*enemies)[i].Name],
			distance:   ctx.PathFinder.DistanceFromMe((*enemies)[i].Position),
		}
	}
	
	sort.Slice(enemyDataList, func(i, j int) bool {
		ei := enemyDataList[i]
		ej := enemyDataList[j]

		if ei.distance > 2 && ej.distance > 2 {
			if ei.isPriority && !ej.isPriority {
				return true
			} else if !ei.isPriority && ej.isPriority {
				return false
			}
		}

		return ei.distance < ej.distance
	})
	
	// Update original slice with sorted order
	for i := range enemyDataList {
		(*enemies)[i] = enemyDataList[i].monster
	}
}

// FindSoulsInRange finds all souls within the specified radius from the player
// Exported function for use in other packages
func FindSoulsInRange(radius int) []data.Monster {
	return findSoulsInRange(radius)
}

// findSoulsInRange finds all souls within the specified radius from the player
func findSoulsInRange(radius int) []data.Monster {
	ctx := context.Get()
	playerPos := ctx.Data.PlayerUnit.Position

	souls := make([]data.Monster, 0, 10) // Pre-allocate with estimated capacity
	for _, m := range ctx.Data.Monsters.Enemies() {
		// Check if alive first (cheaper check)
		if m.Stats[stat.Life] <= 0 {
			continue
		}
		
		// Use map lookup O(1) instead of loop
		if soulNPCsMap[m.Name] {
			distance := pather.DistanceFromPoint(playerPos, m.Position)
			if distance <= radius {
				souls = append(souls, m)
			}
		}
	}

	return souls
}

// checkForSoulsInRange checks if there are any souls within the specified radius
func checkForSoulsInRange(radius int) bool {
	souls := findSoulsInRange(radius)
	return len(souls) > 0
}

// MonsterFilterExcludingDollsAndSouls returns a filter that excludes dangerous dolls and souls
// Dolls: UndeadStygianDoll, UndeadStygianDoll2, UndeadSoulKiller, UndeadSoulKiller2
// Souls: BlackSoul, BlackSoul2, BurningSoul, BurningSoul2
func MonsterFilterExcludingDollsAndSouls() data.MonsterFilter {
	return func(monsters data.Monsters) []data.Monster {
		baseFilter := data.MonsterAnyFilter()
		enemies := monsters.Enemies(baseFilter)
		filteredMonsters := make([]data.Monster, 0, len(enemies))

		for _, m := range enemies {
			// Use map lookup O(1) instead of loop
			if !dangerousNPCsMap[m.Name] {
				filteredMonsters = append(filteredMonsters, m)
			}
		}

		return filteredMonsters
	}
}

func ClearAreaAroundPosition(pos data.Position, radius int, filters ...data.MonsterFilter) error {
	ctx := context.Get()
	ctx.SetLastAction("ClearAreaAroundPosition")

	// Standard behavior: disable item pickup during the clear sequence
	// The high-priority bot loop will handle item pickup automatically
	ctx.DisableItemPickup()
	defer ctx.EnableItemPickup()

	return ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		return selectNextEnemy(ctx, pos, radius, filters...)
	}, nil)
}

// selectNextEnemy finds the next valid enemy to target
func selectNextEnemy(ctx *context.Status, pos data.Position, radius int, filters ...data.MonsterFilter) (data.UnitID, bool) {
	enemies := ctx.Data.Monsters.Enemies(filters...)
	
	// Filter by radius BEFORE sorting to avoid sorting unnecessary enemies
	type candidateEnemy struct {
		monster   data.Monster
		distance  int
		isPriority bool
	}
	
	candidates := make([]candidateEnemy, 0, len(enemies))
	for _, m := range enemies {
		distanceToTarget := pather.DistanceFromPoint(pos, m.Position)
		if distanceToTarget > radius {
			continue
		}
		
		candidates = append(candidates, candidateEnemy{
			monster:    m,
			distance:    distanceToTarget,
			isPriority: priorityMonstersMap[m.Name],
		})
	}
	
	// Sort only the candidates (much smaller set)
	sort.Slice(candidates, func(i, j int) bool {
		ci := candidates[i]
		cj := candidates[j]
		
		if ci.distance > 2 && cj.distance > 2 {
			if ci.isPriority && !cj.isPriority {
				return true
			} else if !ci.isPriority && cj.isPriority {
				return false
			}
		}
		
		return ci.distance < cj.distance
	})

	// Early exit when finding first valid enemy
	for _, candidate := range candidates {
		m := candidate.monster

		// Special case: Vizier can spawn on weird/off-grid tiles in Chaos Sanctuary.
		isVizier := m.Type == data.MonsterTypeSuperUnique && m.Name == npc.StormCaster

		// Skip monsters that exist in data but are placed on non-walkable tiles (often "underwater/off-grid").
		if !isVizier && !ctx.Data.AreaData.IsWalkable(m.Position) {
			continue
		}

		validEnemy := true
		if !ctx.Data.CanTeleport() {
			// If no path exists, do not target it (prevents chasing "ghost" monsters).
			_, _, pathFound := ctx.PathFinder.GetPath(m.Position)
			if !pathFound {
				validEnemy = false
			}

			// Keep the door check to avoid targeting monsters behind closed doors.
			if hasDoorBetween, _ := ctx.PathFinder.HasDoorBetween(ctx.Data.PlayerUnit.Position, m.Position); hasDoorBetween {
				validEnemy = false
			}
		}

		if validEnemy {
			return m.UnitID, true
		}
	}

	return data.UnitID(0), false
}

func ClearThroughPath(pos data.Position, radius int, filter data.MonsterFilter) error {
	return clearThroughPathInternal(pos, radius, filter, false)
}

// ClearThroughPathIgnoreMonsters clears through a path ignoring monsters in pathfinding.
// Useful for "fight through" scenarios like Cow Level where monster density is very high.
func ClearThroughPathIgnoreMonsters(pos data.Position, radius int, filter data.MonsterFilter) error {
	return clearThroughPathInternal(pos, radius, filter, true)
}

func clearThroughPathInternal(pos data.Position, radius int, filter data.MonsterFilter, ignoreMonsters bool) error {
	ctx := context.Get()

	lastMovement := false
	maxDepth := 10 // Prevent infinite recursion
	depth := 0
	
	for {
		ctx.PauseIfNotPriority()
		
		depth++
		if depth > maxDepth {
			return fmt.Errorf("clearThroughPathInternal: max depth reached, possible infinite recursion")
		}

		ClearAreaAroundPosition(ctx.Data.PlayerUnit.Position, radius, filter)

		if lastMovement {
			return nil
		}

		var path pather.Path
		var found bool
		if ignoreMonsters {
			path, _, found = ctx.PathFinder.GetPathIgnoreMonsters(pos)
		} else {
			path, _, found = ctx.PathFinder.GetPath(pos)
		}
		if !found {
			return fmt.Errorf("path could not be calculated")
		}

		movementDistance := radius
		if radius > len(path) {
			movementDistance = len(path)
		}

		dest := data.Position{
			X: path[movementDistance-1].X + ctx.Data.AreaData.OffsetX,
			Y: path[movementDistance-1].Y + ctx.Data.AreaData.OffsetY,
		}

		// Let's handle the last movement logic to MoveTo function, we will trust the pathfinder because
		// it can finish within a bigger distance than we expect (because blockers), so we will just check how far
		// we should be after the latest movement in a theoretical way
		if len(path)-movementDistance <= step.DistanceToFinishMoving {
			lastMovement = true
		}
		// Increasing DistanceToFinishMoving prevent not being to able to finish movement if our destination is center of a large object like Seal in diablo run.
		// is used only for pathing, attack.go will use default DistanceToFinishMoving
		err := step.MoveTo(dest, step.WithDistanceToFinish(7))
		if err != nil {

			if strings.Contains(err.Error(), "monsters detected in movement path") {
				ctx.Logger.Debug("ClearThroughPath: Movement failed due to monsters, attempting to clear them")
				clearErr := ClearAreaAroundPosition(ctx.Data.PlayerUnit.Position, radius+5, filter)
				if clearErr != nil {
					ctx.Logger.Error(fmt.Sprintf("ClearThroughPath: Failed to clear monsters after movement failure: %v", clearErr))
				} else {
					ctx.Logger.Debug("ClearThroughPath: Successfully cleared monsters, continuing with next iteration")
					continue
				}
			}
			return err
		}
		
		// Reset depth counter after successful movement (complete cycle: clear + move)
		depth = 0
	}
}
