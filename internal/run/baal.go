package run

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/d2go/pkg/data/quest"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/utils"
)

var baalThronePosition = data.Position{
	X: 15095,
	Y: 5042,
}

type Baal struct {
	ctx                *context.Status
	clearMonsterFilter data.MonsterFilter // Used to clear area (basically TZ)
	preAtkLast         time.Time
	decoyLast          time.Time
}

func NewBaal(clearMonsterFilter data.MonsterFilter) *Baal {
	return &Baal{
		ctx:                context.Get(),
		clearMonsterFilter: clearMonsterFilter,
	}
}

func (s Baal) Name() string {
	return string(config.BaalRun)
}

func (a Baal) CheckConditions(parameters *RunParameters) SequencerResult {
	farmingRun := IsFarmingRun(parameters)
	if !a.ctx.Data.Quests[quest.Act5RiteOfPassage].Completed() {
		if farmingRun {
			return SequencerSkip
		}
		return SequencerStop
	}
	questCompleted := a.ctx.Data.Quests[quest.Act5EveOfDestruction].Completed()
	if (farmingRun && !questCompleted) || (!farmingRun && questCompleted) {
		return SequencerSkip
	}
	return SequencerOk
}
func (s *Baal) Run(parameters *RunParameters) error {
	// Set filter
	filter := data.MonsterAnyFilter()
	if s.ctx.CharacterCfg.Game.Baal.OnlyElites {
		filter = data.MonsterEliteFilter()
	}
	if s.clearMonsterFilter != nil {
		filter = s.clearMonsterFilter
	}

	err := action.WayPoint(area.TheWorldStoneKeepLevel2)
	if err != nil {
		return err
	}

	if s.ctx.CharacterCfg.Game.Baal.ClearFloors || s.clearMonsterFilter != nil {
		action.ClearCurrentLevel(false, filter)
	}

	err = action.MoveToArea(area.TheWorldStoneKeepLevel3)
	if err != nil {
		return err
	}

	if s.ctx.CharacterCfg.Game.Baal.ClearFloors || s.clearMonsterFilter != nil {
		action.ClearCurrentLevel(false, filter)
	}

	err = action.MoveToArea(area.ThroneOfDestruction)
	if err != nil {
		return err
	}
	err = action.MoveToCoords(baalThronePosition)
	if err != nil {
		return err
	}
	if s.checkForSoulsOrDolls() {
		return errors.New("souls or dolls detected, skipping")
	}

	// Let's move to a safe area and open the portal in companion mode
	if s.ctx.CharacterCfg.Companion.Leader {
		action.MoveToCoords(data.Position{
			X: 15116,
			Y: 5071,
		})
		action.OpenTPIfLeader()
	}

	err = action.ClearAreaAroundPlayer(50, data.MonsterAnyFilter())
	if err != nil {
		return err
	}

	// Force rebuff before waves
	action.Buff()

	// Come back to previous position
	err = action.MoveToCoords(baalThronePosition)
	if err != nil {
		return err
	}

	// Process waves until Baal leaves throne
	s.ctx.Logger.Info("Starting Baal waves...")
	waveTimeout := time.Now().Add(7 * time.Minute)

	lastWaveDetected := false
	isWaitingForPortal := false
	_, isLevelingChar := s.ctx.Char.(context.LevelingCharacter)

	for !s.hasBaalLeftThrone() && time.Now().Before(waveTimeout) {
		s.ctx.PauseIfNotPriority()
		s.ctx.RefreshGameData()

		// Check for souls during waves - CRITICAL: souls attack with lightning that kills quickly
		if s.ctx.CharacterCfg.Game.Baal.SoulQuit {
			if s.checkForSoulsOrDolls(50) {
				s.ctx.Logger.Warn("Souls detected during waves, retreating...")
				return errors.New("souls detected during waves, skipping")
			}
		}

		// Handle souls immediately if detected (even if SoulQuit is off, still prioritize them)
		souls := action.FindSoulsInRange(50)
		if len(souls) > 0 {
			if err := s.handleSoulsImmediately(souls); err != nil {
				s.ctx.Logger.Warn("Error handling souls", "error", err)
				// Continue anyway, but log the error
			}
		}

		// Detect last wave for logging
		if _, found := s.ctx.Data.Monsters.FindOne(npc.BaalsMinion, data.MonsterTypeMinion); found {
			if !lastWaveDetected {
				s.ctx.Logger.Info("Last wave (Baal's Minion) detected")
				lastWaveDetected = true
			}
		} else if lastWaveDetected {

			if !s.ctx.CharacterCfg.Game.Baal.KillBaal && !isLevelingChar {
				s.ctx.Logger.Info("Waves cleared, skipping Baal kill (Fast Exit).")
				return nil
			}

			if !isWaitingForPortal {
				s.ctx.Logger.Info("Waves cleared, moving to portal position to wait...")
				action.MoveToCoords(data.Position{X: 15090, Y: 5008})
				isWaitingForPortal = true
			}

			utils.Sleep(500)
			continue
		}

		if !isWaitingForPortal {
			action.ClearAreaAroundPosition(baalThronePosition, 50, data.MonsterAnyFilter())
			action.MoveToCoords(baalThronePosition)
			s.preAttackBaalWaves()
		}

		utils.Sleep(500) // Prevent excessive checking
	}

	if !s.hasBaalLeftThrone() {
		return errors.New("baal waves timeout - portal never appeared")
	}

	// Baal has entered the chamber
	s.ctx.Logger.Info("Baal has entered the Worldstone Chamber")

	// Kill Baal Logic
	if s.ctx.CharacterCfg.Game.Baal.KillBaal || isLevelingChar {
		action.Buff()

		s.ctx.Logger.Info("Waiting for Baal portal...")
		var baalPortal data.Object
		found := false

		for i := 0; i < 15; i++ {
			baalPortal, found = s.ctx.Data.Objects.FindOne(object.BaalsPortal)
			if found {
				break
			}
			utils.Sleep(300)
		}

		if !found {
			return errors.New("baal portal not found after waves completed")
		}

		// Check for souls before entering portal
		if s.ctx.CharacterCfg.Game.Baal.SoulQuit {
			if s.checkForSoulsOrDolls(50) {
				s.ctx.Logger.Warn("Souls detected before entering Baal portal, retreating...")
				return errors.New("souls detected before entering Baal portal, skipping")
			}
		}

		s.ctx.Logger.Info("Entering Baal portal...")

		// Enter portal
		err = action.InteractObject(baalPortal, func() bool {
			return s.ctx.Data.PlayerUnit.Area == area.TheWorldstoneChamber
		})

		// Verify entry
		if s.ctx.Data.PlayerUnit.Area == area.TheWorldstoneChamber {
			s.ctx.Logger.Info("Successfully entered Worldstone Chamber")
		} else if err != nil {
			return fmt.Errorf("failed to enter baal portal: %w", err)
		}

		// Move to Baal (may fail due to tentacles)
		s.ctx.Logger.Info("Moving to Baal...")
		moveErr := action.MoveToCoords(data.Position{X: 15136, Y: 5943})
		if moveErr != nil {
			if strings.Contains(moveErr.Error(), "path could not be calculated") {
				s.ctx.Logger.Info("Path blocked by tentacles, attacking from current position")
			} else {
				s.ctx.Logger.Warn("Failed to move to Baal", "error", moveErr)
			}
		}

		return s.ctx.Char.KillBaal()
	}

	return nil
}

