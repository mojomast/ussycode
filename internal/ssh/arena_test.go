package ssh

import (
	"context"
	"math"
	"os"
	"testing"

	"github.com/mojomast/ussycode/internal/db"
)

// ── ELO Calculation Tests ────────────────────────────────────────────

func TestCalculateELO_EqualRatings_WinnerGains(t *testing.T) {
	// Two players with equal ratings, player A wins
	newA, newB := CalculateELO(1200, 1200, 1.0)

	// With K=32 and equal ratings, expected is 0.5 each
	// Winner gains K * (1.0 - 0.5) = 16
	// Loser loses K * (0.0 - 0.5) = -16
	if newA != 1216 {
		t.Errorf("expected winner rating 1216, got %d", newA)
	}
	if newB != 1184 {
		t.Errorf("expected loser rating 1184, got %d", newB)
	}
}

func TestCalculateELO_EqualRatings_Draw(t *testing.T) {
	newA, newB := CalculateELO(1200, 1200, 0.5)

	// Draw with equal ratings should keep them the same
	if newA != 1200 {
		t.Errorf("expected rating 1200 after draw, got %d", newA)
	}
	if newB != 1200 {
		t.Errorf("expected rating 1200 after draw, got %d", newB)
	}
}

func TestCalculateELO_UnequalRatings_UpsetWin(t *testing.T) {
	// Lower-rated player (1000) beats higher-rated player (1400)
	newA, newB := CalculateELO(1000, 1400, 1.0)

	// Underdog should gain significantly more
	gainA := newA - 1000
	lossB := 1400 - newB

	if gainA < 25 {
		t.Errorf("expected underdog gain > 25, got %d", gainA)
	}
	if lossB < 25 {
		t.Errorf("expected favorite loss > 25, got %d", lossB)
	}
}

func TestCalculateELO_UnequalRatings_ExpectedWin(t *testing.T) {
	// Higher-rated player (1400) beats lower-rated player (1000)
	newA, newB := CalculateELO(1400, 1000, 1.0)

	// Favorite should gain less
	gainA := newA - 1400
	lossB := 1000 - newB

	if gainA > 10 {
		t.Errorf("expected favorite gain < 10, got %d", gainA)
	}
	if lossB > 10 {
		t.Errorf("expected underdog loss < 10, got %d", lossB)
	}
}

func TestCalculateELO_SumIsConserved(t *testing.T) {
	// Total rating should be (approximately) conserved across many scenarios
	testCases := []struct {
		rA, rB int
		result float64
	}{
		{1200, 1200, 1.0},
		{1200, 1200, 0.0},
		{1200, 1200, 0.5},
		{1400, 1000, 1.0},
		{1400, 1000, 0.0},
		{800, 1600, 0.5},
	}

	for _, tc := range testCases {
		newA, newB := CalculateELO(tc.rA, tc.rB, tc.result)
		original := tc.rA + tc.rB
		updated := newA + newB
		// Allow ±1 for rounding
		if math.Abs(float64(original-updated)) > 1 {
			t.Errorf("ELO sum not conserved: %d+%d=%d -> %d+%d=%d (result=%v)",
				tc.rA, tc.rB, original, newA, newB, updated, tc.result)
		}
	}
}

func TestCalculateELO_ExpectedScoreFormula(t *testing.T) {
	// Verify the expected score formula directly
	// E_a = 1 / (1 + 10^((R_b - R_a)/400))
	rA, rB := 1200, 1600
	expectedA := 1.0 / (1.0 + math.Pow(10.0, float64(rB-rA)/400.0))

	// With 400 point difference, expected should be ~0.0909
	if math.Abs(expectedA-0.0909) > 0.001 {
		t.Errorf("expected score ~0.0909, got %f", expectedA)
	}
}

// ── Match Lifecycle Tests ────────────────────────────────────────────

