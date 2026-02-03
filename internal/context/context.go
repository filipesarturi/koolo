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
	Name                   string
	ExecutionPriority      Priority
	CharacterCfg           *config.CharacterCfg
	Data                   *game.Data
	EventListener          *event.Listener
	HID                    *game.HID
	Logger                 *slog.Logger
	Manager                *game.Manager
	GameReader             *game.MemoryReader
	MemoryInjector         *game.MemoryInjector
	PathFinder             *pather.PathFinder
	BeltManager            *health.BeltManager
	HealthManager          *health.Manager
	DefenseManager         *health.DefenseManager
	EmergencyExitManager   *health.EmergencyExitManager
	Char                   Character
	LastBuffAt             time.Time
	ContextDebug           map[Priority]*Debug
	CurrentGame            *CurrentGameHelper
	SkillPointIndex        int // NEW FIELD: Tracks the next skill to consider from the character's SkillPoints() list
	ForceAttack            bool
	StopSupervisorFn       StopFunc
	CleanStopRequested     bool
	RestartWithCharacter   string
	PacketSender           *game.PacketSender
	IsLevelingCharacter    *bool
	ManualModeActive       bool          // Manual play mode: stops after character selection
	LastPortalTick         time.Time     // NEW FIELD: Tracks last portal creation for spam prevention
	IsBossEquipmentActive  bool          // flag for barb leveling
	Drop                   *drop.Manager // Drop: Per-supervisor Drop manager
	MercReviveFailedNoGold bool          // Flag to track if last revive attempt failed due to insufficient gold
	lastRefreshTime        time.Time
	refreshMutex           sync.RWMutex
	refreshInterval        time.Duration
	checkItemsAfterDeath   func() // Callback para verificar itens após morte de monstro
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
	*ctx.Data = ctx.GameReader.GetData()

	if ctx.IsLevelingCharacter == nil {
		_, isLevelingCharacter := ctx.Char.(LevelingCharacter)
		ctx.IsLevelingCharacter = &isLevelingCharacter
	}

	ctx.Data.IsLevelingCharacter = *ctx.IsLevelingCharacter
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

// IsPickingItems returns if item pickup is in progress (thread-safe)
func (ctx *Context) IsPickingItems() bool {
	ctx.CurrentGame.mutex.Lock()
	defer ctx.CurrentGame.mutex.Unlock()
	return ctx.CurrentGame.IsPickingItems
}

// GetPickingItemsInfo returns if pickup is in progress and for how long (thread-safe)
func (ctx *Context) GetPickingItemsInfo() (bool, time.Duration) {
	ctx.CurrentGame.mutex.Lock()
	defer ctx.CurrentGame.mutex.Unlock()
	if ctx.CurrentGame.IsPickingItems {
		return true, time.Since(ctx.CurrentGame.IsPickingItemsSetAt)
	}
	return false, 0
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
	s.PauseIfNotPriorityWithTimeout(30 * time.Second)
}

