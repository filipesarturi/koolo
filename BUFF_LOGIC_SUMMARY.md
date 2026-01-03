# Resumo da Lógica de Buff

## Visão Geral
O sistema de buff gerencia a aplicação de buffs do personagem, incluindo suporte especial para usar o Memory staff na primeira run e rebuffing normal com CTA.

## Fluxo Principal

### 1. `BuffIfRequired()` - Ponto de Entrada
Esta função decide se buffs são necessários e qual método usar.

#### 1.1. Verificação de Cidade (Town)
- **Se estiver na cidade:**
  - Verifica se `UseMemoryBuff` está habilitado
  - **Se Memory habilitado:**
    - Verifica se Memory buff já foi aplicado (flag `memoryBuffApplied`)
    - **Se não foi aplicado:**
      - Verifica se Energy Shield ou Armor estão faltando
      - Se faltarem, chama `buffWithMemory()` (apenas primeira run)
      - Marca `memoryBuffApplied[characterName] = true`
    - **Retorna** (não continua com buff normal)
  - **Se Memory desabilitado:**
    - Continua com fluxo normal de buff (permite buff na cidade)

#### 1.2. Verificação de Rebuff Necessário
- Chama `IsRebuffRequired()`:
  - **Cooldown:** Não buffa se feito nos últimos 30 segundos
  - **CTA:** Verifica se Battle Orders/Command estão faltando
  - **Buffs de classe:** Verifica se Energy Shield, Armor, Holy Shield, Cyclone Armor estão faltando
  - Retorna `true` se algum buff estiver faltando

#### 1.3. Posicionamento Seguro
- Se `MoveToSafePositionForBuff` habilitado:
  - Verifica se há monstros próximos (< 35 unidades)
  - Se houver, tenta encontrar posição segura
  - Move para posição segura antes de buffar
- Se desabilitado e 2+ monstros próximos:
  - Não buffa (comportamento antigo)

#### 1.4. Chamada para Buff Normal
- Chama `Buff()` para aplicar buffs normalmente

---

### 2. `Buff()` - Aplicação de Buffs Normais

#### 2.1. Verificações Iniciais
- **Cooldown:** Não buffa se feito nos últimos 30 segundos
- **Cidade:** Permite buff na cidade apenas se Memory estiver desabilitado
- **Loading Screen:** Aguarda carregamento se necessário

#### 2.2. Pre-CTA Buffs
- Aplica buffs retornados por `PreCTABuffSkills()`
- Aplica sem verificação de estado (skills sem estados verificáveis)

#### 2.3. CTA Buffs
- Chama `buffCTA(shouldSwapBack)`
- **Se `useSwapForBuffs` ativo:** Não troca de volta após CTA (fica no CTA para buffs de classe)
- **Se `useSwapForBuffs` inativo:** Troca de volta para arma principal após CTA

#### 2.4. Post-CTA Buffs (Buffs de Classe)

**Coleta de Buffs:**
1. **Refresh de dados:** Atualiza estado do jogo (importante após troca de arma)
2. **Para cada skill em `BuffSkills()`:**
   - **Verifica Energy Shield:** Se já está ativo (do Memory), **pula** (não adiciona à lista)
   - **Verifica se skill existe:** Checa se `skillData.Level > 0` (skill aprendida)
   - **Verifica keybinding:** Checa se tem keybinding configurado
   - **Para Armor skills:**
     - Se skill preferida não disponível → tenta fallback
     - Ordem de fallback: ChillingArmor > ShiverArmor > FrozenArmor
     - Verifica se fallback existe no personagem antes de usar

**Aplicação de Buffs:**
1. **Swap opcional para CTA:** Se `useSwapForBuffs` ativo e não está no CTA, troca
2. **Para cada buff na lista:**
   - **Verifica Energy Shield:** Se já está ativo, **pula** (dupla verificação)
   - **Verifica Armor:** Se qualquer armor está ativo, **pula** (não reaplica)
   - **Aplica buff:**
     - Se tem estado verificável → usa `castBuffWithVerify()` (com retry)
     - Se não tem estado → usa `castBuff()` (sem verificação)
   - **Se Armor falhar:**
     - Tenta fallback (primeira armor skill disponível)
     - Refresh de dados antes de tentar fallback
     - Verifica se fallback existe no personagem

3. **Swap de volta:** Se trocou para CTA, volta para arma principal

#### 2.5. Verificação Final
- Verifica se CTA buffs foram aplicados corretamente
- Atualiza `LastBuffAt` (sempre, mesmo se falhar)

---

### 3. `buffWithMemory()` - Buff com Memory Staff

#### 3.1. Verificação Inicial
- Verifica se Memory já está equipado (cenário de recovery)
- Se já equipado → chama `buffWithMemoryAlreadyEquipped()`

#### 3.2. Busca Memory no Stash
- Procura Memory em todas as tabs do stash (1-4)
- Se não encontrar → retorna erro

#### 3.3. Preparação
- Abre stash
- Troca para weapon slot 1 (tab 2) para ver arma atual
- **Salva arma original** (`originalLeftArm`, `originalRightArm`)
- Troca para tab do Memory

#### 3.4. Equipar Memory
- Equipa Memory usando `SHIFT + Left Click`
- **Retry até 3 vezes** se não equipar
- Verifica se Memory está equipado

#### 3.5. Aplicar Buffs
- Fecha stash
- Aplica Energy Shield (se disponível)
- Aplica Armor skill:
  - Usa skill preferida da config (se disponível)
  - Se não disponível, usa primeira disponível (ChillingArmor > ShiverArmor > FrozenArmor)
  - Verifica se skill existe no personagem antes de aplicar

