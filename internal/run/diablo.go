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

var diabloSpawnPosition = data.Position{X: 7792, Y: 5294}
var diabloFightPosition = data.Position{X: 7788, Y: 5292}
var chaosNavToPosition = data.Position{X: 7732, Y: 5292} //into path towards vizier

type Diablo struct {
	ctx *context.Status
}

func NewDiablo() *Diablo {
	return &Diablo{
		ctx: context.Get(),
	}
}

func (d *Diablo) Name() string {
	return string(config.DiabloRun)
}

func (d Diablo) CheckConditions(parameters *RunParameters) SequencerResult {
	farmingRun := IsFarmingRun(parameters)
	questCompleted := d.ctx.Data.Quests[quest.Act4TerrorsEnd].Completed()
	if farmingRun && !questCompleted {
		return SequencerSkip
	}

	if !farmingRun && questCompleted {
		if slices.Contains(d.ctx.Data.PlayerUnit.AvailableWaypoints, area.Harrogath) || d.ctx.Data.PlayerUnit.Area.Act() == 5 {
			return SequencerSkip
		}

		//Workaround AvailableWaypoints only filled when wp menu has been opened on act page
		//Check if any act 5 quest is started or completed
		if action.HasAnyQuestStartedOrCompleted(quest.Act5SiegeOnHarrogath, quest.Act5EveOfDestruction) {
			return SequencerSkip
		}
	}
	return SequencerOk
}

