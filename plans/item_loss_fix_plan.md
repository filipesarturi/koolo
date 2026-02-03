# Plano de Correção: Bug de Perda de Itens

## Visão Geral

Este plano detalha as correções necessárias para prevenir a perda de itens equipados e de inventário.

---

## Fase 1: Implementar Proteção Centralizada

### 1.1 Criar `CanSafelyDrop()` em `drop_helpers.go`

**Arquivo:** `internal/action/drop_helpers.go`

```go
// CanSafelyDrop verifica se um item pode ser dropado com segurança.
// Retorna false para itens equipados, de mercenário, ou em transição.
// ESTA FUNÇÃO DEVE SER CHAMADA EM TODA OPERAÇÃO DE DROP.
func CanSafelyDrop(i data.Item) bool {
    ctx := context.Get()

    // CRÍTICO: NUNCA dropar itens equipados
    if i.Location.LocationType == item.LocationEquipped {
        ctx.Logger.Error("BLOCKED: Attempted to drop equipped item",
            slog.String("item", string(i.Name)),
            slog.Int("unitID", int(i.UnitID)),
            slog.String("bodyLoc", string(i.Location.BodyLocation)))
        return false
    }

    // CRÍTICO: NUNCA dropar itens do mercenário
    if i.Location.LocationType == item.LocationMercenary {
        ctx.Logger.Error("BLOCKED: Attempted to drop mercenary item",
            slog.String("item", string(i.Name)),
            slog.Int("unitID", int(i.UnitID)))
        return false
    }

    // CRÍTICO: NUNCA dropar itens no cursor (em transição)
    if i.Location.LocationType == item.LocationCursor {
        ctx.Logger.Warn("BLOCKED: Attempted to drop cursor item",
            slog.String("item", string(i.Name)),
            slog.Int("unitID", int(i.UnitID)))
        return false
    }

    // CRÍTICO: NUNCA dropar itens no chão
    if i.Location.LocationType == item.LocationGround {
        ctx.Logger.Warn("BLOCKED: Attempted to drop ground item",
            slog.String("item", string(i.Name)),
            slog.Int("unitID", int(i.UnitID)))
        return false
    }

    // Só permitir drop de itens explicitamente no inventário
    if i.Location.LocationType != item.LocationInventory {
        ctx.Logger.Error("BLOCKED: Attempted to drop item from non-inventory location",
            slog.String("item", string(i.Name)),
            slog.Int("unitID", int(i.UnitID)),
            slog.String("location", string(i.Location.LocationType)))
        return false
    }

    return true
}
```

---

## Fase 2: Atualizar Todas as Funções de Drop

### 2.1 Atualizar `DropItem()` em `stash.go`

**Arquivo:** `internal/action/stash.go:578`

**Mudança:** Adicionar verificação `CanSafelyDrop()` no início da função.

```go
func DropItem(i data.Item) {
    ctx := context.Get()
    ctx.SetLastAction("DropItem")

    // PROTEÇÃO CRÍTICA: Verificar se pode dropar
    if !CanSafelyDrop(i) {
        return
    }

    // Proteções existentes...
    if i.Name == "HoradricCube" {
        ctx.Logger.Debug(fmt.Sprintf("Skipping drop for protected item: %s", i.Name))
        return
    }
    // ... resto da função
}
```

### 2.2 Atualizar `DropInventoryItem()` em `item.go`

**Arquivo:** `internal/action/item.go:57`

**Mudança:** Mover `CanSafelyDrop()` para antes de qualquer operação.

```go
func DropInventoryItem(i data.Item) error {
    ctx := context.Get()
    ctx.SetLastAction("DropInventoryItem")

    // PROTEÇÃO CRÍTICA: Verificar se pode dropar PRIMEIRO
    if !CanSafelyDrop(i) {
        return fmt.Errorf("item cannot be safely dropped: %s (location: %s)",
            i.Name, i.Location.LocationType)
    }

    // Proteções existentes...
    if i.Name == "HoradricCube" {
        return nil
    }
    // ... resto da função
}
```

