package context

import (
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/area"
	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/drop"
	"github.com/hectorgimenez/koolo/internal/event"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/hectorgimenez/koolo/internal/health"
	"github.com/hectorgimenez/koolo/internal/pather"
	"github.com/hectorgimenez/koolo/internal/utils"
)

var mu sync.Mutex
var botContexts = make(map[uint64]*Status)

type Priority int

type StopFunc func()

const (
	PriorityHigh       = 0
	PriorityNormal     = 1
	PriorityBackground = 5
	PriorityPause      = 10
	PriorityStop       = 100
)

type Status struct {
	*Context
	Priority Priority
}

type Context struct {
	Name                  string
	ExecutionPriority     Priority
	CharacterCfg          *config.CharacterCfg
	Data                  *game.Data
	EventListener         *event.Listener
	HID                   *game.HID
	Logger                *slog.Logger
	Manager               *game.Manager
	GameReader            *game.MemoryReader
	MemoryInjector        *game.MemoryInjector
	PathFinder            *pather.PathFinder
	BeltManager           *health.BeltManager
	HealthManager         *health.Manager
	DefenseManager        *health.DefenseManager
	Char                  Character
	LastBuffAt            time.Time
	ContextDebug          map[Priority]*Debug
	CurrentGame           *CurrentGameHelper
	SkillPointIndex       int // NEW FIELD: Tracks the next skill to consider from the character's SkillPoints() list
	ForceAttack           bool
	StopSupervisorFn      StopFunc
	CleanStopRequested    bool
	RestartWithCharacter  string
	PacketSender          *game.PacketSender
	IsLevelingCharacter   *bool
	ManualModeActive      bool          // Manual play mode: stops after character selection
	LastPortalTick        time.Time     // NEW FIELD: Tracks last portal creation for spam prevention
	IsBossEquipmentActive bool          // flag for barb leveling
	Drop                  *drop.Manager // Drop: Per-supervisor Drop manager
	lastRefreshTime       time.Time
	refreshMutex          sync.RWMutex
	refreshInterval       time.Duration
	checkItemsAfterDeath  func() // Callback para verificar itens após morte de monstro
}

type Debug struct {
	LastAction string `json:"lastAction"`
	LastStep   string `json:"lastStep"`
}

type CurrentGameHelper struct {
	BlacklistedItems []data.Item
	PickedUpItems    map[int]int
	AreaCorrection   struct {
		Enabled      bool
		ExpectedArea area.ID
	}
	PickupItems                bool
	IsPickingItems             bool
	IsPickingItemsSetAt        time.Time // Tracks when IsPickingItems was set to true
	FailedToCreateGameAttempts int
	FailedMenuAttempts         int
	// When this is set, the supervisor will stop and the manager will start a new supervisor for the specified character.
	SwitchToCharacter string
	// Used to store the original character name when muling, so we can switch back.
	OriginalCharacter string
	CurrentMuleIndex  int
	ShouldCheckStash  bool
	StashFull         bool
	IsStuck           bool      // Flag to track if bot is stuck
	StuckSince        time.Time // Time when stuck was first detected
	mutex             sync.Mutex
}

func (ctx *Context) StopSupervisor() {
	if ctx.StopSupervisorFn != nil {
		ctx.Logger.Info("Game logic requested supervisor stop.", "source", "context")
		ctx.CleanStopRequested = true // SET THE FLAG
		ctx.StopSupervisorFn()
	} else {
		ctx.Logger.Warn("StopSupervisorFn is not set. Cannot stop supervisor from context.")
	}
}

func NewContext(name string) *Status {
	ctx := &Context{
		Name:              name,
		Data:              &game.Data{},
		ExecutionPriority: PriorityNormal,
		ContextDebug: map[Priority]*Debug{
			PriorityBackground: {},
			PriorityNormal:     {},
			PriorityHigh:       {},
			PriorityPause:      {},
			PriorityStop:       {},
		},
		CurrentGame:      NewGameHelper(),
		SkillPointIndex:  0,
		ForceAttack:      false,
		ManualModeActive: false, // Explicitly initialize to false
		refreshInterval:  0 * time.Millisecond,
	}
	ctx.Drop = drop.NewManager(name, ctx.Logger)
	ctx.AttachRoutine(PriorityNormal)

	// Initialize ping getter for adaptive delays (avoids import cycle)
	utils.SetPingGetter(func() int {
		if ctx.Data != nil && ctx.Data.Game.Ping > 0 {
			return ctx.Data.Game.Ping
		}
		return 50 // Safe default
	})

	return Get()
}