func (d *Diablo) Run(parameters *RunParameters) error {
	if IsQuestRun(parameters) && d.ctx.Data.Quests[quest.Act4TerrorsEnd].Completed() {
		if err := d.goToAct5(); err != nil {
			return err
		}
		return nil
	}

	// Just to be sure we always re-enable item pickup after the run
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

	// We move directly to Diablo spawn position if StartFromStar is enabled, not clearing the path
	d.ctx.Logger.Debug(fmt.Sprintf("StartFromStar value: %t", d.ctx.CharacterCfg.Game.Diablo.StartFromStar))
	if d.ctx.CharacterCfg.Game.Diablo.StartFromStar {
		if d.ctx.Data.CanTeleport() {
			if err := action.MoveToCoords(diabloSpawnPosition, step.WithIgnoreMonsters()); err != nil {
				return err
			}
		} else {
			//move to star
			if err := action.MoveToCoords(diabloSpawnPosition, step.WithMonsterFilter(d.getMonsterFilter())); err != nil {
				return err
			}
		}
		//open portal if leader
		if d.ctx.CharacterCfg.Companion.Leader {
			action.OpenTPIfLeader()
			action.Buff()
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
		//open portal in entrance
		if d.ctx.CharacterCfg.Companion.Leader {
			action.OpenTPIfLeader()
			action.Buff()
			action.ClearAreaAroundPlayer(30, data.MonsterAnyFilter())
		}
		//path through towards vizier
		err := action.MoveToCoords(chaosNavToPosition, step.WithClearPathOverride(30), step.WithMonsterFilter(d.getMonsterFilter()))
		if err != nil {
			return err
		}
	}

	d.ctx.RefreshGameData()
	sealGroups := map[string][]object.Name{
		"Vizier":       {object.DiabloSeal4, object.DiabloSeal5}, // Vizier
		"Lord De Seis": {object.DiabloSeal3},                     // Lord De Seis
		"Infector":     {object.DiabloSeal1, object.DiabloSeal2}, // Infector
	}

	// Thanks Go for the lack of ordered maps
	for _, bossName := range []string{"Vizier", "Lord De Seis", "Infector"} {
		d.ctx.Logger.Debug(fmt.Sprint("Heading to ", bossName))

		for _, sealID := range sealGroups[bossName] {
			seal, found := d.ctx.Data.Objects.FindOne(sealID)
			if !found {
				return fmt.Errorf("seal not found: %d", sealID)
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

			//Buff refresh before Infector
			if object.DiabloSeal1 == sealID || isLevelingChar {
				action.Buff()
			}

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
				d.ctx.Logger.Error(fmt.Sprintf("Failed to open seal %d after %d attempts", sealID, maxAttemptsToOpenSeal))
				return fmt.Errorf("failed to open seal %d after %d attempts", sealID, maxAttemptsToOpenSeal)
			}

			// Infector spawns when first seal is enabled
			if object.DiabloSeal1 == sealID {
				if err = d.killSealElite(bossName); err != nil {
					return err
				}
			}
		}

		// Skip Infector boss because was already killed
		if bossName != "Infector" {
			// Wait for the boss to spawn and kill it.
			// Lord De Seis sometimes it's far, and we can not detect him, but we will kill him anyway heading to the next seal
			if err := d.killSealElite(bossName); err != nil && bossName != "Lord De Seis" {
				return err
			}
		}

	}

	if d.ctx.CharacterCfg.Game.Diablo.KillDiablo {

		// Buff BEFORE setting ClearPathDist to 0, so bot can defend itself during buff
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

		// Check if we should disable item pickup for Diablo
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

func (d *Diablo) killSealElite(boss string) error {
	d.ctx.Logger.Debug(fmt.Sprintf("Starting kill sequence for %s", boss))
	startTime := time.Now()

	timeout := 20 * time.Second

	_, isLevelingChar := d.ctx.Char.(context.LevelingCharacter)
	sealElite := data.Monster{}
	sealEliteAlreadyDead := false

	// Map boss name to NPC ID for corpse checking
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
			//Seal elite found (alive or dead), stop detection loop
			break
		}

		//Reset time
		if d.ctx.Data.PlayerUnit.Area.IsTown() {
			startTime = time.Now()
		}

		utils.PingSleep(utils.Light, 250)
	}

	// If seal elite was already dead, no need to kill it
	if sealEliteAlreadyDead {
		return nil
	}

	// If we didn't find the boss at all after timeout, it might have spawned far away or died before we could detect it
	// For Lord De Seis this is acceptable (he can be far), but for others it's suspicious
	if sealElite.UnitID == 0 {
		// Try one more time to check corpses after clearing nearby area
		action.ClearAreaAroundPlayer(40, data.MonsterAnyFilter())
		d.ctx.RefreshGameData()

		for _, corpse := range d.ctx.Data.Corpses {
			if action.IsMonsterSealElite(corpse) && corpse.Name == bossNPCID {
				return nil
			}
		}

		// If it's Lord De Seis, this is acceptable (he spawns far sometimes)
		if boss == "Lord De Seis" {
			d.ctx.Logger.Debug("Lord De Seis not found but this is acceptable, continuing")
			return nil
		}

		return fmt.Errorf("no seal elite found for %s within %v seconds", boss, timeout)
	}

	utils.PingSleep(utils.Medium, 500)

	killSealEliteAttempts := 0
	killStartTime := time.Now()
	killTimeout := 60 * time.Second // Maximum time to kill the boss
	
	if sealElite.UnitID != 0 {
		for killSealEliteAttempts <= 5 {
			// Check timeout
			if time.Since(killStartTime) > killTimeout {
				d.ctx.Logger.Warn(fmt.Sprintf("Kill sequence for %s timed out after %v", boss, killTimeout))
				// Final check if boss is dead
				d.ctx.RefreshGameData()
				_, stillExists := d.ctx.Data.Monsters.FindByID(sealElite.UnitID)
				if !stillExists {
					d.ctx.Logger.Debug(fmt.Sprintf("Boss %s was killed before timeout (UnitID no longer exists)", boss))
					return nil
				}
				// Check if boss HP is 0
				for _, m := range d.ctx.Data.Monsters {
					if action.IsMonsterSealElite(m) && m.Name == bossNPCID {
						if m.Stats[stat.Life] <= 0 {
							d.ctx.Logger.Debug(fmt.Sprintf("Boss %s was killed before timeout (HP is 0)", boss))
							return nil
						}
						// Boss still alive - this is a problem for Vizier
						if boss == "Vizier" {
							return fmt.Errorf("failed to kill Vizier within %v - cannot proceed", killTimeout)
						}
						d.ctx.Logger.Warn(fmt.Sprintf("Boss %s still alive after timeout, but continuing (non-critical)", boss))
						return nil
					}
				}
				// Boss not found - assume dead
				d.ctx.Logger.Debug(fmt.Sprintf("Boss %s not found after timeout, assuming dead", boss))
				return nil
			}
			
			d.ctx.PauseIfNotPriority()
			d.ctx.RefreshGameData()
			m, found := d.ctx.Data.Monsters.FindByID(sealElite.UnitID)

			//If in town, wait until back to battlefield
			if d.ctx.Data.PlayerUnit.Area.IsTown() {
				utils.PingSleep(utils.Light, 100)
				continue
			}

			if !found {
				// Boss UnitID lost, try to re-detect by checking all seal elites
				for _, monster := range d.ctx.Data.Monsters.Enemies(d.ctx.Data.MonsterFilterAnyReachable()) {
					if action.IsMonsterSealElite(monster) && monster.Name == bossNPCID {
						sealElite = monster
						found = true
						break
					}
				}

				if !found {
					// Check corpses - look for the specific boss by name
					for _, corpse := range d.ctx.Data.Corpses {
						if action.IsMonsterSealElite(corpse) && corpse.Name == bossNPCID {
							d.ctx.Logger.Debug(fmt.Sprintf("Successfully killed seal elite %s (found in corpses)", boss))
							return nil
						}
					}

					// Still not found - only fail after multiple attempts (not first iteration)
					if killSealEliteAttempts > 2 {
						return fmt.Errorf("seal elite %s not found after first detection", boss)
					}
					// Continue loop to retry
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

			//d.ctx.Logger.Debug(fmt.Sprintf("Clearing area around seal elite with radius %d", clearRadius))

			// Capture UnitID before clearing
			targetUnitID := sealElite.UnitID
			targetNPCID := bossNPCID

			err := action.ClearAreaAroundPosition(sealElite.Position, clearRadius, func(monsters data.Monsters) (filteredMonsters []data.Monster) {
				if isLevelingChar {
					filteredMonsters = append(filteredMonsters, monsters...)
				} else {
					// Try to find the boss in current monster list, or use the captured sealElite
					bossFound := false
					for _, m := range monsters {
						if m.UnitID == targetUnitID || (action.IsMonsterSealElite(m) && m.Name == targetNPCID) {
							// Only add if alive
							if m.Stats[stat.Life] > 0 {
								filteredMonsters = append(filteredMonsters, m)
								bossFound = true
								break
							}
						}
					}
					// If boss not found or dead, return empty list (ClearAreaAroundPosition will finish)
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

			// After clearing, check if boss was killed
			d.ctx.RefreshGameData()

			// First check if UnitID still exists
			_, unitIDExists := d.ctx.Data.Monsters.FindByID(targetUnitID)
			if !unitIDExists {
				// UnitID no longer exists - boss is dead
				d.ctx.Logger.Debug(fmt.Sprintf("Successfully killed seal elite %s - UnitID %d no longer exists", boss, targetUnitID))
				return nil
			}

			// Check corpses (if not shattered)
			for _, corpse := range d.ctx.Data.Corpses {
				if action.IsMonsterSealElite(corpse) && corpse.Name == bossNPCID {
					d.ctx.Logger.Debug(fmt.Sprintf("Successfully killed seal elite %s after %d attempts (found in corpses)", boss, killSealEliteAttempts))
					return nil
				}
			}

			// Check ALL monsters (not just reachable), since boss may be in weird position
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
				// Boss was detected earlier but now it's gone (not alive, not in corpses, UnitID exists but HP is 0)
				// This means the boss is dead but UnitID still exists (dying animation)
				d.ctx.Logger.Debug(fmt.Sprintf("Successfully killed seal elite %s after %d attempts (HP is 0 or not found)", boss, killSealEliteAttempts))
				return nil
			}

			utils.PingSleep(utils.Light, 250)
		}
	} else {
		return fmt.Errorf("no seal elite found for %s within %v seconds", boss, timeout.Seconds())
	}

	return fmt.Errorf("failed to kill seal elite %s after %d attempts", boss, killSealEliteAttempts)
}

func (d *Diablo) getMonsterFilter() data.MonsterFilter {
	return func(monsters data.Monsters) (filteredMonsters []data.Monster) {
		for _, m := range monsters {
			if !d.ctx.Data.AreaData.IsWalkable(m.Position) {
				continue
			}

			// If FocusOnElitePacks is enabled, only return elite monsters and seal bosses
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

func (d *Diablo) goToAct5() error {
	err := action.WayPoint(area.ThePandemoniumFortress)
	if err != nil {
		return err
	}

	err = action.InteractNPC(npc.Tyrael2)
	if err != nil {
		return err
	}

	//Choose travel to harrogath option
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

func (d Diablo) trySkipCinematic() {
	if !d.ctx.Manager.InGame() {
		// Skip Cinematics
		utils.Sleep(2000)
		action.HoldKey(win.VK_SPACE, 2000)
		utils.Sleep(2000)
		action.HoldKey(win.VK_SPACE, 2000)
		utils.Sleep(2000)
	}
}