### 2.3 Atualizar `dropItemFromInventoryUI()` em `leveling_tools.go`

**Arquivo:** `internal/action/leveling_tools.go:132`

**Mudança:** Adicionar verificação obrigatória.

```go
func dropItemFromInventoryUI(i data.Item) error {
    ctx := context.Get()

    // PROTEÇÃO CRÍTICA: Verificar se pode dropar
    if !CanSafelyDrop(i) {
        return fmt.Errorf("item cannot be safely dropped: %s", i.Name)
    }

    // Verificações existentes...
    var excludedTypes = []string{...}
    // ... resto da função
}
```

### 2.4 Atualizar `dropExcessItems()` em `stash.go`

**Arquivo:** `internal/action/stash.go:544`

**Mudança:** Verificar cada item antes de adicionar à lista de drop.

```go
func dropExcessItems() {
    ctx := context.Get()
    ctx.SetLastAction("dropExcessItems")

    itemsToDrop := make([]data.Item, 0)
    for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
        if i.IsPotion() {
            continue
        }

        // PROTEÇÃO: Verificar se pode dropar
        if !CanSafelyDrop(i) {
            ctx.Logger.Warn("Skipping item that cannot be safely dropped",
                slog.String("item", string(i.Name)),
                slog.String("location", string(i.Location.LocationType)))
            continue
        }

        _, dropIt, _, _ := shouldStashIt(i, false)
        if dropIt {
            itemsToDrop = append(itemsToDrop, i)
        }
    }
    // ... resto da função
}
```

### 2.5 Atualizar `IsDropProtected()` em `drop_helpers.go`

**Arquivo:** `internal/action/drop_helpers.go:23`

**Mudança:** Adicionar `CanSafelyDrop()` como primeira verificação.

```go
func IsDropProtected(i data.Item) bool {
    // PROTEÇÃO CRÍTICA: Verificar segurança primeiro
    if !CanSafelyDrop(i) {
        return true  // Protegido = não pode dropar
    }

    ctx := context.Get()
    // ... resto da função
}
```

---

## Fase 3: Proteger Cenários de Leveling

### 3.1 Atualizar Reset de Stats em `leveling_tools.go`

**Arquivo:** `internal/action/leveling_tools.go:440`

**Mudança:** Verificar estado do item antes de cada operação.

```go
func EnsureStatPoints() error {
    // ... código existente ...

    // Fase 4: Dropar itens restantes
    ctx.Logger.Info("Dropping all remaining inventory items.")
    inventoryItems := ctx.Data.Inventory.ByLocation(item.LocationInventory)

    for _, invItem := range inventoryItems {
        // PROTEÇÃO: Verificar se ainda é seguro dropar
        ctx.RefreshGameData()
        currentItem, found := ctx.Data.Inventory.FindByID(invItem.UnitID)
        if !found {
            continue  // Item já foi movido
        }

        if !CanSafelyDrop(currentItem) {
            ctx.Logger.Warn("Skipping item that is no longer safe to drop",
                slog.String("item", string(currentItem.Name)))
            continue
        }

        if err := dropItemFromInventoryUI(currentItem); err != nil {
            ctx.Logger.Error(fmt.Sprintf("Failed to drop inventory item %s: %v", currentItem.Name, err))
        }
    }
}
```

---

## Fase 4: Proteger Drop em Massa

### 4.1 Atualizar `run/drop.go`

**Arquivo:** `internal/run/drop.go:462`

**Mudança:** Adicionar verificação de segurança.

```go
func (d Drop) dropInventoryDropperables(ctx *context.Status, tab int, quotas *DropQuotaTracker) (int, error) {
    invItems := ctx.Data.Inventory.ByLocation(item.LocationInventory)
    dropped := 0

    for _, it := range invItems {
        if action.IsInLockedInventorySlot(it) {
            continue
        }

        // PROTEÇÃO CRÍTICA
        if !action.CanSafelyDrop(it) {
            ctx.Logger.Error("Skipping unsafe drop",
                slog.String("item", string(it.Name)),
                slog.String("location", string(it.Location.LocationType)))
            continue
        }

        if action.IsDropProtected(it) {
            continue
        }

        // ... resto da operação
    }
}
```

