package action

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
)

// CountessQuestChest is the chest that only opens during the Forgotten Tower quest
const CountessQuestChest = object.Name(371)

var interactableShrines = []object.ShrineType{
	object.ExperienceShrine,
	object.StaminaShrine,
	object.ManaRegenShrine,
	object.SkillShrine,
	object.RefillShrine,
	object.HealthShrine,
	object.ManaShrine,
}

func ClearCurrentLevel(openChests bool, filter data.MonsterFilter) error {
	return ClearCurrentLevelEx(openChests, filter, nil)
}

func ClearCurrentLevelEx(openChests bool, filter data.MonsterFilter, shouldInterrupt func() bool) error {
	ctx := context.Get()
	ctx.SetLastAction("ClearCurrentLevel")

	// We can make this configurable later, but 20 is a good starting radius.
	const pickupRadius = 20
	rooms := ctx.PathFinder.OptimizeRoomsTraverseOrder()
	for _, r := range rooms {
		if errDeath := checkPlayerDeath(ctx); errDeath != nil {
			return errDeath
		}

		if shouldInterrupt != nil && shouldInterrupt() {
			return nil
		}

		// First, clear the room of monsters
		err := clearRoom(r, filter)
		if err != nil {
			ctx.Logger.Warn("Failed to clear room", slog.Any("error", err))
		}

		//ctx.Logger.Debug(fmt.Sprintf("Clearing room complete, attempting to pickup items in a radius of %d", pickupRadius))
		err = ItemPickup(pickupRadius)
		if err != nil {
			ctx.Logger.Warn("Failed to pickup items", slog.Any("error", err))
		}

		// Collect all chests in the room
		var chestsInRoom []data.Object
		if openChests {
			for _, o := range ctx.Data.Objects {
				if r.IsInside(o.Position) && o.IsChest() && o.Selectable {
					// Skip Countess quest chest if quest is already completed (it only opens during the quest)
					if o.Name == CountessQuestChest && ctx.Data.PlayerUnit.Area == area.TowerCellarLevel5 {
						if ctx.Data.Quests[quest.Act1TheForgottenTower].Completed() {
							ctx.Logger.Debug("Skipping Countess quest chest - quest already completed")
							continue
						}
					}
					// Check if we have keys before attempting to open locked chests
					if !hasKeysInInventory() {
						ctx.Logger.Debug("Skipping chest - no keys in inventory", slog.Any("chest_id", o.ID))
						continue
					}
					chestsInRoom = append(chestsInRoom, o)
				}
			}
		}

		// Open chests in batch (works with or without Telekinesis)
		if len(chestsInRoom) > 0 {
			// Use batch opening for multiple chests, individual for single chest
			if len(chestsInRoom) > 1 {
				ctx.Logger.Debug("Opening multiple chests in batch",
					"chestsCount", len(chestsInRoom),
				)
				_ = OpenContainersInBatch(chestsInRoom)
			} else {
				// Single chest - use individual method
				o := chestsInRoom[0]
				ctx.Logger.Debug(fmt.Sprintf("Found chest. attempting to interact. Name=%s. ID=%v UnitID=%v Pos=%v,%v Area='%s' InteractType=%v", o.Desc().Name, o.Name, o.ID, o.Position.X, o.Position.Y, ctx.Data.PlayerUnit.Area.Area().Name, o.InteractType))

				chestDistance := ctx.PathFinder.DistanceFromMe(o.Position)
				canUseTK := canUseTelekinesisForObject(o)
				telekinesisRange := getTelekinesisRange()

				// Only move if not within Telekinesis range (or TK not available)
				if !canUseTK || chestDistance > telekinesisRange {
					err = MoveToCoords(o.Position)
					if err != nil {
						ctx.Logger.Warn("Failed moving to chest", slog.Any("error", err))
					} else {
						err = InteractObject(o, func() bool {
							chest, _ := ctx.Data.Objects.FindByID(o.ID)
							return !chest.Selectable
						})
						if err != nil {
							ctx.Logger.Warn("Failed interacting with chest", slog.Any("error", err))
						} else {
							// Wait for items to drop from the opened chest
							WaitForItemsAfterContainerOpen(o.Position, o)
						}
					}
				} else {
					err = InteractObject(o, func() bool {
						chest, _ := ctx.Data.Objects.FindByID(o.ID)
						return !chest.Selectable
					})
					if err != nil {
						ctx.Logger.Warn("Failed interacting with chest", slog.Any("error", err))
					} else {
						// Wait for items to drop from the opened chest
						WaitForItemsAfterContainerOpen(o.Position, o)
					}
				}
			}
		}
	}

	return nil
}

