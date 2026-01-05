package run

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/utils"
	"github.com/lxn/win"
)

type Ancients struct {
	ctx *context.Status
}

func NewAncients() *Ancients {
	return &Ancients{
		ctx: context.Get(),
	}
}

func (a Ancients) Name() string {
	return string(config.AncientsRun)
}

func (a Ancients) CheckConditions(parameters *RunParameters) SequencerResult {
	if IsFarmingRun(parameters) {
		return SequencerError
	}
	if !a.ctx.Data.Quests[quest.Act4TerrorsEnd].Completed() {
		return SequencerStop
	}
	if a.ctx.Data.Quests[quest.Act5RiteOfPassage].Completed() {
		return SequencerSkip
	}
	return SequencerOk
}

func (a Ancients) Run(parameters *RunParameters) error {
	err := action.WayPoint(area.Harrogath)
	if err != nil {
		return err
	}
	action.Buff()

	err = action.WayPoint(area.TheAncientsWay)
	if err != nil {
		return err
	}
	action.Buff()

	err = action.MoveToArea(area.ArreatSummit)
	if err != nil {
		return err
	}
	action.Buff()

	action.InRunReturnTownRoutine()
	action.Buff()

	a.killAncients()

	action.InRunReturnTownRoutine()

	err = action.MoveToArea(area.TheWorldStoneKeepLevel1)
	if err != nil {
		return err
	}

	err = action.MoveToArea(area.TheWorldStoneKeepLevel2)
	if err != nil {
		return err
	}

	err = action.DiscoverWaypoint()
	if err != nil {
		return err
	}

	// The defer statement above will handle the restoration
	// a.ctx.CharacterCfg.BackToTown = originalBackToTownCfg // This line is now removed
	// a.ctx.Logger.Info("Restored original back-to-town checks after Ancients fight.") // This line is now part of the defer
	return nil
}

func (a Ancients) killAncients() error {
	var ancientsAltar = data.Position{
		X: 10049,
		Y: 12623,
	}

	// Store the original configuration
	originalBackToTownCfg := a.ctx.CharacterCfg.BackToTown
	originalTownChicken := a.ctx.CharacterCfg.Health.TownChickenAt

	// Defer the restoration of the configuration.
	// This will run when the function exits, regardless of how.
	defer func() {
		a.ctx.CharacterCfg.BackToTown = originalBackToTownCfg
		a.ctx.CharacterCfg.Health.TownChickenAt = originalTownChicken
		a.ctx.Logger.Info("Restored original back-to-town checks after Ancients fight.")
	}()

	action.MoveToCoords(ancientsAltar)

	utils.Sleep(1000)
	a.ctx.HID.Click(game.LeftButton, 720, 260)
	utils.Sleep(1000)
	a.ctx.HID.PressKey(win.VK_RETURN)
	utils.Sleep(2000)

	// Modify the configuration for the Ancients fight
	a.ctx.CharacterCfg.BackToTown.NoHpPotions = false
	a.ctx.CharacterCfg.BackToTown.NoMpPotions = false
	a.ctx.CharacterCfg.BackToTown.EquipmentBroken = false
	a.ctx.CharacterCfg.BackToTown.MercDied = false
	a.ctx.CharacterCfg.Health.TownChickenAt = 0

	for {
		ancients := a.ctx.Data.Monsters.Enemies(data.MonsterEliteFilter())
		// Filter out pets, mercenaries, and friendly NPCs
		filteredAncients := make([]data.Monster, 0, len(ancients))
		for _, m := range ancients {
			if !m.IsPet() && !m.IsMerc() && !m.IsGoodNPC() && !m.IsSkip() {
				filteredAncients = append(filteredAncients, m)
			}
		}
		if len(filteredAncients) == 0 {
			break
		}

		a.ctx.Char.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
			for _, m := range d.Monsters.Enemies(data.MonsterEliteFilter()) {
				// Skip pets, mercenaries, and friendly NPCs (allies' summons)
				if m.IsPet() || m.IsMerc() || m.IsGoodNPC() || m.IsSkip() {
					continue
				}
				return m.UnitID, true
			}
			return 0, false
		}, nil)
	}

	return nil
}