// hasBaalLeftThrone checks if Baal has left the throne and entered the Worldstone Chamber
func (s *Baal) hasBaalLeftThrone() bool {
	_, found := s.ctx.Data.Monsters.FindOne(npc.BaalThrone, data.MonsterTypeNone)
	return !found
}

// checkForSoulsOrDolls checks for dangerous souls and dolls
// If radius is > 0, only checks within that radius from player
func (s Baal) checkForSoulsOrDolls(radius ...int) bool {
	var npcIds []npc.ID

	if s.ctx.CharacterCfg.Game.Baal.DollQuit {
		npcIds = append(npcIds, npc.UndeadStygianDoll, npc.UndeadStygianDoll2, npc.UndeadSoulKiller, npc.UndeadSoulKiller2)
	}
	if s.ctx.CharacterCfg.Game.Baal.SoulQuit {
		// Include all variants of souls, not just version 2
		npcIds = append(npcIds, npc.BlackSoul, npc.BlackSoul2, npc.BurningSoul, npc.BurningSoul2)
	}

	if len(npcIds) == 0 {
		return false
	}

	// If radius is specified, check within that radius
	if len(radius) > 0 && radius[0] > 0 {
		for _, m := range s.ctx.Data.Monsters.Enemies() {
			for _, id := range npcIds {
				if m.Name == id && m.Stats[stat.Life] > 0 {
					distance := s.ctx.PathFinder.DistanceFromMe(m.Position)
					if distance <= radius[0] {
						return true
					}
				}
			}
		}
		return false
	}

	// Default behavior: check anywhere
	for _, id := range npcIds {
		if _, found := s.ctx.Data.Monsters.FindOne(id, data.MonsterTypeNone); found {
			return true
		}
	}

	return false
}

