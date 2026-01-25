package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/action/step"
	botCtx "github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/drop"
	"github.com/hectorgimenez/koolo/internal/event"
	"github.com/hectorgimenez/koolo/internal/health"
	"github.com/hectorgimenez/koolo/internal/run"
	"github.com/hectorgimenez/koolo/internal/utils"

	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"golang.org/x/sync/errgroup"
)

type Bot struct {
	ctx                   *botCtx.Context
	lastActivityTimeMux   sync.Mutex
	lastActivityTime      time.Time
	lastKnownPosition     data.Position
	lastPositionCheckTime time.Time
	MuleManager
}

func (b *Bot) NeedsTPsToContinue() bool {
	return !action.HasTPsAvailable()
}

func NewBot(ctx *botCtx.Context, mm MuleManager) *Bot {
	return &Bot{
		ctx:                   ctx,
		lastActivityTime:      time.Now(),      // Initialize
		lastKnownPosition:     data.Position{}, // Will be updated on first game data refresh
		lastPositionCheckTime: time.Now(),      // Initialize
		MuleManager:           mm,
	}
}

func (b *Bot) updateActivityAndPosition() {
	b.lastActivityTimeMux.Lock()
	defer b.lastActivityTimeMux.Unlock()
	b.lastActivityTime = time.Now()
	// Update lastKnownPosition and lastPositionCheckTime only if current game data is valid
	if b.ctx.Data.PlayerUnit.Position != (data.Position{}) {
		b.lastKnownPosition = b.ctx.Data.PlayerUnit.Position
		b.lastPositionCheckTime = time.Now()
	}
}

// getActivityData returns the activity-related data in a thread-safe manner.
func (b *Bot) getActivityData() (time.Time, data.Position, time.Time) {
	b.lastActivityTimeMux.Lock()
	defer b.lastActivityTimeMux.Unlock()
	return b.lastActivityTime, b.lastKnownPosition, b.lastPositionCheckTime
}

