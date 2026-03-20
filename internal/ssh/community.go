package ssh

import (
	"context"
	"fmt"
	"log/slog"
)

func init() {
	RegisterCommand("community", cmdCommunity)
}

// cmdCommunity displays Ussyverse community information and the user's stats.
func cmdCommunity(s *Shell, args []string) error {
	ctx := context.Background()

	s.writeln("")
	s.writeln("  \033[35m╔══════════════════════════════════════════════════════╗\033[0m")
	s.writeln("  \033[35m║\033[0m                                                      \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m   \033[1m\033[35mwelcome to the ussyverse\033[0m                           \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m                                                      \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m   the ussyverse is one developer's ever-expanding     \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m   universe of open-source experiments, built in       \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m   public with an absurd naming convention and a       \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m   genuine obsession with making AI agents that        \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m   actually work.                                      \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m                                                      \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m   created by \033[1mKyle Durepos\033[0m & \033[1mshuv\033[0m                     \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m                                                      \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m   \033[36mhttps://ussy.host\033[0m                                  \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m   \033[36mhttps://discord.gg/6b2Ej3rS3q\033[0m                      \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m   \033[36mhttps://github.com/mojomast\033[0m                         \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m                                                      \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m   every project ships open source under MIT.          \033[35m║\033[0m")
	s.writeln("  \033[35m║\033[0m                                                      \033[35m║\033[0m")
	s.writeln("  \033[35m╚══════════════════════════════════════════════════════╝\033[0m")
	s.writeln("")

	// --- User stats ---
	s.writeln("  \033[1m=== YOUR STATS ===\033[0m")
	s.writeln("")

	// VMs
	running, err := s.gw.DB.RunningVMCountByUser(ctx, s.user.ID)
	if err != nil {
		slog.Warn("community: failed to get running VM count", "error", err)
	}
	total, err := s.gw.DB.VMCountByUser(ctx, s.user.ID)
	if err != nil {
		slog.Warn("community: failed to get total VM count", "error", err)
	}
	s.writef("  vms:           %d running / %d total\n", running, total)

	// Arena rating + rank
	elo, err := s.gw.DB.GetArenaELO(ctx, s.user.ID)
	if err != nil {
		// No arena record yet — show defaults
		s.writef("  arena rating:  1200 (unranked)\n")
		s.writef("  arena record:  0W / 0L / 0D\n")
	} else {
		rank, rankErr := s.gw.DB.GetArenaRank(ctx, s.user.ID)
		if rankErr != nil || rank == 0 {
			s.writef("  arena rating:  %d (unranked)\n", elo.Rating)
		} else {
			s.writef("  arena rating:  %d (rank #%d)\n", elo.Rating, rank)
		}
		s.writef("  arena record:  %dW / %dL / %dD\n", elo.Wins, elo.Losses, elo.Draws)
	}

	// Member since
	s.writef("  member since:  %s\n", s.user.CreatedAt.Format("Jan 2, 2006"))
	s.writef("  trust level:   %s\n", s.user.TrustLevel)
	s.writeln("")

	// --- Ussyverse projects ---
	s.writeln("  \033[1m=== USSYVERSE PROJECTS ===\033[0m")
	s.writeln("")
	s.writeln("  \033[33mussycode\033[0m        self-hosted dev environments (you are here)")
	s.writeln("  \033[33mgeoffrussy\033[0m      AI dev orchestrator")
	s.writeln("  \033[33mbattlebussy\033[0m     autonomous CTF & agent competition")
	s.writeln("  \033[33mopenclawssy\033[0m     security-first agent runtime")
	s.writeln("  \033[33mragussy\033[0m         self-hosted RAG chatbot")
	s.writeln("  \033[33mswarmussy\033[0m       multi-agent orchestration")
	s.writeln("")
	s.writeln("  try: \033[36mnew --template=geoffrussy\033[0m to spin up an agent environment")
	s.writeln("")

	// Footer
	s.writeln(fmt.Sprintf("  \033[90mbuilt for the ussyverse. MIT licensed. ship it.\033[0m"))
	s.writeln("")

	return nil
}
