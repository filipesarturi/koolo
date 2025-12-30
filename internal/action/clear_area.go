package action

import (
	"fmt"
	"log/slog"
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

const pickupOnKillRadius = 15 // Pickup radius when PickupOnKill is enabled

func ClearAreaAroundPlayer(radius int, filter data.MonsterFilter) error {
	return ClearAreaAroundPosition(context.Get().Data.PlayerUnit.Position, radius, filter)
}

func IsPriorityMonster(m data.Monster) bool {
	priorityMonsters := []npc.ID{
		npc.FallenShaman,
		npc.CarverShaman,
		npc.DevilkinShaman,
		npc.DarkShaman,
		npc.WarpedShaman,
		npc.MummyGenerator,
		npc.BaalSubjectMummy,
		npc.FetishShaman,
		// Souls are dangerous and should be prioritized
		npc.BlackSoul,
		npc.BlackSoul2,
		npc.BurningSoul,
		npc.BurningSoul2,
	}

	for _, priorityMonster := range priorityMonsters {
		if m.Name == priorityMonster {
			return true
		}
	}
	return false
}

func SortEnemiesByPriority(enemies *[]data.Monster) {
	ctx := context.Get()
	sort.Slice(*enemies, func(i, j int) bool {
		monsterI := (*enemies)[i]
		monsterJ := (*enemies)[j]

		isPriorityI := IsPriorityMonster(monsterI)
		isPriorityJ := IsPriorityMonster(monsterJ)

		distanceI := ctx.PathFinder.DistanceFromMe(monsterI.Position)
		distanceJ := ctx.PathFinder.DistanceFromMe(monsterJ.Position)

		if distanceI > 2 && distanceJ > 2 {
			if isPriorityI && !isPriorityJ {
				return true
			} else if !isPriorityI && isPriorityJ {
				return false
			}
		}

		return distanceI < distanceJ
	})
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
	soulNPCs := []npc.ID{
		npc.BlackSoul,
		npc.BlackSoul2,
		npc.BurningSoul,
		npc.BurningSoul2,
	}

	var souls []data.Monster
	for _, m := range ctx.Data.Monsters.Enemies() {
		for _, soulNPC := range soulNPCs {
			if m.Name == soulNPC && m.Stats[stat.Life] > 0 {
				distance := pather.DistanceFromPoint(playerPos, m.Position)
				if distance <= radius {
					souls = append(souls, m)
					break
				}
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
	dangerousNPCs := []npc.ID{
		npc.UndeadStygianDoll,
		npc.UndeadStygianDoll2,
		npc.UndeadSoulKiller,
		npc.UndeadSoulKiller2,
		npc.BlackSoul,
		npc.BlackSoul2,
		npc.BurningSoul,
		npc.BurningSoul2,
	}

	return func(monsters data.Monsters) []data.Monster {
		var filteredMonsters []data.Monster
		baseFilter := data.MonsterAnyFilter()

		for _, m := range monsters.Enemies(baseFilter) {
			isDangerous := false
			for _, dangerousNPC := range dangerousNPCs {
				if m.Name == dangerousNPC {
					isDangerous = true
					break
				}
			}

			if !isDangerous {
				filteredMonsters = append(filteredMonsters, m)
			}
		}

		return filteredMonsters
	}
}

func ClearAreaAroundPosition(pos data.Position, radius int, filters ...data.MonsterFilter) error {
	ctx := context.Get()
	ctx.SetLastAction("ClearAreaAroundPosition")

	// Check if PickupOnKill is enabled - if so, use the pickup-between-kills approach
	if ctx.CharacterCfg.Character.PickupOnKill {
		return clearAreaWithPickupOnKill(pos, radius, filters...)
	}

	// Standard behavior: disable item pickup during the clear sequence
	ctx.DisableItemPickup()
	defer ctx.EnableItemPickup()

	return ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		return selectNextEnemy(ctx, pos, radius, filters...)
	}, nil)
}

// clearAreaWithPickupOnKill clears the area while picking up items after each kill
func clearAreaWithPickupOnKill(pos data.Position, radius int, filters ...data.MonsterFilter) error {
	ctx := context.Get()

	for {
		ctx.PauseIfNotPriority()
		ctx.RefreshGameData()

		// Check for enemies in range
		targetID, found := selectNextEnemy(ctx, pos, radius, filters...)
		if !found {
			// No more enemies, do a final pickup sweep and exit
			return nil
		}

		// Track if the monster was killed (we'll check after the attack sequence)
		monsterBefore, monsterFound := ctx.Data.Monsters.FindByID(targetID)
		if !monsterFound {
			continue
		}

		// Disable item pickup during combat
		ctx.DisableItemPickup()

		// Kill just this one monster using a single-target selector
		_ = ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
			// Check if original target is still alive
			monster, stillExists := d.Monsters.FindByID(targetID)
			if !stillExists || monster.Stats[stat.Life] <= 0 {
				return data.UnitID(0), false // Monster dead, exit sequence
			}
			return targetID, true
		}, nil)

		// Re-enable item pickup
		ctx.EnableItemPickup()

		// Check if monster was killed (no longer exists or has 0 HP)
		ctx.RefreshGameData()
		monsterAfter, stillExists := ctx.Data.Monsters.FindByID(targetID)
		monsterKilled := !stillExists || monsterAfter.Stats[stat.Life] <= 0

		// If monster was killed, do a quick pickup of nearby items
		if monsterKilled {
			pickupPos := monsterBefore.Position
			err := ItemPickup(pickupOnKillRadius)
			if err != nil {
				ctx.Logger.Debug("PickupOnKill: Failed to pickup items after kill",
					slog.String("error", err.Error()),
					slog.Int("x", pickupPos.X),
					slog.Int("y", pickupPos.Y))
			}
		}
	}
}

// selectNextEnemy finds the next valid enemy to target
func selectNextEnemy(ctx *context.Status, pos data.Position, radius int, filters ...data.MonsterFilter) (data.UnitID, bool) {
	enemies := ctx.Data.Monsters.Enemies(filters...)
	SortEnemiesByPriority(&enemies)

	for _, m := range enemies {
		distanceToTarget := pather.DistanceFromPoint(pos, m.Position)
		if distanceToTarget > radius {
			continue
		}

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
	ctx := context.Get()

	lastMovement := false
	for {
		ctx.PauseIfNotPriority()

		ClearAreaAroundPosition(ctx.Data.PlayerUnit.Position, radius, filter)

		if lastMovement {
			return nil
		}

		path, _, found := ctx.PathFinder.GetPath(pos)
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
	}
}
