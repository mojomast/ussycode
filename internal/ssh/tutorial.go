package ssh

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func init() {
	RegisterCommand("tutorial", cmdTutorial)
}

// Lesson represents a single tutorial lesson with explanation and validation.
type Lesson struct {
	Number      int
	Title       string
	Explanation string
	Command     string // the expected command the user should type
	Hint        string // shown if the user gets stuck
	Simulate    bool   // if true, lesson is informational-only (no command validation)
}

// tutorialLessons defines all 10 progressive tutorial lessons.
var tutorialLessons = []Lesson{
	{
		Number: 1,
		Title:  "Create Your First VM",
		Explanation: `  Your dev environment lives in a lightweight VM (virtual machine).
  Let's create one now! The 'new' command spins up a fresh Ubuntu
  environment in seconds.

  Try it:`,
		Command: "new --name=mybox",
		Hint:    "Type: new --name=mybox",
	},
	{
		Number: 2,
		Title:  "List Your VMs",
		Explanation: `  Now let's see your environments. The 'ls' command shows all your
  VMs with their status, image, and when they were created.

  Try it:`,
		Command: "ls",
		Hint:    "Type: ls",
	},
	{
		Number: 3,
		Title:  "Connect to Your VM",
		Explanation: `  To get a shell inside your VM, use the 'ssh' command followed
  by the VM name. This drops you into a full Linux environment.

  Try it:`,
		Command: "ssh mybox",
		Hint:    "Type: ssh mybox",
	},
	{
		Number:   4,
		Title:    "Linux Basics",
		Simulate: true,
		Explanation: `  Inside your VM, you have a full Ubuntu environment. Here are
  the essential commands you'll use every day:

    ls          list files and directories
    cd <dir>    change directory
    cat <file>  display file contents
    nano <file> edit a file (simple editor)
    mkdir <dir> create a directory
    rm <file>   remove a file

  When you're done exploring, type 'exit' to return to the
  ussycode shell. Press ENTER to continue.`,
	},
	{
		Number:   5,
		Title:    "Run a Web Server",
		Simulate: true,
		Explanation: `  Your VM can run web servers! Inside the VM, try:

    python3 -m http.server 8080

  This starts a simple HTTP server on port 8080. Your VM's
  web traffic is automatically available via HTTPS at:

    https://mybox.<your-domain>/

  Press ENTER to continue.`,
	},
	{
		Number:   6,
		Title:    "Access via HTTPS",
		Simulate: true,
		Explanation: `  Every VM gets a unique HTTPS URL based on its name:

    https://<vm-name>.ussy.host/

  Port 8080 inside the VM is proxied automatically. If your
  VM is named "mybox", your URL is:

    https://mybox.ussy.host/

  By default, the URL is private (only you can access it).
  We'll cover sharing in lesson 9.

  Press ENTER to continue.`,
	},
	{
		Number:   7,
		Title:    "Install Packages",
		Simulate: true,
		Explanation: `  Inside your VM, you can install any package with apt:

    sudo apt update
    sudo apt install -y nodejs npm python3-pip git curl

  Your VM has full root access. Install whatever you need for
  your project -- compilers, databases, tools, frameworks.

  Press ENTER to continue.`,
	},
	{
		Number:   8,
		Title:    "Use Git",
		Simulate: true,
		Explanation: `  Git is pre-installed in your VM. Common workflows:

    git clone https://github.com/you/repo.git
    cd repo
    # make changes...
    git add .
    git commit -m "my changes"
    git push

  Tip: Add your SSH key to GitHub/GitLab so you can push
  without entering credentials each time.

  Press ENTER to continue.`,
	},
	{
		Number: 9,
		Title:  "Share Your Work",
		Explanation: `  Make your VM's web endpoint publicly accessible so others
  can see your work. The 'share set-public' command opens the
  HTTPS URL to anyone.

  Try it:`,
		Command: "share set-public mybox",
		Hint:    "Type: share set-public mybox",
	},
	{
		Number: 10,
		Title:  "Clean Up",
		Explanation: `  When you're done with a VM, remove it with 'rm'. This frees
  up resources. Don't worry -- you can always create a new one!

  To complete the tutorial, you would normally type:
    rm mybox

  But we'll skip the confirmation for now and just mark this
  lesson complete.`,
		Simulate: true,
	},
}

