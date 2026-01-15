package action

import (
	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/utils"
)

const (
	defaultPollInterval = 50  // ms
	defaultTimeout      = 1500 // ms
)

// WaitForCondition polls until condition returns true or timeout is reached.
// Returns true if condition was met, false if timeout occurred.
func WaitForCondition(condition func() bool, timeoutMs int, pollIntervalMs int) bool {
	if timeoutMs <= 0 {
		timeoutMs = defaultTimeout
	}
	if pollIntervalMs <= 0 {
		pollIntervalMs = defaultPollInterval
	}

	elapsed := 0
	for elapsed < timeoutMs {
		if condition() {
			return true
		}
		utils.Sleep(pollIntervalMs)
		elapsed += pollIntervalMs
	}
	return false
}

// RetryWithPolling executes an action and polls for success condition.
// Retries up to maxAttempts times if condition is not met.
// Returns true if condition was met within attempts, false otherwise.
func RetryWithPolling(action func(), condition func() bool, maxAttempts int, timeoutMs int) bool {
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if timeoutMs <= 0 {
		timeoutMs = defaultTimeout
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		action()
		if WaitForCondition(condition, timeoutMs, defaultPollInterval) {
			return true
		}
	}
	return false
}

// WaitForItemNotInLocation waits until an item is no longer in the specified location.
func WaitForItemNotInLocation(unitID data.UnitID, location item.LocationType, timeoutMs int) bool {
	ctx := context.Get()
	return WaitForCondition(func() bool {
		ctx.RefreshGameData()
		for _, it := range ctx.Data.Inventory.AllItems {
			if it.UnitID == unitID && it.Location.LocationType == location {
				return false
			}
		}
		return true
	}, timeoutMs, defaultPollInterval)
}

// WaitForItemIdentified waits until an item is identified.
func WaitForItemIdentified(unitID data.UnitID, timeoutMs int) bool {
	ctx := context.Get()
	return WaitForCondition(func() bool {
		ctx.RefreshGameData()
		for _, it := range ctx.Data.Inventory.AllItems {
			if it.UnitID == unitID {
				return it.Identified
			}
		}
		return false
	}, timeoutMs, defaultPollInterval)
}

// WaitForCursorEmpty waits until there is no item on the cursor.
func WaitForCursorEmpty(timeoutMs int) bool {
	ctx := context.Get()
	return WaitForCondition(func() bool {
		ctx.RefreshGameData()
		return len(ctx.Data.Inventory.ByLocation(item.LocationCursor)) == 0
	}, timeoutMs, defaultPollInterval)
}

// WaitForMenuOpen waits until a specific menu is open.
type MenuType int

const (
	MenuInventory MenuType = iota
	MenuStash
	MenuCube
	MenuNPCInteract
	MenuNPCShop
	MenuWaypoint
	MenuSkillTree
	MenuCharacter
)

func WaitForMenuOpen(menu MenuType, timeoutMs int) bool {
	ctx := context.Get()
	return WaitForCondition(func() bool {
		ctx.RefreshGameData()
		switch menu {
		case MenuInventory:
			return ctx.Data.OpenMenus.Inventory
		case MenuStash:
			return ctx.Data.OpenMenus.Stash
		case MenuCube:
			return ctx.Data.OpenMenus.Cube
		case MenuNPCInteract:
			return ctx.Data.OpenMenus.NPCInteract
		case MenuNPCShop:
			return ctx.Data.OpenMenus.NPCShop
		case MenuWaypoint:
			return ctx.Data.OpenMenus.Waypoint
		case MenuSkillTree:
			return ctx.Data.OpenMenus.SkillTree
		case MenuCharacter:
			return ctx.Data.OpenMenus.Character
		}
		return false
	}, timeoutMs, defaultPollInterval)
}

// WaitForItemInBelt waits until an item appears in the belt.
func WaitForItemInBelt(unitID data.UnitID, timeoutMs int) bool {
	ctx := context.Get()
	return WaitForCondition(func() bool {
		ctx.RefreshGameData()
		for _, it := range ctx.Data.Inventory.ByLocation(item.LocationBelt) {
			if it.UnitID == unitID {
				return true
			}
		}
		return false
	}, timeoutMs, defaultPollInterval)
}

// WaitForAreaChange waits until the player is in the target area.
func WaitForAreaChange(targetArea area.ID, timeoutMs int) bool {
	ctx := context.Get()
	return WaitForCondition(func() bool {
		ctx.RefreshGameData()
		return ctx.Data.PlayerUnit.Area == targetArea
	}, timeoutMs, defaultPollInterval)
}

// WaitForObjectNotSelectable waits until an object is no longer selectable (opened/used).
func WaitForObjectNotSelectable(objID data.UnitID, timeoutMs int) bool {
	ctx := context.Get()
	return WaitForCondition(func() bool {
		ctx.RefreshGameData()
		obj, found := ctx.Data.Objects.FindByID(objID)
		if !found {
			return true // Object no longer exists
		}
		return !obj.Selectable
	}, timeoutMs, defaultPollInterval)
}

// WaitForGoldChange waits until inventory gold changes from the initial value.
func WaitForGoldChange(initialGold int, timeoutMs int) bool {
	ctx := context.Get()
	return WaitForCondition(func() bool {
		ctx.RefreshGameData()
		return ctx.Data.Inventory.Gold != initialGold
	}, timeoutMs, defaultPollInterval)
}

// WaitForItemInLocation waits until an item appears in the specified location.
func WaitForItemInLocation(unitID data.UnitID, location item.LocationType, timeoutMs int) bool {
	ctx := context.Get()
	return WaitForCondition(func() bool {
		ctx.RefreshGameData()
		for _, it := range ctx.Data.Inventory.AllItems {
			if it.UnitID == unitID && it.Location.LocationType == location {
				return true
			}
		}
		return false
	}, timeoutMs, defaultPollInterval)
}
