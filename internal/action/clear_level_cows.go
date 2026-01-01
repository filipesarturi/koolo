package action

import (
	"log/slog"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/utils"
)

// Cow-only tuned clear: aggressive movement + less pickup spam + fixed alive filtering (only inside cows).
func ClearCurrentLevelCows(openChests bool, filter data.MonsterFilter) error {
	ctx := context.Get()
	ctx.SetLastAction("ClearCurrentLevelCows")

	const (
		pickupRadius     = 10 // smaller for cows
		pickupEveryRooms = 4  // pick up every N rooms + last room
		moveClearRadius  = 20 // used by ClearThroughPath
	)

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

func clearRoomCows(room data.Room, filter data.MonsterFilter, moveClearRadius int) error {
	ctx := context.Get()
	ctx.SetLastAction("clearRoomCows")

	// Use ignore-monsters pathfinding for Cow Level since we need to "fight through" dense packs
	path, _, found := ctx.PathFinder.GetClosestWalkablePathIgnoreMonsters(room.GetCenter())
	if found {
		to := data.Position{
			X: path.To().X + ctx.Data.AreaOrigin.X,
			Y: path.To().Y + ctx.Data.AreaOrigin.Y,
		}

		// Clear while moving (ignore monsters in pathfinding for cow density)
		if err := ClearThroughPathIgnoreMonsters(to, moveClearRadius, filter); err != nil {
			ctx.Logger.Debug("Cows: failed moving to room center, clearing from current position",
				slog.Any("error", err))
		}
	} else {
		// If we can't path to the room center, just clear monsters around current position
		ctx.Logger.Debug("Cows: no path to room center, clearing from current position")
		ClearAreaAroundPlayer(moveClearRadius, filter)
	}

	pickupOnKill := ctx.CharacterCfg.Character.PickupOnKill

	// Anti-stagnation: track iterations on the same target
	const maxStagnantIterations = 5
	var lastTargetID data.UnitID
	stagnantCount := 0

	// Blacklist for monsters we failed to kill in this room
	skippedMonsters := make(map[data.UnitID]bool)

	for {
		ctx.PauseIfNotPriority()
		ctx.RefreshGameData()

		if err := checkPlayerDeath(ctx); err != nil {
			return err
		}

		monsters := getMonstersInRoomCows(room, filter)
		if len(monsters) == 0 {
			return nil
		}

		// Check if all monsters in the room are blacklisted
		validMonstersExist := false
		for _, m := range monsters {
			if !skippedMonsters[m.UnitID] {
				validMonstersExist = true
				break
			}
		}
		if !validMonstersExist {
			ctx.Logger.Debug("Cows: all monsters in room are blacklisted, moving to next room")
			return nil
		}

		SortEnemiesByPriority(&monsters)

		target := data.Monster{}
		for _, m := range monsters {
			// Skip monsters we already failed to kill
			if skippedMonsters[m.UnitID] {
				continue
			}

			if ctx.Char.ShouldIgnoreMonster(m) {
				skippedMonsters[m.UnitID] = true
				continue
			}

			// Verify path exists to the monster (ignore other monsters in path for cow density)
			_, _, mPathFound := ctx.PathFinder.GetPathIgnoreMonsters(m.Position)
			if !mPathFound && !ctx.Data.CanTeleport() {
				skippedMonsters[m.UnitID] = true
				continue
			}

			if m.IsMonsterRaiser() {
				target = m
				break
			}
			if target.UnitID == 0 {
				target = m
			}
		}

		if target.UnitID == 0 {
			// No valid target found even though monsters exist - they're all filtered out
			ctx.Logger.Debug("Cows: no valid targets in room, moving to next room")
			return nil
		}

		// Anti-stagnation check: if stuck on same target too long, blacklist and continue
		if target.UnitID == lastTargetID {
			stagnantCount++
			if stagnantCount >= maxStagnantIterations {
				ctx.Logger.Debug("Cows: stuck on same target, blacklisting and continuing",
					slog.Any("targetID", target.UnitID),
					slog.Int("iterations", stagnantCount))
				skippedMonsters[target.UnitID] = true
				stagnantCount = 0
				continue
			}
		} else {
			stagnantCount = 0
			lastTargetID = target.UnitID
		}

		// Track target position for pickup after kill
		targetPos := target.Position

		// Store HP before attack to detect if we're dealing damage
		monsterHPBefore := target.Stats[stat.Life]

		ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
			m, ok := d.Monsters.FindByID(target.UnitID)
			if ok && m.Stats[stat.Life] > 0 {
				return target.UnitID, true
			}
			return 0, false
		}, nil)

		// After attack sequence, check if monster is still alive with same HP (unkillable)
		ctx.RefreshGameData()
		if m, stillExists := ctx.Data.Monsters.FindByID(target.UnitID); stillExists && m.Stats[stat.Life] > 0 {
			if m.Stats[stat.Life] >= monsterHPBefore {
				// Monster HP didn't decrease, likely unkillable - blacklist it
				ctx.Logger.Debug("Cows: monster HP unchanged after attack, blacklisting",
					slog.Any("targetID", target.UnitID))
				skippedMonsters[target.UnitID] = true
			}
		}

		// If PickupOnKill is enabled, pickup items after each kill
		// Note: RefreshGameData was already called above, so we use current data
		if pickupOnKill {
			m, stillExists := ctx.Data.Monsters.FindByID(target.UnitID)
			if !stillExists || m.Stats[stat.Life] <= 0 {
				if err := ItemPickup(pickupOnKillRadius); err != nil {
					ctx.Logger.Debug("PickupOnKill (cows): Failed to pickup items",
						slog.String("error", err.Error()),
						slog.Int("x", targetPos.X),
						slog.Int("y", targetPos.Y))
				}
			}
		}
	}
}

// Cow-only "alive AND (in-room OR near)" so you don't target corpses near you.
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
