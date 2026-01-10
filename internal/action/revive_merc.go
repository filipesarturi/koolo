package action

import (
	"errors"
	"fmt"

	"github.com/hectorgimenez/d2go/pkg/data/npc"
	botCtx "github.com/hectorgimenez/koolo/internal/context" // ALIAS THIS IMPORT
	"github.com/hectorgimenez/koolo/internal/town"
	"github.com/hectorgimenez/koolo/internal/utils"
	"github.com/lxn/win"
	"github.com/hectorgimenez/d2go/pkg/data/item"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
)


// GetAvailableGold retorna o dinheiro disponível para uso: inventário + aba personal (excluindo shared tabs)
func GetAvailableGold(status *botCtx.Status) int {
	// Dinheiro disponível para uso = inventário + aba personal do baú (tab 1)
	// Dinheiro das abas shared (tabs 2-4) NÃO deve ser contabilizado
	inventoryGold := status.Data.Inventory.Gold
	personalStashGold := 0
	if len(status.Data.Inventory.StashedGold) > 0 {
		personalStashGold = status.Data.Inventory.StashedGold[0] // Tab 1 (personal) = índice 0
	}

	return inventoryGold + personalStashGold
}

// CanAffordMercRevive verifica se tem dinheiro suficiente para reviver o mercenário
func CanAffordMercRevive(status *botCtx.Status) bool {
	// Custo de reviver varia com nível, mas o máximo é 50k
	const estimatedReviveCost = 50000

	availableGold := GetAvailableGold(status)
	return availableGold >= estimatedReviveCost
}

func ReviveMerc() error {
	status := botCtx.Get()

	status.SetLastAction("ReviveMerc")

	if !status.CharacterCfg.Character.UseMerc || status.Data.MercHPPercent() > 0 {
		return nil
	}

	// Verificar dinheiro antes de tentar reviver
	if !CanAffordMercRevive(status) {
		availableGold := GetAvailableGold(status)
		status.Context.MercReviveFailedNoGold = true
		return fmt.Errorf("insufficient gold to revive mercenary (available: %d, required: 50000)", availableGold)
	}

	status.Logger.Info("Merc is dead, let's revive it!")

	mercNPC := town.GetTownByArea(status.Data.PlayerUnit.Area).MercContractorNPC()

	if err := InteractNPC(mercNPC); err != nil {
		return fmt.Errorf("failed to interact with mercenary NPC: %w", err)
	}

	if mercNPC == npc.Tyrael2 {
		status.HID.KeySequence(win.VK_END, win.VK_UP, win.VK_RETURN, win.VK_ESCAPE)
	} else {
		status.HID.KeySequence(win.VK_HOME, win.VK_DOWN, win.VK_RETURN, win.VK_ESCAPE)
	}

	// Aguardar o jogo processar a ressurreição
	utils.PingSleep(utils.Medium, 1000)
	status.RefreshGameData()

	// Verificar se a ressurreição foi bem-sucedida
	if status.Data.MercHPPercent() > 0 {
		status.Logger.Info("Mercenary successfully revived")
		status.Context.MercReviveFailedNoGold = false // Resetar flag de falha
		return nil
	}

	// Se ainda está morto, verificar se foi por falta de dinheiro
	availableGold := GetAvailableGold(status)
	if availableGold < 50000 {
		status.Context.MercReviveFailedNoGold = true
		return fmt.Errorf("failed to revive mercenary - insufficient gold (available: %d, required: 50000)", availableGold)
	}

	// Outro motivo de falha
	status.Logger.Warn("Failed to revive mercenary - unknown reason")
	return errors.New("failed to revive mercenary - mercenary still dead after revive attempt")
}

// NeedsTPsToContinue now correctly accepts *botCtx.Context and checks for at least 1 TP
func NeedsTPsToContinue(ctx *botCtx.Context) bool {
	portalTome, found := ctx.Data.Inventory.Find(item.TomeOfTownPortal, item.LocationInventory)
	if !found {
		return false // No portal tome found, so no TPs, can't go to town.
	}

	qty, found := portalTome.FindStat(stat.Quantity, 0)
	// If quantity stat isn't found, or if quantity is exactly 0, then we can't make a TP.
	return qty.Value > 0 && found
}