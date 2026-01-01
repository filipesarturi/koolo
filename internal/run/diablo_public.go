package run

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/difficulty"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/utils"
	"github.com/lxn/win"
)

// Position for opening TP at star (left of star to avoid Diablo spawn area)
var diabloStarTPPosition = data.Position{X: 7760, Y: 5294}

// DiabloPublic is an optimized version of Diablo run for public games.
// It tolerates seals already opened and bosses already killed by other players.
type DiabloPublic struct {
	ctx *context.Status
}

func NewDiabloPublic() *DiabloPublic {
	return &DiabloPublic{
		ctx: context.Get(),
	}
}

func (d *DiabloPublic) Name() string {
	return string(config.DiabloPublicRun)
}

func (d DiabloPublic) CheckConditions(parameters *RunParameters) SequencerResult {
	farmingRun := IsFarmingRun(parameters)
	questCompleted := d.ctx.Data.Quests[quest.Act4TerrorsEnd].Completed()
	if farmingRun && !questCompleted {
		return SequencerSkip
	}

	if !farmingRun && questCompleted {
		if slices.Contains(d.ctx.Data.PlayerUnit.AvailableWaypoints, area.Harrogath) || d.ctx.Data.PlayerUnit.Area.Act() == 5 {
			return SequencerSkip
		}

		if action.HasAnyQuestStartedOrCompleted(quest.Act5SiegeOnHarrogath, quest.Act5EveOfDestruction) {
			return SequencerSkip
		}
	}
	return SequencerOk
}