func (b *Bot) Run(ctx context.Context, firstRun bool, runs []run.Run) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, ctx := errgroup.WithContext(ctx)

	gameStartedAt := time.Now()
	b.ctx.SwitchPriority(botCtx.PriorityNormal) // Restore priority to normal, in case it was stopped in previous game
	b.ctx.CurrentGame = botCtx.NewGameHelper()  // Reset current game helper structure

	// Register callback for item check after monster death (allows step package to trigger without circular dependency)
	b.ctx.SetCheckItemsAfterDeathCallback(action.CheckItemsAfterMonsterDeath)

	// Reset Memory buff flag for new game
	action.ResetMemoryBuffFlag(b.ctx.Name)
	// Drop: Initialize Drop manager and start watch context
	if b.ctx.Drop == nil {
		b.ctx.Drop = drop.NewManager(b.ctx.Name, b.ctx.Logger)
	}

	err := b.ctx.GameReader.FetchMapData()
	if err != nil {
		return err
	}

	// Let's make sure we have updated game data also fully loaded before performing anything
	b.ctx.WaitForGameToLoad()

	// Cleanup the current game helper structure
	b.ctx.Cleanup()

	// Switch to legacy mode if configured
	action.SwitchToLegacyMode()
	b.ctx.RefreshGameData()

	b.updateActivityAndPosition() // Initial update for activity and position

	// This routine is in charge of refreshing the game data and handling cancellation, will work in parallel with any other execution
	g.Go(func() error {
		b.ctx.AttachRoutine(botCtx.PriorityBackground)
		ticker := time.NewTicker(100 * time.Millisecond)
		for {
			select {
			case <-ctx.Done():
				cancel()
				b.Stop()
				return nil
			case <-ticker.C:
				if b.ctx.ExecutionPriority == botCtx.PriorityPause {
					continue
				}
				b.ctx.RefreshGameData()
				// Update activity here because the bot is actively refreshing game data.
				b.updateActivityAndPosition()
			}
		}
	})

	// This routine is in charge of handling the health/chicken of the bot, will work in parallel with any other execution
	g.Go(func() error {
		b.ctx.AttachRoutine(botCtx.PriorityBackground)
		ticker := time.NewTicker(100 * time.Millisecond)

		const globalLongTermIdleThreshold = 2 * time.Minute // From move.go example
		const minMovementThreshold = 30                     // From move.go example

		for {
			select {
			case <-ctx.Done():
				b.Stop()
				return nil
			case <-ticker.C:
				if b.ctx.ExecutionPriority == botCtx.PriorityPause {
					continue
				}
				if b.ctx.Drop != nil && (b.ctx.Drop.Pending() != nil || b.ctx.Drop.Active() != nil) {
					// Skip health handling while Drop run takes over (character may be out of game)
					continue
				}

				// Check emergency exit BEFORE normal health handling (faster response)
				if b.ctx.EmergencyExitManager != nil {
					if triggered, exitErr := b.ctx.EmergencyExitManager.CheckEmergencyExit(); triggered {
						b.ctx.Logger.Info("EmergencyExitManager: Emergency exit triggered, stopping bot.", "error", exitErr.Error())
						cancel()
						b.Stop()
						return exitErr
					}
				}

				err = b.ctx.HealthManager.HandleHealthAndMana()
				if err != nil {
					b.ctx.Logger.Info("HealthManager: Detected critical error (chicken/death), stopping bot.", "error", err.Error())
					cancel()
					b.Stop()
					return err
				}

				// Always update activity when HealthManager runs, as it signifies process activity
				b.updateActivityAndPosition()

				// Retrieve current activity data in a thread-safe manner
				_, lastKnownPos, lastPosCheckTime := b.getActivityData()
				currentPosition := b.ctx.Data.PlayerUnit.Position

				// Check for position-based long-term idle
				if currentPosition != (data.Position{}) && lastKnownPos != (data.Position{}) { // Ensure valid positions
					distanceFromLastKnown := utils.CalculateDistance(lastKnownPos, currentPosition)

					if distanceFromLastKnown > float64(minMovementThreshold) {
						// Player has moved significantly, reset position-based idle timer
						b.updateActivityAndPosition() // This will update lastKnownPosition and lastPositionCheckTime
						b.ctx.Logger.Debug(fmt.Sprintf("Bot: Player moved significantly (%.2f units), resetting global idle timer.", distanceFromLastKnown))
					} else if time.Since(lastPosCheckTime) > globalLongTermIdleThreshold {
						// Player hasn't moved much for the long-term threshold, quit the game
						b.ctx.Logger.Error(fmt.Sprintf("Bot: Player has been globally idle (no significant movement) for more than %v, quitting game.", globalLongTermIdleThreshold))
						b.Stop()
						return errors.New("bot globally idle for too long (no movement), quitting game")
					}
				} else {
					// If for some reason positions are invalid, just update activity to prevent immediate idle.
					// This handles initial states or temporary data glitches.
					b.updateActivityAndPosition()
				}

				// Check for max game length (this is a separate check from idle)
				if time.Since(gameStartedAt).Seconds() > float64(b.ctx.CharacterCfg.MaxGameLength) {
					b.ctx.Logger.Info("Max game length reached, try to exit game", slog.Float64("duration", time.Since(gameStartedAt).Seconds()))
					b.Stop() // This will set PriorityStop and detach the context
					return fmt.Errorf(
						"max game length reached, try to exit game: %0.2f",
						time.Since(gameStartedAt).Seconds(),
					)
				}
			}
		}
	})
	// High priority loop, this will interrupt (pause) low priority loop
	g.Go(func() error {
		defer func() {
			cancel()
			b.Stop()
			recover()
		}()

		b.ctx.AttachRoutine(botCtx.PriorityHigh)
		ticker := time.NewTicker(time.Millisecond * 100)
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if b.ctx.ExecutionPriority == botCtx.PriorityPause {
					continue
				}

				if b.ctx.Drop != nil && (b.ctx.Drop.Pending() != nil || b.ctx.Drop.Active() != nil) {
					// Drop is in progress, skip high-priority actions until handled
					continue
				}

				// Update activity for high-priority actions as they indicate bot is processing.
				b.updateActivityAndPosition()

				// Sometimes when we switch areas, monsters are not loaded yet, and we don't properly detect the Merc
				// let's add some small delay (just few ms) when this happens, and recheck the merc status
				if b.ctx.CharacterCfg.BackToTown.MercDied && b.ctx.Data.MercHPPercent() <= 0 && b.ctx.CharacterCfg.Character.UseMerc {
					time.Sleep(200 * time.Millisecond)
				}

				// extra RefreshGameData not needed for Legacygraphics/Portraits since Background loop will automatically refresh after 100ms
				if b.ctx.CharacterCfg.ClassicMode && !b.ctx.Data.LegacyGraphics {
					// Toggle Legacy if enabled
					action.SwitchToLegacyMode()
					time.Sleep(150 * time.Millisecond)
				}
				// Hide merc/other players portraits if enabled
				if b.ctx.CharacterCfg.HidePortraits && b.ctx.Data.OpenMenus.PortraitsShown {
					action.HidePortraits()
					time.Sleep(150 * time.Millisecond)
				}
				// Close chat if somehow was opened (prevention)
				if b.ctx.Data.OpenMenus.ChatOpen {
					b.ctx.HID.PressKey(b.ctx.Data.KeyBindings.Chat.Key1[0])
					time.Sleep(150 * time.Millisecond)
				}
				b.ctx.SwitchPriority(botCtx.PriorityHigh)

				// Area correction (only check if enabled)
				if b.ctx.CurrentGame.AreaCorrection.Enabled {
					if err = action.AreaCorrection(); err != nil {
						b.ctx.Logger.Warn("Area correction failed", "error", err)
					}
				}

				// Continuous item pickup check - always verify for drops, especially in public games
				// where other players may kill monsters before the bot
				if !b.ctx.Data.PlayerUnit.Area.IsTown() {
					// Check if there are items to pickup
					hasItems := action.HasItemsToPickup(30)

					if b.ctx.CurrentGame.PickupItems {
						// If PickupItems is enabled, always try pickup (maintains backward compatibility)
						action.ItemPickup(30)
					} else if hasItems {
						// If PickupItems is disabled but there are items on ground,
						// check if we're in active combat before enabling pickup
						// This allows picking up drops from other players while avoiding
						// unnecessary interruptions during intense combat
						hasEnemiesNearby, _ := action.IsAnyEnemyAroundPlayer(10)

						// Only enable pickup if there are no nearby enemies or if items are high priority
						// (runes, uniques, sets) that should be picked up even during combat
						if !hasEnemiesNearby {
							// No nearby enemies, safe to pick up items dropped by other players
							b.ctx.EnableItemPickup()
							action.ItemPickup(30)
							b.ctx.DisableItemPickup()
						} else {
							// Enemies nearby - check if items are high priority (runes, uniques, sets, charms, flawless amethyst)
							items := action.GetItemsToPickup(30)
							hasHighPriorityItems := false
							for _, itm := range items {
								// High priority: runes, uniques, sets, charms, flawless amethyst
								charmName := string(itm.Name)
								itemName := string(itm.Name)
								if itm.Desc().Type == item.TypeRune ||
									itm.Quality == item.QualityUnique ||
									itm.Quality == item.QualitySet ||
									itm.IsRuneword ||
									charmName == "GrandCharm" || charmName == "grandcharm" ||
									charmName == "SmallCharm" || charmName == "smallcharm" ||
									itemName == "FlawlessAmethyst" || itemName == "flawlessamethyst" {
									hasHighPriorityItems = true
									break
								}
							}

							// Pick up high priority items even during combat
							if hasHighPriorityItems {
								b.ctx.EnableItemPickup()
								action.ItemPickup(30)
								b.ctx.DisableItemPickup()
							}
						}
					}
				}

				// Check for stuck item pickup flag and reset if necessary (20 second timeout)
				if b.ctx.IsPickingItems() {
					if b.ctx.ResetStuckItemPickup(20 * time.Second) {
						b.ctx.Logger.Warn("Recovered from stuck item pickup - flag was reset after timeout")
					}
				}

				// Only buff if not picking items
				if !b.ctx.IsPickingItems() {
					action.BuffIfRequired()
				}

				// Defense check
				if b.ctx.DefenseManager != nil {
					if err := b.ctx.DefenseManager.CheckDefense(); err != nil {
						b.ctx.Logger.Warn("Defense manager error", "error", err)
					}
				}

				lvl, _ := b.ctx.Data.PlayerUnit.FindStat(stat.Level, 0)

				MaxLevel := b.ctx.CharacterCfg.Game.StopLevelingAt

				if lvl.Value >= MaxLevel && MaxLevel > 0 {
					b.ctx.Logger.Info(fmt.Sprintf("Player reached level %d (>= MaxLevelAct1 %d). Triggering supervisor stop via context.", lvl.Value, MaxLevel), "run", "Leveling")
					b.ctx.StopSupervisor()
					return nil // Return nil to gracefully end the current run loop
				}

				isInTown := b.ctx.Data.PlayerUnit.Area.IsTown()

				// Check potions in belt
				_, healingPotionsFoundInBelt := b.ctx.Data.Inventory.Belt.GetFirstPotion(data.HealingPotion)
				_, manaPotionsFoundInBelt := b.ctx.Data.Inventory.Belt.GetFirstPotion(data.ManaPotion)
				_, rejuvPotionsFoundInBelt := b.ctx.Data.Inventory.Belt.GetFirstPotion(data.RejuvenationPotion)

				// Check potions in inventory
				hasHealingPotionsInInventory := b.ctx.Data.HasPotionInInventory(data.HealingPotion)
				hasManaPotionsInInventory := b.ctx.Data.HasPotionInInventory(data.ManaPotion)
				hasRejuvPotionsInInventory := b.ctx.Data.HasPotionInInventory(data.RejuvenationPotion)

				// Check if we actually need each type of potion
				needHealingPotionsRefill := !healingPotionsFoundInBelt && b.ctx.CharacterCfg.Inventory.BeltColumns.Total(data.HealingPotion) > 0
				needManaPotionsRefill := !manaPotionsFoundInBelt && b.ctx.CharacterCfg.Inventory.BeltColumns.Total(data.ManaPotion) > 0
				needRejuvPotionsRefill := !rejuvPotionsFoundInBelt && b.ctx.CharacterCfg.Inventory.BeltColumns.Total(data.RejuvenationPotion) > 0

				// Check TP scrolls in belt if using belt for TP
				needTPScrollsRefill := false
				hasTPScrollsInInventory := false
				if b.ctx.CharacterCfg.Inventory.UseScrollTPInBelt {
					_, tpScrollsFoundInBelt := b.ctx.BeltManager.GetFirstScrollTP()
					needTPScrollsRefill = !tpScrollsFoundInBelt
					// Check if we have TP scrolls in inventory
					for _, itm := range b.ctx.Data.Inventory.ByLocation(item.LocationInventory) {
						if itm.Name == item.ScrollOfTownPortal {
							hasTPScrollsInInventory = true
							break
						}
					}
				}

				// Determine if we should refill for each type based on availability in inventory
				shouldRefillHealingPotions := needHealingPotionsRefill && hasHealingPotionsInInventory
				shouldRefillManaPotions := needManaPotionsRefill && hasManaPotionsInInventory
				shouldRefillRejuvPotions := needRejuvPotionsRefill && hasRejuvPotionsInInventory
				shouldRefillTPScrolls := needTPScrollsRefill && hasTPScrollsInInventory

				// Refill belt if:
				// 1. Each potion type (healing/mana) is either already in belt or needed and available in inventory
				// 2. And at least one potion type actually needs refilling
				// Note: If one type (healing/mana) can be refilled but the other cannot, we skip refill and go to town instead
				// 3. BUT will refill in any case if rejuvenation potions are needed and available in inventory
				// 4. OR if TP scrolls are needed and available in inventory
				shouldRefillBelt := ((shouldRefillHealingPotions || healingPotionsFoundInBelt) &&
					(shouldRefillManaPotions || manaPotionsFoundInBelt) &&
					(needHealingPotionsRefill || needManaPotionsRefill)) || shouldRefillRejuvPotions || shouldRefillTPScrolls

				if shouldRefillBelt && !isInTown {
					action.ManageBelt()
					action.RefillBeltFromInventory()
					b.ctx.RefreshGameData()

					// Recheck potions in belt after refill
					_, healingPotionsFoundInBelt = b.ctx.Data.Inventory.Belt.GetFirstPotion(data.HealingPotion)
					_, manaPotionsFoundInBelt = b.ctx.Data.Inventory.Belt.GetFirstPotion(data.ManaPotion)
					needHealingPotionsRefill = !healingPotionsFoundInBelt && b.ctx.CharacterCfg.Inventory.BeltColumns.Total(data.HealingPotion) > 0
					needManaPotionsRefill = !manaPotionsFoundInBelt && b.ctx.CharacterCfg.Inventory.BeltColumns.Total(data.ManaPotion) > 0
				}

				townChicken := b.ctx.CharacterCfg.Health.TownChickenAt > 0 && b.ctx.Data.PlayerUnit.HPPercent() <= b.ctx.CharacterCfg.Health.TownChickenAt

				// Check if we need to go back to town (level, gold, and TP quantity are met, AND then other conditions)
				if _, found := b.ctx.Data.KeyBindings.KeyBindingForSkill(skill.TomeOfTownPortal); found {

					lvl, _ := b.ctx.Data.PlayerUnit.FindStat(stat.Level, 0)

					if !b.NeedsTPsToContinue() { // Now calls b.NeedsTPsToContinue()
						// The curly brace was misplaced here. It should enclose the entire inner 'if' block.
						// This outer 'if' now acts as the gatekeeper for the inner 'if'.
						if (b.ctx.Data.PlayerUnit.TotalPlayerGold() > 500 && lvl.Value <= 5) ||
							(b.ctx.Data.PlayerUnit.TotalPlayerGold() > 1000 && lvl.Value < 20) ||
							(b.ctx.Data.PlayerUnit.TotalPlayerGold() > 5000 && lvl.Value >= 20) {

							// Check mercenary death condition separately to verify gold availability
							mercDead := b.ctx.CharacterCfg.BackToTown.MercDied &&
								b.ctx.Data.MercHPPercent() <= 0 &&
								b.ctx.CharacterCfg.Character.UseMerc
							shouldGoToTownForMerc := false
							if mercDead {
								// Get Status from context to pass to action functions
								status := botCtx.Get()
								// Only go to town if we have enough gold OR if last attempt didn't fail due to no gold
								hasEnoughGold := action.CanAffordMercRevive(status)
								lastAttemptFailedNoGold := b.ctx.MercReviveFailedNoGold
								shouldGoToTownForMerc = hasEnoughGold && !lastAttemptFailedNoGold
								if !shouldGoToTownForMerc && !lastAttemptFailedNoGold {
									// First time detecting merc death without gold - log and set flag
									availableGold := action.GetAvailableGold(status)
									b.ctx.Logger.Info("Mercenary is dead but insufficient gold to revive, skipping town trip",
										"availableGold", availableGold,
										"requiredGold", 50000)
								}
							}

							if (b.ctx.CharacterCfg.BackToTown.NoHpPotions && needHealingPotionsRefill ||
								b.ctx.CharacterCfg.BackToTown.EquipmentBroken && action.IsEquipmentBroken() ||
								b.ctx.CharacterCfg.BackToTown.NoMpPotions && needManaPotionsRefill ||
								townChicken ||
								shouldGoToTownForMerc ||
								b.ctx.CharacterCfg.BackToTown.InventoryFull && action.IsInventoryFull()) &&
								!b.ctx.Data.PlayerUnit.Area.IsTown() &&
								b.ctx.Data.PlayerUnit.Area != area.UberTristram {

								// Log the exact reason for going back to town
								var reason string
								if b.ctx.CharacterCfg.BackToTown.NoHpPotions && needHealingPotionsRefill {
									reason = "No healing potions found"
								} else if b.ctx.CharacterCfg.BackToTown.EquipmentBroken && action.RepairRequired() {
									reason = "Equipment broken"
								} else if b.ctx.CharacterCfg.BackToTown.NoMpPotions && needManaPotionsRefill {
									reason = "No mana potions found"
								} else if shouldGoToTownForMerc {
									reason = "Mercenary is dead"
								} else if townChicken {
									reason = "Town chicken"
								} else if b.ctx.CharacterCfg.BackToTown.InventoryFull && action.IsInventoryFull() {
									reason = "Inventory full"
								}

								b.ctx.Logger.Info("Going back to town", "reason", reason)

								if err = action.InRunReturnTownRoutine(); err != nil {
									// Only return error if it's a critical health error
									// Non-critical errors (like pathfinding issues) should not stop the game
									if b.isCriticalHealthError(err) {
										b.ctx.Logger.Warn("Failed returning town with critical error. Stopping game.", "error", err)
										return err
									}
									// Non-critical error: log and continue
									b.ctx.Logger.Warn("Failed returning town with non-critical error. Continuing.", "error", err)
								}
							}
						}
					}
				} // This closing brace was misplaced. It should be here, closing the outer 'if'.
				b.ctx.SwitchPriority(botCtx.PriorityNormal)
			}
		}
	})

	// Low priority loop, this will keep executing main run scripts
	g.Go(func() error {
		defer func() {
			cancel()
			b.Stop()
			recover()
		}()

		b.ctx.AttachRoutine(botCtx.PriorityNormal)
		for _, r := range runs {
			select {
			case <-ctx.Done():
				return nil
			default:
				skipTownRoutines := false
				if skipper, ok := r.(run.TownRoutineSkipper); ok && skipper.SkipTownRoutines() {
					skipTownRoutines = true
				}

				event.Send(event.RunStarted(event.Text(b.ctx.Name, fmt.Sprintf("Starting run: %s", r.Name())), r.Name()))

				// Update activity here because a new run sequence is starting.
				b.updateActivityAndPosition()

				if !skipTownRoutines {
					err = action.PreRun(firstRun)
					if err != nil {
						// Only exit game for critical health errors, other errors just skip to next run
						if b.isCriticalHealthError(err) {
							b.ctx.Logger.Warn("PreRun failed with critical health error, exiting game", "error", err.Error(), "run", r.Name())
							return err
						}
						// Non-critical error: log and continue to next run
						b.ctx.Logger.Warn("PreRun failed with non-critical error, skipping run", "error", err.Error(), "run", r.Name())
						event.Send(event.RunFinished(event.Text(b.ctx.Name, fmt.Sprintf("Skipped run: %s (PreRun error)", r.Name())), r.Name(), event.FinishedError))
						continue
					}
					firstRun = false
				}

				// Update activity before the main run logic is executed.
				b.updateActivityAndPosition()
				err = r.Run(nil)

				// Drop: Handle Drop interrupt from step functions
				if errors.Is(err, drop.ErrInterrupt) {
					b.ctx.Logger.Info("Drop request acknowledged, switching to Drop routine")
					step.CleanupForDrop()
					_ = b.ctx.Manager.ExitGame()

					d := run.NewDrop()
					if derr := d.Run(nil); derr != nil {
						b.ctx.Logger.Error("Drop run failed", "error", derr)
					} else {
						b.ctx.Logger.Info("Drop run completed successfully")
					}

					// Note: ResetDropContext() is called in Drop.go's defer
					// to handle both success and failure cases consistently

					return nil
				}

				var runFinishReason event.FinishReason
				if err != nil {
					switch {
					case errors.Is(err, health.ErrChicken):
						runFinishReason = event.FinishedChicken
					case errors.Is(err, health.ErrMercChicken):
						runFinishReason = event.FinishedMercChicken
					case errors.Is(err, health.ErrDied):
						runFinishReason = event.FinishedDied
					case errors.Is(err, health.ErrEmergencyExit):
						runFinishReason = event.FinishedEmergencyExit
					case errors.Is(err, errors.New("player idle for too long, quitting game")): // Match the specific error
						runFinishReason = event.FinishedError
					case errors.Is(err, errors.New("bot globally idle for too long (no movement), quitting game")): // Match the specific error for movement-based idle
						runFinishReason = event.FinishedError
					case errors.Is(err, errors.New("player stuck in an unrecoverable movement loop, quitting")): // Match the specific error for movement-based idle
						runFinishReason = event.FinishedError
					case errors.Is(err, action.ErrFailedToEquip): // This is the new line
						runFinishReason = event.FinishedError
					default:
						runFinishReason = event.FinishedError
					}
				} else {
					runFinishReason = event.FinishedOK
				}

				event.Send(event.RunFinished(event.Text(b.ctx.Name, fmt.Sprintf("Finished run: %s", r.Name())), r.Name(), runFinishReason))

				if err != nil {
					// Only exit game for critical health errors, other errors just skip to next run
					if b.isCriticalHealthError(err) {
						b.ctx.Logger.Warn("Critical health error detected, exiting game", "error", err.Error(), "run", r.Name())
						return err
					}
					// Non-critical error: log and continue to next run
					b.ctx.Logger.Warn("Run failed with non-critical error, continuing to next run", "error", err.Error(), "run", r.Name())
					if !skipTownRoutines {
						// Try to execute PostRun even if run failed, but don't fail if PostRun also errors
						if postRunErr := action.PostRun(r == runs[len(runs)-1]); postRunErr != nil {
							if b.isCriticalHealthError(postRunErr) {
								b.ctx.Logger.Warn("PostRun failed with critical error, exiting game", "error", postRunErr.Error())
								return postRunErr
							}
							b.ctx.Logger.Warn("PostRun failed with non-critical error, continuing", "error", postRunErr.Error())
						}
					}
					continue
				}

				if !skipTownRoutines {
					err = action.PostRun(r == runs[len(runs)-1])
					if err != nil {
						// Check if PostRun error is critical
						if b.isCriticalHealthError(err) {
							return err
						}
						// Non-critical PostRun error: log and continue
						b.ctx.Logger.Warn("PostRun failed with non-critical error, continuing to next run", "error", err.Error())
						continue
					}
				}
			}
		}
		return nil
	})

	return g.Wait()
}

// isCriticalHealthError checks if an error is a critical health error that should exit the game
func (b *Bot) isCriticalHealthError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, health.ErrChicken) ||
		errors.Is(err, health.ErrMercChicken) ||
		errors.Is(err, health.ErrDied) ||
		errors.Is(err, health.ErrEmergencyExit)
}

func (b *Bot) Stop() {
	b.ctx.SwitchPriority(botCtx.PriorityStop)
	b.ctx.Detach()
}

type MuleManager interface {
	ShouldMule(stashFull bool, characterName string) (bool, string)
}

type StatsReporter interface {
	ReportStats()
}
