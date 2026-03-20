package ssh

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	RegisterCommand("arena", cmdArena)
}

// ArenaScenario represents a scenario definition loaded from templates/arena/.
type ArenaScenario struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	DurationMinutes int    `json:"duration_minutes"`
	MaxAgents       int    `json:"max_agents"`
	Ports           []int  `json:"ports,omitempty"`
}

// cmdArena is the handler for the "arena" command set.
func cmdArena(s *Shell, args []string) error {
	if len(args) == 0 {
		return cmdArenaHelp(s)
	}

	switch args[0] {
	case "create-match":
		return cmdArenaCreateMatch(s, args[1:])
	case "join":
		return cmdArenaJoin(s, args[1:])
	case "spectate":
		return cmdArenaSpectate(s, args[1:])
	case "leaderboard":
		return cmdArenaLeaderboard(s, args[1:])
	case "list":
		return cmdArenaList(s, args[1:])
	case "history":
		return cmdArenaHistory(s, args[1:])
	case "help":
		return cmdArenaHelp(s)
	default:
		return fmt.Errorf("unknown arena subcommand %q. try: arena help", args[0])
	}
}

func cmdArenaHelp(s *Shell) error {
	s.writeln("")
	s.writeln("  \033[1marena\033[0m -- CTF & agent competition")
	s.writeln("")
	s.writeln("    arena create-match --agents=<n> --scenario=<name>")
	s.writeln("                       create a new match")
	s.writeln("    arena join <match-id>")
	s.writeln("                       join an existing match")
	s.writeln("    arena spectate <match-id>")
	s.writeln("                       watch a match (read-only)")
	s.writeln("    arena leaderboard  show ELO rankings")
	s.writeln("    arena list         list active matches")
	s.writeln("    arena history      show your past match results")
	s.writeln("")
	s.writeln("  \033[33mscenarios:\033[0m web-exploit, code-review")
	s.writeln("")
	return nil
}

// generateMatchID creates a short, unique match ID.
func generateMatchID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "m-" + hex.EncodeToString(b), nil
}

// loadArenaScenario reads a scenario.json from templates/arena/<name>/.
func loadArenaScenario(name string) (*ArenaScenario, error) {
	// Look for scenario in known locations
	searchPaths := []string{
		filepath.Join("templates", "arena", name, "scenario.json"),
		filepath.Join("/etc/ussycode/templates/arena", name, "scenario.json"),
	}

	for _, path := range searchPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var sc ArenaScenario
		if err := json.Unmarshal(data, &sc); err != nil {
			return nil, fmt.Errorf("parse scenario %s: %w", name, err)
		}
		return &sc, nil
	}

	return nil, fmt.Errorf("scenario %q not found", name)
}

// listAvailableScenarios returns names of available arena scenarios.
func listAvailableScenarios() []string {
	var scenarios []string
	searchDirs := []string{
		filepath.Join("templates", "arena"),
		filepath.Join("/etc/ussycode/templates/arena"),
	}

	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				// Check it has a scenario.json
				scenPath := filepath.Join(dir, e.Name(), "scenario.json")
				if _, err := os.Stat(scenPath); err == nil {
					scenarios = append(scenarios, e.Name())
				}
			}
		}
		if len(scenarios) > 0 {
			return scenarios
		}
	}
	return scenarios
}

// ── create-match ──────────────────────────────────────────────────────

func cmdArenaCreateMatch(s *Shell, args []string) error {
	ctx := context.Background()

	// Parse flags
	agents := 2
	scenario := ""

	for _, a := range args {
		if strings.HasPrefix(a, "--agents=") {
			val := strings.TrimPrefix(a, "--agents=")
			n := 0
			if _, err := fmt.Sscanf(val, "%d", &n); err != nil || n < 2 || n > 10 {
				return fmt.Errorf("--agents must be between 2 and 10")
			}
			agents = n
		} else if strings.HasPrefix(a, "--scenario=") {
			scenario = strings.TrimPrefix(a, "--scenario=")
		}
	}

	if scenario == "" {
		return fmt.Errorf("usage: arena create-match --scenario=<name> [--agents=<n>]\n  available scenarios: %s",
			strings.Join(listAvailableScenarios(), ", "))
	}

	// Validate the scenario exists
	sc, err := loadArenaScenario(scenario)
	if err != nil {
		return fmt.Errorf("load scenario: %w", err)
	}

	// Respect scenario's max agents if lower than requested
	if sc.MaxAgents > 0 && agents > sc.MaxAgents {
		agents = sc.MaxAgents
	}

	matchID, err := generateMatchID()
	if err != nil {
		return fmt.Errorf("generate match ID: %w", err)
	}

	slog.Info("arena: creating match",
		"match_id", matchID,
		"scenario", scenario,
		"max_agents", agents,
		"created_by", s.user.Handle)

	match, err := s.gw.DB.CreateArenaMatch(ctx, matchID, scenario, agents, s.user.ID)
	if err != nil {
		return fmt.Errorf("create match: %w", err)
	}

	// Auto-join the creator as the first participant
	if _, err := s.gw.DB.JoinArenaMatch(ctx, matchID, s.user.ID); err != nil {
		return fmt.Errorf("auto-join match: %w", err)
	}

	s.writeln("")
	s.writef("  \033[32mmatch created!\033[0m\n")
	s.writeln("")
	s.writef("  match id:  %s\n", match.MatchID)
	s.writef("  scenario:  %s (%s)\n", sc.Name, sc.Description)
	s.writef("  agents:    1/%d (waiting for opponents)\n", agents)
	s.writef("  duration:  %d min\n", sc.DurationMinutes)
	s.writeln("")
	s.writef("  share this to invite others:\n")
	s.writef("    arena join %s\n", match.MatchID)
	s.writeln("")

	return nil
}

