# Regras de Qualidade de Código

## Princípio Fundamental

**Sempre evitar duplicações de código e procurar reutilizar funcionalidades existentes.**

## DRY (Don't Repeat Yourself)

### Extrair Lógica Comum

✅ **Identificar padrões repetidos e extrair para funções auxiliares**:

```go
// Antes: código duplicado
func PickupItemMouse(it data.Item) error {
    // ... validações ...
    if !ctx.PathFinder.LineOfSight(ctx.Data.PlayerUnit.Position, it.Position) {
        return ErrNoLOSToItem
    }
    // ...
}

func PickupItemPacket(it data.Item) error {
    // ... validações duplicadas ...
    if !ctx.PathFinder.LineOfSight(ctx.Data.PlayerUnit.Position, it.Position) {
        return ErrNoLOSToItem
    }
    // ...
}

// Depois: lógica comum extraída
func validateItemPickup(it data.Item) error {
    if !ctx.PathFinder.LineOfSight(ctx.Data.PlayerUnit.Position, it.Position) {
        return ErrNoLOSToItem
    }
    // outras validações comuns
    return nil
}
```

### Reutilizar Funções Existentes

✅ **Sempre verificar se funcionalidade similar já existe antes de criar nova**:

```go
// Antes de criar nova função, procurar por:
// - Funções similares no mesmo pacote
// - Funções em pacotes relacionados
// - Helpers em pacotes utils
```

✅ **Usar funções auxiliares existentes**:

```go
// Em vez de duplicar lógica de validação
distance := ctx.PathFinder.DistanceFromMe(it.Position)

// Reutilizar função existente
if hasHostileMonstersNearby(it.Position) {
    return ErrMonsterAroundItem
}
```

## Padrões de Duplicação a Evitar

### Validações Repetidas

❌ **Evitar validar as mesmas condições em múltiplos lugares**:

```go
// ERRADO - validação duplicada
func FunctionA() error {
    if !ctx.PathFinder.LineOfSight(...) {
        return ErrNoLOS
    }
    // ...
}

func FunctionB() error {
    if !ctx.PathFinder.LineOfSight(...) {
        return ErrNoLOS
    }
    // ...
}
```

✅ **Extrair validações comuns**:

```go
func validateLineOfSight(pos data.Position) error {
    if !ctx.PathFinder.LineOfSight(ctx.Data.PlayerUnit.Position, pos) {
        return ErrNoLOS
    }
    return nil
}
```

### Lógica de Espera/Timeout

❌ **Evitar duplicar lógica de espera**:

```go
// ERRADO - padrão de espera duplicado
waitingStartTime := time.Now()
for ctx.Data.PlayerUnit.Mode == mode.CastingSkill {
    if time.Since(waitingStartTime) > 2*time.Second {
        break
    }
    time.Sleep(25 * time.Millisecond)
    ctx.RefreshGameData()
}
```

✅ **Criar função auxiliar reutilizável**:

```go
func waitForCharacterIdle(ctx *context.Status, timeout time.Duration) error {
    waitingStartTime := time.Now()
    for ctx.Data.PlayerUnit.Mode == mode.CastingSkill || 
        ctx.Data.PlayerUnit.Mode == mode.Running {
        if time.Since(waitingStartTime) > timeout {
            return ErrTimeout
        }
        time.Sleep(25 * time.Millisecond)
        ctx.RefreshGameData()
    }
    return nil
}
```

### Verificações de Configuração

✅ **Consolidar verificações similares**:

```go
// Em vez de verificar múltiplas vezes
if ctx.CharacterCfg.PacketCasting.UseForItemPickup {
    // ...
}
if ctx.CharacterCfg.PacketCasting.UseForTpInteraction {
    // ...
}

// Considerar helper se o padrão se repetir muito
func canUsePacketFor(operation string) bool {
    switch operation {
    case "itemPickup":
        return ctx.CharacterCfg.PacketCasting.UseForItemPickup
    case "tpInteraction":
        return ctx.CharacterCfg.PacketCasting.UseForTpInteraction
    // ...
    }
    return false
}
```

## Refatoração de Código Duplicado

### Identificar Padrões

✅ **Procurar por**:
- Código similar em múltiplos arquivos
- Funções com lógica quase idêntica
- Validações repetidas
- Padrões de tratamento de erro similares

### Estratégias de Consolidação

1. **Extrair para função auxiliar**:
   - Quando a lógica é idêntica ou muito similar
   - Colocar em pacote apropriado (utils, helpers)

2. **Criar tipo/struct comum**:
   - Quando há dados relacionados que são passados juntos
   - Agrupar em struct para reduzir parâmetros

3. **Usar interfaces**:
   - Quando há múltiplas implementações do mesmo comportamento
   - Permitir polimorfismo e reduzir duplicação

## Verificação Antes de Criar Novo Código

### Checklist

Antes de criar nova funcionalidade, sempre verificar:

1. ✅ Existe função similar no mesmo pacote?
2. ✅ Existe função similar em pacotes relacionados?
3. ✅ Posso estender função existente ao invés de criar nova?
4. ✅ A lógica pode ser extraída para função auxiliar reutilizável?
5. ✅ Há padrão comum que pode ser abstraído?

### Busca no Código

✅ **Sempre fazer busca antes de duplicar**:

```bash
# Buscar por funções similares
grep -r "função similar" internal/
# Buscar por padrões de código
grep -r "padrão comum" internal/
```

## Exemplos do Projeto

### Padrão de Pickup

O projeto já tem um bom exemplo de evitar duplicação em `internal/action/step/pickup_item.go`:

```go
func PickupItem(it data.Item, itemPickupAttempt int) error {
    // Lógica comum de decisão
    if canUseTelekinesisForItem(it) {
        return PickupItemTelekinesis(it, itemPickupAttempt)
    }
    if ctx.CharacterCfg.PacketCasting.UseForItemPickup {
        return PickupItemPacket(it, itemPickupAttempt)
    }
    return PickupItemMouse(it, itemPickupAttempt)
}
```

✅ **Bom padrão**: Uma função de entrada que decide qual implementação usar, evitando duplicar a lógica de decisão.

## Manutenibilidade

### Código Limpo

✅ **Manter funções pequenas e focadas**:
- Uma função, uma responsabilidade
- Fácil de testar
- Fácil de reutilizar

✅ **Nomes descritivos**:
- Funções devem descrever claramente o que fazem
- Variáveis devem ter nomes significativos

### Documentação

✅ **Documentar funções auxiliares reutilizáveis**:

```go
// validateItemPickup performs common validations for item pickup operations.
// It checks line of sight, distance, and monster proximity.
func validateItemPickup(it data.Item) error {
    // ...
}
```

## Ferramentas

- Use `golangci-lint` para detectar duplicações
- Use `go vet` para análise estática
- Revise código regularmente para identificar padrões repetidos
