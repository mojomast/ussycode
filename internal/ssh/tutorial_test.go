package ssh

import (
	"context"
	"os"
	"testing"

	"github.com/mojomast/ussycode/internal/db"
)

// setupTutorialTestDB creates a temp database for tutorial testing.
func setupTutorialTestDB(t *testing.T) *db.DB {
	t.Helper()
	f, err := os.CreateTemp("", "ussycode-tutorial-test-*.db")
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
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

func TestValidateTutorialCommand(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		want     bool
	}{
		{
			name:     "exact match",
			input:    "new --name=mybox",
			expected: "new --name=mybox",
			want:     true,
		},
		{
			name:     "case insensitive command",
			input:    "NEW --name=mybox",
			expected: "new --name=mybox",
			want:     true,
		},
		{
			name:     "extra whitespace",
			input:    "  new   --name=mybox  ",
			expected: "new --name=mybox",
			want:     true,
		},
		{
			name:     "simple command ls",
			input:    "ls",
			expected: "ls",
			want:     true,
		},
		{
			name:     "wrong command",
			input:    "rm mybox",
			expected: "new --name=mybox",
			want:     false,
		},
		{
			name:     "missing argument",
			input:    "new",
			expected: "new --name=mybox",
			want:     false,
		},
		{
			name:     "extra argument",
			input:    "new --name=mybox --image=foo",
			expected: "new --name=mybox",
			want:     false,
		},
		{
			name:     "case insensitive args",
			input:    "share SET-PUBLIC mybox",
			expected: "share set-public mybox",
			want:     true,
		},
		{
			name:     "empty input",
			input:    "",
			expected: "ls",
			want:     false,
		},
		{
			name:     "ssh command",
			input:    "ssh mybox",
			expected: "ssh mybox",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateTutorialCommand(tt.input, tt.expected)
			if got != tt.want {
				t.Errorf("validateTutorialCommand(%q, %q) = %v, want %v",
					tt.input, tt.expected, got, tt.want)
			}
		})
	}
}

func TestTutorialLessonsValid(t *testing.T) {
	// Verify all lessons have required fields
	if len(tutorialLessons) != 10 {
		t.Errorf("expected 10 lessons, got %d", len(tutorialLessons))
	}

	for i, lesson := range tutorialLessons {
		if lesson.Number != i+1 {
			t.Errorf("lesson %d has Number=%d, want %d", i, lesson.Number, i+1)
		}
		if lesson.Title == "" {
			t.Errorf("lesson %d has empty Title", lesson.Number)
		}
		if lesson.Explanation == "" {
			t.Errorf("lesson %d has empty Explanation", lesson.Number)
		}
		if !lesson.Simulate && lesson.Command == "" {
			t.Errorf("non-simulated lesson %d has empty Command", lesson.Number)
		}
	}
}

func TestTutorialProgressDB(t *testing.T) {
	database := setupTutorialTestDB(t)
	ctx := context.Background()

	// Create a test user
	user, err := database.CreateUser(ctx, "tutuser")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Initially, no progress
	progress, err := database.GetTutorialProgress(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetTutorialProgress: %v", err)
	}
	if len(progress) != 0 {
		t.Errorf("expected 0 progress entries, got %d", len(progress))
	}

	// Complete lesson 1
	if err := database.CompleteTutorialLesson(ctx, user.ID, 1); err != nil {
		t.Fatalf("CompleteTutorialLesson(1): %v", err)
	}

	// Complete lesson 3 (out of order)
	if err := database.CompleteTutorialLesson(ctx, user.ID, 3); err != nil {
		t.Fatalf("CompleteTutorialLesson(3): %v", err)
	}

	// Check progress
	progress, err = database.GetTutorialProgress(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetTutorialProgress: %v", err)
	}
	if len(progress) != 2 {
		t.Fatalf("expected 2 progress entries, got %d", len(progress))
	}
	if progress[0].LessonNumber != 1 {
		t.Errorf("expected first entry lesson 1, got %d", progress[0].LessonNumber)
	}
	if progress[1].LessonNumber != 3 {
		t.Errorf("expected second entry lesson 3, got %d", progress[1].LessonNumber)
	}

	// Completing the same lesson again is idempotent
	if err := database.CompleteTutorialLesson(ctx, user.ID, 1); err != nil {
		t.Fatalf("CompleteTutorialLesson(1) again: %v", err)
	}
	progress, err = database.GetTutorialProgress(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetTutorialProgress after dup: %v", err)
	}
	if len(progress) != 2 {
		t.Errorf("expected 2 progress entries after dup insert, got %d", len(progress))
	}

	// Reset progress
	if err := database.ResetTutorialProgress(ctx, user.ID); err != nil {
		t.Fatalf("ResetTutorialProgress: %v", err)
	}
	progress, err = database.GetTutorialProgress(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetTutorialProgress after reset: %v", err)
	}
	if len(progress) != 0 {
		t.Errorf("expected 0 progress entries after reset, got %d", len(progress))
	}
}

