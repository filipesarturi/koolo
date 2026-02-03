# Análise Crítica: Bug de Perda de Itens Equipados/Inventory

## Resumo Executivo

Após análise profunda da codebase, identifiquei **VULNERABILIDADES CRÍTICAS** que permitem a perda de itens equipados e de inventário, violando o princípio fundamental de que esses itens nunca devem ser dropáveis.

---

## Causas Raiz Identificadas

### 1. **FALHA CRÍTICA: `dropItemFromInventoryUI` não verifica `LocationType`**

**Arquivo:** [`internal/action/leveling_tools.go`](internal/action/leveling_tools.go:132)

```go
func dropItemFromInventoryUI(i data.Item) error {
    // APENAS verifica tipos de item excluídos, NÃO verifica se está equipado!
    var excludedTypes = []string{"jave", "tkni", ...}
    if slices.Contains(excludedTypes, string(i.Desc().Type)) {
        return nil  // Só protege por tipo, não por localização
    }
    // ... dropa item sem verificar LocationType
}
```

**Problema:** A função assume que só recebe itens do inventário (`LocationInventory`), mas NÃO VERIFICA `i.Location.LocationType`. Se um item equipado (`LocationEquipped`) for passado, ele será dropado.

---

### 2. **FALHA CRÍTICA: `dropExcessItems` não protege itens equipados**

**Arquivo:** [`internal/action/stash.go`](internal/action/stash.go:544)

```go
func dropExcessItems() {
    for _, i := range ctx.Data.Inventory.ByLocation(item.LocationInventory) {
        // Só itera LocationInventory, mas...
    }
    for _, i := range itemsToDrop {
        DropItem(i)  // Se um item equipado entrar aqui...
    }
}
```

**Problema:** Embora itere apenas `LocationInventory`, se houver race condition ou stale data, itens equipados podem ser processados.

---

### 3. **FALHA CRÍTICA: `DropItem` não verifica `LocationEquipped`**

**Arquivo:** [`internal/action/stash.go`](internal/action/stash.go:578)

```go
func DropItem(i data.Item) {
    // Proteções existentes:
    if i.Name == "HoradricCube" { return }
    if i.Name == item.TomeOfTownPortal { return }
    if i.Name == "WirtsLeg" { return }

    // FALTA: Verificação de LocationType!
    // Um item equipado pode passar por todas as proteções acima
}
```

**Problema:** Não há verificação explícita para `LocationEquipped` ou `LocationMercenary`. Qualquer item que não seja Cube/Tome/Leg pode ser dropado.

---

### 4. **FALHA CRÍTICA: `DropInventoryItem` não verifica localização**

**Arquivo:** [`internal/action/item.go`](internal/action/item.go:57)

```go
func DropInventoryItem(i data.Item) error {
    if i.Name == "HoradricCube" { return nil }
    if i.Name == item.TomeOfTownPortal { return nil }
    if i.Name == "WirtsLeg" { return nil }

    if i.Location.LocationType == item.LocationInventory {
        // Só dropa se for LocationInventory
    }
}
```

**Problema:** Embora tenha verificação, ela vem DEPOIS das proteções de nome. E se `LocationType` for alterado entre a verificação e o drop?

---

### 5. **FALHA CRÍTICA: `IsDropProtected` não protege equipados**

**Arquivo:** [`internal/action/drop_helpers.go`](internal/action/drop_helpers.go:23)

```go
func IsDropProtected(i data.Item) bool {
    if i.Name == "HoradricCube" { return true }
    if i.Name == "WirtsLeg" && cowsRunActive { return true }
    if i.Name == item.Key && inLockedSlot { return true }
    // ... filtros de drop

    // FALTA: Proteção para LocationEquipped!
    return false
}
```

**Problema:** A função central de proteção de drop NÃO verifica se o item está equipado.

---

### 6. **FALHA: `leveling_tools.go` - Reset de Stats dropa equipados**

**Arquivo:** [`internal/action/leveling_tools.go`](internal/action/leveling_tools.go:454)