// PauseIfNotPriorityWithTimeout pauses execution if priority doesn't match, with a custom timeout
func (s *Status) PauseIfNotPriorityWithTimeout(maxWait time.Duration) {
	// DEBUG: Log function entry for diagnosis
	s.Logger.Debug("[DEBUG] PauseIfNotPriorityWithTimeout called",
		slog.Duration("maxWait", maxWait),
		slog.Int("currentPriority", int(s.Priority)),
		slog.Int("executionPriority", int(s.ExecutionPriority)),
		slog.Bool("loadingScreen", s.Data.OpenMenus.LoadingScreen),
		slog.String("area", s.Data.PlayerUnit.Area.Area().Name),
	)

	// This prevents bot from trying to move when loading screen is shown.
	if s.Data.OpenMenus.LoadingScreen {
		time.Sleep(time.Millisecond * 5)
	}

	if s.Priority == s.ExecutionPriority {
		// DEBUG: Fast path - no pause needed
		s.Logger.Debug("[DEBUG] PauseIfNotPriorityWithTimeout: fast path, priorities match")
		return // Fast path: no pause needed
	}

	// Track how long we've been waiting
	pauseStart := time.Now()
	loggedOnce := false
	loggedTwice := false
	lastAction := ""
	lastStep := ""
	if debug, ok := s.ContextDebug[s.Priority]; ok && debug != nil {
		lastAction = debug.LastAction
		lastStep = debug.LastStep
	}

	// DEBUG: Log initial wait state
	s.Logger.Debug("[DEBUG] PauseIfNotPriorityWithTimeout: starting wait",
		slog.String("lastAction", lastAction),
		slog.String("lastStep", lastStep),
		slog.Int("posX", s.Data.PlayerUnit.Position.X),
		slog.Int("posY", s.Data.PlayerUnit.Position.Y),
	)

	// Track priority state changes to detect potential deadlocks
	lastExecutionPriority := s.ExecutionPriority
	priorityChangeCount := 0
	const maxPriorityChanges = 5 // Maximum number of priority changes before forcing continue

	for s.Priority != s.ExecutionPriority {
		// DEBUG: Log each iteration of the wait loop (every 500ms)
		if time.Since(pauseStart)%500*time.Millisecond < 50*time.Millisecond {
			s.Logger.Debug("[DEBUG] PauseIfNotPriorityWithTimeout: still waiting",
				slog.Duration("elapsed", time.Since(pauseStart)),
				slog.Int("priority", int(s.Priority)),
				slog.Int("executionPriority", int(s.ExecutionPriority)),
			)
		}

		if s.ExecutionPriority == PriorityStop {
			s.Logger.Error("[DEBUG] Panic: Bot is stopped during PauseIfNotPriorityWithTimeout")
			panic("Bot is stopped")
		}

		// Detect priority changes to identify potential deadlocks
		if s.ExecutionPriority != lastExecutionPriority {
			priorityChangeCount++
			lastExecutionPriority = s.ExecutionPriority
			s.Logger.Debug("Priority changed during PauseIfNotPriority",
				slog.Int("changeCount", priorityChangeCount),
				slog.Int("oldPriority", int(lastExecutionPriority)),
				slog.Int("newPriority", int(s.ExecutionPriority)),
				slog.Int("waitingPriority", int(s.Priority)),
			)
		}

		// Force continue if priority is changing too frequently (indicates deadlock)
		if priorityChangeCount > maxPriorityChanges {
			s.Logger.Error("Priority deadlock detected - forcing continue to prevent infinite loop",
				slog.Int("priorityChangeCount", priorityChangeCount),
				slog.Int("maxPriorityChanges", maxPriorityChanges),
				slog.Int("priority", int(s.Priority)),
				slog.Int("executionPriority", int(s.ExecutionPriority)),
				slog.Duration("elapsed", time.Since(pauseStart)),
				slog.String("lastAction", lastAction),
				slog.String("lastStep", lastStep),
				slog.Int("posX", s.Data.PlayerUnit.Position.X),
				slog.Int("posY", s.Data.PlayerUnit.Position.Y),
			)
			return
		}

		// Log warning if paused for too long
		pauseDuration := time.Since(pauseStart)
		if pauseDuration > 5*time.Second && !loggedOnce {
			s.Logger.Warn("PauseIfNotPriority blocking for extended time",
				slog.Duration("duration", pauseDuration),
				slog.Int("priority", int(s.Priority)),
				slog.Int("executionPriority", int(s.ExecutionPriority)),
				slog.String("lastAction", lastAction),
				slog.String("lastStep", lastStep),
				slog.Int("posX", s.Data.PlayerUnit.Position.X),
				slog.Int("posY", s.Data.PlayerUnit.Position.Y),
				slog.String("area", s.Data.PlayerUnit.Area.Area().Name),
				slog.Bool("inventoryOpen", s.Data.OpenMenus.Inventory),
				slog.Bool("stashOpen", s.Data.OpenMenus.Stash),
				slog.Bool("merchantOpen", s.Data.OpenMenus.NPCShop),
				slog.Int("priorityChanges", priorityChangeCount),
			)
			loggedOnce = true
		}

		// Log error if paused for very long time (indicates serious issue)
		if pauseDuration > 15*time.Second && !loggedTwice {
			s.Logger.Error("PauseIfNotPriority blocking for very long time - possible deadlock",
				slog.Duration("duration", pauseDuration),
				slog.Int("priority", int(s.Priority)),
				slog.Int("executionPriority", int(s.ExecutionPriority)),
				slog.String("lastAction", lastAction),
				slog.String("lastStep", lastStep),
				slog.Int("posX", s.Data.PlayerUnit.Position.X),
				slog.Int("posY", s.Data.PlayerUnit.Position.Y),
				slog.String("area", s.Data.PlayerUnit.Area.Area().Name),
				slog.Bool("inventoryOpen", s.Data.OpenMenus.Inventory),
				slog.Bool("stashOpen", s.Data.OpenMenus.Stash),
				slog.Bool("merchantOpen", s.Data.OpenMenus.NPCShop),
				slog.Int("priorityChanges", priorityChangeCount),
			)
			loggedTwice = true
		}

		// Safety timeout to prevent infinite blocking
		if pauseDuration > maxWait {
			// Reset priority to PriorityNormal to prevent deadlock after timeout
			// This ensures subsequent PauseIfNotPriority calls don't get stuck
			if s.ExecutionPriority == PriorityHigh {
				s.ExecutionPriority = PriorityNormal
				s.Logger.Debug("[DEBUG] Resetting ExecutionPriority to PriorityNormal after timeout",
					slog.Duration("timeoutDuration", pauseDuration))
			}
			s.Logger.Error("PauseIfNotPriority timeout - forcing continue",
				slog.Duration("duration", pauseDuration),
				slog.Duration("maxWait", maxWait),
				slog.Int("priority", int(s.Priority)),
				slog.Int("executionPriority", int(s.ExecutionPriority)),
				slog.String("lastAction", lastAction),
				slog.String("lastStep", lastStep),
				slog.Int("posX", s.Data.PlayerUnit.Position.X),
				slog.Int("posY", s.Data.PlayerUnit.Position.Y),
				slog.String("area", s.Data.PlayerUnit.Area.Area().Name),
				slog.Bool("inventoryOpen", s.Data.OpenMenus.Inventory),
				slog.Bool("stashOpen", s.Data.OpenMenus.Stash),
				slog.Bool("merchantOpen", s.Data.OpenMenus.NPCShop),
				slog.Int("priorityChanges", priorityChangeCount),
			)
			return // Force continue instead of blocking forever
		}

		time.Sleep(time.Millisecond * 10)
	}

	// DEBUG: Log successful exit
	s.Logger.Debug("[DEBUG] PauseIfNotPriorityWithTimeout: completed",
		slog.Duration("totalWait", time.Since(pauseStart)),
	)
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