func (d *DiabloPublic) Run(parameters *RunParameters) error {
	if IsQuestRun(parameters) && d.ctx.Data.Quests[quest.Act4TerrorsEnd].Completed() {
		if err := d.goToAct5(); err != nil {
			return err
		}
		return nil
	}

	defer func() {
		d.ctx.EnableItemPickup()
	}()

	if err := action.WayPoint(area.RiverOfFlame); err != nil {
		return err
	}

	_, isLevelingChar := d.ctx.Char.(context.LevelingCharacter)

	if err := action.MoveToArea(area.ChaosSanctuary); err != nil {
		return err
	}

	if isLevelingChar {
		action.Buff()
	}

	// Open TP at entrance if leader option is enabled
	if d.ctx.CharacterCfg.Companion.Leader {
		action.OpenTPIfLeader()
		action.Buff()
		action.ClearAreaAroundPlayer(30, data.MonsterAnyFilter())
	}

	d.ctx.Logger.Debug(fmt.Sprintf("StartFromStar value: %t", d.ctx.CharacterCfg.Game.Diablo.StartFromStar))
	if d.ctx.CharacterCfg.Game.Diablo.StartFromStar {
		// Go directly to star
		if d.ctx.Data.CanTeleport() {
			if err := action.MoveToCoords(diabloSpawnPosition, step.WithIgnoreMonsters()); err != nil {
				return err
			}
		} else {
			if err := action.MoveToCoords(diabloSpawnPosition, step.WithMonsterFilter(d.getMonsterFilter())); err != nil {
				return err
			}
		}

		// Open TP at star if leader option is enabled
		if d.ctx.CharacterCfg.Companion.Leader {
			// Move to left of star to open TP (avoid Diablo spawn area)
			if d.ctx.Data.CanTeleport() {
				if err := action.MoveToCoords(diabloStarTPPosition, step.WithIgnoreMonsters()); err != nil {
					return err
				}
			} else {
				if err := action.MoveToCoords(diabloStarTPPosition, step.WithMonsterFilter(d.getMonsterFilter())); err != nil {
					return err
				}
			}
			d.openTPIfNoNearbyPortal(diabloStarTPPosition, 40)
			action.ClearAreaAroundPlayer(30, data.MonsterAnyFilter())
		}

		if !d.ctx.Data.CanTeleport() {
			d.ctx.Logger.Debug("Non-teleporting character detected, clearing path to Vizier from star")
			err := action.MoveToCoords(chaosNavToPosition, step.WithClearPathOverride(30), step.WithMonsterFilter(d.getMonsterFilter()))
			if err != nil {
				d.ctx.Logger.Error(fmt.Sprintf("Failed to clear path to Vizier from star: %v", err))
				return err
			}
			d.ctx.Logger.Debug("Successfully cleared path to Vizier from star")
		}
	} else {
		// Go through the path killing monsters, heading towards star
		err := action.MoveToCoords(chaosNavToPosition, step.WithClearPathOverride(30), step.WithMonsterFilter(d.getMonsterFilter()))
		if err != nil {
			return err
		}

		// Now move to star and open TP there if leader
		if d.ctx.CharacterCfg.Companion.Leader {
			d.ctx.Logger.Debug("Leader mode: Moving to star to open TP for manual players")
			// Move to left of star to open TP (avoid Diablo spawn area)
			if d.ctx.Data.CanTeleport() {
				if err := action.MoveToCoords(diabloStarTPPosition, step.WithIgnoreMonsters()); err != nil {
					return err
				}
			} else {
				if err := action.MoveToCoords(diabloStarTPPosition, step.WithClearPathOverride(30), step.WithMonsterFilter(d.getMonsterFilter())); err != nil {
					return err
				}
			}
			d.openTPIfNoNearbyPortal(diabloStarTPPosition, 40)
			action.ClearAreaAroundPlayer(30, data.MonsterAnyFilter())
		}
	}

	d.ctx.RefreshGameData()
	sealGroups := map[string][]object.Name{
		"Vizier":       {object.DiabloSeal4, object.DiabloSeal5},
		"Lord De Seis": {object.DiabloSeal3},
		"Infector":     {object.DiabloSeal1, object.DiabloSeal2},
	}

	for _, bossName := range []string{"Vizier", "Lord De Seis", "Infector"} {
		d.ctx.Logger.Debug(fmt.Sprint("Heading to ", bossName))

		for _, sealID := range sealGroups[bossName] {
			d.ctx.RefreshGameData()
			seal, found := d.ctx.Data.Objects.FindOne(sealID)
			if !found {
				// PUBLIC GAME TOLERANCE: Seal not found, assume already opened by other player
				d.ctx.Logger.Debug(fmt.Sprintf("Seal %d not found, assuming already opened by other player", sealID))
				continue
			}

			err := action.MoveToCoords(seal.Position, step.WithClearPathOverride(20), step.WithMonsterFilter(d.getMonsterFilter()))
			if err != nil {
				return err
			}

			// Handle the special case for DiabloSeal3
			if sealID == object.DiabloSeal3 && seal.Position.X == 7773 && seal.Position.Y == 5155 {
				if err = action.MoveToCoords(data.Position{X: 7768, Y: 5160}, step.WithClearPathOverride(20), step.WithMonsterFilter(d.getMonsterFilter())); err != nil {
					return fmt.Errorf("failed to move to bugged seal position: %w", err)
				}
			}

			// Clear everything around the seal
			action.ClearAreaAroundPlayer(10, d.ctx.Data.MonsterFilterAnyReachable())

			// Buff refresh before Infector
			if object.DiabloSeal1 == sealID || isLevelingChar {
				action.Buff()
			}

			// Refresh seal state before trying to open
			d.ctx.RefreshGameData()
			seal, _ = d.ctx.Data.Objects.FindOne(sealID)

			// PUBLIC GAME TOLERANCE: Check if seal is already open
			if !seal.Selectable {
				d.ctx.Logger.Debug(fmt.Sprintf("Seal %d already opened, skipping interaction", sealID))
			} else {
				// Try to open the seal
				maxAttemptsToOpenSeal := 3
				attempts := 0

				for attempts < maxAttemptsToOpenSeal {
					seal, _ = d.ctx.Data.Objects.FindOne(sealID)

					if !seal.Selectable {
						break
					}

					if err = action.InteractObject(seal, func() bool {
						seal, _ = d.ctx.Data.Objects.FindOne(sealID)
						return !seal.Selectable
					}); err != nil {
						d.ctx.Logger.Error(fmt.Sprintf("Attempt %d to interact with seal %d: %v failed", attempts+1, sealID, err))
						d.ctx.PathFinder.RandomMovement()
						utils.PingSleep(utils.Medium, 200)
					}

					attempts++
				}

				seal, _ = d.ctx.Data.Objects.FindOne(sealID)
				if seal.Selectable {
					// PUBLIC GAME TOLERANCE: Failed to open seal, but continue anyway
					d.ctx.Logger.Warn(fmt.Sprintf("Failed to open seal %d after %d attempts, continuing anyway (public game tolerance)", sealID, maxAttemptsToOpenSeal))
				}
			}

			// Infector spawns when first seal is enabled
			if object.DiabloSeal1 == sealID {
				if err = d.killSealElite(bossName); err != nil {
					// PUBLIC GAME TOLERANCE: Infector might already be dead
					d.ctx.Logger.Warn(fmt.Sprintf("Infector check: %v, continuing (public game tolerance)", err))
				}
			}
		}

		// Skip Infector boss because was already killed
		if bossName != "Infector" {
			if err := d.killSealElite(bossName); err != nil {
				// PUBLIC GAME TOLERANCE: Boss might already be dead or far away
				if bossName == "Lord De Seis" {
					d.ctx.Logger.Debug(fmt.Sprintf("Lord De Seis: %v, continuing", err))
				} else {
					d.ctx.Logger.Warn(fmt.Sprintf("%s: %v, continuing (public game tolerance)", bossName, err))
				}
			}
		}
	}

	if d.ctx.CharacterCfg.Game.Diablo.KillDiablo {
		action.Buff()

		originalClearPathDistCfg := d.ctx.CharacterCfg.Character.ClearPathDist
		d.ctx.CharacterCfg.Character.ClearPathDist = 0

		defer func() {
			d.ctx.CharacterCfg.Character.ClearPathDist = originalClearPathDistCfg
		}()

		if isLevelingChar && d.ctx.CharacterCfg.Game.Difficulty == difficulty.Normal {
			action.MoveToCoords(diabloSpawnPosition)
			action.InRunReturnTownRoutine()
			step.MoveTo(diabloFightPosition, step.WithIgnoreMonsters())
		} else {
			action.MoveToCoords(diabloSpawnPosition)
		}

		if d.ctx.CharacterCfg.Game.Diablo.DisableItemPickupDuringBosses {
			d.ctx.DisableItemPickup()
		}

		if err := d.ctx.Char.KillDiablo(); err != nil {
			return err
		}

		action.ItemPickup(30)

		if IsQuestRun(parameters) {
			if err := d.goToAct5(); err != nil {
				return err
			}
		}
	}

	return nil
}