// ── join ──────────────────────────────────────────────────────────────

func cmdArenaJoin(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: arena join <match-id>")
	}

	ctx := context.Background()
	matchID := args[0]

	// Look up the match
	match, err := s.gw.DB.GetArenaMatch(ctx, matchID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("match %q not found", matchID)
		}
		return fmt.Errorf("lookup match: %w", err)
	}

	if match.Status != "waiting" {
		return fmt.Errorf("match %s is %s (can only join 'waiting' matches)", matchID, match.Status)
	}

	// Check if already joined
	already, err := s.gw.DB.IsArenaParticipant(ctx, matchID, s.user.ID)
	if err != nil {
		return fmt.Errorf("check participation: %w", err)
	}
	if already {
		return fmt.Errorf("you already joined match %s", matchID)
	}

	// Check capacity
	count, err := s.gw.DB.ArenaParticipantCount(ctx, matchID)
	if err != nil {
		return fmt.Errorf("count participants: %w", err)
	}
	if count >= match.MaxAgents {
		return fmt.Errorf("match %s is full (%d/%d agents)", matchID, count, match.MaxAgents)
	}

	// Join the match
	if _, err := s.gw.DB.JoinArenaMatch(ctx, matchID, s.user.ID); err != nil {
		return fmt.Errorf("join match: %w", err)
	}

	slog.Info("arena: user joined match",
		"match_id", matchID,
		"user", s.user.Handle)

	count++ // include the new participant
	s.writeln("")
	s.writef("  \033[32mjoined match %s!\033[0m\n", matchID)
	s.writef("  scenario:  %s\n", match.Scenario)
	s.writef("  agents:    %d/%d\n", count, match.MaxAgents)

	if count >= match.MaxAgents {
		s.writeln("  \033[33mmatch is full -- starting soon!\033[0m")
		// Auto-start when full
		if err := s.gw.DB.UpdateArenaMatchStatus(ctx, matchID, "running"); err != nil {
			slog.Error("arena: failed to auto-start match", "match_id", matchID, "error", err)
		} else {
			slog.Info("arena: match auto-started (full)", "match_id", matchID)
		}
	} else {
		s.writef("  waiting for %d more agent%s...\n", match.MaxAgents-count, plural(match.MaxAgents-count))
	}

	s.writeln("")
	return nil
}

// ── spectate ──────────────────────────────────────────────────────────

func cmdArenaSpectate(s *Shell, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: arena spectate <match-id>")
	}

	ctx := context.Background()
	matchID := args[0]

	// Look up the match
	match, err := s.gw.DB.GetArenaMatch(ctx, matchID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("match %q not found", matchID)
		}
		return fmt.Errorf("lookup match: %w", err)
	}

	participants, err := s.gw.DB.ListArenaParticipants(ctx, matchID)
	if err != nil {
		return fmt.Errorf("list participants: %w", err)
	}

	s.writeln("")
	s.writef("  \033[1m=== SPECTATING: %s ===\033[0m\n", matchID)
	s.writeln("")
	s.writef("  scenario: %s\n", match.Scenario)
	s.writef("  status:   %s\n", colorMatchStatus(match.Status))
	s.writeln("")

	if len(participants) > 0 {
		s.writef("  %-16s %-10s %6s\n", "AGENT", "STATUS", "SCORE")
		s.writef("  %-16s %-10s %6s\n", "─────", "──────", "─────")
		for _, p := range participants {
			handle := "unknown"
			if u, err := s.gw.DB.UserByID(ctx, p.UserID); err == nil {
				handle = u.Handle
			}
			s.writef("  %-16s %-10s %6d\n", handle, colorParticipantStatus(p.Status), p.Score)
		}
	} else {
		s.writeln("  no participants yet.")
	}

	s.writeln("")
	s.writeln("  (spectate mode is read-only)")
	s.writeln("")

	return nil
}