```go
// Unequip and immediately stash each remaining equipped item
for _, eqItem := range equippedItemsToProcess {
    slotCoords, found := getEquippedSlotCoords(eqItem.Location.BodyLocation, ctx.Data.LegacyGraphics)
    ctx.HID.ClickWithModifier(game.LeftButton, slotCoords.X, slotCoords.Y, game.CtrlKey)
    // ... se falhar, o item pode ficar no inventário e ser dropado depois
}

// Depois dropa TUDO do inventário:
for _, invItem := range inventoryItems {
    dropItemFromInventoryUI(invItem)  // Se um item equipado falhou em ser stashed...
}
```

**Problema:** Se o stash falhar (inventário cheio, lag), o item equipado fica no inventário e é dropado na fase 4.

---

### 7. **Race Condition: Sincronização de Estado**

**Problema:** O código faz múltiplas chamadas a `ctx.RefreshGameData()` entre ações, mas:

1. O estado do jogo pode mudar entre `RefreshGameData()` e a ação real
2. Em lag alto, o item pode mudar de localização sem que o bot perceba
3. Não há atomicidade nas operações de item

**Exemplo:**

```go
// Passo 1: Verifica localização
if i.Location.LocationType == item.LocationInventory {
    // Passo 2: Lag ocorre, item é equipado por outra ação
    // Passo 3: Drop é executado no item agora equipado
    DropItem(i)
}
```

---

### 8. **FALHA: `run/drop.go` - Drop em massa sem verificação**

**Arquivo:** [`internal/run/drop.go`](internal/run/drop.go:462)

```go
for _, it := range invItems {
    if action.IsInLockedInventorySlot(it) { continue }
    if action.IsDropProtected(it) { continue }  // Não protege equipados!

    ctx.HID.ClickWithModifier(game.LeftButton, screenPos.X, screenPos.Y, game.CtrlKey)
}
```

**Problema:** `IsDropProtected` não verifica `LocationEquipped`, então itens equipados podem ser dropados.

---

## Cenários de Exploração

### Cenário 1: Lag durante AutoEquip

1. Bot inicia `AutoEquip()`
2. Item é desequipado para inventário
3. Lag ocorre, estado não é atualizado
4. `dropExcessItems()` é chamado
5. Item recém-desequipado é dropado

### Cenário 2: Desconexão durante Stash

1. Bot move item do stash para inventário
2. Desconexão ocorre
3. Reconexão, estado é inconsistente
4. Bot pensa que item está no inventário
5. `DropItem()` é chamado em item que deveria estar equipado

### Cenário 3: Reset de Stats (Leveling)

1. `EnsureStatPoints()` é chamado
2. Itens equipados são desequipados para stash
3. Inventário cheio, alguns itens ficam no inventário
4. Fase de drop é executada
5. Itens que falharam em ir pro stash são dropados

---

## Matriz de Impacto

| Função                    | Severidade  | Facilidade de Exploração |
| ------------------------- | ----------- | ------------------------ |
| `dropItemFromInventoryUI` | **CRÍTICA** | Alta                     |
| `DropItem`                | **CRÍTICA** | Alta                     |
| `dropExcessItems`         | **ALTA**    | Média                    |
| `DropInventoryItem`       | **ALTA**    | Média                    |
| `IsDropProtected`         | **CRÍTICA** | Alta                     |
| Reset de Stats            | **ALTA**    | Média                    |

---

## Recomendações Imediatas

### 1. Proteção Centralizada

Criar uma função de verificação obrigatória:

```go
func CanSafelyDrop(i data.Item) bool {
    // NUNCA dropar itens equipados
    if i.Location.LocationType == item.LocationEquipped {
        return false
    }
    // NUNCA dropar itens do mercenário
    if i.Location.LocationType == item.LocationMercenary {
        return false
    }
    // NUNCA dropar itens no cursor
    if i.Location.LocationType == item.LocationCursor {
        return false
    }
    // Verificações adicionais...
    return true
}
```

### 2. Verificação em Todas as Funções de Drop

Todas as funções devem chamar `CanSafelyDrop()` antes de qualquer operação.

### 3. Atomicidade

Operações de item devem ser atômicas com verificação de estado antes e depois.

### 4. Logging Aprimorado

Logar o `LocationType` em todas as operações de drop para auditoria.

---

## Próximos Passos

1. Implementar `CanSafelyDrop()` em todas as funções de drop
2. Adicionar testes unitários para cenários de race condition
3. Implementar verificação de estado antes/depois de operações críticas
4. Adicionar métricas de monitoramento para drops suspeitos