func (d *DiabloPublic) killSealElite(boss string) error {
	d.ctx.Logger.Debug(fmt.Sprintf("Starting kill sequence for %s", boss))
	startTime := time.Now()

	// PUBLIC GAME OPTIMIZATION: Reduced timeout for faster runs
	// Lord De Seis often spawns far away, so use shorter timeout
	timeout := 5 * time.Second
	if boss == "Lord De Seis" {
		timeout = 3 * time.Second
	}

	_, isLevelingChar := d.ctx.Char.(context.LevelingCharacter)
	sealElite := data.Monster{}
	sealEliteAlreadyDead := false

	var bossNPCID npc.ID
	switch boss {
	case "Vizier":
		bossNPCID = npc.StormCaster
	case "Lord De Seis":
		bossNPCID = npc.OblivionKnight
	case "Infector":
		bossNPCID = npc.VenomLord
	}

	for time.Since(startTime) < timeout {
		d.ctx.PauseIfNotPriority()
		d.ctx.RefreshGameData()

		// Check for living seal elite
		for _, m := range d.ctx.Data.Monsters.Enemies(d.ctx.Data.MonsterFilterAnyReachable()) {
			if action.IsMonsterSealElite(m) && m.Name == bossNPCID {
				sealElite = m
				break
			}
		}

		// If not found alive, check if already dead in corpses
		if sealElite.UnitID == 0 {
			for _, corpse := range d.ctx.Data.Corpses {
				if action.IsMonsterSealElite(corpse) && corpse.Name == bossNPCID {
					sealEliteAlreadyDead = true
					break
				}
			}
		}

		if sealElite.UnitID != 0 || sealEliteAlreadyDead {
			break
		}

		if d.ctx.Data.PlayerUnit.Area.IsTown() {
			startTime = time.Now()
		}

		utils.PingSleep(utils.Light, 250)
	}

	// If seal elite was already dead, no need to kill it
	if sealEliteAlreadyDead {
		d.ctx.Logger.Debug(fmt.Sprintf("%s already dead (found in corpses)", boss))
		return nil
	}

	// If we didn't find the boss at all after timeout
	if sealElite.UnitID == 0 {
		// Try one more time to check corpses after clearing nearby area
		action.ClearAreaAroundPlayer(40, data.MonsterAnyFilter())
		d.ctx.RefreshGameData()

		for _, corpse := range d.ctx.Data.Corpses {
			if action.IsMonsterSealElite(corpse) && corpse.Name == bossNPCID {
				d.ctx.Logger.Debug(fmt.Sprintf("%s found dead after clearing area", boss))
				return nil
			}
		}

		// If it's Lord De Seis, this is acceptable (he spawns far sometimes)
		if boss == "Lord De Seis" {
			d.ctx.Logger.Debug("Lord De Seis not found but this is acceptable, continuing")
			return nil
		}

		// PUBLIC GAME TOLERANCE: Boss not found, assume killed by other player
		d.ctx.Logger.Debug(fmt.Sprintf("%s not found, assuming killed by other player (public game tolerance)", boss))
		return nil
	}

	utils.PingSleep(utils.Medium, 500)

	killSealEliteAttempts := 0
	killStartTime := time.Now()
	killTimeout := 60 * time.Second

	if sealElite.UnitID != 0 {
		for killSealEliteAttempts <= 5 {
			if time.Since(killStartTime) > killTimeout {
				d.ctx.Logger.Warn(fmt.Sprintf("Kill sequence for %s timed out after %v", boss, killTimeout))
				d.ctx.RefreshGameData()
				_, stillExists := d.ctx.Data.Monsters.FindByID(sealElite.UnitID)
				if !stillExists {
					d.ctx.Logger.Debug(fmt.Sprintf("Boss %s was killed before timeout (UnitID no longer exists)", boss))
					return nil
				}
				for _, m := range d.ctx.Data.Monsters {
					if action.IsMonsterSealElite(m) && m.Name == bossNPCID {
						if m.Stats[stat.Life] <= 0 {
							d.ctx.Logger.Debug(fmt.Sprintf("Boss %s was killed before timeout (HP is 0)", boss))
							return nil
						}
						if boss == "Vizier" {
							return fmt.Errorf("failed to kill Vizier within %v - cannot proceed", killTimeout)
						}
						d.ctx.Logger.Warn(fmt.Sprintf("Boss %s still alive after timeout, but continuing (non-critical)", boss))
						return nil
					}
				}
				d.ctx.Logger.Debug(fmt.Sprintf("Boss %s not found after timeout, assuming dead", boss))
				return nil
			}

			d.ctx.PauseIfNotPriority()
			d.ctx.RefreshGameData()
			m, found := d.ctx.Data.Monsters.FindByID(sealElite.UnitID)

			if d.ctx.Data.PlayerUnit.Area.IsTown() {
				utils.PingSleep(utils.Light, 100)
				continue
			}

			if !found {
				for _, monster := range d.ctx.Data.Monsters.Enemies(d.ctx.Data.MonsterFilterAnyReachable()) {
					if action.IsMonsterSealElite(monster) && monster.Name == bossNPCID {
						sealElite = monster
						found = true
						break
					}
				}

				if !found {
					for _, corpse := range d.ctx.Data.Corpses {
						if action.IsMonsterSealElite(corpse) && corpse.Name == bossNPCID {
							d.ctx.Logger.Debug(fmt.Sprintf("Successfully killed seal elite %s (found in corpses)", boss))
							return nil
						}
					}

					if killSealEliteAttempts > 2 {
						// PUBLIC GAME TOLERANCE: Boss not found, assume killed by other player
						d.ctx.Logger.Debug(fmt.Sprintf("%s not found after detection, assuming killed (public game tolerance)", boss))
						return nil
					}
					utils.PingSleep(utils.Light, 250)
					continue
				}
			}

			killSealEliteAttempts++
			sealElite = m

			var clearRadius int
			if d.ctx.Data.CanTeleport() {
				clearRadius = 30
			} else {
				clearRadius = 40
			}

			targetUnitID := sealElite.UnitID
			targetNPCID := bossNPCID

			err := action.ClearAreaAroundPosition(sealElite.Position, clearRadius, func(monsters data.Monsters) (filteredMonsters []data.Monster) {
				if isLevelingChar {
					filteredMonsters = append(filteredMonsters, monsters...)
				} else {
					bossFound := false
					for _, m := range monsters {
						if m.UnitID == targetUnitID || (action.IsMonsterSealElite(m) && m.Name == targetNPCID) {
							if m.Stats[stat.Life] > 0 {
								filteredMonsters = append(filteredMonsters, m)
								bossFound = true
								break
							}
						}
					}
					if !bossFound {
						d.ctx.Logger.Debug(fmt.Sprintf("Boss %s not found in monster list during clear (likely dead)", boss))
					}
				}
				return filteredMonsters
			})

			if err != nil {
				d.ctx.Logger.Error(fmt.Sprintf("Failed to clear area around seal elite %s: %v", boss, err))
				continue
			}

			d.ctx.RefreshGameData()

			_, unitIDExists := d.ctx.Data.Monsters.FindByID(targetUnitID)
			if !unitIDExists {
				d.ctx.Logger.Debug(fmt.Sprintf("Successfully killed seal elite %s - UnitID %d no longer exists", boss, targetUnitID))
				return nil
			}

			for _, corpse := range d.ctx.Data.Corpses {
				if action.IsMonsterSealElite(corpse) && corpse.Name == bossNPCID {
					d.ctx.Logger.Debug(fmt.Sprintf("Successfully killed seal elite %s after %d attempts (found in corpses)", boss, killSealEliteAttempts))
					return nil
				}
			}

			bossStillAlive := false
			var foundBoss data.Monster
			for _, m := range d.ctx.Data.Monsters {
				if action.IsMonsterSealElite(m) && m.Name == bossNPCID {
					if m.Stats[stat.Life] > 0 {
						bossStillAlive = true
						foundBoss = m
						break
					}
				}
			}

			if bossStillAlive {
				d.ctx.Logger.Debug(fmt.Sprintf("Boss %s still alive with %d HP at position (%d,%d), continuing...", boss, foundBoss.Stats[stat.Life], foundBoss.Position.X, foundBoss.Position.Y))
			} else {
				d.ctx.Logger.Debug(fmt.Sprintf("Successfully killed seal elite %s after %d attempts (HP is 0 or not found)", boss, killSealEliteAttempts))
				return nil
			}

			utils.PingSleep(utils.Light, 250)
		}
	}

	// PUBLIC GAME TOLERANCE: If we reach here, assume boss was killed by someone else
	d.ctx.Logger.Debug(fmt.Sprintf("%s kill sequence ended, assuming dead (public game tolerance)", boss))
	return nil
}

