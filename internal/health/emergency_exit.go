package health

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/hectorgimenez/koolo/internal/config"
	"github.com/hectorgimenez/koolo/internal/game"
	"github.com/lxn/win"
)

// ExitMethod defines the method used to exit the game in emergency situations
type ExitMethod string

const (
	ExitMethodKill    ExitMethod = "kill"     // process.Kill() - fastest but risky
	ExitMethodClose   ExitMethod = "close"    // WM_CLOSE - graceful, game saves
	ExitMethodEscSave ExitMethod = "esc_save" // ESC + menu click - current method
)

// ErrEmergencyExit is returned when emergency exit is triggered
var ErrEmergencyExit = errors.New("emergency exit triggered")

// hpSample stores HP value with timestamp for spike detection
type hpSample struct {
	hp        int
	timestamp time.Time
}

// EmergencyExitManager monitors player health and triggers emergency exit when needed
type EmergencyExitManager struct {
	data       *game.Data
	cfg        *config.CharacterCfg
	logger     *slog.Logger
	hwnd       win.HWND
	pid        uint32
	exitGameFn func() error // ExitGame() function for esc_save method

	// HP history for spike detection
	hpHistory      []hpSample
	maxHistorySize int
	lastCheckTime  time.Time

	mu sync.Mutex
}

// NewEmergencyExitManager creates a new EmergencyExitManager instance
func NewEmergencyExitManager(
	data *game.Data,
	cfg *config.CharacterCfg,
	logger *slog.Logger,
	hwnd win.HWND,
	pid uint32,
	exitGameFn func() error,
) *EmergencyExitManager {
	return &EmergencyExitManager{
		data:           data,
		cfg:            cfg,
		logger:         logger,
		hwnd:           hwnd,
		pid:            pid,
		exitGameFn:     exitGameFn,
		hpHistory:      make([]hpSample, 0, 100),
		maxHistorySize: 100,
	}
}

// CheckEmergencyExit checks all emergency conditions and triggers exit if needed
// Returns true if emergency exit was triggered, along with any error
func (em *EmergencyExitManager) CheckEmergencyExit() (triggered bool, err error) {
	em.mu.Lock()
	defer em.mu.Unlock()

	// Skip if not enabled or in town
	if !em.cfg.Health.EmergencyExitEnabled {
		return false, nil
	}

	if em.data.PlayerUnit.Area.IsTown() {
		return false, nil
	}

	if em.data.PlayerUnit.IsDead() {
		return false, nil
	}

	currentHP := em.data.PlayerUnit.HPPercent()

	// Record HP for spike detection
	em.recordHP(currentHP)

	// Check HP threshold
	if em.checkHPThreshold(currentHP) {
		reason := fmt.Sprintf("HP threshold reached: %d%% <= %d%%", currentHP, em.cfg.Health.EmergencyExitAt)
		if err := em.executeExit(reason); err != nil {
			return true, fmt.Errorf("%w: %v", ErrEmergencyExit, err)
		}
		return true, ErrEmergencyExit
	}

	// Check damage spike
	if em.cfg.Health.DamageSpikeEnabled && em.checkDamageSpike() {
		reason := fmt.Sprintf("Damage spike detected: lost %d%% HP in %dms",
			em.cfg.Health.DamageSpikeThreshold, em.cfg.Health.DamageSpikeDurationMs)
		if err := em.executeExit(reason); err != nil {
			return true, fmt.Errorf("%w: %v", ErrEmergencyExit, err)
		}
		return true, ErrEmergencyExit
	}

	return false, nil
}

// checkHPThreshold checks if HP is at or below the emergency threshold
func (em *EmergencyExitManager) checkHPThreshold(currentHP int) bool {
	if em.cfg.Health.EmergencyExitAt <= 0 {
		return false
	}
	return currentHP <= em.cfg.Health.EmergencyExitAt
}