---

## Fase 5: Implementar Verificação de Estado

### 5.1 Criar `VerifyItemState()`

```go
// VerifyItemState verifica se o item ainda está na localização esperada
// antes de executar uma operação crítica.
func VerifyItemState(unitID data.UnitID, expectedLocation item.LocationType) (data.Item, bool) {
    ctx := context.Get()
    ctx.RefreshGameData()

    item, found := ctx.Data.Inventory.FindByID(unitID)
    if !found {
        return data.Item{}, false
    }

    if item.Location.LocationType != expectedLocation {
        ctx.Logger.Warn("Item location mismatch",
            slog.String("item", string(item.Name)),
            slog.String("expected", string(expectedLocation)),
            slog.String("actual", string(item.Location.LocationType)))
        return item, false
    }

    return item, true
}
```

---

## Fase 6: Testes e Validação

### 6.1 Testes Unitários

```go
func TestCanSafelyDrop(t *testing.T) {
    tests := []struct {
        name     string
        item     data.Item
        expected bool
    }{
        {
            name: "equipped item should not be droppable",
            item: data.Item{
                Name: "Sword",
                Location: data.ItemLocation{
                    LocationType: item.LocationEquipped,
                    BodyLocation: item.LocRightArm,
                },
            },
            expected: false,
        },
        {
            name: "mercenary item should not be droppable",
            item: data.Item{
                Name: "Armor",
                Location: data.ItemLocation{
                    LocationType: item.LocationMercenary,
                },
            },
            expected: false,
        },
        {
            name: "inventory item should be droppable",
            item: data.Item{
                Name: "Ring",
                Location: data.ItemLocation{
                    LocationType: item.LocationInventory,
                },
            },
            expected: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := CanSafelyDrop(tt.item)
            if result != tt.expected {
                t.Errorf("CanSafelyDrop() = %v, want %v", result, tt.expected)
            }
        })
    }
}
```

### 6.2 Testes de Integração

- Simular lag durante operações de drop
- Simular desconexão durante stash
- Simular inventário cheio durante reset de stats

---

## Fase 7: Monitoramento e Alertas

### 7.1 Métricas

```go
var (
    dropBlockedEquipped = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "koolo_drop_blocked_equipped_total",
        Help: "Total number of blocked attempts to drop equipped items",
    })
    dropBlockedMercenary = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "koolo_drop_blocked_mercenary_total",
        Help: "Total number of blocked attempts to drop mercenary items",
    })
)
```

### 7.2 Alertas

- Alerta se mais de 10 tentativas de drop equipado forem bloqueadas em 1 hora
- Alerta se qualquer item equipado for dropado (indica falha na proteção)

---

## Checklist de Implementação

- [ ] 1.1 Implementar `CanSafelyDrop()`
- [ ] 2.1 Atualizar `DropItem()`
- [ ] 2.2 Atualizar `DropInventoryItem()`
- [ ] 2.3 Atualizar `dropItemFromInventoryUI()`
- [ ] 2.4 Atualizar `dropExcessItems()`
- [ ] 2.5 Atualizar `IsDropProtected()`
- [ ] 3.1 Atualizar reset de stats
- [ ] 4.1 Atualizar `run/drop.go`
- [ ] 5.1 Implementar `VerifyItemState()`
- [ ] 6.1 Escrever testes unitários
- [ ] 6.2 Executar testes de integração
- [ ] 7.1 Adicionar métricas
- [ ] 7.2 Configurar alertas

---

## Notas de Implementação

1. **Prioridade:** Implementar Fase 1 e 2 imediatamente (proteção crítica)
2. **Testes:** Executar testes completos antes de deploy
3. **Rollback:** Manter branch de hotfix pronta
4. **Comunicação:** Notificar usuários sobre a correção