func (d *DiabloPublic) getMonsterFilter() data.MonsterFilter {
	return func(monsters data.Monsters) (filteredMonsters []data.Monster) {
		for _, m := range monsters {
			if !d.ctx.Data.AreaData.IsWalkable(m.Position) {
				continue
			}

			if d.ctx.CharacterCfg.Game.Diablo.FocusOnElitePacks {
				if m.IsElite() || action.IsMonsterSealElite(m) {
					filteredMonsters = append(filteredMonsters, m)
				}
			} else {
				filteredMonsters = append(filteredMonsters, m)
			}
		}

		return filteredMonsters
	}
}

func (d *DiabloPublic) goToAct5() error {
	err := action.WayPoint(area.ThePandemoniumFortress)
	if err != nil {
		return err
	}

	err = action.InteractNPC(npc.Tyrael2)
	if err != nil {
		return err
	}

	d.ctx.HID.KeySequence(win.VK_DOWN, win.VK_RETURN)
	utils.Sleep(1000)
	d.ctx.RefreshGameData()
	utils.Sleep(1000)

	d.trySkipCinematic()

	if d.ctx.Data.PlayerUnit.Area.Act() != 5 {
		harrogathPortal, found := d.ctx.Data.Objects.FindOne(object.LastLastPortal)
		if found {
			err = action.InteractObject(harrogathPortal, func() bool {
				utils.Sleep(100)
				ctx := context.Get()
				return !ctx.Manager.InGame() || d.ctx.Data.PlayerUnit.Area.Act() == 5
			})

			if err != nil {
				return err
			}

			d.trySkipCinematic()
		}
		return errors.New("failed to go to act 5")
	}

	return nil
}