#### 3.6. Restaurar Arma Original
- Abre stash novamente
- Procura arma original em todas as tabs
- Troca para tab da arma original
- Equipa arma original usando `SHIFT + Left Click` (automaticamente coloca Memory de volta no stash)
- **Verifica:**
  - Memory voltou para o stash
  - Arma original está equipada
- **Se falhar:** Tenta fallback `restoreWeaponFromCTA()` (equipa CTA do stash)

---

### 4. `buffCTA()` - Buffs do Call to Arms

#### 4.1. Verificações
- Se não tem CTA → retorna `true` (nada a fazer)
- Verifica cooldown de falhas de swap (evita loops infinitos)

#### 4.2. Swap para CTA
- Verifica se já está no CTA (tem Battle Orders)
- Se não está, troca para CTA
- Se swap falhar → registra falha e retorna `false`

#### 4.3. Aplicar Buffs CTA
- Aplica Battle Command
- Aplica Battle Orders
- Verifica se foram aplicados

#### 4.4. Swap de Volta
- Se `shouldSwapBack = true` → troca de volta para arma principal
- Se `shouldSwapBack = false` → deixa no CTA (para buffs de classe depois)

---

### 5. `IsRebuffRequired()` - Verificação de Necessidade de Rebuff

#### 5.1. Cooldown
- Não buffa se feito nos últimos 30 segundos
- Não buffa se estiver na cidade

#### 5.2. Verificação de Buffs
- **CTA:** Verifica se Battle Orders/Command estão faltando
- **Energy Shield:** Verifica se está faltando
- **Armor:** Verifica se qualquer armor está faltando (Frozen/Shiver/Chilling)
- **Holy Shield:** Verifica se está faltando
- **Cyclone Armor:** Verifica se está faltando

Retorna `true` se **qualquer** buff estiver faltando.

---

## Proteções e Fallbacks

### 1. Verificação de Estado Antes de Aplicar
- **Energy Shield:** Verifica `state.Energyshield` antes de aplicar
- **Armor:** Verifica `state.Frozenarmor/Shiverarmor/Chillingarmor` antes de aplicar
- **Evita reaplicação:** Não aplica se já está ativo (especialmente importante para buffs do Memory)

### 2. Fallback de Armor Skills
- Se skill preferida não disponível → tenta primeira disponível
- Ordem: ChillingArmor > ShiverArmor > FrozenArmor
- Verifica se skill existe no personagem antes de usar

### 3. Verificação de Skill no Personagem
- Verifica `skillData.Level > 0` antes de tentar aplicar
- Evita tentar aplicar skills que o personagem não tem
- Importante após troca de arma (Memory → CTA)

### 4. Refresh de Dados
- Refresh antes de coletar buffs (após CTA)
- Refresh antes de tentar fallback
- Garante que dados refletem arma atual

### 5. Recovery de Memory
- Se Memory já está equipado ao iniciar → aplica buffs e restaura arma
- Se arma original não encontrada → usa CTA como fallback

### 6. Proteção Contra Loops
- Cooldown de 30 segundos entre buffs
- Tracking de falhas de swap de arma
- Cooldown de 60 segundos após 3 falhas de swap

---

## Mapeamento de Skills para Estados

O sistema usa `skillToState` para verificar se buffs foram aplicados:
- `EnergyShield` → `state.Energyshield`
- `FrozenArmor` → `state.Frozenarmor`
- `ShiverArmor` → `state.Shiverarmor`
- `ChillingArmor` → `state.Chillingarmor`
- `BattleOrders` → `state.Battleorders`
- `BattleCommand` → `state.Battlecommand`
- E outros...

---

## Configurações

### `UseMemoryBuff`
- Se `true`: Usa Memory staff na primeira run (apenas na cidade)
- Se `false`: Buffa normalmente na cidade também

### `PreferredArmorSkill`
- `"frozen"`: Prefere Frozen Armor
- `"shiver"`: Prefere Shiver Armor
- `"chilling"`: Prefere Chilling Armor
- `""` (vazio): Usa primeira disponível (auto)

### `UseSwapForBuffs`
- Se `true`: Usa CTA para buffs de classe (não troca de volta)
- Se `false`: Troca de volta para arma principal após CTA

---

## Fluxo de Decisão Resumido

```
BuffIfRequired()
├─ Na cidade?
│  ├─ Memory habilitado?
│  │  ├─ Memory já aplicado? → Retorna
│  │  └─ Precisa de buffs? → buffWithMemory() → Retorna
│  └─ Memory desabilitado? → Continua
├─ IsRebuffRequired()?
│  └─ Não → Retorna
├─ Monstros próximos?
│  ├─ MoveToSafePosition habilitado? → Move para posição segura
│  └─ 2+ monstros e feature desabilitado? → Retorna
└─ Buff() → Aplica buffs normalmente
```

---

## Pontos Importantes

1. **Memory só na primeira run:** Flag `memoryBuffApplied` garante que Memory só é usado uma vez
2. **Verificação de estado:** Sempre verifica se buff já está ativo antes de aplicar
3. **Fallback inteligente:** Se skill preferida não disponível, usa alternativa
4. **Refresh de dados:** Sempre atualiza dados após troca de arma
5. **Proteção contra loops:** Cooldowns e tracking de falhas
6. **Recovery:** Se Memory ficar equipado, sistema tenta restaurar arma original ou CTA