func TestTutorialProgressIsolation(t *testing.T) {
	// Verify that tutorial progress is isolated between users
	database := setupTutorialTestDB(t)
	ctx := context.Background()

	user1, err := database.CreateUser(ctx, "tutor-one")
	if err != nil {
		t.Fatalf("CreateUser(1): %v", err)
	}
	user2, err := database.CreateUser(ctx, "tutor-two")
	if err != nil {
		t.Fatalf("CreateUser(2): %v", err)
	}

	// User 1 completes lessons 1, 2, 3
	for _, n := range []int{1, 2, 3} {
		if err := database.CompleteTutorialLesson(ctx, user1.ID, n); err != nil {
			t.Fatalf("CompleteTutorialLesson(user1, %d): %v", n, err)
		}
	}

	// User 2 completes lesson 5
	if err := database.CompleteTutorialLesson(ctx, user2.ID, 5); err != nil {
		t.Fatalf("CompleteTutorialLesson(user2, 5): %v", err)
	}

	// Verify isolation
	p1, err := database.GetTutorialProgress(ctx, user1.ID)
	if err != nil {
		t.Fatalf("GetTutorialProgress(user1): %v", err)
	}
	if len(p1) != 3 {
		t.Errorf("user1: expected 3 entries, got %d", len(p1))
	}

	p2, err := database.GetTutorialProgress(ctx, user2.ID)
	if err != nil {
		t.Fatalf("GetTutorialProgress(user2): %v", err)
	}
	if len(p2) != 1 {
		t.Errorf("user2: expected 1 entry, got %d", len(p2))
	}
	if len(p2) > 0 && p2[0].LessonNumber != 5 {
		t.Errorf("user2: expected lesson 5, got %d", p2[0].LessonNumber)
	}

	// Reset user 1 doesn't affect user 2
	if err := database.ResetTutorialProgress(ctx, user1.ID); err != nil {
		t.Fatalf("ResetTutorialProgress(user1): %v", err)
	}
	p2, err = database.GetTutorialProgress(ctx, user2.ID)
	if err != nil {
		t.Fatalf("GetTutorialProgress(user2) after reset: %v", err)
	}
	if len(p2) != 1 {
		t.Errorf("user2 after user1 reset: expected 1 entry, got %d", len(p2))
	}
}

func TestHasArgFlag(t *testing.T) {
	tests := []struct {
		args []string
		flag string
		want bool
	}{
		{[]string{"--reset"}, "--reset", true},
		{[]string{"--lesson=5"}, "--reset", false},
		{[]string{"--reset", "--lesson=5"}, "--reset", true},
		{[]string{}, "--reset", false},
		{[]string{"reset"}, "--reset", false},
	}

	for _, tt := range tests {
		got := hasArgFlag(tt.args, tt.flag)
		if got != tt.want {
			t.Errorf("hasArgFlag(%v, %q) = %v, want %v", tt.args, tt.flag, got, tt.want)
		}
	}
}