func (s *Baal) preAttackBaalWaves() {
	// Positions adapted from kolbot baal.js preattack
	blizzPos := data.Position{X: 15094, Y: 5027}
	hammerPos := data.Position{X: 15094, Y: 5029}
	throneCenter := data.Position{X: 15093, Y: 5029}
	forwardPos := data.Position{X: 15116, Y: 5026}

	// Simple global cooldown between preattacks to avoid spam
	const preAtkCooldown = 1500 * time.Millisecond
	if !s.preAtkLast.IsZero() && time.Since(s.preAtkLast) < preAtkCooldown {
		return
	}

	if s.ctx.Data.PlayerUnit.Skills[skill.Blizzard].Level > 0 {
		step.CastAtPosition(skill.Blizzard, true, blizzPos)
		s.preAtkLast = time.Now()
		return
	}

	if s.ctx.Data.PlayerUnit.Skills[skill.Meteor].Level > 0 {
		step.CastAtPosition(skill.Meteor, true, blizzPos)
		s.preAtkLast = time.Now()
		return
	}
	if s.ctx.Data.PlayerUnit.Skills[skill.FrozenOrb].Level > 0 {
		step.CastAtPosition(skill.FrozenOrb, true, blizzPos)
		s.preAtkLast = time.Now()
		return
	}

	if s.ctx.Data.PlayerUnit.Skills[skill.BlessedHammer].Level > 0 {
		if kb, found := s.ctx.Data.KeyBindings.KeyBindingForSkill(skill.Concentration); found {
			s.ctx.HID.PressKeyBinding(kb)
		}
		step.CastAtPosition(skill.BlessedHammer, true, hammerPos)
		s.preAtkLast = time.Now()
		return
	}

	if s.ctx.Data.PlayerUnit.Skills[skill.Decoy].Level > 0 {
		const decoyCooldown = 10 * time.Second
		if s.decoyLast.IsZero() || time.Since(s.decoyLast) > decoyCooldown {
			decoyPos := data.Position{X: 15092, Y: 5028}
			step.CastAtPosition(skill.Decoy, false, decoyPos)
			s.decoyLast = time.Now()
			s.preAtkLast = time.Now()
			return
		}
	}

	if s.ctx.Data.PlayerUnit.Skills[skill.PoisonNova].Level > 0 {
		step.CastAtPosition(skill.PoisonNova, true, s.ctx.Data.PlayerUnit.Position)
		s.preAtkLast = time.Now()
		return
	}
	if s.ctx.Data.PlayerUnit.Skills[skill.DimVision].Level > 0 {
		step.CastAtPosition(skill.DimVision, true, blizzPos)
		s.preAtkLast = time.Now()
		return
	}

	// Druid:
	if s.ctx.Data.PlayerUnit.Skills[skill.Tornado].Level > 0 {
		step.CastAtPosition(skill.Tornado, true, throneCenter)
		s.preAtkLast = time.Now()
		return
	}
	if s.ctx.Data.PlayerUnit.Skills[skill.Fissure].Level > 0 {
		step.CastAtPosition(skill.Fissure, true, forwardPos)
		s.preAtkLast = time.Now()
		return
	}
	if s.ctx.Data.PlayerUnit.Skills[skill.Volcano].Level > 0 {
		step.CastAtPosition(skill.Volcano, true, forwardPos)
		s.preAtkLast = time.Now()
		return
	}

	// Assassin:
	if s.ctx.Data.PlayerUnit.Skills[skill.LightningSentry].Level > 0 {
		for i := 0; i < 3; i++ {
			step.CastAtPosition(skill.LightningSentry, true, throneCenter)
			utils.Sleep(80)
		}
		s.preAtkLast = time.Now()
		return
	}
	if s.ctx.Data.PlayerUnit.Skills[skill.DeathSentry].Level > 0 {
		for i := 0; i < 2; i++ {
			step.CastAtPosition(skill.DeathSentry, true, throneCenter)
			utils.Sleep(80)
		}
		s.preAtkLast = time.Now()
		return
	}
	if s.ctx.Data.PlayerUnit.Skills[skill.ShockWeb].Level > 0 {
		step.CastAtPosition(skill.ShockWeb, true, throneCenter)
		s.preAtkLast = time.Now()
		return
	}
}