func (d DiabloPublic) trySkipCinematic() {
	if !d.ctx.Manager.InGame() {
		utils.Sleep(2000)
		action.HoldKey(win.VK_SPACE, 2000)
		utils.Sleep(2000)
		action.HoldKey(win.VK_SPACE, 2000)
		utils.Sleep(2000)
	}
}

// openTPIfNoNearbyPortal opens a town portal only if there's no existing portal nearby
func (d *DiabloPublic) openTPIfNoNearbyPortal(position data.Position, radius int) {
	d.ctx.RefreshGameData()

	radiusSquared := float64(radius * radius)

	// Check if there's already a town portal nearby
	for _, obj := range d.ctx.Data.Objects {
		if obj.IsPortal() {
			distanceSquared := d.distanceSquared(obj.Position, position)
			if distanceSquared <= radiusSquared {
				d.ctx.Logger.Debug(fmt.Sprintf("Town portal already exists nearby (within radius %d), skipping TP creation", radius))
				return
			}
		}
	}

	d.ctx.Logger.Debug("No nearby town portal found, opening new TP at star")
	action.OpenTPIfLeader()
}

// distanceSquared calculates the squared distance between two positions (avoids sqrt for performance)
func (d *DiabloPublic) distanceSquared(p1, p2 data.Position) float64 {
	dx := float64(p1.X - p2.X)
	dy := float64(p1.Y - p2.Y)
	return dx*dx + dy*dy
}