func NewGameHelper() *CurrentGameHelper {
	return &CurrentGameHelper{
		PickupItems:                true,
		PickedUpItems:              make(map[int]int),
		BlacklistedItems:           []data.Item{},
		FailedToCreateGameAttempts: 0,
		IsStuck:                    false,
		StuckSince:                 time.Time{},
	}
}

func Get() *Status {
	mu.Lock()
	defer mu.Unlock()
	return botContexts[getGoroutineID()]
}

func (s *Status) SetLastAction(actionName string) {
	s.Context.ContextDebug[s.Priority].LastAction = actionName
}

func (s *Status) SetLastStep(stepName string) {
	s.Context.ContextDebug[s.Priority].LastStep = stepName
}

func getGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	stackTrace := string(buf[:n])
	fields := strings.Fields(stackTrace)
	id, _ := strconv.ParseUint(fields[1], 10, 64)

	return id
}

func (ctx *Context) RefreshGameData() {
	ctx.refreshMutex.RLock()
	now := time.Now()
	// Early return if cache is still valid
	if !ctx.lastRefreshTime.IsZero() && now.Sub(ctx.lastRefreshTime) < ctx.refreshInterval {
		ctx.refreshMutex.RUnlock()
		return
	}
	ctx.refreshMutex.RUnlock()

	// Upgrade to write lock for actual refresh
	ctx.refreshMutex.Lock()
	defer ctx.refreshMutex.Unlock()

	// Double-check pattern: another goroutine might have refreshed while we waited
	if !ctx.lastRefreshTime.IsZero() && time.Since(ctx.lastRefreshTime) < ctx.refreshInterval {
		return
	}

	*ctx.Data = ctx.GameReader.GetData()
	if ctx.IsLevelingCharacter == nil {
		_, isLevelingCharacter := ctx.Char.(LevelingCharacter)
		ctx.IsLevelingCharacter = &isLevelingCharacter
	}
	ctx.Data.IsLevelingCharacter = *ctx.IsLevelingCharacter
	ctx.lastRefreshTime = time.Now()
}

// RefreshGameDataForce forces a refresh of game data, ignoring the cache TTL.
// Use this when you need guaranteed fresh data, such as after critical actions.
func (ctx *Context) RefreshGameDataForce() {
	ctx.refreshMutex.Lock()
	defer ctx.refreshMutex.Unlock()

	*ctx.Data = ctx.GameReader.GetData()
	if ctx.IsLevelingCharacter == nil {
		_, isLevelingCharacter := ctx.Char.(LevelingCharacter)
		ctx.IsLevelingCharacter = &isLevelingCharacter
	}
	ctx.Data.IsLevelingCharacter = *ctx.IsLevelingCharacter
	ctx.lastRefreshTime = time.Now()
}

func (ctx *Context) RefreshInventory() {
	ctx.Data.Inventory = ctx.GameReader.GetInventory()
}

func (ctx *Context) Detach() {
	mu.Lock()
	defer mu.Unlock()
	delete(botContexts, getGoroutineID())
}

func (ctx *Context) AttachRoutine(priority Priority) {
	mu.Lock()
	defer mu.Unlock()
	botContexts[getGoroutineID()] = &Status{Priority: priority, Context: ctx}
}

func (ctx *Context) SwitchPriority(priority Priority) {
	ctx.ExecutionPriority = priority
}

func (ctx *Context) DisableItemPickup() {
	ctx.CurrentGame.PickupItems = false
}

func (ctx *Context) EnableItemPickup() {
	ctx.CurrentGame.PickupItems = true
}

func (ctx *Context) SetPickingItems(value bool) {
	ctx.CurrentGame.mutex.Lock()
	ctx.CurrentGame.IsPickingItems = value
	if value {
		ctx.CurrentGame.IsPickingItemsSetAt = time.Now()
	} else {
		ctx.CurrentGame.IsPickingItemsSetAt = time.Time{} // Reset timestamp when flag is cleared
	}
	ctx.CurrentGame.mutex.Unlock()
}

