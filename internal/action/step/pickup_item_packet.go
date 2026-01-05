package step

import (
	"fmt"
	"log/slog"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/utils"
)

func PickupItemPacket(it data.Item, itemPickupAttempt int) error {
	ctx := context.Get()

	// Wait for the character to finish casting or moving before proceeding
	if err := waitForCharacterReady(waitForCharacterTimeout); err != nil {
		return err
	}

	// Validate preconditions (monsters, LOS, distance)
	if err := validatePickupPreconditions(it, 7, true); err != nil {
		return err
	}

	distance := ctx.PathFinder.DistanceFromMe(it.Position)
	ctx.Logger.Debug("Picking up item via packet",
		slog.String("itemName", string(it.Desc().Name)),
		slog.String("itemQuality", it.Quality.ToString()),
		slog.Int("unitID", int(it.UnitID)),
		slog.Int("distance", distance),
		slog.Int("attempt", itemPickupAttempt),
	)

	targetItem := it

	ctx.PauseIfNotPriority()
	ctx.RefreshGameData()

	if hasHostileMonstersNearby(it.Position) {
		ctx.Logger.Debug("Monsters detected around item, aborting packet pickup",
			slog.String("itemName", string(it.Desc().Name)),
			slog.Int("unitID", int(it.UnitID)),
		)
		return ErrMonsterAroundItem
	}

	// Check if item still exists
	_, exists := findItemOnGround(targetItem.UnitID)
	if !exists {
		ctx.Logger.Info("Item already picked up (packet method)",
			slog.String("itemName", string(targetItem.Desc().Name)),
			slog.String("itemQuality", targetItem.Quality.ToString()),
			slog.Int("unitID", int(targetItem.UnitID)),
			slog.Int("attempt", itemPickupAttempt),
		)
		ctx.CurrentGame.PickedUpItems[int(targetItem.UnitID)] = int(ctx.Data.PlayerUnit.Area.Area().ID)
		return nil
	}

	// Send packet to pick up item
	err := ctx.PacketSender.PickUpItem(targetItem)
	if err != nil {
		ctx.Logger.Error("Packet pickup failed",
			slog.String("itemName", string(targetItem.Desc().Name)),
			slog.Int("unitID", int(targetItem.UnitID)),
			slog.String("error", err.Error()),
		)
		return fmt.Errorf("packet pickup failed: %w", err)
	}

	for i := 0; i < 5; i++ {
		utils.PingSleep(utils.Light, 150)
		ctx.RefreshInventory()

		// Verify pickup
		_, stillExists := findItemOnGround(targetItem.UnitID)
		if !stillExists {
			ctx.Logger.Info("Picked up item via packet",
				slog.String("itemName", string(targetItem.Desc().Name)),
				slog.String("itemQuality", targetItem.Quality.ToString()),
				slog.Int("unitID", int(targetItem.UnitID)),
				slog.Int("attempt", itemPickupAttempt),
				slog.Int("verificationAttempt", i+1),
			)
			ctx.CurrentGame.PickedUpItems[int(targetItem.UnitID)] = int(ctx.Data.PlayerUnit.Area.Area().ID)
			return nil
		}
	}

	ctx.Logger.Warn("Packet sent but item still on ground",
		slog.String("itemName", string(targetItem.Desc().Name)),
		slog.Int("unitID", int(targetItem.UnitID)),
		slog.Int("verificationAttempts", 5),
	)
	return fmt.Errorf("packet pickup failed - item still on ground")
}