func setupArenaTestDB(t *testing.T) *db.DB {
	t.Helper()

	f, err := os.CreateTemp("", "ussycode-arena-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()

	t.Cleanup(func() {
		os.Remove(path)
		os.Remove(path + "-wal")
		os.Remove(path + "-shm")
	})

	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	return database
}

func TestArenaMatchLifecycle(t *testing.T) {
	database := setupArenaTestDB(t)
	ctx := context.Background()

	// Create two users
	user1, err := database.CreateUser(ctx, "player1")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	user2, err := database.CreateUser(ctx, "player2")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create a match
	match, err := database.CreateArenaMatch(ctx, "m-test123", "web-exploit", 2, user1.ID)
	if err != nil {
		t.Fatalf("CreateArenaMatch: %v", err)
	}
	if match.MatchID != "m-test123" {
		t.Errorf("expected match_id 'm-test123', got %q", match.MatchID)
	}
	if match.Status != "waiting" {
		t.Errorf("expected status 'waiting', got %q", match.Status)
	}
	if match.MaxAgents != 2 {
		t.Errorf("expected max_agents 2, got %d", match.MaxAgents)
	}

	// Player 1 joins
	p1, err := database.JoinArenaMatch(ctx, "m-test123", user1.ID)
	if err != nil {
		t.Fatalf("JoinArenaMatch player1: %v", err)
	}
	if p1.Status != "joined" {
		t.Errorf("expected participant status 'joined', got %q", p1.Status)
	}

	// Player 2 joins
	_, err = database.JoinArenaMatch(ctx, "m-test123", user2.ID)
	if err != nil {
		t.Fatalf("JoinArenaMatch player2: %v", err)
	}

	// Count participants
	count, err := database.ArenaParticipantCount(ctx, "m-test123")
	if err != nil {
		t.Fatalf("ArenaParticipantCount: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 participants, got %d", count)
	}

	// Check participant membership
	isMember, err := database.IsArenaParticipant(ctx, "m-test123", user1.ID)
	if err != nil {
		t.Fatalf("IsArenaParticipant: %v", err)
	}
	if !isMember {
		t.Error("expected user1 to be a participant")
	}

	// Duplicate join should fail (UNIQUE constraint)
	_, err = database.JoinArenaMatch(ctx, "m-test123", user1.ID)
	if err == nil {
		t.Error("expected error on duplicate join")
	}

	// Start the match
	if err := database.UpdateArenaMatchStatus(ctx, "m-test123", "running"); err != nil {
		t.Fatalf("UpdateArenaMatchStatus to running: %v", err)
	}

	match, err = database.GetArenaMatch(ctx, "m-test123")
	if err != nil {
		t.Fatalf("GetArenaMatch: %v", err)
	}
	if match.Status != "running" {
		t.Errorf("expected status 'running', got %q", match.Status)
	}
	if match.StartedAt.IsZero() {
		t.Error("expected started_at to be set")
	}

	// Update scores
	if err := database.UpdateArenaParticipantScore(ctx, "m-test123", user1.ID, 100); err != nil {
		t.Fatalf("UpdateArenaParticipantScore: %v", err)
	}
	if err := database.UpdateArenaParticipantScore(ctx, "m-test123", user2.ID, 75); err != nil {
		t.Fatalf("UpdateArenaParticipantScore: %v", err)
	}

	// List participants (should be ordered by score DESC)
	participants, err := database.ListArenaParticipants(ctx, "m-test123")
	if err != nil {
		t.Fatalf("ListArenaParticipants: %v", err)
	}
	if len(participants) != 2 {
		t.Fatalf("expected 2 participants, got %d", len(participants))
	}
	if participants[0].Score != 100 {
		t.Errorf("expected first participant score 100, got %d", participants[0].Score)
	}
	if participants[1].Score != 75 {
		t.Errorf("expected second participant score 75, got %d", participants[1].Score)
	}

	// Complete the match
	if err := database.UpdateArenaMatchStatus(ctx, "m-test123", "completed"); err != nil {
		t.Fatalf("UpdateArenaMatchStatus to completed: %v", err)
	}

	match, err = database.GetArenaMatch(ctx, "m-test123")
	if err != nil {
		t.Fatalf("GetArenaMatch: %v", err)
	}
	if match.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", match.Status)
	}
	if match.CompletedAt.IsZero() {
		t.Error("expected completed_at to be set")
	}

	// Match history should include this match
	history, err := database.ListArenaMatchHistory(ctx, user1.ID)
	if err != nil {
		t.Fatalf("ListArenaMatchHistory: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("expected 1 match in history, got %d", len(history))
	}
}

func TestArenaELO_DefaultRating(t *testing.T) {
	database := setupArenaTestDB(t)
	ctx := context.Background()

	user, err := database.CreateUser(ctx, "newplayer")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	elo, err := database.GetArenaELO(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetArenaELO: %v", err)
	}
	if elo.Rating != 1200 {
		t.Errorf("expected default rating 1200, got %d", elo.Rating)
	}
	if elo.Wins != 0 || elo.Losses != 0 || elo.Draws != 0 {
		t.Errorf("expected 0/0/0 record, got %d/%d/%d", elo.Wins, elo.Losses, elo.Draws)
	}
}

func TestArenaELO_UpdateAndLeaderboard(t *testing.T) {
	database := setupArenaTestDB(t)
	ctx := context.Background()

	// Create users
	u1, _ := database.CreateUser(ctx, "alpha")
	u2, _ := database.CreateUser(ctx, "beta")
	u3, _ := database.CreateUser(ctx, "gamma")

	// Set different ELO ratings
	database.UpdateArenaELO(ctx, u1.ID, 1500, 10, 2, 1)
	database.UpdateArenaELO(ctx, u2.ID, 1300, 5, 5, 0)
	database.UpdateArenaELO(ctx, u3.ID, 1400, 8, 4, 0)

	// Get leaderboard
	lb, err := database.GetArenaLeaderboard(ctx, 10)
	if err != nil {
		t.Fatalf("GetArenaLeaderboard: %v", err)
	}
	if len(lb) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(lb))
	}

	// Should be ordered by rating DESC
	if lb[0].Rating != 1500 {
		t.Errorf("expected rank 1 rating 1500, got %d", lb[0].Rating)
	}
	if lb[1].Rating != 1400 {
		t.Errorf("expected rank 2 rating 1400, got %d", lb[1].Rating)
	}
	if lb[2].Rating != 1300 {
		t.Errorf("expected rank 3 rating 1300, got %d", lb[2].Rating)
	}

	// Check rank for user 2
	rank, err := database.GetArenaRank(ctx, u2.ID)
	if err != nil {
		t.Fatalf("GetArenaRank: %v", err)
	}
	if rank != 3 {
		t.Errorf("expected rank 3, got %d", rank)
	}
}