// handleSoulsImmediately handles souls with strategic teleport and Nova if available
// This function prioritizes speed - souls attack with lightning that kills quickly
func (s *Baal) handleSoulsImmediately(souls []data.Monster) error {
	if len(souls) == 0 {
		return nil
	}

	// Check if character has Nova and Teleport
	hasNova := s.ctx.Data.PlayerUnit.Skills[skill.Nova].Level > 0
	hasTeleport := s.ctx.Data.CanTeleport() && s.ctx.Data.PlayerUnit.Skills[skill.Teleport].Level > 0

	// If we have Nova and Teleport, use strategic positioning
	if hasNova && hasTeleport {
		// Calculate best position quickly (with timeout)
		bestPos, hits, found := s.findBestNovaPositionForSouls(souls)
		if found && hits >= 2 {
			// Teleport to best position
			if err := action.MoveToCoords(bestPos); err != nil {
				s.ctx.Logger.Debug("Failed to teleport to best Nova position for souls", "error", err)
				// Fallback: teleport to centroid
				centroid := s.calculateCentroid(souls)
				if err := action.MoveToCoords(centroid); err != nil {
					return err
				}
			}
			// Cast Nova immediately after teleport (no delay)
			// Pre-select Nova skill to minimize time
			if kb, found := s.ctx.Data.KeyBindings.KeyBindingForSkill(skill.Nova); found {
				s.ctx.HID.PressKeyBinding(kb)
			}
			// Cast Nova at current position (Nova is area effect)
			step.SecondaryAttack(skill.Nova, souls[0].UnitID, 1, step.Distance(0, 8))
			return nil
		} else if len(souls) > 0 {
			// Fallback: teleport to first soul or centroid
			centroid := s.calculateCentroid(souls)
			if err := action.MoveToCoords(centroid); err != nil {
				return err
			}
			// Cast Nova immediately
			if kb, found := s.ctx.Data.KeyBindings.KeyBindingForSkill(skill.Nova); found {
				s.ctx.HID.PressKeyBinding(kb)
			}
			step.SecondaryAttack(skill.Nova, souls[0].UnitID, 1, step.Distance(0, 8))
			return nil
		}
	}

	// If no Nova/Teleport, souls will be handled by priority system
	return nil
}

