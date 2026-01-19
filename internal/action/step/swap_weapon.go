package step

import (
	"errors"
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/utils"
)

var ErrWeaponSwapTimeout = errors.New("weapon swap timeout - failed to swap weapons")

func SwapToMainWeapon() error {
	return swapWeapon(false)
}

func SwapToCTA() error {
	return swapWeapon(true)
}

// SwapToSlot swaps to a specific weapon slot (0 = main, 1 = secondary)
func SwapToSlot(targetSlot int) error {
	ctx := context.Get()
	ctx.SetLastStep(fmt.Sprintf("SwapToSlot_%d", targetSlot))

	// Timeout after 5 seconds to prevent infinite loop
	timeout := time.Now().Add(5 * time.Second)
	maxAttempts := 10
	attempts := 0

	for {
		// Check timeout first
		if time.Now().After(timeout) || attempts >= maxAttempts {
			ctx.Logger.Warn("Weapon swap timeout reached",
				"targetSlot", targetSlot,
				"attempts", attempts,
			)
			return ErrWeaponSwapTimeout
		}

		// Pause the execution if the priority is not the same as the execution priority
		// The timeout check above ensures we don't block indefinitely
		ctx.PauseIfNotPriority()

		// Refresh game data to get current slot
		ctx.RefreshGameData()

		// Check if we already have the desired weapon slot
		if ctx.Data.ActiveWeaponSlot == targetSlot {
			return nil
		}

		// Press swap key
		ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.SwapWeapons)
		attempts++

		// Wait for the swap to take effect
		utils.PingSleep(utils.Light, 300)

		// Refresh data after swap
		ctx.RefreshGameData()

		// Check again after swap
		if ctx.Data.ActiveWeaponSlot == targetSlot {
			return nil
		}

		// Small delay before next attempt
		utils.Sleep(200)
	}
}

func swapWeapon(toCTA bool) error {
	ctx := context.Get()

	// Set appropriate last step for debugging
	if toCTA {
		ctx.SetLastStep("SwapToCTA")
	} else {
		ctx.SetLastStep("SwapToMainWeapon")
	}

	// Timeout after 5 seconds to prevent infinite loop
	timeout := time.Now().Add(5 * time.Second)
	maxAttempts := 10
	attempts := 0

	for {
		// Check timeout first
		if time.Now().After(timeout) || attempts >= maxAttempts {
			ctx.Logger.Warn("Weapon swap timeout reached",
				"toCTA", toCTA,
				"attempts", attempts,
			)
			return ErrWeaponSwapTimeout
		}

		// Pause the execution if the priority is not the same as the execution priority
		// The timeout check above ensures we don't block indefinitely
		ctx.PauseIfNotPriority()

		// Refresh game data to get current skill state
		ctx.RefreshGameData()

		// Check if we already have the desired weapon set
		_, found := ctx.Data.PlayerUnit.Skills[skill.BattleOrders]
		if (toCTA && found) || (!toCTA && !found) {
			return nil
		}

		// Press swap key
		ctx.HID.PressKeyBinding(ctx.Data.KeyBindings.SwapWeapons)
		attempts++

		// Wait for the swap to take effect
		utils.PingSleep(utils.Light, 300)

		// Refresh data after swap
		ctx.RefreshGameData()

		// Check again after swap
		_, found = ctx.Data.PlayerUnit.Skills[skill.BattleOrders]
		if (toCTA && found) || (!toCTA && !found) {
			return nil
		}

		// Small delay before next attempt
		utils.Sleep(200)
	}
}