func clearRoom(room data.Room, filter data.MonsterFilter) error {
	ctx := context.Get()
	ctx.SetLastAction("clearRoom")
	roomStartTime := time.Now()
	monstersKilled := 0

	roomCenter := room.GetCenter()
	path, _, found := ctx.PathFinder.GetClosestWalkablePath(roomCenter)

	// If center is not reachable, try alternative positions around the room
	if !found {
		// Try corners and edges of the room as fallback positions (more positions, larger radius)
		alternativePositions := []data.Position{
			// Close positions (radius 3-5)
			{X: roomCenter.X + 5, Y: roomCenter.Y},
			{X: roomCenter.X - 5, Y: roomCenter.Y},
			{X: roomCenter.X, Y: roomCenter.Y + 5},
			{X: roomCenter.X, Y: roomCenter.Y - 5},
			{X: roomCenter.X + 3, Y: roomCenter.Y + 3},
			{X: roomCenter.X - 3, Y: roomCenter.Y - 3},
			{X: roomCenter.X + 3, Y: roomCenter.Y - 3},
			{X: roomCenter.X - 3, Y: roomCenter.Y + 3},
			// Medium positions (radius 7-10)
			{X: roomCenter.X + 8, Y: roomCenter.Y},
			{X: roomCenter.X - 8, Y: roomCenter.Y},
			{X: roomCenter.X, Y: roomCenter.Y + 8},
			{X: roomCenter.X, Y: roomCenter.Y - 8},
			{X: roomCenter.X + 6, Y: roomCenter.Y + 6},
			{X: roomCenter.X - 6, Y: roomCenter.Y - 6},
			// Far positions (radius 10-12)
			{X: roomCenter.X + 10, Y: roomCenter.Y + 5},
			{X: roomCenter.X - 10, Y: roomCenter.Y - 5},
		}

		for _, altPos := range alternativePositions {
			path, _, found = ctx.PathFinder.GetClosestWalkablePath(altPos)
			if found {
				ctx.Logger.Debug("Using alternative position for room clearing",
					slog.Int("originalX", roomCenter.X),
					slog.Int("originalY", roomCenter.Y),
					slog.Int("altX", altPos.X),
					slog.Int("altY", altPos.Y),
				)
				break
			}
		}

		// Last resort: try to find path from current player position towards room center
		if !found {
			playerPos := ctx.Data.PlayerUnit.Position
			// Try positions between player and room center
			dx := (roomCenter.X - playerPos.X) / 3
			dy := (roomCenter.Y - playerPos.Y) / 3
			midPositions := []data.Position{
				{X: playerPos.X + dx, Y: playerPos.Y + dy},
				{X: playerPos.X + dx*2, Y: playerPos.Y + dy*2},
			}
			for _, midPos := range midPositions {
				path, _, found = ctx.PathFinder.GetClosestWalkablePath(midPos)
				if found {
					ctx.Logger.Debug("Using midpoint position for room clearing",
						slog.Int("playerX", playerPos.X),
						slog.Int("playerY", playerPos.Y),
						slog.Int("midX", midPos.X),
						slog.Int("midY", midPos.Y),
					)
					break
				}
			}
		}

		if !found {
			// Final fallback: skip this room and continue (don't block entire clear)
			ctx.Logger.Warn("Skipping room - no path found to any position",
				slog.Int("roomCenterX", roomCenter.X),
				slog.Int("roomCenterY", roomCenter.Y),
				slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
			)
			return nil // Return nil to continue with other rooms instead of blocking
		}
	}

	to := data.Position{
		X: path.To().X + ctx.Data.AreaOrigin.X,
		Y: path.To().Y + ctx.Data.AreaOrigin.Y,
	}
	err := MoveToCoords(to, step.WithMonsterFilter(filter))
	if err != nil {
		return fmt.Errorf("failed moving to room center: %w", err)
	}

	for {
		ctx.PauseIfNotPriority()

		if err := checkPlayerDeath(ctx); err != nil {
			return err
		}

		monsters := getMonstersInRoom(room, filter)
		if len(monsters) == 0 {
			// Log slow room clears for performance analysis
			roomDuration := time.Since(roomStartTime)
			if roomDuration > 15*time.Second && monstersKilled > 0 {
				ctx.Logger.Info("Slow room clear",
					slog.Duration("duration", roomDuration),
					slog.Int("monstersKilled", monstersKilled),
					slog.String("area", ctx.Data.PlayerUnit.Area.Area().Name),
				)
			}
			return nil
		}

		SortEnemiesByPriority(&monsters)

		// Check if there are monsters that can summon new monsters, and kill them first
		targetMonster := data.Monster{}
		for _, m := range monsters {
			if !ctx.Char.ShouldIgnoreMonster(m) {
				if m.IsMonsterRaiser() {
					targetMonster = m
					break
				} else if targetMonster.UnitID == 0 {
					targetMonster = m
				}
			}
		}

		if targetMonster.UnitID == 0 {
			//No valid targets, done
			return nil
		}

		_, _, mPathFound := ctx.PathFinder.GetPath(targetMonster.Position)
		if mPathFound {
			if !ctx.Data.CanTeleport() {
				hasDoorBetween, door := ctx.PathFinder.HasDoorBetween(ctx.Data.PlayerUnit.Position, targetMonster.Position)

				if hasDoorBetween && door.Selectable {
					ctx.Logger.Debug("Door is blocking the path to the monster, moving closer")
					MoveTo(func() (data.Position, bool) {
						return door.Position, true
					})
				}
			}

			ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
				m, found := d.Monsters.FindByID(targetMonster.UnitID)
				if found && m.Stats[stat.Life] > 0 {
					return targetMonster.UnitID, true
				}
				return 0, false
			}, nil)
			monstersKilled++
		}
	}
}

func getMonstersInRoom(room data.Room, filter data.MonsterFilter) []data.Monster {
	ctx := context.Get()
	ctx.SetLastAction("getMonstersInRoom")

	monstersInRoom := make([]data.Monster, 0)
	for _, m := range ctx.Data.Monsters.Enemies(filter) {
		// Fix operator precedence: alive AND (in room OR close to player).
		if m.Stats[stat.Life] <= 0 {
			continue
		}

		// Skip pets, mercenaries, and friendly NPCs (allies' summons)
		if m.IsPet() || m.IsMerc() || m.IsGoodNPC() || m.IsSkip() {
			continue
		}

		if !(room.IsInside(m.Position) || ctx.PathFinder.DistanceFromMe(m.Position) < 30) {
			continue
		}

		// Skip monsters that exist in data but are placed on non-walkable tiles (often "underwater/off-grid").
		// Keep Vizier exception (Chaos Sanctuary).
		isVizier := m.Type == data.MonsterTypeSuperUnique && m.Name == npc.StormCaster
		if !isVizier && !ctx.Data.AreaData.IsWalkable(m.Position) {
			continue
		}

		monstersInRoom = append(monstersInRoom, m)
	}

	return monstersInRoom
}