// findBestNovaPositionForSouls finds the best position to teleport to maximize Nova hits on souls
// This is a FAST version - limited search to maintain speed (critical for survival)
func (s *Baal) findBestNovaPositionForSouls(souls []data.Monster) (data.Position, int, bool) {
	if len(souls) == 0 {
		return data.Position{}, 0, false
	}

	startTime := time.Now()
	const maxSearchTime = 50 * time.Millisecond
	const novaRadius = 8 // Nova spell radius in tiles
	const maxCandidates = 30

	playerPos := s.ctx.Data.PlayerUnit.Position
	centroid := s.calculateCentroid(souls)

	// Check current position first
	currentHits := s.countSoulsInNovaRange(playerPos, souls, novaRadius)
	if currentHits >= 2 {
		return playerPos, currentHits, true
	}

	// Quick search: check positions around centroid
	searchRadius := 6
	if len(souls) >= 5 {
		searchRadius = 5 // Smaller radius for larger groups
	}

	candidates := make([]data.Position, 0, maxCandidates)
	isWalkable := s.ctx.Data.AreaData.IsWalkable

	// Generate candidate positions around centroid
	for x := centroid.X - searchRadius; x <= centroid.X+searchRadius && len(candidates) < maxCandidates; x++ {
		for y := centroid.Y - searchRadius; y <= centroid.Y+searchRadius && len(candidates) < maxCandidates; y++ {
			if time.Since(startTime) > maxSearchTime {
				// Timeout - return best found so far or centroid
				if len(candidates) > 0 {
					bestPos := candidates[0]
					bestHits := s.countSoulsInNovaRange(bestPos, souls, novaRadius)
					return bestPos, bestHits, true
				}
				return centroid, s.countSoulsInNovaRange(centroid, souls, novaRadius), true
			}

			pos := data.Position{X: x, Y: y}
			if !isWalkable(pos) {
				continue
			}

			// Only consider positions reasonably close to player (avoid long teleports)
			distToPlayer := s.ctx.PathFinder.DistanceFromMe(pos)
			if distToPlayer > 20 {
				continue
			}

			candidates = append(candidates, pos)
		}
	}

	// Find best candidate
	bestPos := centroid
	bestHits := s.countSoulsInNovaRange(centroid, souls, novaRadius)

	for _, candidate := range candidates {
		hits := s.countSoulsInNovaRange(candidate, souls, novaRadius)
		if hits > bestHits {
			bestHits = hits
			bestPos = candidate
		}
		// If we found a position that hits 2+ souls, that's good enough (speed over perfection)
		if bestHits >= 2 {
			break
		}
	}

	return bestPos, bestHits, bestHits >= 2
}

// calculateCentroid calculates the centroid position of a group of souls
func (s *Baal) calculateCentroid(souls []data.Monster) data.Position {
	if len(souls) == 0 {
		return s.ctx.Data.PlayerUnit.Position
	}

	var sumX, sumY int
	for _, soul := range souls {
		sumX += soul.Position.X
		sumY += soul.Position.Y
	}

	return data.Position{
		X: sumX / len(souls),
		Y: sumY / len(souls),
	}
}

// countSoulsInNovaRange counts how many souls are within Nova radius from a position
func (s *Baal) countSoulsInNovaRange(pos data.Position, souls []data.Monster, radius int) int {
	r2 := radius * radius
	hits := 0

	for _, soul := range souls {
		if soul.Stats[stat.Life] <= 0 {
			continue
		}

		dx := pos.X - soul.Position.X
		dy := pos.Y - soul.Position.Y
		dist2 := dx*dx + dy*dy

		if dist2 <= r2 {
			hits++
		}
	}

	return hits
}