// cmdTutorial is the handler for the "tutorial" command.
func cmdTutorial(s *Shell, args []string) error {
	ctx := context.Background()

	// Handle --reset flag
	if hasArgFlag(args, "--reset") {
		if err := s.gw.DB.ResetTutorialProgress(ctx, s.user.ID); err != nil {
			return fmt.Errorf("reset tutorial: %w", err)
		}
		s.writeln("  tutorial progress reset. type 'tutorial' to start over.")
		return nil
	}

	// Handle --lesson=N flag
	targetLesson := 0
	for _, a := range args {
		if strings.HasPrefix(a, "--lesson=") {
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--lesson="))
			if err != nil || n < 1 || n > len(tutorialLessons) {
				return fmt.Errorf("invalid lesson number (must be 1-%d)", len(tutorialLessons))
			}
			targetLesson = n
		}
	}

	// Load progress
	progress, err := s.gw.DB.GetTutorialProgress(ctx, s.user.ID)
	if err != nil {
		return fmt.Errorf("load tutorial progress: %w", err)
	}

	completedSet := make(map[int]bool)
	for _, p := range progress {
		completedSet[p.LessonNumber] = true
	}

	// Determine which lesson to show
	lessonIdx := 0
	if targetLesson > 0 {
		lessonIdx = targetLesson - 1
	} else {
		// Resume from first incomplete lesson
		lessonIdx = len(tutorialLessons) // all complete by default
		for i, l := range tutorialLessons {
			if !completedSet[l.Number] {
				lessonIdx = i
				break
			}
		}
	}

	// All lessons complete?
	if lessonIdx >= len(tutorialLessons) {
		s.writeln("")
		s.writeln("  \033[32m=== TUTORIAL COMPLETE ===\033[0m")
		s.writeln("")
		s.writef("  you've finished all %d lessons!\n", len(tutorialLessons))
		s.writeln("  you're ready to build. here are some next steps:")
		s.writeln("")
		s.writeln("    doc getting-started    read the full quickstart guide")
		s.writeln("    projects               browse project templates")
		s.writeln("    new --name=myproject   create a new environment")
		s.writeln("")
		s.writeln("  type 'tutorial --reset' to replay the tutorial.")
		s.writeln("")
		return nil
	}

	lesson := tutorialLessons[lessonIdx]
	return runLesson(s, lesson, completedSet)
}