// SetCheckItemsAfterDeathCallback sets a callback function to check items after monster death
// This allows step package to trigger item checks without importing action package
func (ctx *Context) SetCheckItemsAfterDeathCallback(fn func()) {
	ctx.checkItemsAfterDeath = fn
}

// CheckItemsAfterDeath calls the registered callback to check items after monster death
// Returns true if callback was called, false if no callback is registered
func (ctx *Context) CheckItemsAfterDeath() bool {
	if ctx.checkItemsAfterDeath != nil {
		ctx.checkItemsAfterDeath()
		return true
	}
	return false
}

func (s *Status) PauseIfNotPriority() {
	// This prevents bot from trying to move when loading screen is shown.
	if s.Data.OpenMenus.LoadingScreen {
		time.Sleep(time.Millisecond * 5)
	}

	for s.Priority != s.ExecutionPriority {
		if s.ExecutionPriority == PriorityStop {
			panic("Bot is stopped")
		}

		time.Sleep(time.Millisecond * 10)
	}
}

// PauseIfNotPriorityWithTimeout is like PauseIfNotPriority but with a maximum wait time.
// Returns true if priority was acquired, false if timeout was reached.
func (s *Status) PauseIfNotPriorityWithTimeout(timeout time.Duration) bool {
	// This prevents bot from trying to move when loading screen is shown.
	if s.Data.OpenMenus.LoadingScreen {
		time.Sleep(time.Millisecond * 5)
	}

	deadline := time.Now().Add(timeout)
	for s.Priority != s.ExecutionPriority {
		if s.ExecutionPriority == PriorityStop {
			panic("Bot is stopped")
		}

		if time.Now().After(deadline) {
			return false // Timeout reached
		}

		time.Sleep(time.Millisecond * 10)
	}
	return true
}
func (ctx *Context) WaitForGameToLoad() {
	for ctx.Data.OpenMenus.LoadingScreen {
		time.Sleep(100 * time.Millisecond)
		ctx.RefreshGameData()
	}
	// Add a small buffer to ensure everything is fully loaded
	time.Sleep(300 * time.Millisecond)
}

func (ctx *Context) Cleanup() {
	ctx.Logger.Debug("Resetting blacklisted items")

	// Remove all items from the blacklisted items list
	ctx.CurrentGame.BlacklistedItems = []data.Item{}

	// flag reset in case something goes wrong (barb leveling)
	ctx.IsBossEquipmentActive = false

	// Remove all items from the picked up items map if it exceeds 200 items
	if len(ctx.CurrentGame.PickedUpItems) > 200 {
		ctx.Logger.Debug("Resetting picked up items map due to exceeding 200 items")
		ctx.CurrentGame.PickedUpItems = make(map[int]int)
	}
	// Reset counters on cleanup for a new session
	ctx.CurrentGame.FailedToCreateGameAttempts = 0
	ctx.CurrentGame.FailedMenuAttempts = 0 // Also reset this on cleanup
}

// ResetStuckItemPickup checks if IsPickingItems has been stuck for more than the timeout duration
// and resets it if necessary. Returns true if the flag was reset, false otherwise.
func (ctx *Context) ResetStuckItemPickup(timeout time.Duration) bool {
	ctx.CurrentGame.mutex.Lock()
	defer ctx.CurrentGame.mutex.Unlock()

	if !ctx.CurrentGame.IsPickingItems {
		return false // Flag is not set, nothing to reset
	}

	if ctx.CurrentGame.IsPickingItemsSetAt.IsZero() {
		// Timestamp not set, assume it's stuck and reset
		ctx.Logger.Warn("IsPickingItems flag is set but timestamp is zero, resetting flag")
		ctx.CurrentGame.IsPickingItems = false
		ctx.CurrentGame.IsPickingItemsSetAt = time.Time{}
		return true
	}

	if time.Since(ctx.CurrentGame.IsPickingItemsSetAt) > timeout {
		ctx.Logger.Warn("IsPickingItems flag has been stuck for too long, resetting to recover",
			"duration", time.Since(ctx.CurrentGame.IsPickingItemsSetAt),
			"timeout", timeout,
		)
		ctx.CurrentGame.IsPickingItems = false
		ctx.CurrentGame.IsPickingItemsSetAt = time.Time{}
		return true
	}

	return false
}