// checkDamageSpike checks if player lost too much HP in a short time window
func (em *EmergencyExitManager) checkDamageSpike() bool {
	if len(em.hpHistory) < 2 {
		return false
	}

	threshold := em.cfg.Health.DamageSpikeThreshold
	durationMs := em.cfg.Health.DamageSpikeDurationMs

	if threshold <= 0 || durationMs <= 0 {
		return false
	}

	duration := time.Duration(durationMs) * time.Millisecond
	now := time.Now()
	cutoff := now.Add(-duration)

	// Find the oldest sample within the time window
	var oldestInWindow *hpSample
	for i := range em.hpHistory {
		if em.hpHistory[i].timestamp.After(cutoff) {
			oldestInWindow = &em.hpHistory[i]
			break
		}
	}

	if oldestInWindow == nil {
		return false
	}

	// Get current HP (most recent sample)
	currentSample := em.hpHistory[len(em.hpHistory)-1]

	// Calculate HP lost
	hpLost := oldestInWindow.hp - currentSample.hp

	return hpLost >= threshold
}

// recordHP adds a new HP sample to the history
func (em *EmergencyExitManager) recordHP(hp int) {
	now := time.Now()

	// Add new sample
	em.hpHistory = append(em.hpHistory, hpSample{
		hp:        hp,
		timestamp: now,
	})

	// Remove old samples (keep only those within the last 5 seconds or max size)
	cutoff := now.Add(-5 * time.Second)
	startIdx := 0
	for i, sample := range em.hpHistory {
		if sample.timestamp.After(cutoff) {
			startIdx = i
			break
		}
	}

	if startIdx > 0 {
		em.hpHistory = em.hpHistory[startIdx:]
	}

	// Enforce max history size
	if len(em.hpHistory) > em.maxHistorySize {
		em.hpHistory = em.hpHistory[len(em.hpHistory)-em.maxHistorySize:]
	}
}

// executeExit performs the emergency exit using the configured method
func (em *EmergencyExitManager) executeExit(reason string) error {
	method := ExitMethod(em.cfg.Health.EmergencyExitMethod)
	if method == "" {
		method = ExitMethodClose // Default to close (balanced)
	}

	em.logger.Error("EMERGENCY EXIT TRIGGERED",
		slog.String("reason", reason),
		slog.String("method", string(method)),
		slog.Int("currentHP", em.data.PlayerUnit.HPPercent()),
	)

	switch method {
	case ExitMethodKill:
		return em.killProcess()
	case ExitMethodClose:
		return em.closeWindow()
	case ExitMethodEscSave:
		return em.escSave()
	default:
		em.logger.Warn("Unknown exit method, falling back to close", slog.String("method", string(method)))
		return em.closeWindow()
	}
}

// killProcess terminates the game process immediately (fastest, but risky)
func (em *EmergencyExitManager) killProcess() error {
	em.logger.Info("Emergency exit: Killing process", slog.Uint64("pid", uint64(em.pid)))

	process, err := os.FindProcess(int(em.pid))
	if err != nil {
		return fmt.Errorf("failed to find process: %w", err)
	}

	if err := process.Kill(); err != nil {
		return fmt.Errorf("failed to kill process: %w", err)
	}

	return nil
}

// closeWindow sends WM_CLOSE to the game window (graceful, game saves)
func (em *EmergencyExitManager) closeWindow() error {
	em.logger.Info("Emergency exit: Sending WM_CLOSE to window", slog.Uint64("hwnd", uint64(em.hwnd)))

	// Send WM_CLOSE message to the game window
	win.PostMessage(em.hwnd, win.WM_CLOSE, 0, 0)

	return nil
}

// escSave uses the traditional ESC + menu click method (safest, but slower)
func (em *EmergencyExitManager) escSave() error {
	em.logger.Info("Emergency exit: Using ESC + Save method")

	if em.exitGameFn == nil {
		return errors.New("exitGameFn is not set")
	}

	return em.exitGameFn()
}

// UpdateConfig updates the configuration reference (useful when config changes)
func (em *EmergencyExitManager) UpdateConfig(cfg *config.CharacterCfg) {
	em.mu.Lock()
	defer em.mu.Unlock()
	em.cfg = cfg
}

// ClearHistory clears the HP history (useful when entering a new game)
func (em *EmergencyExitManager) ClearHistory() {
	em.mu.Lock()
	defer em.mu.Unlock()
	em.hpHistory = em.hpHistory[:0]
}
