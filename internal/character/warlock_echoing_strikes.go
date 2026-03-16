package character

import (
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/npc"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/d2go/pkg/data/state"
	"github.com/hectorgimenez/koolo/internal/action"
	"github.com/hectorgimenez/koolo/internal/action/step"
	"github.com/hectorgimenez/koolo/internal/context"
	"github.com/hectorgimenez/koolo/internal/game"
)

const (
	echoingStrikesMaxAttacksLoop = 15
	echoingStrikesMinDistance    = 5
	echoingStrikesMaxDistance    = 12
	echoingStrikesDangerDist    = 4
	echoingStrikesSafeDist      = 6
)

type WarlockEchoingStrikes struct {
	BaseCharacter
}

func (s WarlockEchoingStrikes) ShouldIgnoreMonster(m data.Monster) bool {
	if m.IsPet() || m.IsMerc() || m.IsGoodNPC() || m.IsSkip() {
		return true
	}

	// Warlock summons (Bind Demon) are not in d2go's IsPet() list
	switch m.Name {
	case npc.WarGoatman, npc.Tainted3, npc.WarDefiler:
		return true
	}
	if m.States.HasState(state.BindDemonUnderling) {
		return true
	}

	return false
}

func (s WarlockEchoingStrikes) CheckKeyBindings() []skill.ID {
	requireKeybindings := []skill.ID{skill.EchoingStrike, skill.SigilLethargy, skill.BladeWarp, skill.BindDemon}
	missingKeybindings := []skill.ID{}

	for _, cskill := range requireKeybindings {
		if _, found := s.Data.KeyBindings.KeyBindingForSkill(cskill); !found {
			missingKeybindings = append(missingKeybindings, cskill)
		}
	}

	if len(missingKeybindings) > 0 {
		s.Logger.Debug("There are missing required key bindings.", slog.Any("Bindings", missingKeybindings))
	}

	return missingKeybindings
}

func (s WarlockEchoingStrikes) BuffSkills() []skill.ID {
	skillsList := make([]skill.ID, 0)

	// Psychic Ward buff
	if _, found := s.Data.KeyBindings.KeyBindingForSkill(skill.PsychicWard); found {
		skillsList = append(skillsList, skill.PsychicWard)
	}

	return skillsList
}

func (s WarlockEchoingStrikes) PreCTABuffSkills() []skill.ID {
	// Psychic Ward should be cast before CTA buffs so BO/BC benefit from it
	skillsList := make([]skill.ID, 0)

	if _, found := s.Data.KeyBindings.KeyBindingForSkill(skill.PsychicWard); found {
		skillsList = append(skillsList, skill.PsychicWard)
	}

	return skillsList
}

func (s WarlockEchoingStrikes) KillMonsterSequence(
	monsterSelector func(d game.Data) (data.UnitID, bool),
	skipOnImmunities []stat.Resist,
) error {
	completedAttackLoops := 0
	previousUnitID := 0
	var lastReposition time.Time
	var lastLethargy time.Time

	for {
		context.Get().PauseIfNotPriority()

		if s.Context.Data.PlayerUnit.IsDead() {
			return nil
		}

		id, found := monsterSelector(*s.Data)
		if !found {
			return nil
		}
		if previousUnitID != int(id) {
			completedAttackLoops = 0
		}

		if !s.preBattleChecks(id, skipOnImmunities) {
			return nil
		}

		if completedAttackLoops >= echoingStrikesMaxAttacksLoop {
			return nil
		}

		monster, found := s.Data.Monsters.FindByID(id)
		if !found {
			s.Logger.Info("Monster not found", slog.String("monster", fmt.Sprintf("%v", monster)))
			return nil
		}

		mana, _ := s.Data.PlayerUnit.FindStat(stat.Mana, 0)

		// Reposition if enemies are too close
		if time.Since(lastReposition) > time.Second*4 {
			isAnyEnemyNearby, _ := action.IsAnyEnemyAroundPlayer(echoingStrikesDangerDist)
			if isAnyEnemyNearby {
				if safePos, found := action.FindSafePosition(monster, echoingStrikesDangerDist, echoingStrikesSafeDist, echoingStrikesMinDistance, echoingStrikesMaxDistance); found {
					step.MoveTo(safePos, step.WithIgnoreMonsters())
					lastReposition = time.Now()
				}
			}
		}

		// Cast Sigil: Lethargy on bosses/elites if not already debuffed
		if s.Data.PlayerUnit.Skills[skill.SigilLethargy].Level > 0 && mana.Value > 5 &&
			(monster.Type == data.MonsterTypeUnique || monster.Type == data.MonsterTypeSuperUnique) &&
			!monster.States.HasState(state.Sigillethargy) && time.Since(lastLethargy) > time.Second*4 {
			step.SecondaryAttack(skill.SigilLethargy, id, 1, step.Distance(echoingStrikesMinDistance, echoingStrikesMaxDistance))
			lastLethargy = time.Now()
		}

		// Main attack: Echoing Strike (ranged projectile, closer = wider spread)
		opts := []step.AttackOption{step.Distance(echoingStrikesMinDistance, echoingStrikesMaxDistance)}
		if s.Data.PlayerUnit.Skills[skill.EchoingStrike].Level > 0 && mana.Value > 5 {
			step.SecondaryAttack(skill.EchoingStrike, id, 4, opts...)
		} else {
			step.PrimaryAttack(id, 1, true, step.Distance(1, 3))
		}

		completedAttackLoops++
		previousUnitID = int(id)
		time.Sleep(time.Millisecond * 100)
	}
}