// ── leaderboard ──────────────────────────────────────────────────────

func cmdArenaLeaderboard(s *Shell, args []string) error {
	ctx := context.Background()
	jsonOut, _ := hasFlag(args, "--json")

	entries, err := s.gw.DB.GetArenaLeaderboard(ctx, 25)
	if err != nil {
		return fmt.Errorf("get leaderboard: %w", err)
	}

	if jsonOut {
		return s.writeJSON(entries)
	}

	s.writeln("")
	s.writeln("  \033[1m=== ARENA LEADERBOARD ===\033[0m")
	s.writeln("")

	if len(entries) == 0 {
		s.writeln("  no matches played yet. be the first!")
		s.writeln("  type 'arena create-match --scenario=web-exploit' to start.")
		s.writeln("")
		return nil
	}

	s.writef("  %-4s  %-16s  %6s  %4s  %4s  %4s\n",
		"RANK", "PLAYER", "ELO", "W", "L", "D")
	s.writef("  %-4s  %-16s  %6s  %4s  %4s  %4s\n",
		"────", "──────", "───", "─", "─", "─")

	for i, e := range entries {
		handle := "unknown"
		if u, err := s.gw.DB.UserByID(ctx, e.UserID); err == nil {
			handle = u.Handle
		}

		rankStr := fmt.Sprintf("#%d", i+1)
		if i == 0 {
			rankStr = "\033[33m#1\033[0m" // gold
		} else if i == 1 {
			rankStr = "\033[37m#2\033[0m" // silver
		} else if i == 2 {
			rankStr = "\033[31m#3\033[0m" // bronze
		}

		s.writef("  %-4s  %-16s  %6d  %4d  %4d  %4d\n",
			rankStr, handle, e.Rating, e.Wins, e.Losses, e.Draws)
	}

	s.writeln("")
	return nil
}

// ── list ──────────────────────────────────────────────────────────────

func cmdArenaList(s *Shell, args []string) error {
	ctx := context.Background()
	jsonOut, _ := hasFlag(args, "--json")

	matches, err := s.gw.DB.ListArenaMatches(ctx)
	if err != nil {
		return fmt.Errorf("list matches: %w", err)
	}

	if jsonOut {
		return s.writeJSON(matches)
	}

	s.writeln("")
	s.writeln("  \033[1m=== ACTIVE MATCHES ===\033[0m")
	s.writeln("")

	if len(matches) == 0 {
		s.writeln("  no active matches. create one:")
		s.writeln("    arena create-match --scenario=web-exploit")
		s.writeln("")
		return nil
	}

	s.writef("  %-14s  %-14s  %-10s  %7s  %s\n",
		"MATCH ID", "SCENARIO", "STATUS", "AGENTS", "CREATED")
	s.writef("  %-14s  %-14s  %-10s  %7s  %s\n",
		"────────", "────────", "──────", "──────", "───────")

	for _, m := range matches {
		count, _ := s.gw.DB.ArenaParticipantCount(ctx, m.MatchID)
		s.writef("  %-14s  %-14s  %-10s  %3d/%-3d  %s\n",
			m.MatchID, m.Scenario, colorMatchStatus(m.Status),
			count, m.MaxAgents, relativeTime(m.CreatedAt.Time))
	}

	s.writeln("")
	return nil
}

// ── history ──────────────────────────────────────────────────────────

func cmdArenaHistory(s *Shell, args []string) error {
	ctx := context.Background()
	jsonOut, _ := hasFlag(args, "--json")

	matches, err := s.gw.DB.ListArenaMatchHistory(ctx, s.user.ID)
	if err != nil {
		return fmt.Errorf("list history: %w", err)
	}

	if jsonOut {
		return s.writeJSON(matches)
	}

	s.writeln("")
	s.writeln("  \033[1m=== MATCH HISTORY ===\033[0m")
	s.writeln("")

	if len(matches) == 0 {
		s.writeln("  no match history yet.")
		s.writeln("")
		return nil
	}

	s.writef("  %-14s  %-14s  %-10s  %s\n",
		"MATCH ID", "SCENARIO", "RESULT", "PLAYED")
	s.writef("  %-14s  %-14s  %-10s  %s\n",
		"────────", "────────", "──────", "──────")

	for _, m := range matches {
		s.writef("  %-14s  %-14s  %-10s  %s\n",
			m.MatchID, m.Scenario, colorMatchStatus(m.Status),
			relativeTime(m.CreatedAt.Time))
	}

	s.writeln("")
	return nil
}

// ── ELO calculation ──────────────────────────────────────────────────