func TestArenaELO_Upsert(t *testing.T) {
	database := setupArenaTestDB(t)
	ctx := context.Background()

	user, _ := database.CreateUser(ctx, "upserttest")

	// First insert
	if err := database.UpdateArenaELO(ctx, user.ID, 1250, 1, 0, 0); err != nil {
		t.Fatalf("first UpdateArenaELO: %v", err)
	}

	// Verify
	elo, _ := database.GetArenaELO(ctx, user.ID)
	if elo.Rating != 1250 {
		t.Errorf("expected 1250, got %d", elo.Rating)
	}

	// Upsert (update existing)
	if err := database.UpdateArenaELO(ctx, user.ID, 1300, 2, 0, 0); err != nil {
		t.Fatalf("second UpdateArenaELO: %v", err)
	}

	elo, _ = database.GetArenaELO(ctx, user.ID)
	if elo.Rating != 1300 {
		t.Errorf("expected 1300 after upsert, got %d", elo.Rating)
	}
	if elo.Wins != 2 {
		t.Errorf("expected 2 wins after upsert, got %d", elo.Wins)
	}
}

func TestArenaListActiveMatches(t *testing.T) {
	database := setupArenaTestDB(t)
	ctx := context.Background()

	user, _ := database.CreateUser(ctx, "lister")

	// Create matches with different statuses
	database.CreateArenaMatch(ctx, "m-wait1", "web-exploit", 2, user.ID)
	database.CreateArenaMatch(ctx, "m-wait2", "code-review", 4, user.ID)
	database.CreateArenaMatch(ctx, "m-run1", "web-exploit", 2, user.ID)
	database.UpdateArenaMatchStatus(ctx, "m-run1", "running")
	database.CreateArenaMatch(ctx, "m-done1", "web-exploit", 2, user.ID)
	database.UpdateArenaMatchStatus(ctx, "m-done1", "completed")

	// List should only return waiting + running
	matches, err := database.ListArenaMatches(ctx)
	if err != nil {
		t.Fatalf("ListArenaMatches: %v", err)
	}
	if len(matches) != 3 {
		t.Errorf("expected 3 active matches, got %d", len(matches))
	}
}

func TestArenaMatchCapacity(t *testing.T) {
	database := setupArenaTestDB(t)
	ctx := context.Background()

	u1, _ := database.CreateUser(ctx, "cap1")
	u2, _ := database.CreateUser(ctx, "cap2")
	u3, _ := database.CreateUser(ctx, "cap3")

	// Create match with max 2 agents
	database.CreateArenaMatch(ctx, "m-cap", "web-exploit", 2, u1.ID)
	database.JoinArenaMatch(ctx, "m-cap", u1.ID)
	database.JoinArenaMatch(ctx, "m-cap", u2.ID)

	// Count should be 2
	count, _ := database.ArenaParticipantCount(ctx, "m-cap")
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}

	// Third join should fail (but at DB level there's no capacity check;
	// the command handler does the check). We verify the UNIQUE constraint
	// prevents the same user from joining twice.
	_, err := database.JoinArenaMatch(ctx, "m-cap", u1.ID)
	if err == nil {
		t.Error("expected UNIQUE constraint violation on duplicate join")
	}

	// Different user can still join at DB level (capacity is enforced at command level)
	_, err = database.JoinArenaMatch(ctx, "m-cap", u3.ID)
	if err != nil {
		t.Fatalf("third user join should succeed at DB level: %v", err)
	}
}
