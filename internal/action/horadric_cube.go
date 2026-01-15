package action

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/object"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/ui"
	"github.com/lxn/win"
)

func CubeAddItems(items ...data.Item) error {
	ctx := context.Get()
	ctx.SetLastAction("CubeAddItems")

	// Ensure stash is open
	if !ctx.Data.OpenMenus.Stash {
		bank, _ := ctx.Data.Objects.FindOne(object.Bank)
		err := InteractObject(bank, func() bool {
			return ctx.Data.OpenMenus.Stash
		})
		if err != nil {
			return err
		}
	}
	// Clear messages like TZ change or public game spam.  Prevent bot from clicking on messages
	ClearMessages()
	ctx.Logger.Info("Adding items to the Horadric Cube", slog.Any("items", items))

	// If items are on the Stash, pickup them to the inventory
	for _, itm := range items {
		nwIt := itm
		if nwIt.Location.LocationType != item.LocationStash && nwIt.Location.LocationType != item.LocationSharedStash {
			continue
		}

		// Check in which tab the item is and switch to it
		switch nwIt.Location.LocationType {
		case item.LocationStash:
			SwitchStashTab(1)
		case item.LocationSharedStash:
			SwitchStashTab(nwIt.Location.Page + 1)
		}

		ctx.Logger.Debug("Item found on the stash, picking it up", slog.String("Item", string(nwIt.Name)))
		screenPos := ui.GetScreenCoordsForItem(nwIt)
		originalLocation := nwIt.Location.LocationType

		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
		WaitForItemNotInLocation(nwIt.UnitID, originalLocation, 1500)
	}

	err := ensureCubeIsOpen()
	if err != nil {
		return err
	}

	err = ensureCubeIsEmpty()
	if err != nil {
		return err
	}

	for _, itm := range items {
		for _, updatedItem := range ctx.Data.Inventory.AllItems {
			if itm.UnitID == updatedItem.UnitID {
				ctx.Logger.Debug("Moving Item to the Horadric Cube", slog.String("Item", string(itm.Name)))

				screenPos := ui.GetScreenCoordsForItem(updatedItem)

				ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
				WaitForItemInLocation(updatedItem.UnitID, item.LocationCube, 1500)
			}
		}
	}

	return nil
}

func CubeTransmute() error {
	ctx := context.Get()

	err := ensureCubeIsOpen()
	if err != nil {
		return err
	}

	ctx.Logger.Debug("Transmuting items in the Horadric Cube")

	// Store items before transmute to detect change
	itemsBefore := len(ctx.Data.Inventory.ByLocation(item.LocationCube))

	if ctx.Data.LegacyGraphics {
		ctx.HID.Click(game.LeftButton, ui.CubeTransmuteBtnXClassic, ui.CubeTransmuteBtnYClassic)
	} else {
		ctx.HID.Click(game.LeftButton, ui.CubeTransmuteBtnX, ui.CubeTransmuteBtnY)
	}

	// Wait for transmute to complete (items in cube change)
	WaitForCondition(func() bool {
		ctx.RefreshGameData()
		itemsAfter := len(ctx.Data.Inventory.ByLocation(item.LocationCube))
		return itemsAfter != itemsBefore
	}, 3000, 100)

	// Take the items out of the cube
	ctx.RefreshGameData()
	for _, itm := range ctx.Data.Inventory.ByLocation(item.LocationCube) {
		ctx.Logger.Debug("Moving Item to the inventory", slog.String("Item", string(itm.Name)))

		screenPos := ui.GetScreenCoordsForItem(itm)

		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
		WaitForItemNotInLocation(itm.UnitID, item.LocationCube, 1500)
	}

	return step.CloseAllMenus()
}

func EmptyCube() error {
	err := ensureCubeIsOpen()
	if err != nil {
		return err
	}

	err = ensureCubeIsEmpty()
	if err != nil {
		return err
	}

	return step.CloseAllMenus()
}

func ensureCubeIsEmpty() error {
	ctx := context.Get()
	if !ctx.Data.OpenMenus.Cube {
		return errors.New("horadric Cube window not detected")
	}

	cubeItems := ctx.Data.Inventory.ByLocation(item.LocationCube)
	if len(cubeItems) == 0 {
		return nil
	}

	ctx.Logger.Debug("Emptying the Horadric Cube")
	for _, itm := range cubeItems {
		ctx.Logger.Debug("Moving Item to the inventory", slog.String("Item", string(itm.Name)))

		screenPos := ui.GetScreenCoordsForItem(itm)

		ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)

		if !WaitForItemNotInLocation(itm.UnitID, item.LocationCube, 1500) {
			return fmt.Errorf("item %s could not be removed from the cube", itm.Name)
		}
	}

	ctx.HID.PressKey(win.VK_ESCAPE)
	WaitForCondition(func() bool {
		ctx.RefreshGameData()
		return !ctx.Data.OpenMenus.Cube
	}, 1000, 50)

	stashInventory(true)

	return ensureCubeIsOpen()
}

func ensureCubeIsOpen() error {
	ctx := context.Get()
	ctx.Logger.Debug("Opening Horadric Cube...")

	if ctx.Data.OpenMenus.Cube {
		ctx.Logger.Debug("Horadric Cube window already open")
		return nil
	}

	cube, found := ctx.Data.Inventory.Find("HoradricCube", item.LocationInventory, item.LocationStash)
	if !found {
		return errors.New("horadric cube not found in inventory")
	}

	// If cube is in stash, switch to the correct tab
	if cube.Location.LocationType == item.LocationStash || cube.Location.LocationType == item.LocationSharedStash {
		ctx := context.Get()

		// Ensure stash is open
		if !ctx.Data.OpenMenus.Stash {
			bank, _ := ctx.Data.Objects.FindOne(object.Bank)
			err := InteractObject(bank, func() bool {
				return ctx.Data.OpenMenus.Stash
			})
			if err != nil {
				return err
			}
		}

		SwitchStashTab(cube.Location.Page + 1)
	}

	screenPos := ui.GetScreenCoordsForItem(cube)

	maxAttempts := 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx.HID.Click(game.RightButton, screenPos.X, screenPos.Y)

		if WaitForMenuOpen(MenuCube, 1500) {
			ctx.Logger.Debug("Horadric Cube window detected")
			return nil
		}

		if attempt < maxAttempts {
			ctx.Logger.Debug("Cube open attempt failed, retrying...", "attempt", attempt)
		}
	}

	return errors.New("horadric Cube window not detected")
}