// ELO constants
const (
	eloStartRating = 1200
	eloKFactor     = 32
)

// CalculateELO computes new ELO ratings for two players after a match.
// result: 1.0 = player A wins, 0.0 = player B wins, 0.5 = draw.
// Returns (newRatingA, newRatingB).
func CalculateELO(ratingA, ratingB int, result float64) (int, int) {
	// Expected scores
	expectedA := 1.0 / (1.0 + math.Pow(10.0, float64(ratingB-ratingA)/400.0))
	expectedB := 1.0 / (1.0 + math.Pow(10.0, float64(ratingA-ratingB)/400.0))

	// New ratings
	newA := float64(ratingA) + eloKFactor*(result-expectedA)
	newB := float64(ratingB) + eloKFactor*((1.0-result)-expectedB)

	return int(math.Round(newA)), int(math.Round(newB))
}

// UpdateELOAfterMatch updates ELO ratings for all participants after a match.
// This supports 2-player matches by comparing the top scorer (winner) vs others.
func UpdateELOAfterMatch(ctx context.Context, s *Shell, matchID string) error {
	participants, err := s.gw.DB.ListArenaParticipants(ctx, matchID)
	if err != nil {
		return fmt.Errorf("list participants: %w", err)
	}

	if len(participants) < 2 {
		return nil // need at least 2 players
	}

	// Find winner (highest score) and loser(s)
	// For simplicity, treat it as pairwise: winner vs each loser
	// Participants are already sorted by score DESC from the query
	winner := participants[0]

	// Check for draw (all same score)
	allDraw := true
	for _, p := range participants[1:] {
		if p.Score != winner.Score {
			allDraw = false
			break
		}
	}

	if allDraw {
		// Everyone draws
		for _, p := range participants {
			elo, err := s.gw.DB.GetArenaELO(ctx, p.UserID)
			if err != nil {
				slog.Error("arena: failed to get ELO", "user_id", p.UserID, "error", err)
				continue
			}
			// In a multi-way draw, just bump draws counter -- no rating change
			if err := s.gw.DB.UpdateArenaELO(ctx, p.UserID, elo.Rating, elo.Wins, elo.Losses, elo.Draws+1); err != nil {
				slog.Error("arena: failed to update ELO", "user_id", p.UserID, "error", err)
			}
		}
		return nil
	}

	// Winner vs each loser
	winnerELO, err := s.gw.DB.GetArenaELO(ctx, winner.UserID)
	if err != nil {
		return fmt.Errorf("get winner ELO: %w", err)
	}

	currentWinnerRating := winnerELO.Rating
	for _, loser := range participants[1:] {
		if loser.Score == winner.Score {
			continue // skip ties (should have been caught above, but be safe)
		}

		loserELO, err := s.gw.DB.GetArenaELO(ctx, loser.UserID)
		if err != nil {
			slog.Error("arena: failed to get loser ELO", "user_id", loser.UserID, "error", err)
			continue
		}

		newWinnerRating, newLoserRating := CalculateELO(currentWinnerRating, loserELO.Rating, 1.0)

		if err := s.gw.DB.UpdateArenaELO(ctx, loser.UserID, newLoserRating, loserELO.Wins, loserELO.Losses+1, loserELO.Draws); err != nil {
			slog.Error("arena: failed to update loser ELO", "user_id", loser.UserID, "error", err)
		}

		currentWinnerRating = newWinnerRating
	}

	// Update winner's record
	if err := s.gw.DB.UpdateArenaELO(ctx, winner.UserID, currentWinnerRating, winnerELO.Wins+1, winnerELO.Losses, winnerELO.Draws); err != nil {
		return fmt.Errorf("update winner ELO: %w", err)
	}

	slog.Info("arena: ELO updated after match",
		"match_id", matchID,
		"winner", winner.UserID,
		"new_rating", currentWinnerRating)

	return nil
}

// ── helpers ──────────────────────────────────────────────────────────

func colorMatchStatus(status string) string {
	switch status {
	case "waiting":
		return "\033[33m" + status + "\033[0m"
	case "running":
		return "\033[32m" + status + "\033[0m"
	case "completed":
		return "\033[36m" + status + "\033[0m"
	case "cancelled":
		return "\033[31m" + status + "\033[0m"
	default:
		return status
	}
}

func colorParticipantStatus(status string) string {
	switch status {
	case "joined":
		return "\033[33m" + status + "\033[0m"
	case "ready":
		return "\033[36m" + status + "\033[0m"
	case "playing":
		return "\033[32m" + status + "\033[0m"
	case "finished":
		return "\033[35m" + status + "\033[0m"
	case "disconnected":
		return "\033[31m" + status + "\033[0m"
	default:
		return status
	}
}