func (s WarlockEchoingStrikes) killMonster(npc npc.ID, t data.MonsterType) error {
	return s.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		m, found := d.Monsters.FindOne(npc, t)
		if !found {
			return 0, false
		}
		return m.UnitID, true
	}, nil)
}

func (s WarlockEchoingStrikes) killMonsterByName(id npc.ID, monsterType data.MonsterType, skipOnImmunities []stat.Resist) error {
	for {
		monster, found := s.Data.Monsters.FindOne(id, monsterType)
		if !found {
			return nil
		}

		if monster.Stats[stat.Life] <= 0 {
			return nil
		}

		err := s.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
			m, found := d.Monsters.FindOne(id, monsterType)
			if !found {
				return 0, false
			}
			return m.UnitID, true
		}, skipOnImmunities)

		if err != nil {
			s.Logger.Warn(fmt.Sprintf("Error during KillMonsterSequence for %v: %v", id, err))
		}

		time.Sleep(time.Millisecond * 250)
	}
}

func (s WarlockEchoingStrikes) killBoss(bossNPC npc.ID, timeout time.Duration) error {
	s.Logger.Info(fmt.Sprintf("Starting kill sequence for %v...", bossNPC))
	startTime := time.Now()
	var lastBossLethargy time.Time

	for {
		context.Get().PauseIfNotPriority()

		if time.Since(startTime) > timeout {
			s.Logger.Error(fmt.Sprintf("Timed out waiting for %v.", bossNPC))
			return fmt.Errorf("%v timeout", bossNPC)
		}

		if s.Context.Data.PlayerUnit.IsDead() {
			return nil
		}

		boss, found := s.Data.Monsters.FindOne(bossNPC, data.MonsterTypeUnique)
		if !found {
			time.Sleep(time.Second)
			continue
		}

		if boss.Stats[stat.Life] <= 0 {
			s.Logger.Info(fmt.Sprintf("%v has been defeated.", bossNPC))
			if bossNPC == npc.BaalCrab {
				time.Sleep(time.Second * 1)
			}
			return nil
		}

		mana, _ := s.Data.PlayerUnit.FindStat(stat.Mana, 0)

		// Cast Sigil: Lethargy on boss if not already debuffed
		if s.Data.PlayerUnit.Skills[skill.SigilLethargy].Level > 0 && mana.Value > 5 &&
			!boss.States.HasState(state.Sigillethargy) && time.Since(lastBossLethargy) > time.Second*4 {
			step.SecondaryAttack(skill.SigilLethargy, boss.UnitID, 1, step.Distance(10, 15))
			lastBossLethargy = time.Now()
		}

		// Main attack: Echoing Strike
		if s.Data.PlayerUnit.Skills[skill.EchoingStrike].Level > 0 && mana.Value > 5 {
			step.SecondaryAttack(skill.EchoingStrike, boss.UnitID, 4, step.Distance(echoingStrikesMinDistance, echoingStrikesMaxDistance))
		} else {
			step.PrimaryAttack(boss.UnitID, 1, true, step.Distance(1, 3))
		}

		time.Sleep(time.Millisecond * 100)
	}
}

func (s WarlockEchoingStrikes) KillCountess() error {
	return s.killMonsterByName(npc.DarkStalker, data.MonsterTypeSuperUnique, nil)
}

func (s WarlockEchoingStrikes) KillAndariel() error {
	return s.killBoss(npc.Andariel, time.Second*220)
}

func (s WarlockEchoingStrikes) KillSummoner() error {
	return s.killMonsterByName(npc.Summoner, data.MonsterTypeUnique, nil)
}

func (s WarlockEchoingStrikes) KillDuriel() error {
	return s.killBoss(npc.Duriel, time.Second*220)
}

func (s WarlockEchoingStrikes) KillCouncil() error {
	return s.KillMonsterSequence(func(d game.Data) (data.UnitID, bool) {
		var councilMembers []data.Monster
		for _, m := range d.Monsters {
			if m.Name == npc.CouncilMember || m.Name == npc.CouncilMember2 || m.Name == npc.CouncilMember3 {
				councilMembers = append(councilMembers, m)
			}
		}

		sort.Slice(councilMembers, func(i, j int) bool {
			distanceI := s.PathFinder.DistanceFromMe(councilMembers[i].Position)
			distanceJ := s.PathFinder.DistanceFromMe(councilMembers[j].Position)
			return distanceI < distanceJ
		})

		if len(councilMembers) > 0 {
			return councilMembers[0].UnitID, true
		}

		return 0, false
	}, nil)
}

func (s WarlockEchoingStrikes) KillMephisto() error {
	return s.killBoss(npc.Mephisto, time.Second*220)
}

func (s WarlockEchoingStrikes) KillIzual() error {
	return s.killBoss(npc.Izual, time.Second*220)
}

func (s WarlockEchoingStrikes) KillDiablo() error {
	return s.killBoss(npc.Diablo, time.Second*220)
}

func (s WarlockEchoingStrikes) KillPindle() error {
	return s.killMonsterByName(npc.DefiledWarrior, data.MonsterTypeSuperUnique, nil)
}

func (s WarlockEchoingStrikes) KillNihlathak() error {
	return s.killMonsterByName(npc.Nihlathak, data.MonsterTypeSuperUnique, nil)
}

func (s WarlockEchoingStrikes) KillBaal() error {
	return s.killBoss(npc.BaalCrab, time.Second*240)
}