// runLesson presents a single tutorial lesson and handles validation.
func runLesson(s *Shell, lesson Lesson, completedSet map[int]bool) error {
	ctx := context.Background()

	// Print lesson header
	s.writeln("")
	s.writef("  \033[1m\033[35m── Lesson %d/%d: %s ──\033[0m\n", lesson.Number, len(tutorialLessons), lesson.Title)
	s.writeln("")

	// Show completion status
	if completedSet[lesson.Number] {
		s.writeln("  \033[32m✓ already completed\033[0m")
		s.writeln("")
	}

	// Print explanation
	s.writeln(lesson.Explanation)
	s.writeln("")

	if lesson.Simulate {
		// Informational lesson -- wait for ENTER then mark complete
		s.writef("  \033[33m[press ENTER to continue]\033[0m ")
		_, _ = s.term.ReadLine()

		if err := s.gw.DB.CompleteTutorialLesson(ctx, s.user.ID, lesson.Number); err != nil {
			return fmt.Errorf("save progress: %w", err)
		}

		s.writef("  \033[32m✓ Lesson %d complete!\033[0m\n", lesson.Number)
		s.writeln("")

		// Auto-advance to next lesson
		return advanceToNext(s, lesson.Number)
	}

	// Interactive lesson -- user needs to type the command
	s.writef("  \033[36m→ %s\033[0m\n", lesson.Command)
	s.writeln("")
	s.writef("  type the command below (or 'skip' to skip, 'quit' to exit tutorial):\n")
	s.writeln("")

	for attempts := 0; attempts < 10; attempts++ {
		s.writef("  \033[35mtutorial>\033[0m ")
		line, err := s.term.ReadLine()
		if err != nil {
			return nil // user disconnected
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if line == "quit" || line == "exit" {
			s.writeln("  exiting tutorial. type 'tutorial' to resume later.")
			return nil
		}

		if line == "skip" {
			if err := s.gw.DB.CompleteTutorialLesson(ctx, s.user.ID, lesson.Number); err != nil {
				return fmt.Errorf("save progress: %w", err)
			}
			s.writef("  \033[33m⏭ Lesson %d skipped.\033[0m\n", lesson.Number)
			return advanceToNext(s, lesson.Number)
		}

		if line == "hint" {
			if lesson.Hint != "" {
				s.writef("  \033[33m💡 %s\033[0m\n", lesson.Hint)
			}
			continue
		}

		// Validate the command: check if it matches the expected command
		if validateTutorialCommand(line, lesson.Command) {
			// Execute the actual command through the shell's dispatch
			if cmdErr := s.dispatchCommand(line); cmdErr != nil {
				s.writef("  \033[33mnote: %v (that's ok for the tutorial)\033[0m\n", cmdErr)
			}

			// Mark lesson complete
			if err := s.gw.DB.CompleteTutorialLesson(ctx, s.user.ID, lesson.Number); err != nil {
				return fmt.Errorf("save progress: %w", err)
			}

			s.writeln("")
			s.writef("  \033[32m✓ Lesson %d complete!\033[0m\n", lesson.Number)
			s.writeln("")

			return advanceToNext(s, lesson.Number)
		}

		s.writef("  \033[31mnot quite.\033[0m try: %s (or type 'hint' for help)\n", lesson.Command)
	}

	s.writeln("  too many attempts. type 'tutorial' to try again later.")
	return nil
}

// advanceToNext asks if the user wants to continue to the next lesson.
func advanceToNext(s *Shell, currentLesson int) error {
	if currentLesson >= len(tutorialLessons) {
		// Was the last lesson
		s.writeln("  \033[32m=== TUTORIAL COMPLETE! ===\033[0m")
		s.writeln("  you're ready to build. type 'help' for all commands.")
		s.writeln("")
		return nil
	}

	s.writef("  continue to lesson %d? [Y/n] ", currentLesson+1)
	line, err := s.term.ReadLine()
	if err != nil {
		return nil
	}

	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" || line == "y" || line == "yes" {
		// Get updated progress
		ctx := context.Background()
		progress, _ := s.gw.DB.GetTutorialProgress(ctx, s.user.ID)
		completedSet := make(map[int]bool)
		for _, p := range progress {
			completedSet[p.LessonNumber] = true
		}
		return runLesson(s, tutorialLessons[currentLesson], completedSet)
	}

	s.writeln("  type 'tutorial' to resume later.")
	return nil
}

// validateTutorialCommand checks if a user's input matches the expected command.
// It's flexible: allows extra whitespace and is case-insensitive for the command name.
func validateTutorialCommand(input, expected string) bool {
	// Normalize both strings
	inputParts := strings.Fields(strings.TrimSpace(input))
	expectedParts := strings.Fields(strings.TrimSpace(expected))

	if len(inputParts) != len(expectedParts) {
		return false
	}

	for i := range inputParts {
		// Command name (first part) is case-insensitive
		if i == 0 {
			if !strings.EqualFold(inputParts[i], expectedParts[i]) {
				return false
			}
			continue
		}
		// For flags/args, exact match (but allow case-insensitive for names)
		if !strings.EqualFold(inputParts[i], expectedParts[i]) {
			return false
		}
	}

	return true
}

// hasArgFlag checks if a specific flag is present in the argument list.
func hasArgFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
